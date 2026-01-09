// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package learning

import (
	"context"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestCustomDimensionTuning tests Phase 8 custom dimension support in pattern tuning
func TestCustomDimensionTuning(t *testing.T) {
	tests := []struct {
		name                    string
		patterns                []*loomv1.PatternMetric
		dimensionWeights        map[string]float64
		expectedPriorityChanges map[string]int32 // pattern -> expected priority delta sign
	}{
		{
			name: "teradata compliance dimension",
			patterns: []*loomv1.PatternMetric{
				{
					PatternName: "sql_query_optimizer",
					Domain:      "teradata",
					TotalUsages: 100,
					SuccessRate: 0.85,
					JudgeCriterionScores: map[string]float64{
						"quality":             0.85,
						"cost":                0.80,
						"teradata_compliance": 0.95, // High compliance (0.95)
					},
				},
				{
					PatternName: "generic_sql_generator",
					Domain:      "teradata",
					TotalUsages: 100,
					SuccessRate: 0.75,
					JudgeCriterionScores: map[string]float64{
						"quality":             0.75,
						"cost":                0.85,
						"teradata_compliance": 0.30, // Low compliance (below 0.5 threshold)
					},
				},
			},
			dimensionWeights: map[string]float64{
				"teradata_compliance": 0.7, // Custom dimension heavily weighted
				"quality":             0.2,
				"cost":                0.1,
			},
			expectedPriorityChanges: map[string]int32{
				"sql_query_optimizer":   1,  // Should increase (high teradata_compliance: 0.95*0.7 + 0.85*0.2 + 0.80*0.1 = 0.915 > 0.5)
				"generic_sql_generator": -1, // Should decrease (low teradata_compliance: 0.30*0.7 + 0.75*0.2 + 0.85*0.1 = 0.445 < 0.5)
			},
		},
		{
			name: "mixed standard and custom dimensions",
			patterns: []*loomv1.PatternMetric{
				{
					PatternName: "hipaa_validator",
					Domain:      "healthcare",
					TotalUsages: 200,
					SuccessRate: 0.90,
					JudgeCriterionScores: map[string]float64{
						"quality":          0.90,
						"safety":           0.88,
						"hipaa_compliance": 0.95, // Custom dimension
						"cost":             0.70,
					},
				},
			},
			dimensionWeights: map[string]float64{
				"hipaa_compliance": 0.5, // Custom dimension
				"safety":           0.3, // Standard dimension
				"quality":          0.2, // Standard dimension
			},
			expectedPriorityChanges: map[string]int32{
				"hipaa_validator": 1, // Should increase (high scores)
			},
		},
		{
			name: "custom dimension with no patterns having it",
			patterns: []*loomv1.PatternMetric{
				{
					PatternName: "basic_pattern",
					Domain:      "test",
					TotalUsages: 100,
					SuccessRate: 0.80,
					JudgeCriterionScores: map[string]float64{
						"quality": 0.80,
					},
				},
			},
			dimensionWeights: map[string]float64{
				"nonexistent_dimension": 1.0, // This dimension doesn't exist in patterns
			},
			expectedPriorityChanges: map[string]int32{
				// No changes expected since fallback to equal weighting
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create learning agent
			la := &LearningAgent{
				tracer: observability.NewNoOpTracer(),
			}

			// Calculate tunings with custom dimensions
			tunings := la.calculatePatternTunings(
				tt.patterns,
				loomv1.TuningStrategy_TUNING_MODERATE,
				nil, // No optimization goal
				"",  // No pattern library path
				tt.dimensionWeights,
				nil, // No target dimensions
			)

			// Verify tunings match expectations
			tuningMap := make(map[string]*loomv1.PatternTuning)
			for _, tuning := range tunings {
				tuningMap[tuning.PatternName] = tuning
			}

			for patternName, expectedSign := range tt.expectedPriorityChanges {
				tuning, ok := tuningMap[patternName]
				if !ok {
					if expectedSign != 0 {
						t.Errorf("expected tuning for %q but got none", patternName)
					}
					continue
				}

				// Check priority adjustment sign
				if len(tuning.Adjustments) == 0 {
					t.Errorf("expected priority adjustment for %q but got none", patternName)
					continue
				}

				for _, adj := range tuning.Adjustments {
					if adj.ParameterName == "priority" {
						oldPriority := parseInt32(adj.OldValue)
						newPriority := parseInt32(adj.NewValue)
						actualSign := int32(0)
						if newPriority > oldPriority {
							actualSign = 1
						} else if newPriority < oldPriority {
							actualSign = -1
						}

						if actualSign != expectedSign {
							t.Errorf("pattern %q: expected priority change sign %d, got %d (old=%d, new=%d)",
								patternName, expectedSign, actualSign, oldPriority, newPriority)
						}
					}
				}
			}
		})
	}
}

// TestCustomDimensionInGenerateImprovements tests custom dimensions in improvement generation
func TestCustomDimensionInGenerateImprovements(t *testing.T) {
	patterns := []*loomv1.PatternMetric{
		{
			PatternName:   "teradata_query_pattern",
			Domain:        "teradata",
			TotalUsages:   100,
			SuccessRate:   0.85,
			JudgePassRate: 0.60, // Low pass rate
			JudgeCriterionScores: map[string]float64{
				"quality":             0.85,
				"teradata_compliance": 0.55, // Below threshold (0.75)
			},
		},
	}

	la := &LearningAgent{
		tracer: observability.NewNoOpTracer(),
	}

	// Generate improvements
	improvements := la.generateImprovementsWithJudgeFeedback(patterns)

	// Verify improvement generated for low teradata_compliance score
	foundTeradataImprovement := false
	for _, imp := range improvements {
		if imp.TargetPattern == "teradata_query_pattern" {
			// Check if improvement mentions teradata compliance or low judge pass rate
			if stringContains(imp.Description, "teradata") ||
				stringContains(imp.Description, "judge pass rate") {
				foundTeradataImprovement = true
				break
			}
		}
	}

	if !foundTeradataImprovement {
		t.Error("expected improvement for low teradata_compliance score, but none found")
	}
}

// TestMultipleCustomDimensions tests handling of multiple custom dimensions
func TestMultipleCustomDimensions(t *testing.T) {
	patterns := []*loomv1.PatternMetric{
		{
			PatternName: "healthcare_pattern",
			Domain:      "healthcare",
			TotalUsages: 200,
			SuccessRate: 0.88,
			JudgeCriterionScores: map[string]float64{
				"quality":          0.88,
				"safety":           0.85,
				"hipaa_compliance": 0.92, // Custom dimension 1
				"phi_detection":    0.90, // Custom dimension 2
				"gdpr_compliance":  0.87, // Custom dimension 3
			},
		},
	}

	dimensionWeights := map[string]float64{
		"hipaa_compliance": 0.4,
		"phi_detection":    0.3,
		"gdpr_compliance":  0.2,
		"safety":           0.1,
	}

	la := &LearningAgent{
		tracer: observability.NewNoOpTracer(),
	}

	tunings := la.calculatePatternTunings(
		patterns,
		loomv1.TuningStrategy_TUNING_MODERATE,
		nil,
		"",
		dimensionWeights,
		nil,
	)

	if len(tunings) == 0 {
		t.Fatal("expected tuning result, got none")
	}

	// Verify high scores across multiple custom dimensions results in priority increase
	tuning := tunings[0]
	if len(tuning.Adjustments) == 0 {
		t.Fatal("expected priority adjustment, got none")
	}

	for _, adj := range tuning.Adjustments {
		if adj.ParameterName == "priority" {
			oldPriority := parseInt32(adj.OldValue)
			newPriority := parseInt32(adj.NewValue)

			if newPriority <= oldPriority {
				t.Errorf("expected priority increase for high-scoring pattern, got old=%d new=%d",
					oldPriority, newPriority)
			}
		}
	}
}

// TestDimensionWeightValidation tests unused dimension weight tracking
func TestDimensionWeightValidation(t *testing.T) {
	patterns := []*loomv1.PatternMetric{
		{
			PatternName: "test_pattern",
			Domain:      "test",
			TotalUsages: 100,
			SuccessRate: 0.80,
			JudgeCriterionScores: map[string]float64{
				"quality": 0.80,
			},
		},
	}

	// Specify dimension weights that don't match pattern dimensions
	dimensionWeights := map[string]float64{
		"nonexistent_dimension": 1.0,
	}

	// Create mock tracer that captures metrics
	mockTracer := &mockMetricTracer{
		metrics: make(map[string]int),
	}

	la := &LearningAgent{
		tracer: mockTracer,
	}

	la.calculatePatternTunings(
		patterns,
		loomv1.TuningStrategy_TUNING_MODERATE,
		nil,
		"",
		dimensionWeights,
		nil,
	)

	// Verify warning metric was recorded
	if mockTracer.metrics["learning_agent.unused_dimension_weight"] != 1 {
		t.Error("expected unused_dimension_weight metric to be recorded")
	}
}

// mockMetricTracer for testing metric recording
type mockMetricTracer struct {
	metrics map[string]int
}

func (m *mockMetricTracer) StartSpan(ctx context.Context, name string, opts ...observability.SpanOption) (context.Context, *observability.Span) {
	span := &observability.Span{}
	return ctx, span
}

func (m *mockMetricTracer) EndSpan(span *observability.Span) {}

func (m *mockMetricTracer) RecordMetric(name string, value float64, tags map[string]string) {
	m.metrics[name]++
}

func (m *mockMetricTracer) RecordEvent(ctx context.Context, name string, attributes map[string]interface{}) {
}

func (m *mockMetricTracer) Flush(ctx context.Context) error { return nil }

// Helper to check if string contains substring (reuse existing contains from engine.go)
func stringContains(s, substr string) bool {
	return contains(s, substr)
}
