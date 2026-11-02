// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package loops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func giveWork(t *testing.T, s *SQLiteStore, id, conversation, agentID string, kind Kind) *Run {
	t.Helper()
	run := &Run{
		ID: id, WorkspaceID: "ws-1", SessionID: conversation,
		AgentID: agentID, Kind: kind, OwnSession: "own-" + id,
		Brief: "plan the release loop",
	}
	require.NoError(t, s.CreateAgentRun(context.Background(), run))
	return run
}

// TestAgentRunNeedsAnAgentAKindAndItsOwnSession: all three are what make the row
// routable. A row missing any of them would be created, listed, and then fail
// when something tried to deliver a message to it — so the refusal belongs at
// the write.
func TestAgentRunNeedsAnAgentAKindAndItsOwnSession(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		run  *Run
	}{
		{"no agent", &Run{ID: "r", WorkspaceID: "ws", Kind: Connected, OwnSession: "s"}},
		{"no session", &Run{ID: "r", WorkspaceID: "ws", Kind: Connected, AgentID: "a"}},
		{"no kind", &Run{ID: "r", WorkspaceID: "ws", AgentID: "a", OwnSession: "s"}},
		{"bad kind", &Run{ID: "r", WorkspaceID: "ws", AgentID: "a", OwnSession: "s", Kind: "nested"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, s.CreateAgentRun(ctx, tc.run))
		})
	}
}

// TestAgentRunsAndLoopRunsShareTheTableWithoutSeeingEachOther is the whole point
// of one table: routing asks what work is out and gets only agent work, while
// the loop engine's own reads are untouched. Get the discriminator wrong and a
// loop run appears in the conversation's run list, where nothing can deliver to
// it.
func TestAgentRunsAndLoopRunsShareTheTableWithoutSeeingEachOther(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	spawn(t, s, "loop-1", "ws-1") // a spec's run, session_id = sess-1
	agentRun := giveWork(t, s, "r1", "sess-1", "spec-writer", Connected)

	open, err := s.OpenAgentRuns(ctx, "sess-1")
	require.NoError(t, err)
	require.Len(t, open, 1, "the loop run must not appear in routing's list")
	assert.Equal(t, agentRun.ID, open[0].ID)

	// And the loop's own read still sees both, because it asks a different
	// question: what work belongs to this workspace.
	all, err := s.List(ctx, "ws-1")
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Asking for an agent run by a loop run's id is a miss, not a silent
	// substitution.
	_, err = s.AgentRun(ctx, "loop-1")
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestAgentRunRoundTripsItsRoutingFields guards the columns the scan reads. A
// column added to the table but missed in scanRun reads as the zero value, which
// silently makes every run look spawned and unattributed.
func TestAgentRunRoundTripsItsRoutingFields(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	giveWork(t, s, "r1", "sess-1", "spec-writer", Connected)

	got, err := s.AgentRun(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, "spec-writer", got.AgentID)
	assert.Equal(t, Connected, got.Kind)
	assert.Equal(t, "own-r1", got.OwnSession)
	assert.Equal(t, "sess-1", got.SessionID, "who gave out the work")
	assert.Equal(t, Running, got.Status)

	assert.Empty(t, got.SpecName, "a routing run has no spec, and that is the discriminator")
}

// TestOpenAgentRunsIsWhatTheRouterClassifiesAgainst: it must hold everything
// still continuable and nothing finished. A finished run left in the list makes
// the router offer to continue work that is over; a waiting run left out makes
// it start the same work twice.
func TestOpenAgentRunsIsWhatTheRouterClassifiesAgainst(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	giveWork(t, s, "working", "sess-1", "builder", Spawned)
	giveWork(t, s, "waiting", "sess-1", "builder", Spawned)
	giveWork(t, s, "finished", "sess-1", "spec-writer", Connected)
	giveWork(t, s, "elsewhere", "sess-2", "builder", Spawned)

	require.NoError(t, s.Finish(ctx, "finished", Done, "", "spec written"))

	open, err := s.OpenAgentRuns(ctx, "sess-1")
	require.NoError(t, err)
	var ids []string
	for _, r := range open {
		ids = append(ids, r.ID)
	}
	assert.ElementsMatch(t, []string{"working", "waiting"}, ids)
}

// TestConnectedRunsSurviveFinishing: what was said in a conversation stays part
// of it. Filter the thread on open runs and yesterday's planning disappears from
// the transcript the moment the planner hands back.
func TestConnectedRunsSurviveFinishing(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	giveWork(t, s, "r1", "sess-1", "spec-writer", Connected)
	giveWork(t, s, "r2", "sess-1", "builder", Spawned)
	require.NoError(t, s.Finish(ctx, "r1", Done, "", "spec written"))

	connected, err := s.ConnectedAgentRuns(ctx, "sess-1")
	require.NoError(t, err)
	require.Len(t, connected, 1, "the spawned run is not part of the thread")
	assert.Equal(t, "r1", connected[0].ID)
}

// TestStoppingOpenRunsClosesOutAConversation: clearing a conversation makes its
// runs unreachable, because the router only ever classifies against the runs of
// the conversation it is in. Left running they would be work nothing can finish,
// answer or stop.
func TestStoppingOpenRunsClosesOutAConversation(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	giveWork(t, s, "r1", "sess-1", "spec-writer", Connected)
	giveWork(t, s, "r2", "sess-1", "builder", Spawned)
	giveWork(t, s, "r3", "sess-2", "builder", Spawned)

	n, err := s.StopOpenAgentRuns(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, 2, n, "both the working and the waiting run are closed out")

	open, err := s.OpenAgentRuns(ctx, "sess-1")
	require.NoError(t, err)
	assert.Empty(t, open)

	// Another conversation's work is untouched.
	other, err := s.OpenAgentRuns(ctx, "sess-2")
	require.NoError(t, err)
	assert.Len(t, other, 1)
}

// TestRoutingColumnsAreAddedToAnExistingDatabase: the columns arrive by ALTER, so
// a database written before them must gain them on open rather than failing. The
// second open is the case that matters — a re-add must be recognised and ignored,
// not treated as a schema failure.
func TestRoutingColumnsAreAddedToAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	giveWork(t, s, "r1", "sess-1", "spec-writer", Connected)

	// Re-opening over the same handle runs every statement again.
	again, err := NewSQLiteStore(ctx, s.db)
	require.NoError(t, err, "a second open must not fail on columns that already exist")

	got, err := again.AgentRun(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, "spec-writer", got.AgentID)
}

// TestAllAgentRunsKeepsFinishedWork. The conversation's own agent asks what it
// has handed out; a list that drops finished work cannot say what was made, what
// failed, or what a follow-up refers to.
func TestAllAgentRunsKeepsFinishedWork(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	giveWork(t, s, "running", "sess-1", "builder", Spawned)
	giveWork(t, s, "finished", "sess-1", "spec-writer", Connected)
	giveWork(t, s, "broken", "sess-1", "builder", Spawned)
	giveWork(t, s, "elsewhere", "sess-2", "builder", Spawned)
	spawn(t, s, "loop-1", "ws-1") // a spec's run, same conversation

	require.NoError(t, s.Finish(ctx, "finished", Done, "", "spec written"))
	require.NoError(t, s.Finish(ctx, "broken", Failed, "", "could not build"))

	all, err := s.AllAgentRuns(ctx, "sess-1")
	require.NoError(t, err)
	var ids []string
	for _, r := range all {
		ids = append(ids, r.ID)
	}
	assert.Equal(t, []string{"running", "finished", "broken"}, ids,
		"want every agent run of the conversation, oldest first, and no loop run")

	// Open is the narrower question, and still answers it.
	open, err := s.OpenAgentRuns(ctx, "sess-1")
	require.NoError(t, err)
	require.Len(t, open, 1)
	assert.Equal(t, "running", open[0].ID)
}
