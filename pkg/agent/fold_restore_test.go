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

// D-5 (Fold, breaker & restore, Part C) — Seam 3 (fold-aware restore) component acceptance
// tests.
//
// These tests drive the real pkg/agent runtime — Memory.GetOrCreateSessionWithAgent's
// restore path against a real in-process SQLite SessionStore, simulating a process restart
// by pointing a fresh *Memory at the same store, following the exact pattern
// memory_cross_session_test.go's TestMemory_GetOrCreateSessionWithAgent_SurvivesRestart_*
// established. They assert:
//   - restore-pure-bulkload-reclassify-recount-no-pressure: restore bulk-loads every durable
//     row, reclassifies legacy/unclassified rows, recounts tokens, and applies no pressure
//     (no fold/valve), even when the restored content already sits in the red zone.
//   - restore-recompute-carry-persisted-fold-verbatim-fallback-refold: with a persisted
//     fold, restore reproduces recompute_carry(flat[:foldIndex]) + flat[foldIndex:]; with an
//     absent or unreadable (corrupt JSON, out-of-range foldIndex) fold record, restore falls
//     back to a full verbatim load and the next red beat re-folds (self-healing).
//   - resume-after-b15-restore-reproduces-b16-exactly: a session folded then restarted
//     reproduces the next beat's compiled context exactly, byte for byte, against an
//     identically-scripted session that was never restarted.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// msgSignature reduces a Message to the fields that matter for "the LLM-visible compiled
// context" — deliberately excluding the DB-assigned ID and Timestamp, which are storage
// metadata that legitimately differs between a message that only ever lived in-process and
// one that round-tripped through LoadMessages, but is never part of what the LLM sees.
func msgSignature(m Message) string {
	return fmt.Sprintf("%s|%s|%s|%s|%v", m.Role, m.ContextClass, m.ToolUseID, m.Content, m.ToolCalls)
}

func msgSignatures(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = msgSignature(m)
	}
	return out
}

// --- restore-pure-bulkload-reclassify-recount-no-pressure ---

func TestRestore_PureBulkLoad_ReclassifiesLegacyRows_RecountsTokens_NoPressureApplied(t *testing.T) {
	tmpfile := t.TempDir() + "/restore-bulkload.db"
	store, err := NewSessionStore(tmpfile, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	sessionID := "restore-bulkload"

	memory1 := NewMemoryWithStore(store)
	memory1.SetContextLimits(2000, 0)
	session := memory1.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	require.NotNil(t, session)

	big := sentenceRepeat(50) // ~501 tokens
	legacyMessages := []Message{
		{Role: "user", Content: "legacy user " + big, Timestamp: time.Now()}, // ContextClass unset -> legacy/unclassified
		{Role: "assistant", Content: "legacy assistant " + big, Timestamp: time.Now()},
		{Role: "user", Content: "legacy user two " + big, Timestamp: time.Now()},
		{Role: "assistant", Content: "legacy assistant two " + big, Timestamp: time.Now()},
	}
	for _, m := range legacyMessages {
		require.Empty(t, m.ContextClass, "test setup: these rows must be legacy/unclassified")
		memory1.AddMessage(ctx, sessionID, m)
	}

	// --- Simulate a process restart: fresh Memory over the same store. ---
	memory2 := NewMemoryWithStore(store)
	memory2.SetContextLimits(2000, 0)
	restored := memory2.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	require.NotNil(t, restored)

	// Reclassify: persisted-empty rows resolve to concrete classes by the
	// same structural rules used at construction (D-3).
	require.Len(t, restored.Messages, len(legacyMessages))
	for i, m := range restored.Messages {
		if legacyMessages[i].Role == "user" {
			assert.Equal(t, ClassLedger, m.ContextClass, "unclassified user rows must reclassify to ledger on restore")
		} else {
			assert.Equal(t, ClassNarrative, m.ContextClass, "unclassified assistant rows must reclassify to narrative on restore")
		}
	}

	segMem, ok := restored.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	// Pure bulk-load: every durable row landed in L1, nothing evicted or
	// folded, even though the restored content is now well over the red
	// zone.
	assert.Equal(t, len(legacyMessages), segMem.GetL1MessageCount(), "restore must bulk-load every durable row into L1")
	assert.Empty(t, segMem.GetL2Summary(), "restore must never populate L2 — folding is prepareContext's job, not restore's")

	_, redPct := segMem.ZoneThresholds()
	require.GreaterOrEqual(t, segMem.BudgetPct(), redPct, "test setup: the restored content must genuinely be over red to prove restore applies no pressure")

	// Recount: the token count reflects a genuine recomputation over the
	// restored content, not a stale/zero carry-over.
	assert.Greater(t, segMem.GetTokenCount(), 0)
}

// --- restore-recompute-carry-persisted-fold-verbatim-fallback-refold ---

func TestRestore_PersistedFold_RecomputesCarryPlusPostFoldTail(t *testing.T) {
	tmpfile := t.TempDir() + "/restore-recompute-carry.db"
	store, err := NewSessionStore(tmpfile, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	sessionID := "restore-recompute-carry"

	memory1 := NewMemoryWithStore(store)
	session := memory1.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	segMem.SetCompressor(&mockCompressor{enabled: true})

	preFold := []Message{
		{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "M2 narrative", Timestamp: time.Now()},
	}
	for _, m := range preFold {
		memory1.AddMessage(ctx, sessionID, m)
	}
	require.NoError(t, segMem.Fold(ctx, 0, len(session.Messages)))

	postFold := []Message{
		{Role: "user", Content: "M3 approved", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "M4 after fold", Timestamp: time.Now()},
	}
	for _, m := range postFold {
		memory1.AddMessage(ctx, sessionID, m)
	}

	flatBeforeRestart := make([]Message, len(session.Messages))
	copy(flatBeforeRestart, session.Messages)

	// --- restart ---
	memory2 := NewMemoryWithStore(store)
	restored := memory2.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	restoredSegMem, ok := restored.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	snaps, err := store.LoadMemorySnapshots(ctx, sessionID, "l2_summary", 0)
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	var rec foldRecord
	require.NoError(t, json.Unmarshal([]byte(snaps[0].Content), &rec))

	wantCarry := computeCarry(flatBeforeRestart[:rec.FoldIndex])
	wantTail := flatBeforeRestart[rec.FoldIndex:]
	wantL1 := append(append([]Message{}, wantCarry...), wantTail...)

	assert.Equal(t, msgSignatures(wantL1), msgSignatures(restoredSegMem.GetMessages()),
		"restore must set L1 = recompute_carry(flat[:foldIndex]) + flat[foldIndex:]")
	assert.Equal(t, rec.Residue, restoredSegMem.GetL2Summary())
}

func TestRestore_AbsentFold_VerbatimRestoreThenNextRedBeatReFolds(t *testing.T) {
	tmpfile := t.TempDir() + "/restore-absent-fold.db"
	store, err := NewSessionStore(tmpfile, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	sessionID := "restore-absent-fold"

	memory1 := NewMemoryWithStore(store)
	memory1.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	msgs := []Message{
		{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "M2", Timestamp: time.Now()},
	}
	for _, m := range msgs {
		memory1.AddMessage(ctx, sessionID, m)
	}

	// No fold was ever performed — no memory_snapshots row exists.
	snaps, err := store.LoadMemorySnapshots(ctx, sessionID, "l2_summary", 0)
	require.NoError(t, err)
	require.Empty(t, snaps, "test setup: no fold record must exist")

	memory2 := NewMemoryWithStore(store)
	restored := memory2.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	segMem, ok := restored.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	assert.Empty(t, segMem.GetL2Summary(), "absent fold must restore verbatim — L2 stays empty")
	assert.Equal(t, len(msgs), segMem.GetL1MessageCount())

	// The next red beat must re-fold: drive the real single-writer seam.
	segMem.SetCompressor(&mockCompressor{enabled: true})
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax())
	_, redPct := segMem.ZoneThresholds()
	require.GreaterOrEqual(t, segMem.BudgetPct(), redPct, "test setup: must land in red to exercise re-fold")

	_, err = ag.prepareContext(ctx, restored)
	require.NoError(t, err)
	assert.NotEmpty(t, segMem.GetL2Summary(), "the first red beat after an absent-fold restore must re-fold (self-healing)")
}

func TestRestore_UnreadableFold_VerbatimRestoreThenReFold(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	ctx := context.Background()
	sessionID := "restore-unreadable-fold"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	msgs := []Message{
		{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()},
		{Role: "assistant", Content: "M2", Timestamp: time.Now()},
	}
	for _, m := range msgs {
		require.NoError(t, store.SaveMessage(ctx, sessionID, m))
	}
	// A corrupt/unreadable fold record (not valid JSON).
	require.NoError(t, store.SaveMemorySnapshot(ctx, sessionID, "l2_summary", "not json", 0))

	memory2 := NewMemoryWithStore(store)
	restored := memory2.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	segMem, ok := restored.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	assert.Empty(t, segMem.GetL2Summary(), "an unreadable fold record must fall back to verbatim restore")
	assert.Equal(t, len(msgs), segMem.GetL1MessageCount())

	segMem.SetCompressor(&mockCompressor{enabled: true})
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax())

	_, err := ag.prepareContext(ctx, restored)
	require.NoError(t, err)
	assert.NotEmpty(t, segMem.GetL2Summary(), "the next red beat must self-heal by re-folding")
}

func TestRestore_FoldIndexOutOfRange_TreatedAsUnreadable(t *testing.T) {
	store := newContextClassSQLiteStore(t)
	ctx := context.Background()
	sessionID := "restore-foldindex-oor"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))

	require.NoError(t, store.SaveMessage(ctx, sessionID, Message{Role: "user", Content: "M1", ContextClass: ClassLedger, Timestamp: time.Now()}))

	bad, err := json.Marshal(foldRecord{Residue: "stale residue", FoldIndex: 999})
	require.NoError(t, err)
	require.NoError(t, store.SaveMemorySnapshot(ctx, sessionID, "l2_summary", string(bad), 0))

	memory2 := NewMemoryWithStore(store)
	restored := memory2.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
	segMem, ok := restored.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)

	assert.Empty(t, segMem.GetL2Summary(), "an out-of-range foldIndex must be treated as unreadable — verbatim restore, not a bad slice")
	assert.Equal(t, 1, segMem.GetL1MessageCount())
}

// --- resume-after-b15-restore-reproduces-b16-exactly ---

func TestRestore_ResumeAfterFold_ReproducesNextBeatExactly(t *testing.T) {
	buildPreFold := func(t *testing.T, sessionID string, store *SessionStore) (*Memory, *Session) {
		t.Helper()
		mem := NewMemoryWithStore(store)
		ctx := context.Background()
		session := mem.GetOrCreateSessionWithAgent(ctx, sessionID, "", "")
		segMem, ok := session.SegmentedMem.(*SegmentedMemory)
		require.True(t, ok)
		segMem.SetCompressor(&mockCompressor{enabled: true, compressFn: func(msgs []Message) string {
			return "fixed residue text"
		}})

		preFold := []Message{
			{Role: "user", Content: "Give data scientists read-only access.", ContextClass: ClassLedger, Timestamp: time.Now()},
			{Role: "assistant", Content: "Loaded skill.", Timestamp: time.Now()},
			{Role: "user", Content: "approved", ContextClass: ClassLedger, Timestamp: time.Now()},
		}
		for _, m := range preFold {
			mem.AddMessage(ctx, sessionID, m)
		}
		require.NoError(t, segMem.Fold(ctx, 0, len(session.Messages)))
		return mem, session
	}

	b16 := Message{Role: "user", Content: "approved", ContextClass: ClassLedger, Timestamp: time.Now()}

	// --- Live: never restarted. ---
	liveStore := newContextClassSQLiteStore(t)
	liveMem, liveSession := buildPreFold(t, "resume-live", liveStore)
	liveMem.AddMessage(context.Background(), "resume-live", b16)
	liveContext := liveSession.GetMessages()

	// --- Same script, but restart after the fold and before the next beat. ---
	restartStore := newContextClassSQLiteStore(t)
	buildPreFold(t, "resume-restart", restartStore)

	mem2 := NewMemoryWithStore(restartStore)
	restored := mem2.GetOrCreateSessionWithAgent(context.Background(), "resume-restart", "", "")
	mem2.AddMessage(context.Background(), "resume-restart", b16)
	restoredContext := restored.GetMessages()

	assert.Equal(t, msgSignatures(liveContext), msgSignatures(restoredContext),
		"resume-after-fold restore must reproduce the next beat's compiled context exactly")
}
