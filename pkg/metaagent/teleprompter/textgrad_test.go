// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package teleprompter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

func TestNewJudgeGradientEngine(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
		})

		require.NoError(t, err)
		assert.NotNil(t, engine)
		assert.Equal(t, []string{"test-judge"}, engine.judgeIDs)
		assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE, engine.aggregation)
	})

	t.Run("nil config", func(t *testing.T) {
		_, err := NewJudgeGradientEngine(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config required")
	})

	t.Run("nil orchestrator", func(t *testing.T) {
		_, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			JudgeIDs: []string{"test-judge"},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "orchestrator required")
	})

	t.Run("empty judge IDs", func(t *testing.T) {
		_, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "at least one judge ID required")
	})

	t.Run("custom aggregation", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
			Aggregation:  loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
		})

		require.NoError(t, err)
		assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS, engine.aggregation)
	})

	t.Run("default tracer and logger", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
		})

		require.NoError(t, err)
		assert.NotNil(t, engine.tracer)
		assert.NotNil(t, engine.logger)
	})
}

func TestJudgeGradientEngine_Backward(t *testing.T) {
	t.Run("success - stores gradient in variables", func(t *testing.T) {
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				return &loomv1.EvaluateResponse{
					Passed:     false,
					FinalScore: 72.5,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "quality-judge",
							JudgeName:    "Quality Judge",
							Verdict:      "PARTIAL_PASS",
							OverallScore: 75.0,
							DimensionScores: map[string]float64{
								"quality": 75.0,
								"safety":  65.0,
							},
							Reasoning: "Quality is acceptable but safety needs improvement",
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"quality-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "system_prompt", Value: "You are a helpful assistant"},
		}

		example := &loomv1.Example{
			Inputs:  map[string]string{"query": "test query"},
			Outputs: map[string]string{"answer": "expected answer"},
		}

		result := &ExecutionResult{
			Outputs: map[string]string{"answer": "actual answer"},
			TraceID: "test-trace-123",
			Success: true,
		}

		err = engine.Backward(context.Background(), example, result, variables)
		require.NoError(t, err)

		// Verify gradient was stored
		assert.NotEmpty(t, variables[0].Gradient)
		assert.Contains(t, variables[0].Gradient, "quality: 75/100")
		assert.Contains(t, variables[0].Gradient, "safety: 65/100")
		assert.Contains(t, variables[0].Gradient, "Overall Score: 72.5/100")
	})

	t.Run("evaluation failure", func(t *testing.T) {
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				return nil, assert.AnError
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{{Name: "test", Value: "value"}}
		example := &loomv1.Example{Inputs: map[string]string{"query": "test"}}
		result := &ExecutionResult{Outputs: map[string]string{"answer": "test"}}

		err = engine.Backward(context.Background(), example, result, variables)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "judge evaluation failed")
	})

	t.Run("multiple variables get same gradient", func(t *testing.T) {
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				return &loomv1.EvaluateResponse{
					Passed:     true,
					FinalScore: 90.0,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "test-judge",
							OverallScore: 90.0,
							DimensionScores: map[string]float64{
								"quality": 90.0,
							},
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "var1", Value: "value1"},
			{Name: "var2", Value: "value2"},
			{Name: "var3", Value: "value3"},
		}

		example := &loomv1.Example{Inputs: map[string]string{"query": "test"}}
		result := &ExecutionResult{Outputs: map[string]string{"answer": "test"}}

		err = engine.Backward(context.Background(), example, result, variables)
		require.NoError(t, err)

		// All variables should have the same gradient
		assert.NotEmpty(t, variables[0].Gradient)
		for i := 1; i < len(variables); i++ {
			assert.Equal(t, variables[0].Gradient, variables[i].Gradient)
		}
	})
}

func TestJudgeGradientEngine_Step(t *testing.T) {
	t.Run("success - generates improvements", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{
				Name:  "system_prompt",
				Value: "You are a helpful assistant",
				Gradient: `[Dimension Scores]
quality: 75/100
safety: 65/100 ⚠️
cost: 80/100

Overall Score: 73.3/100 ✗

[Suggestions]
- safety (65/100): Add input validation to prevent SQL injection
- quality (75/100): Improve error handling in generated queries`,
			},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.NotEmpty(t, improvements)

		// Should generate improvements for failing dimensions (safety, quality)
		assert.GreaterOrEqual(t, len(improvements), 2)

		// Check for safety improvement
		var hasSafety bool
		for _, imp := range improvements {
			if strings.Contains(imp.Description, "safety") {
				hasSafety = true
				assert.Equal(t, loomv1.ImpactLevel_IMPACT_CRITICAL, imp.Impact)
				assert.Equal(t, loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE, imp.Type)
			}
		}
		assert.True(t, hasSafety, "Expected safety improvement")
	})

	t.Run("no gradient - no improvements", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{Name: "test", Value: "value", Gradient: ""}, // No gradient
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.Empty(t, improvements)
	})

	t.Run("high scores - no improvements", func(t *testing.T) {
		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: &MockOrchestrator{},
			JudgeIDs:     []string{"test-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		variables := []*Variable{
			{
				Name:  "test",
				Value: "value",
				Gradient: `[Dimension Scores]
quality: 95/100 ✓
safety: 98/100 ✓
cost: 92/100 ✓

Overall Score: 95.0/100 ✓`,
			},
		}

		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)
		assert.Empty(t, improvements) // All scores above threshold
	})
}

func TestJudgeGradientEngine_formatGradient(t *testing.T) {
	engine, _ := NewJudgeGradientEngine(&JudgeGradientConfig{
		Orchestrator: &MockOrchestrator{},
		JudgeIDs:     []string{"test-judge"},
	})

	t.Run("single verdict", func(t *testing.T) {
		evalResp := &loomv1.EvaluateResponse{
			Passed:     false,
			FinalScore: 75.0,
			Verdicts: []*loomv1.JudgeResult{
				{
					DimensionScores: map[string]float64{
						"quality": 75.0,
						"safety":  65.0,
					},
					Reasoning: "Safety needs improvement",
				},
			},
		}

		gradient := engine.formatGradient(evalResp)

		assert.Contains(t, gradient, "[Dimension Scores]")
		assert.Contains(t, gradient, "quality: 75/100")
		assert.Contains(t, gradient, "safety: 65/100")
		assert.Contains(t, gradient, "⚠️") // Warning for low safety score
		assert.Contains(t, gradient, "Overall Score: 75.0/100")
		assert.Contains(t, gradient, "✗") // Failed
		assert.Contains(t, gradient, "[Suggestions]")
		assert.Contains(t, gradient, "Safety needs improvement")
	})

	t.Run("multiple verdicts - averages scores", func(t *testing.T) {
		evalResp := &loomv1.EvaluateResponse{
			Passed:     true,
			FinalScore: 85.0,
			Verdicts: []*loomv1.JudgeResult{
				{
					DimensionScores: map[string]float64{
						"quality": 80.0,
					},
				},
				{
					DimensionScores: map[string]float64{
						"quality": 90.0,
					},
				},
			},
		}

		gradient := engine.formatGradient(evalResp)

		// Average of 80 and 90 is 85
		assert.Contains(t, gradient, "quality: 85/100")
	})

	t.Run("high scores - shows checkmark", func(t *testing.T) {
		evalResp := &loomv1.EvaluateResponse{
			Passed:     true,
			FinalScore: 95.0,
			Verdicts: []*loomv1.JudgeResult{
				{
					DimensionScores: map[string]float64{
						"quality": 95.0,
					},
				},
			},
		}

		gradient := engine.formatGradient(evalResp)

		assert.Contains(t, gradient, "quality: 95/100 ✓")
		assert.Contains(t, gradient, "Overall Score: 95.0/100 ✓")
	})
}

func TestJudgeGradientEngine_parseGradientScores(t *testing.T) {
	engine, _ := NewJudgeGradientEngine(&JudgeGradientConfig{
		Orchestrator: &MockOrchestrator{},
		JudgeIDs:     []string{"test-judge"},
	})

	t.Run("parses dimension scores correctly", func(t *testing.T) {
		gradient := `[Dimension Scores]
quality: 75/100
safety: 65/100 ⚠️
cost: 80/100

Overall Score: 73.3/100 ✗`

		scores := engine.parseGradientScores(gradient)

		assert.Equal(t, 75.0, scores["quality"])
		assert.Equal(t, 65.0, scores["safety"])
		assert.Equal(t, 80.0, scores["cost"])
		assert.NotContains(t, scores, "Overall Score") // Should not parse overall score
	})

	t.Run("empty gradient", func(t *testing.T) {
		scores := engine.parseGradientScores("")
		assert.Empty(t, scores)
	})

	t.Run("malformed gradient", func(t *testing.T) {
		gradient := "random text with no scores"
		scores := engine.parseGradientScores(gradient)
		assert.Empty(t, scores)
	})
}

func TestJudgeGradientEngine_generateImprovementForDimension(t *testing.T) {
	engine, _ := NewJudgeGradientEngine(&JudgeGradientConfig{
		Orchestrator: &MockOrchestrator{},
		JudgeIDs:     []string{"test-judge"},
	})

	variable := &Variable{
		Name:  "system_prompt",
		Value: "You are a helpful assistant",
	}

	t.Run("safety below threshold", func(t *testing.T) {
		improvement := engine.generateImprovementForDimension(variable, "safety", 65.0)

		require.NotNil(t, improvement)
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_CRITICAL, improvement.Impact)
		assert.Equal(t, loomv1.ImprovementType_IMPROVEMENT_PARAMETER_TUNE, improvement.Type)
		assert.Contains(t, improvement.Description, "safety")
		assert.Contains(t, improvement.Description, "65.0%")
		assert.NotNil(t, improvement.Details)
		assert.Greater(t, improvement.Details.ExpectedSuccessRateDelta, 0.0)
	})

	t.Run("quality below threshold", func(t *testing.T) {
		improvement := engine.generateImprovementForDimension(variable, "quality", 75.0)

		require.NotNil(t, improvement)
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_HIGH, improvement.Impact)
		assert.Equal(t, loomv1.ImprovementType_IMPROVEMENT_TEMPLATE_ADJUST, improvement.Type)
		assert.Contains(t, improvement.Description, "quality")
	})

	t.Run("cost below threshold", func(t *testing.T) {
		improvement := engine.generateImprovementForDimension(variable, "cost", 70.0)

		require.NotNil(t, improvement)
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_MEDIUM, improvement.Impact)
		assert.Contains(t, improvement.Description, "cost")
	})

	t.Run("domain below threshold", func(t *testing.T) {
		improvement := engine.generateImprovementForDimension(variable, "domain", 70.0)

		require.NotNil(t, improvement)
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_HIGH, improvement.Impact)
		assert.Equal(t, loomv1.ImprovementType_IMPROVEMENT_TEMPLATE_ADJUST, improvement.Type)
		assert.Contains(t, improvement.Description, "domain")
	})

	t.Run("score above threshold - no improvement", func(t *testing.T) {
		improvement := engine.generateImprovementForDimension(variable, "safety", 85.0)
		assert.Nil(t, improvement) // Above threshold (70.0)
	})

	t.Run("unknown dimension", func(t *testing.T) {
		improvement := engine.generateImprovementForDimension(variable, "unknown_dimension", 60.0)

		require.NotNil(t, improvement)
		assert.Equal(t, loomv1.ImpactLevel_IMPACT_MEDIUM, improvement.Impact)
		assert.Contains(t, improvement.Description, "unknown_dimension")
	})
}

func TestJudgeGradientEngine_Integration(t *testing.T) {
	t.Run("complete backward-step cycle", func(t *testing.T) {
		// Mock orchestrator that returns judge verdicts
		mockOrch := &MockOrchestrator{
			evaluateFunc: func(ctx context.Context, req *loomv1.EvaluateRequest) (*loomv1.EvaluateResponse, error) {
				return &loomv1.EvaluateResponse{
					Passed:     false,
					FinalScore: 70.0,
					Verdicts: []*loomv1.JudgeResult{
						{
							JudgeId:      "quality-judge",
							JudgeName:    "Quality Judge",
							Verdict:      "FAIL",
							OverallScore: 70.0,
							DimensionScores: map[string]float64{
								"quality": 70.0, // Below 80% threshold
								"safety":  60.0, // Below 70% threshold
							},
							Reasoning: "Quality and safety need improvement",
						},
					},
				}, nil
			},
		}

		engine, err := NewJudgeGradientEngine(&JudgeGradientConfig{
			Orchestrator: mockOrch,
			JudgeIDs:     []string{"quality-judge"},
			Tracer:       observability.NewNoOpTracer(),
		})
		require.NoError(t, err)

		// Create variables
		variables := []*Variable{
			{Name: "system_prompt", Value: "You are a helpful assistant"},
		}

		// Create example and result
		example := &loomv1.Example{
			Inputs:  map[string]string{"query": "Generate SQL query"},
			Outputs: map[string]string{"answer": "SELECT * FROM users"},
		}

		result := &ExecutionResult{
			Outputs: map[string]string{"answer": "SELECT * FROM users WHERE id = 1"},
			TraceID: "test-trace-123",
			Success: true,
		}

		// Backward pass
		err = engine.Backward(context.Background(), example, result, variables)
		require.NoError(t, err)

		// Verify gradient was set
		assert.NotEmpty(t, variables[0].Gradient)
		assert.Contains(t, variables[0].Gradient, "quality: 70/100")
		assert.Contains(t, variables[0].Gradient, "safety: 60/100")

		// Step
		improvements, err := engine.Step(context.Background(), variables)
		require.NoError(t, err)

		// Should have improvements for both failing dimensions
		assert.GreaterOrEqual(t, len(improvements), 2)

		// Verify improvements contain dimension-specific fixes
		var hasSafety, hasQuality bool
		for _, imp := range improvements {
			if strings.Contains(imp.Description, "safety") {
				hasSafety = true
				assert.Equal(t, loomv1.ImpactLevel_IMPACT_CRITICAL, imp.Impact)
			}
			if strings.Contains(imp.Description, "quality") {
				hasQuality = true
				assert.Equal(t, loomv1.ImpactLevel_IMPACT_HIGH, imp.Impact)
			}
		}
		assert.True(t, hasSafety && hasQuality, "Expected both safety and quality improvements")
	})
}

func TestHelperFunctions(t *testing.T) {
	t.Run("extractQuery", func(t *testing.T) {
		example := &loomv1.Example{
			Inputs: map[string]string{
				"query": "test query",
				"other": "other value",
			},
		}
		query := extractQuery(example)
		assert.Equal(t, "test query", query)
	})

	t.Run("extractQuery - concatenates if no standard field", func(t *testing.T) {
		example := &loomv1.Example{
			Inputs: map[string]string{
				"field1": "value1",
				"field2": "value2",
			},
		}
		query := extractQuery(example)
		assert.Contains(t, query, "field1: value1")
		assert.Contains(t, query, "field2: value2")
	})

	t.Run("extractResponse", func(t *testing.T) {
		result := &ExecutionResult{
			Outputs: map[string]string{
				"answer": "test answer",
				"other":  "other value",
			},
		}
		response := extractResponse(result)
		assert.Equal(t, "test answer", response)
	})

	t.Run("buildMetadata", func(t *testing.T) {
		example := &loomv1.Example{
			Metadata: map[string]string{"key": "value"},
			Outputs:  map[string]string{"answer": "expected"},
		}
		result := &ExecutionResult{
			TraceID:   "trace-123",
			Success:   true,
			Rationale: "test rationale",
		}

		metadata := buildMetadata(example, result)

		assert.Equal(t, "value", metadata["key"])
		assert.Equal(t, "trace-123", metadata["trace_id"])
		assert.Equal(t, "true", metadata["success"])
		assert.Equal(t, "test rationale", metadata["rationale"])
		assert.Contains(t, metadata["expected_output"], "expected")
	})

	t.Run("average", func(t *testing.T) {
		assert.Equal(t, 0.0, average([]float64{}))
		assert.Equal(t, 5.0, average([]float64{5.0}))
		assert.Equal(t, 7.5, average([]float64{5.0, 10.0}))
		assert.Equal(t, 6.0, average([]float64{4.0, 6.0, 8.0}))
	})

	t.Run("min", func(t *testing.T) {
		assert.Equal(t, 1.0, min(1.0, 2.0))
		assert.Equal(t, 1.0, min(2.0, 1.0))
		assert.Equal(t, 5.0, min(5.0, 5.0))
		assert.Equal(t, -1.0, min(-1.0, 0.0))
	})
}
