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

package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// TestE2E_HumanRequest_RespondToRequest verifies CT-3 against real postgres: the
// atomic conditional UPDATE that resolves a pending, non-expired request exactly
// once, and the no-op-returning-nil behavior on already-decided and expired rows.
// Every access is RLS-scoped to the caller's user_id and expiry is judged against
// the DB clock (expires_at > now()).
//
// The postgres human_requests row has a foreign key to sessions, so a session is
// created first; all subtests share it and mint their own request IDs.
func TestE2E_HumanRequest_RespondToRequest(t *testing.T) {
	if !isPostgres() {
		t.Skip("RespondToRequest CAS/expiry contract is asserted against the postgres backend")
	}

	pool := auditTestPool(t)
	tracer := observability.NewNoOpTracer()

	userID := uniqueTestID("user-respond")
	sessionID := uniqueTestID("sess-respond")
	ctx := postgres.ContextWithUserID(context.Background(), userID)

	// The human_requests.session_id FK requires an existing session for this user.
	sessions := postgres.NewSessionStore(pool, tracer, nil)
	require.NoError(t, sessions.SaveSession(ctx, &agent.Session{
		ID:        sessionID,
		AgentID:   "test-agent",
		UserID:    userID,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}), "creating the owning session should succeed")
	t.Cleanup(func() {
		_ = sessions.DeleteSession(ctx, sessionID)
	})

	store := postgres.NewHumanRequestStore(pool, tracer)

	// seedPending stores a pending request under the test session with the given
	// id and expiry, and returns it. A past expiresAt makes it already-expired.
	seedPending := func(t *testing.T, id string, expiresAt time.Time) *shuttle.HumanRequest {
		t.Helper()
		req := &shuttle.HumanRequest{
			ID:          id,
			AgentID:     "test-agent",
			SessionID:   sessionID,
			Question:    "Should I proceed?",
			RequestType: "approval",
			Priority:    "normal",
			Timeout:     5 * time.Minute,
			CreatedAt:   time.Now().UTC(),
			ExpiresAt:   expiresAt,
			Status:      "pending",
		}
		require.NoError(t, store.Store(ctx, req))
		return req
	}

	// AC2: resolving a pending, non-expired request stamps status/response/
	// responded_by/responded_at exactly once and persists that single outcome.
	t.Run("pending non-expired request resolves once and stamps the outcome", func(t *testing.T) {
		id := uniqueTestID("hr-resolve")
		seedPending(t, id, time.Now().UTC().Add(5*time.Minute))

		err := store.RespondToRequest(ctx, id, "approved", "Yes, proceed",
			"alice@example.com", map[string]interface{}{"confirmed": true})
		require.NoError(t, err)

		got, err := store.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "approved", got.Status, "status is stamped")
		assert.Equal(t, "Yes, proceed", got.Response, "response is stamped")
		assert.Equal(t, "alice@example.com", got.RespondedBy, "responded_by is stamped")
		assert.NotNil(t, got.RespondedAt, "responded_at is stamped")
		assert.Equal(t, true, got.ResponseData["confirmed"], "response_data is stamped")
	})

	// AC1/AC2: under two concurrent responders the single-statement CAS admits
	// exactly one atomic winner — the persisted row carries one responder's full
	// tuple, never an interleaved mix, proving resolve-once on the real backend.
	t.Run("concurrent responders yield exactly one atomic winner", func(t *testing.T) {
		id := uniqueTestID("hr-concurrent")
		seedPending(t, id, time.Now().UTC().Add(5*time.Minute))

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = store.RespondToRequest(ctx, id, "approved", "by-alice", "alice@example.com", nil)
		}()
		go func() {
			defer wg.Done()
			_ = store.RespondToRequest(ctx, id, "rejected", "by-bob", "bob@example.com", nil)
		}()
		wg.Wait()

		got, err := store.Get(ctx, id)
		require.NoError(t, err)
		require.NotEqual(t, "pending", got.Status, "the row is resolved exactly once, not left pending")
		assert.NotNil(t, got.RespondedAt, "the winner stamps responded_at")

		// The persisted tuple belongs wholly to one responder — no field crossed
		// over from the loser, which is what atomic single-winner resolution means.
		switch got.Status {
		case "approved":
			assert.Equal(t, "by-alice", got.Response)
			assert.Equal(t, "alice@example.com", got.RespondedBy)
		case "rejected":
			assert.Equal(t, "by-bob", got.Response)
			assert.Equal(t, "bob@example.com", got.RespondedBy)
		default:
			t.Fatalf("unexpected winning status %q", got.Status)
		}
	})

	// AC3: a second respond on an already-decided row is a no-op that returns nil
	// (not an error) and does not mutate the row — Get reads the original outcome.
	t.Run("second respond on a decided row is a no-op and does not mutate", func(t *testing.T) {
		id := uniqueTestID("hr-decided")
		seedPending(t, id, time.Now().UTC().Add(5*time.Minute))

		require.NoError(t, store.RespondToRequest(ctx, id, "approved", "Yes", "alice@example.com", nil))

		// A conflicting second respond returns nil and leaves the first outcome intact.
		require.NoError(t, store.RespondToRequest(ctx, id, "rejected", "No", "bob@example.com", nil))

		got, err := store.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "approved", got.Status)
		assert.Equal(t, "Yes", got.Response)
		assert.Equal(t, "alice@example.com", got.RespondedBy)
	})

	// AC4: a respond on a row whose expires_at is already past does not resolve it
	// and returns nil (not an error); the row stays pending and unstamped.
	t.Run("respond on an expired row is a no-op and leaves it pending", func(t *testing.T) {
		id := uniqueTestID("hr-expired")
		seedPending(t, id, time.Now().UTC().Add(-1*time.Minute))

		require.NoError(t, store.RespondToRequest(ctx, id, "approved", "Yes", "alice@example.com", nil))

		got, err := store.Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "pending", got.Status, "an expired row is not resolved")
		assert.Empty(t, got.RespondedBy, "an expired row is left unstamped")
	})
}
