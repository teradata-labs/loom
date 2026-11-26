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
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/azureopenai"
	"github.com/teradata-labs/loom/pkg/llm/gemini"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/llm/scheduler"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

type schedStubLLM struct{ calls int }

func (s *schedStubLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	s.calls++
	return &LLMResponse{Content: "ok", Usage: llmtypes.Usage{TotalTokens: 42}}, nil
}
func (s *schedStubLLM) Name() string  { return "sched-stub" }
func (s *schedStubLLM) Model() string { return "sched-model" }

// Every provider call through the chatWithRetry funnel must acquire and
// release a slot when scheduling is enabled and the turn carries SlotInfo —
// and must be a transparent pass-through otherwise.
func TestChatWithRetryAcquiresSchedulerSlot(t *testing.T) {
	scheduler.SetEnabled(true)
	defer scheduler.SetEnabled(false)

	llm := &schedStubLLM{}
	a := &Agent{id: "sched-test", llm: llm, config: &Config{}}

	base := session.WithSessionID(context.Background(), "sess-sched")
	stamped := scheduler.WithSlotInfo(base, loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)
	ctx := &agentContext{Context: stamped, tracer: observability.NewNoOpTracer()}

	before := scheduler.Default().For(a.schedulerScope(), scheduler.Config{}).State().GrantsTotal

	resp, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	require.Equal(t, 1, llm.calls)

	st := scheduler.Default().For(a.schedulerScope(), scheduler.Config{}).State()
	assert.Equal(t, before+1, st.GrantsTotal, "the call must have consumed exactly one grant")
	assert.Equal(t, int64(0), st.ReservedTokensOutstanding, "the grant must be released after the call")

	// Second call classifies IN_FLIGHT via the shared SlotInfo.
	si := scheduler.SlotInfoFrom(stamped)
	require.NotNil(t, si)

	// Without SlotInfo the funnel passes through untouched.
	plain := &agentContext{Context: base, tracer: observability.NewNoOpTracer()}
	_, err = a.chatWithRetry(plain, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	st2 := scheduler.Default().For(a.schedulerScope(), scheduler.Config{}).State()
	assert.Equal(t, before+1, st2.GrantsTotal, "an unstamped turn must not touch the scheduler")
}

// throttleStubLLM is a fake provider with NO scheduler wiring of its own: it
// only returns what any HTTP client returns — an llm.ThrottleError on 429,
// a clean response otherwise.
type throttleStubLLM struct {
	name     string
	throttle bool
}

func (s *throttleStubLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	if s.throttle {
		return nil, llm.NewThrottleError(errors.New("API error (status 429): busy"), 250*time.Millisecond)
	}
	return &LLMResponse{Content: "ok", Usage: llmtypes.Usage{TotalTokens: 10}}, nil
}
func (s *throttleStubLLM) Name() string  { return s.name }
func (s *throttleStubLLM) Model() string { return "m" }

// The provider-agnostic AIMD seam: throttle and success outcomes reach the
// scope's scheduler at the chatWithRetry funnel for ANY provider, with zero
// per-provider client wiring — and with or without a SlotInfo grant, because
// an unscheduled call's 429 depletes the same shared quota.
func TestFunnelObservesThrottleAndSuccessProviderAgnostic(t *testing.T) {
	scheduler.SetEnabled(true)
	defer scheduler.SetEnabled(false)

	// Unique scope per run: schedulers live in the process-wide registry, so
	// a repeated run (-count>1) must not inherit a prior run's hold-off.
	stub := &throttleStubLLM{name: fmt.Sprintf("funnel-agnostic-%d", time.Now().UnixNano())}
	a := &Agent{id: "funnel-test", llm: stub, config: &Config{}}
	ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

	sched := scheduler.Default().For(a.schedulerScope(), scheduler.Config{})
	before := sched.State().EffectiveTokensPerMinute

	// A throttle now PAUSES the conversation rather than failing it (see
	// throttlePatience in llm_retry.go), so a permanently-throttling stub
	// surfaces its error at the caller's deadline instead of immediately.
	// The deadline is what bounds this call; the AIMD observation itself
	// happens at absorption, before the first wait.
	stub.throttle = true
	throttledCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := a.chatWithRetry(
		&agentContext{Context: throttledCtx, tracer: observability.NewNoOpTracer()},
		[]Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err)
	st := sched.State()
	assert.Equal(t, before/2, st.EffectiveTokensPerMinute,
		"a surfaced llm.ThrottleError must halve the scope's ceiling")
	assert.NotNil(t, st.NextWake, "the throttle's RetryAfter must arm the scope's wake")

	stub.throttle = false
	_, err = a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Greater(t, sched.State().EffectiveTokensPerMinute, before/2,
		"a clean completion must grow the ceiling back (AIMD increase)")
}

// usageStubLLM returns a fixed Usage so the scheduler's charge can be observed.
type usageStubLLM struct {
	name  string
	usage llmtypes.Usage
}

func (s *usageStubLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", Usage: s.usage}, nil
}
func (s *usageStubLLM) Name() string  { return s.name }
func (s *usageStubLLM) Model() string { return "m" }

// The funnel must release a grant with the provider-METERED usage. OpenAI and
// Gemini count cached prompt tokens toward TPM, so a cached call is charged
// its raw total (Usage.RateLimitTokens), not the cache-exclusive TotalTokens;
// charging 341 for a 17,519-token call would over-admit until 429s arrive.
// A provider that sets no RateLimitTokens is still charged TotalTokens.
func TestChatWithRetryChargesMeteredTokensToScheduler(t *testing.T) {
	scheduler.SetEnabled(true)
	defer scheduler.SetEnabled(false)

	// litellm-shaped cached call: prompt_tokens 17183 = 5 uncached + 16817
	// read + 361 write, 336 completion.
	cached := llmtypes.Usage{InputTokens: 5, OutputTokens: 336, TotalTokens: 341,
		CacheReadInputTokens: 16817, CacheCreationInputTokens: 361}

	tests := []struct {
		name            string
		rateLimitTokens int
		wantNextAdmits  bool
	}{
		// 17,519 charged + a 5,000 reservation exceeds the 20,000 budget.
		{name: "cached call is charged the raw total", rateLimitTokens: 17519, wantNextAdmits: false},
		// 341 charged + 5,000 fits.
		{name: "no RateLimitTokens is charged TotalTokens", rateLimitTokens: 0, wantNextAdmits: true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := cached
			u.RateLimitTokens = tt.rateLimitTokens
			// Unique scope per run: schedulers live in the process-wide
			// registry, so a repeated run (-count>1) must not inherit the
			// 17,519 tokens the cached case leaves in the window.
			llm := &usageStubLLM{name: fmt.Sprintf("metered-stub-%d-%d", i, time.Now().UnixNano()), usage: u}
			a := &Agent{id: "metered-test", llm: llm, config: &Config{}}

			s := scheduler.Default().For(a.schedulerScope(), scheduler.Config{})
			// Pinned ceiling, full utilization, no batch headroom: budget = 20,000.
			s.SetConfig(20000, 1.0, 0, -1)

			base := session.WithSessionID(context.Background(), "sess-metered")
			stamped := scheduler.WithSlotInfo(base, loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)
			ctx := &agentContext{Context: stamped, tracer: observability.NewNoOpTracer()}
			_, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
			require.NoError(t, err)
			require.Equal(t, int64(0), s.State().ReservedTokensOutstanding)

			actx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			g, err := s.Acquire(actx, scheduler.Request{ReservationTokens: 5000})
			if tt.wantNextAdmits {
				require.NoError(t, err)
				g.Release(1)
				return
			}
			require.Error(t, err, "the window must already hold the cached call's metered 17,519 tokens")
			assert.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

type scriptedErrorStubLLM struct {
	calls int
	fn    func(call int, ctx context.Context) (*LLMResponse, error)
}

func (s *scriptedErrorStubLLM) Chat(ctx context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	s.calls++
	return s.fn(s.calls, ctx)
}
func (s *scriptedErrorStubLLM) Name() string  { return "scripted-err-stub" }
func (s *scriptedErrorStubLLM) Model() string { return "scripted-err-model" }

func TestDispatchChat_TransientModelFaultsAndTypedRefusals(t *testing.T) {
	timeoutMsg := "The model took too long to respond"
	streamMsg := "The model stream was interrupted"
	notReadyMsg := "The model is not ready"
	throttleMsg := "Rate exceeded"

	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			name: "wrapped Bedrock ModelTimeoutException retries and succeeds on call 2",
			err:  fmt.Errorf("bedrock invoke failed: %w", &bedrocktypes.ModelTimeoutException{Message: &timeoutMsg}),
		},
		{
			name: "wrapped Bedrock ModelStreamErrorException retries and succeeds on call 2",
			err:  fmt.Errorf("bedrock invoke failed: %w", &bedrocktypes.ModelStreamErrorException{Message: &streamMsg}),
		},
		{
			name: "wrapped Bedrock ModelNotReadyException retries and succeeds on call 2",
			err:  fmt.Errorf("bedrock invoke failed: %w", &bedrocktypes.ModelNotReadyException{Message: &notReadyMsg}),
		},
		{
			name: "wrapped Bedrock ThrottlingException retries and succeeds on call 2",
			err:  fmt.Errorf("bedrock invoke failed: %w", &bedrocktypes.ThrottlingException{Message: &throttleMsg}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &scriptedErrorStubLLM{
				fn: func(call int, _ context.Context) (*LLMResponse, error) {
					if call == 1 {
						return nil, tc.err
					}
					return &LLMResponse{Content: "recovered"}, nil
				},
			}
			cfg := DefaultConfig()
			cfg.Retry.MaxRetries = 2
			cfg.Retry.InitialDelay = time.Millisecond
			cfg.Retry.MaxDelay = 2 * time.Millisecond
			a := &Agent{id: "retry-transient-fault", llm: stub, config: cfg, throttleClock: &virtualThrottleClock{now: time.Unix(0, 0)}}
			ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

			resp, err := a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, "recovered", resp.Content)
			assert.Equal(t, 2, stub.calls)
		})
	}

	t.Run("typed permanent Smithy client refusal takes one call", func(t *testing.T) {
		stub := &scriptedErrorStubLLM{
			fn: func(int, context.Context) (*LLMResponse, error) {
				return nil, fmt.Errorf("bedrock invoke failed: %w", &smithy.GenericAPIError{
					Code:    "ValidationException",
					Message: "invalid model identifier",
					Fault:   smithy.FaultClient,
				})
			},
		}
		cfg := DefaultConfig()
		cfg.Retry.MaxRetries = 3
		cfg.Retry.InitialDelay = time.Millisecond
		a := &Agent{id: "retry-perm-smithy", llm: stub, config: cfg}
		ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

		_, err := a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
		require.Error(t, err)
		assert.Equal(t, 1, stub.calls)
	})

	t.Run("exhausted inner rate-limiter budget on a 5xx takes one outer call", func(t *testing.T) {
		stub := &scriptedErrorStubLLM{
			fn: func(int, context.Context) (*LLMResponse, error) {
				return nil, &llm.RetriesExhaustedError{
					Attempts: 6,
					Cause:    "transient",
					Err:      llm.NewTransientError(errors.New("API error (status 503): overloaded"), http.StatusServiceUnavailable, 0),
				}
			},
		}
		cfg := DefaultConfig()
		cfg.Retry.MaxRetries = 3
		cfg.Retry.InitialDelay = time.Millisecond
		a := &Agent{id: "retry-exhausted-inner", llm: stub, config: cfg}
		ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

		_, err := a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
		require.Error(t, err)
		assert.Equal(t, 1, stub.calls)
	})

	t.Run("context cancellation during call stops immediately", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(context.Background())
		stub := &scriptedErrorStubLLM{
			fn: func(int, context.Context) (*LLMResponse, error) {
				cancel()
				return nil, errors.New("request interrupted")
			},
		}
		cfg := DefaultConfig()
		cfg.Retry.MaxRetries = 3
		cfg.Retry.InitialDelay = time.Millisecond
		a := &Agent{id: "retry-cancel", llm: stub, config: cfg}
		ctx := &agentContext{Context: callCtx, tracer: observability.NewNoOpTracer()}

		_, err := a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
		require.Error(t, err)
		assert.Equal(t, 1, stub.calls)
	})

	t.Run("real OpenAI client HTTP 401 and JSON error body fail in one call", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"authentication_error","code":"invalid_api_key"}}`))
		}))
		defer srv.Close()

		client := openai.NewClient(openai.Config{APIKey: "bad-key", Endpoint: srv.URL, Model: "gpt-4o"})
		cfg := DefaultConfig()
		cfg.Retry.MaxRetries = 3
		cfg.Retry.InitialDelay = time.Millisecond
		a := &Agent{id: "retry-openai-401", llm: client, config: cfg}
		ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

		_, err := a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
		require.Error(t, err)
		assert.Equal(t, 1, calls)
	})

	t.Run("real Gemini client HTTP 400 error fails in one call", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid","status":"INVALID_ARGUMENT"}}`))
		}))
		defer srv.Close()

		u, err := url.Parse(srv.URL)
		require.NoError(t, err)
		origTransport := http.DefaultTransport
		http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			cloned := req.Clone(req.Context())
			cloned.URL.Scheme = u.Scheme
			cloned.URL.Host = u.Host
			return origTransport.RoundTrip(cloned)
		})
		defer func() { http.DefaultTransport = origTransport }()

		client := gemini.NewClient(gemini.Config{
			APIKey: "bad-key",
			Model:  "gemini-2.5-flash",
		})
		cfg := DefaultConfig()
		cfg.Retry.MaxRetries = 3
		cfg.Retry.InitialDelay = time.Millisecond
		a := &Agent{id: "retry-gemini-400", llm: client, config: cfg}
		ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

		_, err = a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
		require.Error(t, err)
		assert.Equal(t, 1, calls)
	})

	t.Run("real Azure OpenAI client HTML 403 gateway error fails in one call", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<html><body>403 Forbidden</body></html>`))
		}))
		defer srv.Close()

		client, err := azureopenai.NewClient(azureopenai.Config{
			Endpoint:     srv.URL,
			DeploymentID: "gpt-4o",
			APIKey:       "bad-key",
		})
		require.NoError(t, err)
		cfg := DefaultConfig()
		cfg.Retry.MaxRetries = 3
		cfg.Retry.InitialDelay = time.Millisecond
		a := &Agent{id: "retry-azure-403", llm: client, config: cfg}
		ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

		_, err = a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
		require.Error(t, err)
		assert.Equal(t, 1, calls)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errorStubLLM struct {
	calls int
	err   error
}

func (s *errorStubLLM) Chat(_ context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	s.calls++
	return nil, s.err
}
func (s *errorStubLLM) Name() string  { return "err-stub" }
func (s *errorStubLLM) Model() string { return "err-model" }

func TestDispatchChat_SkipsNonRetryable4xxErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		// wantCalls is the exact provider call count. wantThrottled instead
		// asserts the throttle was waited out under the throttle budget: more
		// calls than the retry budget allows, ending in a throttle error.
		wantCalls     int
		wantThrottled bool
	}{
		{
			name:      "HTTP 400 status error",
			err:       llm.WrapHTTPStatus(http.StatusBadRequest, errors.New("API error (status 400): bad request")),
			wantCalls: 1,
		},
		{
			name:      "HTTP 401 unauthorized error",
			err:       llm.WrapHTTPStatus(http.StatusUnauthorized, errors.New("API error (status 401): invalid api key")),
			wantCalls: 1,
		},
		{
			name:      "HTTP 403 forbidden error",
			err:       llm.WrapHTTPStatus(http.StatusForbidden, errors.New("API error (status 403): forbidden")),
			wantCalls: 1,
		},
		{
			name:      "HTTP 404 not found error",
			err:       llm.WrapHTTPStatus(http.StatusNotFound, errors.New("API error (status 404): model not found")),
			wantCalls: 1,
		},
		{
			name:      "HTTP 422 unprocessable entity",
			err:       llm.WrapHTTPStatus(http.StatusUnprocessableEntity, errors.New("API error (status 422): unprocessable")),
			wantCalls: 1,
		},
		{
			name:      "OpenAI authentication error body",
			err:       llm.WrapHTTPStatus(http.StatusUnauthorized, errors.New("OpenAI API error: Incorrect API key provided (type: authentication_error)")),
			wantCalls: 1,
		},
		{
			name:      "Gemini 400 error body",
			err:       llm.WrapHTTPStatus(http.StatusBadRequest, errors.New("gemini API error: Invalid argument (code: 400)")),
			wantCalls: 1,
		},
		{
			name:      "Anthropic SDK 400 error",
			err:       fmt.Errorf("bedrock invoke failed: %w", &anthropic.Error{StatusCode: 400, Request: &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/v1/messages"}}}),
			wantCalls: 1,
		},
		{
			name:      "AWS Smithy client fault error",
			err:       &smithy.GenericAPIError{Code: "ValidationException", Message: "invalid model id", Fault: smithy.FaultClient},
			wantCalls: 1,
		},
		{
			name: "provider internal rate limiter exhausted on a 5xx is final",
			err: &llm.RetriesExhaustedError{
				Attempts: 5,
				Cause:    "transient",
				Err:      llm.NewTransientError(errors.New("API error (status 503): overloaded"), http.StatusServiceUnavailable, 0),
			},
			wantCalls: 1,
		},
		{
			name: "provider internal rate limiter exhausted on a throttle is waited out",
			err: &llm.RetriesExhaustedError{
				Attempts: 5,
				Cause:    "throttling",
				Err:      llm.NewThrottleError(errors.New("API error (status 429): rate limit"), 0),
			},
			wantThrottled: true,
		},
		{
			name:          "HTTP 429 throttle is waited out",
			err:           llm.NewThrottleError(errors.New("API error (status 429): rate limit"), time.Millisecond),
			wantThrottled: true,
		},
		{
			name:      "HTTP 408 timeout is retried",
			err:       llm.WrapHTTPStatus(http.StatusRequestTimeout, errors.New("API error (status 408): request timeout")),
			wantCalls: 3,
		},
		{
			name:      "HTTP 500 server error is retried",
			err:       llm.WrapHTTPStatus(http.StatusInternalServerError, errors.New("API error (status 500): internal error")),
			wantCalls: 3,
		},
		{
			name:      "HTTP 503 unavailable is retried",
			err:       llm.WrapHTTPStatus(http.StatusServiceUnavailable, errors.New("API error (status 503): overloaded")),
			wantCalls: 3,
		},
		{
			name:      "AWS Smithy server fault is retried",
			err:       &smithy.GenericAPIError{Code: "InternalServerException", Message: "transient", Fault: smithy.FaultServer},
			wantCalls: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &errorStubLLM{err: tt.err}
			cfg := DefaultConfig()
			cfg.Retry.MaxRetries = 2
			cfg.Retry.InitialDelay = time.Millisecond
			cfg.Retry.MaxDelay = 2 * time.Millisecond
			// Throttle waits run on a virtual clock, so the throttle budget
			// elapses at once.
			a := &Agent{id: "retry-4xx-test", llm: stub, config: cfg, throttleClock: &virtualThrottleClock{now: time.Unix(0, 0)}}
			ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

			_, err := a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
			require.Error(t, err)
			if tt.wantThrottled {
				assert.True(t, llm.IsThrottle(err), "want a throttle error, got %v", err)
				assert.Greater(t, stub.calls, cfg.Retry.MaxRetries+1)
				return
			}
			assert.Equal(t, tt.wantCalls, stub.calls)
		})
	}
}
