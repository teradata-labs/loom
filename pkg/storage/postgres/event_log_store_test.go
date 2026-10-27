// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
)

// eventLogFixture is an event log store connected as an RLS-subject role (see
// rlsSubjectPool), plus the admin pool for seeding and cleanup. Tests against
// the superuser connection would pass whether or not the policies work.
type eventLogFixture struct {
	admin *pgxpool.Pool
	app   *pgxpool.Pool
	store *EventLogStore
}

func newEventLogFixture(t *testing.T) *eventLogFixture {
	t.Helper()
	admin := migratedAdminPool(t)
	app := rlsSubjectPool(t, admin)
	return &eventLogFixture{admin: admin, app: app, store: NewEventLogStore(app, nil)}
}

// session seeds a sessions row owned by a fresh user and returns that user's
// context and the session ID.
func (f *eventLogFixture) session(t *testing.T) (context.Context, string) {
	t.Helper()
	userID := "evtlog-user-" + uuid.NewString()
	sessionID := "evtlog-sess-" + uuid.NewString()
	_, err := f.admin.Exec(context.Background(),
		"INSERT INTO sessions (id, agent_id, user_id) VALUES ($1, 'agent-1', $2)", sessionID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = f.admin.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})
	return ContextWithUserID(context.Background(), userID), sessionID
}

func TestExecutionEventTables_RLSForced(t *testing.T) {
	admin := migratedAdminPool(t)
	requireForcedRLS(t, admin, "execution_events")
	requireForcedRLS(t, admin, "execution_leases")
}

func TestEventLogStore_RecordIntentThenResult_LoadEventsOrdered(t *testing.T) {
	f := newEventLogFixture(t)
	ctx, sessionID := f.session(t)

	require.NoError(t, f.store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)))
	require.NoError(t, f.store.RecordResult(ctx, sessionID, 0, 0, eventlog.EventLLMCallResult, "", []byte(`{"content":"hi"}`)))
	require.NoError(t, f.store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventToolCallIntent, "call-1", []byte(`{"tool_name":"search"}`)))

	events, err := f.store.LoadEvents(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, eventlog.EventLLMCallIntent, events[0].EventType)
	require.Equal(t, eventlog.EventLLMCallResult, events[1].EventType)
	require.Equal(t, "call-1", events[2].ToolUseID)
}

func TestEventLogStore_RecordIntent_IdempotentOnRetry(t *testing.T) {
	f := newEventLogFixture(t)
	ctx, sessionID := f.session(t)

	for i := 0; i < 3; i++ {
		require.NoError(t, f.store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventToolCallIntent, "call-1", []byte(`{"a": 1}`)), "attempt %d", i)
	}
	events, err := f.store.LoadEvents(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1, "identical retries must leave one row")
}

// Two writers recording different calls at the same step and position must
// not have the second one silently dropped.
func TestEventLogStore_RecordIntent_DifferentPayloadConflicts(t *testing.T) {
	f := newEventLogFixture(t)
	ctx, sessionID := f.session(t)

	require.NoError(t, f.store.RecordIntent(ctx, sessionID, 3, 0, eventlog.EventToolCallIntent, "a", []byte(`{"tool_name":"one"}`)))
	err := f.store.RecordIntent(ctx, sessionID, 3, 0, eventlog.EventToolCallIntent, "b", []byte(`{"tool_name":"two"}`))
	require.True(t, errors.Is(err, eventlog.ErrEventConflict), "got %v, want ErrEventConflict", err)
}

func TestEventLogStore_NextStepIndex_ZeroThenIncrementing(t *testing.T) {
	f := newEventLogFixture(t)
	ctx, sessionID := f.session(t)

	next, err := f.store.NextStepIndex(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, 0, next)

	require.NoError(t, f.store.RecordIntent(ctx, sessionID, 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)))
	require.NoError(t, f.store.RecordIntent(ctx, sessionID, 1, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)))
	next, err = f.store.NextStepIndex(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, 2, next)
}

func TestEventLogStore_SessionLease(t *testing.T) {
	f := newEventLogFixture(t)

	t.Run("one holder at a time", func(t *testing.T) {
		ctx, sessionID := f.session(t)
		won, err := f.store.AcquireSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.True(t, won, "the session's owner must be able to take its lease")
		won, err = f.store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.False(t, won)
		won, err = f.store.AcquireSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.True(t, won, "the holder may re-acquire its own lease")
	})

	t.Run("concurrent acquires have exactly one winner", func(t *testing.T) {
		ctx, sessionID := f.session(t)
		const n = 8
		var wg sync.WaitGroup
		wins := make(chan string, n)
		for i := 0; i < n; i++ {
			holder := uuid.NewString()
			wg.Add(1)
			go func() {
				defer wg.Done()
				won, err := f.store.AcquireSessionLease(ctx, sessionID, holder, time.Minute)
				if err == nil && won {
					wins <- holder
				}
			}()
		}
		wg.Wait()
		close(wins)
		require.Len(t, wins, 1)
	})

	t.Run("expired lease is taken over and the old holder loses it", func(t *testing.T) {
		ctx, sessionID := f.session(t)
		won, err := f.store.AcquireSessionLease(ctx, sessionID, "h1", -time.Second)
		require.NoError(t, err)
		require.True(t, won)
		won, err = f.store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.True(t, won, "an expired lease must be reclaimable")
		held, err := f.store.RenewSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.False(t, held, "the old holder must learn it lost the lease")
	})

	t.Run("renewal keeps the lease past its first expiry", func(t *testing.T) {
		ctx, sessionID := f.session(t)
		won, err := f.store.AcquireSessionLease(ctx, sessionID, "h1", -time.Second)
		require.NoError(t, err)
		require.True(t, won)
		held, err := f.store.RenewSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.True(t, held)
		won, err = f.store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.False(t, won, "a renewed lease must not be taken over")
	})

	t.Run("only the holder releases", func(t *testing.T) {
		ctx, sessionID := f.session(t)
		_, err := f.store.AcquireSessionLease(ctx, sessionID, "h1", time.Minute)
		require.NoError(t, err)
		require.NoError(t, f.store.ReleaseSessionLease(ctx, sessionID, "h2"))
		won, err := f.store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.False(t, won, "a release by another holder must not clear the lease")
		require.NoError(t, f.store.ReleaseSessionLease(ctx, sessionID, "h1"))
		won, err = f.store.AcquireSessionLease(ctx, sessionID, "h2", time.Minute)
		require.NoError(t, err)
		require.True(t, won)
	})
}

func TestEventLogStore_Prune(t *testing.T) {
	f := newEventLogFixture(t)
	ctx, sessionID := f.session(t)

	for step := 0; step < 4; step++ {
		require.NoError(t, f.store.RecordIntent(ctx, sessionID, step, 0, eventlog.EventLLMCallIntent, "", []byte(`{}`)))
	}
	require.NoError(t, f.store.PruneResolvedStep(ctx, sessionID, 3))
	require.NoError(t, f.store.PruneStepsBefore(ctx, sessionID, 2))

	events, err := f.store.LoadEvents(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, 2, events[0].StepIndex)
}

// TestEventLogStore_TenantIsolation covers the cross-tenant cases from review
// round 1: another user must not read, write, delete or lease events on a
// session it does not own.
func TestEventLogStore_TenantIsolation(t *testing.T) {
	f := newEventLogFixture(t)
	ctxA, sessionID := f.session(t)
	ctxB, _ := f.session(t) // user B, with a session of its own

	require.NoError(t, f.store.RecordIntent(ctxA, sessionID, 0, 0, eventlog.EventToolCallIntent, "c", []byte(`{"tool_name":"a","input":"secret"}`)))

	events, err := f.store.LoadEvents(ctxB, sessionID)
	require.NoError(t, err)
	require.Empty(t, events, "user B read user A's events")
	next, err := f.store.NextStepIndex(ctxB, sessionID)
	require.NoError(t, err)
	require.Equal(t, 0, next, "user B saw user A's step counter")

	err = f.store.RecordIntent(ctxB, sessionID, 5, 0, eventlog.EventToolCallIntent, "c", []byte(`{"tool_name":"b"}`))
	require.Error(t, err, "user B wrote an event into user A's session")

	require.NoError(t, f.store.PruneResolvedStep(ctxB, sessionID, 0))
	require.NoError(t, f.store.PruneStepsBefore(ctxB, sessionID, 10))
	eventsA, err := f.store.LoadEvents(ctxA, sessionID)
	require.NoError(t, err)
	require.Len(t, eventsA, 1, "user B's prunes deleted user A's events")

	_, err = f.store.AcquireSessionLease(ctxB, sessionID, "b", time.Minute)
	require.Error(t, err, "user B took a lease on user A's session")
	won, err := f.store.AcquireSessionLease(ctxA, sessionID, "a", time.Minute)
	require.NoError(t, err)
	require.True(t, won, "user A could not lease its own session")
	held, err := f.store.RenewSessionLease(ctxB, sessionID, "a", time.Minute)
	require.NoError(t, err)
	require.False(t, held, "user B renewed user A's lease")
	require.NoError(t, f.store.ReleaseSessionLease(ctxB, sessionID, "a"))
	held, err = f.store.RenewSessionLease(ctxA, sessionID, "a", time.Minute)
	require.NoError(t, err)
	require.True(t, held, "user B released user A's lease")

	// The policy itself, not only the store's WHERE clauses, hides A's rows.
	var visible int
	require.NoError(t, execInTx(ctxB, f.app, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM execution_events WHERE session_id = $1", sessionID).Scan(&visible)
	}))
	require.Zero(t, visible, "row-level security let user B see user A's events")
}
