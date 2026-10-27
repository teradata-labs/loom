// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
)

// seedEventLogSessionSQLite inserts a minimal sessions row, mirroring the
// INSERT shape at pkg/storage/sqlite/migrator_test.go:171.
func seedEventLogSessionSQLite(t *testing.T, db *sql.DB) string {
	t.Helper()
	sessionID := fmt.Sprintf("evtlog-test-%d", time.Now().UnixNano())
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

	if err := store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordResult(ctx, sessionID, 0, 0, eventlog.EventLLMCallResult, "", []byte(`{"content":"hi"}`)); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	if err := store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventToolCallIntent, "call-1", []byte(`{"tool_name":"search"}`)); err != nil {
		t.Fatalf("RecordIntent (tool): %v", err)
	}

	events, err := store.LoadEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	if events[0].EventType != eventlog.EventLLMCallIntent || events[1].EventType != eventlog.EventLLMCallResult {
		t.Errorf("events not in expected order: %+v", events)
	}
	if events[2].ToolUseID != "call-1" {
		t.Errorf("events[2].ToolUseID = %q, want %q", events[2].ToolUseID, "call-1")
	}
}

func TestEventLogStore_LoadEvents_CreatedAtParsedCorrectly(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	beforeRecord := time.Now()
	if err := store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	afterRecord := time.Now()

	events, err := store.LoadEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	e := events[0]
	if e.CreatedAt.IsZero() {
		t.Error("ExecutionEvent.CreatedAt is zero time; datetime parsing failed")
	}

	// Verify CreatedAt is reasonably close to the time we recorded (within a few seconds).
	// Account for SQLite's datetime('now') truncating to whole-second resolution.
	lowerBound := beforeRecord.Add(-2 * time.Second)
	upperBound := afterRecord.Add(5 * time.Second)
	if e.CreatedAt.Before(lowerBound) || e.CreatedAt.After(upperBound) {
		t.Errorf("CreatedAt = %v, not within [%v, %v]", e.CreatedAt, lowerBound, upperBound)
	}
}

func TestEventLogStore_RecordIntent_IdempotentOnRetry(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	for i := 0; i < 3; i++ {
		if err := store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventToolCallIntent, "call-1", []byte(`{}`)); err != nil {
			t.Fatalf("RecordIntent attempt %d: %v", i, err)
		}
	}
	events, err := store.LoadEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d after 3 identical RecordIntent calls, want 1 (idempotent)", len(events))
	}
}

func TestEventLogStore_NextStepIndex_ZeroThenIncrementing(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	next, err := store.NextStepIndex(ctx, sessionID)
	if err != nil {
		t.Fatalf("NextStepIndex (empty): %v", err)
	}
	if next != 0 {
		t.Errorf("NextStepIndex on a session with no events = %d, want 0", next)
	}

	if err := store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	if err := store.RecordIntent(ctx, sessionID, 3, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)); err != nil {
		t.Fatalf("RecordIntent: %v", err)
	}
	next, err = store.NextStepIndex(ctx, sessionID)
	if err != nil {
		t.Fatalf("NextStepIndex: %v", err)
	}
	if next != 4 {
		t.Errorf("NextStepIndex after max step_index=3 = %d, want 4", next)
	}
}

func TestEventLogStore_ClaimSessionResume_ExactlyOneWinner(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	won1, err := store.ClaimSessionResume(ctx, sessionID, "token-1", time.Minute)
	if err != nil || !won1 {
		t.Fatalf("first claim: won=%v err=%v", won1, err)
	}
	won2, err := store.ClaimSessionResume(ctx, sessionID, "token-2", time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if won2 {
		t.Error("second claim should not win while the first claim's lease is live")
	}
}

func TestEventLogStore_ClaimSessionResume_StaleClaimIsReclaimable(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	won1, err := store.ClaimSessionResume(ctx, sessionID, "token-1", -1*time.Second) // already-expired lease
	if err != nil || !won1 {
		t.Fatalf("first claim: won=%v err=%v", won1, err)
	}

	won2, err := store.ClaimSessionResume(ctx, sessionID, "token-2", time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if !won2 {
		t.Error("a claim whose lease already expired must be reclaimable")
	}
}

func TestEventLogStore_ReleaseSessionResume_OnlyOwnerCanRelease(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	if _, err := store.ClaimSessionResume(ctx, sessionID, "token-1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.ReleaseSessionResume(ctx, sessionID, "wrong-token"); err != nil {
		t.Fatalf("release with wrong token should no-op, not error: %v", err)
	}
	won, err := store.ClaimSessionResume(ctx, sessionID, "token-2", time.Minute)
	if err != nil {
		t.Fatalf("claim after wrong-token release attempt: %v", err)
	}
	if won {
		t.Error("claim must still be held by token-1 — a wrong-token release must not have cleared it")
	}

	if err := store.ReleaseSessionResume(ctx, sessionID, "token-1"); err != nil {
		t.Fatalf("release with correct token: %v", err)
	}
	won, err = store.ClaimSessionResume(ctx, sessionID, "token-2", time.Minute)
	if err != nil || !won {
		t.Fatalf("claim after correct release: won=%v err=%v, want true/nil", won, err)
	}
}

func TestEventLogStore_PruneResolvedStep_RemovesOnlyThatStep(t *testing.T) {
	db := migratedDB(t)
	store := NewEventLogStore(db, nil)
	ctx := context.Background()
	sessionID := seedEventLogSessionSQLite(t, db)

	if err := store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)); err != nil {
		t.Fatalf("RecordIntent step 0: %v", err)
	}
	if err := store.RecordIntent(ctx, sessionID, 1, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)); err != nil {
		t.Fatalf("RecordIntent step 1: %v", err)
	}
	if err := store.PruneResolvedStep(ctx, sessionID, 0); err != nil {
		t.Fatalf("PruneResolvedStep: %v", err)
	}
	events, err := store.LoadEvents(ctx, sessionID)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 || events[0].StepIndex != 1 {
		t.Fatalf("events after pruning step 0 = %+v, want only step 1's event", events)
	}
}
