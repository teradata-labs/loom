// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// TestCloseTask_DoubleCloseReturnsTheSettledRowWithTheSentinel pins the
// contract the store-level close guard added in round 5 and shipped untested:
// a second close is a row-level no-op that reports ErrTaskAlreadyTerminal AND
// hands back the settled row. Manager.CloseTask returns that row to its
// caller on the sentinel path, and task_board's close tool dereferences it —
// so a store that reports the sentinel with a nil task panics the tool. The
// Postgres twin did exactly that; this test and its Postgres counterpart keep
// both stores on the same contract.
func TestCloseTask_DoubleCloseReturnsTheSettledRowWithTheSentinel(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	created, err := store.CreateTask(ctx, &task.Task{
		Title: "close me", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)

	first, err := store.CloseTask(ctx, created.ID, "first reason")
	require.NoError(t, err)
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, first.Status)

	second, err := store.CloseTask(ctx, created.ID, "second reason")
	require.ErrorIs(t, err, task.ErrTaskAlreadyTerminal,
		"a close that finds the row terminal must report the sentinel, not succeed or fail generically")
	require.NotNil(t, second, "the sentinel travels WITH the settled row; nil here is the panic")
	assert.Equal(t, created.ID, second.ID)
	assert.Equal(t, "first reason", second.CloseReason,
		"the losing close must not overwrite the winner's reason")
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, second.Status)

	// The guard is on terminal status, not on DONE specifically: a CANCELLED
	// row is equally settled.
	cancelled, err := store.CreateTask(ctx, &task.Task{
		Title: "cancelled", Status: loomv1.TaskStatus_TASK_STATUS_CANCELLED,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)
	got, err := store.CloseTask(ctx, cancelled.ID, "late close")
	require.ErrorIs(t, err, task.ErrTaskAlreadyTerminal)
	require.NotNil(t, got)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_CANCELLED, got.Status,
		"closing a cancelled task must not resurrect it as DONE")

	// A missing row is a plain error, not the sentinel — the manager's sentinel
	// branch returns success, and a close of nothing is not a success.
	_, err = store.CloseTask(ctx, "no-such-task", "x")
	require.Error(t, err)
	assert.False(t, errors.Is(err, task.ErrTaskAlreadyTerminal),
		"only a terminal row earns the sentinel")
}

// TestManagerCloseTask_DoubleCloseThroughTheManagerReturnsATask is the
// end-to-end shape of the panic: the manager's sentinel branch must return a
// task, never (nil, nil).
func TestManagerCloseTask_DoubleCloseThroughTheManagerReturnsATask(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
	ctx := context.Background()

	created, err := mgr.CreateTask(ctx, &task.Task{
		Title: "twice", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)

	_, err = mgr.CloseTask(ctx, created.ID, "done")
	require.NoError(t, err)

	again, err := mgr.CloseTask(ctx, created.ID, "done again")
	require.NoError(t, err, "a lost close race is benign, not an error")
	require.NotNil(t, again, "task_board's close tool builds a detail map from this — nil panics")
	assert.Equal(t, created.ID, again.ID)
	assert.Equal(t, "done", again.CloseReason)
}

// noAggregateStore holds a TaskStore in a NAMED field so that CountByStatus is
// not promoted and the manager's type assertion for task.StatusCounter fails.
// This is the exact shape the StatusCounter contract prescribes for scoping
// wrappers, and it is the only way to drive Manager.countByStatusPaged against
// a real store from a test.
type noAggregateStore struct {
	inner *TaskStore
}

func (w *noAggregateStore) CreateTask(ctx context.Context, t *task.Task) (*task.Task, error) {
	return w.inner.CreateTask(ctx, t)
}
func (w *noAggregateStore) GetTask(ctx context.Context, id string) (*task.Task, error) {
	return w.inner.GetTask(ctx, id)
}
func (w *noAggregateStore) GetTaskByIdempotencyKey(ctx context.Context, key string) (*task.Task, error) {
	return w.inner.GetTaskByIdempotencyKey(ctx, key)
}
func (w *noAggregateStore) HasOpenSkillTasks(ctx context.Context, a, b string) (bool, error) {
	return w.inner.HasOpenSkillTasks(ctx, a, b)
}
func (w *noAggregateStore) ListBySkillRun(ctx context.Context, a, b string) ([]*task.Task, error) {
	return w.inner.ListBySkillRun(ctx, a, b)
}
func (w *noAggregateStore) UpdateTask(ctx context.Context, t *task.Task, fields []string) (*task.Task, error) {
	return w.inner.UpdateTask(ctx, t, fields)
}
func (w *noAggregateStore) SetAcceptanceCriteria(ctx context.Context, id, c string) (*task.Task, error) {
	return w.inner.SetAcceptanceCriteria(ctx, id, c)
}
func (w *noAggregateStore) DeleteTask(ctx context.Context, id string) error {
	return w.inner.DeleteTask(ctx, id)
}
func (w *noAggregateStore) ListTasks(ctx context.Context, opts task.ListTasksOpts) ([]*task.Task, int, error) {
	return w.inner.ListTasks(ctx, opts)
}
func (w *noAggregateStore) ClaimTask(ctx context.Context, a, b, c string) (*task.Task, error) {
	return w.inner.ClaimTask(ctx, a, b, c)
}
func (w *noAggregateStore) ReleaseTask(ctx context.Context, a, b string) (*task.Task, error) {
	return w.inner.ReleaseTask(ctx, a, b)
}
func (w *noAggregateStore) CloseTask(ctx context.Context, id, reason string) (*task.Task, error) {
	return w.inner.CloseTask(ctx, id, reason)
}
func (w *noAggregateStore) TransitionTask(ctx context.Context, id string, s loomv1.TaskStatus) (*task.Task, error) {
	return w.inner.TransitionTask(ctx, id, s)
}
func (w *noAggregateStore) AddDependency(ctx context.Context, d *task.TaskDependency) error {
	return w.inner.AddDependency(ctx, d)
}
func (w *noAggregateStore) RemoveDependency(ctx context.Context, a, b string) error {
	return w.inner.RemoveDependency(ctx, a, b)
}
func (w *noAggregateStore) GetDependencies(ctx context.Context, id string) ([]*task.TaskDependency, error) {
	return w.inner.GetDependencies(ctx, id)
}
func (w *noAggregateStore) GetDependents(ctx context.Context, id string) ([]*task.TaskDependency, error) {
	return w.inner.GetDependents(ctx, id)
}
func (w *noAggregateStore) GetReadyFront(ctx context.Context, b string, o task.ReadyFrontOpts) ([]*task.Task, error) {
	return w.inner.GetReadyFront(ctx, b, o)
}
func (w *noAggregateStore) GetBlockedTasks(ctx context.Context, b string) ([]*task.Task, error) {
	return w.inner.GetBlockedTasks(ctx, b)
}
func (w *noAggregateStore) CreateBoard(ctx context.Context, b *task.TaskBoard) (*task.TaskBoard, error) {
	return w.inner.CreateBoard(ctx, b)
}
func (w *noAggregateStore) GetBoard(ctx context.Context, id string) (*task.TaskBoard, error) {
	return w.inner.GetBoard(ctx, id)
}
func (w *noAggregateStore) ListBoards(ctx context.Context) ([]*task.TaskBoard, error) {
	return w.inner.ListBoards(ctx)
}
func (w *noAggregateStore) RecordHistory(ctx context.Context, e *task.TaskHistoryEntry) error {
	return w.inner.RecordHistory(ctx, e)
}
func (w *noAggregateStore) GetHistory(ctx context.Context, id string) ([]*task.TaskHistoryEntry, error) {
	return w.inner.GetHistory(ctx, id)
}
func (w *noAggregateStore) Close() error { return w.inner.Close() }

var _ task.TaskStore = (*noAggregateStore)(nil)

// TestPagedCountIsExactOverNonUniqueSortKeys drives the manager's paged
// fallback — the path a downstream store without StatusCounter takes — across
// several OFFSET pages of rows that share (priority, created_at). created_at is
// stored at second precision, so 1,100 inserts produce large runs of equal
// keys; without a unique tiebreak in ORDER BY, adjacent pages double-count
// and drop rows across boundaries (the overlap this repo's admin doc
// measured). Exactness here is the property the round-5 rowid tiebreak exists
// for, and round 5 shipped it without a test.
//
// Honest limit: SQLite tends to return equal-key rows in rowid order even
// without the tiebreak, so this pins the paging contract more than it proves
// the ORDER BY. The Postgres twin is where equal-key order is genuinely
// unstable across plans.
func TestPagedCountIsExactOverNonUniqueSortKeys(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()
	_, err := store.CreateBoard(ctx, &task.TaskBoard{ID: "paged", Name: "paged"})
	require.NoError(t, err)

	// 1,100 rows: strictly more than two 500-row fallback pages, so at least
	// two page boundaries fall inside runs of equal (priority, created_at).
	const open, done = 600, 500
	for i := 0; i < open; i++ {
		_, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("o%d", i), BoardID: "paged",
			Status: loomv1.TaskStatus_TASK_STATUS_OPEN, CreatedVia: taskctx.CreatedViaAgent})
		require.NoError(t, err)
	}
	for i := 0; i < done; i++ {
		_, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("d%d", i), BoardID: "paged",
			Status: loomv1.TaskStatus_TASK_STATUS_DONE, CreatedVia: taskctx.CreatedViaAgent})
		require.NoError(t, err)
	}

	mgr := task.NewManager(&noAggregateStore{inner: store}, nil, observability.NewNoOpTracer(), nil)
	counts, err := mgr.CountByStatus(ctx, task.CountByStatusOpts{BoardID: "paged"})
	require.NoError(t, err)
	assert.Equal(t, open+done, counts.Total, "paged fallback must be exact, not approximately right")
	assert.Equal(t, open, counts.Open)
	assert.Equal(t, done, counts.Done)

	// The same property stated directly on ListTasks, both sort directions:
	// paging the whole board with a small window yields every id exactly once.
	for _, newest := range []bool{false, true} {
		seen := map[string]int{}
		for offset := 0; ; offset += 97 {
			page, _, err := store.ListTasks(ctx, task.ListTasksOpts{
				BoardID: "paged", Limit: 97, Offset: offset, NewestFirst: newest})
			require.NoError(t, err)
			for _, tk := range page {
				seen[tk.ID]++
			}
			if len(page) < 97 {
				break
			}
		}
		assert.Len(t, seen, open+done, "NewestFirst=%v: every row appears", newest)
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("NewestFirst=%v: task %s appeared %d times across pages", newest, id, n)
			}
		}
	}
}

// TestCancelTask_IsGuardedLikeClose pins the TaskCanceller capability: a
// cancel is a terminal transition with the same status guard as close, so a
// cancel that loses to a close (or a second cancel) changes nothing and reports
// ErrTaskAlreadyTerminal with the settled row. Before the capability, the
// manager cancelled by read-modify-write and a cancel racing a close flipped a
// DONE row to CANCELLED.
func TestCancelTask_IsGuardedLikeClose(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	var _ task.TaskCanceller = store

	created, err := store.CreateTask(ctx, &task.Task{
		Title: "cancel me", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)
	claimed, err := store.ClaimTask(ctx, created.ID, "agent-1", "sess-1")
	require.NoError(t, err)
	require.NotNil(t, claimed.ClaimedAt, "rig sanity: the claim stamped claimed_at")

	cancelled, err := store.CancelTask(ctx, created.ID, "no longer needed")
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_CANCELLED, cancelled.Status)
	assert.Equal(t, "no longer needed", cancelled.CloseReason)
	assert.NotNil(t, cancelled.ClosedAt)
	assert.Empty(t, cancelled.AssigneeAgentID, "cancel releases the claim")
	assert.Empty(t, cancelled.ClaimedBySession)
	assert.Nil(t, cancelled.ClaimedAt, "cancel clears claimed_at, as the manager's cancel always did")

	again, err := store.CancelTask(ctx, created.ID, "second")
	require.ErrorIs(t, err, task.ErrTaskAlreadyTerminal)
	require.NotNil(t, again)
	assert.Equal(t, "no longer needed", again.CloseReason, "the second cancel must not overwrite the first")

	// The case that mattered: a DONE row must not become CANCELLED.
	done, err := store.CreateTask(ctx, &task.Task{
		Title: "done", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)
	_, err = store.CloseTask(ctx, done.ID, "finished")
	require.NoError(t, err)
	got, err := store.CancelTask(ctx, done.ID, "late cancel")
	require.ErrorIs(t, err, task.ErrTaskAlreadyTerminal)
	require.NotNil(t, got)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, got.Status, "a cancel must never downgrade DONE")
	assert.Equal(t, "finished", got.CloseReason)
}

// TestManagerCancelTask_AfterCloseKeepsDone is the manager-level shape of the
// race AbortForTurn can lose: the turn's abort cancels a task an agent close
// already settled. The result is the settled row, no error, and DONE stays.
func TestManagerCancelTask_AfterCloseKeepsDone(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
	ctx := context.Background()

	created, err := mgr.CreateTask(ctx, &task.Task{
		Title: "raced", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)
	_, err = mgr.CloseTask(ctx, created.ID, "done first")
	require.NoError(t, err)

	got, err := mgr.CancelTask(ctx, created.ID, "abort after the fact")
	require.NoError(t, err, "a lost cancel race is benign")
	require.NotNil(t, got)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, got.Status)
	assert.Equal(t, "done first", got.CloseReason)

	history, err := store.GetHistory(ctx, created.ID)
	require.NoError(t, err)
	for _, h := range history {
		assert.NotEqual(t, "cancelled", h.Action, "a no-op cancel must record no history")
	}
}
