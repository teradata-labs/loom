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

// D-1 (Contract 2 core: single-writer pressure pipeline) acceptance tests.
//
// These tests drive the real pkg/agent runtime — Agent.prepareContext, SegmentedMemory,
// and (for the full-loop case) Agent.Chat — through an injectable LLMProvider, never a
// Claude SDK mock. They assert:
//   - below-yellow-no-mutation:    AddMessage/ReplayMessages/prepareContext never compress
//                                  or evict while budget usage stays under the yellow zone.
//   - single-writer-both-sites:    across a full conversation loop that hits both LLM-bound
//                                  call sites, nothing but prepareContext's dispatch ever
//                                  touches L1/L2.
//   - zone-defaults-overridable:   yellow=70%/red=85% by default, overridable via the
//                                  compression profile carried through agent config.
//   - unknown-window-fallback:     when the token window is unknown (GetUsage's total==0),
//                                  the zone thresholds fall back to the compression profile's
//                                  warning/critical thresholds.
//   - pressure-paths-pure:         AddMessage, ReplayMessages, and recoverOutputTokenCB no
//                                  longer mutate memory under budget pressure.
//   - trim-confined-reset-context: AggressiveTrim/TrimLastN's effects are only reachable via
//                                  Agent.ResetSessionContext (reset_context).
//
// These tests bind to the seam this story defines (LLD "Contracts" section):
//   - Agent.prepareContext(ctx, session) ([]Message, error)
//   - SegmentedMemory.BudgetPct() float64
//   - SegmentedMemory.ValveEvict(ctx) / Fold(ctx, ledgerUserTurns, flatLen int) error — D-1
//     declared this surface; ValveEvict's body is D-4's, Fold's is D-5's (see
//     valve_evict_test.go / fold_test.go for their full behavioral coverage)
//   - userLedgerCount(session) int — D-1 declared this surface; D-5 fills its body
//   - SegmentedMemory.ZoneThresholds() (yellowPct, redPct float64) — resolves the configured/
//     default yellow=70/red=85 zone, applying the unknown-window fallback to the compression
//     profile's WarningThresholdPercent/CriticalThresholdPercent
//   - CompressionProfile.YellowThresholdPercent / RedThresholdPercent int — the two new,
//     profile-flat (not workload-varying), agent-config-overridable knobs backing the above

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// seedL1TokenPressure force-sets sm's cached L1 token usage (bypassing the real tokenizer)
// so pressure-path tests can deterministically land in a specific budget zone without
// depending on the tokenizer's byte-to-token ratio for a giant block of filler text. It only
// touches the token-accounting cache, not sm.l1Messages, so message-count assertions made by
// the caller before/after the real call under test stay meaningful.
func seedL1TokenPressure(sm *SegmentedMemory, tokens int) {
	sm.cachedL1Tokens = tokens
	sm.tokenCountDirty = false
	sm.updateTokenCount()
}

// --- below-yellow-no-mutation ---

func TestPrepareContext_BelowYellow_AddMessage_NoMutation(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom content", 200000, 20000)

	for i := 0; i < 40; i++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("short message %d", i)})
	}

	require.Less(t, sm.BudgetPct(), 70.0, "test setup: must stay below yellow to exercise the invariant")
	assert.Equal(t, 40, len(sm.l1Messages), "AddMessage must never compress or evict below the yellow zone")
	assert.Empty(t, sm.l2Summary, "AddMessage must never populate L2 below the yellow zone")
}

func TestPrepareContext_BelowYellow_ReplayMessages_NoMutation(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom content", 200000, 20000)

	msgs := make([]Message, 100)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: fmt.Sprintf("restored message %d", i)}
	}
	sm.ReplayMessages(ctx, msgs)

	require.Less(t, sm.BudgetPct(), 70.0, "test setup: a large restore must still stay below yellow to exercise the invariant")
	assert.Equal(t, 100, len(sm.l1Messages), "ReplayMessages must never compress or evict below the yellow zone")
	assert.Empty(t, sm.l2Summary, "ReplayMessages must never populate L2 below the yellow zone")
}

func TestPrepareContext_BelowYellow_DispatchIsNoOp(t *testing.T) {
	ctx := context.Background()
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	sm := NewSegmentedMemory("rom content", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "hi"})
	require.Less(t, sm.BudgetPct(), 70.0, "test setup: must stay below yellow")

	session := &Session{ID: "below-yellow-session", SegmentedMem: sm}
	before := session.GetMessages() // routes through GetMessagesForLLM — the same assembler read prepareContext returns

	out, err := ag.prepareContext(ctx, session)

	require.NoError(t, err)
	assert.Equal(t, before, out, "prepareContext must return the assembler read unchanged below yellow")
	assert.Equal(t, 1, len(sm.l1Messages))
	assert.Empty(t, sm.l2Summary)
}

// --- single-writer-both-sites ---

// TestPrepareContext_FullLoop_BothCallSites_SingleWriter drives the real conversation loop
// end-to-end via Agent.Chat — the real pkg/agent/cmd/looms runtime with an injectable
// LLMProvider (mockToolCallingLLM), never a Claude SDK mock (gates-drive-looms-runtime).
// alwaysCallTools never emits end_turn, so the loop is guaranteed to exhaust MaxTurns and
// reach BOTH LLM-bound call sites: the per-turn loop call and the max-turns synthesis call.
// Across both, nothing but prepareContext's (stubbed, no-op in D-1) dispatch may touch L1/L2 —
// the deleted per-message/restore/pressure compressors must never fire at either site.
func TestPrepareContext_FullLoop_BothCallSites_SingleWriter(t *testing.T) {
	backend := &mockBackend{}
	llm := &mockToolCallingLLM{alwaysCallTools: true}

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxTurns = 2
	cfg.MaxToolExecutions = 50

	ag := NewAgent(backend, llm, WithConfig(cfg))
	ag.RegisterTool(&mockCalculatorTool{})

	ctx := context.Background()
	resp, err := ag.Chat(ctx, "single-writer-session", "please help with something")
	require.NoError(t, err)
	require.NotNil(t, resp)

	session, ok := ag.memory.GetSession("single-writer-session")
	require.True(t, ok)
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	assert.Empty(t, segMem.GetL2Summary(),
		"no automatic compression may fire at either LLM-bound call site — prepareContext is the only writer, and its dispatch is a no-op stub in D-1")
	assert.GreaterOrEqual(t, segMem.GetL1MessageCount(), cfg.MaxTurns*2,
		"the loop must have actually exercised both the turn loop and the synthesis call")
}

// --- zone-defaults-overridable ---

func TestZoneThresholds_DefaultsSeventyEightyFive(t *testing.T) {
	sm := NewSegmentedMemory("rom", 200000, 20000) // balanced profile, no override

	yellow, red := sm.ZoneThresholds()

	assert.Equal(t, 70.0, yellow, "yellow zone must default to 70pct regardless of workload profile")
	assert.Equal(t, 85.0, red, "red zone must default to 85pct regardless of workload profile")
}

func TestZoneThresholds_OverridableViaCompressionProfile(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	profile.YellowThresholdPercent = 55
	profile.RedThresholdPercent = 90

	sm := NewSegmentedMemoryWithCompression("rom", 200000, 20000, profile)

	yellow, red := sm.ZoneThresholds()
	assert.Equal(t, 55.0, yellow)
	assert.Equal(t, 90.0, red)
}

func TestZoneThresholds_OverridableViaAgentConfig(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	profile.YellowThresholdPercent = 42
	profile.RedThresholdPercent = 66

	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithCompressionProfile(&profile))
	session := ag.memory.GetOrCreateSession(context.Background(), "zone-cfg-session")
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	yellow, red := segMem.ZoneThresholds()
	assert.Equal(t, 42.0, yellow, "the agent-config compression profile override must reach the session's SegmentedMemory")
	assert.Equal(t, 66.0, red)
}

// --- unknown-window-fallback ---

func TestZoneThresholds_UnknownWindowFallsBackToProfileThresholds(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED] // Warning=60, Critical=75
	sm := NewSegmentedMemoryWithCompression("rom", 200000, 20000, profile)

	// Simulate an unknown token window/basis: GetUsage()'s total collapses to 0.
	sm.tokenBudget = NewTokenBudget(0, 0)
	_, _, total := sm.GetTokenBudgetUsage()
	require.Zero(t, total, "test setup: the window must genuinely be unknown (total==0)")

	yellow, red := sm.ZoneThresholds()

	assert.Equal(t, float64(profile.WarningThresholdPercent), yellow,
		"unknown window must fall back to the profile's warning threshold, not the configured/default yellow zone")
	assert.Equal(t, float64(profile.CriticalThresholdPercent), red,
		"unknown window must fall back to the profile's critical threshold, not the configured/default red zone")
}

// --- pressure-paths-pure ---

func TestAddMessage_NeverCompressesRegardlessOfBudgetPressure(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 1000, 0) // 1000 tokens available
	seedL1TokenPressure(sm, 900)             // simulate L1 already critical without giant filler text
	require.Greater(t, sm.BudgetPct(), 85.0, "test setup: must land in the red zone")

	sm.AddMessage(ctx, Message{Role: "user", Content: "one more turn while critical"})

	assert.Equal(t, 1, len(sm.l1Messages), "AddMessage is pure admission — it must never compress, even under critical pressure")
	assert.Empty(t, sm.l2Summary, "AddMessage must never populate L2 — compression now lives solely behind prepareContext's stubbed dispatch")
}

func TestReplayMessages_NeverCompressesRegardlessOfBudgetPressure(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 1000, 0)
	seedL1TokenPressure(sm, 900)
	require.Greater(t, sm.BudgetPct(), 85.0, "test setup: must land in the red zone")

	msgs := make([]Message, 25)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: fmt.Sprintf("restored message %d", i)}
	}
	sm.ReplayMessages(ctx, msgs)

	assert.Equal(t, 25, len(sm.l1Messages), "ReplayMessages is a pure bulk-load — it must never compress, even under critical pressure")
	assert.Empty(t, sm.l2Summary, "ReplayMessages must never populate L2 — compression now lives solely behind prepareContext's stubbed dispatch")
}

// TestPrepareContext_RedZoneDispatch_InvokesRealFold covers the D-1 seam's routing
// responsibility now that D-5 has filled Fold's body: prepareContext must dispatch to Fold
// (not ValveEvict, not a no-op) once budget usage reaches the red zone, and a fold that
// doesn't trip the breaker must never surface an error. Fold's own behavior (carry
// partitioning, breaker, residue, persistence) is covered exhaustively by fold_test.go /
// fold_capstone_test.go (D-5) — this test only pins the zone-dispatch wiring.
func TestPrepareContext_RedZoneDispatch_InvokesRealFold(t *testing.T) {
	ctx := context.Background()
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	sm := NewSegmentedMemory("rom", 1000, 0)
	sm.SetCompressor(&mockCompressor{enabled: true})
	sm.AddMessage(ctx, Message{Role: "user", Content: "already in context"})
	seedL1TokenPressure(sm, 900)
	require.Greater(t, sm.BudgetPct(), 85.0, "test setup: must land in the red zone")

	session := &Session{ID: "red-zone-session", SegmentedMem: sm}

	out, err := ag.prepareContext(ctx, session)

	require.NoError(t, err, "a fold that does not trip the breaker must never error")
	assert.NotEmpty(t, sm.GetL2Summary(), "the red-zone dispatch must have invoked Fold, which always writes at least the recall pointer")
	assert.Equal(t, out, session.GetMessages(), "prepareContext must return the same assembler read GetMessages exposes")
}

func TestRecoverOutputTokenCB_NoLongerTrimsMemory(t *testing.T) {
	_, span := observability.NewNoOpTracer().StartSpan(context.Background(), "test")
	recovery := newRecoveryOrchestrator(nil, span)

	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 200000, 20000)
	for i := 0; i < 10; i++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("m%d", i)})
	}
	session := &Session{ID: "cb-session", SegmentedMem: sm, Messages: make([]Message, 10)}
	tracker := newConsecutiveFailureTracker()
	for i := 0; i < 8; i++ {
		tracker.recordOutputTokenExhaustion(true)
	}

	recovered, err := recovery.recoverOutputTokenCB(ctx, session, sm, tracker, 8)

	require.NoError(t, err)
	assert.True(t, recovered)
	assert.Equal(t, 0, tracker.outputTokenExhaustions, "the failure tracker must still be reset")

	// The class-blind trim is gone: memory only grows by the recovery nudge, never a trim.
	assert.Equal(t, 11, len(sm.l1Messages), "recoverOutputTokenCB must no longer trim segmented memory — only admit the recovery nudge")
	assert.Equal(t, 11, len(session.Messages), "the flat list must gain only the recovery nudge, never a trim")
	nudge := session.Messages[len(session.Messages)-1]
	assert.Equal(t, "user", nudge.Role)
	assert.Contains(t, nudge.Content, "Simplify your approach")
}

// --- trim-confined-reset-context ---

func TestResetSessionContext_ClearsFlatMessageList(t *testing.T) {
	ctx := context.Background()
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	session := ag.memory.GetOrCreateSession(ctx, "reset-session")
	for i := 0; i < 10; i++ {
		session.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("m%d", i)})
	}
	require.NotEmpty(t, session.Messages, "test setup: the flat list must be populated before reset")

	ok := ag.ResetSessionContext("reset-session")
	require.True(t, ok)

	assert.Empty(t, session.Messages,
		"reset_context must clear the flat Messages list too — AggressiveTrim's session.TrimLastN(0) sync is wired into the reset path")
	segMem, isSegMem := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, isSegMem)
	assert.Equal(t, 0, segMem.GetL1MessageCount())
}

// --- Fold/ValveEvict/userLedgerCount seam contracts (D-1 declared the surface; ValveEvict's
// body is D-4's, Fold's and userLedgerCount's are D-5's — full behavioral coverage for both
// lives in valve_evict_test.go and fold_test.go/fold_capstone_test.go respectively). These
// tests pin only the narrow D-1-level contracts: the wrapper's signature and delegation. ---

func TestUserLedgerCount_CountsOnlyLedgerClassUserMessages(t *testing.T) {
	session := &Session{
		ID: "ledger-session",
		Messages: []Message{
			{Role: "user", Content: "one"}, // unclassified (narrative-default) — not counted
			{Role: "assistant", Content: "two"},
			{Role: "user", Content: "three", ContextClass: ClassLedger},
		},
	}

	assert.Equal(t, 1, userLedgerCount(session), "userLedgerCount must count only Role:user messages classed ledger, delegating to the same countLedgerUsers basis Fold's breaker uses")
}

func TestValveEvict_NoStoreConfigured_NoOp(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "hello"})
	before := sm.GetMessages()
	beforeL2 := sm.GetL2Summary()

	sm.ValveEvict(ctx) // no SetSessionStore call: a stub would be unrecoverable, so the valve disables itself (C-022)

	assert.Equal(t, before, sm.GetMessages(), "with no durable session store wired, ValveEvict must evict nothing")
	assert.Equal(t, beforeL2, sm.GetL2Summary())
}
