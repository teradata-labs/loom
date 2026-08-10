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

// TestE2E_HumanRequest_SelfDescribe verifies CT-1 against real postgres: the
// first-class kind/summary columns round-trip a self-describing request through
// Store -> Get -> ListBySession (AC1), and a request carrying no Kind is stored
// with NULL kind/summary and reads back as an empty Kind without error, treated
// as a question (AC4 backward-read additivity).
//
// The postgres human_requests row has a foreign key to sessions, so a session is
// created first; user_id RLS scopes every operation to the caller.
func TestE2E_HumanRequest_SelfDescribe(t *testing.T) {
	if !isPostgres() {
		t.Skip("human_request self-describe columns are asserted against the postgres backend")
	}

	pool := auditTestPool(t)
	tracer := observability.NewNoOpTracer()

	userID := uniqueTestID("user-hitl")
	sessionID := uniqueTestID("sess-hitl")
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

	// AC1: a self-describing approval request round-trips Kind/Summary through
	// Store -> Get -> ListBySession via the first-class columns.
	t.Run("kind and summary round-trip through Store, Get, ListBySession", func(t *testing.T) {
		approval := &shuttle.HumanRequest{
			ID:          uniqueTestID("hr-approval"),
			AgentID:     "test-agent",
			SessionID:   sessionID,
			Question:    `Approve tool call "delete_table"?`,
			RequestType: "approval",
			Priority:    "normal",
			Kind:        "approval",
			Summary:     "delete_table DELETE FROM users WHERE id=42",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		}
		require.NoError(t, store.Store(ctx, approval))

		got, err := store.Get(ctx, approval.ID)
		require.NoError(t, err)
		assert.Equal(t, "approval", got.Kind, "Get returns the stored kind")
		assert.Equal(t, approval.Summary, got.Summary, "Get returns the stored summary")

		bySession, err := store.ListBySession(ctx, sessionID)
		require.NoError(t, err)
		found := false
		for _, hr := range bySession {
			if hr.ID == approval.ID {
				found = true
				assert.Equal(t, "approval", hr.Kind, "ListBySession projects the kind column")
				assert.Equal(t, approval.Summary, hr.Summary, "ListBySession projects the summary column")
			}
		}
		assert.True(t, found, "the stored request appears in ListBySession")
	})

	// AC4: a request carrying no Kind is written with NULL kind/summary columns
	// and reads back as an empty Kind without error (treated as a question).
	t.Run("absent kind stores NULL and reads back empty", func(t *testing.T) {
		noKind := &shuttle.HumanRequest{
			ID:          uniqueTestID("hr-nokind"),
			AgentID:     "test-agent",
			SessionID:   sessionID,
			Question:    "Legacy request with no kind",
			RequestType: "input",
			Priority:    "normal",
			// Kind/Summary left zero → nullableString writes NULL columns.
			Timeout:   5 * time.Minute,
			CreatedAt: now,
			ExpiresAt: now.Add(5 * time.Minute),
			Status:    "pending",
		}
		require.NoError(t, store.Store(ctx, noKind))

		got, err := store.Get(ctx, noKind.ID)
		require.NoError(t, err, "a NULL kind column reads back without error")
		assert.Empty(t, got.Kind, "NULL kind scans to empty (consumers treat empty as question)")
		assert.Empty(t, got.Summary, "NULL summary scans to empty")
	})
}
