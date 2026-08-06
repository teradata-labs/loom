// Copyright 2026 Teradata. Licensed under the Apache License, Version 2.0.
//
// EAC-5 (SC-007, test-arch Entry 6) — Two sessions on one Agent are fully
// isolated. One session's skill load never changes another's advertised tools,
// and a handle/id owned by one (user,session) is not resolvable by another.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/storage"
)

func TestEpic_EAC5_MultiSessionIsolation(t *testing.T) {
	const sessA = "s3-eac5-A"
	const sessB = "s3-eac5-B"

	// S3: sessions A and B on ONE agent. A loads a skill (registers web_search
	// into A's ledger) and stores a >=64 KiB result (a handle in A's partition);
	// B does plain work. Capture through the M9 dump.
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		// A: load tooled (registers required tool), then a big result (offload).
		loadCall("a1", epicSkillTooled), finalTurn(),
		epicEmitCall("a2", map[string]interface{}{"bytes": 70 * 1024}), finalTurn(),
		// B: a plain turn.
		epicEmitCall("b1", map[string]interface{}{"text": "b-work"}), finalTurn(),
	}}
	rig := epicNewRig(t, llm, epicOpts{dump: true})

	// B's advertised tools BEFORE A acts.
	sB := rig.session(t, sessB)
	bBefore := advertisedNames(rig.agent, sB)

	// A's activity: load (registers web_search into A's ledger) + a big result.
	rig.chat(t, sessA, "load tooled and scan")
	rig.chat(t, sessA, "scan more")

	// (1) A's load never changes B's advertised tools — byte-unchanged.
	bAfter := advertisedNames(rig.agent, sB)
	assert.Equal(t, bBefore, bAfter, "session A's skill load does not change session B's advertised tools")
	assert.NotContains(t, bAfter, epicRequiredTool,
		"A's required-tool registration is scoped to A's ledger, absent from B")

	// B does its own turn; still never sees A's required tool.
	rig.chat(t, sessB, "b work")
	recs := epicReadDump(t, rig.sinkDir, rig.agent)
	for _, rec := range recs {
		if rec.SessionID != sessB {
			continue
		}
		assert.NotContains(t, epicToolNames(rec), epicRequiredTool,
			"B's dumped advertised tools never include A's required tool")
	}

	// A's offload handle (from A's dump).
	handle := ""
	for _, rec := range recs {
		if rec.SessionID != sessA {
			continue
		}
		for _, m := range rec.Messages {
			if mm := refIDPattern.FindStringSubmatch(m.Content); len(mm) == 2 {
				handle = mm[1]
			}
		}
	}
	require.NotEmpty(t, handle, "A stored a large result by reference (a handle in A's partition)")

	// (2, integrated) B driving query_tool_result(A's handle) resolves to
	// not-found; the owning session A resolves it.
	recall := NewQueryToolResultTool(nil, rig.shared)
	bRes, err := recall.Execute(session.WithSessionID(context.Background(), sessB),
		map[string]interface{}{"reference_id": handle, "offset": float64(0), "limit": float64(1_000_000)})
	require.NoError(t, err)
	assert.False(t, bRes.Success, "session B cannot resolve a handle owned by session A (not-found)")

	aRes, err := recall.Execute(session.WithSessionID(context.Background(), sessA),
		map[string]interface{}{"reference_id": handle, "offset": float64(0), "limit": float64(1_000_000)})
	require.NoError(t, err)
	assert.True(t, aRes.Success, "only the owning session A resolves its own handle")

	// (2, direct store seat) SharedMemoryStore.Get: a handle owned by one session
	// is not-found for another; only the owning partition resolves it.
	store := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes: 8 * 1024 * 1024, CompressionThreshold: 1 * 1024 * 1024, TTLSeconds: 3600,
	})
	ref, err := store.Store("h-owned", []byte("owner-only bytes"), "text/plain", nil, sessA)
	require.NoError(t, err)
	got, err := store.Get(ref, sessA)
	require.NoError(t, err)
	assert.Equal(t, "owner-only bytes", string(got), "the owning partition resolves its handle")
	_, err = store.Get(ref, sessB)
	require.Error(t, err, "a foreign partition receives not-found for a handle it does not own")

	// (2, direct store seat) ErrorStore.Get: an error id owned by one session is
	// not-found for another (reuses the delivered partition drivers).
	es := newTestErrorStore(t)
	errID := storeOwnedError(t, es, sessA)
	_, gerr := es.Get(session.WithSessionID(context.Background(), sessB), errID)
	require.Error(t, gerr, "a foreign session receives no error details for another session's error id")
	owned, oerr := es.Get(session.WithSessionID(context.Background(), sessA), errID)
	require.NoError(t, oerr)
	assert.Equal(t, errID, owned.ID, "the owning session resolves its own error id")

	t.Logf("EAC-5 OK: B tools byte-stable across A's load; A's handle not-found for B; store+error partitions enforced")
}
