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
	"time"

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
			llm := &usageStubLLM{name: fmt.Sprintf("metered-stub-%d", i), usage: u}
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
