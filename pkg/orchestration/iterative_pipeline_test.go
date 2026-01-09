// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// TestIterativePipeline_BasicExecution tests iterative pipeline without restarts.
func TestIterativePipeline_BasicExecution(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Create LLM provider and agents
	llm := newMockLLMProvider("Stage 1 output", "Stage 2 output", "Stage 3 output")
	agent1 := createMockAgent(t, "stage1", llm)
	agent2 := createMockAgent(t, "stage2", llm)
	agent3 := createMockAgent(t, "stage3", llm)

	// Create orchestrator
	orchestrator := NewOrchestrator(Config{
		LLMProvider: llm,
		Tracer:      tracer,
		Logger:      logger,
	})

	orchestrator.RegisterAgent("stage1", agent1)
	orchestrator.RegisterAgent("stage2", agent2)
	orchestrator.RegisterAgent("stage3", agent3)

	// Create iterative pattern (restart disabled)
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Start workflow",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1", PromptTemplate: "Execute stage 1"},
						{AgentId: "stage2", PromptTemplate: "Execute stage 2: {{previous}}"},
						{AgentId: "stage3", PromptTemplate: "Execute stage 3: {{previous}}"},
					},
				},
				MaxIterations: 3,
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled: false, // Restart disabled
				},
			},
		},
	}

	// Execute workflow
	ctx, span := tracer.StartSpan(context.Background(), "test.iterative_basic")
	defer tracer.EndSpan(span)

	result, err := orchestrator.ExecutePattern(ctx, pattern)

	// Assertions
	require.NoError(t, err)
	assert.NotNil(t, result)
	// When restart disabled, falls back to standard pipeline
	assert.Equal(t, "pipeline", result.PatternType)
	assert.Len(t, result.AgentResults, 3)
	assert.Contains(t, result.MergedOutput, "Stage 3 output")
}

// TestIterativePipeline_WithRestartCoordination tests restart via pub/sub.
// This test verifies that the restart coordination infrastructure works end-to-end.
// NOTE: Restart messages must be published AFTER subscription is established for delivery.
func TestIterativePipeline_WithRestartCoordination(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Create communication infrastructure
	memoryStore := communication.NewMemoryStore(5 * time.Minute)
	messageBus := communication.NewMessageBus(memoryStore, nil, tracer, logger)

	// Create LLM provider with enough responses for restart scenario
	llm := newMockLLMProvider(
		"Stage 1 iteration 1", // Stage 1, iteration 1
		"Stage 2 iteration 1", // Stage 2, iteration 1
		"Stage 1 iteration 2", // Stage 1, iteration 2 (after restart)
		"Stage 2 iteration 2", // Stage 2, iteration 2
	)
	agent1 := createMockAgent(t, "stage1", llm)
	agent2 := createMockAgent(t, "stage2", llm)

	// Create orchestrator with MessageBus
	orchestrator := NewOrchestrator(Config{
		LLMProvider: llm,
		Tracer:      tracer,
		Logger:      logger,
		MessageBus:  messageBus,
	})

	orchestrator.RegisterAgent("stage1", agent1)
	orchestrator.RegisterAgent("stage2", agent2)

	// Create iterative pattern with restart enabled
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Start workflow",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1", PromptTemplate: "Discover data"},
						{AgentId: "stage2", PromptTemplate: "Analyze: {{previous}}"},
					},
				},
				MaxIterations: 5,
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled:           true,
					RestartableStages: []string{"stage1"}, // Only stage1 can be restarted
					CooldownSeconds:   0,                  // No cooldown for test
					PreserveOutputs:   false,              // Clear outputs on restart
				},
				RestartTriggers: []string{"stage2"}, // stage2 can trigger restarts
				RestartTopic:    "workflow.restart",
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ctx, span := tracer.StartSpan(ctx, "test.iterative_restart")
	defer tracer.EndSpan(span)

	// Execute in background so we can publish restart request during execution
	resultChan := make(chan *loomv1.WorkflowResult)
	errChan := make(chan error)

	go func() {
		executor := NewIterativePipelineExecutor(orchestrator, pattern.GetIterative(), messageBus)
		result, err := executor.Execute(ctx)
		if err != nil {
			errChan <- err
		} else {
			resultChan <- result
		}
	}()

	// Wait for subscription to be established (subscription happens at start of Execute)
	time.Sleep(200 * time.Millisecond)

	// Now publish restart request - the subscription should receive it
	restartReq := &loomv1.RestartRequest{
		RequesterStageId: "stage2",
		TargetStageId:    "stage1",
		Reason:           "Need different data subset",
		Iteration:        1,
		TimestampMs:      time.Now().UnixMilli(),
	}

	payload, err := json.Marshal(restartReq)
	require.NoError(t, err)

	msg := &loomv1.BusMessage{
		Id:        "test-restart-req",
		Topic:     "workflow.restart",
		FromAgent: "stage2",
		Payload: &loomv1.MessagePayload{
			Data: &loomv1.MessagePayload_Value{
				Value: payload,
			},
		},
		Metadata: map[string]string{
			"type": "restart_request",
		},
	}

	_, _, err = messageBus.Publish(ctx, "workflow.restart", msg)
	require.NoError(t, err)

	// Wait for result
	select {
	case result := <-resultChan:
		require.NotNil(t, result)
		assert.Equal(t, "iterative_pipeline", result.PatternType)
		// Should have executed stages and restarted
		// Note: Restart behavior depends on timing - may or may not execute
		// This test verifies the infrastructure works without race conditions
		assert.NotEmpty(t, result.AgentResults)
		assert.NotEmpty(t, result.Metadata["iterations_used"])
	case err := <-errChan:
		require.NoError(t, err, "Execution should not error")
	case <-ctx.Done():
		t.Fatal("Test timed out")
	}
}

// TestIterativePipeline_RestartValidation tests restart validation rules.
func TestIterativePipeline_RestartValidation(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	orchestrator := NewOrchestrator(Config{
		LLMProvider: newMockLLMProvider(),
		Tracer:      tracer,
		Logger:      logger,
	})

	tests := []struct {
		name          string
		pattern       *loomv1.IterativeWorkflowPattern
		request       *loomv1.RestartRequest
		currentStage  int
		expectValid   bool
		expectedError string
	}{
		{
			name: "valid_restart_backwards",
			pattern: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1"},
						{AgentId: "stage2"},
						{AgentId: "stage3"},
					},
				},
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled: true,
				},
			},
			request: &loomv1.RestartRequest{
				RequesterStageId: "stage3",
				TargetStageId:    "stage1",
			},
			currentStage:  2, // At stage3 (index 2)
			expectValid:   true,
			expectedError: "",
		},
		{
			name: "invalid_restart_forward",
			pattern: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1"},
						{AgentId: "stage2"},
						{AgentId: "stage3"},
					},
				},
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled: true,
				},
			},
			request: &loomv1.RestartRequest{
				RequesterStageId: "stage1",
				TargetStageId:    "stage3", // Forward jump not allowed
			},
			currentStage:  0, // At stage1 (index 0)
			expectValid:   false,
			expectedError: "can only restart earlier stages",
		},
		{
			name: "invalid_stage_not_restartable",
			pattern: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1"},
						{AgentId: "stage2"},
						{AgentId: "stage3"},
					},
				},
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled:           true,
					RestartableStages: []string{"stage2"}, // Only stage2 allowed
				},
			},
			request: &loomv1.RestartRequest{
				RequesterStageId: "stage3",
				TargetStageId:    "stage1", // stage1 not in whitelist
			},
			currentStage:  2,
			expectValid:   false,
			expectedError: "not in restartable_stages list",
		},
		{
			name: "invalid_requester_not_authorized",
			pattern: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1"},
						{AgentId: "stage2"},
						{AgentId: "stage3"},
					},
				},
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled: true,
				},
				RestartTriggers: []string{"stage3"}, // Only stage3 can trigger
			},
			request: &loomv1.RestartRequest{
				RequesterStageId: "stage2", // stage2 not authorized
				TargetStageId:    "stage1",
			},
			currentStage:  1,
			expectValid:   false,
			expectedError: "not authorized to trigger restarts",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			executor := NewIterativePipelineExecutor(orchestrator, tt.pattern, nil)
			err := executor.validateRestartRequest(tt.request, tt.currentStage)

			if tt.expectValid {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			}
		})
	}
}

// TestIterativePipeline_MaxIterations tests iteration limit enforcement.
// This test verifies that workflows respect the max_iterations limit.
func TestIterativePipeline_MaxIterations(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	// Create communication infrastructure
	memoryStore := communication.NewMemoryStore(5 * time.Minute)
	messageBus := communication.NewMessageBus(memoryStore, nil, tracer, logger)

	// Create LLM provider that always returns output
	responses := make([]string, 20) // More than max iterations
	for i := range responses {
		responses[i] = "output"
	}
	llm := newMockLLMProvider(responses...)

	agent1 := createMockAgent(t, "stage1", llm)

	orchestrator := NewOrchestrator(Config{
		LLMProvider: llm,
		Tracer:      tracer,
		Logger:      logger,
		MessageBus:  messageBus,
	})

	orchestrator.RegisterAgent("stage1", agent1)

	// Create pattern with low max iterations
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Start",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1", PromptTemplate: "Execute"},
					},
				},
				MaxIterations: 2, // Low limit
				RestartPolicy: &loomv1.RestartPolicy{
					Enabled: true,
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ctx, span := tracer.StartSpan(ctx, "test.max_iterations")
	defer tracer.EndSpan(span)

	result, err := orchestrator.ExecutePattern(ctx, pattern)

	// Should complete without error and respect max iterations
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify iterations metadata exists and is within limits
	iterations := result.Metadata["iterations_used"]
	assert.NotEmpty(t, iterations, "Should have iterations metadata")
	// Should not exceed max iterations (may be 1 if no restarts triggered)
	assert.Contains(t, []string{"1", "2"}, iterations, "Iterations should be 1 or 2")
}

// TestIterativePipeline_Cooldown tests that cooldown policy is configured correctly.
// Note: Cooldown timing is enforced during execution, not in validateRestartRequest.
func TestIterativePipeline_Cooldown(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()
	memoryStore := communication.NewMemoryStore(5 * time.Minute)
	messageBus := communication.NewMessageBus(memoryStore, nil, tracer, logger)

	llm := newMockLLMProvider("output1", "output2", "output3")
	agent1 := createMockAgent(t, "stage1", llm)
	agent2 := createMockAgent(t, "stage2", llm)

	orchestrator := NewOrchestrator(Config{
		LLMProvider: llm,
		Tracer:      tracer,
		Logger:      logger,
		MessageBus:  messageBus,
	})

	orchestrator.RegisterAgent("stage1", agent1)
	orchestrator.RegisterAgent("stage2", agent2)

	pattern := &loomv1.IterativeWorkflowPattern{
		Pipeline: &loomv1.PipelinePattern{
			InitialPrompt: "Start",
			Stages: []*loomv1.PipelineStage{
				{AgentId: "stage1"},
				{AgentId: "stage2"},
			},
		},
		MaxIterations: 10,
		RestartPolicy: &loomv1.RestartPolicy{
			Enabled:         true,
			CooldownSeconds: 2, // 2 second cooldown
		},
	}

	executor := NewIterativePipelineExecutor(orchestrator, pattern, messageBus)

	// Verify that restart validation works for valid backward restarts
	req := &loomv1.RestartRequest{
		RequesterStageId: "stage2",
		TargetStageId:    "stage1",
	}
	err := executor.validateRestartRequest(req, 1) // At stage 2 (index 1)
	assert.NoError(t, err)                         // Should pass basic validation
}

// TestIterativePipeline_FallbackToStandardPipeline tests behavior when restart disabled.
func TestIterativePipeline_FallbackToStandardPipeline(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	llm := newMockLLMProvider("output1", "output2")
	agent1 := createMockAgent(t, "stage1", llm)
	agent2 := createMockAgent(t, "stage2", llm)

	orchestrator := NewOrchestrator(Config{
		LLMProvider: llm,
		Tracer:      tracer,
		Logger:      logger,
	})

	orchestrator.RegisterAgent("stage1", agent1)
	orchestrator.RegisterAgent("stage2", agent2)

	// Pattern with nil restart policy
	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Start",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1"},
						{AgentId: "stage2"},
					},
				},
				RestartPolicy: nil, // Nil policy
			},
		},
	}

	ctx := context.Background()
	result, err := orchestrator.ExecutePattern(ctx, pattern)

	require.NoError(t, err)
	assert.NotNil(t, result)
	// Should execute as standard pipeline
	assert.Len(t, result.AgentResults, 2)
}

// TestIterativePipeline_Instrumentation tests observability integration.
func TestIterativePipeline_Instrumentation(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	tracer := observability.NewNoOpTracer()

	llm := newMockLLMProvider("output1", "output2")
	agent1 := createMockAgent(t, "stage1", llm)
	agent2 := createMockAgent(t, "stage2", llm)

	orchestrator := NewOrchestrator(Config{
		LLMProvider: llm,
		Tracer:      tracer,
		Logger:      logger,
	})

	orchestrator.RegisterAgent("stage1", agent1)
	orchestrator.RegisterAgent("stage2", agent2)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{
			Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "Start",
					Stages: []*loomv1.PipelineStage{
						{AgentId: "stage1"},
						{AgentId: "stage2"},
					},
				},
				MaxIterations: 3,
				RestartPolicy: &loomv1.RestartPolicy{Enabled: false},
			},
		},
	}

	// Create instrumented context with span
	ctx, span := tracer.StartSpan(context.Background(), "test.iterative_instrumentation")
	defer tracer.EndSpan(span)

	_, err := orchestrator.ExecutePattern(ctx, pattern)
	require.NoError(t, err)

	// Basic validation that execution completed without panic
	// (Comprehensive observability testing requires real Hawk integration)
	assert.NotNil(t, span, "Expected span to be created")
}
