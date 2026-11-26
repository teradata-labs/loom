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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// These run in the ORDINARY gate (no build tag), like the human-request store
// tests: CI provides a postgres service and sets TEST_POSTGRES_URL; a local run
// without one skips.

// testTaskStore connects, migrates, and returns a store plus a user-scoped
// context. Rows are cleaned up by user_id so parallel runs cannot see each
// other's fixtures.
func testTaskStore(t *testing.T) (*TaskStore, context.Context) {
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

	store := NewTaskStore(pool, observability.NewNoOpTracer())
	userID := hrUniqueID("task-user")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM task_history WHERE task_id IN (SELECT id FROM tasks WHERE user_id = $1)", userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM tasks WHERE user_id = $1", userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM task_boards WHERE user_id = $1", userID)
	})
	return store, ContextWithUserID(ctx, userID)
}

// TestCloseTask_DoubleCloseReturnsTheSettledRowWithTheSentinel_Postgres pins
// the Postgres half of the round-5 close guard. The store assigned the settled
// row inside the transaction closure and then discarded it on the error
// return, so Manager.CloseTask's sentinel branch returned (nil, nil) and the
// task_board close tool dereferenced nil. SQLite already returned
// (existing, sentinel); both stores now do.
func TestCloseTask_DoubleCloseReturnsTheSettledRowWithTheSentinel_Postgres(t *testing.T) {
	store, ctx := testTaskStore(t)

	created, err := store.CreateTask(ctx, &task.Task{
		Title: "close me", Status: loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)

	first, err := store.CloseTask(ctx, created.ID, "first reason")
	require.NoError(t, err)
	require.Equal(t, loomv1.TaskStatus_TASK_STATUS_DONE, first.Status)

	second, err := store.CloseTask(ctx, created.ID, "second reason")
	require.ErrorIs(t, err, task.ErrTaskAlreadyTerminal)
	require.NotNil(t, second, "the sentinel must travel WITH the settled row; nil here was the panic")
	assert.Equal(t, created.ID, second.ID)
	assert.Equal(t, "first reason", second.CloseReason, "the losing close must not overwrite the winner's reason")

	// End to end through the manager: the path the tool takes.
	mgr := task.NewManager(store, nil, observability.NewNoOpTracer(), nil)
	again, err := mgr.CloseTask(ctx, created.ID, "third")
	require.NoError(t, err)
	require.NotNil(t, again, "Manager.CloseTask must never return (nil, nil)")
	assert.Equal(t, created.ID, again.ID)
}

// TestListTasks_PagesAreDisjointAndCompleteOverEqualKeys_Postgres pins the
// round-5 ORDER BY tiebreak on the backend where it matters: Postgres gives no
// ordering guarantee for rows with equal sort keys, and different plans for
// adjacent OFFSET pages return overlapping windows. Every row must appear
// exactly once when paging the board in both directions.
func TestListTasks_PagesAreDisjointAndCompleteOverEqualKeys_Postgres(t *testing.T) {
	store, ctx := testTaskStore(t)

	boardID := hrUniqueID("paged-board")
	_, err := store.CreateBoard(ctx, &task.TaskBoard{ID: boardID, Name: "paged"})
	require.NoError(t, err)

	const n = 230
	for i := 0; i < n; i++ {
		_, err := store.CreateTask(ctx, &task.Task{
			Title: fmt.Sprintf("t%d", i), BoardID: boardID,
			Status: loomv1.TaskStatus_TASK_STATUS_OPEN, CreatedVia: taskctx.CreatedViaAgent})
		require.NoError(t, err)
	}

	for _, newest := range []bool{false, true} {
		seen := map[string]int{}
		for offset := 0; ; offset += 37 {
			page, _, err := store.ListTasks(ctx, task.ListTasksOpts{
				BoardID: boardID, Limit: 37, Offset: offset, NewestFirst: newest})
			require.NoError(t, err)
			for _, tk := range page {
				seen[tk.ID]++
			}
			if len(page) < 37 {
				break
			}
		}
		assert.Len(t, seen, n, "NewestFirst=%v: every row appears", newest)
		for id, c := range seen {
			if c != 1 {
				t.Fatalf("NewestFirst=%v: task %s appeared %d times across pages", newest, id, c)
			}
		}
	}
}

// TestCancelTask_IsGuardedLikeClose_Postgres is the Postgres twin: the
// TaskCanceller capability decides a cancel-vs-close race at the row, and a
// DONE row never becomes CANCELLED.
func TestCancelTask_IsGuardedLikeClose_Postgres(t *testing.T) {
	store, ctx := testTaskStore(t)

	var _ task.TaskCanceller = store

	created, err := store.CreateTask(ctx, &task.Task{
		Title: "cancel me", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		CreatedVia: taskctx.CreatedViaAgent,
	})
	require.NoError(t, err)
	_, err = store.ClaimTask(ctx, created.ID, "agent-1", "sess-1")
	require.NoError(t, err)

	cancelled, err := store.CancelTask(ctx, created.ID, "no longer needed")
	require.NoError(t, err)
	assert.Equal(t, loomv1.TaskStatus_TASK_STATUS_CANCELLED, cancelled.Status)
	assert.Empty(t, cancelled.AssigneeAgentID)
	assert.Empty(t, cancelled.ClaimedBySession)
	assert.Nil(t, cancelled.ClaimedAt)

	again, err := store.CancelTask(ctx, created.ID, "second")
	require.ErrorIs(t, err, task.ErrTaskAlreadyTerminal)
	require.NotNil(t, again, "the sentinel must travel with the settled row on Postgres too")
	assert.Equal(t, "no longer needed", again.CloseReason)

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
}
