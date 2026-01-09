// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/orchestration"
	"github.com/teradata-labs/loom/pkg/scheduler"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setupTestSchedulerServer creates a MultiAgentServer with a scheduler for testing
func setupTestSchedulerServer(t *testing.T) (*MultiAgentServer, *scheduler.Scheduler) {
	ctx := context.Background()
	logger := zap.NewNop()

	// Create registry
	registry, err := agent.NewRegistry(agent.RegistryConfig{
		Logger: logger,
	})
	require.NoError(t, err)

	// Create orchestrator
	orchestrator := orchestration.NewOrchestrator(orchestration.Config{
		Registry:    registry,
		Tracer:      observability.NewNoOpTracer(),
		Logger:      logger,
		LLMProvider: nil,
	})

	// Create scheduler with in-memory database
	// Each call to setupTestSchedulerServer gets its own isolated :memory: database
	sched, err := scheduler.NewScheduler(ctx, scheduler.Config{
		WorkflowDir:  "",
		DBPath:       ":memory:",
		Orchestrator: orchestrator,
		Registry:     registry,
		Tracer:       observability.NewNoOpTracer(),
		Logger:       logger,
		HotReload:    false,
	})
	require.NoError(t, err)

	// Create server
	server := &MultiAgentServer{
		agents:        make(map[string]*agent.Agent),
		workflowStore: NewWorkflowStore(),
	}
	server.ConfigureScheduler(sched)
	server.SetAgentRegistry(registry)

	return server, sched
}

// TestScheduleWorkflow_Validation tests validation for ScheduleWorkflow RPC
func TestScheduleWorkflow_Validation(t *testing.T) {
	tests := []struct {
		name           string
		req            *loomv1.ScheduleWorkflowRequest
		setupScheduler bool
		expectCode     codes.Code
		expectError    string
	}{
		{
			name: "missing workflow_name",
			req: &loomv1.ScheduleWorkflowRequest{
				WorkflowName: "",
				Pattern: &loomv1.WorkflowPattern{
					Pattern: &loomv1.WorkflowPattern_Pipeline{
						Pipeline: &loomv1.PipelinePattern{
							InitialPrompt: "test",
							Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
						},
					},
				},
				Schedule: &loomv1.ScheduleConfig{
					Cron: "0 */6 * * *",
				},
			},
			setupScheduler: true,
			expectCode:     codes.InvalidArgument,
			expectError:    "workflow_name is required",
		},
		{
			name: "missing pattern",
			req: &loomv1.ScheduleWorkflowRequest{
				WorkflowName: "test-workflow",
				Pattern:      nil,
				Schedule: &loomv1.ScheduleConfig{
					Cron: "0 */6 * * *",
				},
			},
			setupScheduler: true,
			expectCode:     codes.InvalidArgument,
			expectError:    "pattern is required",
		},
		{
			name: "missing schedule",
			req: &loomv1.ScheduleWorkflowRequest{
				WorkflowName: "test-workflow",
				Pattern: &loomv1.WorkflowPattern{
					Pattern: &loomv1.WorkflowPattern_Pipeline{
						Pipeline: &loomv1.PipelinePattern{
							InitialPrompt: "test",
							Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
						},
					},
				},
				Schedule: nil,
			},
			setupScheduler: true,
			expectCode:     codes.InvalidArgument,
			expectError:    "schedule is required",
		},
		{
			name: "missing cron expression",
			req: &loomv1.ScheduleWorkflowRequest{
				WorkflowName: "test-workflow",
				Pattern: &loomv1.WorkflowPattern{
					Pattern: &loomv1.WorkflowPattern_Pipeline{
						Pipeline: &loomv1.PipelinePattern{
							InitialPrompt: "test",
							Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
						},
					},
				},
				Schedule: &loomv1.ScheduleConfig{
					Cron: "",
				},
			},
			setupScheduler: true,
			expectCode:     codes.InvalidArgument,
			expectError:    "schedule.cron is required",
		},
		{
			name: "scheduler not configured",
			req: &loomv1.ScheduleWorkflowRequest{
				WorkflowName: "test-workflow",
				Pattern: &loomv1.WorkflowPattern{
					Pattern: &loomv1.WorkflowPattern_Pipeline{
						Pipeline: &loomv1.PipelinePattern{
							InitialPrompt: "test",
							Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
						},
					},
				},
				Schedule: &loomv1.ScheduleConfig{
					Cron: "0 */6 * * *",
				},
			},
			setupScheduler: false,
			expectCode:     codes.FailedPrecondition,
			expectError:    "scheduler not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var server *MultiAgentServer
			if tt.setupScheduler {
				server, _ = setupTestSchedulerServer(t)
			} else {
				// Server without scheduler
				server = &MultiAgentServer{
					agents:        make(map[string]*agent.Agent),
					workflowStore: NewWorkflowStore(),
				}
			}

			_, err := server.ScheduleWorkflow(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "Expected gRPC status error")
			assert.Equal(t, tt.expectCode, st.Code(), "Status code mismatch")
			assert.Equal(t, tt.expectError, st.Message(), "Error message mismatch")
		})
	}
}

// TestScheduleWorkflow_Success tests successful schedule creation
func TestScheduleWorkflow_Success(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	req := &loomv1.ScheduleWorkflowRequest{
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test prompt",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1", PromptTemplate: "stage 1"},
					},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:                "0 */6 * * *",
			Timezone:            "UTC",
			Enabled:             true,
			SkipIfRunning:       true,
			MaxExecutionSeconds: 3600,
			Variables: map[string]string{
				"key1": "value1",
			},
		},
	}

	resp, err := server.ScheduleWorkflow(context.Background(), req)

	require.NoError(t, err)
	assert.NotEmpty(t, resp.ScheduleId)
	assert.Contains(t, resp.ScheduleId, "rpc-test-workflow-")
	assert.Equal(t, "test-workflow", resp.Schedule.WorkflowName)
	assert.Equal(t, "", resp.Schedule.YamlPath) // RPC-created schedules have empty yaml_path
	assert.True(t, resp.Schedule.Schedule.Enabled)
}

// TestUpdateScheduledWorkflow_Validation tests validation for UpdateScheduledWorkflow RPC
func TestUpdateScheduledWorkflow_Validation(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	tests := []struct {
		name        string
		req         *loomv1.UpdateScheduledWorkflowRequest
		expectCode  codes.Code
		expectError string
	}{
		{
			name: "missing schedule_id",
			req: &loomv1.UpdateScheduledWorkflowRequest{
				ScheduleId: "",
				Schedule: &loomv1.ScheduleConfig{
					Cron: "0 8 * * *",
				},
			},
			expectCode:  codes.InvalidArgument,
			expectError: "schedule_id is required",
		},
		{
			name: "schedule not found",
			req: &loomv1.UpdateScheduledWorkflowRequest{
				ScheduleId: "nonexistent-schedule-123",
				Schedule: &loomv1.ScheduleConfig{
					Cron: "0 8 * * *",
				},
			},
			expectCode:  codes.NotFound,
			expectError: "schedule not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.UpdateScheduledWorkflow(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "Expected gRPC status error")
			assert.Equal(t, tt.expectCode, st.Code(), "Status code mismatch")
			assert.Contains(t, st.Message(), tt.expectError, "Error message mismatch")
		})
	}
}

// TestUpdateScheduledWorkflow_YAMLSourcedRestriction tests that YAML-sourced schedules cannot be updated
func TestUpdateScheduledWorkflow_YAMLSourcedRestriction(t *testing.T) {
	server, sched := setupTestSchedulerServer(t)

	// Create a schedule with yaml_path (simulating YAML-sourced)
	yamlSchedule := &loomv1.ScheduledWorkflow{
		Id:           "yaml-schedule-123",
		WorkflowName: "yaml-workflow",
		YamlPath:     "/path/to/workflow.yaml", // YAML-sourced
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 8 * * *",
			Enabled: true,
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	err := sched.AddSchedule(context.Background(), yamlSchedule)
	require.NoError(t, err)

	// Try to update it via RPC
	req := &loomv1.UpdateScheduledWorkflowRequest{
		ScheduleId: "yaml-schedule-123",
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 9 * * *", // Different cron
			Enabled: true,
		},
	}

	_, err = server.UpdateScheduledWorkflow(context.Background(), req)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "cannot update YAML-sourced schedules via RPC")
}

// TestUpdateScheduledWorkflow_Success tests successful update of RPC-created schedule
func TestUpdateScheduledWorkflow_Success(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	// First create a schedule via RPC
	createReq := &loomv1.ScheduleWorkflowRequest{
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 */6 * * *",
			Enabled: true,
		},
	}

	createResp, err := server.ScheduleWorkflow(context.Background(), createReq)
	require.NoError(t, err)

	// Now update it
	updateReq := &loomv1.UpdateScheduledWorkflowRequest{
		ScheduleId: createResp.ScheduleId,
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 8 * * *", // Change to daily at 8 AM
			Enabled: false,       // Disable it
		},
	}

	updateResp, err := server.UpdateScheduledWorkflow(context.Background(), updateReq)

	require.NoError(t, err)
	assert.Equal(t, createResp.ScheduleId, updateResp.ScheduleId)
	assert.Equal(t, "0 8 * * *", updateResp.Schedule.Schedule.Cron)
	assert.False(t, updateResp.Schedule.Schedule.Enabled)
}

// TestGetScheduledWorkflow_Validation tests validation for GetScheduledWorkflow RPC
func TestGetScheduledWorkflow_Validation(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	tests := []struct {
		name        string
		req         *loomv1.GetScheduledWorkflowRequest
		expectCode  codes.Code
		expectError string
	}{
		{
			name:        "missing schedule_id",
			req:         &loomv1.GetScheduledWorkflowRequest{ScheduleId: ""},
			expectCode:  codes.InvalidArgument,
			expectError: "schedule_id is required",
		},
		{
			name:        "schedule not found",
			req:         &loomv1.GetScheduledWorkflowRequest{ScheduleId: "nonexistent-123"},
			expectCode:  codes.NotFound,
			expectError: "schedule not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.GetScheduledWorkflow(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.expectCode, st.Code())
			assert.Contains(t, st.Message(), tt.expectError)
		})
	}
}

// TestGetScheduledWorkflow_Success tests successful retrieval
func TestGetScheduledWorkflow_Success(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	// Create a schedule
	createReq := &loomv1.ScheduleWorkflowRequest{
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 */6 * * *",
			Enabled: true,
		},
	}

	createResp, err := server.ScheduleWorkflow(context.Background(), createReq)
	require.NoError(t, err)

	// Retrieve it
	getReq := &loomv1.GetScheduledWorkflowRequest{
		ScheduleId: createResp.ScheduleId,
	}

	getResp, err := server.GetScheduledWorkflow(context.Background(), getReq)

	require.NoError(t, err)
	assert.Equal(t, createResp.ScheduleId, getResp.Id)
	assert.Equal(t, "test-workflow", getResp.WorkflowName)
}

// TestListScheduledWorkflows tests listing schedules
func TestListScheduledWorkflows(t *testing.T) {
	// Create fresh server for this test
	server, _ := setupTestSchedulerServer(t)

	// Create 3 schedules with different enabled states
	scheduleIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		req := &loomv1.ScheduleWorkflowRequest{
			WorkflowName: fmt.Sprintf("list-test-workflow-%d", i),
			Pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Pipeline{
					Pipeline: &loomv1.PipelinePattern{
						InitialPrompt: "test",
						Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
					},
				},
			},
			Schedule: &loomv1.ScheduleConfig{
				Cron:    "0 */6 * * *",
				Enabled: i == 1, // Only middle one is enabled
			},
		}
		resp, err := server.ScheduleWorkflow(context.Background(), req)
		require.NoError(t, err)
		scheduleIDs[i] = resp.ScheduleId
	}

	t.Run("list all schedules", func(t *testing.T) {
		resp, err := server.ListScheduledWorkflows(context.Background(), &loomv1.ListScheduledWorkflowsRequest{})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(resp.Schedules), 3, "Should have at least 3 schedules")

		// Verify our schedules are present
		foundCount := 0
		for _, s := range resp.Schedules {
			for _, id := range scheduleIDs {
				if s.Id == id {
					foundCount++
				}
			}
		}
		assert.Equal(t, 3, foundCount, "All 3 created schedules should be present")
	})

	t.Run("list enabled only", func(t *testing.T) {
		resp, err := server.ListScheduledWorkflows(context.Background(), &loomv1.ListScheduledWorkflowsRequest{
			EnabledOnly: true,
		})
		require.NoError(t, err)

		// Verify filtering works
		for _, s := range resp.Schedules {
			assert.True(t, s.Schedule.Enabled, "EnabledOnly filter should only return enabled schedules")
		}

		// Verify our enabled schedule is present
		foundEnabled := false
		for _, s := range resp.Schedules {
			if s.Id == scheduleIDs[1] {
				foundEnabled = true
				break
			}
		}
		assert.True(t, foundEnabled, "Our enabled schedule should be present")
	})
}

// TestDeleteScheduledWorkflow_Validation tests validation for DeleteScheduledWorkflow RPC
func TestDeleteScheduledWorkflow_Validation(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	tests := []struct {
		name        string
		req         *loomv1.DeleteScheduledWorkflowRequest
		expectCode  codes.Code
		expectError string
	}{
		{
			name:        "missing schedule_id",
			req:         &loomv1.DeleteScheduledWorkflowRequest{ScheduleId: ""},
			expectCode:  codes.InvalidArgument,
			expectError: "schedule_id is required",
		},
		{
			name:        "schedule not found",
			req:         &loomv1.DeleteScheduledWorkflowRequest{ScheduleId: "nonexistent-123"},
			expectCode:  codes.NotFound,
			expectError: "schedule not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.DeleteScheduledWorkflow(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.expectCode, st.Code())
			assert.Contains(t, st.Message(), tt.expectError)
		})
	}
}

// TestDeleteScheduledWorkflow_YAMLSourcedRestriction tests that YAML-sourced schedules cannot be deleted
func TestDeleteScheduledWorkflow_YAMLSourcedRestriction(t *testing.T) {
	server, sched := setupTestSchedulerServer(t)

	// Create a YAML-sourced schedule
	yamlSchedule := &loomv1.ScheduledWorkflow{
		Id:           "yaml-schedule-456",
		WorkflowName: "yaml-workflow",
		YamlPath:     "/path/to/workflow.yaml",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 8 * * *",
			Enabled: true,
		},
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}

	err := sched.AddSchedule(context.Background(), yamlSchedule)
	require.NoError(t, err)

	// Try to delete it
	req := &loomv1.DeleteScheduledWorkflowRequest{
		ScheduleId: "yaml-schedule-456",
	}

	_, err = server.DeleteScheduledWorkflow(context.Background(), req)

	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "cannot delete YAML-sourced schedules via RPC")
}

// TestDeleteScheduledWorkflow_Success tests successful deletion
func TestDeleteScheduledWorkflow_Success(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	// Create a schedule
	createReq := &loomv1.ScheduleWorkflowRequest{
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 */6 * * *",
			Enabled: true,
		},
	}

	createResp, err := server.ScheduleWorkflow(context.Background(), createReq)
	require.NoError(t, err)

	// Delete it
	deleteReq := &loomv1.DeleteScheduledWorkflowRequest{
		ScheduleId: createResp.ScheduleId,
	}

	_, err = server.DeleteScheduledWorkflow(context.Background(), deleteReq)
	require.NoError(t, err)

	// Verify it's gone
	getReq := &loomv1.GetScheduledWorkflowRequest{
		ScheduleId: createResp.ScheduleId,
	}
	_, err = server.GetScheduledWorkflow(context.Background(), getReq)
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestTriggerScheduledWorkflow_Validation tests validation for TriggerScheduledWorkflow RPC
func TestTriggerScheduledWorkflow_Validation(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	tests := []struct {
		name        string
		req         *loomv1.TriggerScheduledWorkflowRequest
		expectCode  codes.Code
		expectError string
	}{
		{
			name:        "missing schedule_id",
			req:         &loomv1.TriggerScheduledWorkflowRequest{ScheduleId: ""},
			expectCode:  codes.InvalidArgument,
			expectError: "schedule_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.TriggerScheduledWorkflow(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.expectCode, st.Code())
			assert.Contains(t, st.Message(), tt.expectError)
		})
	}
}

// TestPauseSchedule_Validation tests validation for PauseSchedule RPC
func TestPauseSchedule_Validation(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	tests := []struct {
		name        string
		req         *loomv1.PauseScheduleRequest
		expectCode  codes.Code
		expectError string
	}{
		{
			name:        "missing schedule_id",
			req:         &loomv1.PauseScheduleRequest{ScheduleId: ""},
			expectCode:  codes.InvalidArgument,
			expectError: "schedule_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.PauseSchedule(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.expectCode, st.Code())
			assert.Contains(t, st.Message(), tt.expectError)
		})
	}
}

// TestResumeSchedule_Validation tests validation for ResumeSchedule RPC
func TestResumeSchedule_Validation(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	tests := []struct {
		name        string
		req         *loomv1.ResumeScheduleRequest
		expectCode  codes.Code
		expectError string
	}{
		{
			name:        "missing schedule_id",
			req:         &loomv1.ResumeScheduleRequest{ScheduleId: ""},
			expectCode:  codes.InvalidArgument,
			expectError: "schedule_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.ResumeSchedule(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.expectCode, st.Code())
			assert.Contains(t, st.Message(), tt.expectError)
		})
	}
}

// TestPauseResumeSchedule_Success tests pause/resume functionality
func TestPauseResumeSchedule_Success(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	// Create a schedule
	createReq := &loomv1.ScheduleWorkflowRequest{
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 */6 * * *",
			Enabled: true,
		},
	}

	createResp, err := server.ScheduleWorkflow(context.Background(), createReq)
	require.NoError(t, err)

	// Pause it
	pauseReq := &loomv1.PauseScheduleRequest{
		ScheduleId: createResp.ScheduleId,
	}
	_, err = server.PauseSchedule(context.Background(), pauseReq)
	require.NoError(t, err)

	// Verify it's disabled
	getResp, err := server.GetScheduledWorkflow(context.Background(), &loomv1.GetScheduledWorkflowRequest{
		ScheduleId: createResp.ScheduleId,
	})
	require.NoError(t, err)
	assert.False(t, getResp.Schedule.Enabled)

	// Resume it
	resumeReq := &loomv1.ResumeScheduleRequest{
		ScheduleId: createResp.ScheduleId,
	}
	_, err = server.ResumeSchedule(context.Background(), resumeReq)
	require.NoError(t, err)

	// Verify it's enabled again
	getResp, err = server.GetScheduledWorkflow(context.Background(), &loomv1.GetScheduledWorkflowRequest{
		ScheduleId: createResp.ScheduleId,
	})
	require.NoError(t, err)
	assert.True(t, getResp.Schedule.Enabled)
}

// TestGetScheduleHistory_Validation tests validation for GetScheduleHistory RPC
func TestGetScheduleHistory_Validation(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	tests := []struct {
		name        string
		req         *loomv1.GetScheduleHistoryRequest
		expectCode  codes.Code
		expectError string
	}{
		{
			name:        "missing schedule_id",
			req:         &loomv1.GetScheduleHistoryRequest{ScheduleId: ""},
			expectCode:  codes.InvalidArgument,
			expectError: "schedule_id is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.GetScheduleHistory(context.Background(), tt.req)

			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok)
			assert.Equal(t, tt.expectCode, st.Code())
			assert.Contains(t, st.Message(), tt.expectError)
		})
	}
}

// TestGetScheduleHistory_Success tests successful retrieval of history
func TestGetScheduleHistory_Success(t *testing.T) {
	server, _ := setupTestSchedulerServer(t)

	// Create a schedule
	createReq := &loomv1.ScheduleWorkflowRequest{
		WorkflowName: "test-workflow",
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "test",
					Stages:        []*loomv1.PipelineStage{{AgentId: "agent1"}},
				},
			},
		},
		Schedule: &loomv1.ScheduleConfig{
			Cron:    "0 */6 * * *",
			Enabled: true,
		},
	}

	createResp, err := server.ScheduleWorkflow(context.Background(), createReq)
	require.NoError(t, err)

	// Get history (should be empty initially)
	histReq := &loomv1.GetScheduleHistoryRequest{
		ScheduleId: createResp.ScheduleId,
		Limit:      10,
	}

	histResp, err := server.GetScheduleHistory(context.Background(), histReq)

	require.NoError(t, err)
	assert.NotNil(t, histResp.Executions)
	// History should be empty since we haven't executed yet
	assert.Equal(t, 0, len(histResp.Executions))
}
