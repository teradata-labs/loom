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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// TestE2E_HumanRequest_Params verifies CT-1P against real postgres: the held
// call's full parameter map and its truncation flag survive Store -> Get /
// ListBySession / ListPending through the params_json / params_truncated columns
// migration 000022 adds (AC5), and rows carrying no params — a request stored
// without them, and a row whose params_truncated is NULL — read back as an empty
// map with the flag false, with Kind/Summary untouched (AC6).
//
// The postgres human_requests row has a foreign key to sessions, so a session is
// created first; user_id RLS scopes every store operation to the caller, and the
// per-run unique user id keeps ListPending scoped to this test's rows.
func TestE2E_HumanRequest_Params(t *testing.T) {
	if !isPostgres() {
		t.Skip("human_request params columns are asserted against the postgres backend")
	}

	pool := auditTestPool(t)
	tracer := observability.NewNoOpTracer()

	userID := uniqueTestID("user-hitl-params")
	sessionID := uniqueTestID("sess-hitl-params")
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
	now := time.Now().UTC()

	// Values chosen to survive a JSON round-trip verbatim: numbers as float64,
	// plus a nested object and an array, so the whole map compares by equality.
	heldParams := map[string]interface{}{
		"stmt":    "DELETE FROM users WHERE id=42",
		"confirm": true,
		"dry_run": false,
		"rows":    float64(42),
		"options": map[string]interface{}{"cascade": true, "timeout_s": float64(30)},
		"targets": []interface{}{"users", "sessions"},
	}
	summary := "delete_table DELETE FROM users WHERE id=42"

	// AC5: a request carrying params and ParamsTruncated=true projects both back
	// through all three SELECT lists — Get, ListBySession and ListPending.
	// AC6(c): Kind and Summary on that same row are what Store wrote.
	cutID := uniqueTestID("hr-params-cut")
	t.Run("params and a true truncated flag round-trip through Store, Get, ListBySession, ListPending", func(t *testing.T) {
		cut := &shuttle.HumanRequest{
			ID:              cutID,
			AgentID:         "test-agent",
			SessionID:       sessionID,
			Question:        `Approve tool call "delete_table"?`,
			RequestType:     "approval",
			Priority:        "normal",
			Kind:            "approval",
			Summary:         summary,
			Params:          heldParams,
			ParamsTruncated: true,
			Timeout:         5 * time.Minute,
			CreatedAt:       now,
			ExpiresAt:       now.Add(5 * time.Minute),
			Status:          "pending",
		}
		require.NoError(t, store.Store(ctx, cut))

		got, err := store.Get(ctx, cutID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, heldParams, got.Params, "Get returns the stored parameter map whole, nested values included")
		assert.True(t, got.ParamsTruncated, "Get returns the stored truncated flag")
		assert.Equal(t, "approval", got.Kind, "the params columns leave Kind as stored")
		assert.Equal(t, summary, got.Summary, "the params columns leave Summary as stored")

		bySession, err := store.ListBySession(ctx, sessionID)
		require.NoError(t, err)
		found := findRequest(bySession, cutID)
		require.NotNil(t, found, "the stored request appears in ListBySession")
		assert.Equal(t, heldParams, found.Params, "ListBySession projects the params_json column")
		assert.True(t, found.ParamsTruncated, "ListBySession projects the params_truncated column")
		assert.Equal(t, summary, found.Summary, "ListBySession leaves Summary as stored")

		pending, err := store.ListPending(ctx)
		require.NoError(t, err)
		fromPending := findRequest(pending, cutID)
		require.NotNil(t, fromPending, "the pending request appears in ListPending")
		assert.Equal(t, heldParams, fromPending.Params, "ListPending projects the params_json column")
		assert.True(t, fromPending.ParamsTruncated, "ListPending projects the params_truncated column")
	})

	// AC5: nothing cut — the same carriage stores the flag as false.
	t.Run("params with an uncut payload round-trip the truncated flag as false", func(t *testing.T) {
		whole := &shuttle.HumanRequest{
			ID:          uniqueTestID("hr-params-whole"),
			AgentID:     "test-agent",
			SessionID:   sessionID,
			Question:    `Approve tool call "drop_index"?`,
			RequestType: "approval",
			Priority:    "normal",
			Kind:        "approval",
			Summary:     "drop_index DROP INDEX idx_users_email",
			Params:      map[string]interface{}{"index": "idx_users_email", "force": false},
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		}
		require.NoError(t, store.Store(ctx, whole))

		got, err := store.Get(ctx, whole.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, whole.Params, got.Params, "Get returns the uncut parameter map")
		assert.False(t, got.ParamsTruncated, "an uncut payload reads back false")
	})

	// AC6(a): a request stored with no params (a question) writes params_json NULL
	// and reads back as an empty map with the flag false — the shape a row written
	// before migration 000022 also has. AC6(c): its Kind/Summary are unchanged.
	t.Run("a request stored with no params reads back empty params and false", func(t *testing.T) {
		noParams := &shuttle.HumanRequest{
			ID:          uniqueTestID("hr-noparams"),
			AgentID:     "test-agent",
			SessionID:   sessionID,
			Question:    "Which environment should this run against?",
			RequestType: "input",
			Priority:    "normal",
			Kind:        "question",
			Summary:     "Which environment should this run against?",
			// Params/ParamsTruncated left zero → params_json is written NULL.
			Timeout:   5 * time.Minute,
			CreatedAt: now,
			ExpiresAt: now.Add(5 * time.Minute),
			Status:    "pending",
		}
		require.NoError(t, store.Store(ctx, noParams))

		var paramsJSONIsNull bool
		require.NoError(t, pool.QueryRow(context.Background(),
			"SELECT params_json IS NULL FROM human_requests WHERE id = $1", noParams.ID,
		).Scan(&paramsJSONIsNull))
		assert.True(t, paramsJSONIsNull, "a request with no params writes params_json NULL")

		got, err := store.Get(ctx, noParams.ID)
		require.NoError(t, err, "a NULL params_json column reads back without error")
		require.NotNil(t, got)
		assert.Empty(t, got.Params, "NULL params_json scans to an empty parameter map")
		assert.False(t, got.ParamsTruncated, "a request stored with no params reads the flag back as false")
		assert.Equal(t, "question", got.Kind, "the params columns leave Kind as stored")
		assert.Equal(t, noParams.Summary, got.Summary, "the params columns leave Summary as stored")

		bySession, err := store.ListBySession(ctx, sessionID)
		require.NoError(t, err, "ListBySession reads a params-less row without error")
		found := findRequest(bySession, noParams.ID)
		require.NotNil(t, found, "the params-less request appears in ListBySession")
		assert.Empty(t, found.Params, "ListBySession projects NULL params_json as an empty map")
		assert.False(t, found.ParamsTruncated, "ListBySession projects the params-less row as false")
	})

	// AC6(b): the scan site tolerates a NULL params_truncated. Raw SQL is what
	// forces that NULL — the store's writer always binds a boolean, so no row it
	// writes reaches this branch.
	t.Run("a row whose params_truncated is NULL scans to false", func(t *testing.T) {
		nullFlag := &shuttle.HumanRequest{
			ID:          uniqueTestID("hr-null-flag"),
			AgentID:     "test-agent",
			SessionID:   sessionID,
			Question:    `Approve tool call "truncate_table"?`,
			RequestType: "approval",
			Priority:    "normal",
			Kind:        "approval",
			Summary:     "truncate_table TRUNCATE TABLE events",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		}
		require.NoError(t, store.Store(ctx, nullFlag))

		_, err := pool.Exec(context.Background(),
			"UPDATE human_requests SET params_json = NULL, params_truncated = NULL WHERE id = $1", nullFlag.ID)
		require.NoError(t, err, "forcing the params columns to NULL should succeed")

		got, err := store.Get(ctx, nullFlag.ID)
		require.NoError(t, err, "a NULL params_truncated column reads back without error")
		require.NotNil(t, got)
		assert.Empty(t, got.Params, "the NULL params_json scans to an empty parameter map")
		assert.False(t, got.ParamsTruncated, "a NULL params_truncated scans to false")
		assert.Equal(t, nullFlag.Summary, got.Summary, "Summary is unchanged on a row with NULL params columns")

		bySession, err := store.ListBySession(ctx, sessionID)
		require.NoError(t, err, "ListBySession reads a NULL params_truncated row without error")
		found := findRequest(bySession, nullFlag.ID)
		require.NotNil(t, found, "the NULL-flag request appears in ListBySession")
		assert.False(t, found.ParamsTruncated, "ListBySession scans a NULL params_truncated to false")
	})
}

// findRequest returns the request with the given ID, or nil when the list does
// not carry it.
func findRequest(requests []*shuttle.HumanRequest, id string) *shuttle.HumanRequest {
	for _, req := range requests {
		if req.ID == id {
			return req
		}
	}
	return nil
}
