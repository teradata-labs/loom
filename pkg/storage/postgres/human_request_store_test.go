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

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// The postgres store is the deployed configuration: the resolve-once + expiry
// law must hold on it, not only on its in-memory and SQLite twins. These
// tests run in the ORDINARY gate (no build tag): CI provides a postgres
// service and sets TEST_POSTGRES_URL; a local run without one skips.

func testHumanRequestStore(t *testing.T) (*HumanRequestStore, context.Context, string) {
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

	store := NewHumanRequestStore(pool, observability.NewNoOpTracer())
	userID := hrUniqueID("hr-user")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM human_requests WHERE user_id = $1", userID)
	})
	return store, ContextWithUserID(context.Background(), userID), userID
}

// seedSession satisfies fk_human_requests_session for a request fixture.
func seedSession(t *testing.T, ctx context.Context, store *HumanRequestStore, sessionID string) {
	t.Helper()
	_, err := store.pool.Exec(ctx,
		"INSERT INTO sessions (id, agent_id) VALUES ($1, 'agent-1') ON CONFLICT (id) DO NOTHING", sessionID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})
}

// hrUniqueID returns a test-unique identifier (the integration-tagged helper
// of the same shape is invisible to untagged builds).
func hrUniqueID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func pendingRequest(id, sessionID string, expiresAt time.Time) *shuttle.HumanRequest {
	now := time.Now()
	return &shuttle.HumanRequest{
		ID:          id,
		AgentID:     "agent-1",
		SessionID:   sessionID,
		Question:    "Approve tool call \"execute_sql\"?",
		Context:     map[string]interface{}{"kind": "approval"},
		RequestType: "approval",
		Priority:    "normal",
		Kind:        "approval",
		Summary:     "execute_sql DROP TABLE t",
		Timeout:     5 * time.Minute,
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
		Status:      "pending",
	}
}

func TestHumanRequestStore_PendingRowRequiresExpiry(t *testing.T) {
	store, ctx, _ := testHumanRequestStore(t)
	sess := hrUniqueID("sess")
	seedSession(t, ctx, store, sess)
	req := pendingRequest(hrUniqueID("req"), sess, time.Time{})
	require.Error(t, store.Store(ctx, req),
		"a pending row with no expiry would be permanently approvable — the store must refuse it")
}

func TestHumanRequestStore_StoreOwnedExpiryGuard(t *testing.T) {
	store, ctx, _ := testHumanRequestStore(t)
	id := hrUniqueID("req")
	sess := hrUniqueID("sess")
	seedSession(t, ctx, store, sess)
	require.NoError(t, store.Store(ctx, pendingRequest(id, sess, time.Now().Add(-time.Minute))))

	// An expired row is not resolvable, and the caller-supplied status cannot
	// lift the guard.
	require.NoError(t, store.RespondToRequest(ctx, id, "approved", "", "human", nil))
	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
	require.NoError(t, store.RespondToRequest(ctx, id, "timeout", "", "attacker", nil))
	got, err = store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status,
		"the caller-supplied status must not decide whether the expiry guard applies")

	// ExpireRequest is the one path that closes past expiry.
	require.NoError(t, store.ExpireRequest(ctx, id, "system:expiry"))
	got, err = store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status)
	require.Equal(t, "system:expiry", got.RespondedBy)
}

func TestHumanRequestStore_ExpireNeverOverwritesDecision(t *testing.T) {
	store, ctx, _ := testHumanRequestStore(t)
	id := hrUniqueID("req")
	sess := hrUniqueID("sess")
	seedSession(t, ctx, store, sess)
	require.NoError(t, store.Store(ctx, pendingRequest(id, sess, time.Now().Add(time.Hour))))
	require.NoError(t, store.RespondToRequest(ctx, id, "approved", "yes", "anuj", nil))

	require.NoError(t, store.ExpireRequest(ctx, id, "system:cancel"))
	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "approved", got.Status, "closing is not resolving")
	require.Equal(t, "anuj", got.RespondedBy)
}

// The abandon write's identity leg on the deployed store: an ExpireRequest
// carrying the WRONG tenant identity matches zero rows — so a detached close
// that lost its user id would silently retire nothing, which is exactly why
// the waiter's detached context must keep the caller's values.
func TestHumanRequestStore_ExpireRequestIsTenantScoped(t *testing.T) {
	store, ctx, _ := testHumanRequestStore(t)
	id := hrUniqueID("req")
	sess := hrUniqueID("sess")
	seedSession(t, ctx, store, sess)
	require.NoError(t, store.Store(ctx, pendingRequest(id, sess, time.Now().Add(time.Hour))))

	otherCtx := ContextWithUserID(context.Background(), hrUniqueID("other-user"))
	require.NoError(t, store.ExpireRequest(otherCtx, id, "system:cancel"))
	got, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status,
		"another tenant's close must not retire this tenant's hold")

	require.NoError(t, store.ExpireRequest(ctx, id, "system:cancel"))
	got, err = store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status)
}

// TestHumanRequestStore_TaskIDStampsAndRoundTrips pins round-5 M1 for the
// production backend: migration 000024 installed human_requests.task_id, but
// this store's INSERT never wrote it and no SELECT read it — so hr.TaskID was
// always empty on Postgres, and ResumeChat's durable identity restore (the
// PRIMARY path; the emitter rebind is only the legacy fallback) was inert on
// exactly the deployed HITL-park shape. Same attribution rule as both SQLite
// stores: explicit TaskID wins, ambient attribution is the fallback.
func TestHumanRequestStore_TaskIDStampsAndRoundTrips(t *testing.T) {
	store, ctx, _ := testHumanRequestStore(t)
	sessionID := hrUniqueID("sess-task")
	seedSession(t, ctx, store, sessionID)

	base := func(id string) *shuttle.HumanRequest {
		return &shuttle.HumanRequest{
			ID: hrUniqueID(id), AgentID: "agent-1", SessionID: sessionID,
			Question: "q", RequestType: "parked", Priority: "normal", Status: "pending",
			CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}
	}

	explicit := base("r-explicit")
	explicit.TaskID = "task-explicit"
	require.NoError(t, store.Store(ctx, explicit))
	got, err := store.Get(ctx, explicit.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "task-explicit", got.TaskID, "explicit TaskID must survive the round trip")

	ambient := taskctx.ContextWithAttribution(ctx, taskctx.Attribution{
		TaskID: "task-ambient", SessionID: sessionID})
	ambientReq := base("r-ambient")
	require.NoError(t, store.Store(ambient, ambientReq))
	got, err = store.Get(ctx, ambientReq.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "task-ambient", got.TaskID,
		"the park's ambient attribution must stamp the row — this is what ResumeChat restores from")

	none := base("r-none")
	require.NoError(t, store.Store(ctx, none))
	got, err = store.Get(ctx, none.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got.TaskID, "no task reads back empty, same as a legacy row")

	bySession, err := store.ListBySession(ctx, sessionID)
	require.NoError(t, err)
	seen := map[string]string{}
	for _, hr := range bySession {
		seen[hr.ID] = hr.TaskID
	}
	require.Equal(t, "task-explicit", seen[explicit.ID], "the list path reads task_id back too")
}
