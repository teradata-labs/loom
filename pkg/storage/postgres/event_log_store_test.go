// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/observability"
)

// testEventLogPool skips unless TEST_POSTGRES_URL is set, matching this
// package's existing live-DB test convention — see testHumanRequestStore in
// human_request_store_test.go, which this mirrors exactly.
func testEventLogPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// seedEventLogSession inserts a minimal sessions row so execution_events'
// session_id foreign key is satisfiable — mirrors seedSession in
// human_request_store_test.go (that helper takes *HumanRequestStore, not
// reusable here directly, so this is the same SQL against *EventLogStore's
// pool instead).
func seedEventLogSession(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	sessionID := fmt.Sprintf("evtlog-test-%d", time.Now().UnixNano())
	_, err := pool.Exec(context.Background(),
		"INSERT INTO sessions (id, agent_id) VALUES ($1, 'agent-1') ON CONFLICT (id) DO NOTHING", sessionID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})
	return sessionID
}

func TestEventLogStore_RecordIntentThenResult_LoadEventsOrdered(t *testing.T) {
	pool := testEventLogPool(t)
	store := NewEventLogStore(pool, nil)
	ctx := context.Background()
	sessionID := seedEventLogSession(t, pool)

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

func TestEventLogStore_RecordIntent_IdempotentOnRetry(t *testing.T) {
	pool := testEventLogPool(t)
	store := NewEventLogStore(pool, nil)
	ctx := context.Background()
	sessionID := seedEventLogSession(t, pool)

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
	pool := testEventLogPool(t)
	store := NewEventLogStore(pool, nil)
	ctx := context.Background()
	sessionID := seedEventLogSession(t, pool)

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
	pool := testEventLogPool(t)
	store := NewEventLogStore(pool, nil)
	ctx := context.Background()
	sessionID := seedEventLogSession(t, pool)

	won1, err := store.ClaimSessionResume(ctx, sessionID, "token-1", time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !won1 {
		t.Fatal("first claim on an unclaimed session should win")
	}

	won2, err := store.ClaimSessionResume(ctx, sessionID, "token-2", time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if won2 {
		t.Error("second claim on an already-claimed, non-expired session should NOT win")
	}
}

func TestEventLogStore_ClaimSessionResume_StaleClaimIsReclaimable(t *testing.T) {
	pool := testEventLogPool(t)
	store := NewEventLogStore(pool, nil)
	ctx := context.Background()
	sessionID := seedEventLogSession(t, pool)

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
	pool := testEventLogPool(t)
	store := NewEventLogStore(pool, nil)
	ctx := context.Background()
	sessionID := seedEventLogSession(t, pool)

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
	pool := testEventLogPool(t)
	store := NewEventLogStore(pool, nil)
	ctx := context.Background()
	sessionID := seedEventLogSession(t, pool)

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
