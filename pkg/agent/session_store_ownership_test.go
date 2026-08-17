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
