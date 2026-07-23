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

// D-4 (Admission + valve eviction + recall, Part B) — Seam 2 (SegmentedMemory.ValveEvict)
// component acceptance tests.
//
// These tests drive the real pkg/agent runtime — SegmentedMemory.AddMessage,
// SetSessionStore, and ValveEvict directly (the exported surface ValveEvict fills, D-1's
// no-op stub) plus a real in-process SQLite SessionStore (temp file, no server) for the
// durable-row assertions, following the same pattern as session_store_cross_session_test.go
// and session_store_context_class_test.go. They assert:
//
//   - valve-oldest-first-payoff-bar-protects-charter-ledger-narrative: candidates are walked
//     oldest→newest, the newest keepRecentBallast(=3) ballast items and any existing stub are
//     never candidates, charter/ledger/narrative messages are never candidates regardless of
//     size or position, and the batch fires only once the aggregate reclaim reaches
//     minValvePayoffTokens(=20000) — below that bar nothing is evicted.
//   - stub-inmemory-only-preserves-tooluseid-durable-row-untouched: eviction rewrites only the
//     in-memory l1Messages projection (Content becomes the recall_context stub, ToolUseID/
//     Role/ContextClass left intact so the stub stays a valid tool_result); the durable
//     `messages` row saved via SessionStorage is never rewritten and reads back unchanged.
//   - no-store-no-evict-log-once: with no durable session store wired, ValveEvict evicts
//     nothing (even when the payoff bar would otherwise be met) and logs the disablement
//     event exactly once across repeated calls, never once per call.
//   - post-restore-reeviction-byte-identical-stub: because the stub's ref is the evicted tool
//     result's ToolUseID — identity-derived, never minted — reloading a session's durable rows
//     into a fresh SegmentedMemory (simulating a process restart) and running ValveEvict again
//     reproduces byte-for-byte the same stub a live, never-restarted session would have
//     produced.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// sentenceRepeat builds deterministic, tokenizer-friendly filler content: the tiktoken
// cl100k_base encoder (via TokenCounter.CountTokens) counts almost exactly 10*n+1 tokens for
// n repeats of this 45-character sentence, letting tests target an exact side of the
// minValuePayoffTokens(=20000) bar without depending on the tokenizer's byte-to-token ratio
// for arbitrary text.
func sentenceRepeat(n int) string {
	return strings.Repeat("The quick brown fox jumps over the lazy dog. ", n)
}

// eventCountingTracer wraps a NoOpTracer, additionally counting RecordEvent calls by name —
// observability.MockTracer intentionally discards RecordEvent (event content isn't its
// concern), so this local test double is used instead to assert the valve's "log once"
// guarantee (C-022).
type eventCountingTracer struct {
	*observability.NoOpTracer
	mu     sync.Mutex
	events map[string]int
}

func newEventCountingTracer() *eventCountingTracer {
	return &eventCountingTracer{NoOpTracer: observability.NewNoOpTracer(), events: make(map[string]int)}
}

func (t *eventCountingTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events[name]++
}

func (t *eventCountingTracer) count(name string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.events[name]
}

// addBallastTurn appends an assistant tool_use + its tool_result to sm's L1 cache, and — when
// store is non-nil — persists both as durable rows too, simulating a live conversation where
// each turn is written through to storage as it happens.
func addBallastTurn(t *testing.T, ctx context.Context, sm *SegmentedMemory, store *SessionStore, sessionID, toolUseID, toolName, content string, class ContextClass) {
	t.Helper()
	assistantMsg := Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: []ToolCall{{ID: toolUseID, Name: toolName}},
		Timestamp: time.Now(),
	}
	toolMsg := Message{
		Role:         "tool",
		Content:      content,
		ToolUseID:    toolUseID,
		ContextClass: class,
		Timestamp:    time.Now(),
	}
	sm.AddMessage(ctx, assistantMsg)
	sm.AddMessage(ctx, toolMsg)
	if store != nil {
		require.NoError(t, store.SaveMessage(ctx, sessionID, assistantMsg))
		require.NoError(t, store.SaveMessage(ctx, sessionID, toolMsg))
	}
}

// newValveScenario builds a session backed by a real in-process SQLite SessionStore, then
// populates it with an interleaved narrative/ledger/charter/ballast conversation:
//
//  1. narrative (assistant text, large)                                    — never a candidate
//  2. ballastA  tool result (oldest ballast, large: ~12001 tok)             — candidate
//  3. ballastB  tool result (large: ~12001 tok)                            — candidate
//  4. ledger    tool result (large)                                        — never a candidate
//  5. ballastC  tool result (small, kept — newest-3 window)                — protected
//  6. charter   tool result (large, e.g. manage_skills)                    — never a candidate
//  7. ballastD  tool result (small, kept)                                  — protected
//  8. ballastE  tool result (small, newest, kept)                          — protected
//
// ballastA+ballastB alone reach ~24002 tokens, clearing the 20000 payoff bar, while
// ballastC/D/E are the newest 3 ballast items and must never be touched.
func newValveScenario(t *testing.T) (sm *SegmentedMemory, store *SessionStore, sessionID string) {
	t.Helper()
	store = newContextClassSQLiteStore(t)
	sessionID = "valve-scenario"

	ctx := context.Background()
	session := &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.SaveSession(ctx, session))

	sm = NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	sm.AddMessage(ctx, Message{Role: "assistant", Content: sentenceRepeat(2000), Timestamp: time.Now()})

	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_ballastA", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_ballastB", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_ledger", "run_migration", sentenceRepeat(1200), ClassLedger)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_ballastC", "query_data", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_charter", "manage_skills", sentenceRepeat(1200), ClassCharter)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_ballastD", "query_data", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_ballastE", "query_data", sentenceRepeat(5), ClassBallast)

	return sm, store, sessionID
}

// messageByToolUseID finds the message with the given ToolUseID in msgs, failing the test if
// absent — the eviction stub must preserve ToolUseID unchanged, so lookup by it must survive.
func messageByToolUseID(t *testing.T, msgs []Message, toolUseID string) Message {
	t.Helper()
	for _, m := range msgs {
		if m.ToolUseID == toolUseID {
			return m
		}
	}
	t.Fatalf("no message with ToolUseID %q found", toolUseID)
	return Message{}
}

// TestValveEvict_OldestFirstPayoffBarProtectsCharterLedgerNarrative covers
// valve-oldest-first-payoff-bar-protects-charter-ledger-narrative: candidates walk
// oldest→newest excluding the newest 3 ballast items, fire only once the batch reaches the
// 20000-token payoff bar, and charter/ledger/narrative are never touched regardless of size
// or position in the conversation.
func TestValveEvict_OldestFirstPayoffBarProtectsCharterLedgerNarrative(t *testing.T) {
	sm, _, _ := newValveScenario(t)
	ctx := context.Background()

	before := sm.GetMessages()

	sm.ValveEvict(ctx)

	after := sm.GetMessages()
	require.Len(t, after, len(before), "ValveEvict must never add or remove messages, only rewrite Content")

	// Oldest two ballast candidates (A, B) — evicted: stubbed, in-memory only.
	for _, id := range []string{"toolu_ballastA", "toolu_ballastB"} {
		msg := messageByToolUseID(t, after, id)
		assert.True(t, strings.HasPrefix(msg.Content, evictedStubPrefix),
			"oldest ballast candidate %s must be replaced with an eviction stub", id)
		assert.Contains(t, msg.Content, "recall_context('"+id+"')", "stub must name the ToolUseID as its recall ref")
		assert.Contains(t, msg.Content, "query_data", "stub must name the originating tool")
	}

	// Newest 3 ballast items (C, D, E) are protected by keepRecentBallast, untouched.
	for _, id := range []string{"toolu_ballastC", "toolu_ballastD", "toolu_ballastE"} {
		msg := messageByToolUseID(t, after, id)
		assert.Equal(t, sentenceRepeat(5), msg.Content, "newest-3 ballast item %s must never be evicted", id)
	}

	// Ledger and charter tool results are never candidates, regardless of size.
	ledgerMsg := messageByToolUseID(t, after, "toolu_ledger")
	assert.Equal(t, sentenceRepeat(1200), ledgerMsg.Content, "ledger tool result must never be evicted")
	charterMsg := messageByToolUseID(t, after, "toolu_charter")
	assert.Equal(t, sentenceRepeat(1200), charterMsg.Content, "charter tool result must never be evicted")

	// The narrative assistant message (no ToolUseID) is unchanged: find it by role.
	var narrativeAfter *Message
	for i := range after {
		if after[i].Role == "assistant" && after[i].ToolUseID == "" && len(after[i].ToolCalls) == 0 {
			narrativeAfter = &after[i]
			break
		}
	}
	require.NotNil(t, narrativeAfter, "narrative assistant message must still be present")
	assert.Equal(t, sentenceRepeat(2000), narrativeAfter.Content, "narrative message must never be evicted")
}

// TestValveEvict_BelowPayoffBarEvictsNothing covers the payoff-bar half of
// valve-oldest-first-payoff-bar-protects-charter-ledger-narrative in isolation: with more
// than keepRecentBallast(=3) ballast items, there IS a non-empty candidate set once the
// newest 3 are excluded, but its aggregate reclaim (~1001 tokens) falls short of the
// 20000-token bar — so ValveEvict must evict nothing at all.
func TestValveEvict_BelowPayoffBarEvictsNothing(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	sessionID := "valve-below-bar"
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	// 4 ballast items: only the oldest is a candidate (the newest 3 are kept), and its
	// ~1001 tokens fall well short of the 20000 bar.
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_p", "query_data", sentenceRepeat(100), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_q", "query_data", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_r", "query_data", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_s", "query_data", sentenceRepeat(5), ClassBallast)

	before := sm.GetMessages()
	sm.ValveEvict(ctx)
	after := sm.GetMessages()

	assert.Equal(t, before, after, "below the 20000-token payoff bar, ValveEvict must evict nothing")
}

// TestValveEvict_ExistingStubNeverReEvicted covers the "excluding ... existing stubs" half of
// valve-oldest-first-payoff-bar-protects-charter-ledger-narrative: a message already replaced
// by a prior valve pass (content carrying the eviction-stub prefix) must never be selected as
// a candidate again — it is excluded from ballastIdx entirely, so it neither gets
// re-stubbed/double-wrapped nor consumes one of the keepRecentBallast "protected" slots.
func TestValveEvict_ExistingStubNeverReEvicted(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	sessionID := "valve-existing-stub"
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	sm := NewSegmentedMemory("rom", 0, 0)
	sm.SetSessionStore(store, sessionID)

	// A message that already looks like a valve stub (simulating a prior eviction pass).
	alreadyStubbed := evictedStubPrefix + "query_data result, 999 tok → recall_context('toolu_old')]"
	sm.AddMessage(ctx, Message{
		Role:         "tool",
		Content:      alreadyStubbed,
		ToolUseID:    "toolu_old",
		ContextClass: ClassBallast,
		Timestamp:    time.Now(),
	})

	// Enough fresh ballast to clear the payoff bar on its own (~24002 tokens across 2 items),
	// plus 3 more small ones so the newest-3 window is exactly these three, not reaching back
	// to the pre-stubbed message.
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_x", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_y", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_z1", "query_data", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_z2", "query_data", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm, store, sessionID, "toolu_z3", "query_data", sentenceRepeat(5), ClassBallast)

	sm.ValveEvict(ctx)
	after := sm.GetMessages()

	oldMsg := messageByToolUseID(t, after, "toolu_old")
	assert.Equal(t, alreadyStubbed, oldMsg.Content, "an already-stubbed message must be left exactly as-is, never re-wrapped")

	for _, id := range []string{"toolu_x", "toolu_y"} {
		msg := messageByToolUseID(t, after, id)
		assert.True(t, strings.HasPrefix(msg.Content, evictedStubPrefix), "%s must be evicted (payoff bar cleared by x+y alone)", id)
	}
}

// TestValveEvict_StubInMemoryOnlyPreservesToolUseIDDurableRowUntouched covers
// stub-inmemory-only-preserves-tooluseid-durable-row-untouched: the eviction stub replaces
// Content only in the in-memory l1Messages projection (read via GetMessages), preserving
// ToolUseID/Role/ContextClass so the stub remains a valid tool_result; the durable `messages`
// row (read back via the real SQLite SessionStore, never the ephemeral in-memory layer) keeps
// its original, unstubbed content.
func TestValveEvict_StubInMemoryOnlyPreservesToolUseIDDurableRowUntouched(t *testing.T) {
	sm, store, sessionID := newValveScenario(t)
	ctx := context.Background()

	sm.ValveEvict(ctx)

	evictedID := "toolu_ballastA"
	inMemory := messageByToolUseID(t, sm.GetMessages(), evictedID)

	// In-memory projection: stubbed, but the tool_result-shape fields survive intact.
	assert.True(t, strings.HasPrefix(inMemory.Content, evictedStubPrefix), "in-memory Content must be replaced with the stub")
	assert.Equal(t, evictedID, inMemory.ToolUseID, "ToolUseID must be preserved unchanged so the stub stays a valid tool_result")
	assert.Equal(t, "tool", inMemory.Role, "Role must remain \"tool\" so tool_use/tool_result pairing holds")
	assert.Equal(t, ClassBallast, inMemory.ContextClass, "ContextClass must remain ballast (re-evictable, ballast pairing invariant)")

	// Durable row: read back through the real store, independent of l1Messages.
	durableRows, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	durable := messageByToolUseID(t, durableRows, evictedID)
	assert.Equal(t, sentenceRepeat(1200), durable.Content, "the durable messages row must never be rewritten by ValveEvict — original content must read back unchanged")
	assert.Equal(t, evictedID, durable.ToolUseID)
	assert.Equal(t, string(ClassBallast), durable.ContextClass)
}

// TestValveEvict_NoStoreEvictsNothingLogsOnce covers no-store-no-evict-log-once: with no
// durable session store wired (sessionStore == nil), ValveEvict must evict nothing — even
// when the candidate batch would otherwise clear the payoff bar — and must log the
// "disabled, no durable store" event exactly once, not once per call.
func TestValveEvict_NoStoreEvictsNothingLogsOnce(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("rom", 0, 0)
	tracer := newEventCountingTracer()
	sm.SetTracer(tracer)
	// Deliberately never call SetSessionStore: sessionStore stays nil.

	addBallastTurn(t, ctx, sm, nil, "", "toolu_1", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, nil, "", "toolu_2", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, nil, "", "toolu_3", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, nil, "", "toolu_4", "query_data", sentenceRepeat(1200), ClassBallast)
	addBallastTurn(t, ctx, sm, nil, "", "toolu_5", "query_data", sentenceRepeat(1200), ClassBallast)

	before := sm.GetMessages()

	sm.ValveEvict(ctx)
	sm.ValveEvict(ctx)
	sm.ValveEvict(ctx)

	after := sm.GetMessages()
	assert.Equal(t, before, after, "with no durable session store, ValveEvict must evict nothing at all, ever")
	assert.Equal(t, 1, tracer.count("memory.valve_disabled_no_store"), "the no-store disablement must log exactly once, not once per call")
}

// TestValveEvict_PostRestoreReEvictionByteIdenticalStub covers
// post-restore-reeviction-byte-identical-stub: the LLD's DESIGN-PROBE FINDING establishes that
// the recall ref must be the evicted tool result's ToolUseID rather than Message.ID, because
// messages.id is populated only on read (SaveMessage never captures LastInsertId) — so a
// live, never-restarted message has ID=="" while the same row reloaded from the database has
// a real ID. A ref keyed on Message.ID would therefore differ before and after a restart; a
// ref keyed on ToolUseID (durable, set at construction, unaffected by the read-vs-write id
// gap) must not. This test proves the fix: it evicts the same content twice — once in a live
// SegmentedMemory, once in a fresh one rebuilt from the durable rows (simulating a process
// restart via ReplayMessages, the documented bulk-restore path) — and asserts the two stubs
// are byte-identical despite the underlying Message.ID differing.
func TestValveEvict_PostRestoreReEvictionByteIdenticalStub(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	sessionID := "valve-post-restore"
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	// Live (pre-restart) session: one ballast item large enough to clear the payoff bar alone,
	// plus 3 small trailing ones so it isn't itself in the protected newest-3 window.
	sm1 := NewSegmentedMemory("rom", 0, 0)
	sm1.SetSessionStore(store, sessionID)
	addBallastTurn(t, ctx, sm1, store, sessionID, "toolu_restore_me", "get_account_history", sentenceRepeat(2000), ClassBallast)
	addBallastTurn(t, ctx, sm1, store, sessionID, "toolu_r1", "get_account_history", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm1, store, sessionID, "toolu_r2", "get_account_history", sentenceRepeat(5), ClassBallast)
	addBallastTurn(t, ctx, sm1, store, sessionID, "toolu_r3", "get_account_history", sentenceRepeat(5), ClassBallast)

	preEviction := messageByToolUseID(t, sm1.GetMessages(), "toolu_restore_me")
	require.Equal(t, "", preEviction.ID, "precondition: a live, never-persisted-then-reloaded message has no DB-assigned ID yet")

	sm1.ValveEvict(ctx)
	liveStub := messageByToolUseID(t, sm1.GetMessages(), "toolu_restore_me")
	require.True(t, strings.HasPrefix(liveStub.Content, evictedStubPrefix), "precondition: the live session must actually evict this item")

	// Simulate a process restart: reload the untouched durable rows into a brand-new
	// SegmentedMemory via ReplayMessages (the documented pure bulk-load restore path), then
	// run ValveEvict again.
	restoredRows, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	restoredMsg := messageByToolUseID(t, restoredRows, "toolu_restore_me")
	require.NotEqual(t, "", restoredMsg.ID, "a row reloaded via LoadMessages must carry its DB-assigned ID — the exact asymmetry the ref must not depend on")

	sm2 := NewSegmentedMemory("rom", 0, 0)
	sm2.SetSessionStore(store, sessionID)
	sm2.ReplayMessages(ctx, restoredRows)

	sm2.ValveEvict(ctx)
	restoredStub := messageByToolUseID(t, sm2.GetMessages(), "toolu_restore_me")

	assert.Equal(t, liveStub.Content, restoredStub.Content,
		"a post-restore re-eviction must reproduce a byte-identical stub, despite Message.ID differing between the live and restored message")
}
