// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package loops

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
)

func openStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := agent.OpenDB(agent.DBConfig{Path: filepath.Join(t.TempDir(), "loops.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(context.Background(), db)
	require.NoError(t, err)
	return store
}

func spawn(t *testing.T, s Store, id, workspace string) *Run {
	t.Helper()
	run := &Run{
		ID: id, WorkspaceID: workspace, SessionID: "sess-1",
		SpecName: "cycle",
		Brief:    "fix the flaky test", Dir: "/ws/" + workspace + "/units/" + id,
		Status: Running, StatePath: []string{"triage"}, CapUSD: 8,
	}
	require.NoError(t, s.Create(context.Background(), run))
	return run
}

// A run is a row in the workspace it belongs to, and its identity survives
// being read back: whose it is, and the loop it is of, by name.
func TestRun_IsRecordedAndReadBack(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")

	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, "ws-a", got.WorkspaceID)
	assert.Equal(t, "sess-1", got.SessionID, "the spawning session is provenance")
	assert.Equal(t, "cycle", got.SpecName)
	assert.Equal(t, []string{"triage"}, got.StatePath)
	assert.Equal(t, Running, got.Status)
	assert.EqualValues(t, 8, got.CapUSD)
	assert.False(t, got.CreatedAt.IsZero())

	_, err = s.Get(ctx, "nope")
	assert.ErrorIs(t, err, ErrNotFound)
}

// The list is per workspace, because that is who owns runs. A second
// workspace's run is not in the first's list however it was spawned.
func TestList_IsPerWorkspaceNewestFirst(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	first := spawn(t, s, "r-old", "ws-a")
	first.CreatedAt = time.Now().Add(-time.Hour)
	spawn(t, s, "r-new", "ws-a")
	spawn(t, s, "r-other", "ws-b")

	list, err := s.List(ctx, "ws-a")
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, r := range list {
		assert.Equal(t, "ws-a", r.WorkspaceID)
	}

	other, err := s.List(ctx, "ws-b")
	require.NoError(t, err)
	require.Len(t, other, 1)
	assert.Equal(t, "r-other", other[0].ID)
}

// Progress is called on every transition: where it is now, and what it has
// spent. Both must be readable by anyone watching, which is the whole point of
// runs being rows.
func TestProgress_IsVisibleToAWatcher(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")

	require.NoError(t, s.Progress(ctx, "r-1", []string{"cycle", "impl"}, 1.25))
	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"cycle", "impl"}, got.StatePath, "nested paths survive")
	assert.InDelta(t, 1.25, got.SpentUSD, 0.001)

	assert.ErrorIs(t, s.Progress(ctx, "gone", nil, 0), ErrNotFound)
}

// Parking is what a run does when only a person can answer. It must give up its
// lease at the same moment: a parked run is not being walked by anyone, and the
// next process to look must be free to take it.
func TestPark_StopsForAPersonAndReleasesTheLease(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	require.NoError(t, s.Acquire(ctx, "r-1", "proc-1", time.Minute))

	require.NoError(t, s.Park(ctx, "r-1", ReasonGate))
	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, Waiting, got.Status)
	assert.Equal(t, ReasonGate, got.SuspendReason)
	assert.Equal(t, "proc-1", got.LeaseOwner, "a parked run is still its process's")

	// While that process proves it is alive, nobody else takes it; the same
	// process resumes it freely.
	assert.ErrorIs(t, s.Acquire(ctx, "r-1", "proc-2", time.Minute), ErrLeaseHeld)
	require.NoError(t, s.Acquire(ctx, "r-1", "proc-1", time.Minute))
}

// A new process adopts what a dead one left parked: it takes the lease and
// nothing else. The run is still parked, on the same question, and now the
// new process is the one whose heartbeat says it is alive.
func TestAdopt_TakesAStaleLeaseAndLeavesTheParkAlone(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	require.NoError(t, s.Acquire(ctx, "r-1", "dead/1", time.Minute))
	require.NoError(t, s.Park(ctx, "r-1", ReasonUnroutable))
	require.NoError(t, s.SavePark(ctx, "r-1", []byte("the question")))

	assert.ErrorIs(t, s.Adopt(ctx, "r-1", "proc-2", time.Minute), ErrLeaseHeld, "a fresh lease is not adopted")
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, s.Adopt(ctx, "r-1", "proc-2", time.Millisecond))

	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, Waiting, got.Status)
	assert.Equal(t, ReasonUnroutable, got.SuspendReason)
	assert.Equal(t, []byte("the question"), got.ParkRecord)
	assert.Equal(t, "proc-2", got.LeaseOwner)
	require.NoError(t, s.Heartbeat(ctx, "r-1", "proc-2"), "the adopter can now prove it is alive")
	assert.ErrorIs(t, s.Adopt(ctx, "nope", "proc-2", 0), ErrNotFound)
}

// A finished run keeps its answer: the terminal state it reached and the report
// its caller reads. It is no longer anywhere, so its path is cleared.
func TestFinish_KeepsTheAnswer(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	require.NoError(t, s.Progress(ctx, "r-1", []string{"impl"}, 3))

	require.NoError(t, s.Finish(ctx, "r-1", Done, "merged", "PR #12 merged; 3 tests added"))
	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, Done, got.Status)
	assert.Equal(t, "merged", got.Terminal)
	assert.Contains(t, got.Report, "PR #12")
	assert.Empty(t, got.StatePath)
	assert.False(t, got.EndedAt.IsZero())
	assert.InDelta(t, 3, got.SpentUSD, 0.001, "what it spent is kept")
}

// A finished run taken up again is working again, where it begins, and no
// longer says how it ended; what it did and spent before stands.
func TestReopen_TakesAFinishedRunUpAgain(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	require.NoError(t, s.Moved(ctx, "r-1", []string{"PROVISION"}, map[string]string{"a": "1"}, 2))
	require.NoError(t, s.AppendStep(ctx, &Step{RunID: "r-1", State: "OPEN", Signal: "opened", At: time.Now()}))
	require.NoError(t, s.Finish(ctx, "r-1", Done, "IMPOSSIBLE", "the credential is missing"))

	require.NoError(t, s.Reopen(ctx, "r-1", []string{"PROVISION"}, map[string]string{"a": "1", "b": "2"}))
	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, Running, got.Status)
	assert.Equal(t, []string{"PROVISION"}, got.StatePath)
	assert.Equal(t, map[string]string{"a": "1", "b": "2"}, got.Vars)
	assert.Empty(t, got.Terminal)
	assert.Empty(t, got.Report)
	assert.True(t, got.EndedAt.IsZero(), "a run working again has not ended")
	assert.InDelta(t, 2, got.SpentUSD, 0.001, "what it spent is kept")
	walk, err := s.Walk(ctx, "r-1")
	require.NoError(t, err)
	assert.Len(t, walk, 1, "what it did is kept")
	require.Error(t, s.Reopen(ctx, "no-such-run", nil, nil), "a run that does not exist cannot be taken up")
}

// The lease is what makes one executor and many watchers safe.
func TestLease_OneExecutorAtATime(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")

	require.NoError(t, s.Acquire(ctx, "r-1", "proc-1", time.Minute))
	assert.ErrorIs(t, s.Acquire(ctx, "r-1", "proc-2", time.Minute), ErrLeaseHeld,
		"a second process cannot walk a run someone else is walking")
	assert.NoError(t, s.Acquire(ctx, "r-1", "proc-1", time.Minute),
		"the holder re-taking its own lease is not a collision")

	assert.NoError(t, s.Heartbeat(ctx, "r-1", "proc-1"))
	assert.ErrorIs(t, s.Heartbeat(ctx, "r-1", "proc-2"), ErrLeaseHeld,
		"only the holder can prove the run is alive")

	require.NoError(t, s.Release(ctx, "r-1", "proc-1"))
	assert.NoError(t, s.Acquire(ctx, "r-1", "proc-2", time.Minute), "released means available")

	assert.ErrorIs(t, s.Acquire(ctx, "gone", "proc-1", time.Minute), ErrNotFound)
}

// A process that dies stops proving it is alive. That, and only that, is what
// makes its runs available again — so a crash costs a resume, not a run.
func TestLease_AStaleHolderLosesTheRun(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	require.NoError(t, s.Acquire(ctx, "r-1", "dead-proc", time.Minute))

	// Nothing is stale yet.
	abandoned, err := s.Abandoned(ctx, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, abandoned, "a fresh heartbeat is not abandonment")
	assert.ErrorIs(t, s.Acquire(ctx, "r-1", "live-proc", time.Minute), ErrLeaseHeld)

	// Time passes with no heartbeat: reconcile finds it and can take it.
	time.Sleep(3 * time.Millisecond)
	abandoned, err = s.Abandoned(ctx, time.Millisecond)
	require.NoError(t, err)
	require.Len(t, abandoned, 1)
	assert.Equal(t, "r-1", abandoned[0].ID)
	require.NoError(t, s.Acquire(ctx, "r-1", "live-proc", time.Millisecond))

	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, "live-proc", got.LeaseOwner)
	assert.Equal(t, Running, got.Status)

	// A parked run is never abandoned: nobody is supposed to be holding it.
	require.NoError(t, s.Park(ctx, "r-1", ReasonGate))
	time.Sleep(3 * time.Millisecond)
	abandoned, err = s.Abandoned(ctx, time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, abandoned)
}

// The walk is append-only and ordered, and a state entered twice is two rows —
// that is how circling is visible afterwards.
func TestWalk_IsTheRecordOfWhatHappened(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")

	require.NoError(t, s.AppendStep(ctx, &Step{RunID: "r-1", State: "triage", AgentID: "a1", Signal: "ready", Visit: 1, CostUSD: 0.2, DurationMS: 900}))
	require.NoError(t, s.AppendStep(ctx, &Step{RunID: "r-1", State: "impl", AgentID: "a2", Signal: "done", Visit: 1, CostUSD: 1.1}))
	require.NoError(t, s.AppendStep(ctx, &Step{RunID: "r-1", State: "test", Tool: "shell_execute", Signal: "failed", Visit: 1}))
	require.NoError(t, s.AppendStep(ctx, &Step{RunID: "r-1", State: "impl", AgentID: "a2", Signal: "done", Visit: 2}))

	walk, err := s.Walk(ctx, "r-1")
	require.NoError(t, err)
	require.Len(t, walk, 4)
	for i, st := range walk {
		assert.Equal(t, i+1, st.Seq, "the store assigns the sequence")
	}
	assert.Equal(t, []string{"triage", "impl", "test", "impl"}, []string{walk[0].State, walk[1].State, walk[2].State, walk[3].State})
	assert.Equal(t, 2, walk[3].Visit, "coming round again is visible")
	assert.Equal(t, "shell_execute", walk[2].Tool)
	assert.Empty(t, walk[2].AgentID, "a tool state has no agent")
	assert.InDelta(t, 0.2, walk[0].CostUSD, 0.001)

	empty, err := s.Walk(ctx, "r-2")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// What a park leaves behind rides on the run's row, exactly as the engine
// produced it, until the run is resumed.
func TestSavePark_RidesOnTheRun(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")

	before, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Empty(t, before.ParkRecord, "a run that never parked has none")

	record := []byte{0x0a, 0x05, 'p', 'a', 'r', 'k', '!'}
	require.NoError(t, s.SavePark(ctx, "r-1", record))
	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, record, got.ParkRecord, "the bytes are kept as the engine produced them")

	require.NoError(t, s.SavePark(ctx, "r-1", nil))
	cleared, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Empty(t, cleared.ParkRecord, "a resume clears it")
}

// Delegation makes a tree of runs: a state that delegates gets children, and
// finding them is how the parent is woken with their outcomes.
func TestChildren_AreTheDelegationTree(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "parent", "ws-a")

	for _, id := range []string{"child-1", "child-2"} {
		child := spawn(t, s, id, "ws-a")
		child.ParentRunID, child.ParentState = "parent", "fan"
		require.NoError(t, s.Finish(ctx, id, Done, "passed", ""))
	}
	// Re-create with the parent set, which is how a delegated run is spawned.
	require.NoError(t, s.Create(ctx, &Run{ID: "child-3", WorkspaceID: "ws-a", ParentRunID: "parent",
		ParentState: "fan", SpecName: "qa", Dir: "/d", Status: Running}))

	children, err := s.Children(ctx, "parent")
	require.NoError(t, err)
	require.Len(t, children, 1, "only runs recorded with the parent are its children")
	assert.Equal(t, "child-3", children[0].ID)
	assert.Equal(t, "fan", children[0].ParentState)
}

// A ceiling raised on resume lives on the run, beside the cap it was spawned
// with, which stays what it was.
func TestGrant_RaisesCeilingsOnTheRun(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")

	require.NoError(t, s.Grant(ctx, "r-1", 5, 40))
	require.NoError(t, s.Grant(ctx, "r-1", 2.5, 10))

	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.InDelta(t, 7.5, got.GrantedCostUSD, 0.001, "grants accumulate")
	assert.Equal(t, 50, got.GrantedTransitions)
	assert.InDelta(t, 8, got.CapUSD, 0.001, "the cap it was spawned with is untouched")
}

// A stop is a park from outside. The run's place survives it exactly as a
// park's does — state path, vars, park record — because the only difference
// between the two is who stopped the run and whether a question is open.
func TestStop_IsAParkFromOutsideAndKeepsThePlace(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	require.NoError(t, s.Acquire(ctx, "r-1", "proc-1", time.Minute))
	require.NoError(t, s.Moved(ctx, "r-1", []string{"fix"}, map[string]string{"branch": "b"}, 0.5))

	require.NoError(t, s.Stop(ctx, "r-1"))
	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, Stopped, got.Status)
	assert.Equal(t, []string{"fix"}, got.StatePath, "a stopped run is still somewhere")
	assert.Equal(t, "b", got.Vars["branch"])
	assert.Empty(t, got.LeaseOwner, "a stopped run is held by nobody")

	// Which is what lets it be resumed: the lease is free and the place is there.
	require.NoError(t, s.Acquire(ctx, "r-1", "proc-2", time.Minute))
	got, err = s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, Running, got.Status)

	assert.ErrorIs(t, s.Stop(ctx, "nope"), ErrNotFound)
}

// Discard is the one exit that is not the walk's own. It takes the run, its
// steps, and everything delegated under it, and frees the id.
func TestDiscard_RemovesTheRunAndItsTree(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	for _, r := range []*Run{
		{ID: "r-1:build", ParentRunID: "r-1", SpecName: "cycle:build"},
		{ID: "r-1:build:test", ParentRunID: "r-1:build", SpecName: "cycle:build:test"},
	} {
		r.WorkspaceID, r.Dir = "ws-a", "/ws"
		require.NoError(t, s.Create(ctx, r))
	}
	require.NoError(t, s.AppendStep(ctx, &Step{RunID: "r-1", State: "triage", Signal: "ok", At: time.Now()}))
	require.NoError(t, s.AppendStep(ctx, &Step{RunID: "r-1:build:test", State: "a", Signal: "ok", At: time.Now()}))
	spawn(t, s, "r-2", "ws-a") // a neighbour, untouched

	require.NoError(t, s.Discard(ctx, "r-1"))

	for _, id := range []string{"r-1", "r-1:build", "r-1:build:test"} {
		_, err := s.Get(ctx, id)
		assert.ErrorIs(t, err, ErrNotFound, id)
		steps, err := s.Walk(ctx, id)
		require.NoError(t, err)
		assert.Empty(t, steps, id)
	}
	_, err := s.Get(ctx, "r-2")
	assert.NoError(t, err, "the neighbour stays")
	assert.ErrorIs(t, s.Discard(ctx, "r-1"), ErrNotFound, "gone is gone")

	// The id is free again.
	spawn(t, s, "r-1", "ws-a")
}

// Two kinds of row never hold a lease: an agent run, which is routing's, and
// a delegated child, which is walked inside its parent. Neither is abandoned
// in the loop's sense, so reconcile must not see them — and ones an earlier
// reconcile did park are put back on the store's next open.
func TestAbandoned_LeavesAgentRunsAndChildrenAlone(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a") // a loop run, never heartbeated: abandoned
	require.NoError(t, s.CreateAgentRun(ctx, &Run{ID: "a-1", WorkspaceID: "ws-a", SessionID: "conv",
		AgentID: "onboarding-loop", Kind: Connected, OwnSession: "own-1", Status: Running}))
	require.NoError(t, s.Create(ctx, &Run{ID: "r-1:build", WorkspaceID: "ws-a", ParentRunID: "r-1",
		SpecName: "cycle:build", Dir: "/ws", Status: Running, StatePath: []string{"test"}}))

	got, err := s.Abandoned(ctx, time.Millisecond)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "r-1", got[0].ID)

	// The damage an earlier process did is undone when the store is opened.
	require.NoError(t, s.Park(ctx, "a-1", ReasonProcessAbandoned))
	require.NoError(t, s.Park(ctx, "r-1:build", ReasonProcessAbandoned))
	reopened, err := NewSQLiteStore(ctx, s.db)
	require.NoError(t, err)
	for _, id := range []string{"a-1", "r-1:build"} {
		run, err := reopened.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, Running, run.Status, id)
		assert.Empty(t, run.SuspendReason, id)
	}
	child, _ := reopened.Get(ctx, "r-1:build")
	assert.Equal(t, []string{"test"}, child.StatePath, "the child still stands where it stood")
}

// Taking the lease is what makes a run running again, and a run that is
// running is not parked on anything: the reason it stopped goes with the park.
func TestAcquire_ClearsWhyTheRunHadStopped(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r-1", "ws-a")
	require.NoError(t, s.Park(ctx, "r-1", ReasonUnroutable))
	require.NoError(t, s.Acquire(ctx, "r-1", "proc-2", time.Minute))
	got, err := s.Get(ctx, "r-1")
	require.NoError(t, err)
	assert.Equal(t, Running, got.Status)
	assert.Empty(t, got.SuspendReason)
}

// A manager's word waits on its parked run, survives the process — it is on
// the row — and is spent when the run is taken up again.
func TestAHeldWordWaitsOnTheRowUntilTheRunIsTakenUp(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	spawn(t, s, "r1", "ws")
	require.NoError(t, s.Park(ctx, "r1", ReasonUnroutable))

	require.NoError(t, s.HoldWord(ctx, "r1", `{"action":"approve"}`))
	run, err := s.Get(ctx, "r1")
	require.NoError(t, err)
	require.Equal(t, `{"action":"approve"}`, run.Word)

	require.NoError(t, s.Acquire(ctx, "r1", "me", time.Minute))
	run, err = s.Get(ctx, "r1")
	require.NoError(t, err)
	require.Empty(t, run.Word, "taking the run up did not spend the word")
	require.ErrorIs(t, s.HoldWord(ctx, "no-such-run", "x"), ErrNotFound)
}
