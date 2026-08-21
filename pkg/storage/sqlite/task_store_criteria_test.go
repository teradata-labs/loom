// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
)

// TestTask_SetAcceptanceCriteria_Guard: the write-once guard lives in the
// UPDATE statement itself — set-while-empty and identical re-sends succeed,
// different values are refused with task.ErrAcceptanceCriteriaLocked.
func TestTask_SetAcceptanceCriteria_Guard(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	created, err := store.CreateTask(ctx, &task.Task{
		Title:  "guarded",
		Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
	})
	require.NoError(t, err)

	// Set while empty.
	got, err := store.SetAcceptanceCriteria(ctx, created.ID, "output has columns a,b,c")
	require.NoError(t, err)
	assert.Equal(t, "output has columns a,b,c", got.AcceptanceCriteria)

	// Identical re-send is an idempotent success.
	got, err = store.SetAcceptanceCriteria(ctx, created.ID, "output has columns a,b,c")
	require.NoError(t, err)
	assert.Equal(t, "output has columns a,b,c", got.AcceptanceCriteria)

	// A different value is refused with the typed sentinel.
	_, err = store.SetAcceptanceCriteria(ctx, created.ID, "different goalposts")
	require.Error(t, err)
	assert.True(t, errors.Is(err, task.ErrAcceptanceCriteriaLocked),
		"locked violation must wrap task.ErrAcceptanceCriteriaLocked, got: %v", err)

	// Stored value is untouched.
	after, err := store.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "output has columns a,b,c", after.AcceptanceCriteria)

	// Unknown task is a not-found error, not a locked error.
	_, err = store.SetAcceptanceCriteria(ctx, "no-such-task", "whatever")
	require.Error(t, err)
	assert.False(t, errors.Is(err, task.ErrAcceptanceCriteriaLocked))
	assert.Contains(t, err.Error(), "not found")

	// Empty criteria are rejected outright.
	_, err = store.SetAcceptanceCriteria(ctx, created.ID, "")
	require.Error(t, err)
}

// TestTask_SetAcceptanceCriteria_ConcurrentOneWins is the M1 TOCTOU
// regression: two concurrent different-value writers race the guard —
// exactly one wins, the loser gets the locked error, and the stored value is
// the winner's.
func TestTask_SetAcceptanceCriteria_ConcurrentOneWins(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	created, err := store.CreateTask(ctx, &task.Task{
		Title:  "contested",
		Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
	})
	require.NoError(t, err)

	type outcome struct {
		criteria string
		err      error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, criteria := range []string{"criteria-A", "criteria-B"} {
		go func(criteria string) {
			<-start
			_, setErr := store.SetAcceptanceCriteria(ctx, created.ID, criteria)
			results <- outcome{criteria: criteria, err: setErr}
		}(criteria)
	}
	close(start)

	var winner string
	losers := 0
	for i := 0; i < 2; i++ {
		o := <-results
		if o.err == nil {
			require.Empty(t, winner, "both concurrent writers won — guard is not atomic")
			winner = o.criteria
			continue
		}
		losers++
		assert.True(t, errors.Is(o.err, task.ErrAcceptanceCriteriaLocked),
			"loser must get the locked error, got: %v", o.err)
	}
	require.NotEmpty(t, winner, "one writer must win")
	require.Equal(t, 1, losers)

	after, err := store.GetTask(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, winner, after.AcceptanceCriteria)
}

// TestTask_ListTasks_SessionFilter: SessionID scopes to the session's
// working set — claimed by it or created in it (metadata attribution) — with
// the metadata match anchored and LIKE-metacharacter safe.
func TestTask_ListTasks_SessionFilter(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	createdIn, err := store.CreateTask(ctx, &task.Task{
		Title: "created in session", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		Metadata: map[string]string{task.CreatedBySessionMetadataKey: "sess-1"},
	})
	require.NoError(t, err)

	claimedBy, err := store.CreateTask(ctx, &task.Task{
		Title: "claimed by session", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
	})
	require.NoError(t, err)
	_, err = store.ClaimTask(ctx, claimedBy.ID, "agent-a", "sess-1")
	require.NoError(t, err)

	_, err = store.CreateTask(ctx, &task.Task{
		Title: "other session", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		Metadata: map[string]string{task.CreatedBySessionMetadataKey: "sess-2"},
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, &task.Task{
		Title: "no session", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
	})
	require.NoError(t, err)
	// A metadata key merely ending in the attribution key must not match
	// (the leading quote anchors the key in the LIKE pattern).
	_, err = store.CreateTask(ctx, &task.Task{
		Title: "suffix key", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		Metadata: map[string]string{"not_" + task.CreatedBySessionMetadataKey: "sess-1"},
	})
	require.NoError(t, err)

	tasks, total, err := store.ListTasks(ctx, task.ListTasksOpts{SessionID: "sess-1", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	ids := make([]string, 0, len(tasks))
	for _, tk := range tasks {
		ids = append(ids, tk.ID)
	}
	assert.ElementsMatch(t, []string{createdIn.ID, claimedBy.ID}, ids)

	// LIKE metacharacters in the session id must match literally, never as
	// wildcards: "sess%1" must not sweep in "sess-1" tasks.
	wildcardTask, err := store.CreateTask(ctx, &task.Task{
		Title: "wildcard session", Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		Metadata: map[string]string{task.CreatedBySessionMetadataKey: "sess%1"},
	})
	require.NoError(t, err)
	tasks, total, err = store.ListTasks(ctx, task.ListTasksOpts{SessionID: "sess%1", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	assert.Equal(t, wildcardTask.ID, tasks[0].ID)
}

// TestTask_ListTasks_StatusesFilter: the Statuses inclusion list filters
// server-side and the returned total reflects the filter.
func TestTask_ListTasks_StatusesFilter(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	for _, st := range []loomv1.TaskStatus{
		loomv1.TaskStatus_TASK_STATUS_OPEN,
		loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		loomv1.TaskStatus_TASK_STATUS_DONE,
		loomv1.TaskStatus_TASK_STATUS_CANCELLED,
	} {
		_, err := store.CreateTask(ctx, &task.Task{Title: "t-" + st.String(), Status: st})
		require.NoError(t, err)
	}

	tasks, total, err := store.ListTasks(ctx, task.ListTasksOpts{
		Statuses: []loomv1.TaskStatus{
			loomv1.TaskStatus_TASK_STATUS_OPEN,
			loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		},
		Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	for _, tk := range tasks {
		assert.Contains(t, []loomv1.TaskStatus{
			loomv1.TaskStatus_TASK_STATUS_OPEN,
			loomv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
		}, tk.Status)
	}
}

// TestTask_ListTasks_NewestFirst: NewestFirst orders by created_at DESC so a
// windowed reader keeps the most recent tasks.
func TestTask_ListTasks_NewestFirst(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := 0; i < 3; i++ {
		created, err := store.CreateTask(ctx, &task.Task{
			Title:  "ordered",
			Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		})
		require.NoError(t, err)
		// created_at has second precision in SQLite; give each row a
		// distinct timestamp so the ordering assertion is deterministic.
		_, err = db.ExecContext(ctx, `UPDATE tasks SET created_at = datetime(?) WHERE id = ?`,
			base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339), created.ID)
		require.NoError(t, err)
		ids = append(ids, created.ID)
	}

	tasks, _, err := store.ListTasks(ctx, task.ListTasksOpts{NewestFirst: true, Limit: 2})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, ids[2], tasks[0].ID, "newest task must come first")
	assert.Equal(t, ids[1], tasks[1].ID)

	// Default ordering is unchanged (priority ASC, created_at ASC).
	tasks, _, err = store.ListTasks(ctx, task.ListTasksOpts{Limit: 3})
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	assert.Equal(t, ids[0], tasks[0].ID, "default order must remain oldest-first")
}
