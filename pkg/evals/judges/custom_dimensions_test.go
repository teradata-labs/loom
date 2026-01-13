// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//go:build hawk

// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package judges

import (
	"context"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// customDimTestLLMProvider for testing (renamed to avoid conflicts with judge_agent_adapter_test.go)
type customDimTestLLMProvider struct{}

func (m *customDimTestLLMProvider) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{
		Content: "test response",
	}, nil
}

func (m *customDimTestLLMProvider) Name() string {
	return "custom-dim-test"
}

func (m *customDimTestLLMProvider) Model() string {
	return "claude-3-5-sonnet-20241022"
}

// TestCustomDimensionValidation tests Phase 8 custom dimension validation
func TestCustomDimensionValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      *loomv1.JudgeConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid custom dimension with name",
			config: &loomv1.JudgeConfig{
				Id:                  "test-judge",
				Name:                "Test Judge",
				Dimensions:          []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM},
				CustomDimensionName: "teradata_compliance",
			},
			expectError: false,
		},
		{
			name: "custom dimension without name - should fail",
			config: &loomv1.JudgeConfig{
				Id:         "test-judge",
				Name:       "Test Judge",
				Dimensions: []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM},
			},
			expectError: true,
			errorMsg:    "custom_dimension_name must be set",
		},
		{
			name: "custom dimension name without CUSTOM dimension - ignored",
			config: &loomv1.JudgeConfig{
				Id:                  "test-judge",
				Name:                "Test Judge",
				Dimensions:          []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY},
				CustomDimensionName: "teradata_compliance",
			},
			expectError: false, // Not an error, just ignored
		},
		{
			name: "mixed standard and custom dimensions",
			config: &loomv1.JudgeConfig{
				Id:   "test-judge",
				Name: "Test Judge",
				Dimensions: []loomv1.JudgeDimension{
					loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY,
					loomv1.JudgeDimension_JUDGE_DIMENSION_SAFETY,
					loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM,
				},
				CustomDimensionName: "hipaa_compliance",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProvider := &customDimTestLLMProvider{}

			_, err := NewLLMJudge(mockProvider, tt.config, observability.NewNoOpTracer())

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				} else if tt.errorMsg != "" && !stringContainsSubstr(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestCustomDimensionScoring tests that custom dimensions appear in dimension scores
func TestCustomDimensionScoring(t *testing.T) {
	tests := []struct {
		name                    string
		config                  *loomv1.JudgeConfig
		expectedDimensionScores []string // Dimension names that should be in scores
	}{
		{
			name: "teradata compliance custom dimension",
			config: &loomv1.JudgeConfig{
				Id:   "teradata-judge",
				Name: "Teradata Compliance Judge",
				Dimensions: []loomv1.JudgeDimension{
					loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM,
				},
				CustomDimensionName:        "teradata_compliance",
				CustomDimensionDescription: "Validates Teradata SQL syntax and best practices",
			},
			expectedDimensionScores: []string{
				"quality",
				"correctness",
				"completeness",
				"safety",
				"teradata_compliance", // Custom dimension
			},
		},
		{
			name: "hipaa compliance with mixed dimensions",
			config: &loomv1.JudgeConfig{
				Id:   "hipaa-judge",
				Name: "HIPAA Compliance Judge",
				Dimensions: []loomv1.JudgeDimension{
					loomv1.JudgeDimension_JUDGE_DIMENSION_SAFETY,
					loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM,
				},
				CustomDimensionName: "hipaa_compliance",
			},
			expectedDimensionScores: []string{
				"quality",
				"correctness",
				"completeness",
				"safety",
				"hipaa_compliance", // Custom dimension
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProvider := &customDimTestLLMProvider{}

			judge, err := NewLLMJudge(mockProvider, tt.config, observability.NewNoOpTracer())
			if err != nil {
				t.Fatalf("failed to create judge: %v", err)
			}

			// Verify config has custom dimension
			if judge.Config().CustomDimensionName != tt.config.CustomDimensionName {
				t.Errorf("expected custom dimension name %q, got %q",
					tt.config.CustomDimensionName, judge.Config().CustomDimensionName)
			}

			// Note: Full evaluation testing would require mocking Hawk's judge
			// For now, we verify the configuration is properly stored
		})
	}
}

// TestOrchestratorWithCustomDimensions tests orchestrator handling of custom dimensions
func TestOrchestratorWithCustomDimensions(t *testing.T) {
	registry := NewRegistry()
	aggregator := NewAggregator(nil)
	tracer := observability.NewNoOpTracer()

	orch := NewOrchestrator(&Config{
		Registry:   registry,
		Aggregator: aggregator,
		Tracer:     tracer,
	})

	// Create mock judges with custom dimensions
	mockProvider := &customDimTestLLMProvider{}

	// Teradata compliance judge
	teradataConfig := &loomv1.JudgeConfig{
		Id:   "teradata-compliance",
		Name: "Teradata Compliance",
		Dimensions: []loomv1.JudgeDimension{
			loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM,
		},
		CustomDimensionName:        "teradata_compliance",
		CustomDimensionDescription: "Validates Teradata SQL syntax",
		Criticality:                loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
	}

	teradataJudge, err := NewLLMJudge(mockProvider, teradataConfig, tracer)
	if err != nil {
		t.Fatalf("failed to create teradata judge: %v", err)
	}

	if err := registry.Register(teradataJudge); err != nil {
		t.Fatalf("failed to register judge: %v", err)
	}

	// Verify judge is registered
	judges, err := registry.GetJudges([]string{"teradata-compliance"})
	if err != nil {
		t.Fatalf("failed to get judges: %v", err)
	}

	if len(judges) != 1 {
		t.Fatalf("expected 1 judge, got %d", len(judges))
	}

	// Verify custom dimension configuration
	judgeConfig := judges[0].Config()
	if judgeConfig.CustomDimensionName != "teradata_compliance" {
		t.Errorf("expected custom dimension name 'teradata_compliance', got %q",
			judgeConfig.CustomDimensionName)
	}

	// Test evaluation context (would normally trigger actual evaluation)
	ctx := context.Background()
	evalCtx := &loomv1.EvaluationContext{
		AgentId:   "test-agent",
		SessionId: "test-session",
		Prompt:    "SELECT * FROM table",
		Response:  "Query executed successfully",
		TraceId:   "test-trace",
	}

	// Note: Full orchestration testing requires mocking Hawk's judge evaluation
	// For Phase 8, we verify the configuration propagation
	_ = ctx
	_ = evalCtx
	_ = orch
}

// TestAggregatorWithCustomDimensions tests that aggregator handles custom dimension scores
func TestAggregatorWithCustomDimensions(t *testing.T) {
	aggregator := NewAggregator(nil)

	// Create verdicts with custom dimension scores
	verdicts := []*loomv1.JudgeResult{
		{
			JudgeId:      "teradata-judge",
			JudgeName:    "Teradata Compliance",
			OverallScore: 85.0,
			Verdict:      "PASS",
			DimensionScores: map[string]float64{
				"quality":              0.85,
				"teradata_compliance":  0.90, // Custom dimension
				"teradata_performance": 0.80, // Another custom dimension
			},
		},
		{
			JudgeId:      "quality-judge",
			JudgeName:    "Quality Judge",
			OverallScore: 90.0,
			Verdict:      "PASS",
			DimensionScores: map[string]float64{
				"quality":     0.90,
				"correctness": 0.88,
			},
		},
	}

	// Create mock judges
	judges := []Judge{
		&customDimensionMockJudge{id: "teradata-judge", weight: 1.0},
		&customDimensionMockJudge{id: "quality-judge", weight: 1.0},
	}

	// Aggregate with weighted average
	result := aggregator.Aggregate(verdicts, judges, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE)

	// Verify custom dimensions are aggregated
	if result.AvgDimensionScores == nil {
		t.Fatal("expected dimension scores, got nil")
	}

	// Check teradata_compliance dimension
	teradataScore, ok := result.AvgDimensionScores["teradata_compliance"]
	if !ok {
		t.Error("expected teradata_compliance dimension in aggregated scores")
	} else if teradataScore != 0.90 {
		t.Errorf("expected teradata_compliance score 0.90, got %.2f", teradataScore)
	}

	// Check teradata_performance dimension
	perfScore, ok := result.AvgDimensionScores["teradata_performance"]
	if !ok {
		t.Error("expected teradata_performance dimension in aggregated scores")
	} else if perfScore != 0.80 {
		t.Errorf("expected teradata_performance score 0.80, got %.2f", perfScore)
	}

	// Check quality dimension (should be averaged across both judges)
	qualityScore, ok := result.AvgDimensionScores["quality"]
	if !ok {
		t.Error("expected quality dimension in aggregated scores")
	} else {
		expectedQuality := (0.85 + 0.90) / 2.0
		if qualityScore != expectedQuality {
			t.Errorf("expected quality score %.2f, got %.2f", expectedQuality, qualityScore)
		}
	}
}

// customDimensionMockJudge for testing (renamed to avoid conflicts)
type customDimensionMockJudge struct {
	id          string
	name        string
	weight      float64
	criticality loomv1.JudgeCriticality
}

func (m *customDimensionMockJudge) ID() string                           { return m.id }
func (m *customDimensionMockJudge) Name() string                         { return m.name }
func (m *customDimensionMockJudge) Criteria() []string                   { return []string{"test"} }
func (m *customDimensionMockJudge) Weight() float64                      { return m.weight }
func (m *customDimensionMockJudge) Criticality() loomv1.JudgeCriticality { return m.criticality }
func (m *customDimensionMockJudge) Dimensions() []loomv1.JudgeDimension {
	return []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY}
}
func (m *customDimensionMockJudge) Config() *loomv1.JudgeConfig { return nil }
func (m *customDimensionMockJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	return &loomv1.JudgeResult{}, nil
}

// Helper function - uses existing contains() from retry.go
func stringContainsSubstr(s, substr string) bool {
	return contains(s, substr)
}
