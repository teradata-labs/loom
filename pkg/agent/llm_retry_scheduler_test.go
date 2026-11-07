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
	"net/url"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm"
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

	stub.throttle = true
	_, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
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
		name      string
		err       error
		wantCalls int
	}{
		{
			name:      "HTTP 400 status string",
			err:       errors.New("API error (status 400): bad request"),
			wantCalls: 1,
		},
		{
			name:      "HTTP 401 unauthorized string",
			err:       errors.New("API error (status 401): invalid api key"),
			wantCalls: 1,
		},
		{
			name:      "HTTP 403 forbidden string",
			err:       errors.New("API error (status 403): forbidden"),
			wantCalls: 1,
		},
		{
			name:      "HTTP 404 not found string",
			err:       errors.New("API error (status 404): model not found"),
			wantCalls: 1,
		},
		{
			name:      "HTTP 422 unprocessable entity",
			err:       errors.New("API error (status 422): unprocessable"),
			wantCalls: 1,
		},
		{
			name:      "OpenAI authentication error body",
			err:       errors.New("OpenAI API error: Incorrect API key provided (type: invalid_request_error, code: invalid_api_key)"),
			wantCalls: 1,
		},
		{
			name:      "Gemini 400 error body",
			err:       errors.New("gemini API error: Invalid argument (code: 400, status: INVALID_ARGUMENT)"),
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
			name:      "provider internal rate limiter already exhausted retries",
			err:       errors.New("exceeded 5 retries due to throttling: API error (status 429): rate limit"),
			wantCalls: 1,
		},
		{
			name:      "HTTP 429 throttle is retried",
			err:       llm.NewThrottleError(errors.New("API error (status 429): rate limit"), time.Millisecond),
			wantCalls: 3,
		},
		{
			name:      "HTTP 408 timeout is retried",
			err:       errors.New("API error (status 408): request timeout"),
			wantCalls: 3,
		},
		{
			name:      "HTTP 500 server error is retried",
			err:       errors.New("API error (status 500): internal error"),
			wantCalls: 3,
		},
		{
			name:      "HTTP 503 unavailable is retried",
			err:       errors.New("API error (status 503): overloaded"),
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
			a := &Agent{id: "retry-4xx-test", llm: stub, config: cfg}
			ctx := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}

			_, err := a.dispatchChat(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
			require.Error(t, err)
			assert.Equal(t, tt.wantCalls, stub.calls)
		})
	}
}

