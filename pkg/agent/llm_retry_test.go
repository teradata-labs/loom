// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// flakyLLM fails its first failures calls with err, then succeeds.
type flakyLLM struct {
	calls    int
	failures int
	err      error
}

func (f *flakyLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, f.err
	}
	return &LLMResponse{Content: "ok", Usage: llmtypes.Usage{TotalTokens: 7}}, nil
}
func (f *flakyLLM) Name() string  { return "flaky" }
func (f *flakyLLM) Model() string { return "flaky-model" }

func retryTestContext() *agentContext {
	return &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}
}

// A transient provider failure must not end the turn: an agent whose Config
// never mentions retry — every agent loom's registry builds, and every one an
// embedder builds setting only its own fields — retries with the defaults. A
// zero Config once meant retry off, and the first internalServerException
// killed the conversation.
func TestChatWithRetry_TransientProviderErrorIsRetried(t *testing.T) {
	provider := &flakyLLM{
		failures: 2,
		err:      fmt.Errorf("stream error: received exception internalServerException: try again"),
	}
	a := &Agent{id: "retry-test", llm: provider, config: &Config{}}

	resp, err := a.chatWithRetry(retryTestContext(), []Message{{Role: "user", Content: "q"}}, nil)

	require.NoError(t, err, "two transient failures then success must surface as success")
	require.NotNil(t, resp)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 3, provider.calls, "two failed attempts plus the one that succeeded")
}

// Retries are finite: a provider that never recovers surfaces the last error
// after the whole policy is spent, and reports how many attempts it took.
func TestChatWithRetry_ExhaustsAndReportsAttemptCount(t *testing.T) {
	provider := &flakyLLM{failures: 1000, err: errors.New("permanently broken")}
	a := &Agent{id: "retry-test", llm: provider, config: &Config{}}

	_, err := a.chatWithRetry(retryTestContext(), []Message{{Role: "user", Content: "q"}}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "permanently broken", "the provider's own error is preserved")
	assert.Equal(t, DefaultRetryConfig().MaxRetries+1, provider.calls, "one initial attempt plus the retry budget")
}

// A refusal positively identified as context-too-long is answered by relief,
// never by retry: re-sending the same oversized prompt cannot succeed, so it
// must surface on the first attempt with the sentinel intact.
func TestChatWithRetry_ContextTooLongIsNeverRetried(t *testing.T) {
	provider := &flakyLLM{
		failures: 1000,
		err:      fmt.Errorf("stream error: prompt is too long: %w", llm.ErrContextTooLong),
	}
	a := &Agent{id: "retry-test", llm: provider, config: &Config{}}

	_, err := a.chatWithRetry(retryTestContext(), []Message{{Role: "user", Content: "q"}}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrContextTooLong, "the sentinel must reach the relief path")
	assert.Equal(t, 1, provider.calls, "no retry — relief is the only response to a context refusal")
}

// Retry stays a setting: a config that turns it off gets one attempt, and one
// that names its own budget gets that many.
func TestChatWithRetry_TheConfigsRetryIsHonoured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry RetryConfig
		calls int
	}{
		{"turned off", RetryConfig{MaxRetries: -1}, 1},
		{"off as main wrote it", RetryConfig{Enabled: false, MaxRetries: 3}, 1},
		{"its own budget", RetryConfig{Enabled: true, MaxRetries: 1, InitialDelay: 1}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &flakyLLM{failures: 1000, err: errors.New("down")}
			a := &Agent{id: "retry-test", llm: provider, config: &Config{Retry: tc.retry}}
			_, err := a.chatWithRetry(retryTestContext(), []Message{{Role: "user", Content: "q"}}, nil)
			require.Error(t, err)
			assert.Equal(t, tc.calls, provider.calls)
		})
	}
}

// A request the provider refused as malformed or unauthorised is refused again
// on every re-send, so it is not re-sent.
func TestChatWithRetry_ARefusedRequestIsNotRetried(t *testing.T) {
	for _, refusal := range []string{
		"API error (status 401): invalid x-api-key",
		"API error (status 400): messages: field required",
		"API error (status 403): forbidden",
		"operation error Bedrock Runtime: ConverseStream, ValidationException: malformed",
		"operation error Bedrock Runtime: Converse, AccessDeniedException: not authorized",
	} {
		provider := &flakyLLM{failures: 1000, err: errors.New(refusal)}
		a := &Agent{id: "retry-test", llm: provider, config: &Config{}}
		_, err := a.chatWithRetry(retryTestContext(), []Message{{Role: "user", Content: "q"}}, nil)
		require.Error(t, err)
		assert.Equal(t, 1, provider.calls, "%q was re-sent", refusal)
	}
}

// flakyStream streams said, then fails, for its first failures calls.
type flakyStream struct {
	flakyLLM
	said string
}

func (f *flakyStream) ChatStream(_ context.Context, _ []Message, _ []shuttle.Tool, cb llmtypes.TokenCallback) (*LLMResponse, error) {
	f.calls++
	if f.calls <= f.failures {
		if f.said != "" {
			cb(f.said)
		}
		return nil, f.err
	}
	cb("ok")
	return &LLMResponse{Content: "ok"}, nil
}

// A stream that fails before any text reached the client is sent again; one
// that fails after its words went out is not, or the client would read them
// twice.
func TestChatWithRetry_AStreamIsRetriedOnlyBeforeItsTextIsSent(t *testing.T) {
	streamCtx := func() *agentContext {
		c := retryTestContext()
		c.progressCallback = func(ProgressEvent) {}
		return c
	}
	transient := fmt.Errorf("stream error: received exception internalServerException")

	before := &flakyStream{flakyLLM: flakyLLM{failures: 1, err: transient}}
	a := &Agent{id: "retry-test", llm: before, config: &Config{}}
	resp, err := a.chatWithRetry(streamCtx(), []Message{{Role: "user", Content: "q"}}, nil)
	require.NoError(t, err, "a stream that failed before any text was not retried")
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 2, before.calls)

	after := &flakyStream{flakyLLM: flakyLLM{failures: 1, err: transient}, said: "half an answer"}
	a = &Agent{id: "retry-test", llm: after, config: &Config{}}
	_, err = a.chatWithRetry(streamCtx(), []Message{{Role: "user", Content: "q"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internalServerException", "the provider's error is kept")
	assert.Equal(t, 1, after.calls, "a stream whose text the client already has was sent again")
}
