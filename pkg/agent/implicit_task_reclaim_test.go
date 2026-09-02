// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	sqlitestore "github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// The implicit emitter's per-session and per-turn maps are freed only by
// ForgetSession and EndTurn. The emitter is built once in NewAgent and the
// agent outlives every session it serves, so the wiring tested here is the
// only thing standing between a long-running server and unbounded heap growth.
//
// The emitter's maps are private to pkg/task (drained directly in
// pkg/task/implicit_lifecycle_test.go). These tests assert the same
// reclamation from outside, through observable emitter behaviour:
//
//   - The CAP COUNTER is observable because a cap of 1 makes the second turn
//     of a session decline. If retirement freed the counter, a session id
//     reused after retirement mints again; if it did not, it stays declined
//     forever.
//   - The PER-TURN MEMO is observable because a memo hit returns (nil task)
//     while a released memo falls through to the idempotent create and returns
//     the existing task.

// reclaimRig is an agent with a real task store and an emitter capped at one
// task per session, which is what makes reclamation observable.
type reclaimRig struct {
	agent   *Agent
	emitter *task.ImplicitEmitter
}

// newReclaimRig builds the rig. maxPerSession is the emitter's cap:
//   - 1 makes the CAP COUNTER observable (the second turn of a session
//     declines), which is what the session-retirement tests read.
//   - 0 leaves the default cap in place, so the cap never fires and the
//     PER-TURN MEMO is the only thing that can make a mint return nil, which
//     is what the per-turn test reads.
func newReclaimRig(t *testing.T, maxPerSession int) *reclaimRig {
	t.Helper()

	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "tasks.db")+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mig, err := sqlitestore.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))

	mgr := task.NewManager(
		sqlitestore.NewTaskStore(db, observability.NewNoOpTracer()),
		nil, observability.NewNoOpTracer(), nil)

	policy := task.ResolveImplicitPolicy(&loomv1.ImplicitTaskConfig{
		MaxPerSession: int32(maxPerSession),
	})
	emitter := task.NewImplicitEmitter(mgr, policy, observability.NewNoOpTracer(), zap.NewNop())

	a := NewAgent(&mockBackend{}, &countingLLM{},
		WithConfig(DefaultConfig()),
		WithTaskBoard(mgr, nil, &loomv1.TaskBoardConfig{}),
		WithImplicitTaskEmitter(emitter),
	)
	return &reclaimRig{agent: a, emitter: emitter}
}

// mint runs one turn for a session. The board id is the session id, which is
// what maybeRecordImplicitTask defaults to when no DefaultBoardId is set.
func (r *reclaimRig) mint(t *testing.T, sessionID string, turn int) *task.Task {
	t.Helper()
	_, created, err := r.emitter.EnsureForTurn(context.Background(), task.TurnRequest{
		SessionID:   sessionID,
		AgentID:     "agent-1",
		BoardID:     sessionID,
		TurnIndex:   turn,
		Trigger:     loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
		UserMessage: "do work",
	})
	require.NoError(t, err)
	return created
}

// TestDeleteSession_ReclaimsImplicitEmitterState: Agent.DeleteSession frees
// every other piece of per-session state; the emitter must join in.
func TestDeleteSession_ReclaimsImplicitEmitterState(t *testing.T) {
	r := newReclaimRig(t, 1)
	const sid = "sess-retired"

	require.NotNil(t, r.mint(t, sid, 0), "first turn of a session mints")
	require.Nil(t, r.mint(t, sid, 1), "second turn must hit the cap of 1 (rig sanity)")

	r.agent.DeleteSession(sid)

	if got := r.mint(t, sid, 2); got == nil {
		t.Fatal("DeleteSession must drop the emitter's cap counter for the session; " +
			"a retired session id still counts against MaxPerSession, so the emitter's " +
			"per-session maps are never freed and grow for the life of the process")
	}
}

// TestClearAllSessions_ReclaimsImplicitEmitterState is the same contract for
// DeleteSession's bulk sibling. It also pins the ordering: ClearAllSessions
// must read the session list BEFORE ClearAll empties it.
func TestClearAllSessions_ReclaimsImplicitEmitterState(t *testing.T) {
	r := newReclaimRig(t, 1)
	ids := []string{"sess-a", "sess-b", "sess-c"}

	for _, sid := range ids {
		// The session must exist in memory: ClearAllSessions frees by walking
		// ListSessions, so a session it cannot see is a session it cannot free.
		require.NotNil(t, r.agent.CreateSession(context.Background(), sid, ""))
		require.NotNil(t, r.mint(t, sid, 0), "first turn of %s mints", sid)
		require.Nil(t, r.mint(t, sid, 1), "second turn of %s must hit the cap (rig sanity)", sid)
	}

	r.agent.ClearAllSessions()

	for _, sid := range ids {
		if got := r.mint(t, sid, 2); got == nil {
			t.Errorf("ClearAllSessions must drop the emitter's state for %s; it still counts against MaxPerSession", sid)
		}
	}
}

// TestCompleteImplicitTask_ReleasesTurnMemo covers the per-turn half of the
// wiring: completeImplicitTask is chat()'s single deferred teardown hook, and
// it must release the turn's memo.
//
// A nil binding is passed deliberately. That is the "turn recorded no task"
// shape, which takes completeImplicitTask's early return — so this also pins
// the decision that EndTurn runs BEFORE that return rather than after it.
func TestCompleteImplicitTask_ReleasesTurnMemo(t *testing.T) {
	// Default cap, so a decline can only come from the memo, never the cap.
	r := newReclaimRig(t, 0)
	const sid = "sess-memo"

	first := r.mint(t, sid, 0)
	require.NotNil(t, first, "first turn mints")
	require.Nil(t, r.mint(t, sid, 0), "a second trigger in the same turn must hit the memo (rig sanity)")

	r.agent.completeImplicitTask(context.Background(), nil, sid, 0, "done")

	again := r.mint(t, sid, 0)
	if again == nil {
		t.Fatal("completeImplicitTask must release the turn's memo (EndTurn), and must do so " +
			"before its no-task-to-close early return; otherwise the memo map grows with " +
			"conversation length for the life of the process")
	}
	// The memo is gone but the row is not re-created: the idempotency key still
	// resolves to the same task.
	if again.ID != first.ID {
		t.Errorf("releasing the memo must not mint a duplicate task: got %s, want %s", again.ID, first.ID)
	}
}

// TestCompleteImplicitTask_SurvivesACanceledRequestContext pins the close
// against the one turn shape most likely to need it.
//
// completeImplicitTask runs deferred, so when the turn ended BECAUSE the caller
// canceled, the request context is already dead by the time the close runs.
// CompleteForTurn's own GetTask then failed on the dead context and returned
// silently — leaving the task IN_PROGRESS forever, on exactly the turn whose
// close reason says "canceled". The close now runs on a bounded context derived
// with WithoutCancel.
func TestCompleteImplicitTask_SurvivesACanceledRequestContext(t *testing.T) {
	r := newReclaimRig(t, 0)

	// Mint through a bound context, the way a real turn does.
	ctx, cancel := context.WithCancel(context.Background())
	ctx, binding := taskctx.ContextWithBinding(ctx)
	_, created, err := r.emitter.EnsureForTurn(ctx, task.TurnRequest{
		SessionID:   "sess-cancel",
		AgentID:     "agent-1",
		BoardID:     "sess-cancel",
		TurnIndex:   0,
		Trigger:     loomv1.ImplicitTaskTrigger_IMPLICIT_TASK_TRIGGER_TOOL_CALL,
		UserMessage: "do the thing",
	})
	require.NoError(t, err)
	require.NotNil(t, created)

	// The caller cancels; the turn unwinds; the deferred close runs LAST, on
	// the context the cancellation already killed.
	cancel()
	r.agent.completeImplicitTask(ctx, binding, "sess-cancel", 0, "Turn canceled by the caller.")

	got, err := r.agent.taskManager.GetTask(context.Background(), created.ID)
	require.NoError(t, err)
	require.True(t, task.IsTerminal(got.Status),
		"a canceled turn's task must still close; got status %s", task.StatusName(got.Status))
}
