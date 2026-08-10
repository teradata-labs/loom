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
