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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// fakeThrottleClock is a throttleClock that never sleeps: After advances the
// clock by the wait and fires at once, and a stub provider calls Advance to
// model the time a provider invocation takes (its RateLimiter's own retries).
type fakeThrottleClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeThrottleClock() *fakeThrottleClock {
	return &fakeThrottleClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeThrottleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeThrottleClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func (c *fakeThrottleClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Sleeps returns the patience waits taken so far, in order.
func (c *fakeThrottleClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// clockedLLM fails its first `failures` invocations with err (every
// invocation when failures < 0) and then succeeds. Each invocation advances
// the fake clock by cost first, as a provider RateLimiter that runs its own
// retries and backoff inside the call would.
type clockedLLM struct {
	clock    *fakeThrottleClock
	cost     time.Duration
	err      error
	failures int

	mu    sync.Mutex
	calls int
}

func (s *clockedLLM) Name() string  { return "clocked-stub" }
func (s *clockedLLM) Model() string { return "clocked-stub-model" }

func (s *clockedLLM) invoke() error {
	s.clock.Advance(s.cost)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failures < 0 || s.calls <= s.failures {
		return s.err
	}
	return nil
}

func (s *clockedLLM) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *clockedLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	if err := s.invoke(); err != nil {
		return nil, err
	}
	return &LLMResponse{Content: "ok", StopReason: "end_turn"}, nil
}

// clockedStreamingLLM is the streaming twin of clockedLLM.
type clockedStreamingLLM struct {
	clockedLLM
}

func (s *clockedStreamingLLM) ChatStream(_ context.Context, _ []Message, _ []shuttle.Tool, cb llmtypes.TokenCallback) (*LLMResponse, error) {
	if err := s.invoke(); err != nil {
		return nil, err
	}
	if cb != nil {
		cb("ok")
	}
	return &LLMResponse{Content: "ok", StopReason: "end_turn"}, nil
}

// exhaustedWrapError is a stand-in for any "out of retries" wrapper that is
// not llm.RetriesExhaustedError: its own message names no throttle, and it
// exposes the last provider error through Unwrap. The patience rule must hold
// through it via errors.As, not via message text. The tests below also use
// the real llm.RetriesExhaustedError that the provider RateLimiter returns.
type exhaustedWrapError struct {
	attempts int
	err      error
}

func (e *exhaustedWrapError) Error() string {
	return fmt.Sprintf("provider gave up after %d attempts: %v", e.attempts, e.err)
}

func (e *exhaustedWrapError) Unwrap() error { return e.err }

// throttlePath is one of the three ways chatWithRetry reaches the provider.
type throttlePath struct {
	name      string
	streaming bool
	retry     RetryConfig
}

func throttlePaths() []throttlePath {
	return []throttlePath{
		{name: "retry-enabled", retry: patientRetry()},
		{name: "streaming", streaming: true, retry: patientRetry()},
		{name: "retry-disabled", retry: RetryConfig{}},
	}
}

// runClockedCall runs one chatWithRetry on the given path against a stub on
// clk, and returns the result, the stub and the fake time the call took.
func runClockedCall(t *testing.T, path throttlePath, clk *fakeThrottleClock, cost time.Duration, failures int, provErr error) (*LLMResponse, error, *clockedLLM, time.Duration) {
	t.Helper()
	var (
		provider LLMProvider
		stub     *clockedLLM
		cb       ProgressCallback
	)
	if path.streaming {
		s := &clockedStreamingLLM{clockedLLM: clockedLLM{clock: clk, cost: cost, err: provErr, failures: failures}}
		provider, stub, cb = s, &s.clockedLLM, func(ProgressEvent) {}
	} else {
		s := &clockedLLM{clock: clk, cost: cost, err: provErr, failures: failures}
		provider, stub = s, s
	}
	a := &Agent{id: "t-clocked-" + path.name, llm: provider, config: &Config{Retry: path.retry}, throttleClock: clk}

	// Real-time backstop only: the fake clock makes every wait instant.
	ctxBase, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx := &agentContext{Context: ctxBase, tracer: observability.NewNoOpTracer(), progressCallback: cb}

	start := clk.Now()
	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	return resp, err, stub, clk.Now().Sub(start)
}

// F1 (Josh-Schoen, PR #359): the throttle budget is wall-clock time, not the
// sum of the patience sleeps. A permanent throttle whose every invocation
// takes time (a provider RateLimiter running MaxRetries+1 sends with backoff
// inside each call) must still surface by the budget, plus at most the one
// invocation that runs after the last wait. Counting sleeps only let such a
// call run for 16 to 17 minutes at limiter defaults, and about 65 minutes
// with Retry-After: 60.
func TestThrottlePatience_WallClockBudgetBoundsPermanentThrottle(t *testing.T) {
	plain429 := fmt.Errorf("LLM request failed after 6 retries due to throttling: %w",
		errors.New("API error (status 429): rate_limit_exceeded"))
	hinted60 := fmt.Errorf("LLM request failed after 6 retries due to throttling: %w",
		llm.NewThrottleError(errors.New("API error (status 429)"), 60*time.Second))

	cases := []struct {
		name string
		cost time.Duration // fake time each provider invocation takes
		err  error
	}{
		{name: "instant invocations", cost: 0, err: plain429},
		// Limiter defaults: max_retries 5, retry_backoff 1s doubling, so six
		// sends with 1+2+4+8+16 s of backoff, about 31 s per invocation.
		{name: "limiter retries inside each call", cost: 31 * time.Second, err: plain429},
		{name: "limiter retries with Retry-After 60", cost: 5 * time.Minute, err: hinted60},
	}
	for _, tc := range cases {
		for _, path := range throttlePaths() {
			t.Run(tc.name+"/"+path.name, func(t *testing.T) {
				t.Parallel()
				clk := newFakeThrottleClock()
				resp, err, stub, took := runClockedCall(t, path, clk, tc.cost, -1, tc.err)

				require.Error(t, err, "a permanent throttle must surface once the budget is spent")
				assert.Nil(t, resp)
				assert.Contains(t, err.Error(), "patience budget")
				assert.True(t, llm.IsThrottle(err), "the surfaced error must still classify as a throttle")
				assert.GreaterOrEqual(t, stub.Calls(), 2, "patience must have re-invoked at least once")
				assert.LessOrEqual(t, took, throttleBudget+tc.cost,
					"wall-clock time throttled (%s over %d invocations, %d waits) must stay within the %s budget plus one invocation (%s)",
					took, stub.Calls(), len(clk.Sleeps()), throttleBudget, tc.cost)
			})
		}
	}
}

// The error a RateLimiter returns after it runs out of its own retries on a
// throttle behaves the SAME on all three paths: it is waited out under the
// budget and the call goes on. This holds through any wrapping: a %w chain
// that keeps the 429 text, a typed wrap whose message names no throttle, and
// the real llm.RetriesExhaustedError. The retry-enabled path checks for a
// throttle before it checks llm.IsRetriesExhausted, so the exhausted
// throttle is not final there either.
func TestThrottleExhausted_SameOnAllPaths(t *testing.T) {
	const hint = 5 * time.Second
	errs := []struct {
		name string
		err  error
	}{
		{
			name: "fmt wrap of a 429",
			err: fmt.Errorf("LLM request failed after 6 retries due to throttling: %w",
				errors.New("API error (status 429): rate_limit_exceeded, retry after 5 seconds")),
		},
		{
			name: "typed wrap of a ThrottleError",
			err:  &exhaustedWrapError{attempts: 6, err: llm.NewThrottleError(errors.New("provider asked us to slow down"), hint)},
		},
		{
			name: "llm.RetriesExhaustedError of a ThrottleError",
			err: &llm.RetriesExhaustedError{Attempts: 6, Cause: "throttling",
				Err: llm.NewThrottleError(errors.New("API error (status 429): rate_limit_exceeded"), hint)},
		},
	}
	for _, e := range errs {
		results := map[string][]time.Duration{}
		for _, path := range throttlePaths() {
			t.Run(e.name+"/"+path.name, func(t *testing.T) {
				require.True(t, llm.IsThrottle(e.err), "precondition: the wrapped error is a throttle")
				clk := newFakeThrottleClock()
				resp, err, stub, _ := runClockedCall(t, path, clk, time.Second, 2, e.err)

				require.NoError(t, err, "a throttle-exhausted error must be waited out, not returned")
				assert.Equal(t, "ok", resp.Content)
				assert.Equal(t, 3, stub.Calls(), "two throttled invocations, then the success")
				results[path.name] = clk.Sleeps()
			})
		}
		t.Run(e.name+"/identical waits", func(t *testing.T) {
			want := results["retry-disabled"]
			require.Len(t, want, 2)
			for name, got := range results {
				assert.Equal(t, want, got, "path %s waited differently from retry-disabled", name)
			}
		})
	}
}

// A RateLimiter's "out of retries" error whose cause is NOT a throttle (a
// transient 5xx) never enters throttle patience on any path: no wait, no
// patience re-invocation, and the provider error stays in the chain.
//
// The retry-disabled and streaming paths return it at once. The retry-enabled
// path returns the real llm.RetriesExhaustedError at once too (the limiter
// already spent the budget), but still runs its own failure retry over the
// stand-in wrappers (MaxRetries+1 invocations), because only that typed error
// is final there. The last row puts "429" and "rate limit" in the 5xx body:
// the typed llm.TransientError wins over the message fallback, so it is still
// not a throttle.
func TestNonThrottleExhausted_NeverWaitsOnAnyPath(t *testing.T) {
	cause := errors.New("API error (status 503): upstream unavailable")
	sniffable := errors.New("API error (status 503): upstream rate limit proxy failed, request id 4290001")
	errs := []struct {
		name  string
		cause error
		err   error
		// retryEnabledCalls is the provider invocations on the retry-enabled
		// path; streaming and retry-disabled always make one.
		retryEnabledCalls int
	}{
		{
			name: "fmt wrap", cause: cause,
			err:               fmt.Errorf("LLM request failed after 6 retries due to a transient server error: %w", cause),
			retryEnabledCalls: patientRetry().MaxRetries + 1, // agent failure retry
		},
		{
			name: "typed wrap", cause: cause,
			err:               &exhaustedWrapError{attempts: 6, err: cause},
			retryEnabledCalls: patientRetry().MaxRetries + 1, // agent failure retry
		},
		{
			name: "llm.RetriesExhaustedError of a TransientError", cause: cause,
			err: &llm.RetriesExhaustedError{Attempts: 6, Cause: "a transient server error",
				Err: llm.NewTransientError(cause, 503, 0)},
			retryEnabledCalls: 1, // the limiter's budget is final
		},
		{
			name: "llm.RetriesExhaustedError of a TransientError whose body names 429", cause: sniffable,
			err: &llm.RetriesExhaustedError{Attempts: 6, Cause: "a transient server error",
				Err: llm.NewTransientError(sniffable, 503, 0)},
			retryEnabledCalls: 1, // the limiter's budget is final
		},
	}
	for _, e := range errs {
		wantCalls := map[string]int{
			"retry-enabled":  e.retryEnabledCalls,
			"streaming":      1,
			"retry-disabled": 1,
		}
		for _, path := range throttlePaths() {
			t.Run(e.name+"/"+path.name, func(t *testing.T) {
				t.Parallel()
				require.False(t, llm.IsThrottle(e.err), "precondition: the wrapped error is not a throttle")
				clk := newFakeThrottleClock()
				resp, err, stub, _ := runClockedCall(t, path, clk, time.Second, -1, e.err)

				require.Error(t, err)
				assert.Nil(t, resp)
				assert.ErrorIs(t, err, e.cause, "the provider error must stay in the chain")
				assert.NotContains(t, err.Error(), "patience budget")
				assert.Empty(t, clk.Sleeps(), "a non-throttle error must never start a throttle wait")
				assert.Equal(t, wantCalls[path.name], stub.Calls())
			})
		}
	}
}

// F1 (Josh-Schoen, PR #359, 2026-11-06): a context-too-long refusal is never
// a throttle, even when its text matches llm.IsThrottle's substring fallback.
// Anthropic puts the raw 400 body into the error, and a token count such as
// 204296 contains "429". Before the fix, all three paths waited this error
// out as a throttle and re-sent a prompt that can never fit, for up to the
// whole budget, before compaction relief could run. It must return at once,
// after one provider call, with ErrContextTooLong in the chain.
func TestContextTooLong_NeverWaitedOutAsThrottle(t *testing.T) {
	tooLong := fmt.Errorf("API error (status 400): prompt is too long: 204296 tokens > 200000 maximum: %w",
		llm.ErrContextTooLong)
	require.True(t, llm.IsThrottle(tooLong),
		"precondition: the substring fallback classifies this refusal as a throttle")

	for _, path := range throttlePaths() {
		t.Run(path.name, func(t *testing.T) {
			t.Parallel()
			clk := newFakeThrottleClock()
			resp, err, stub, took := runClockedCall(t, path, clk, time.Second, -1, tooLong)

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.ErrorIs(t, err, llm.ErrContextTooLong, "the refusal must reach relief as context-too-long")
			assert.NotContains(t, err.Error(), "patience budget")
			assert.NotContains(t, err.Error(), "llm throttled")
			assert.Equal(t, 1, stub.Calls(), "a context-too-long refusal must not be re-sent")
			assert.Empty(t, clk.Sleeps(), "a context-too-long refusal must never start a throttle wait")
			assert.Equal(t, time.Second, took, "only the one provider invocation may take time")
		})
	}
}

// throttlingLLM fails every invocation with err and never moves any clock.
type throttlingLLM struct {
	err   error
	mu    sync.Mutex
	calls int
}

func (s *throttlingLLM) Name() string  { return "throttling-stub" }
func (s *throttlingLLM) Model() string { return "throttling-stub-model" }

func (s *throttlingLLM) Chat(context.Context, []Message, []shuttle.Tool) (*LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return nil, s.err
}

func (s *throttlingLLM) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// WithInstantThrottleWaitsForTesting keeps the real budget and backoff and
// only removes the sleeping: a permanent throttle surfaces with the same
// budget error after the full budget of virtual time, in real milliseconds.
func TestWithInstantThrottleWaitsForTesting(t *testing.T) {
	plain429 := errors.New("API error (status 429): rate_limit_exceeded")
	for _, path := range []throttlePath{
		{name: "retry-enabled", retry: patientRetry()},
		{name: "retry-disabled", retry: RetryConfig{}},
	} {
		t.Run(path.name, func(t *testing.T) {
			t.Parallel()
			stub := &throttlingLLM{err: plain429}
			a := &Agent{id: "t-instant-" + path.name, llm: stub, config: &Config{Retry: path.retry}}
			WithInstantThrottleWaitsForTesting()(a)
			clk, ok := a.throttleClock.(*virtualThrottleClock)
			require.True(t, ok, "the option must install the virtual throttle clock")

			ctxBase, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx := &agentContext{Context: ctxBase, tracer: observability.NewNoOpTracer()}

			virtualStart := clk.Now()
			realStart := time.Now()
			resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
			realTook := time.Since(realStart)
			virtualTook := clk.Now().Sub(virtualStart)

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.Contains(t, err.Error(), "patience budget", "the budget error surfaces unchanged")
			assert.True(t, llm.IsThrottle(err))
			assert.GreaterOrEqual(t, stub.Calls(), 2, "patience must have re-invoked at least once")
			assert.LessOrEqual(t, virtualTook, throttleBudget, "the virtual waits stay within the budget")
			assert.Greater(t, virtualTook, throttleBudget-throttleMaxDelay,
				"the budget is spent in virtual time before the error surfaces")
			assert.Less(t, realTook, 10*time.Second, "no real sleeping")
		})
	}
}

// The virtual clock is safe for concurrent use, as an agent's throttle clock
// is shared by every concurrent LLM call of that agent.
func TestVirtualThrottleClock_Concurrent(t *testing.T) {
	clk := &virtualThrottleClock{now: time.Unix(0, 0)}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				<-clk.After(time.Second)
				_ = clk.Now()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, time.Unix(0, 0).Add(1600*time.Second), clk.Now())
}
