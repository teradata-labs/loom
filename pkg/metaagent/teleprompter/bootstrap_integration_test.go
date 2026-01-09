// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package teleprompter

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestBootstrapFewShot_WithMultiJudgeMetric demonstrates Phase 3 integration:
// Using MultiJudgeMetric with BootstrapFewShot for multi-dimensional demonstration selection
func TestBootstrapFewShot_WithMultiJudgeMetric(t *testing.T) {
	t.Run("successful compilation with dimension-aware selection", func(t *testing.T) {
		// Create mock orchestrator that returns dimension scores
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				// Return varying scores based on input (simulating different quality levels)
				example := req.Context.Prompt

				var qualityScore, safetyScore, costScore float64
				if example == "good-example" {
					qualityScore = 95.0
					safetyScore = 90.0
					costScore = 85.0
				} else if example == "ok-example" {
					qualityScore = 75.0
					safetyScore = 80.0
					costScore = 90.0
				} else {
					qualityScore = 60.0
					safetyScore = 55.0
					costScore = 70.0
				}

				avgScore := (qualityScore + safetyScore + costScore) / 3.0

				return &loomv1.EvaluateResponse{
					Passed:     avgScore >= 70.0,
					FinalScore: avgScore,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "quality-judge",
							JudgeName:    "Quality Judge",
							Verdict:      "PASS",
							OverallScore: avgScore,
							DimensionScores: map[string]float64{
								"quality": qualityScore,
								"safety":  safetyScore,
								"cost":    costScore,
							},
							Reasoning: fmt.Sprintf("Example quality: %.0f%%", qualityScore),
						},
					},
				}, nil
			},
		}

		// Create MultiJudgeMetric
		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"quality-judge"},
			Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		// Create mock agent
		agent := &MockAgent{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				query := inputs["query"]
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "generated answer for " + query},
					TraceID: "trace-" + query,
					Success: true,
				}, nil
			},
		}

		// Create trainset with varying quality
		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "good-example"},
				Outputs: map[string]string{"answer": "expected answer 1"},
			},
			{
				Id:      "ex2",
				Inputs:  map[string]string{"query": "ok-example"},
				Outputs: map[string]string{"answer": "expected answer 2"},
			},
			{
				Id:      "ex3",
				Inputs:  map[string]string{"query": "bad-example"},
				Outputs: map[string]string{"answer": "expected answer 3"},
			},
		}

		// Create BootstrapFewShot teleprompter
		registry := NewRegistry()
		bootstrap := NewBootstrapFewShot(observability.NewNoOpTracer(), registry)

		// Run compilation
		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7, // 70% threshold
			MaxRounds:            1,
		}

		result, err := bootstrap.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		// Verify compilation succeeded
		require.NoError(t, err)
		require.NotNil(t, result)

		// Should select 2 demonstrations (ex1 and ex2, not ex3 which is below threshold)
		assert.Equal(t, 2, len(result.Demonstrations))
		assert.Equal(t, int32(2), result.SuccessfulTraces)

		// Verify trainset score is reasonable
		assert.Greater(t, result.TrainsetScore, 0.0)

		t.Logf("Compilation successful: %d demonstrations selected", len(result.Demonstrations))
		t.Logf("Trainset score: %.4f", result.TrainsetScore)
	})

	t.Run("dimension scores captured in execution traces", func(t *testing.T) {
		// Create mock orchestrator
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				return &loomv1.EvaluateResponse{
					Passed:     true,
					FinalScore: 85.0,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "test-judge",
							OverallScore: 85.0,
							DimensionScores: map[string]float64{
								"quality": 90.0,
								"safety":  80.0,
								"cost":    85.0,
							},
						},
					},
				}, nil
			},
		}

		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		// Create base teleprompter to test RunOnTrainset directly
		bt := NewBaseTeleprompter(observability.NewNoOpTracer(), NewRegistry())

		agent := &MockAgent{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "test answer"},
					TraceID: "trace-123",
					Success: true,
				}, nil
			},
		}

		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "test"},
				Outputs: map[string]string{"answer": "expected"},
			},
		}

		// Run on trainset
		traces, err := bt.RunOnTrainset(context.Background(), agent, trainset, metric, 0.7)
		require.NoError(t, err)
		require.Len(t, traces, 1)

		// Verify dimension scores were captured
		trace := traces[0]
		assert.NotNil(t, trace.DimensionScores, "Dimension scores should be populated")
		assert.Equal(t, 90.0, trace.DimensionScores["quality"])
		assert.Equal(t, 80.0, trace.DimensionScores["safety"])
		assert.Equal(t, 85.0, trace.DimensionScores["cost"])

		// Verify judge verdicts were captured
		assert.NotNil(t, trace.JudgeVerdicts, "Judge verdicts should be populated")
		assert.Len(t, trace.JudgeVerdicts, 1)

		t.Logf("Dimension scores captured: %v", trace.DimensionScores)
	})

	t.Run("quality vs cost trade-off demonstration", func(t *testing.T) {
		// Demonstrate selecting demonstrations with different dimension priorities
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				query := req.Context.Prompt

				var qualityScore, costScore float64
				// "high-quality" example: great quality, expensive
				// "low-cost" example: ok quality, cheap
				if query == "high-quality" {
					qualityScore = 95.0
					costScore = 60.0 // Expensive (low cost score)
				} else if query == "low-cost" {
					qualityScore = 75.0
					costScore = 95.0 // Cheap (high cost score)
				} else {
					qualityScore = 80.0
					costScore = 80.0
				}

				avgScore := (qualityScore + costScore) / 2.0

				return &loomv1.EvaluateResponse{
					Passed:     avgScore >= 70.0,
					FinalScore: avgScore,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "multi-dim-judge",
							OverallScore: avgScore,
							DimensionScores: map[string]float64{
								"quality": qualityScore,
								"cost":    costScore,
							},
							Reasoning: fmt.Sprintf("Quality: %.0f%%, Cost: %.0f%%", qualityScore, costScore),
						},
					},
				}, nil
			},
		}

		// Create metric with weighted dimensions (quality > cost)
		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"multi-dim-judge"},
			DimensionWeights: map[string]float64{
				"quality": 2.0, // Quality weighted 2x
				"cost":    1.0,
			},
			Tracer: observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		agent := &MockAgent{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "answer"},
					TraceID: "trace-" + inputs["query"],
					Success: true,
				}, nil
			},
		}

		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "high-quality"},
				Outputs: map[string]string{"answer": "expected 1"},
			},
			{
				Id:      "ex2",
				Inputs:  map[string]string{"query": "low-cost"},
				Outputs: map[string]string{"answer": "expected 2"},
			},
		}

		// Run compilation
		registry := NewRegistry()
		bootstrap := NewBootstrapFewShot(observability.NewNoOpTracer(), registry)

		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7,
			MaxRounds:            1,
		}

		result, err := bootstrap.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		require.NoError(t, err)
		require.NotNil(t, result)

		// Both demonstrations should be selected (both above 70% threshold)
		assert.Equal(t, 2, len(result.Demonstrations))

		t.Logf("Selected %d demonstrations with quality-weighted metric", len(result.Demonstrations))
		t.Logf("Trainset score: %.4f", result.TrainsetScore)
	})

	t.Run("safety-first filtering", func(t *testing.T) {
		// Demonstrate that low safety scores filter out demonstrations
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				query := req.Context.Prompt

				var qualityScore, safetyScore float64
				if query == "safe-example" {
					qualityScore = 80.0
					safetyScore = 90.0
				} else {
					qualityScore = 95.0 // High quality
					safetyScore = 40.0  // But unsafe! (avg = 67.5 < 70%)
				}

				avgScore := (qualityScore + safetyScore) / 2.0

				return &loomv1.EvaluateResponse{
					Passed:     avgScore >= 70.0,
					FinalScore: avgScore,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "safety-judge",
							OverallScore: avgScore,
							DimensionScores: map[string]float64{
								"quality": qualityScore,
								"safety":  safetyScore,
							},
						},
					},
				}, nil
			},
		}

		metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"safety-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		agent := &MockAgent{
			id: "test-agent",
			runFunc: func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
				return &ExecutionResult{
					Inputs:  inputs,
					Outputs: map[string]string{"answer": "answer"},
					TraceID: "trace-" + inputs["query"],
					Success: true,
				}, nil
			},
		}

		trainset := []*loomv1.Example{
			{
				Id:      "ex1",
				Inputs:  map[string]string{"query": "safe-example"},
				Outputs: map[string]string{"answer": "expected 1"},
			},
			{
				Id:      "ex2",
				Inputs:  map[string]string{"query": "unsafe-example"},
				Outputs: map[string]string{"answer": "expected 2"},
			},
		}

		registry := NewRegistry()
		bootstrap := NewBootstrapFewShot(observability.NewNoOpTracer(), registry)

		config := &loomv1.TeleprompterConfig{
			MaxBootstrappedDemos: 2,
			MinConfidence:        0.7, // 70% threshold filters out unsafe example
			MaxRounds:            1,
		}

		result, err := bootstrap.Compile(context.Background(), &CompileRequest{
			AgentID:  "test-agent",
			Agent:    agent,
			Trainset: trainset,
			Metric:   metric,
			Config:   config,
		})

		require.NoError(t, err)
		require.NotNil(t, result)

		// Only safe example should be selected (unsafe filtered out by threshold)
		assert.Equal(t, 1, len(result.Demonstrations))
		assert.Equal(t, int32(1), result.SuccessfulTraces)

		t.Logf("Safety filtering: %d demonstrations selected (unsafe filtered out)", len(result.Demonstrations))
	})
}

// MockAgent implements the Agent interface for testing
type MockAgent struct {
	id      string
	runFunc func(ctx context.Context, inputs map[string]string) (*ExecutionResult, error)
}

func (m *MockAgent) Run(ctx context.Context, inputs map[string]string) (*ExecutionResult, error) {
	if m.runFunc != nil {
		return m.runFunc(ctx, inputs)
	}
	return &ExecutionResult{
		Inputs:  inputs,
		Outputs: map[string]string{"answer": "default answer"},
		TraceID: "trace-123",
		Success: true,
	}, nil
}

func (m *MockAgent) Clone() Agent {
	return &MockAgent{
		id:      m.id,
		runFunc: m.runFunc,
	}
}

func (m *MockAgent) GetMemory() Memory {
	return &MockMemory{}
}

func (m *MockAgent) GetID() string {
	return m.id
}

// MockMemory implements the Memory interface for testing
type MockMemory struct {
	prompts        map[string]string
	demonstrations []*loomv1.Demonstration
}

func (m *MockMemory) UpdateLearnedLayer(
	optimizedPrompts map[string]string,
	demonstrations []*loomv1.Demonstration,
) error {
	m.prompts = optimizedPrompts
	m.demonstrations = demonstrations
	return nil
}

func (m *MockMemory) GetLearnedLayer() (map[string]string, []*loomv1.Demonstration, error) {
	return m.prompts, m.demonstrations, nil
}

func (m *MockMemory) GetLearnedVersion() string {
	return "test-version"
}
