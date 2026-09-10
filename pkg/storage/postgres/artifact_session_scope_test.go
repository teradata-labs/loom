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
package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/artifacts"
	"github.com/teradata-labs/loom/pkg/observability"
)

// ListArtifacts/GetArtifact accept a caller-supplied session_id. The server
// authorizes it against the caller's own sessions, but in a multi-tenant
// deployment the layer that actually contains a foreign session id is this
// store's unconditional `user_id = $1` predicate — session_id is only ever
// additive on top of it, never a substitute.
//
// These tests pin that composition, because it is the invariant the server
// delegates to rather than duplicates: if session_id ever started replacing
// the user predicate, the server-level tests in pkg/server would still pass
// and only these would fail.
//
// They run in the ORDINARY gate (no build tag, matching
// human_request_store_test.go): CI provides a postgres service and sets
// TEST_POSTGRES_URL; a local run without one skips. The `integration`-tagged
// helpers of this shape are invisible to untagged builds, so the fixture is
// local to this file.

func artifactScopeStore(t *testing.T) (*ArtifactStore, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL store test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "failed to connect to PostgreSQL")
	t.Cleanup(pool.Close)

	migrator, err := NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx), "failed to run migrations")

	return NewArtifactStore(pool, observability.NewNoOpTracer()), pool
}

// asUniqueID returns a test-unique identifier (the integration-tagged helper
// of the same shape is invisible to untagged builds).
func asUniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// seedArtifactSession satisfies the artifacts.session_id foreign key.
func seedArtifactSession(t *testing.T, pool *pgxpool.Pool, sessionID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO sessions (id, agent_id) VALUES ($1, 'agent-1') ON CONFLICT (id) DO NOTHING", sessionID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})
}

// seedUserArtifact indexes one artifact as userID, which is where the store
// takes user_id from (Index reads UserIDFromContext).
func seedUserArtifact(t *testing.T, store *ArtifactStore, pool *pgxpool.Pool, userID, sessionID, id, name string) {
	t.Helper()
	require.NoError(t, store.Index(ContextWithUserID(context.Background(), userID), &artifacts.Artifact{
		ID:        id,
		Name:      name,
		Path:      "/tmp/" + id,
		Source:    artifacts.SourceAgent,
		SessionID: sessionID,
	}), "index artifact %s for %s", id, userID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM artifacts WHERE id = $1", id)
	})
}

// A session filter must not reach across the user boundary: naming another
// tenant's session returns nothing, not that tenant's files.
func TestPGListSessionFilterStaysWithinUser(t *testing.T) {
	store, pool := artifactScopeStore(t)

	alice, bob := asUniqueID("alice"), asUniqueID("bob")
	aliceSession, bobSession := asUniqueID("sess-alice"), asUniqueID("sess-bob")
	seedArtifactSession(t, pool, aliceSession)
	seedArtifactSession(t, pool, bobSession)

	aliceArtifact, bobArtifact := asUniqueID("art-alice"), asUniqueID("art-bob")
	seedUserArtifact(t, store, pool, alice, aliceSession, aliceArtifact, "report.md")
	seedUserArtifact(t, store, pool, bob, bobSession, bobArtifact, "report.md")

	aliceCtx := ContextWithUserID(context.Background(), alice)

	own, err := store.List(aliceCtx, &artifacts.Filter{SessionID: &aliceSession})
	require.NoError(t, err)
	require.Len(t, own, 1, "alice should see exactly her own artifact")
	require.Equal(t, aliceArtifact, own[0].ID)

	foreign, err := store.List(aliceCtx, &artifacts.Filter{SessionID: &bobSession})
	require.NoError(t, err)
	require.Empty(t, foreign, "a session filter must not cross the user_id boundary")
}

// The same boundary on the name path, which is what an explicit session_id
// exists for. Both users have a report.md, so a leak here would hand back the
// wrong tenant's file under a name the caller legitimately owns.
func TestPGGetByNameSessionScopeStaysWithinUser(t *testing.T) {
	store, pool := artifactScopeStore(t)

	alice, bob := asUniqueID("alice"), asUniqueID("bob")
	aliceSession, bobSession := asUniqueID("sess-alice"), asUniqueID("sess-bob")
	seedArtifactSession(t, pool, aliceSession)
	seedArtifactSession(t, pool, bobSession)

	aliceArtifact, bobArtifact := asUniqueID("art-alice"), asUniqueID("art-bob")
	seedUserArtifact(t, store, pool, alice, aliceSession, aliceArtifact, "report.md")
	seedUserArtifact(t, store, pool, bob, bobSession, bobArtifact, "report.md")

	aliceCtx := ContextWithUserID(context.Background(), alice)

	got, err := store.GetByName(aliceCtx, "report.md", aliceSession)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, aliceArtifact, got.ID, "alice's own name lookup resolves to her artifact")

	// A miss surfaces as an error from this store; either shape is acceptable.
	// The one outcome that must never occur is bob's artifact coming back.
	leaked, err := store.GetByName(aliceCtx, "report.md", bobSession)
	if err == nil && leaked != nil {
		require.NotEqual(t, bobArtifact, leaked.ID,
			"a name lookup scoped to another user's session returned that user's artifact")
	}
}
