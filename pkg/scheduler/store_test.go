// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestStore_CreateAndGet(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-1",
		WorkflowName: "test-workflow",
		YamlPath:     "/path/to/workflow.yaml",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test prompt",
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:     "0 */6 * * *",
			Timezone: "UTC",
			Enabled:  true,
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	// Create schedule
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Get schedule
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, schedule.Id, retrieved.Id)
	assert.Equal(t, schedule.WorkflowName, retrieved.WorkflowName)
	assert.Equal(t, schedule.YamlPath, retrieved.YamlPath)
	assert.Equal(t, schedule.Schedule.Cron, retrieved.Schedule.Cron)
	assert.Equal(t, schedule.Schedule.Timezone, retrieved.Schedule.Timezone)
	assert.Equal(t, schedule.Schedule.Enabled, retrieved.Schedule.Enabled)
}

func TestStore_GetNotFound(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	_, err := store.Get(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_Update(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create initial schedule
	schedule := createTestSchedule("update-test-1", "original-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Update schedule
	schedule.WorkflowName = "updated-workflow"
	schedule.Schedule.Cron = "0 12 * * *"
	schedule.NextExecutionAt = time.Now().Add(1 * time.Hour).Unix()

	err = store.Update(ctx, schedule)
	require.NoError(t, err)

	// Verify update
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, "updated-workflow", retrieved.WorkflowName)
	assert.Equal(t, "0 12 * * *", retrieved.Schedule.Cron)
	assert.Equal(t, schedule.NextExecutionAt, retrieved.NextExecutionAt)
}

func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("delete-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Delete schedule
	err = store.Delete(ctx, schedule.Id)
	require.NoError(t, err)

	// Verify deletion
	_, err = store.Get(ctx, schedule.Id)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_List(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create multiple schedules
	schedules := []*loomv1.ScheduledWorkflow{
		createTestSchedule("list-test-1", "workflow-1"),
		createTestSchedule("list-test-2", "workflow-2"),
		createTestSchedule("list-test-3", "workflow-3"),
	}

	for _, s := range schedules {
		err := store.Create(ctx, s)
		require.NoError(t, err)
	}

	// List all schedules
	retrieved, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, retrieved, 3)

	// Verify IDs are present
	ids := make(map[string]bool)
	for _, s := range retrieved {
		ids[s.Id] = true
	}
	assert.True(t, ids["list-test-1"])
	assert.True(t, ids["list-test-2"])
	assert.True(t, ids["list-test-3"])
}

func TestStore_RecordSuccess(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("success-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Record success
	err = store.RecordSuccess(ctx, schedule.Id)
	require.NoError(t, err)

	// Verify stats
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(1), retrieved.Stats.TotalExecutions)
	assert.Equal(t, int32(1), retrieved.Stats.SuccessfulExecutions)
	assert.Equal(t, "success", retrieved.Stats.LastStatus)
}

func TestStore_RecordFailure(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("failure-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Record failure
	errorMsg := "workflow execution failed"
	err = store.RecordFailure(ctx, schedule.Id, errorMsg)
	require.NoError(t, err)

	// Verify stats
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(1), retrieved.Stats.TotalExecutions)
	assert.Equal(t, int32(1), retrieved.Stats.FailedExecutions)
	assert.Equal(t, "failed", retrieved.Stats.LastStatus)
	assert.Equal(t, errorMsg, retrieved.Stats.LastError)
}

func TestStore_IncrementSkipped(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("skipped-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Increment skipped
	err = store.IncrementSkipped(ctx, schedule.Id)
	require.NoError(t, err)

	// Verify stats
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(1), retrieved.Stats.SkippedExecutions)
	assert.Equal(t, "skipped", retrieved.Stats.LastStatus)
}

func TestStore_UpdateCurrentExecution(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("current-exec-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Set current execution
	executionID := "exec-12345"
	err = store.UpdateCurrentExecution(ctx, schedule.Id, executionID)
	require.NoError(t, err)

	// Verify
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, executionID, retrieved.CurrentExecutionId)

	// Clear current execution
	err = store.UpdateCurrentExecution(ctx, schedule.Id, "")
	require.NoError(t, err)

	retrieved, err = store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Empty(t, retrieved.CurrentExecutionId)
}

func TestStore_UpdateNextExecution(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("next-exec-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Update next execution time
	nextExec := time.Now().Add(2 * time.Hour).Unix()
	err = store.UpdateNextExecution(ctx, schedule.Id, nextExec)
	require.NoError(t, err)

	// Verify
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, nextExec, retrieved.NextExecutionAt)
}

func TestStore_RecordExecution(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("exec-history-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Record execution
	exec := &loomv1.ScheduleExecution{
		ExecutionId: "exec-123",
		StartedAt:   time.Now().Unix(),
		CompletedAt: time.Now().Unix() + 60,
		Status:      "success",
		DurationMs:  60000,
	}

	err = store.RecordExecution(ctx, exec, schedule.Id)
	require.NoError(t, err)

	// Retrieve history
	history, err := store.GetExecutionHistory(ctx, schedule.Id, 10)
	require.NoError(t, err)
	assert.Len(t, history, 1)
	assert.Equal(t, exec.ExecutionId, history[0].ExecutionId)
	assert.Equal(t, exec.Status, history[0].Status)
	assert.Equal(t, exec.DurationMs, history[0].DurationMs)
}

func TestStore_GetExecutionHistory_Limit(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("history-limit-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Record multiple executions
	for i := 0; i < 10; i++ {
		exec := &loomv1.ScheduleExecution{
			ExecutionId: string(rune('a' + i)),
			StartedAt:   time.Now().Unix() + int64(i),
			CompletedAt: time.Now().Unix() + int64(i) + 60,
			Status:      "success",
			DurationMs:  60000,
		}
		err = store.RecordExecution(ctx, exec, schedule.Id)
		require.NoError(t, err)
	}

	// Get history with limit
	history, err := store.GetExecutionHistory(ctx, schedule.Id, 5)
	require.NoError(t, err)
	assert.Len(t, history, 5)
}

func TestStore_GetDueSchedules(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	now := time.Now()

	// Create schedules with different next execution times
	pastSchedule := createTestSchedule("due-past", "workflow-past")
	pastSchedule.NextExecutionAt = now.Add(-1 * time.Hour).Unix()
	pastSchedule.Schedule.Enabled = true

	nowSchedule := createTestSchedule("due-now", "workflow-now")
	nowSchedule.NextExecutionAt = now.Unix()
	nowSchedule.Schedule.Enabled = true

	futureSchedule := createTestSchedule("due-future", "workflow-future")
	futureSchedule.NextExecutionAt = now.Add(1 * time.Hour).Unix()
	futureSchedule.Schedule.Enabled = true

	disabledSchedule := createTestSchedule("due-disabled", "workflow-disabled")
	disabledSchedule.NextExecutionAt = now.Add(-1 * time.Hour).Unix()
	disabledSchedule.Schedule.Enabled = false

	err := store.Create(ctx, pastSchedule)
	require.NoError(t, err)
	err = store.Create(ctx, nowSchedule)
	require.NoError(t, err)
	err = store.Create(ctx, futureSchedule)
	require.NoError(t, err)
	err = store.Create(ctx, disabledSchedule)
	require.NoError(t, err)

	// Get due schedules
	due, err := store.GetDueSchedules(ctx, now.Unix())
	require.NoError(t, err)

	// Should only return past and now schedules (both enabled)
	assert.Len(t, due, 2)

	ids := make(map[string]bool)
	for _, s := range due {
		ids[s.Id] = true
	}
	assert.True(t, ids["due-past"])
	assert.True(t, ids["due-now"])
	assert.False(t, ids["due-future"])
	assert.False(t, ids["due-disabled"])
}

func TestStore_Persistence(t *testing.T) {
	ctx := context.Background()

	// Create temp DB path
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	logger := zaptest.NewLogger(t)

	// Create store and add schedule
	store1, err := NewStore(ctx, dbPath, logger)
	require.NoError(t, err)

	schedule := createTestSchedule("persist-test-1", "test-workflow")
	err = store1.Create(ctx, schedule)
	require.NoError(t, err)

	// Close store
	err = store1.Close()
	require.NoError(t, err)

	// Reopen store
	store2, err := NewStore(ctx, dbPath, logger)
	require.NoError(t, err)
	defer func() { _ = store2.Close() }()

	// Verify schedule persisted
	retrieved, err := store2.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, schedule.Id, retrieved.Id)
	assert.Equal(t, schedule.WorkflowName, retrieved.WorkflowName)
}

func TestStore_ConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrent access test in short mode")
	}

	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create initial schedules
	for i := 0; i < 10; i++ {
		schedule := createTestSchedule(string(rune('a'+i)), "workflow")
		err := store.Create(ctx, schedule)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent reads
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			scheduleID := string(rune('a' + (id % 10)))
			_, err := store.Get(ctx, scheduleID)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	// Concurrent updates
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			scheduleID := string(rune('a' + (id % 10)))
			err := store.RecordSuccess(ctx, scheduleID)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	// Concurrent lists
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.List(ctx)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}
}

func TestStore_MultipleStats(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create schedule
	schedule := createTestSchedule("stats-test-1", "test-workflow")
	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Record multiple successes and failures
	for i := 0; i < 5; i++ {
		err = store.RecordSuccess(ctx, schedule.Id)
		require.NoError(t, err)
	}

	for i := 0; i < 3; i++ {
		err = store.RecordFailure(ctx, schedule.Id, "error")
		require.NoError(t, err)
	}

	for i := 0; i < 2; i++ {
		err = store.IncrementSkipped(ctx, schedule.Id)
		require.NoError(t, err)
	}

	// Verify stats
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, int32(8), retrieved.Stats.TotalExecutions) // 5 success + 3 failure
	assert.Equal(t, int32(5), retrieved.Stats.SuccessfulExecutions)
	assert.Equal(t, int32(3), retrieved.Stats.FailedExecutions)
	assert.Equal(t, int32(2), retrieved.Stats.SkippedExecutions)
	assert.Equal(t, "skipped", retrieved.Stats.LastStatus) // Last operation was skip
}

// Helper functions

func setupTestStore(t *testing.T) *Store {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	logger := zaptest.NewLogger(t)

	store, err := NewStore(context.Background(), dbPath, logger)
	require.NoError(t, err)

	return store
}

func createTestSchedule(id, workflowName string) *loomv1.ScheduledWorkflow {
	return &loomv1.ScheduledWorkflow{
		Id:           id,
		WorkflowName: workflowName,
		YamlPath:     "/path/to/workflow.yaml",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test prompt",
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:                "0 */6 * * *",
			Timezone:            "UTC",
			Enabled:             true,
			SkipIfRunning:       true,
			MaxExecutionSeconds: 3600,
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Stats:     &loomv1.ScheduleStats{},
	}
}
