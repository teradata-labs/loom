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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
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
