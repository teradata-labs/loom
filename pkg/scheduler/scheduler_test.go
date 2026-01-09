// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
)

// setupTestScheduler creates a test scheduler with in-memory database
func setupTestScheduler(t *testing.T) *Scheduler {
	ctx := context.Background()
	logger := zap.NewNop()

	// Create registry
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		Logger: logger,
	})
	require.NoError(t, err)

	// Create a minimal orchestrator (won't actually execute workflows in these tests)
	orchestrator := orchestration.NewOrchestrator(orchestration.Config{
		Registry:    registry,
		Tracer:      observability.NewNoOpTracer(),
		Logger:      logger,
		LLMProvider: nil, // Not needed for these tests
	})

	// Use a unique temporary database file for each test to avoid conflicts in parallel test execution
	dbPath := filepath.Join(t.TempDir(), "scheduler.db")

	// Create scheduler using NewScheduler constructor
	scheduler, err := NewScheduler(ctx, Config{
		WorkflowDir:  "",
		DBPath:       dbPath,
		Orchestrator: orchestrator,
		Registry:     registry,
		Tracer:       observability.NewNoOpTracer(),
		Logger:       logger,
		HotReload:    false,
	})
	require.NoError(t, err)

	return scheduler
}

func TestScheduler_AddSchedule(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-1",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:     "0 */6 * * *", // Every 6 hours (seconds minutes hours days months weekday)
			Timezone: "UTC",
			Enabled:  true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Verify schedule was added to in-memory map
	scheduler.mu.RLock()
	_, exists := scheduler.schedules[schedule.Id]
	scheduler.mu.RUnlock()
	assert.True(t, exists)

	// Verify schedule was persisted to database
	retrieved, err := scheduler.store.Get(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, schedule.Id, retrieved.Id)
	assert.Equal(t, schedule.WorkflowName, retrieved.WorkflowName)

	// Verify timestamps were set
	assert.Greater(t, retrieved.CreatedAt, int64(0))
	assert.Greater(t, retrieved.UpdatedAt, int64(0))
	assert.Greater(t, retrieved.NextExecutionAt, int64(0))
}

func TestScheduler_AddSchedule_InvalidCron(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-invalid",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "invalid cron expression",
			Enabled: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cron expression")
}

func TestScheduler_GetSchedule(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-get",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Get schedule
	retrieved, err := scheduler.GetSchedule(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, schedule.Id, retrieved.Id)
	assert.Equal(t, schedule.WorkflowName, retrieved.WorkflowName)
}

func TestScheduler_GetSchedule_NotFound(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	_, err := scheduler.GetSchedule(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestScheduler_ListSchedules(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	// Add multiple schedules
	for i := 0; i < 3; i++ {
		schedule := &loomv1.ScheduledWorkflow{
			Id:           fmt.Sprintf("test-schedule-%d", i),
			WorkflowName: fmt.Sprintf("workflow-%d", i),
			Pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Pipeline{
					Pipeline: &loomv1.PipelinePattern{},
				},
			},
			Schedule: &loomv1.ScheduleConfig{
				Cron:    "0 0 * * *",
				Enabled: true,
			},
		}
		err := scheduler.AddSchedule(ctx, schedule)
		require.NoError(t, err)
	}

	// List schedules
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Len(t, schedules, 3)
}

func TestScheduler_RemoveSchedule(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-remove",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Remove schedule
	err = scheduler.RemoveSchedule(ctx, schedule.Id)
	require.NoError(t, err)

	// Verify schedule was removed from in-memory map
	scheduler.mu.RLock()
	_, exists := scheduler.schedules[schedule.Id]
	scheduler.mu.RUnlock()
	assert.False(t, exists)

	// Verify schedule was removed from database
	_, err = scheduler.store.Get(ctx, schedule.Id)
	assert.Error(t, err)
}

func TestScheduler_UpdateSchedule(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-update",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Update schedule with new cron expression
	schedule.Schedule.Cron = "0 */12 * * *" // Every 12 hours (seconds minutes hours days months weekday)
	err = scheduler.UpdateSchedule(ctx, schedule)
	require.NoError(t, err)

	// Verify schedule was updated
	updated, err := scheduler.GetSchedule(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Equal(t, "0 */12 * * *", updated.Schedule.Cron)
}

func TestScheduler_PauseSchedule(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-pause",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Pause schedule
	err = scheduler.PauseSchedule(ctx, schedule.Id)
	require.NoError(t, err)

	// Verify schedule is disabled
	paused, err := scheduler.GetSchedule(ctx, schedule.Id)
	require.NoError(t, err)
	assert.False(t, paused.Schedule.Enabled)
}

func TestScheduler_ResumeSchedule(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-resume",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: false, // Start paused
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Resume schedule
	err = scheduler.ResumeSchedule(ctx, schedule.Id)
	require.NoError(t, err)

	// Verify schedule is enabled
	resumed, err := scheduler.GetSchedule(ctx, schedule.Id)
	require.NoError(t, err)
	assert.True(t, resumed.Schedule.Enabled)
}

func TestScheduler_SkipIfRunning(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-skip",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1"},
					},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:          "0 0 * * *",
			Enabled:       true,
			SkipIfRunning: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Mark workflow as running by adding to runningWorkflows map
	scheduler.mu.Lock()
	scheduler.runningWorkflows[schedule.Id] = "execution-1"
	scheduler.mu.Unlock()

	// Trigger execution (should be skipped because SkipIfRunning=true and workflow is marked as running)
	_, err = scheduler.TriggerNow(ctx, schedule.Id, true, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "previous execution still running")

	// Clean up
	scheduler.mu.Lock()
	delete(scheduler.runningWorkflows, schedule.Id)
	scheduler.mu.Unlock()
}

func TestScheduler_TriggerNow(t *testing.T) {
	t.Skip("This test requires actual agent execution which is not set up in the test environment")
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-trigger",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1", PromptTemplate: "test"},
					},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Trigger execution
	executionID, err := scheduler.TriggerNow(ctx, schedule.Id, false, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, executionID)

	// Verify execution was recorded
	retrieved, err := scheduler.GetSchedule(ctx, schedule.Id)
	require.NoError(t, err)
	assert.Greater(t, retrieved.LastExecutionAt, int64(0))
}

func TestScheduler_GetHistory(t *testing.T) {
	t.Skip("This test requires actual agent execution which is not set up in the test environment")
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-history",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1", PromptTemplate: "test"},
					},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Trigger multiple executions
	for i := 0; i < 3; i++ {
		_, err := scheduler.TriggerNow(ctx, schedule.Id, false, nil)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond) // Small delay between executions
	}

	// Get history
	history, err := scheduler.GetHistory(ctx, schedule.Id, 10)
	require.NoError(t, err)
	assert.Len(t, history, 3)

	// Verify history is ordered by time (most recent first)
	for i := 0; i < len(history)-1; i++ {
		assert.GreaterOrEqual(t, history[i].StartedAt, history[i+1].StartedAt)
	}
}

func TestScheduler_ConcurrentOperations(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	var wg sync.WaitGroup
	numGoroutines := 10

	// Concurrent AddSchedule
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			schedule := &loomv1.ScheduledWorkflow{
				Id:           fmt.Sprintf("concurrent-schedule-%d", id),
				WorkflowName: fmt.Sprintf("workflow-%d", id),
				Pattern: &loomv1.WorkflowPattern{
					Pattern: &loomv1.WorkflowPattern_Pipeline{
						Pipeline: &loomv1.PipelinePattern{},
					},
				},
				Schedule: &loomv1.ScheduleConfig{
					Cron:    "0 0 * * *",
					Enabled: true,
				},
			}

			err := scheduler.AddSchedule(ctx, schedule)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Verify all schedules were added
	schedules, err := scheduler.ListSchedules(ctx)
	require.NoError(t, err)
	assert.Len(t, schedules, numGoroutines)

	// Concurrent GetSchedule
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			scheduleID := fmt.Sprintf("concurrent-schedule-%d", id)
			_, err := scheduler.GetSchedule(ctx, scheduleID)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()
}

func TestScheduler_NextExecutionCalculation(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	testCases := []struct {
		name     string
		cron     string
		timezone string
	}{
		{
			name:     "every hour UTC",
			cron:     "0 * * * *", // Every hour (seconds minutes hours days months weekday)
			timezone: "UTC",
		},
		{
			name:     "every 6 hours America/New_York",
			cron:     "0 */6 * * *", // Every 6 hours
			timezone: "America/New_York",
		},
		{
			name:     "daily at midnight Europe/London",
			cron:     "0 0 * * *", // Daily at midnight
			timezone: "Europe/London",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			schedule := &loomv1.ScheduledWorkflow{
				Id:           fmt.Sprintf("test-schedule-%s", tc.name),
				WorkflowName: "test-workflow",
				Pattern: &loomv1.WorkflowPattern{
					Pattern: &loomv1.WorkflowPattern_Pipeline{
						Pipeline: &loomv1.PipelinePattern{},
					},
				},
				Schedule: &loomv1.ScheduleConfig{
					Cron:     tc.cron,
					Timezone: tc.timezone,
					Enabled:  true,
				},
			}

			err := scheduler.AddSchedule(ctx, schedule)
			require.NoError(t, err)

			// Verify next execution time was calculated
			retrieved, err := scheduler.GetSchedule(ctx, schedule.Id)
			require.NoError(t, err)
			assert.Greater(t, retrieved.NextExecutionAt, time.Now().Unix())
		})
	}
}

func TestScheduler_VariableInterpolation(t *testing.T) {
	ctx := context.Background()
	scheduler := setupTestScheduler(t)

	schedule := &loomv1.ScheduledWorkflow{
		Id:           "test-schedule-variables",
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 0 * * *",
			Enabled: true,
			Variables: map[string]string{
				"env":    "production",
				"region": "us-east-1",
			},
		},
	}

	err := scheduler.AddSchedule(ctx, schedule)
	require.NoError(t, err)

	// Trigger with additional variables
	additionalVars := map[string]string{
		"customer_id": "12345",
	}

	_, err = scheduler.TriggerNow(ctx, schedule.Id, false, additionalVars)
	require.NoError(t, err)

	// Note: Variable interpolation happens in orchestrator.ExecutePattern
	// We just verify the execution succeeds
}
