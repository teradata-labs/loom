// Copyright (c) 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build fts5

package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestParseSessionMode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected loomv1.ScheduledSessionMode
	}{
		{
			name:     "new",
			input:    "new",
			expected: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_NEW,
		},
		{
			name:     "resume",
			input:    "resume",
			expected: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_RESUME,
		},
		{
			name:     "empty string",
			input:    "",
			expected: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_UNSPECIFIED,
		},
		{
			name:     "uppercase NEW",
			input:    "NEW",
			expected: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_NEW,
		},
		{
			name:     "invalid",
			input:    "invalid",
			expected: loomv1.ScheduledSessionMode_SCHEDULED_SESSION_MODE_UNSPECIFIED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseSessionMode(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestStore_LastWorkflowID_Roundtrip(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create a schedule
	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-1",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1", PromptTemplate: "do stuff"},
					},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:     "0 * * * *",
			Timezone: "UTC",
			Enabled:  true,
		},
		Stats:     &loomv1.ScheduleStats{},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Update last workflow ID
	err = store.UpdateLastWorkflowID(ctx, schedule.Id, "wf-abc-123")
	require.NoError(t, err)

	// Get schedule and verify LastWorkflowId
	retrieved, err := store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, "wf-abc-123", retrieved.LastWorkflowId)
}

func TestStore_Migration_V1(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "migration-test.db")
	logger := zaptest.NewLogger(t)

	// Creating a store triggers migration
	store, err := NewStore(ctx, dbPath, logger)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Verify last_workflow_id column exists on scheduled_workflows
	// by running a query that references it
	var dummy sql.NullString
	err = store.db.QueryRowContext(ctx,
		"SELECT last_workflow_id FROM scheduled_workflows LIMIT 1",
	).Scan(&dummy)
	// sql.ErrNoRows is expected (no rows yet), but no column error means
	// the column exists
	if err != nil {
		assert.ErrorIs(t, err, sql.ErrNoRows,
			"expected sql.ErrNoRows (column exists but table is empty), got: %v", err)
	}

	// Verify workflow_id column exists on schedule_executions
	err = store.db.QueryRowContext(ctx,
		"SELECT workflow_id FROM schedule_executions LIMIT 1",
	).Scan(&dummy)
	if err != nil {
		assert.ErrorIs(t, err, sql.ErrNoRows,
			"expected sql.ErrNoRows (column exists but table is empty), got: %v", err)
	}

	// Verify schema version was bumped to at least 1
	var version int
	err = store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, version, 1, "schema version should be >= 1 after migration")
}

func TestStore_RecordExecution_WithWorkflowID(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	// Create a schedule
	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-1",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1", PromptTemplate: "do stuff"},
					},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:     "0 * * * *",
			Timezone: "UTC",
			Enabled:  true,
		},
		Stats:     &loomv1.ScheduleStats{},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	err := store.Create(ctx, schedule)
	require.NoError(t, err)

	// Record an execution with a workflow_id set
	exec := &loomv1.ScheduleExecution{
		ExecutionId: fmt.Sprintf("exec-%d", time.Now().UnixNano()),
		StartedAt:   time.Now().Unix(),
		CompletedAt: time.Now().Unix() + 30,
		Status:      "success",
		DurationMs:  30000,
		WorkflowId:  "wf-session-456",
	}

	err = store.RecordExecution(ctx, exec, schedule.Id)
	require.NoError(t, err)

	// Retrieve history and verify workflow_id is present
	history, err := store.GetExecutionHistory(ctx, schedule.Id, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, "wf-session-456", history[0].WorkflowId)
	assert.Equal(t, exec.ExecutionId, history[0].ExecutionId)
	assert.Equal(t, exec.Status, history[0].Status)
}
