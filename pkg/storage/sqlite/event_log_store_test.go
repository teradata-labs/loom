// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/types"
)

// seedEventLogSessionSQLite inserts a minimal sessions row, mirroring the
// INSERT shape at pkg/storage/sqlite/migrator_test.go:171.
func seedEventLogSessionSQLite(t *testing.T, db *sql.DB) string {
	t.Helper()
	sessionID := "evtlog-test-" + uuid.NewString()
	now := time.Now().Unix()
	_, err := db.Exec(
		"INSERT INTO sessions (id, name, agent_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		sessionID, "event-log-test", "agent-1", now, now,
	)
	require.NoError(t, err)
	return sessionID
}

func TestEventLogStore_RecordIntentThenResult_LoadEventsOrdered(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	require.NoError(t, store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)))
	require.NoError(t, store.RecordResult(ctx, sessionID, 0, 0, eventlog.EventLLMCallResult, "", []byte(`{"content":"hi"}`)))
	require.NoError(t, store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventToolCallIntent, "call-1", []byte(`{"tool_name":"search"}`)))

	events, err := store.LoadEvents(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, eventlog.EventLLMCallIntent, events[0].EventType)
	require.Equal(t, eventlog.EventLLMCallResult, events[1].EventType)
	require.Equal(t, "call-1", events[2].ToolUseID)
}

// created_at is written in UTC; the parsed time must be the real instant, not
// a local-time string read as UTC.
func TestEventLogStore_LoadEvents_CreatedAtIsUTCInstant(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	before := time.Now()
	require.NoError(t, store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)))
	after := time.Now()

	events, err := store.LoadEvents(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	got := events[0].CreatedAt
	require.False(t, got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)),
		"CreatedAt = %v, want within a second of [%v, %v]", got, before, after)
}

func TestEventLogStore_RecordIntent_IdempotentOnRetry(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	for i := 0; i < 3; i++ {
		require.NoError(t, store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventToolCallIntent, "call-1", []byte(`{"a":1}`)), "attempt %d", i)
	}
	events, err := store.LoadEvents(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1, "identical retries must leave one row")
}

// Two writers recording different calls at the same step and position must
// not have the second one silently dropped.
func TestEventLogStore_RecordIntent_DifferentPayloadConflicts(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	require.NoError(t, store.RecordIntent(ctx, sessionID, 3, 0, eventlog.EventToolCallIntent, "a", []byte(`{"tool_name":"one"}`)))
	err := store.RecordIntent(ctx, sessionID, 3, 0, eventlog.EventToolCallIntent, "b", []byte(`{"tool_name":"two"}`))
	require.True(t, errors.Is(err, eventlog.ErrEventConflict), "got %v, want ErrEventConflict", err)
}

func TestEventLogStore_NextStepIndex_ZeroThenIncrementing(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	next, err := store.NextStepIndex(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, 0, next)

	require.NoError(t, store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)))
	require.NoError(t, store.RecordIntent(ctx, sessionID, 1, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)))
	next, err = store.NextStepIndex(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, 2, next)
}

func TestEventLogStore_SessionLease(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()

	t.Run("one holder at a time", func(t *testing.T) {
		sessionID := seedEventLogSessionSQLite(t, db)
		won, err := store.AcquireSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.True(t, won)
		won, err = store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.False(t, won, "a live lease must not be taken by another holder")
		won, err = store.AcquireSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.True(t, won, "the holder may re-acquire its own lease")
	})

	t.Run("expired lease is taken over and the old holder loses it", func(t *testing.T) {
		sessionID := seedEventLogSessionSQLite(t, db)
		won, err := store.AcquireSessionLease(ctx, sessionID, "h1", -time.Second)
		require.NoError(t, err)
		require.True(t, won)
		won, err = store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.True(t, won, "an expired lease must be reclaimable")
		held, err := store.RenewSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.False(t, held, "the old holder must learn it lost the lease")
	})

	t.Run("renewal keeps the lease past its first expiry", func(t *testing.T) {
		sessionID := seedEventLogSessionSQLite(t, db)
		won, err := store.AcquireSessionLease(ctx, sessionID, "h1", -time.Second)
		require.NoError(t, err)
		require.True(t, won)
		held, err := store.RenewSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.True(t, held)
		won, err = store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.False(t, won, "a renewed lease must not be taken over")
	})

	t.Run("only the holder releases", func(t *testing.T) {
		sessionID := seedEventLogSessionSQLite(t, db)
		_, err := store.AcquireSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.NoError(t, store.ReleaseSessionLease(ctx, sessionID, "h2"))
		won, err := store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.False(t, won, "a release by another holder must not clear the lease")
		require.NoError(t, store.ReleaseSessionLease(ctx, sessionID, "h1"))
		won, err = store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.True(t, won)
	})
}

func TestEventLogStore_Prune(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	for step := 0; step < 4; step++ {
		require.NoError(t, store.RecordIntent(ctx, sessionID, step, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)))
	}
	require.NoError(t, store.PruneResolvedStep(ctx, sessionID, 3))
	require.NoError(t, store.PruneStepsBefore(ctx, sessionID, 2))

	events, err := store.LoadEvents(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, 2, events[0].StepIndex)
}

// SQLite has no row-level security, so the store itself must keep one user's
// events and leases away from another's.
func TestEventLogStore_TenantIsolation(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctxA := types.ContextWithUserID(context.Background(), "user-a")
	ctxB := types.ContextWithUserID(context.Background(), "user-b")
	sessionID := seedEventLogSessionSQLite(t, db)

	require.NoError(t, store.RecordIntent(ctxA, sessionID, 0, 0, eventlog.EventToolCallIntent, "c", []byte(`{"tool_name":"a"}`)))

	events, err := store.LoadEvents(ctxB, sessionID)
	require.NoError(t, err)
	require.Empty(t, events, "user B read user A's events")
	next, err := store.NextStepIndex(ctxB, sessionID)
	require.NoError(t, err)
	require.Equal(t, 0, next, "user B saw user A's step counter")

	// B's write at the same key must neither collide with nor replace A's.
	require.NoError(t, store.RecordIntent(ctxB, sessionID, 0, 0, eventlog.EventToolCallIntent, "c", []byte(`{"tool_name":"b"}`)))
	require.NoError(t, store.PruneResolvedStep(ctxB, sessionID, 0))
	require.NoError(t, store.PruneStepsBefore(ctxB, sessionID, 10))
	eventsA, err := store.LoadEvents(ctxA, sessionID)
	require.NoError(t, err)
	require.Len(t, eventsA, 1, "user B's writes or prunes changed user A's events")
	require.JSONEq(t, `{"tool_name":"a"}`, string(eventsA[0].Payload))

	won, err := store.AcquireSessionLease(ctxA, sessionID, "a", time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	held, err := store.RenewSessionLease(ctxB, sessionID, "a", time.Minute)
	require.NoError(t, err)
	require.False(t, held, "user B renewed user A's lease")
	require.NoError(t, store.ReleaseSessionLease(ctxB, sessionID, "a"))
	held, err = store.RenewSessionLease(ctxA, sessionID, "a", time.Minute)
	require.NoError(t, err)
	require.True(t, held, "user B released user A's lease")
}
