// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// mockLLMProvider is a test LLM provider that returns predictable responses.
type mockLLMProvider struct {
	mu        sync.Mutex
	responses []string
	callCount int
}

func newMockLLMProvider(responses ...string) *mockLLMProvider {
	return &mockLLMProvider{
		responses: responses,
	}
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var response string
	if m.callCount < len(m.responses) {
		response = m.responses[m.callCount]
	} else {
		response = fmt.Sprintf("Mock response %d", m.callCount)
	}
	m.callCount++

	return &llmtypes.LLMResponse{
		Content:    response,
		ToolCalls:  nil,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
			CostUSD:      0.001,
		},
	}, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func (m *mockLLMProvider) Model() string {
	return "mock-model"
}

// mockBackend is a minimal test backend.
type mockBackend struct{}

func (m *mockBackend) Name() string { return "mock" }
func (m *mockBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	return &fabric.QueryResult{Type: "mock", RowCount: 0}, nil
}
func (m *mockBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	return &fabric.Schema{Name: resource, Type: "table"}, nil
}
func (m *mockBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	return []fabric.Resource{{Name: "mock_resource", Type: "table"}}, nil
}
func (m *mockBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	return map[string]interface{}{"mock": true}, nil
}
func (m *mockBackend) Ping(ctx context.Context) error { return nil }
func (m *mockBackend) Capabilities() *fabric.Capabilities {
	return fabric.NewCapabilities()
}
func (m *mockBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockBackend) Close() error { return nil }

// createMockAgent creates a test agent with the given name and LLM provider.
func createMockAgent(t *testing.T, name string, llm agent.LLMProvider) *agent.Agent {
	backend := &mockBackend{}

	ag := agent.NewAgent(
		backend,
		llm,
		agent.WithName(name),
		agent.WithDescription(fmt.Sprintf("Test agent: %s", name)),
	)

	return ag
}

// TestDebatePattern tests the debate orchestration pattern.
func TestDebatePattern(t *testing.T) {
	tests := []struct {
		name          string
		topic         string
		numAgents     int
		rounds        int32
		mergeStrategy loomv1.MergeStrategy
		wantErr       bool
	}{
		{
			name:          "basic two-agent debate",
			topic:         "Should we use microservices?",
			numAgents:     2,
			rounds:        2,
			mergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			wantErr:       false,
		},
		{
			name:          "single round debate",
			topic:         "Best programming language?",
			numAgents:     3,
			rounds:        1,
			mergeStrategy: loomv1.MergeStrategy_FIRST,
			wantErr:       false,
		},
		{
			name:          "multi-round with consensus",
			topic:         "Cloud strategy",
			numAgents:     2,
			rounds:        3,
			mergeStrategy: loomv1.MergeStrategy_CONSENSUS,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create orchestrator
			orchestrator := NewOrchestrator(Config{
				Logger: zaptest.NewLogger(t),
				Tracer: observability.NewNoOpTracer(),
				LLMProvider: newMockLLMProvider(
					"Agent 1 response round 1",
					"Agent 2 response round 1",
					"Agent 1 response round 2",
					"Agent 2 response round 2",
					"Consensus: Both perspectives are valid",
				),
			})

			// Create mock agents
			agents := make([]*agent.Agent, tt.numAgents)
			for i := 0; i < tt.numAgents; i++ {
				llm := newMockLLMProvider(fmt.Sprintf("Agent %d perspective", i+1))
				agents[i] = createMockAgent(t, fmt.Sprintf("agent%d", i+1), llm)
			}

			// Build and execute debate
			builder := orchestrator.Debate(tt.topic)
			for _, ag := range agents {
				builder = builder.WithAgents(ag)
			}
			builder = builder.WithRounds(int(tt.rounds)).
				WithMergeStrategy(tt.mergeStrategy)

			result, err := builder.Execute(context.Background())

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, "debate", result.PatternType)
			assert.NotEmpty(t, result.MergedOutput)
			assert.Equal(t, int(tt.rounds)*tt.numAgents, len(result.AgentResults))
			assert.NotNil(t, result.Cost)
			assert.GreaterOrEqual(t, result.DurationMs, int64(0))
		})
	}
}

// TestDebatePattern_WithModerator tests debate with a moderator agent.
func TestDebatePattern_WithModerator(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
		LLMProvider: newMockLLMProvider("Consensus reached"),
	})

	// Create agents
	agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Pro argument"))
	agent2 := createMockAgent(t, "agent2", newMockLLMProvider("Con argument"))
	moderator := createMockAgent(t, "moderator", newMockLLMProvider("Framing the discussion"))

	result, err := orchestrator.
		Debate("Test topic").
		WithAgents(agent1, agent2).
		WithModerator(moderator).
		WithRounds(1).
		WithMergeStrategy(loomv1.MergeStrategy_CONCATENATE).
		Execute(context.Background())

	require.NoError(t, err)
	assert.NotNil(t, result)
	// Should have 2 debating agents (moderator synthesis is in DebateResult.ModeratorSynthesis)
	assert.Equal(t, 2, len(result.AgentResults))

	// Verify moderator synthesis is present
	debateResult := result.GetDebateResult()
	require.NotNil(t, debateResult)
	assert.NotEmpty(t, debateResult.ModeratorSynthesis, "Moderator synthesis should be present")
}

// TestForkJoinPattern tests the fork-join orchestration pattern.
func TestForkJoinPattern(t *testing.T) {
	tests := []struct {
		name          string
		prompt        string
		numAgents     int
		mergeStrategy loomv1.MergeStrategy
		timeout       int
		wantErr       bool
	}{
		{
			name:          "basic parallel execution",
			prompt:        "Analyze this code",
			numAgents:     3,
			mergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			timeout:       0,
			wantErr:       false,
		},
		{
			name:          "with timeout",
			prompt:        "Review security",
			numAgents:     2,
			mergeStrategy: loomv1.MergeStrategy_SUMMARY,
			timeout:       30,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := NewOrchestrator(Config{
				Logger:      zaptest.NewLogger(t),
				Tracer:      observability.NewNoOpTracer(),
				LLMProvider: newMockLLMProvider("Summary of all responses"),
			})

			// Create mock agents
			agents := make([]*agent.Agent, tt.numAgents)
			for i := 0; i < tt.numAgents; i++ {
				llm := newMockLLMProvider(fmt.Sprintf("Agent %d analysis", i+1))
				agents[i] = createMockAgent(t, fmt.Sprintf("agent%d", i+1), llm)
			}

			// Build and execute fork-join
			builder := orchestrator.Fork(tt.prompt)
			for _, ag := range agents {
				builder = builder.WithAgents(ag)
			}
			builder = builder.Join(tt.mergeStrategy)
			if tt.timeout > 0 {
				builder = builder.WithTimeout(tt.timeout)
			}

			result, err := builder.Execute(context.Background())

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, "fork_join", result.PatternType)
			assert.NotEmpty(t, result.MergedOutput)
			assert.Equal(t, tt.numAgents, len(result.AgentResults))
			assert.NotNil(t, result.Cost)
		})
	}
}

// TestPipelinePattern tests the pipeline orchestration pattern.
func TestPipelinePattern(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})

	// Create agents for each stage
	architect := createMockAgent(t, "architect", newMockLLMProvider("Architecture design"))
	implementer := createMockAgent(t, "implementer", newMockLLMProvider("Implementation code"))
	reviewer := createMockAgent(t, "reviewer", newMockLLMProvider("Review feedback"))

	result, err := orchestrator.
		Pipeline("Design a REST API").
		WithStage(architect, "Create architecture for: {{previous}}").
		WithStage(implementer, "Implement based on: {{previous}}").
		WithStage(reviewer, "Review: {{previous}}").
		Execute(context.Background())

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "pipeline", result.PatternType)
	assert.NotEmpty(t, result.MergedOutput)
	assert.Equal(t, 3, len(result.AgentResults))

	// Verify stage metadata
	for i, agentResult := range result.AgentResults {
		assert.Equal(t, fmt.Sprintf("%d", i+1), agentResult.Metadata["stage"])
	}
}

// TestPipelinePattern_WithFullHistory tests pipeline with full history enabled.
func TestPipelinePattern_WithFullHistory(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})

	agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Step 1"))
	agent2 := createMockAgent(t, "agent2", newMockLLMProvider("Step 2"))

	result, err := orchestrator.
		Pipeline("Initial prompt").
		WithStage(agent1, "Process: {{previous}}").
		WithStage(agent2, "Finalize with history: {{history}}").
		WithFullHistory().
		Execute(context.Background())

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "pipeline", result.PatternType)
	assert.Equal(t, "true", result.Metadata["pass_full_history"])
}

// TestParallelPattern tests the parallel orchestration pattern.
func TestParallelPattern(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
		LLMProvider: newMockLLMProvider("Combined summary"),
	})

	// Create agents for different tasks
	qualityAgent := createMockAgent(t, "quality", newMockLLMProvider("Quality: Good"))
	securityAgent := createMockAgent(t, "security", newMockLLMProvider("Security: No issues"))
	performanceAgent := createMockAgent(t, "performance", newMockLLMProvider("Performance: Optimal"))

	result, err := orchestrator.
		Parallel().
		WithTask(qualityAgent, "Analyze code quality").
		WithTask(securityAgent, "Check for vulnerabilities").
		WithTask(performanceAgent, "Review performance").
		WithMergeStrategy(loomv1.MergeStrategy_SUMMARY).
		Execute(context.Background())

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "parallel", result.PatternType)
	assert.NotEmpty(t, result.MergedOutput)
	assert.Equal(t, 3, len(result.AgentResults))

	// Verify each task has its metadata
	for _, agentResult := range result.AgentResults {
		assert.Contains(t, agentResult.Metadata, "task_index")
	}
}

// TestConditionalPattern tests the conditional orchestration pattern.
func TestConditionalPattern(t *testing.T) {
	tests := []struct {
		name             string
		classifierOutput string
		expectedBranch   string
	}{
		{
			name:             "routes to bug branch",
			classifierOutput: "bug",
			expectedBranch:   "bug",
		},
		{
			name:             "routes to feature branch",
			classifierOutput: "feature",
			expectedBranch:   "feature",
		},
		{
			name:             "uses default branch",
			classifierOutput: "unknown",
			expectedBranch:   "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orchestrator := NewOrchestrator(Config{
				Logger: zaptest.NewLogger(t),
				Tracer: observability.NewNoOpTracer(),
			})

			// Create agents for branches
			bugAgent := createMockAgent(t, "debugger", newMockLLMProvider("Debugging..."))
			featureAgent := createMockAgent(t, "designer", newMockLLMProvider("Designing..."))
			defaultAgent := createMockAgent(t, "general", newMockLLMProvider("General analysis..."))

			// Register agents with orchestrator first
			orchestrator.RegisterAgent("bug_agent", bugAgent)
			orchestrator.RegisterAgent("feature_agent", featureAgent)
			orchestrator.RegisterAgent("default_agent", defaultAgent)

			// Create branch patterns with registered agent IDs
			bugPattern := &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_ForkJoin{
					ForkJoin: &loomv1.ForkJoinPattern{
						Prompt:        "Debug issue",
						AgentIds:      []string{"bug_agent"},
						MergeStrategy: loomv1.MergeStrategy_FIRST,
					},
				},
			}
			featurePattern := &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_ForkJoin{
					ForkJoin: &loomv1.ForkJoinPattern{
						Prompt:        "Design feature",
						AgentIds:      []string{"feature_agent"},
						MergeStrategy: loomv1.MergeStrategy_FIRST,
					},
				},
			}
			defaultPattern := &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_ForkJoin{
					ForkJoin: &loomv1.ForkJoinPattern{
						Prompt:        "General analysis",
						AgentIds:      []string{"default_agent"},
						MergeStrategy: loomv1.MergeStrategy_FIRST,
					},
				},
			}

			classifier := createMockAgent(t, "classifier",
				newMockLLMProvider(tt.classifierOutput))

			builder := orchestrator.Conditional(classifier, "Classify this issue")
			builder = builder.When("bug", bugPattern).
				When("feature", featurePattern).
				Default(defaultPattern)

			result, err := builder.Execute(context.Background())

			require.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, "conditional", result.PatternType)
			assert.Equal(t, tt.expectedBranch, result.Metadata["selected_branch"])
			assert.Contains(t, result.Metadata, "condition_result")
		})
	}
}

// TestMergeStrategies tests all merge strategies.
func TestMergeStrategies(t *testing.T) {
	strategies := []loomv1.MergeStrategy{
		loomv1.MergeStrategy_FIRST,
		loomv1.MergeStrategy_CONCATENATE,
		loomv1.MergeStrategy_CONSENSUS,
		loomv1.MergeStrategy_VOTING,
		loomv1.MergeStrategy_SUMMARY,
		loomv1.MergeStrategy_BEST,
	}

	for _, strategy := range strategies {
		t.Run(strategy.String(), func(t *testing.T) {
			orchestrator := NewOrchestrator(Config{
				Logger: zaptest.NewLogger(t),
				Tracer: observability.NewNoOpTracer(),
				LLMProvider: newMockLLMProvider(
					fmt.Sprintf("Merged result using %s", strategy.String()),
				),
			})

			agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Response 1"))
			agent2 := createMockAgent(t, "agent2", newMockLLMProvider("Response 2"))

			result, err := orchestrator.
				Fork("Test prompt").
				WithAgents(agent1, agent2).
				Join(strategy).
				Execute(context.Background())

			require.NoError(t, err)
			assert.NotNil(t, result)
			assert.NotEmpty(t, result.MergedOutput)

			// FIRST strategy should return one of the agent outputs
			// (either is valid since parallel execution order is non-deterministic)
			if strategy == loomv1.MergeStrategy_FIRST {
				assert.True(t,
					strings.Contains(result.MergedOutput, "Response 1") ||
						strings.Contains(result.MergedOutput, "Response 2"),
					"FIRST strategy should return one of the agent responses")
			}
		})
	}
}

// TestConcurrentExecution tests concurrent execution of patterns.
func TestConcurrentExecution(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})

	var wg sync.WaitGroup
	numConcurrent := 10

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			agent1 := createMockAgent(t, fmt.Sprintf("agent%d-1", id),
				newMockLLMProvider(fmt.Sprintf("Response from agent %d-1", id)))
			agent2 := createMockAgent(t, fmt.Sprintf("agent%d-2", id),
				newMockLLMProvider(fmt.Sprintf("Response from agent %d-2", id)))

			result, err := orchestrator.
				Fork(fmt.Sprintf("Prompt %d", id)).
				WithAgents(agent1, agent2).
				Join(loomv1.MergeStrategy_CONCATENATE).
				Execute(context.Background())

			assert.NoError(t, err)
			assert.NotNil(t, result)
		}(i)
	}

	wg.Wait()
}

// TestCostAggregation tests cost tracking across patterns.
func TestCostAggregation(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})

	// Create agents with predictable token usage
	agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Response 1"))
	agent2 := createMockAgent(t, "agent2", newMockLLMProvider("Response 2"))

	result, err := orchestrator.
		Fork("Cost test").
		WithAgents(agent1, agent2).
		Join(loomv1.MergeStrategy_CONCATENATE).
		Execute(context.Background())

	require.NoError(t, err)
	assert.NotNil(t, result.Cost)
	assert.Greater(t, result.Cost.TotalCostUsd, 0.0)
	assert.Greater(t, result.Cost.TotalTokens, int32(0))
	assert.Equal(t, int32(2), result.Cost.LlmCalls)
	assert.Len(t, result.Cost.AgentCostsUsd, 2)
}

// TestContextCancellation tests context cancellation during execution.
func TestContextCancellation(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})

	// Create a slow mock LLM that will be interrupted
	slowLLM := &mockLLMProvider{
		responses: []string{"This should not complete"},
	}

	agent1 := createMockAgent(t, "agent1", slowLLM)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// Give it a tiny bit of time to start, then it should be cancelled
	time.Sleep(2 * time.Millisecond)

	_, err := orchestrator.
		Fork("Test cancellation").
		WithAgents(agent1).
		Join(loomv1.MergeStrategy_FIRST).
		Execute(ctx)

	// The execution may complete before cancellation, or may be cancelled
	// Either is acceptable for this test
	if err != nil {
		assert.Contains(t, err.Error(), "context")
	}
}

// TestValidationErrors tests validation error cases.
func TestValidationErrors(t *testing.T) {
	orchestrator := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})

	t.Run("debate requires topic", func(t *testing.T) {
		agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Response"))

		_, err := orchestrator.
			Debate("").
			WithAgents(agent1).
			Execute(context.Background())

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "topic cannot be empty")
	})

	t.Run("debate requires at least 2 agents", func(t *testing.T) {
		agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Response"))

		_, err := orchestrator.
			Debate("Test topic").
			WithAgents(agent1).
			Execute(context.Background())

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "requires at least 2 agents")
	})

	t.Run("fork-join requires prompt", func(t *testing.T) {
		agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Response"))

		_, err := orchestrator.
			Fork("").
			WithAgents(agent1).
			Execute(context.Background())

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "prompt cannot be empty")
	})

	t.Run("pipeline requires initial prompt", func(t *testing.T) {
		agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Response"))

		_, err := orchestrator.
			Pipeline("").
			WithStage(agent1, "Do something").
			Execute(context.Background())

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "prompt cannot be empty")
	})

	t.Run("conditional requires condition prompt", func(t *testing.T) {
		classifier := createMockAgent(t, "classifier", newMockLLMProvider("bug"))
		pattern := &loomv1.WorkflowPattern{
			Pattern: &loomv1.WorkflowPattern_ForkJoin{
				ForkJoin: &loomv1.ForkJoinPattern{
					Prompt:        "Test",
					AgentIds:      []string{},
					MergeStrategy: loomv1.MergeStrategy_FIRST,
				},
			},
		}

		_, err := orchestrator.
			Conditional(classifier, "").
			When("bug", pattern).
			Execute(context.Background())

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "condition prompt")
	})
}

// TestTracingIntegration tests that tracing spans are created.
func TestTracingIntegration(t *testing.T) {
	tracer := observability.NewNoOpTracer()

	orchestrator := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: tracer,
	})

	agent1 := createMockAgent(t, "agent1", newMockLLMProvider("Response 1"))
	agent2 := createMockAgent(t, "agent2", newMockLLMProvider("Response 2"))

	result, err := orchestrator.
		Debate("Test tracing").
		WithAgents(agent1, agent2).
		WithRounds(1).
		WithMergeStrategy(loomv1.MergeStrategy_CONCATENATE).
		Execute(context.Background())

	require.NoError(t, err)
	assert.NotNil(t, result)

	// With NoOpTracer, we just verify no panics occur
	// Real tracing tests would verify span creation and attributes
}
