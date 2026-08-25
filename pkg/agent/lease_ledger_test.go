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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/scheduler"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

var (
	classNew      = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_NEW
	classInFlight = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_IN_FLIGHT
	classHolder   = loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_RESOURCE_HOLDER
)

// leaseScriptLLM returns scripted responses and records the scheduler
// priority class visible at each LLM call — the observable the lifecycle
// test asserts on (the same SlotInfo.Class the scheduler's AcquireForCall
// classifies with).
type leaseScriptLLM struct {
	mu        sync.Mutex
	responses []mockLLMResponse
	idx       int
	classes   []loomv1.SlotPriorityClass
}

func (m *leaseScriptLLM) Chat(ctx context.Context, _ []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	class := loomv1.SlotPriorityClass_SLOT_PRIORITY_CLASS_UNSPECIFIED
	if si := scheduler.SlotInfoFrom(ctx); si != nil {
		class = si.Class()
	}
	m.classes = append(m.classes, class)
	if m.idx >= len(m.responses) {
		return &LLMResponse{Content: "done", Usage: llmtypes.Usage{TotalTokens: 10}}, nil
	}
	r := m.responses[m.idx]
	m.idx++
	return &LLMResponse{
		Content:   r.content,
		ToolCalls: r.toolCalls,
		Usage:     llmtypes.Usage{InputTokens: 5, OutputTokens: 5, TotalTokens: 10},
	}, nil
}

func (m *leaseScriptLLM) Name() string  { return "lease-script" }
func (m *leaseScriptLLM) Model() string { return "lease-v1" }

func (m *leaseScriptLLM) capturedClasses() []loomv1.SlotPriorityClass {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]loomv1.SlotPriorityClass(nil), m.classes...)
}

// leaseTool acquires or releases the backend-defined lease
// "db-session"/"sess-42" via the shuttle lease-event contract, driven by the
// "op" parameter.
func leaseTool() *shuttle.MockTool {
	return &shuttle.MockTool{
		MockName:        "lease_tool",
		MockDescription: "Acquires or releases a backend session lease",
		MockExecute: func(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			res := &shuttle.Result{Success: true, Data: "ok"}
			switch params["op"] {
			case "acquire":
				shuttle.AppendLeaseEvent(res, shuttle.LeaseEvent{Action: shuttle.LeaseAcquired, Kind: "db-session", ID: "sess-42"})
			case "release":
				shuttle.AppendLeaseEvent(res, shuttle.LeaseEvent{Action: shuttle.LeaseReleased, Kind: "db-session", ID: "sess-42"})
			}
			return res, nil
		},
	}
}

func newLeaseTestAgent(llm LLMProvider) *Agent {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(leaseTool())
	return ag
}

// The full lease lifecycle across turns: a tool's LeaseAcquired lifts the
// turn's remaining LLM calls to RESOURCE_HOLDER; a following turn on the
// same session STARTS as holder (fresh SlotInfo, seeded from the ledger);
// LeaseReleased empties the ledger and the class falls back to the
// call-count progression.
func TestLeaseLifecycleAcrossTurns(t *testing.T) {
	llm := &leaseScriptLLM{responses: []mockLLMResponse{
		// Turn 1: acquire the lease, then synthesize.
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "lease_tool", Input: map[string]interface{}{"op": "acquire"}}}},
		{content: "acquired"},
		// Turn 2: no tools — the first (only) LLM call probes the seed.
		{content: "still holding"},
		// Turn 3: release the lease, then synthesize.
		{toolCalls: []llmtypes.ToolCall{{ID: "c2", Name: "lease_tool", Input: map[string]interface{}{"op": "release"}}}},
		{content: "released"},
		// Turn 4: no tools — nothing seeds after the release.
		{content: "not holding"},
	}}
	ag := newLeaseTestAgent(llm)
	const sessionID = "lease-lifecycle-session"

	// Turn 1: fresh conversation, fresh SlotInfo (as the server installs
	// per turn: priorCalls=0 on an unresumed session).
	ctx1 := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)
	_, err := ag.Chat(ctx1, sessionID, "acquire the lease")
	require.NoError(t, err)
	classes := llm.capturedClasses()
	require.Len(t, classes, 2)
	assert.Equal(t, classNew, classes[0], "first call of a fresh conversation classifies NEW")
	assert.Equal(t, classHolder, classes[1], "the call after LeaseAcquired classifies RESOURCE_HOLDER")
	si1 := scheduler.SlotInfoFrom(ctx1)
	require.NotNil(t, si1)
	assert.Equal(t, classHolder, si1.Class(), "the turn's SlotInfo stays marked after the turn")
	assert.True(t, ag.leases.holds(sessionID))

	// Turn 2: a FRESH SlotInfo — the mark died with turn 1's. The session's
	// ledger seeds the new turn, so its first call classifies RESOURCE_HOLDER
	// (without the seed it would classify IN_FLIGHT via priorCalls).
	ctx2 := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 1)
	_, err = ag.Chat(ctx2, sessionID, "keep working")
	require.NoError(t, err)
	classes = llm.capturedClasses()
	require.Len(t, classes, 3)
	assert.Equal(t, classHolder, classes[2], "a turn on a lease-holding session STARTS as RESOURCE_HOLDER")

	// Turn 3: the tool releases the lease mid-turn. The turn starts seeded
	// (the session still held the lease), then the ledger empties and the
	// synthesis call falls back to IN_FLIGHT.
	ctx3 := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 1)
	_, err = ag.Chat(ctx3, sessionID, "release the lease")
	require.NoError(t, err)
	classes = llm.capturedClasses()
	require.Len(t, classes, 5)
	assert.Equal(t, classHolder, classes[3], "still holding at turn start")
	assert.Equal(t, classInFlight, classes[4], "after LeaseReleased the class falls back to IN_FLIGHT")
	assert.False(t, ag.leases.holds(sessionID), "the ledger must be empty after the release")

	// Turn 4: nothing seeds — a released session schedules like any other.
	ctx4 := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 1)
	_, err = ag.Chat(ctx4, sessionID, "carry on")
	require.NoError(t, err)
	classes = llm.capturedClasses()
	require.Len(t, classes, 6)
	assert.Equal(t, classInFlight, classes[5], "a lease-free resumed turn classifies IN_FLIGHT")
}

// A brand-new session on the same agent is unaffected by another session's
// leases, and classifies NEW on its first call.
func TestLeaseSeedingIsPerSession(t *testing.T) {
	llm := &leaseScriptLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "lease_tool", Input: map[string]interface{}{"op": "acquire"}}}},
		{content: "acquired"},
		{content: "other session"},
	}}
	ag := newLeaseTestAgent(llm)

	ctx1 := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)
	_, err := ag.Chat(ctx1, "holder-session", "acquire the lease")
	require.NoError(t, err)
	require.True(t, ag.leases.holds("holder-session"))

	ctx2 := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)
	_, err = ag.Chat(ctx2, "fresh-session", "hello")
	require.NoError(t, err)
	classes := llm.capturedClasses()
	require.Len(t, classes, 3)
	assert.Equal(t, classNew, classes[2], "another session must not inherit the holder's class")
	assert.False(t, ag.leases.holds("fresh-session"))
}

// Session deletion retires the session's leases — a leaked ledger entry on a
// deleted session would pin RESOURCE_HOLDER priority forever.
func TestDeleteSessionRetiresLeases(t *testing.T) {
	llm := &leaseScriptLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "lease_tool", Input: map[string]interface{}{"op": "acquire"}}}},
		{content: "acquired"},
	}}
	ag := newLeaseTestAgent(llm)
	const sessionID = "lease-delete-session"

	ctx := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)
	_, err := ag.Chat(ctx, sessionID, "acquire the lease")
	require.NoError(t, err)
	require.True(t, ag.leases.holds(sessionID))

	ag.DeleteSession(sessionID)
	assert.False(t, ag.leases.holds(sessionID))
	ag.leases.mu.Lock()
	_, leaked := ag.leases.held[sessionID]
	ag.leases.mu.Unlock()
	assert.False(t, leaked, "DeleteSession must remove the ledger entry, not just empty it")
}

// ClearAllSessions is DeleteSession's sibling retirement path.
func TestClearAllSessionsRetiresLeases(t *testing.T) {
	ag := newLeaseTestAgent(&leaseScriptLLM{})
	ag.leases.apply("s1", []shuttle.LeaseEvent{{Action: shuttle.LeaseAcquired, Kind: "db-session", ID: "a"}})
	ag.leases.apply("s2", []shuttle.LeaseEvent{{Action: shuttle.LeaseAcquired, Kind: "db-session", ID: "b"}})

	ag.ClearAllSessions()
	assert.False(t, ag.leases.holds("s1"))
	assert.False(t, ag.leases.holds("s2"))
}

func TestLeaseLedgerApply(t *testing.T) {
	acquire := func(kind, id string) shuttle.LeaseEvent {
		return shuttle.LeaseEvent{Action: shuttle.LeaseAcquired, Kind: kind, ID: id}
	}
	release := func(kind, id string) shuttle.LeaseEvent {
		return shuttle.LeaseEvent{Action: shuttle.LeaseReleased, Kind: kind, ID: id}
	}

	tests := []struct {
		name   string
		events []shuttle.LeaseEvent
		want   bool
	}{
		{
			name:   "acquire holds",
			events: []shuttle.LeaseEvent{acquire("db-session", "s1")},
			want:   true,
		},
		{
			name:   "acquire then release empties",
			events: []shuttle.LeaseEvent{acquire("db-session", "s1"), release("db-session", "s1")},
			want:   false,
		},
		{
			name:   "re-acquire is idempotent",
			events: []shuttle.LeaseEvent{acquire("db-session", "s1"), acquire("db-session", "s1"), release("db-session", "s1")},
			want:   false,
		},
		{
			name:   "double release is a tolerated no-op",
			events: []shuttle.LeaseEvent{acquire("db-session", "s1"), release("db-session", "s1"), release("db-session", "s1")},
			want:   false,
		},
		{
			name:   "release of unknown is a tolerated no-op",
			events: []shuttle.LeaseEvent{release("db-session", "never-acquired")},
			want:   false,
		},
		{
			name:   "release then acquire still holds",
			events: []shuttle.LeaseEvent{release("db-session", "s1"), acquire("db-session", "s1")},
			want:   true,
		},
		{
			name:   "one of two released still holds",
			events: []shuttle.LeaseEvent{acquire("db-session", "s1"), acquire("api-slot", "7"), release("db-session", "s1")},
			want:   true,
		},
		{
			name:   "identity is the exact kind+id pair",
			events: []shuttle.LeaseEvent{acquire("db-session", "s1"), release("other-kind", "s1")},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var l leaseLedger
			got := l.apply("sess", tt.events)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.want, l.holds("sess"))
		})
	}
}

// Concurrent tool results emitting lease events on the same session: each
// worker acquires and releases its own lease; the ledger must end empty with
// no double-count drift and no data race (run under -race).
func TestLeaseLedgerConcurrentToolResults(t *testing.T) {
	ag := &Agent{} // zero-value ledger is ready; no other agent state is touched
	ctx := scheduler.WithSlotInfo(context.Background(), loomv1.SlotOrigin_SLOT_ORIGIN_BATCH, 0)
	const sessionID = "race-session"
	const workers = 32

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("lease-%d", n)
			acq := &shuttle.Result{Success: true}
			shuttle.AppendLeaseEvent(acq, shuttle.LeaseEvent{Action: shuttle.LeaseAcquired, Kind: "db-session", ID: id})
			ag.applyLeaseEvents(ctx, sessionID, acq)
			ag.seedLeaseHolding(ctx, sessionID)
			rel := &shuttle.Result{Success: true}
			shuttle.AppendLeaseEvent(rel, shuttle.LeaseEvent{Action: shuttle.LeaseReleased, Kind: "db-session", ID: id})
			ag.applyLeaseEvents(ctx, sessionID, rel)
		}(i)
	}
	wg.Wait()

	assert.False(t, ag.leases.holds(sessionID), "every acquire was matched by a release")
}

// Without SlotInfo (unwired path) lease events still track in the ledger and
// the scheduler calls are no-ops — nothing panics, nothing leaks.
func TestApplyLeaseEventsWithoutSlotInfo(t *testing.T) {
	ag := &Agent{}
	res := &shuttle.Result{Success: true}
	shuttle.AppendLeaseEvent(res, shuttle.LeaseEvent{Action: shuttle.LeaseAcquired, Kind: "db-session", ID: "s1"})
	ag.applyLeaseEvents(context.Background(), "bare", res)
	assert.True(t, ag.leases.holds("bare"))
	ag.seedLeaseHolding(context.Background(), "bare")
}
