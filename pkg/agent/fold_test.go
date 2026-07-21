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

// D-5 (Fold, breaker & restore, Part B fold + Part C) — Seam 1
// (SegmentedMemory.Fold + breaker) and Seam 2 (CompactMemory -> Fold)
// component acceptance tests.
//
// These tests drive the real pkg/agent runtime — SegmentedMemory.Fold, CompactMemory, and
// (for the persistence-facing cases) a real in-process SQLite SessionStore (temp file, no
// server), following the same pattern D-3/D-4's own suites established
// (session_store_context_class_test.go, valve_evict_test.go). No Claude SDK mock, no fake
// LLM server: MemoryCompressor is the documented pluggable-backend seam (LLD "Contracts"),
// so a test double implementing it is ordinary dependency injection, not a mocked harness.
//
// They assert:
//   - pre-fold-transcript-recoverable-no-copy: flat[:foldIndex] recovers the pre-fold L1
//     transcript from the durable messages rows; Fold never writes a second copy.
//   - carry-set-charter-ledger-adjacency-closure-first-user: the carry set is exactly
//     charter + ledger + ledger-user adjacency + tool-pair closure, in original order,
//     starting with a user message.
//   - property-random-toolpair-100pct-api-valid: over randomized tool-pair layouts, every
//     fold produces an API-valid carried sequence (zero orphaned tool_results).
//   - ballast-valve-evict-no-payoff-narrative-compress-once: remaining ballast is dropped
//     unconditionally (no payoff bar, unlike ValveEvict) and remaining narrative is
//     compressed exactly once.
//   - residue-structured-summary-recall-pointer-heuristic-fallback-logged: the residue
//     prefers a StrictMemoryCompressor, receives only narrative leftovers (never charter or
//     ledger), carries the recall pointer, and falls back to a heuristic summary with a
//     logged degraded event on compressor error or absence.
//   - persist-residue-foldindex-durable-carry-not-persisted: exactly {residue, foldIndex} is
//     persisted to the l2_summary snapshot; the carry set itself is never persisted.
//   - breaker-third-fold-within-3-turns-reset-context: a third fold within 3 ledger-user
//     turns of the previous fold returns a *RecoverableError (reset_context) instead of
//     folding, and the session-memory-tool route (CompactMemory -> Fold) shares the same
//     breaker counter (SC-005).

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- shared helpers ---

// toolPairMessages builds an assistant tool_use message and its matching tool_result,
// wired by ToolUseID, without admitting or persisting them — callers assemble a raw flat
// history or admit the pair themselves.
func toolPairMessages(toolUseID, toolName, content string, class ContextClass) (assistant, result Message) {
	assistant = Message{Role: "assistant", ToolCalls: []ToolCall{{ID: toolUseID, Name: toolName}}, Timestamp: time.Now()}
	result = Message{Role: "tool", Content: content, ToolUseID: toolUseID, ContextClass: class, Timestamp: time.Now()}
	return assistant, result
}

// assertAPIValidCarry asserts carry is a structurally valid sequence for an Anthropic/
// Bedrock-style API call (SC-004): non-empty, starting with a user message, with no
// orphaned tool_result and no assistant tool_call left without its tool_result.
func assertAPIValidCarry(t *testing.T, carry []Message) {
	t.Helper()
	if len(carry) == 0 {
		t.Fatal("carry must never be empty — every fold must produce a non-empty, API-valid sequence")
	}
	if carry[0].Role != "user" {
		t.Fatalf("carry[0] must be a user message, got role %q", carry[0].Role)
	}

	assistantCallIDs := make(map[string]bool)
	for _, msg := range carry {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				assistantCallIDs[tc.ID] = true
			}
		}
	}
	resultPresent := make(map[string]bool)
	for _, msg := range carry {
		if msg.Role == "tool" && msg.ToolUseID != "" {
			resultPresent[msg.ToolUseID] = true
			if !assistantCallIDs[msg.ToolUseID] {
				t.Fatalf("orphaned tool_result: ToolUseID %q has no issuing assistant tool_call in the carry", msg.ToolUseID)
			}
		}
	}
	for id := range assistantCallIDs {
		if !resultPresent[id] {
			t.Fatalf("assistant tool_call %q has no matching tool_result in the carry", id)
		}
	}
}

// --- pre-fold-transcript-recoverable-no-copy ---

func TestFold_PreFoldTranscript_RecoverableFromDurableRows_NoSeparateCopy(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	sessionID := "fold-transcript-recover"
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	sm := NewSegmentedMemory("rom", 200000, 0)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&mockCompressor{enabled: true})

	preFold := []Message{
		{Role: "user", Content: "M1 kickoff", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "M2 plan", Timestamp: time.Now()},
	}
	a3, r3 := toolPairMessages("t-charter", "manage_skills", "M3 skill body", ClassCharter)
	preFold = append(preFold, a3, r3)
	a4, r4 := toolPairMessages("t-ballast", "query_data", "M5 ballast discovery", ClassBallast)
	preFold = append(preFold, a4, r4)
	preFold = append(preFold, Message{Role: "user", Content: "M7 approved", ContextClass: ClassLedger, Timestamp: time.Now()})

	for _, m := range preFold {
		sm.AddMessage(ctx, m)
		require.NoError(t, store.SaveMessage(ctx, sessionID, m))
	}

	flatLen := len(preFold)
	require.NoError(t, sm.Fold(ctx, 0, flatLen))

	// No separate transcript copy: Fold never calls SaveMessage — the durable
	// messages table has exactly the rows that were admitted pre-fold.
	durable, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, durable, flatLen, "Fold must not write any additional durable message rows")

	for i, m := range preFold {
		assert.Equal(t, m.Role, durable[i].Role)
		assert.Equal(t, m.Content, durable[i].Content)
		assert.Equal(t, m.ContextClass, durable[i].ContextClass)
		assert.Equal(t, m.ToolUseID, durable[i].ToolUseID)
	}

	snaps, err := store.LoadMemorySnapshots(ctx, sessionID, "l2_summary", 0)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	var rec foldRecord
	require.NoError(t, json.Unmarshal([]byte(snaps[0].Content), &rec))
	assert.Equal(t, flatLen, rec.FoldIndex, "foldIndex must equal the flat durable length at fold time")

	// flat[:foldIndex] reproduces the pre-fold transcript exactly.
	recovered := durable[:rec.FoldIndex]
	require.Len(t, recovered, len(preFold))
	for i, m := range preFold {
		assert.Equal(t, m.Content, recovered[i].Content)
	}

	// The residue is a pointer, not a duplicated transcript: it must name the
	// recall pointer, and must never embed any pre-fold message's content
	// verbatim (a size-independent check — unlike a raw byte-length
	// comparison, this holds regardless of how small or large the pre-fold
	// transcript happens to be).
	assert.Contains(t, rec.Residue, fmt.Sprintf("recall_context('fold:%d')", flatLen))
	for _, m := range preFold {
		if m.Content == "" {
			continue
		}
		assert.NotContains(t, rec.Residue, m.Content, "the residue must never embed a pre-fold message's content verbatim — it is a pointer, not a copy")
	}
}

// --- carry-set-charter-ledger-adjacency-closure-first-user ---

func TestFold_CarrySet_CharterLedgerAdjacencyClosure_FirstMessageUser(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 200000, 0)
	sm.SetCompressor(&mockCompressor{enabled: true})

	m1 := Message{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()}
	m2 := Message{Role: "assistant", Content: "M2", Timestamp: time.Now()} // pulled via adjacency to m3
	m3 := Message{Role: "user", Content: "M3", ContextClass: ClassLedger, Timestamp: time.Now()}
	m4, m5 := toolPairMessages("t-charter", "manage_skills", "M5 skill", ClassCharter) // m4 pulled via closure
	a6 := Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "t2", Name: "query_data"}, {ID: "t3", Name: "query_data"}}, Timestamp: time.Now()}
	m7 := Message{Role: "tool", Content: "M7 ballast", ToolUseID: "t2", ContextClass: ClassBallast, Timestamp: time.Now()}
	m8 := Message{Role: "tool", Content: "M8 ballast", ToolUseID: "t3", ContextClass: ClassBallast, Timestamp: time.Now()}
	m9 := Message{Role: "user", Content: "M9", ContextClass: ClassLedger, Timestamp: time.Now()} // preceded by a "tool" message — no adjacency
	m10 := Message{Role: "assistant", Content: "M10 analysis", Timestamp: time.Now()}

	flat := []Message{m1, m2, m3, m4, m5, a6, m7, m8, m9, m10}
	for _, m := range flat {
		sm.AddMessage(ctx, m)
	}

	require.NoError(t, sm.Fold(ctx, 0, len(flat)))

	got := sm.GetMessages()
	wantContents := []string{"M1", "M2", "M3", "", "M5 skill", "M9"}
	require.Len(t, got, len(wantContents), "carry must be exactly charter+ledger+adjacency+closure")
	assert.Equal(t, "user", got[0].Role, "carry must start with a user message")
	for i := range wantContents {
		assert.Equal(t, wantContents[i], got[i].Content, "carry[%d] content mismatch", i)
	}
	assertAPIValidCarry(t, got)

	for _, m := range got {
		assert.NotEqual(t, "t2", m.ToolUseID, "the ballast tool-pair (a6/m7/m8) must never survive a fold")
		assert.NotEqual(t, "t3", m.ToolUseID, "the ballast tool-pair (a6/m7/m8) must never survive a fold")
		assert.NotEqual(t, "M10 analysis", m.Content, "narrative not adjacent to a ledger user must never survive a fold")
	}
}

// --- property-random-toolpair-100pct-api-valid ---

// randomFoldFlatHistory builds a randomized flat message history: it always starts with a
// ledger-class user message (M1, matching the design's standing invariant that a session
// always opens on a ledger user turn) and then appends a random mix of plain ledger-user
// turns, tool-less narrative assistant turns, and tool-call turns (1-3 tool_results under
// one assistant, each independently classed charter/ledger/ballast) — the randomized
// "tool-pair layouts" the property test drives Fold's carry partition across.
func randomFoldFlatHistory(rng *rand.Rand, steps int) []Message {
	flat := []Message{{Role: "user", Content: "kickoff", ContextClass: ClassLedger, Timestamp: time.Now()}}
	toolSeq := 0
	classes := []ContextClass{ClassCharter, ClassLedger, ClassBallast}

	for i := 0; i < steps; i++ {
		switch rng.Intn(5) {
		case 0:
			flat = append(flat, Message{Role: "user", Content: fmt.Sprintf("turn %d", i), ContextClass: ClassLedger, Timestamp: time.Now()})
		case 1:
			flat = append(flat, Message{Role: "assistant", Content: fmt.Sprintf("analysis %d", i), Timestamp: time.Now()})
		default:
			n := 1 + rng.Intn(3)
			calls := make([]ToolCall, n)
			results := make([]Message, n)
			for j := 0; j < n; j++ {
				toolSeq++
				id := fmt.Sprintf("t%d", toolSeq)
				calls[j] = ToolCall{ID: id, Name: "some_tool"}
				results[j] = Message{Role: "tool", Content: fmt.Sprintf("result %d", toolSeq), ToolUseID: id, ContextClass: classes[rng.Intn(len(classes))], Timestamp: time.Now()}
			}
			flat = append(flat, Message{Role: "assistant", ToolCalls: calls, Timestamp: time.Now()})
			flat = append(flat, results...)
		}
	}
	return flat
}

func TestFold_Property_RandomToolPairLayouts_AlwaysAPIValid(t *testing.T) {
	const iterations = 300
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < iterations; iter++ {
		flat := randomFoldFlatHistory(rng, 1+rng.Intn(30))
		t.Run(fmt.Sprintf("iter-%d", iter), func(t *testing.T) {
			carry := computeCarry(flat)
			assertAPIValidCarry(t, carry)
		})
	}
}

func TestFold_Property_RealFold_MatchesComputeCarry(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 25; iter++ {
		flat := randomFoldFlatHistory(rng, 1+rng.Intn(20))
		t.Run(fmt.Sprintf("iter-%d", iter), func(t *testing.T) {
			ctx := context.Background()
			sm := NewSegmentedMemory("rom", 200000, 0)
			sm.SetCompressor(&mockCompressor{enabled: true})
			for _, m := range flat {
				sm.AddMessage(ctx, m)
			}

			require.NoError(t, sm.Fold(ctx, 0, len(flat)))

			got := sm.GetMessages()
			assertAPIValidCarry(t, got)
			assert.Equal(t, computeCarry(flat), got, "Fold's L1 must equal computeCarry(flat) — the same partition restore recomputes")
		})
	}
}

// --- ballast-valve-evict-no-payoff-narrative-compress-once ---

func TestFold_RemainingBallastDroppedUnconditionally_NarrativeCompressedOnce(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 200000, 0)
	var captured [][]Message
	compressor := &mockCompressor{enabled: true, compressFn: func(msgs []Message) string {
		captured = append(captured, msgs)
		return "structured residue"
	}}
	sm.SetCompressor(compressor)

	m1 := Message{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()}
	a2, r2 := toolPairMessages("t1", "query_data", "tiny ballast 1", ClassBallast)
	a3, r3 := toolPairMessages("t2", "query_data", "tiny ballast 2", ClassBallast)
	n1 := Message{Role: "assistant", Content: "narrative note", Timestamp: time.Now()}

	// The dropped ballast is well under ValveEvict's own payoff bar (default
	// 20000 tokens) — proving fold's drop is unconditional, not payoff-gated.
	tc := GetTokenCounter()
	ballastTokens := tc.CountTokens(r2.Content) + tc.CountTokens(r3.Content)
	require.Less(t, ballastTokens, sm.minValvePayoffTokens, "test setup: dropped ballast must be far under the valve's own payoff bar")

	flat := []Message{m1, a2, r2, a3, r3, n1}
	for _, m := range flat {
		sm.AddMessage(ctx, m)
	}

	require.NoError(t, sm.Fold(ctx, 0, len(flat)))

	got := sm.GetMessages()
	for _, m := range got {
		assert.NotEqual(t, ClassBallast, m.ContextClass, "no ballast may survive a fold, regardless of the payoff bar")
		assert.NotContains(t, m.Content, "ballast", "ballast content must be dropped, not stubbed, at fold time")
	}

	require.Len(t, captured, 1, "narrative must be compressed exactly once per fold")
	assert.Equal(t, []Message{a2, a3, n1}, captured[0], "the compressor must receive exactly the narrative-class leftovers, in order — never the dropped ballast")

	l2 := sm.GetL2Summary()
	assert.Contains(t, l2, "structured residue")
	assert.NotContains(t, l2, "tiny ballast", "dropped ballast content must never appear in the residue")
}

// --- residue-structured-summary-recall-pointer-heuristic-fallback-logged ---

// strictMockCompressor implements both MemoryCompressor and StrictMemoryCompressor,
// letting tests prove Fold prefers CompressMessagesStrict when available and distinguish
// its input from CompressMessages'.
type strictMockCompressor struct {
	enabled         bool
	strictOut       string
	strictErr       error
	receivedRegular [][]Message
	receivedStrict  [][]Message
}

func (m *strictMockCompressor) CompressMessages(_ context.Context, messages []Message) (string, error) {
	m.receivedRegular = append(m.receivedRegular, messages)
	return "REGULAR-should-never-be-used", nil
}

func (m *strictMockCompressor) CompressMessagesStrict(_ context.Context, messages []Message) (string, error) {
	m.receivedStrict = append(m.receivedStrict, messages)
	if m.strictErr != nil {
		return "", m.strictErr
	}
	return m.strictOut, nil
}

func (m *strictMockCompressor) IsEnabled() bool { return m.enabled }

func TestFold_Residue_PrefersStrictCompressor_ExcludesCharterAndLedger(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 200000, 0)
	sc := &strictMockCompressor{enabled: true, strictOut: "STATE: onboarding done. DECISIONS: excluded Name,Cabin. COMMITMENTS: Gate 4 pending. REFS: S1,S2."}
	sm.SetCompressor(sc)

	m1 := Message{Role: "user", Content: "M1 instruction", ContextClass: ClassLedger, Timestamp: time.Now()}
	a2, r2 := toolPairMessages("t-charter", "manage_skills", "charter body", ClassCharter)
	m3 := Message{Role: "user", Content: "approved", ContextClass: ClassLedger, Timestamp: time.Now()}
	n1 := Message{Role: "assistant", Content: "narrative note about state", Timestamp: time.Now()}

	flat := []Message{m1, a2, r2, m3, n1}
	for _, m := range flat {
		sm.AddMessage(ctx, m)
	}

	require.NoError(t, sm.Fold(ctx, 0, len(flat)))

	assert.Empty(t, sc.receivedRegular, "Fold must prefer CompressMessagesStrict when the compressor implements it, never falling back to CompressMessages")
	require.Len(t, sc.receivedStrict, 1)
	assert.Equal(t, []Message{n1}, sc.receivedStrict[0], "only narrative leftovers go to the compressor — never charter or ledger (never restating instructions or approvals)")

	l2 := sm.GetL2Summary()
	assert.Contains(t, l2, "STATE: onboarding done")
	assert.Contains(t, l2, fmt.Sprintf("recall_context('fold:%d')", len(flat)))
}

func TestFold_Residue_DegradedFallback_LoggedOnCompressorError(t *testing.T) {
	ctx := context.Background()
	tracer := newEventCountingTracer()
	sm := NewSegmentedMemory("rom", 200000, 0)
	sm.SetTracer(tracer)
	sc := &strictMockCompressor{enabled: true, strictErr: fmt.Errorf("llm unavailable")}
	sm.SetCompressor(sc)

	flat := []Message{
		{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "narrative that must still be summarized somehow", Timestamp: time.Now()},
	}
	for _, m := range flat {
		sm.AddMessage(ctx, m)
	}

	require.NoError(t, sm.Fold(ctx, 0, len(flat)))

	assert.Equal(t, 1, tracer.count("memory.fold.residue_degraded_fallback"), "a compressor failure must be logged exactly once as a degraded fallback")
	l2 := sm.GetL2Summary()
	assert.NotEmpty(t, l2)
	assert.Contains(t, l2, "recall_context('fold:")
}

func TestFold_Residue_DegradedFallback_LoggedWhenNoCompressorConfigured(t *testing.T) {
	ctx := context.Background()
	tracer := newEventCountingTracer()
	sm := NewSegmentedMemory("rom", 200000, 0)
	sm.SetTracer(tracer)
	// No SetCompressor call: sm.compressor stays nil.

	flat := []Message{
		{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "narrative note", Timestamp: time.Now()},
	}
	for _, m := range flat {
		sm.AddMessage(ctx, m)
	}

	require.NoError(t, sm.Fold(ctx, 0, len(flat)))

	assert.Equal(t, 1, tracer.count("memory.fold.residue_degraded_fallback"))
}

// --- persist-residue-foldindex-durable-carry-not-persisted ---

func TestFold_PersistsResidueAndFoldIndexOnly_CarryNeverPersisted(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	sessionID := "fold-persist-only"
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	sm := NewSegmentedMemory("rom", 200000, 0)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&mockCompressor{enabled: true})

	flat := []Message{
		{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "narrative to be folded away, never persisted verbatim in the carry", Timestamp: time.Now()},
	}
	for _, m := range flat {
		sm.AddMessage(ctx, m)
		require.NoError(t, store.SaveMessage(ctx, sessionID, m))
	}

	require.NoError(t, sm.Fold(ctx, 0, len(flat)))

	snaps, err := store.LoadMemorySnapshots(ctx, sessionID, "l2_summary", 0)
	require.NoError(t, err)
	require.Len(t, snaps, 1, "exactly one fold record must be persisted")

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(snaps[0].Content), &raw))
	assert.ElementsMatch(t, []string{"residue", "foldIndex"}, mapKeys(raw), "the persisted envelope must carry only {residue, foldIndex} — the carry set itself is never persisted")

	var rec foldRecord
	require.NoError(t, json.Unmarshal([]byte(snaps[0].Content), &rec))
	assert.Equal(t, len(flat), rec.FoldIndex)
	assert.NotEmpty(t, rec.Residue)
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// --- breaker-third-fold-within-3-turns-reset-context ---

func TestFold_Breaker_ThirdCloseFoldReturnsRecoverableError(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 200000, 0)
	sm.SetCompressor(&mockCompressor{enabled: true})

	addLedger := func(n int) {
		sm.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("ledger %d", n), ContextClass: ClassLedger, Timestamp: time.Now()})
	}

	// 4 folds, each 1 ledger-user turn after the previous (gap=1 < breakerLedgerTurnWindow=3
	// every time): the first 3 succeed, the 4th (the third CLOSE fold) trips the breaker.
	for i := 1; i <= 3; i++ {
		addLedger(i)
		require.NoError(t, sm.Fold(ctx, 0, sm.flatMessageCount), "fold #%d must succeed", i)
	}

	beforeL1 := sm.GetMessages()
	beforeL2 := sm.GetL2Summary()

	addLedger(4)
	err := sm.Fold(ctx, 0, sm.flatMessageCount)

	require.Error(t, err)
	var rerr *RecoverableError
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, "token_budget_exceeded", rerr.ErrorType)
	assert.Equal(t, "reset_context", rerr.RecoveryAction)
	assert.True(t, rerr.Retryable)

	// The tripped attempt must not have folded: only the just-admitted
	// ledger message is new; L2 is untouched.
	afterL1 := sm.GetMessages()
	assert.Equal(t, len(beforeL1)+1, len(afterL1), "the breaker must refuse to fold — only the admitted ledger message is new")
	assert.Equal(t, beforeL2, sm.GetL2Summary(), "a tripped fold must never rewrite L2")
}

func TestFold_Breaker_SessionMemoryToolRouteCountsTowardSameBreaker(t *testing.T) {
	ctx := context.Background()
	tracer := newEventCountingTracer()
	sm := NewSegmentedMemory("rom", 200000, 0)
	sm.SetTracer(tracer)
	sm.SetCompressor(&mockCompressor{enabled: true})

	addLedger := func(n int) {
		sm.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("ledger %d", n), ContextClass: ClassLedger, Timestamp: time.Now()})
	}

	// fold #1 and #3 via the direct seam, #2 via the session-memory-tool
	// route (CompactMemory -> Fold) — both routes must share
	// sm.foldTurnHistory (SC-005).
	addLedger(1)
	require.NoError(t, sm.Fold(ctx, 0, sm.flatMessageCount))
	require.Len(t, sm.foldTurnHistory, 1)

	addLedger(2)
	sm.CompactMemory(ctx)
	require.Len(t, sm.foldTurnHistory, 2, "CompactMemory's fold must extend the shared breaker history")

	addLedger(3)
	require.NoError(t, sm.Fold(ctx, 0, sm.flatMessageCount))
	require.Len(t, sm.foldTurnHistory, 3)

	addLedger(4)
	before := sm.GetMessages()
	messagesCompacted, tokensSaved := sm.CompactMemory(ctx)

	assert.Equal(t, 0, messagesCompacted)
	assert.Equal(t, 0, tokensSaved)
	assert.Len(t, sm.foldTurnHistory, 3, "a tripped fold — even via CompactMemory — must never extend the shared breaker history")
	assert.Equal(t, before, sm.GetMessages(), "CompactMemory must leave memory untouched once the breaker trips")
	assert.Equal(t, 1, tracer.count("memory.fold.breaker_tripped"), "the breaker trip must be observable regardless of which route produced it")
}
