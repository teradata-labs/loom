// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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

// A session's cost is the sum of what its rows cost, the rows relief folded
// away included, and it is the owner's to read: another owner sums to zero.
func TestSessionCost_SumsEveryRowForItsOwner(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)
	var _ agent.CostStorage = store

	owner := uniqueID("user-cost")
	sessionID := createTestSession(t, store, owner, uniqueID("sess-cost"), "cost-agent")
	ctx := ContextWithUserID(context.Background(), owner)
	save := func(msg agent.Message, turnStart bool) {
		t.Helper()
		require.NoError(t, store.SaveMessage(ctx, sessionID, &msg, turnStart))
	}
	save(agent.Message{Role: "user", Content: "q"}, true)
	save(agent.Message{Role: "assistant", Content: "a", CostUSD: 0.25}, false)
	save(agent.Message{Role: "user", Content: "q2"}, true)
	save(agent.Message{Role: "assistant", Content: "a2", CostUSD: 0.5}, false)

	usd, err := store.SessionCost(ctx, sessionID)
	require.NoError(t, err)
	require.InDelta(t, 0.75, usd, 1e-9)

	// Fold the first turn away: the ledger still holds it.
	msgs, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	seq, err := strconv.ParseInt(msgs[1].ID, 10, 64)
	require.NoError(t, err)
	require.NoError(t, store.FoldMessages(ctx, sessionID, []int64{seq}, 1, "covers msg:1-1\nsummary"))
	usd, err = store.SessionCost(ctx, sessionID)
	require.NoError(t, err)
	require.InDelta(t, 0.75, usd, 1e-9, "a folded row left the ledger")

	other := ContextWithUserID(context.Background(), uniqueID("user-other"))
	usd, err = store.SessionCost(other, sessionID)
	require.NoError(t, err)
	require.Zero(t, usd, "another owner read the session's cost")
}

// RewriteToolResults rewrites the owner's tool rows and nothing else.
func TestRewriteToolResults_IsTheOwners(t *testing.T) {
	pool := testPool(t)
	store := testSessionStore(t, pool)
	var rw agent.ResultRewriter = store

	owner := uniqueID("user-rw")
	sessionID := createTestSession(t, store, owner, uniqueID("sess-rw"), "rw-agent")
	ctx := ContextWithUserID(context.Background(), owner)
	for _, m := range []agent.Message{{Role: "user", Content: "q"}, {Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "c1", Name: "rows"}}}, {Role: "tool", ToolUseID: "c1", Content: "rows"}} {
		mm := m
		require.NoError(t, store.SaveMessage(ctx, sessionID, &mm, m.Role == "user"))
	}
	msgs, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	toolSeq, err := strconv.ParseInt(msgs[2].ID, 10, 64)
	require.NoError(t, err)
	userSeq, err := strconv.ParseInt(msgs[0].ID, 10, 64)
	require.NoError(t, err)

	other := ContextWithUserID(context.Background(), uniqueID("user-other"))
	require.NoError(t, rw.RewriteToolResults(other, sessionID, []int64{toolSeq}, "not yours"))
	msgs, err = store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, "rows", msgs[2].Content, "another owner rewrote the row")

	require.NoError(t, rw.RewriteToolResults(ctx, sessionID, []int64{toolSeq, userSeq}, "the note"))
	msgs, err = store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, "the note", msgs[2].Content)
	require.Equal(t, "q", msgs[0].Content, "a row that is not a tool result was rewritten")
}
