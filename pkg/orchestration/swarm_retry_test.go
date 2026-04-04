// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

func TestIsValidVote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		vote      *loomv1.SwarmVote
		rawOutput string
		want      bool
	}{
		{
			name:      "valid vote with VOTE: prefix",
			vote:      &loomv1.SwarmVote{Choice: "Option A", Confidence: 0.8},
			rawOutput: "VOTE: Option A\nCONFIDENCE: 0.8\nREASONING: because...",
			want:      true,
		},
		{
			name:      "abstain without VOTE prefix - parsing failed",
			vote:      &loomv1.SwarmVote{Choice: "abstain", Confidence: 0.5},
			rawOutput: "I think we should go with option A because it's better",
			want:      false,
		},
		{
			name:      "non-abstain choice without VOTE prefix",
			vote:      &loomv1.SwarmVote{Choice: "Option A", Confidence: 0.5},
			rawOutput: "Option A is clearly the best",
			want:      true,
		},
		{
			name:      "has VOTE prefix even though choice is abstain",
			vote:      &loomv1.SwarmVote{Choice: "abstain", Confidence: 0.5},
			rawOutput: "VOTE: abstain\nCONFIDENCE: 0.5",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isValidVote(tt.vote, tt.rawOutput))
		})
	}
}

func TestBuildVoteRetryPrompt(t *testing.T) {
	t.Parallel()

	executor := &SwarmExecutor{
		pattern: &loomv1.SwarmPattern{
			Question: "Which database should we use?",
			RetryPolicy: &loomv1.OutputRetryPolicy{
				MaxRetries: 2,
			},
		},
	}

	prompt := executor.buildVoteRetryPrompt(
		"I think PostgreSQL is the best option because of its strong ecosystem",
		1, 2,
	)

	assert.Contains(t, prompt, "could not be parsed as a valid vote")
	assert.Contains(t, prompt, "PostgreSQL is the best")
	assert.Contains(t, prompt, "VOTE:")
	assert.Contains(t, prompt, "CONFIDENCE:")
	assert.Contains(t, prompt, "REASONING:")
	assert.Contains(t, prompt, "Which database should we use?")
	assert.Contains(t, prompt, "retry 1 of 2")
}

func TestBuildJudgeRetryPrompt(t *testing.T) {
	t.Parallel()

	executor := &SwarmExecutor{
		pattern: &loomv1.SwarmPattern{
			RetryPolicy: &loomv1.OutputRetryPolicy{MaxRetries: 2},
		},
	}

	distribution := map[string]int32{
		"PostgreSQL": 2,
		"MySQL":      2,
	}

	prompt := executor.buildJudgeRetryPrompt(distribution, "I prefer PostgreSQL", 1, 2)

	assert.Contains(t, prompt, `"I prefer PostgreSQL"`)
	assert.Contains(t, prompt, "does not match any of the available options")
	assert.Contains(t, prompt, "retry 1 of 2")
	// Check that both options are listed
	assert.Contains(t, prompt, "PostgreSQL")
	assert.Contains(t, prompt, "MySQL")
}

func TestSwarmRetry_VoteParsingFailureRetried(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// First call returns prose (no VOTE: format), second returns correct format
	agentLLM := newMockLLMProvider(
		"I believe PostgreSQL is the best choice due to its extensive feature set",
		"VOTE: PostgreSQL\nCONFIDENCE: 0.9\nREASONING: Strong ecosystem and reliability",
	)
	ag := createMockAgent(t, "voter", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("voter", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Swarm{
			Swarm: &loomv1.SwarmPattern{
				Question:            "Which database?",
				AgentIds:            []string{"voter"},
				Strategy:            loomv1.VotingStrategy_MAJORITY,
				ConfidenceThreshold: 0.5,
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 2,
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "swarm", result.PatternType)
	assert.Equal(t, "PostgreSQL", result.MergedOutput)
}

func TestSwarmRetry_NoRetryPolicy(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Returns prose without VOTE: format — no retry policy, so defaults to abstain
	agentLLM := newMockLLMProvider(
		"I think PostgreSQL is best",
	)
	ag := createMockAgent(t, "voter", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("voter", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Swarm{
			Swarm: &loomv1.SwarmPattern{
				Question:            "Which database?",
				AgentIds:            []string{"voter"},
				Strategy:            loomv1.VotingStrategy_MAJORITY,
				ConfidenceThreshold: 0.5,
				// No RetryPolicy
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	// Should default to "abstain" (old behavior — no retry)
	assert.Equal(t, "swarm", result.PatternType)
	assert.Equal(t, "abstain", result.MergedOutput)
}

func TestSwarmRetry_VoteRetryMaxExhausted(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// All responses are prose — never follow the VOTE: format
	agentLLM := newMockLLMProvider(
		"I think PostgreSQL is great",
		"PostgreSQL is definitely the way to go",
		"My recommendation is PostgreSQL",
	)
	ag := createMockAgent(t, "voter", agentLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("voter", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Swarm{
			Swarm: &loomv1.SwarmPattern{
				Question:            "Which database?",
				AgentIds:            []string{"voter"},
				Strategy:            loomv1.VotingStrategy_MAJORITY,
				ConfidenceThreshold: 0.5,
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 2,
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	// All retries exhausted — falls back to "abstain" (graceful degradation)
	assert.Equal(t, "abstain", result.MergedOutput)

	// 3 calls: 1 initial + 2 retries
	agentLLM.mu.Lock()
	assert.Equal(t, 3, agentLLM.callCount)
	agentLLM.mu.Unlock()
}

func TestSwarmRetry_JudgeInvalidChoiceRetried(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Two voters with conflicting votes (creates a tie)
	voter1LLM := newMockLLMProvider("VOTE: PostgreSQL\nCONFIDENCE: 0.9\nREASONING: Best for OLTP")
	voter1 := createMockAgent(t, "voter1", voter1LLM)

	voter2LLM := newMockLLMProvider("VOTE: MySQL\nCONFIDENCE: 0.9\nREASONING: Simpler to manage")
	voter2 := createMockAgent(t, "voter2", voter2LLM)

	// Judge first returns invalid option, then valid
	judgeLLM := newMockLLMProvider("I think SQLite would be better actually", "PostgreSQL")
	judge := createMockAgent(t, "judge", judgeLLM)

	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer})
	orch.RegisterAgent("voter1", voter1)
	orch.RegisterAgent("voter2", voter2)
	orch.RegisterAgent("judge", judge)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Swarm{
			Swarm: &loomv1.SwarmPattern{
				Question:            "Which database?",
				AgentIds:            []string{"voter1", "voter2"},
				Strategy:            loomv1.VotingStrategy_MAJORITY,
				ConfidenceThreshold: 0.5,
				JudgeAgentId:        "judge",
				RetryPolicy: &loomv1.OutputRetryPolicy{
					MaxRetries: 2,
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "PostgreSQL", result.MergedOutput)
}

func TestSwarmRetry_IncludeValidValuesFalse(t *testing.T) {
	t.Parallel()

	executor := &SwarmExecutor{
		pattern: &loomv1.SwarmPattern{
			Question: "Which database?",
			RetryPolicy: &loomv1.OutputRetryPolicy{
				MaxRetries:         2,
				IncludeValidValues: false,
			},
		},
	}

	prompt := executor.buildVoteRetryPrompt("bad output", 1, 2)

	// With IncludeValidValues=false, the format template should be omitted
	assert.NotContains(t, prompt, "REQUIRED FORMAT")
	assert.NotContains(t, prompt, "EXAMPLE:")
	// But the question and failure explanation should still be present
	assert.Contains(t, prompt, "Which database?")
	assert.Contains(t, prompt, "could not be parsed as a valid vote")
}
