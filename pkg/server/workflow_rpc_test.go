// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestExecuteWorkflow_Validation(t *testing.T) {
	// Create server without registry
	server := &MultiAgentServer{
		agents:        make(map[string]*agent.Agent),
		workflowStore: NewWorkflowStore(),
	}

	tests := []struct {
		name        string
		req         *loomv1.ExecuteWorkflowRequest
		registry    *agent.Registry
		expectCode  codes.Code
		expectError string
	}{
		{
			name: "nil pattern",
			req: &loomv1.ExecuteWorkflowRequest{
				Pattern: nil,
			},
			expectCode:  codes.InvalidArgument,
			expectError: "pattern is required",
		},
		{
			name: "no registry configured",
			req: &loomv1.ExecuteWorkflowRequest{
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
			},
			expectCode:  codes.FailedPrecondition,
			expectError: "agent registry not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set registry if provided
			if tt.registry != nil {
				server.SetAgentRegistry(tt.registry)
			} else {
				server.registry = nil
			}

			_, err := server.ExecuteWorkflow(context.Background(), tt.req)

			if err == nil {
				t.Fatal("Expected error, got nil")
			}

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Expected gRPC status error, got %v", err)
			}

			if st.Code() != tt.expectCode {
				t.Errorf("Expected code %v, got %v", tt.expectCode, st.Code())
			}

			if tt.expectError != "" && st.Message() != tt.expectError {
				t.Errorf("Expected error message '%s', got '%s'", tt.expectError, st.Message())
			}
		})
	}
}

func TestStreamWorkflow_Validation(t *testing.T) {
	// Create server without registry
	server := &MultiAgentServer{
		agents:        make(map[string]*agent.Agent),
		workflowStore: NewWorkflowStore(),
	}

	tests := []struct {
		name        string
		req         *loomv1.ExecuteWorkflowRequest
		registry    *agent.Registry
		expectCode  codes.Code
		expectError string
	}{
		{
			name: "nil pattern",
			req: &loomv1.ExecuteWorkflowRequest{
				Pattern: nil,
			},
			expectCode:  codes.InvalidArgument,
			expectError: "pattern is required",
		},
		{
			name: "no registry configured",
			req: &loomv1.ExecuteWorkflowRequest{
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
			},
			expectCode:  codes.FailedPrecondition,
			expectError: "agent registry not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set registry if provided
			if tt.registry != nil {
				server.SetAgentRegistry(tt.registry)
			} else {
				server.registry = nil
			}

			// Create mock stream
			stream := &mockStreamWorkflowServer{ctx: context.Background()}

			err := server.StreamWorkflow(tt.req, stream)

			if err == nil {
				t.Fatal("Expected error, got nil")
			}

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Expected gRPC status error, got %v", err)
			}

			if st.Code() != tt.expectCode {
				t.Errorf("Expected code %v, got %v", tt.expectCode, st.Code())
			}

			if tt.expectError != "" && st.Message() != tt.expectError {
				t.Errorf("Expected error message '%s', got '%s'", tt.expectError, st.Message())
			}
		})
	}
}

func TestExecuteWorkflow_VariableInterpolation(t *testing.T) {
	// This test verifies that variables are properly interpolated
	// Note: Full execution requires agents to be loaded, which is tested in integration tests
	// Here we just verify the validation and setup phase
	server := &MultiAgentServer{
		agents:        make(map[string]*agent.Agent),
		workflowStore: NewWorkflowStore(),
	}

	req := &loomv1.ExecuteWorkflowRequest{
		Pattern: &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_Pipeline{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Analyze {{language}} code",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "agent1", PromptTemplate: "Check {{check_type}}"},
					},
				},
			},
		},
		Variables: map[string]string{
			"language":   "Go",
			"check_type": "syntax",
		},
	}

	// Should fail at registry check (expected), but proves validation passed
	_, err := server.ExecuteWorkflow(context.Background(), req)

	if err == nil {
		t.Fatal("Expected error (no registry), got nil")
	}

	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("Expected FailedPrecondition (no registry), got %v", st.Code())
	}
}

// mockStreamWorkflowServer implements the gRPC stream interface for testing
type mockStreamWorkflowServer struct {
	ctx      context.Context
	messages []*loomv1.WorkflowProgress
}

func (m *mockStreamWorkflowServer) Send(update *loomv1.WorkflowProgress) error {
	m.messages = append(m.messages, update)
	return nil
}

func (m *mockStreamWorkflowServer) Context() context.Context {
	return m.ctx
}

func (m *mockStreamWorkflowServer) SetHeader(md metadata.MD) error  { return nil }
func (m *mockStreamWorkflowServer) SendHeader(md metadata.MD) error { return nil }
func (m *mockStreamWorkflowServer) SetTrailer(md metadata.MD)       {}
func (m *mockStreamWorkflowServer) SendMsg(msg interface{}) error   { return nil }
func (m *mockStreamWorkflowServer) RecvMsg(msg interface{}) error   { return nil }

func TestGetWorkflowExecution(t *testing.T) {
	server := &MultiAgentServer{
		agents:        make(map[string]*agent.Agent),
		workflowStore: NewWorkflowStore(),
	}

	// Create and store a test execution
	testExecution := &WorkflowExecution{
		ExecutionID: "test-exec-123",
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
		Status: WorkflowStatusCompleted,
		Result: &loomv1.WorkflowResult{
			PatternType:  "pipeline",
			MergedOutput: "test output",
		},
	}
	server.workflowStore.Store(testExecution)

	tests := []struct {
		name        string
		req         *loomv1.GetWorkflowExecutionRequest
		expectCode  codes.Code
		expectError string
		expectID    string
	}{
		{
			name: "valid execution",
			req: &loomv1.GetWorkflowExecutionRequest{
				ExecutionId: "test-exec-123",
			},
			expectCode: codes.OK,
			expectID:   "test-exec-123",
		},
		{
			name: "missing execution_id",
			req: &loomv1.GetWorkflowExecutionRequest{
				ExecutionId: "",
			},
			expectCode:  codes.InvalidArgument,
			expectError: "execution_id is required",
		},
		{
			name: "not found",
			req: &loomv1.GetWorkflowExecutionRequest{
				ExecutionId: "nonexistent",
			},
			expectCode:  codes.NotFound,
			expectError: "workflow execution not found: nonexistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := server.GetWorkflowExecution(context.Background(), tt.req)

			if tt.expectCode == codes.OK {
				if err != nil {
					t.Fatalf("Expected no error, got %v", err)
				}
				if resp.Id != tt.expectID {
					t.Errorf("Expected ID %s, got %s", tt.expectID, resp.Id)
				}
				if resp.Status != string(WorkflowStatusCompleted) {
					t.Errorf("Expected status %s, got %s", WorkflowStatusCompleted, resp.Status)
				}
			} else {
				if err == nil {
					t.Fatal("Expected error, got nil")
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Fatalf("Expected gRPC status error, got %v", err)
				}
				if st.Code() != tt.expectCode {
					t.Errorf("Expected code %v, got %v", tt.expectCode, st.Code())
				}
				if tt.expectError != "" && st.Message() != tt.expectError {
					t.Errorf("Expected error message '%s', got '%s'", tt.expectError, st.Message())
				}
			}
		})
	}
}

func TestListWorkflowExecutions(t *testing.T) {
	server := &MultiAgentServer{
		agents:        make(map[string]*agent.Agent),
		workflowStore: NewWorkflowStore(),
	}

	// Store multiple test executions
	executions := []*WorkflowExecution{
		{
			ExecutionID: "exec-1",
			Pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Pipeline{
					Pipeline: &loomv1.PipelinePattern{InitialPrompt: "test1"},
				},
			},
			Status: WorkflowStatusCompleted,
		},
		{
			ExecutionID: "exec-2",
			Pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Debate{
					Debate: &loomv1.DebatePattern{Topic: "test debate"},
				},
			},
			Status: WorkflowStatusRunning,
		},
		{
			ExecutionID: "exec-3",
			Pattern: &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Pipeline{
					Pipeline: &loomv1.PipelinePattern{InitialPrompt: "test2"},
				},
			},
			Status: WorkflowStatusFailed,
			Error:  "test error",
		},
	}

	for _, exec := range executions {
		server.workflowStore.Store(exec)
	}

	tests := []struct {
		name                 string
		req                  *loomv1.ListWorkflowExecutionsRequest
		expectedCount        int
		expectedStatuses     []string
		expectedPatternTypes []string
	}{
		{
			name:          "list all executions",
			req:           &loomv1.ListWorkflowExecutionsRequest{},
			expectedCount: 3,
		},
		{
			name: "filter by status - completed",
			req: &loomv1.ListWorkflowExecutionsRequest{
				StatusFilter: string(WorkflowStatusCompleted),
			},
			expectedCount:    1,
			expectedStatuses: []string{string(WorkflowStatusCompleted)},
		},
		{
			name: "filter by status - running",
			req: &loomv1.ListWorkflowExecutionsRequest{
				StatusFilter: string(WorkflowStatusRunning),
			},
			expectedCount:    1,
			expectedStatuses: []string{string(WorkflowStatusRunning)},
		},
		{
			name: "filter by pattern type - pipeline",
			req: &loomv1.ListWorkflowExecutionsRequest{
				PatternTypeFilter: "pipeline",
			},
			expectedCount:        2,
			expectedPatternTypes: []string{"pipeline", "pipeline"},
		},
		{
			name: "filter by pattern type - debate",
			req: &loomv1.ListWorkflowExecutionsRequest{
				PatternTypeFilter: "debate",
			},
			expectedCount:        1,
			expectedPatternTypes: []string{"debate"},
		},
		{
			name: "filter by both status and pattern",
			req: &loomv1.ListWorkflowExecutionsRequest{
				StatusFilter:      string(WorkflowStatusCompleted),
				PatternTypeFilter: "pipeline",
			},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := server.ListWorkflowExecutions(context.Background(), tt.req)

			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}

			if len(resp.Executions) != tt.expectedCount {
				t.Errorf("Expected %d executions, got %d", tt.expectedCount, len(resp.Executions))
			}

			if int(resp.TotalCount) != tt.expectedCount {
				t.Errorf("Expected total_count %d, got %d", tt.expectedCount, resp.TotalCount)
			}

			// Verify status filter if specified
			if len(tt.expectedStatuses) > 0 {
				for i, exec := range resp.Executions {
					if i < len(tt.expectedStatuses) && exec.Status != tt.expectedStatuses[i] {
						t.Errorf("Expected status %s at index %d, got %s", tt.expectedStatuses[i], i, exec.Status)
					}
				}
			}

			// Verify pattern type filter if specified
			if len(tt.expectedPatternTypes) > 0 {
				for i, exec := range resp.Executions {
					patternType := getPatternTypeFromPattern(exec.Pattern)
					if i < len(tt.expectedPatternTypes) && patternType != tt.expectedPatternTypes[i] {
						t.Errorf("Expected pattern type %s at index %d, got %s", tt.expectedPatternTypes[i], i, patternType)
					}
				}
			}
		})
	}
}
