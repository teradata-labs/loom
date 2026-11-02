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

// A transient provider failure must not end the turn. Retry is a property of
// the transport, not a setting: an agent whose Config never mentions retry —
// which is every agent built by an embedder that sets only its own fields — is
// still covered. Before retry stopped being configurable, a zero Config meant
// retry off, and the first internalServerException killed the conversation.
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
	assert.Equal(t, retryMaxRetries+1, provider.calls, "one initial attempt plus the retry budget")
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
