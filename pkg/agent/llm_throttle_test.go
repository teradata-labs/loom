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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/scheduler"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// throttleOnceLLM fails its first N Chat calls with a 429 and then succeeds.
type throttleOnceLLM struct {
	mu       sync.Mutex
	failures int
	calls    int
	// model overrides the reported model name. The slot scheduler's scope is
	// derived from provider name + model, and scheduler.Default() is
	// process-global, so a test that registers or mutates a scope must use a
	// model no other run shares (see uniqueStubModel).
	model string
}

func (s *throttleOnceLLM) Name() string { return "throttle-stub" }
func (s *throttleOnceLLM) Model() string {
	if s.model != "" {
		return s.model
	}
	return "throttle-stub-model"
}

// stubModelSeq makes uniqueStubModel distinct across -count and -cpu reruns
// of the same test in one process.
var stubModelSeq atomic.Uint64

// uniqueStubModel returns a model name, and therefore a scheduler scope, that
// no other test run in this process uses.
func uniqueStubModel(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("throttle-stub-model/%s/%d", t.Name(), stubModelSeq.Add(1))
}

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

// patientRetry is a retry-enabled config whose failure backoff is negligible,
// so a test's wall time is the throttle-patience wait alone.
func patientRetry() RetryConfig {
	return RetryConfig{Enabled: true, MaxRetries: 1, InitialDelay: time.Millisecond, Multiplier: 2, MaxDelay: time.Millisecond}
}

// With retry DISABLED a throttle still pauses and goes again rather than
// ending the conversation: disabling retry opts out of retrying failures, not
// out of honoring provider flow control. Retry is off for every agent that
// looms serve builds and for registry agents with a behavior: block, so this
// is the common case. Covered on both the direct and the streaming path; the
// stub succeeds on its second call.
func TestChatWithRetry_ThrottleDoesNotKill_RetryDisabled(t *testing.T) {
	disabled := []struct {
		name  string
		retry RetryConfig
	}{
		{"zero value", RetryConfig{}},
		{"enabled false", RetryConfig{Enabled: false, MaxRetries: 3, InitialDelay: time.Millisecond, Multiplier: 2, MaxDelay: time.Millisecond}},
		{"max retries zero", RetryConfig{Enabled: true, MaxRetries: 0, InitialDelay: time.Millisecond, Multiplier: 2, MaxDelay: time.Millisecond}},
	}
	for _, streaming := range []bool{false, true} {
		for _, tc := range disabled {
			t.Run(fmt.Sprintf("%s/streaming=%v", tc.name, streaming), func(t *testing.T) {
				// Each subtest owns its stub and never enables the scheduler,
				// so the 2 s patience waits can overlap.
				t.Parallel()
				var (
					provider LLMProvider
					calls    func() int
					cb       ProgressCallback
					want     string
				)
				if streaming {
					stub := &throttleOnceStreamingLLM{throttleOnceLLM: throttleOnceLLM{failures: 1}}
					provider = stub
					calls = func() int { stub.mu.Lock(); defer stub.mu.Unlock(); return stub.streamCalls }
					cb = func(ProgressEvent) {}
					want = "hello"
				} else {
					stub := &throttleOnceLLM{failures: 1}
					provider = stub
					calls = func() int { stub.mu.Lock(); defer stub.mu.Unlock(); return stub.calls }
					want = "ok"
				}
				a := &Agent{id: "t-disabled", llm: provider, config: &Config{Retry: tc.retry}}

				// Backstop only: one 2 s patience wait fits well inside it.
				base, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				ctx := &agentContext{Context: base, tracer: observability.NewNoOpTracer(), progressCallback: cb}

				start := time.Now()
				resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
				elapsed := time.Since(start)

				require.NoError(t, err, "a retry-disabled agent must ride out a throttle, not fail")
				assert.Equal(t, want, resp.Content)
				assert.Equal(t, 2, calls(), "the throttled call must be re-invoked once")
				assert.GreaterOrEqual(t, elapsed, throttleInitialDelay,
					"the re-invocation must come after a patience wait (took %s)", elapsed)
			})
		}
	}
}

// A 429 on the non-streaming path with retry ENABLED must not consume retry
// attempts: one throttle plus MaxRetries genuine failures still succeeds.
func TestChatWithRetry_ThrottleConsumesNoAttempts(t *testing.T) {
	stub := &throttleOnceLLM{failures: 1}
	a := &Agent{id: "t2", llm: stub, config: &Config{Retry: patientRetry()}}
	ctx := newCtxDumpContext("sess-throttle-r", observability.NewNoOpTracer(), nil)

	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
}

// A 429 on the STREAMING path of a retry-enabled agent — the path that
// previously killed the conversation outright — must pause, re-invoke, and
// deliver clean content with no tokens duplicated from the failed attempt.
func TestChatWithStreaming_ThrottleDoesNotKill(t *testing.T) {
	stub := &throttleOnceStreamingLLM{throttleOnceLLM: throttleOnceLLM{failures: 1}}
	require.True(t, llmtypes.SupportsStreaming(stub))

	a := &Agent{id: "t3", llm: stub, config: &Config{Retry: patientRetry()}}
	// Collect EVERY partial, not only the last: the final "LLM response
	// complete" event carries resp.Content and would hide a duplicated
	// partial emitted after the throttle wait.
	var (
		partialsMu sync.Mutex
		partials   []string
	)
	ctx := newCtxDumpContext("sess-throttle-str", observability.NewNoOpTracer(), func(ev ProgressEvent) {
		if ev.PartialContent != "" {
			partialsMu.Lock()
			partials = append(partials, ev.PartialContent)
			partialsMu.Unlock()
		}
	})

	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello", resp.Content)
	assert.Equal(t, 2, stub.streamCalls, "throttled stream must re-invoke")

	partialsMu.Lock()
	defer partialsMu.Unlock()
	require.NotEmpty(t, partials, "streaming must emit progress partials")
	for i, p := range partials {
		assert.NotContains(t, p, "junk-from-failed-attempt",
			"partial %d (%q) carries a token from the throttled attempt; "+
				"buffered state must reset before re-invoking", i, p)
	}
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

// An absorbed throttle must still reach the slot scheduler's AIMD seam.
//
// chatWithRetry observes the call outcome in a defer, which only ever sees
// the FINAL error — so once patience rides a 429 out and the retry succeeds,
// that defer reports SUCCESS and would GROW the scope's ceiling on the very
// signal that should shrink it. absorbThrottle reports at the moment of
// absorption to close that hole; this pins it.
func TestChatWithRetry_AbsorbedThrottleStillHalvesCeiling(t *testing.T) {
	// A fresh scope per run: scheduler.Default() is process-global and
	// Registry.For keeps the first registration, so a shared model name would
	// start every rerun with the ceiling the previous run already halved.
	stub := &throttleOnceLLM{failures: 1, model: uniqueStubModel(t)}
	a := &Agent{id: "t-aimd", llm: stub, config: &Config{Retry: patientRetry()}}

	// Register the scope with a known ceiling BEFORE enabling scheduling
	// (Registry.For keeps the first registration's config).
	sched := scheduler.Default().For(a.schedulerScope(), scheduler.Config{
		TokensPerMinute:     1000,
		InteractiveHeadroom: -1,
	})
	before := sched.State().EffectiveTokensPerMinute
	require.Equal(t, int64(1000), before)

	scheduler.SetEnabled(true)
	defer scheduler.SetEnabled(false)

	ctx := newCtxDumpContext("sess-throttle-aimd", observability.NewNoOpTracer(), nil)
	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 2, stub.calls, "the throttle must have been absorbed and retried")

	after := sched.State().EffectiveTokensPerMinute
	assert.Less(t, after, before,
		"a throttle absorbed by patience must still shrink the scope's ceiling; "+
			"if it does not, the AIMD seam saw the eventual success and grew instead")
}
