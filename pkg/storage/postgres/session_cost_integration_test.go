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

package postgres

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// The tests in this file run in the ordinary gate: CI sets TEST_POSTGRES_URL,
// and a run without it skips (migratedAdminPool). They carry no integration
// tag, so their helpers are here and not in the tagged rls_integration_test.go.

// costTestPool is a migrated pool on TEST_POSTGRES_URL, or a skip.
// costTestPool is a pool on a role that row-level security applies to
// (rlsSubjectPool), so the owner tests prove the policies and the user_id
// predicate together, not the predicate alone on a superuser connection.
func costTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return rlsSubjectPool(t, migratedAdminPool(t))
}

// costTestStore is a SessionStore on pool with a no-op tracer.
func costTestStore(pool *pgxpool.Pool) *SessionStore {
	return NewSessionStore(pool, observability.NewNoOpTracer(), nil)
}

// costTestSession saves a session that userID owns and deletes it at cleanup.
// It returns sessionID.
func costTestSession(t *testing.T, store *SessionStore, userID, sessionID, agentID string) string {
	t.Helper()
	ctx := ContextWithUserID(context.Background(), userID)
	sess := &agent.Session{ID: sessionID, AgentID: agentID, UserID: userID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	require.NoError(t, store.SaveSession(ctx, sess), "failed to create test session for user %s", userID)
	t.Cleanup(func() { _ = store.DeleteSession(ContextWithUserID(context.Background(), userID), sessionID) })
	return sessionID
}

// A session's cost is the sum of what its rows cost, the rows relief folded
// away included, and it is the owner's to read: another owner sums to zero.
func TestSessionCost_SumsEveryRowForItsOwner(t *testing.T) {
	pool := costTestPool(t)
	store := costTestStore(pool)
	var _ agent.CostStorage = store

	owner := hrUniqueID("user-cost")
	sessionID := costTestSession(t, store, owner, hrUniqueID("sess-cost"), "cost-agent")
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

	other := ContextWithUserID(context.Background(), hrUniqueID("user-other"))
	usd, err = store.SessionCost(other, sessionID)
	require.NoError(t, err)
	require.Zero(t, usd, "another owner read the session's cost")
}

// RewriteToolResults rewrites the owner's tool rows and nothing else.
func TestRewriteToolResults_IsTheOwners(t *testing.T) {
	pool := costTestPool(t)
	store := costTestStore(pool)
	var rw agent.ResultRewriter = store

	owner := hrUniqueID("user-rw")
	sessionID := costTestSession(t, store, owner, hrUniqueID("sess-rw"), "rw-agent")
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

	other := ContextWithUserID(context.Background(), hrUniqueID("user-other"))
	require.ErrorIs(t, rw.RewriteToolResults(other, sessionID, []int64{toolSeq}, "not yours"), agent.ErrNoToolResultsRewritten,
		"a rewrite that changed no row must say so")
	msgs, err = store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, "rows", msgs[2].Content, "another owner rewrote the row")

	require.NoError(t, rw.RewriteToolResults(ctx, sessionID, []int64{toolSeq, userSeq}, "the note"))
	msgs, err = store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, "the note", msgs[2].Content)
	require.Equal(t, "q", msgs[0].Content, "a row that is not a tool result was rewritten")
}

// AddRowUsage adds the cost and the carried token figures to the owner's row
// only, and the session's cost follows. A row that is not there, or is
// another owner's, is an error.
func TestAddRowUsage_IsTheOwners(t *testing.T) {
	pool := costTestPool(t)
	store := costTestStore(pool)
	var adder agent.RowUsageAdder = store

	owner := hrUniqueID("user-add")
	sessionID := costTestSession(t, store, owner, hrUniqueID("sess-add"), "add-agent")
	ctx := ContextWithUserID(context.Background(), owner)
	user := agent.Message{Role: "user", Content: "q", InputTokens: 3}
	require.NoError(t, store.SaveMessage(ctx, sessionID, &user, true))
	seq, err := strconv.ParseInt(user.ID, 10, 64)
	require.NoError(t, err)
	u := agent.Usage{CostUSD: 0.25, TotalTokens: 110, InputTokens: 100, OutputTokens: 10, ThinkingTokens: 4}

	other := ContextWithUserID(context.Background(), hrUniqueID("user-other"))
	require.Error(t, adder.AddRowUsage(other, sessionID, seq, u), "another owner added usage")
	require.Error(t, adder.AddRowUsage(ctx, sessionID, seq+100000, u), "a missing row took the usage")

	require.NoError(t, adder.AddRowUsage(ctx, sessionID, seq, u))
	usd, err := store.SessionCost(ctx, sessionID)
	require.NoError(t, err)
	require.InDelta(t, 0.25, usd, 1e-9, "the session cost does not hold the added usage")
	msgs, err := store.LoadMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, 110, msgs[0].TokenCount)
	require.Equal(t, 10, msgs[0].OutputTokens)
	require.Equal(t, 4, msgs[0].ThinkingTokens)
	require.Equal(t, 3, msgs[0].InputTokens, "the row's input figure is its own prompt, not the added call's")
}
