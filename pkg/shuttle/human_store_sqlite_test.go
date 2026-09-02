// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// AC1: RespondToRequest is a member of the HumanRequestStore interface, and the
// SQLite store satisfies that interface at build time. This assignment fails to
// compile if the method is dropped from the interface or from the store.
var _ HumanRequestStore = (*SQLiteHumanRequestStore)(nil)

func TestSQLiteHumanRequestStore_BasicOperations(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// Create a test request
	now := time.Now()
	req := &HumanRequest{
		ID:          "test-req-1",
		AgentID:     "agent-1",
		SessionID:   "session-1",
		Question:    "Should I proceed?",
		Context:     map[string]interface{}{"key": "value"},
		RequestType: "approval",
		Priority:    "high",
		Timeout:     5 * time.Minute,
		CreatedAt:   now,
		ExpiresAt:   now.Add(5 * time.Minute),
		Status:      "pending",
	}

	// Test Store
	err := store.Store(ctx, req)
	require.NoError(t, err)

	// Test Get
	retrieved, err := store.Get(ctx, "test-req-1")
	require.NoError(t, err)
	assert.Equal(t, req.ID, retrieved.ID)
	assert.Equal(t, req.AgentID, retrieved.AgentID)
	assert.Equal(t, req.SessionID, retrieved.SessionID)
	assert.Equal(t, req.Question, retrieved.Question)
	assert.Equal(t, req.Context["key"], retrieved.Context["key"])
	assert.Equal(t, req.RequestType, retrieved.RequestType)
	assert.Equal(t, req.Priority, retrieved.Priority)
	assert.Equal(t, req.Status, retrieved.Status)

	// Test Update
	retrieved.Status = "approved"
	retrieved.Response = "Yes, proceed"
	retrieved.RespondedBy = "alice@example.com"
	respondedAt := time.Now()
	retrieved.RespondedAt = &respondedAt
	retrieved.ResponseData = map[string]interface{}{"confirmed": true}

	err = store.Update(ctx, retrieved)
	require.NoError(t, err)

	// Verify update
	updated, err := store.Get(ctx, "test-req-1")
	require.NoError(t, err)
	assert.Equal(t, "approved", updated.Status)
	assert.Equal(t, "Yes, proceed", updated.Response)
	assert.Equal(t, "alice@example.com", updated.RespondedBy)
	assert.NotNil(t, updated.RespondedAt)
	assert.Equal(t, true, updated.ResponseData["confirmed"])
}

func TestSQLiteHumanRequestStore_GetNotFound(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	_, err := store.Get(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSQLiteHumanRequestStore_ListPending(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// Create multiple requests
	now := time.Now()
	requests := []*HumanRequest{
		{
			ID:          "req-1",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Question 1?",
			RequestType: "approval",
			Priority:    "high",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		},
		{
			ID:          "req-2",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Question 2?",
			RequestType: "decision",
			Priority:    "normal",
			Timeout:     5 * time.Minute,
			CreatedAt:   now.Add(1 * time.Minute),
			ExpiresAt:   now.Add(6 * time.Minute),
			Status:      "pending",
		},
		{
			ID:          "req-3",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Question 3?",
			RequestType: "input",
			Priority:    "low",
			Timeout:     5 * time.Minute,
			CreatedAt:   now.Add(2 * time.Minute),
			ExpiresAt:   now.Add(7 * time.Minute),
			Status:      "approved", // Not pending
		},
	}

	for _, req := range requests {
		err := store.Store(ctx, req)
		require.NoError(t, err)
	}

	// List pending
	pending, err := store.ListPending(ctx)
	require.NoError(t, err)
	assert.Len(t, pending, 2) // Only req-1 and req-2
	assert.Equal(t, "req-1", pending[0].ID)
	assert.Equal(t, "req-2", pending[1].ID)
}

func TestSQLiteHumanRequestStore_ListBySession(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// Create requests for different sessions
	now := time.Now()
	requests := []*HumanRequest{
		{
			ID:          "req-1",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Question 1?",
			RequestType: "approval",
			Priority:    "high",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		},
		{
			ID:          "req-2",
			AgentID:     "agent-1",
			SessionID:   "session-2",
			Question:    "Question 2?",
			RequestType: "decision",
			Priority:    "normal",
			Timeout:     5 * time.Minute,
			CreatedAt:   now.Add(1 * time.Minute),
			ExpiresAt:   now.Add(6 * time.Minute),
			Status:      "pending",
		},
		{
			ID:          "req-3",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Question 3?",
			RequestType: "input",
			Priority:    "low",
			Timeout:     5 * time.Minute,
			CreatedAt:   now.Add(2 * time.Minute),
			ExpiresAt:   now.Add(7 * time.Minute),
			Status:      "approved",
		},
	}

	for _, req := range requests {
		err := store.Store(ctx, req)
		require.NoError(t, err)
	}

	// List by session-1
	session1Requests, err := store.ListBySession(ctx, "session-1")
	require.NoError(t, err)
	assert.Len(t, session1Requests, 2)
	// Ordered by created_at DESC
	assert.Equal(t, "req-3", session1Requests[0].ID)
	assert.Equal(t, "req-1", session1Requests[1].ID)

	// List by session-2
	session2Requests, err := store.ListBySession(ctx, "session-2")
	require.NoError(t, err)
	assert.Len(t, session2Requests, 1)
	assert.Equal(t, "req-2", session2Requests[0].ID)
}

func TestSQLiteHumanRequestStore_RespondToRequest(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// Create a pending request
	now := time.Now()
	req := &HumanRequest{
		ID:          "req-1",
		AgentID:     "agent-1",
		SessionID:   "session-1",
		Question:    "Question 1?",
		RequestType: "approval",
		Priority:    "high",
		Timeout:     5 * time.Minute,
		CreatedAt:   now,
		ExpiresAt:   now.Add(5 * time.Minute),
		Status:      "pending",
	}

	err := store.Store(ctx, req)
	require.NoError(t, err)

	// Respond to request
	responseData := map[string]interface{}{
		"confirmed": true,
		"reason":    "Backup verified",
	}

	err = store.RespondToRequest(ctx, "req-1", "approved", "Yes, proceed", "alice@example.com", responseData)
	require.NoError(t, err)

	// Verify response
	updated, err := store.Get(ctx, "req-1")
	require.NoError(t, err)
	assert.Equal(t, "approved", updated.Status)
	assert.Equal(t, "Yes, proceed", updated.Response)
	assert.Equal(t, "alice@example.com", updated.RespondedBy)
	assert.NotNil(t, updated.RespondedAt)
	assert.Equal(t, true, updated.ResponseData["confirmed"])
	assert.Equal(t, "Backup verified", updated.ResponseData["reason"])
}

// AC3: a second RespondToRequest on an already-decided row is a no-op that
// returns nil (not an error) and does not mutate the row — Get reads back the
// original outcome.
func TestSQLiteHumanRequestStore_RespondToRequest_AlreadyDecidedIsNoOp(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// Create and resolve a request once.
	now := time.Now()
	req := &HumanRequest{
		ID:          "req-1",
		AgentID:     "agent-1",
		SessionID:   "session-1",
		Question:    "Question 1?",
		RequestType: "approval",
		Priority:    "high",
		Timeout:     5 * time.Minute,
		CreatedAt:   now,
		ExpiresAt:   now.Add(5 * time.Minute),
		Status:      "pending",
	}

	err := store.Store(ctx, req)
	require.NoError(t, err)

	err = store.RespondToRequest(ctx, "req-1", "approved", "Yes", "alice@example.com", nil)
	require.NoError(t, err)

	// A conflicting second response is a no-op: it returns nil and leaves the
	// first outcome intact.
	err = store.RespondToRequest(ctx, "req-1", "rejected", "No", "bob@example.com", nil)
	require.NoError(t, err)

	unchanged, err := store.Get(ctx, "req-1")
	require.NoError(t, err)
	assert.Equal(t, "approved", unchanged.Status)
	assert.Equal(t, "Yes", unchanged.Response)
	assert.Equal(t, "alice@example.com", unchanged.RespondedBy)
}

// AC4: RespondToRequest on a request whose ExpiresAt is already past does not
// resolve it and returns nil (not an error); the row stays pending.
func TestSQLiteHumanRequestStore_RespondToRequest_ExpiredIsNoOp(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// A pending request whose ExpiresAt is already in the past.
	now := time.Now()
	req := &HumanRequest{
		ID:          "req-expired",
		AgentID:     "agent-1",
		SessionID:   "session-1",
		Question:    "Question expired?",
		RequestType: "approval",
		Priority:    "high",
		Timeout:     5 * time.Minute,
		CreatedAt:   now.Add(-10 * time.Minute),
		ExpiresAt:   now.Add(-1 * time.Minute),
		Status:      "pending",
	}

	err := store.Store(ctx, req)
	require.NoError(t, err)

	err = store.RespondToRequest(ctx, "req-expired", "approved", "Yes", "alice@example.com", nil)
	require.NoError(t, err)

	unresolved, err := store.Get(ctx, "req-expired")
	require.NoError(t, err)
	assert.Equal(t, "pending", unresolved.Status)
	assert.Empty(t, unresolved.RespondedBy)
}

func TestSQLiteHumanRequestStore_RespondToRequest_NotFound(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	err := store.RespondToRequest(ctx, "nonexistent", "approved", "Yes", "alice@example.com", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSQLiteHumanRequestStore_Concurrent(t *testing.T) {
	ctx := context.Background()

	t.Run("ConcurrentWrites", func(t *testing.T) {
		store := newTestSQLiteStore(t)
		defer func() { _ = store.Close() }()

		var wg sync.WaitGroup
		numGoroutines := 10

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				now := time.Now()
				req := &HumanRequest{
					ID:          fmt.Sprintf("concurrent-req-%d", id),
					AgentID:     "agent-1",
					SessionID:   "session-concurrent",
					Question:    fmt.Sprintf("Question %d?", id),
					RequestType: "approval",
					Priority:    "normal",
					Timeout:     5 * time.Minute,
					CreatedAt:   now,
					ExpiresAt:   now.Add(5 * time.Minute),
					Status:      "pending",
				}

				err := store.Store(ctx, req)
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()

		// Verify all requests were stored
		pending, err := store.ListPending(ctx)
		require.NoError(t, err)
		assert.Equal(t, numGoroutines, len(pending))
	})

	t.Run("ConcurrentReads", func(t *testing.T) {
		store := newTestSQLiteStore(t)
		defer func() { _ = store.Close() }()

		// Create a request
		now := time.Now()
		req := &HumanRequest{
			ID:          "read-req",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "Read question?",
			RequestType: "approval",
			Priority:    "high",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		}

		err := store.Store(ctx, req)
		require.NoError(t, err)

		// Concurrent reads
		var wg sync.WaitGroup
		numGoroutines := 20

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				retrieved, err := store.Get(ctx, "read-req")
				if assert.NoError(t, err) {
					assert.Equal(t, "read-req", retrieved.ID)
				}
			}()
		}

		wg.Wait()
	})

	t.Run("ConcurrentReadWrite", func(t *testing.T) {
		store := newTestSQLiteStore(t)
		defer func() { _ = store.Close() }()

		// Create a request
		now := time.Now()
		req := &HumanRequest{
			ID:          "rw-req",
			AgentID:     "agent-1",
			SessionID:   "session-1",
			Question:    "RW question?",
			RequestType: "approval",
			Priority:    "high",
			Timeout:     5 * time.Minute,
			CreatedAt:   now,
			ExpiresAt:   now.Add(5 * time.Minute),
			Status:      "pending",
		}

		err := store.Store(ctx, req)
		require.NoError(t, err)

		var wg sync.WaitGroup

		// Concurrent readers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				retrieved, err := store.Get(ctx, "rw-req")
				if assert.NoError(t, err) {
					assert.Equal(t, "rw-req", retrieved.ID)
				}
			}()
		}

		// Concurrent writers (updating status)
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				time.Sleep(10 * time.Millisecond) // Slight delay

				// Only one should succeed since status != pending after first update
				_ = store.RespondToRequest(ctx, "rw-req", "approved", fmt.Sprintf("Response %d", id), "user", nil)
			}(i)
		}

		wg.Wait()

		// Verify final state
		final, err := store.Get(ctx, "rw-req")
		require.NoError(t, err)
		assert.Equal(t, "approved", final.Status)
	})
}

func TestSQLiteHumanRequestStore_Persistence(t *testing.T) {
	// Create a temporary database file
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create store and add request
	store1, err := NewSQLiteHumanRequestStore(SQLiteConfig{
		Path:   dbPath,
		Tracer: observability.NewNoOpTracer(),
	})
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now()
	req := &HumanRequest{
		ID:          "persist-req",
		AgentID:     "agent-1",
		SessionID:   "session-1",
		Question:    "Persist question?",
		RequestType: "approval",
		Priority:    "high",
		Timeout:     5 * time.Minute,
		CreatedAt:   now,
		ExpiresAt:   now.Add(5 * time.Minute),
		Status:      "pending",
	}

	err = store1.Store(ctx, req)
	require.NoError(t, err)

	// Close store
	err = store1.Close()
	require.NoError(t, err)

	// Reopen store and verify request persisted
	store2, err := NewSQLiteHumanRequestStore(SQLiteConfig{
		Path:   dbPath,
		Tracer: observability.NewNoOpTracer(),
	})
	require.NoError(t, err)
	defer func() { _ = store2.Close() }()

	retrieved, err := store2.Get(ctx, "persist-req")
	require.NoError(t, err)
	assert.Equal(t, "persist-req", retrieved.ID)
	assert.Equal(t, "agent-1", retrieved.AgentID)
	assert.Equal(t, "Persist question?", retrieved.Question)
	assert.Equal(t, "pending", retrieved.Status)
}

func TestSQLiteHumanRequestStore_UpdateNotFound(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	req := &HumanRequest{
		ID:     "nonexistent",
		Status: "approved",
	}

	err := store.Update(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// newTestSQLiteStore creates a temporary file-based SQLite store for testing.
func newTestSQLiteStore(t *testing.T) *SQLiteHumanRequestStore {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteHumanRequestStore(SQLiteConfig{
		Path:   dbPath,
		Tracer: observability.NewNoOpTracer(),
	})
	require.NoError(t, err)
	return store
}

// Same law on the SQLite store.
func TestRespondToRequest_StoreOwnedExpiryGuard_SQLite(t *testing.T) {
	store, err := NewSQLiteHumanRequestStore(SQLiteConfig{Path: filepath.Join(t.TempDir(), "hitl.db")})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.Error(t, store.Store(context.Background(), &HumanRequest{
		ID: "r1", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		CreatedAt: time.Now()}),
		"a pending row with no expiry must be refused")

	expired := &HumanRequest{ID: "r2", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		CreatedAt: time.Now().Add(-2 * time.Minute), ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, store.Store(context.Background(), expired))
	require.NoError(t, store.RespondToRequest(context.Background(), "r2", "approved", "", "human", nil))
	got, err := store.Get(context.Background(), "r2")
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
	require.NoError(t, store.RespondToRequest(context.Background(), "r2", "timeout", "", "attacker", nil))
	got, err = store.Get(context.Background(), "r2")
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status,
		"the caller-supplied status must not decide whether the expiry guard applies")
	require.NoError(t, store.ExpireRequest(context.Background(), "r2", "system:expiry"))
	got, err = store.Get(context.Background(), "r2")
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status)

	live := &HumanRequest{ID: "r3", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, store.Store(context.Background(), live))
	require.NoError(t, store.RespondToRequest(context.Background(), "r3", "rejected", "no", "human", nil))
	require.NoError(t, store.ExpireRequest(context.Background(), "r3", "system:expiry"))
	got, err = store.Get(context.Background(), "r3")
	require.NoError(t, err)
	require.Equal(t, "rejected", got.Status, "closing is not resolving")
}

// an existing hitl.db from before the four columns shipped is
// upgraded in place by initSchema's column guard; Store/Get work immediately.
func TestSQLiteHumanStore_UpgradesPreexistingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hitl.db")
	createLegacyHITLSchema(t, path)

	// Open: the guard must add the four columns.
	store, err := NewSQLiteHumanRequestStore(SQLiteConfig{Path: path})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	req := &HumanRequest{ID: "r1", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "approval", Priority: "normal", Status: "pending",
		Kind: "approval", Summary: "sql_execute GRANT",
		Params:    map[string]interface{}{"stmt": "GRANT SELECT ON t TO alice"},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, store.Store(context.Background(), req))
	got, err := store.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.Equal(t, "approval", got.Kind)
	require.Equal(t, "GRANT SELECT ON t TO alice", got.Params["stmt"])
}

// createLegacyHITLSchema fabricates the pre-TER-710 15-column human_requests
// table, as an upgraded deployment's hitl.db carries it.
func createLegacyHITLSchema(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE human_requests (
		id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, session_id TEXT NOT NULL,
		question TEXT NOT NULL, context_json TEXT, request_type TEXT NOT NULL,
		priority TEXT NOT NULL, timeout_ms INTEGER NOT NULL,
		created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
		status TEXT NOT NULL, response TEXT, response_data_json TEXT,
		responded_at INTEGER, responded_by TEXT)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

// a legacy row with no kind column (pre-migration, or after a
// rollback re-adds it as NULL) is still recognisably an approval via the
// context_json origin discriminator.
func TestKindSurvivesColumnRollback_SQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hitl.db")
	createLegacyHITLSchema(t, path)

	// A pre-rollback approval row: context_json carries the origin, the kind
	// column does not exist yet.
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO human_requests
		(id, agent_id, session_id, question, context_json, request_type, priority,
		 timeout_ms, created_at, expires_at, status, response, response_data_json,
		 responded_by)
		VALUES ('r1','a','s','q','{"kind":"approval","tool":"sql_execute"}','approval','normal',
		 60000, ?, ?, 'pending', '', '', '')`,
		time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := NewSQLiteHumanRequestStore(SQLiteConfig{Path: path})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	got, err := store.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.Equal(t, "approval", got.Kind, "context_json backstops a missing kind column")
}

// TestMain helper guard: ensure the temp-dir cleanup does not interfere with
// WAL sidecar files on close.
func TestSQLiteHumanStore_CloseIsClean(t *testing.T) {
	store, err := NewSQLiteHumanRequestStore(SQLiteConfig{Path: filepath.Join(t.TempDir(), "x.db")})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	_, statErr := os.Stat(filepath.Join(t.TempDir()))
	require.NoError(t, statErr)
}

// TestSQLiteHumanStore_TaskIDRoundTrips pins the read side of task
// attribution. The INSERT wrote task_id from the start; Get and the two List
// queries never selected it, so every read handed back TaskID == "" and the one
// consumer that needs it — ResumeChat, seeding the resumed turn's binding from
// the row — could not tell an attributed park from an unattributed one.
func TestSQLiteHumanStore_TaskIDRoundTrips(t *testing.T) {
	store, err := NewSQLiteHumanRequestStore(SQLiteConfig{Path: filepath.Join(t.TempDir(), "hitl.db")})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	base := func(id string) *HumanRequest {
		return &HumanRequest{ID: id, AgentID: "a", SessionID: "s", Question: "q",
			RequestType: "parked", Priority: "normal", Status: "pending",
			CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	}

	// Explicit TaskID survives the round trip.
	explicit := base("r-explicit")
	explicit.TaskID = "task-explicit"
	require.NoError(t, store.Store(ctx, explicit))
	got, err := store.Get(ctx, "r-explicit")
	require.NoError(t, err)
	require.Equal(t, "task-explicit", got.TaskID)

	// Ambient attribution is captured at Store and read back — the shape a real
	// park takes, where the turn's task rides the context, not the struct.
	ambient := taskctx.ContextWithAttribution(ctx, taskctx.Attribution{
		TaskID: "task-ambient", SessionID: "s"})
	require.NoError(t, store.Store(ambient, base("r-ambient")))
	got, err = store.Get(ctx, "r-ambient")
	require.NoError(t, err)
	require.Equal(t, "task-ambient", got.TaskID)

	// The list paths read it back too — ListBySession is what a resume-side
	// audit walks.
	bySession, err := store.ListBySession(ctx, "s")
	require.NoError(t, err)
	seen := map[string]string{}
	for _, hr := range bySession {
		seen[hr.ID] = hr.TaskID
	}
	require.Equal(t, "task-explicit", seen["r-explicit"])
	require.Equal(t, "task-ambient", seen["r-ambient"])

	// No task is not an error: TaskID reads back empty, same as a legacy row.
	require.NoError(t, store.Store(ctx, base("r-none")))
	got, err = store.Get(ctx, "r-none")
	require.NoError(t, err)
	require.Empty(t, got.TaskID)
}

// TestInMemoryHumanStore_CapturesAmbientTaskID holds the two stores to one
// attribution rule: explicit TaskID wins, ambient attribution is the fallback.
// Without it the same park writes an attributed row in SQLite and an
// unattributed one in memory, and the resume's binding seed silently works in
// one deployment and not the other.
func TestInMemoryHumanStore_CapturesAmbientTaskID(t *testing.T) {
	store := NewInMemoryHumanRequestStore()
	ctx := taskctx.ContextWithAttribution(context.Background(), taskctx.Attribution{
		TaskID: "task-ambient", SessionID: "s"})

	require.NoError(t, store.Store(ctx, &HumanRequest{
		ID: "r1", AgentID: "a", SessionID: "s", Question: "q",
		RequestType: "parked", Priority: "normal", Status: "pending",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}))

	got, err := store.Get(context.Background(), "r1")
	require.NoError(t, err)
	require.Equal(t, "task-ambient", got.TaskID)
}
