// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

func TestSQLiteHumanRequestStore_BasicOperations(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()

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
	defer store.Close()

	ctx := context.Background()

	_, err := store.Get(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSQLiteHumanRequestStore_ListPending(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()

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
	defer store.Close()

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
	defer store.Close()

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

func TestSQLiteHumanRequestStore_RespondToRequest_AlreadyResponded(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()

	ctx := context.Background()

	// Create and respond to a request
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

	// Try to respond again
	err = store.RespondToRequest(ctx, "req-1", "rejected", "No", "bob@example.com", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already responded")
}

func TestSQLiteHumanRequestStore_RespondToRequest_NotFound(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()

	ctx := context.Background()

	err := store.RespondToRequest(ctx, "nonexistent", "approved", "Yes", "alice@example.com", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSQLiteHumanRequestStore_Concurrent(t *testing.T) {
	ctx := context.Background()

	t.Run("ConcurrentWrites", func(t *testing.T) {
		store := newTestSQLiteStore(t)
		defer store.Close()

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
		defer store.Close()

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
		defer store.Close()

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
	defer store2.Close()

	retrieved, err := store2.Get(ctx, "persist-req")
	require.NoError(t, err)
	assert.Equal(t, "persist-req", retrieved.ID)
	assert.Equal(t, "agent-1", retrieved.AgentID)
	assert.Equal(t, "Persist question?", retrieved.Question)
	assert.Equal(t, "pending", retrieved.Status)
}

func TestSQLiteHumanRequestStore_UpdateNotFound(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close()

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
