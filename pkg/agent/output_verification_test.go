// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// scriptedLLM answers conversation calls from a fixed script and judge calls
// (recognized by the strict-verdict instruction in the prompt) from a verdict
// queue. It records every call's messages for assertions.
type scriptedLLM struct {
	mu        sync.Mutex
	responses []string // conversation responses, in order
	verdicts  []string // judge verdicts, in order ("" panics — test bug)
	calls     [][]llmtypes.Message
	judgeIdx  int
	convIdx   int
}

const verdictMarker = "Respond with exactly `PASS`"

func (s *scriptedLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	copied := make([]llmtypes.Message, len(messages))
	copy(copied, messages)
	s.calls = append(s.calls, copied)

	last := messages[len(messages)-1]
	if strings.Contains(last.Content, verdictMarker) {
		require := s.verdicts[s.judgeIdx] // panics on script exhaustion = test bug
		s.judgeIdx++
		return &llmtypes.LLMResponse{Content: require, Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 2}}, nil
	}

	if s.convIdx >= len(s.responses) {
		return &llmtypes.LLMResponse{Content: "unscripted fallback", Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5}}, nil
	}
	resp := s.responses[s.convIdx]
	s.convIdx++
	return &llmtypes.LLMResponse{Content: resp, Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func (s *scriptedLLM) Name() string  { return "scripted" }
func (s *scriptedLLM) Model() string { return "scripted-v1" }

// lastConversationCall returns the messages of the most recent
// non-judge LLM call.
func (s *scriptedLLM) lastConversationCall() []llmtypes.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.calls) - 1; i >= 0; i-- {
		last := s.calls[i][len(s.calls[i])-1]
		if !strings.Contains(last.Content, verdictMarker) {
			return s.calls[i]
		}
	}
	return nil
}

func newVerificationAgent(t *testing.T, llm llmtypes.LLMProvider, ov *OutputVerificationConfig, mutate ...func(*Config)) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.OutputVerification = ov
	for _, m := range mutate {
		m(cfg)
	}
	return NewAgent(&mockBackend{}, llm, WithConfig(cfg))
}

const testSchema = `{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`

func TestOutputVerification_SchemaRetryThenPass(t *testing.T) {
	t.Parallel()

	llm := &scriptedLLM{responses: []string{
		"this is not json at all",
		`{"answer": "42"}`,
	}}
	ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
		Schema:             testSchema,
		MaxRetries:         2,
		IncludeValidValues: true,
	})

	resp, err := ag.Chat(context.Background(), "s1", "answer as json")
	require.NoError(t, err)

	assert.Equal(t, `{"answer": "42"}`, resp.Content)
	assert.Equal(t, "passed", resp.Metadata["output_verification"])
	assert.Equal(t, 1, resp.Metadata["output_verification_attempts"])

	// The retry turn must have seen a feedback message explaining the failure
	// and showing the schema.
	retryMsgs := llm.lastConversationCall()
	require.NotNil(t, retryMsgs)
	feedback := retryMsgs[len(retryMsgs)-1].Content
	assert.Contains(t, feedback, "OUTPUT VALIDATION FAILED")
	assert.Contains(t, feedback, "no valid JSON found")
	assert.Contains(t, feedback, testSchema)
}

func TestOutputVerification_Exhaustion(t *testing.T) {
	t.Parallel()

	llm := &scriptedLLM{responses: []string{"bad one", "bad two", "bad three"}}
	ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
		Schema:     testSchema,
		MaxRetries: 2,
	})

	resp, err := ag.Chat(context.Background(), "s1", "answer as json")
	require.NoError(t, err, "verification exhaustion must degrade gracefully, never error")

	assert.Equal(t, "bad three", resp.Content, "last output is returned unchanged")
	assert.Equal(t, "failed", resp.Metadata["output_verification"])
	assert.Equal(t, 2, resp.Metadata["output_verification_attempts"])
	assert.Contains(t, resp.Metadata["output_verification_error"], "no valid JSON found")
}

func TestOutputVerification_Criteria(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		verdicts     []string
		responses    []string
		wantOutcome  string
		wantAttempts int
		wantContent  string
	}{
		{
			name:         "criteria pass first try",
			verdicts:     []string{"PASS"},
			responses:    []string{"the answer cites table users"},
			wantOutcome:  "passed",
			wantAttempts: 0,
			wantContent:  "the answer cites table users",
		},
		{
			name:         "criteria fail then pass",
			verdicts:     []string{"FAIL: no table cited", "PASS"},
			responses:    []string{"no citation here", "cites table users"},
			wantOutcome:  "passed",
			wantAttempts: 1,
			wantContent:  "cites table users",
		},
		{
			name:         "malformed verdict is inconclusive, burns no retry",
			verdicts:     []string{"Looks good to me!"},
			responses:    []string{"whatever answer"},
			wantOutcome:  "judge_inconclusive",
			wantAttempts: 0,
			wantContent:  "whatever answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			llm := &scriptedLLM{responses: tt.responses, verdicts: tt.verdicts}
			ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
				AcceptanceCriteria: "The answer must cite at least one table name.",
				MaxRetries:         3,
				IncludeValidValues: true,
			})

			resp, err := ag.Chat(context.Background(), "s1", "hello")
			require.NoError(t, err)
			assert.Equal(t, tt.wantOutcome, resp.Metadata["output_verification"])
			assert.Equal(t, tt.wantAttempts, resp.Metadata["output_verification_attempts"])
			assert.Equal(t, tt.wantContent, resp.Content)
		})
	}
}

func TestOutputVerification_CriteriaFeedbackCarriesReason(t *testing.T) {
	t.Parallel()

	llm := &scriptedLLM{
		responses: []string{"first answer", "second answer"},
		verdicts:  []string{"FAIL: missing the users table", "PASS"},
	}
	ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
		AcceptanceCriteria: "Must mention the users table.",
		MaxRetries:         1,
		IncludeValidValues: true,
	})

	resp, err := ag.Chat(context.Background(), "s1", "hello")
	require.NoError(t, err)
	assert.Equal(t, "passed", resp.Metadata["output_verification"])

	retryMsgs := llm.lastConversationCall()
	require.NotNil(t, retryMsgs)
	feedback := retryMsgs[len(retryMsgs)-1].Content
	assert.Contains(t, feedback, "missing the users table")
	assert.Contains(t, feedback, "ACCEPTANCE CRITERIA")
	assert.Contains(t, feedback, "Must mention the users table.")
}

func TestOutputVerification_Disabled(t *testing.T) {
	t.Parallel()

	llm := &scriptedLLM{responses: []string{"plain answer"}}
	ag := newVerificationAgent(t, llm, nil)

	resp, err := ag.Chat(context.Background(), "s1", "hello")
	require.NoError(t, err)
	assert.Equal(t, "plain answer", resp.Content)
	assert.NotContains(t, resp.Metadata, "output_verification")
}

func TestOutputVerification_BudgetExhaustionSkipsSynthesizedOutput(t *testing.T) {
	t.Parallel()

	// Every conversation response is schema-invalid; MaxTurns bounds the
	// verification retries, and the forced-synthesis answer is not verified.
	llm := &scriptedLLM{responses: []string{"bad", "bad", "bad", "bad", "bad", "bad"}}
	ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
		Schema:     testSchema,
		MaxRetries: 10,
	}, func(cfg *Config) {
		cfg.MaxTurns = 2
	})

	resp, err := ag.Chat(context.Background(), "s1", "answer as json")
	require.NoError(t, err)
	assert.Equal(t, true, resp.Metadata["synthesized"])
	assert.Equal(t, "skipped_budget_exhausted", resp.Metadata["output_verification"])
}

func TestOutputVerification_EmptyNudgeRunsBeforeVerification(t *testing.T) {
	t.Parallel()

	// First response empty (triggers the nudge), then schema-invalid
	// (triggers a verification retry), then valid.
	llm := &scriptedLLM{responses: []string{"", "not json", `{"answer":"ok"}`}}
	ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
		Schema:     testSchema,
		MaxRetries: 2,
	})

	resp, err := ag.Chat(context.Background(), "s1", "answer as json")
	require.NoError(t, err)
	assert.Equal(t, `{"answer":"ok"}`, resp.Content)
	assert.Equal(t, true, resp.Metadata["empty_retried"])
	assert.Equal(t, "passed", resp.Metadata["output_verification"])
	assert.Equal(t, 1, resp.Metadata["output_verification_attempts"])
}

func TestOutputVerification_CooldownHonorsCancellation(t *testing.T) {
	t.Parallel()

	llm := &scriptedLLM{responses: []string{"bad"}}
	ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
		Schema:     testSchema,
		MaxRetries: 3,
		CooldownMs: 30_000, // far longer than the test timeout
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var resp *Response
	var err error
	go func() {
		resp, err = ag.Chat(ctx, "s1", "answer as json")
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Chat did not return after context cancellation during verification cooldown")
	}
	require.NoError(t, err, "cancellation during cooldown degrades gracefully")
	assert.Equal(t, "failed", resp.Metadata["output_verification"])
}

func TestOutputVerification_CustomFeedbackTemplate(t *testing.T) {
	t.Parallel()

	llm := &scriptedLLM{responses: []string{"bad", `{"answer":"ok"}`}}
	ag := newVerificationAgent(t, llm, &OutputVerificationConfig{
		Schema:           testSchema,
		MaxRetries:       1,
		FeedbackTemplate: "RETRY {{attempt}}/{{max_retries}}: {{error}}",
	})

	resp, err := ag.Chat(context.Background(), "s1", "answer as json")
	require.NoError(t, err)
	assert.Equal(t, "passed", resp.Metadata["output_verification"])

	retryMsgs := llm.lastConversationCall()
	require.NotNil(t, retryMsgs)
	feedback := retryMsgs[len(retryMsgs)-1].Content
	assert.True(t, strings.HasPrefix(feedback, "RETRY 1/1: "), "custom template used, got: %s", feedback)
}

func TestStripProgressCallback(t *testing.T) {
	t.Parallel()

	base := &agentContext{
		Context:          context.Background(),
		session:          &Session{ID: "s"},
		tracer:           nil,
		progressCallback: func(ProgressEvent) {},
	}
	stripped := stripProgressCallback(base)
	assert.Nil(t, stripped.ProgressCallback(), "judge calls must never stream to the client")
	assert.Equal(t, "s", stripped.Session().ID, "session identity preserved")
}
