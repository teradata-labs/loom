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
// Tests for per-user session isolation at the SQLite storage layer
// (migration 000009, MCP 2026-07-28 Immediate brief Part A).
package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

func newOwnershipTestStore(t *testing.T) *SessionStore {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func ownedSession(id string) *Session {
	now := time.Now()
	return &Session{ID: id, Name: "n", CreatedAt: now, UpdatedAt: now, Context: map[string]interface{}{}}
}

func userCtx(user string) context.Context {
	return types.ContextWithUserID(context.Background(), user)
}

func TestSQLiteSessionIsolation(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA := userCtx("user-a")
	ctxB := userCtx("user-b")

	require.NoError(t, store.SaveSession(ctxA, ownedSession("sess-a")))

	// Owner loads fine and the owner is recorded.
	loaded, err := store.LoadSession(ctxA, "sess-a")
	require.NoError(t, err)
	assert.Equal(t, "user-a", loaded.UserID)

	// Wrong owner: indistinguishable from not-found.
	_, err = store.LoadSession(ctxB, "sess-a")
	require.Error(t, err)
	_, notFoundErr := store.LoadSession(ctxB, "does-not-exist")
	require.Error(t, notFoundErr)

	// Lists are filtered per user.
	require.NoError(t, store.SaveSession(ctxB, ownedSession("sess-b")))
	idsA, err := store.ListSessions(ctxA)
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-a"}, idsA)
	idsB, err := store.ListSessions(ctxB)
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-b"}, idsB)

	// Cross-user delete is a silent no-op.
	require.NoError(t, store.DeleteSession(ctxB, "sess-a"))
	_, err = store.LoadSession(ctxA, "sess-a")
	require.NoError(t, err, "foreign delete must not remove the owner's session")
}

func TestSQLiteUpsertCannotClobberForeignSession(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA := userCtx("user-a")
	ctxB := userCtx("user-b")

	original := ownedSession("shared-id")
	original.Name = "owned-by-a"
	require.NoError(t, store.SaveSession(ctxA, original))

	// B saves a session reusing A's ID: the ownership-guarded upsert must
	// leave A's row untouched.
	intruder := ownedSession("shared-id")
	intruder.Name = "owned-by-b"
	require.NoError(t, store.SaveSession(ctxB, intruder))

	loaded, err := store.LoadSession(ctxA, "shared-id")
	require.NoError(t, err)
	assert.Equal(t, "owned-by-a", loaded.Name, "foreign upsert must not overwrite the row")
	assert.Equal(t, "user-a", loaded.UserID)

	// And B did not acquire the session either.
	_, err = store.LoadSession(ctxB, "shared-id")
	require.Error(t, err)
}

func TestSQLiteDefaultIdentityRoundTrip(t *testing.T) {
	// Local single-tenant mode: no ctx identity → default-user owner, and
	// everything behaves exactly as before migration 000009.
	store := newOwnershipTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SaveSession(ctx, ownedSession("local")))
	loaded, err := store.LoadSession(ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, types.DefaultUserID, loaded.UserID)

	ids, err := store.ListSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"local"}, ids)

	require.NoError(t, store.DeleteSession(ctx, "local"))
	_, err = store.LoadSession(ctx, "local")
	require.Error(t, err)
}

func TestSQLiteLoadAgentSessionsFiltered(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA := userCtx("user-a")
	ctxB := userCtx("user-b")

	sessA := ownedSession("agent-sess-a")
	sessA.AgentID = "agent-1"
	require.NoError(t, store.SaveSession(ctxA, sessA))
	sessB := ownedSession("agent-sess-b")
	sessB.AgentID = "agent-1"
	require.NoError(t, store.SaveSession(ctxB, sessB))

	idsA, err := store.LoadAgentSessions(ctxA, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-sess-a"}, idsA)
}

// TestSQLiteChildDataGuardedByOwnedParent (review finding 5, PR #328):
// message reads and writes must be guarded through the owned parent —
// predicating on session_id alone would let any caller who learns an ID
// read or write another user's conversation.
func TestSQLiteChildDataGuardedByOwnedParent(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA := userCtx("user-a")
	ctxB := userCtx("user-b")

	require.NoError(t, store.SaveSession(ctxA, ownedSession("sess-child")))
	require.NoError(t, store.SaveMessage(ctxA, "sess-child", &Message{Role: "user", Content: "mine"}, true))

	// Foreign write: rejected as not-found (never revealing existence).
	err := store.SaveMessage(ctxB, "sess-child", &Message{Role: "user", Content: "intruder"}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Foreign read: empty, indistinguishable from a missing session.
	msgs, err := store.LoadMessages(ctxB, "sess-child")
	require.NoError(t, err)
	assert.Empty(t, msgs)

	// Owner still reads their own conversation.
	msgs, err = store.LoadMessages(ctxA, "sess-child")
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "mine", msgs[0].Content)

	// Foreign tool-execution and snapshot writes are rejected the same way.
	require.Error(t, store.SaveToolExecution(ctxB, "sess-child", ToolExecution{ToolName: "x"}))
	require.Error(t, store.SaveMemorySnapshot(ctxB, "sess-child", "summary", "s", 1))
}

// TestSQLiteDeleteForeignSessionHasNoSideEffects (review finding 5, PR #328):
// deleting a foreign session is a complete no-op — no cleanup hooks, no
// filesystem removal — because the owner-predicated DELETE affected no row.
func TestSQLiteDeleteForeignSessionHasNoSideEffects(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA := userCtx("user-a")
	ctxB := userCtx("user-b")

	require.NoError(t, store.SaveSession(ctxA, ownedSession("sess-del")))

	hookCalls := 0
	store.RegisterCleanupHook(func(ctx context.Context, sessionID string) {
		hookCalls++
	})

	// Foreign delete: silent no-op, hooks must not fire.
	require.NoError(t, store.DeleteSession(ctxB, "sess-del"))
	assert.Zero(t, hookCalls, "cleanup hooks must not fire for a no-op delete")

	// The owner's session is intact.
	loaded, err := store.LoadSession(ctxA, "sess-del")
	require.NoError(t, err)
	require.NotNil(t, loaded)

	// The owner's delete fires hooks exactly once.
	require.NoError(t, store.DeleteSession(ctxA, "sess-del"))
	assert.Equal(t, 1, hookCalls)
}

// TestSQLiteSaveSessionContextIsAuthoritative (review finding 5, PR #328):
// a caller cannot claim another owner by presetting session.UserID — the
// authenticated context wins.
func TestSQLiteSaveSessionContextIsAuthoritative(t *testing.T) {
	store := newOwnershipTestStore(t)

	forged := ownedSession("sess-forged")
	forged.UserID = "victim"
	require.NoError(t, store.SaveSession(userCtx("attacker"), forged))

	// The row belongs to the authenticated caller, not the claimed victim.
	loaded, err := store.LoadSession(userCtx("attacker"), "sess-forged")
	require.NoError(t, err)
	require.NotNil(t, loaded)

	fromVictim, err := store.LoadSession(userCtx("victim"), "sess-forged")
	require.Error(t, err, "the claimed owner must not receive the attacker's session")
	_ = fromVictim
}

// setupTwoUserCorpus stores one session per user, each with a distinctive
// searchable message, and returns the two identity contexts.
func setupTwoUserCorpus(t *testing.T, store *SessionStore) (ctxA, ctxB context.Context) {
	t.Helper()
	ctxA, ctxB = userCtx("user-a"), userCtx("user-b")
	sa := ownedSession("sess-a")
	sa.AgentID = "agent-x"
	sb := ownedSession("sess-b")
	sb.AgentID = "agent-x"
	require.NoError(t, store.SaveSession(ctxA, sa))
	require.NoError(t, store.SaveSession(ctxB, sb))
	require.NoError(t, store.SaveMessage(ctxA, "sess-a", &Message{Role: "user", Content: "alpha secret ledger"}, true))
	require.NoError(t, store.SaveMessage(ctxB, "sess-b", &Message{Role: "user", Content: "beta secret ledger"}, true))
	return ctxA, ctxB
}

func TestSQLiteSearchMessagesScopedToOwner(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA, ctxB := setupTwoUserCorpus(t, store)

	// All-sessions search returns only the caller's rows.
	got, err := store.SearchMessages(ctxA, "", "secret ledger", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Content, "alpha")

	// Naming the foreign session explicitly yields nothing.
	got, err = store.SearchMessages(ctxB, "sess-a", "secret ledger", 10)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSQLiteSearchMessagesByAgentScopedToOwner(t *testing.T) {
	store := newOwnershipTestStore(t)
	_, ctxB := setupTwoUserCorpus(t, store)

	// Both users share agent-x; each search sees only its own corpus.
	got, err := store.SearchMessagesByAgent(ctxB, "agent-x", "secret ledger", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Content, "beta")
}

func TestSQLiteLoadMessagesForAgentScopedToOwner(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA, _ := setupTwoUserCorpus(t, store)

	got, err := store.LoadMessagesForAgent(ctxA, "agent-x")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Content, "alpha")
}

func TestSQLiteGetStatsScopedToOwner(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA, _ := setupTwoUserCorpus(t, store)
	require.NoError(t, store.SaveMessage(ctxA, "sess-a", &Message{Role: "assistant", Content: "more"}, false))

	stats, err := store.GetStats(ctxA)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.SessionCount, "stats must count only the caller's sessions")
	assert.Equal(t, 2, stats.MessageCount, "stats must count only the caller's messages")
}

func TestSQLiteHostileParentLinkageDoesNotLeak(t *testing.T) {
	store := newOwnershipTestStore(t)
	ctxA, ctxB := userCtx("user-a"), userCtx("user-b")

	// Victim's coordinator session with a shared-context message.
	parent := ownedSession("victim-parent")
	parent.AgentID = "coordinator"
	require.NoError(t, store.SaveSession(ctxA, parent))
	msg := &Message{Role: "assistant", Content: "victim coordinator state", SessionContext: types.SessionContextCoordinator}
	require.NoError(t, store.SaveMessage(ctxA, "victim-parent", msg, true))

	// Attacker links their own child to the victim's session.
	child := ownedSession("attacker-child")
	child.ParentSessionID = "victim-parent"
	require.NoError(t, store.SaveSession(ctxB, child))

	got, err := store.LoadMessagesFromParentSession(ctxB, "attacker-child")
	require.NoError(t, err)
	assert.Empty(t, got, "foreign parent must read as parentless, never leak its messages")

	// The legitimate owner's parent flow still works.
	ownChild := ownedSession("own-child")
	ownChild.ParentSessionID = "victim-parent"
	require.NoError(t, store.SaveSession(ctxA, ownChild))
	got, err = store.LoadMessagesFromParentSession(ctxA, "own-child")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Content, "victim coordinator state")
}
