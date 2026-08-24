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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// throttleOnceLLM fails its first N Chat calls with a 429 and then succeeds.
type throttleOnceLLM struct {
	mu       sync.Mutex
	failures int
	calls    int
}

func (s *throttleOnceLLM) Name() string  { return "throttle-stub" }
func (s *throttleOnceLLM) Model() string { return "throttle-stub-model" }

func (s *throttleOnceLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls <= s.failures {
		return nil, fmt.Errorf("API error (status 429): {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"Please retry after 1 seconds.\"}}")
	}
	return &LLMResponse{Content: "ok", StopReason: "end_turn"}, nil
}

// throttleOnceStreamingLLM is the streaming twin.
type throttleOnceStreamingLLM struct {
	throttleOnceLLM
	streamCalls int
}

func (s *throttleOnceStreamingLLM) ChatStream(_ context.Context, _ []Message, _ []shuttle.Tool, cb llmtypes.TokenCallback) (*LLMResponse, error) {
	s.mu.Lock()
	s.streamCalls++
	n := s.streamCalls
	s.mu.Unlock()
	if n <= s.failures {
		// Emit a token BEFORE failing: a retried stream must not duplicate it.
		if cb != nil {
			cb("junk-from-failed-attempt")
		}
		return nil, fmt.Errorf("API error (status 429): rate_limit_exceeded")
	}
	if cb != nil {
		cb("hel")
		cb("lo")
	}
	return &LLMResponse{Content: "hello", StopReason: "end_turn"}, nil
}

// A 429 on the non-streaming path with retry DISABLED must pause and go
// again, not kill the conversation — disabling retry opts out of retrying
// failures, not out of honoring flow control.
func TestChatWithRetry_ThrottleDoesNotKill_RetryDisabled(t *testing.T) {
	stub := &throttleOnceLLM{failures: 1}
	a := &Agent{id: "t1", llm: stub, config: &Config{}} // Retry zero-value => direct path
	ctx := newCtxDumpContext("sess-throttle-ns", observability.NewNoOpTracer(), nil)

	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 2, stub.calls)
}

// A 429 on the non-streaming path with retry ENABLED must not consume retry
// attempts: one throttle plus MaxRetries genuine failures still succeeds.
func TestChatWithRetry_ThrottleConsumesNoAttempts(t *testing.T) {
	stub := &throttleOnceLLM{failures: 1}
	a := &Agent{id: "t2", llm: stub, config: &Config{
		Retry: RetryConfig{Enabled: true, MaxRetries: 1, InitialDelay: time.Millisecond, Multiplier: 2, MaxDelay: time.Millisecond},
	}}
	ctx := newCtxDumpContext("sess-throttle-r", observability.NewNoOpTracer(), nil)

	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
}

// A 429 on the STREAMING path — the path that previously killed the
// conversation outright — must pause, re-invoke, and deliver clean content
// with no tokens duplicated from the failed attempt.
func TestChatWithStreaming_ThrottleDoesNotKill(t *testing.T) {
	stub := &throttleOnceStreamingLLM{throttleOnceLLM: throttleOnceLLM{failures: 1}}
	require.True(t, llmtypes.SupportsStreaming(stub))

	a := &Agent{id: "t3", llm: stub, config: &Config{}}
	var lastPartial string
	ctx := newCtxDumpContext("sess-throttle-str", observability.NewNoOpTracer(), func(ev ProgressEvent) {
		if ev.PartialContent != "" {
			lastPartial = ev.PartialContent
		}
	})

	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", resp.Content)
	assert.Equal(t, 2, stub.streamCalls, "throttled stream must re-invoke")
	assert.NotContains(t, lastPartial, "junk-from-failed-attempt",
		"buffered tokens from the failed attempt must be discarded on retry")
}

// The patience budget is a hard stop: once cumulative throttle waiting would
// exceed it, the throttle error surfaces instead of waiting again.
func TestThrottlePatienceBudget(t *testing.T) {
	p := throttlePatience{total: throttleBudget - time.Second}
	ctx := newCtxDumpContext("sess-throttle-budget", observability.NewNoOpTracer(), nil)
	err := p.wait(ctx, fmt.Errorf("API error (status 429): still throttled"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "patience budget")
}
