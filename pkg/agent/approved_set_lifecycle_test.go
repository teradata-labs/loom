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

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// The approved set is session-keyed authorization state whose lifecycle is
// owned by the AGENT's retirement and rebuild paths — these tests state the
// invariant over those paths, driven through Agent, not through the shuttle
// primitive: membership survives a rebuild and does not survive a retire.

func sessionCtx(id string) context.Context {
	return session.WithSessionID(context.Background(), id)
}

func recordApproval(t *testing.T, ag *Agent, sessionID, id string) {
	t.Helper()
	require.NoError(t, ag.ApprovedSet().Record(sessionCtx(sessionID), "approved_stmts",
		[]shuttle.CallIdentity{shuttle.CallIdentity(id)}))
}

func isApproved(t *testing.T, ag *Agent, sessionID, id string) bool {
	t.Helper()
	ok, err := ag.ApprovedSet().Contains(sessionCtx(sessionID), "approved_stmts", shuttle.CallIdentity(id))
	require.NoError(t, err)
	return ok
}

func TestApprovedSet_RetirementPathsDropBuckets(t *testing.T) {
	ag := NewAgent(nil, nil, WithName("lifecycle"))
	ag.AdoptApprovedSet(shuttle.NewApprovedSet())

	// DeleteSession retires exactly its own session's approvals.
	recordApproval(t, ag, "s1", "stmt-a")
	recordApproval(t, ag, "s2", "stmt-b")
	ag.DeleteSession("s1")
	require.False(t, isApproved(t, ag, "s1", "stmt-a"), "a deleted session's approvals are dropped")
	require.True(t, isApproved(t, ag, "s2", "stmt-b"), "another session's approvals survive")

	// ClearAllSessions retires every session's approvals — the sibling
	// retirement path must hold the same bound.
	ag.CreateSession(context.Background(), "s2", "second")
	ag.ClearAllSessions()
	require.False(t, isApproved(t, ag, "s2", "stmt-b"),
		"clearing all sessions must free every approved-set bucket, or the interface's live-sessions bound is false")
}

func TestApprovedSet_SurvivesRebuildPaths(t *testing.T) {
	old := NewAgent(nil, nil, WithName("outgoing"))
	old.AdoptApprovedSet(shuttle.NewApprovedSet())
	recordApproval(t, old, "s1", "stmt-a")

	// A hot swap hands the outgoing agent's set to its replacement: a rebuild
	// is an operator action on the agent, not on its sessions.
	replacement := NewAgent(nil, nil, WithName("replacement"))
	replacement.AdoptApprovedSet(old.ApprovedSet())
	require.True(t, isApproved(t, replacement, "s1", "stmt-a"),
		"a recorded approval must survive the agent rebuild")

	// SetSharedMemory must not clobber a live set (the reload path calls it
	// on the replacement after adoption in some orderings).
	replacement.SetSharedMemory(nil)
	require.True(t, isApproved(t, replacement, "s1", "stmt-a"),
		"SetSharedMemory must never discard live sessions' recorded approvals")
}
