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

//go:build integration

package postgres

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
)

// TestMessages_RoundTrip_ContextCompilationColumns proves the full
// save→load path through scanMessages on real postgres: every SELECT lists
// the 000019 columns (evicted, folded, turn) and the scan maps them onto
// agent.Message. This is the exact seam where a column/destination mismatch
// makes every postgres session read fail — it must be exercised on a live
// database, not asserted by migration count.
func TestMessages_RoundTrip_ContextCompilationColumns(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)

	userID := uniqueID("user-rt")
	sessionID := createTestSession(t, store, userID, uniqueID("sess-rt"), "rt-agent")
	ctx := ContextWithUserID(context.Background(), userID)

	// Turn 1: user + assistant tool call + tool result. Turn 2: user.
	save := func(msg agent.Message, turnStart bool) {
		t.Helper()
		require.NoError(t, store.SaveMessage(ctx, sessionID, &msg, turnStart))
	}
	save(agent.Message{Role: "user", Content: "q1"}, true)
	save(agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "c1", Name: "query"}}}, false)
	save(agent.Message{Role: "tool", ToolUseID: "c1", Content: "42 rows"}, false)
	save(agent.Message{Role: "user", Content: "q2"}, true)

	// Load back: turn must be derived-at-insert (1,1,1,2), flags false.
	msgs, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err, "LoadMessages must scan every selected column")
	require.Len(t, msgs, 4)
	wantTurns := []int64{1, 1, 1, 2}
	for i, m := range msgs {
		require.Equal(t, wantTurns[i], m.Turn, "msg %d turn", i)
		require.False(t, m.Evicted, "msg %d evicted", i)
		require.False(t, m.Folded, "msg %d folded", i)
	}
	require.Equal(t, "42 rows", msgs[2].Content)
	require.Equal(t, "c1", msgs[2].ToolUseID)

	// Relief flags roundtrip: evict the tool row, fold the first user row.
	toolSeq, err := strconv.ParseInt(msgs[2].ID, 10, 64)
	require.NoError(t, err)
	firstSeq, err := strconv.ParseInt(msgs[0].ID, 10, 64)
	require.NoError(t, err)
	require.NoError(t, store.MarkEvicted(ctx, sessionID, []int64{toolSeq}))
	require.NoError(t, store.FoldMessages(ctx, sessionID, []int64{firstSeq}, 1, "covers msg:1-1\nsummary"))

	msgs, err = store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	// The folded row is filtered at the read; the evicted row returns flagged.
	require.Len(t, msgs, 3, "folded rows are filtered at the database read")
	for _, m := range msgs {
		require.False(t, m.Folded)
	}
	require.True(t, msgs[1].Evicted, "the evicted tool row returns with its flag")
	require.Equal(t, int64(1), msgs[1].Turn, "turn survives the roundtrip")

	// LoadSession drives the same scan through its own SELECT.
	sess, err := store.LoadSession(ctx, sessionID)
	require.NoError(t, err, "LoadSession must scan every selected column")
	require.NotNil(t, sess)
}

// TestMessages_CrossAgentReadsFilterFolded proves the two cross-agent readers
// exclude folded rows, matching LoadSession/LoadMessages and the SQLite store.
// Before the fix both queries selected `folded` and never filtered on it, so a
// postgres deployment leaked folded rows into cross-agent context while SQLite
// did not.
func TestMessages_CrossAgentReadsFilterFolded(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)

	userID := uniqueID("user-xa")
	agentID := uniqueID("agent-xa")
	parentID := createTestSession(t, store, userID, uniqueID("sess-parent"), agentID)
	ctx := ContextWithUserID(context.Background(), userID)

	require.NoError(t, store.SaveMessage(ctx, parentID,
		&agent.Message{Role: "user", Content: "folded-away", SessionContext: "coordinator"}, true))
	require.NoError(t, store.SaveMessage(ctx, parentID,
		&agent.Message{Role: "user", Content: "still-here", SessionContext: "coordinator"}, true))

	msgs, err := store.LoadMessages(ctx, parentID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	foldSeq, err := strconv.ParseInt(msgs[0].ID, 10, 64)
	require.NoError(t, err)
	require.NoError(t, store.FoldMessages(ctx, parentID, []int64{foldSeq}, 1, "covers msg:1-1\nsummary"))

	// Reader 1: by agent.
	byAgent, err := store.LoadMessagesForAgent(ctx, agentID)
	require.NoError(t, err)
	for _, m := range byAgent {
		require.NotEqual(t, "folded-away", m.Content, "LoadMessagesForAgent must not return folded rows")
	}
	require.NotEmpty(t, byAgent, "the unfolded row is still returned")

	// Reader 2: from parent session, via a child session.
	childID := createTestSession(t, store, userID, uniqueID("sess-child"), uniqueID("agent-child"))
	_, err = pool.Exec(ctx, "UPDATE sessions SET parent_session_id = $1 WHERE id = $2", parentID, childID)
	require.NoError(t, err)

	fromParent, err := store.LoadMessagesFromParentSession(ctx, childID)
	require.NoError(t, err)
	require.NotEmpty(t, fromParent, "the unfolded coordinator row is visible to the child")
	for _, m := range fromParent {
		require.NotEqual(t, "folded-away", m.Content, "LoadMessagesFromParentSession must not return folded rows")
	}
}
