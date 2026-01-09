// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package teleprompter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// MockOrchestrator implements a mock judge orchestrator for testing
type MockOrchestrator struct {
	evaluateFunc func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error)
}

func (m *MockOrchestrator) Evaluate(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
	if m.evaluateFunc != nil {
		return m.evaluateFunc(ctx, req)
	}

	// Default: return passing evaluation
	return &loomv1.EvaluateResponse{
		Passed:     true,
		FinalScore: 85.0,
		Verdicts: []*loomv1.JudgeResult{
			{
				JudgeId:      "test-judge-1",
				JudgeName:    "Quality Judge",
				Verdict:      "PASS",
				OverallScore: 85.0,
				DimensionScores: map[string]float64{
					"quality": 85.0,
					"safety":  90.0,
				},
			},
		},
		Aggregated: &loomv1.AggregatedJudgeMetrics{
			WeightedAverageScore: 85.0,
		},
	}, nil
}

func TestNewMultiJudgeMetric(t *testing.T) {
	tests := []struct {
		name        string
		config      *MultiJudgeMetricConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: &MultiJudgeMetricConfig{
				Orchestrator: &MockOrchestrator{},
				JudgeIDs:     []string{"judge-1", "judge-2"},
				Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
				MinThreshold: 80.0,
			},
			expectError: false,
		},
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
			errorMsg:    "config required",
		},
		{
			name: "missing orchestrator",
			config: &MultiJudgeMetricConfig{
				JudgeIDs: []string{"judge-1"},
			},
			expectError: true,
			errorMsg:    "orchestrator required",
		},
		{
			name: "missing judge IDs",
			config: &MultiJudgeMetricConfig{
				Orchestrator: &MockOrchestrator{},
				JudgeIDs:     []string{},
			},
			expectError: true,
			errorMsg:    "at least one judge ID required",
		},
		{
			name: "defaults applied",
			config: &MultiJudgeMetricConfig{
				Orchestrator: &MockOrchestrator{},
				JudgeIDs:     []string{"judge-1"},
				// Omit aggregation and threshold - should get defaults
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metric, err := NewMultiJudgeMetric(tt.config)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, metric)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, metric)

				// Verify defaults
				if tt.config.Aggregation == loomv1.AggregationStrategy_AGGREGATION_STRATEGY_UNSPECIFIED {
					assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE, metric.aggregation)
				}
				if tt.config.MinThreshold == 0 {
					assert.Equal(t, 80.0, metric.minThreshold)
				}
			}
		})
	}
}

func TestNewMultiJudgeMetricFromProto(t *testing.T) {
	orchestrator := &MockOrchestrator{}

	protoConfig := &loomv1.MultiJudgeMetricConfig{
		JudgeIds:    []string{"judge-1", "judge-2"},
		Aggregation: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		TargetDimensions: []loomv1.JudgeDimension{
			loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY,
			loomv1.JudgeDimension_JUDGE_DIMENSION_SAFETY,
		},
		DimensionWeights: map[string]float64{
			"quality": 2.0,
			"safety":  3.0,
		},
		MinThreshold: 75.0,
		ExportToHawk: true,
	}

	metric, err := NewMultiJudgeMetricFromProto(
		orchestrator,
		protoConfig,
		observability.NewNoOpTracer(),
		zap.NewNop(),
	)

	require.NoError(t, err)
	assert.NotNil(t, metric)
	assert.Equal(t, 2, len(metric.judgeIDs))
	assert.Equal(t, 2, len(metric.targetDimensions))
	assert.Equal(t, 2, len(metric.dimensionWeights))
	assert.Equal(t, 75.0, metric.minThreshold)
	assert.True(t, metric.exportToHawk)
}

func TestMultiJudgeMetric_Evaluate(t *testing.T) {
	tests := []struct {
		name          string
		example       *loomv1.Example
		result        *ExecutionResult
		mockResponse  *loomv1.EvaluateResponse
		mockError     error
		expectedScore float64
		expectError   bool
	}{
		{
			name: "successful evaluation - high score",
			example: &loomv1.Example{
				Id: "example-1",
				Inputs: map[string]string{
					"query": "What is 2+2?",
				},
				Outputs: map[string]string{
					"answer": "4",
				},
			},
			result: &ExecutionResult{
				Outputs: map[string]string{
					"answer": "4",
				},
				TraceID: "trace-123",
				Success: true,
			},
			mockResponse: &loomv1.EvaluateResponse{
				Passed:     true,
				FinalScore: 95.0,
				Verdicts: []*loomv1.JudgeResult{
					{
						JudgeId:      "quality-judge",
						JudgeName:    "Quality Judge",
						Verdict:      "PASS",
						OverallScore: 95.0,
						DimensionScores: map[string]float64{
							"quality": 95.0,
						},
					},
				},
				Aggregated: &loomv1.AggregatedJudgeMetrics{
					WeightedAverageScore: 95.0,
				},
			},
			expectedScore: 0.95, // Normalized to [0, 1]
			expectError:   false,
		},
		{
			name: "successful evaluation - low score",
			example: &loomv1.Example{
				Id: "example-2",
				Inputs: map[string]string{
					"query": "What is the capital of France?",
				},
				Outputs: map[string]string{
					"answer": "Paris",
				},
			},
			result: &ExecutionResult{
				Outputs: map[string]string{
					"answer": "London", // Wrong answer
				},
				TraceID: "trace-456",
				Success: true,
			},
			mockResponse: &loomv1.EvaluateResponse{
				Passed:     false,
				FinalScore: 25.0,
				Verdicts: []*loomv1.JudgeResult{
					{
						JudgeId:      "quality-judge",
						JudgeName:    "Quality Judge",
						Verdict:      "FAIL",
						OverallScore: 25.0,
						DimensionScores: map[string]float64{
							"quality": 25.0,
						},
					},
				},
				Aggregated: &loomv1.AggregatedJudgeMetrics{
					WeightedAverageScore: 25.0,
				},
			},
			expectedScore: 0.25,
			expectError:   false,
		},
		{
			name: "evaluation with dimension weights",
			example: &loomv1.Example{
				Id: "example-3",
				Inputs: map[string]string{
					"query": "Test query",
				},
				Outputs: map[string]string{
					"answer": "Test answer",
				},
			},
			result: &ExecutionResult{
				Outputs: map[string]string{
					"answer": "Test response",
				},
				TraceID: "trace-789",
				Success: true,
			},
			mockResponse: &loomv1.EvaluateResponse{
				Passed:     true,
				FinalScore: 80.0,
				Verdicts: []*loomv1.JudgeResult{
					{
						JudgeId:      "multi-dim-judge",
						JudgeName:    "Multi-Dimensional Judge",
						Verdict:      "PASS",
						OverallScore: 80.0,
						DimensionScores: map[string]float64{
							"quality": 70.0,
							"safety":  90.0,
							"cost":    85.0,
						},
					},
				},
				Aggregated: &loomv1.AggregatedJudgeMetrics{
					WeightedAverageScore: 80.0,
				},
			},
			expectedScore: 0.80, // Will use aggregated score (no dimension weights configured in this test)
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOrch := &MockOrchestrator{
				evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
					if tt.mockError != nil {
						return nil, tt.mockError
					}
					return tt.mockResponse, nil
				},
			}

			metric, err := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
				Orchestrator: mockOrch,
				JudgeIDs:     []string{"test-judge"},
				Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
				MinThreshold: 80.0,
				Tracer:       observability.NewNoOpTracer(),
				Logger:       zap.NewNop(),
			})
			require.NoError(t, err)

			score, err := metric.Evaluate(context.Background(), tt.example, tt.result)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.InDelta(t, tt.expectedScore, score, 0.01, "Score should match expected value")
			}
		})
	}
}

func TestMultiJudgeMetric_CalculateWeightedScore(t *testing.T) {
	tests := []struct {
		name             string
		dimensionWeights map[string]float64
		response         *loomv1.EvaluateResponse
		expectedScore    float64
	}{
		{
			name:             "no dimension weights - use aggregated score",
			dimensionWeights: nil,
			response: &loomv1.EvaluateResponse{
				FinalScore: 85.0,
				Aggregated: &loomv1.AggregatedJudgeMetrics{
					WeightedAverageScore: 87.5,
				},
				Verdicts: []*loomv1.JudgeResult{
					{OverallScore: 85.0},
				},
			},
			expectedScore: 87.5,
		},
		{
			name: "with dimension weights - quality and safety",
			dimensionWeights: map[string]float64{
				"quality": 2.0,
				"safety":  3.0,
			},
			response: &loomv1.EvaluateResponse{
				Verdicts: []*loomv1.JudgeResult{
					{
						DimensionScores: map[string]float64{
							"quality": 80.0, // weight 2.0 -> 160
							"safety":  90.0, // weight 3.0 -> 270
						},
					},
				},
			},
			// (80*2 + 90*3) / (2+3) = 430 / 5 = 86
			expectedScore: 86.0,
		},
		{
			name: "multiple verdicts with dimension weights",
			dimensionWeights: map[string]float64{
				"quality": 1.0,
				"cost":    1.0,
			},
			response: &loomv1.EvaluateResponse{
				Verdicts: []*loomv1.JudgeResult{
					{
						DimensionScores: map[string]float64{
							"quality": 80.0,
							"cost":    70.0,
						},
					},
					{
						DimensionScores: map[string]float64{
							"quality": 90.0,
							"cost":    80.0,
						},
					},
				},
			},
			// (80*1 + 70*1 + 90*1 + 80*1) / (1+1+1+1) = 320 / 4 = 80
			expectedScore: 80.0,
		},
		{
			name: "dimension weights but no matching dimension scores - fallback to overall",
			dimensionWeights: map[string]float64{
				"nonexistent": 1.0,
			},
			response: &loomv1.EvaluateResponse{
				Verdicts: []*loomv1.JudgeResult{
					{
						OverallScore: 75.0,
						DimensionScores: map[string]float64{
							"quality": 80.0,
						},
					},
					{
						OverallScore: 85.0,
						DimensionScores: map[string]float64{
							"safety": 90.0,
						},
					},
				},
			},
			// Fall back to overall scores: (75 + 85) / 2 = 80
			expectedScore: 80.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metric := &MultiJudgeMetric{
				dimensionWeights: tt.dimensionWeights,
			}

			score := metric.calculateWeightedScore(tt.response)
			assert.InDelta(t, tt.expectedScore, score, 0.01, "Score should match expected value")
		})
	}
}

func TestMultiJudgeMetric_Type(t *testing.T) {
	metric := &MultiJudgeMetric{}
	assert.Equal(t, loomv1.MetricType_METRIC_MULTI_JUDGE, metric.Type())
}

func TestMultiJudgeMetric_Name(t *testing.T) {
	metric := &MultiJudgeMetric{
		judgeIDs:    []string{"judge-1", "judge-2", "judge-3"},
		aggregation: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
	}

	name := metric.Name()
	assert.Contains(t, name, "MultiJudge")
	assert.Contains(t, name, "3 judges")
	assert.Contains(t, name, "WEIGHTED_AVERAGE")
}

func TestExactMatchMetric_Evaluate(t *testing.T) {
	metric := NewExactMatchMetric()

	tests := []struct {
		name          string
		example       *loomv1.Example
		result        *ExecutionResult
		expectedScore float64
	}{
		{
			name: "exact match",
			example: &loomv1.Example{
				Outputs: map[string]string{
					"answer": "Paris",
				},
			},
			result: &ExecutionResult{
				Outputs: map[string]string{
					"answer": "Paris",
				},
			},
			expectedScore: 1.0,
		},
		{
			name: "no match",
			example: &loomv1.Example{
				Outputs: map[string]string{
					"answer": "Paris",
				},
			},
			result: &ExecutionResult{
				Outputs: map[string]string{
					"answer": "London",
				},
			},
			expectedScore: 0.0,
		},
		{
			name: "case sensitive - no match",
			example: &loomv1.Example{
				Outputs: map[string]string{
					"answer": "paris",
				},
			},
			result: &ExecutionResult{
				Outputs: map[string]string{
					"answer": "Paris",
				},
			},
			expectedScore: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, err := metric.Evaluate(context.Background(), tt.example, tt.result)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedScore, score)
		})
	}
}

func TestExactMatchMetric_Type(t *testing.T) {
	metric := NewExactMatchMetric()
	assert.Equal(t, loomv1.MetricType_METRIC_EXACT_MATCH, metric.Type())
}

func TestExactMatchMetric_Name(t *testing.T) {
	metric := NewExactMatchMetric()
	assert.Equal(t, "ExactMatch", metric.Name())
}

// TestMultiJudgeMetric_Integration tests the metric with a more realistic orchestrator
func TestMultiJudgeMetric_Integration(t *testing.T) {
	t.Skip("Skipping integration test - requires full judge infrastructure")

	// This test would require:
	// 1. Real judge registry with registered judges
	// 2. Real LLM provider (or mock)
	// 3. Full orchestrator setup
	//
	// Example structure:
	// registry := judges.NewRegistry()
	// registry.RegisterJudge(qualityJudge)
	// registry.RegisterJudge(safetyJudge)
	//
	// aggregator := judges.NewAggregator(nil)
	// orchestrator := judges.NewOrchestrator(&judges.Config{
	//     Registry:   registry,
	//     Aggregator: aggregator,
	// })
	//
	// metric := NewMultiJudgeMetric(&MultiJudgeMetricConfig{
	//     Orchestrator: orchestrator,
	//     JudgeIDs:     []string{"quality-judge", "safety-judge"},
	// })
	//
	// example := &loomv1.Example{...}
	// result := &ExecutionResult{...}
	//
	// score, err := metric.Evaluate(ctx, example, result)
	// assert.NoError(t, err)
	// assert.Greater(t, score, 0.0)
}
