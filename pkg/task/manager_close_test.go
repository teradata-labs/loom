// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package task

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// sentinelWithoutRowStore reports ErrTaskAlreadyTerminal from CloseTask with a
// nil task — the shape the Postgres store had, and the shape any downstream
// store that adopts the sentinel without reading the contract closely will
// have. Embedding is fine for this fake: lifecycleStore implements no optional
// capability, so nothing is wrongly promoted.
type sentinelWithoutRowStore struct {
	*lifecycleStore
}

func (s *sentinelWithoutRowStore) CloseTask(_ context.Context, id, _ string) (*Task, error) {
	return nil, fmt.Errorf("close task %s: %w", id, ErrTaskAlreadyTerminal)
}

// TestManagerCloseTask_SentinelWithoutARowStillReturnsATask pins the manager's
// half of the double-close panic: on the sentinel path it must hand its caller
// a task, and when the store did not supply one, the pre-close read is the
// settled row. (nil, nil) here reached task_board's close tool as a nil
// dereference.
func TestManagerCloseTask_SentinelWithoutARowStillReturnsATask(t *testing.T) {
	inner := newLifecycleStore()
	created, err := inner.CreateTask(context.Background(), &Task{
		Title: "settled", Status: loomv1.TaskStatus_TASK_STATUS_DONE, SkillIdempotencyKey: "k",
	})
	require.NoError(t, err)

	mgr := NewManager(&sentinelWithoutRowStore{lifecycleStore: inner}, nil, nil, zap.NewNop())
	got, err := mgr.CloseTask(context.Background(), created.ID, "again")
	require.NoError(t, err, "a lost close race is benign")
	require.NotNil(t, got, "the sentinel branch must never return (nil, nil)")
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, got.Status)
}

// updatingStore is a lifecycleStore with a real UpdateTask and NO TaskCanceller
// capability — the shape of a downstream store that predates the capability.
// Manager.CancelTask must take the read-modify-write fallback against it.
type updatingStore struct {
	*lifecycleStore
}

func (s *updatingStore) UpdateTask(_ context.Context, t *Task, _ []string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, existing := range s.byKey {
		if existing.ID == t.ID {
			cp := *t
			s.byKey[k] = &cp
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("task %s not found", t.ID)
}

// TestManagerCancelTask_FallbackGuardsTerminalRows: without the capability the
// manager cannot decide a true race at the row, but it must still refuse the
// sequential cases — a second cancel, or a cancel after a close — rather than
// flip a settled row and re-fire its side effects.
func TestManagerCancelTask_FallbackGuardsTerminalRows(t *testing.T) {
	inner := newLifecycleStore()
	store := &updatingStore{lifecycleStore: inner}
	if _, ok := TaskStore(store).(TaskCanceller); ok {
		t.Fatal("rig sanity: this fake must NOT implement TaskCanceller, or the fallback is untested")
	}
	mgr := NewManager(store, nil, nil, zap.NewNop())
	ctx := context.Background()

	open, err := inner.CreateTask(ctx, &Task{Title: "open", Status: loomv1.TaskStatus_TASK_STATUS_OPEN, SkillIdempotencyKey: "k1"})
	require.NoError(t, err)
	cancelled, err := mgr.CancelTask(ctx, open.ID, "not needed")
	require.NoError(t, err)
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_CANCELLED, cancelled.Status, "the fallback still cancels an open task")

	again, err := mgr.CancelTask(ctx, open.ID, "second")
	require.NoError(t, err)
	require.Equal(t, "not needed", again.CloseReason, "a second cancel is a no-op, not an overwrite")

	done, err := inner.CreateTask(ctx, &Task{Title: "done", Status: loomv1.TaskStatus_TASK_STATUS_DONE, CloseReason: "finished", SkillIdempotencyKey: "k2"})
	require.NoError(t, err)
	got, err := mgr.CancelTask(ctx, done.ID, "late")
	require.NoError(t, err)
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, got.Status, "a cancel must never downgrade DONE")
	require.Equal(t, "finished", got.CloseReason)
}
