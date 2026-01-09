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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewAggregator(t *testing.T) {
	agg := NewAggregator(nil)
	require.NotNil(t, agg)
	assert.NotNil(t, agg.config)
	assert.Equal(t, int32(80), agg.config.DefaultMinPassingScore)
}

func TestAggregator_WeightedAverage(t *testing.T) {
	tests := []struct {
		name                string
		verdicts            []*loomv1.JudgeResult
		judges              []Judge
		expectedWeightedAvg float64
		expectedMinScore    float64
		expectedMaxScore    float64
		expectedPassRate    float64
	}{
		{
			name: "two judges equal weights",
			verdicts: []*loomv1.JudgeResult{
				{JudgeId: "judge1", JudgeName: "Judge 1", Verdict: "PASS", OverallScore: 90, ExecutionTimeMs: 100, CostUsd: 0.01},
				{JudgeId: "judge2", JudgeName: "Judge 2", Verdict: "PASS", OverallScore: 80, ExecutionTimeMs: 150, CostUsd: 0.02},
			},
			judges: []Judge{
				&mockJudge{id: "judge1", name: "Judge 1", weight: 1.0},
				&mockJudge{id: "judge2", name: "Judge 2", weight: 1.0},
			},
			expectedWeightedAvg: 85.0, // (90*1.0 + 80*1.0) / (1.0 + 1.0)
			expectedMinScore:    80.0,
			expectedMaxScore:    90.0,
			expectedPassRate:    1.0,
		},
		{
			name: "two judges different weights",
			verdicts: []*loomv1.JudgeResult{
				{JudgeId: "judge1", JudgeName: "Judge 1", Verdict: "PASS", OverallScore: 90, ExecutionTimeMs: 100, CostUsd: 0.01},
				{JudgeId: "judge2", JudgeName: "Judge 2", Verdict: "FAIL", OverallScore: 60, ExecutionTimeMs: 150, CostUsd: 0.02},
			},
			judges: []Judge{
				&mockJudge{id: "judge1", name: "Judge 1", weight: 2.0},
				&mockJudge{id: "judge2", name: "Judge 2", weight: 1.0},
			},
			expectedWeightedAvg: 80.0, // (90*2.0 + 60*1.0) / (2.0 + 1.0) = 240/3
			expectedMinScore:    60.0,
			expectedMaxScore:    90.0,
			expectedPassRate:    0.5,
		},
		{
			name: "four judges mixed verdicts",
			verdicts: []*loomv1.JudgeResult{
				{JudgeId: "judge1", Verdict: "PASS", OverallScore: 95, ExecutionTimeMs: 100, CostUsd: 0.01},
				{JudgeId: "judge2", Verdict: "PASS", OverallScore: 85, ExecutionTimeMs: 120, CostUsd: 0.01},
				{JudgeId: "judge3", Verdict: "FAIL", OverallScore: 45, ExecutionTimeMs: 80, CostUsd: 0.01},
				{JudgeId: "judge4", Verdict: "FAIL", OverallScore: 55, ExecutionTimeMs: 90, CostUsd: 0.01},
			},
			judges: []Judge{
				&mockJudge{id: "judge1", weight: 1.0},
				&mockJudge{id: "judge2", weight: 1.0},
				&mockJudge{id: "judge3", weight: 1.0},
				&mockJudge{id: "judge4", weight: 1.0},
			},
			expectedWeightedAvg: 70.0, // (95+85+45+55)/4
			expectedMinScore:    45.0,
			expectedMaxScore:    95.0,
			expectedPassRate:    0.5,
		},
	}

	agg := NewAggregator(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agg.Aggregate(
				tt.verdicts,
				tt.judges,
				loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
			)

			assert.InDelta(t, tt.expectedWeightedAvg, result.WeightedAverageScore, 0.01)
			assert.InDelta(t, tt.expectedMinScore, result.MinScore, 0.01)
			assert.InDelta(t, tt.expectedMaxScore, result.MaxScore, 0.01)
			assert.InDelta(t, tt.expectedPassRate, result.PassRate, 0.01)
			assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE, result.Strategy)
		})
	}
}

func TestAggregator_AllMustPass(t *testing.T) {
	tests := []struct {
		name             string
		verdicts         []*loomv1.JudgeResult
		expectedPassRate float64
	}{
		{
			name: "all pass",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS", OverallScore: 90},
				{Verdict: "PASS", OverallScore: 85},
			},
			expectedPassRate: 1.0,
		},
		{
			name: "one fails",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS", OverallScore: 90},
				{Verdict: "FAIL", OverallScore: 50},
			},
			expectedPassRate: 0.5,
		},
		{
			name: "all fail",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "FAIL", OverallScore: 50},
				{Verdict: "FAIL", OverallScore: 45},
			},
			expectedPassRate: 0.0,
		},
	}

	agg := NewAggregator(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agg.Aggregate(
				tt.verdicts,
				nil,
				loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
			)

			assert.InDelta(t, tt.expectedPassRate, result.PassRate, 0.01)
			assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS, result.Strategy)
		})
	}
}

func TestAggregator_MajorityPass(t *testing.T) {
	tests := []struct {
		name             string
		verdicts         []*loomv1.JudgeResult
		expectedPassRate float64
	}{
		{
			name: "majority pass (3/5)",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS", OverallScore: 90},
				{Verdict: "PASS", OverallScore: 85},
				{Verdict: "PASS", OverallScore: 80},
				{Verdict: "FAIL", OverallScore: 50},
				{Verdict: "FAIL", OverallScore: 45},
			},
			expectedPassRate: 0.6,
		},
		{
			name: "exactly 50% (2/4)",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS", OverallScore: 90},
				{Verdict: "PASS", OverallScore: 85},
				{Verdict: "FAIL", OverallScore: 50},
				{Verdict: "FAIL", OverallScore: 45},
			},
			expectedPassRate: 0.5,
		},
	}

	agg := NewAggregator(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agg.Aggregate(
				tt.verdicts,
				nil,
				loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
			)

			assert.InDelta(t, tt.expectedPassRate, result.PassRate, 0.01)
			assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS, result.Strategy)
		})
	}
}

func TestAggregator_AnyPass(t *testing.T) {
	tests := []struct {
		name             string
		verdicts         []*loomv1.JudgeResult
		expectedPassRate float64
	}{
		{
			name: "one pass",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS", OverallScore: 90},
				{Verdict: "FAIL", OverallScore: 50},
				{Verdict: "FAIL", OverallScore: 45},
			},
			expectedPassRate: 0.333,
		},
		{
			name: "none pass",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "FAIL", OverallScore: 50},
				{Verdict: "FAIL", OverallScore: 45},
			},
			expectedPassRate: 0.0,
		},
	}

	agg := NewAggregator(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agg.Aggregate(
				tt.verdicts,
				nil,
				loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS,
			)

			assert.InDelta(t, tt.expectedPassRate, result.PassRate, 0.01)
			assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS, result.Strategy)
		})
	}
}

func TestAggregator_MinScore(t *testing.T) {
	verdicts := []*loomv1.JudgeResult{
		{Verdict: "PASS", OverallScore: 95},
		{Verdict: "PASS", OverallScore: 85},
		{Verdict: "FAIL", OverallScore: 45},
	}

	agg := NewAggregator(nil)
	result := agg.Aggregate(
		verdicts,
		nil,
		loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE,
	)

	// Min score should be used as weighted average
	assert.InDelta(t, 45.0, result.WeightedAverageScore, 0.01)
	assert.InDelta(t, 45.0, result.MinScore, 0.01)
	assert.InDelta(t, 95.0, result.MaxScore, 0.01)
	assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE, result.Strategy)
}

func TestAggregator_MaxScore(t *testing.T) {
	verdicts := []*loomv1.JudgeResult{
		{Verdict: "PASS", OverallScore: 95},
		{Verdict: "PASS", OverallScore: 85},
		{Verdict: "FAIL", OverallScore: 45},
	}

	agg := NewAggregator(nil)
	result := agg.Aggregate(
		verdicts,
		nil,
		loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE,
	)

	// Max score should be used as weighted average
	assert.InDelta(t, 95.0, result.WeightedAverageScore, 0.01)
	assert.InDelta(t, 45.0, result.MinScore, 0.01)
	assert.InDelta(t, 95.0, result.MaxScore, 0.01)
	assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE, result.Strategy)
}

func TestAggregator_EmptyVerdicts(t *testing.T) {
	agg := NewAggregator(nil)
	result := agg.Aggregate(
		[]*loomv1.JudgeResult{},
		nil,
		loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
	)

	assert.NotNil(t, result)
	assert.Equal(t, loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE, result.Strategy)
}

func TestAggregator_ComputeFinalVerdict(t *testing.T) {
	tests := []struct {
		name            string
		aggregated      *loomv1.AggregatedJudgeMetrics
		expectedVerdict string
	}{
		{
			name: "weighted average - pass",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy:             loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
				WeightedAverageScore: 85.0,
			},
			expectedVerdict: "PASS",
		},
		{
			name: "weighted average - partial",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy:             loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
				WeightedAverageScore: 70.0,
			},
			expectedVerdict: "PARTIAL",
		},
		{
			name: "weighted average - fail",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy:             loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
				WeightedAverageScore: 50.0,
			},
			expectedVerdict: "FAIL",
		},
		{
			name: "all must pass - pass",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
				PassRate: 1.0,
			},
			expectedVerdict: "PASS",
		},
		{
			name: "all must pass - fail",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
				PassRate: 0.9,
			},
			expectedVerdict: "FAIL",
		},
		{
			name: "majority pass - pass",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
				PassRate: 0.6,
			},
			expectedVerdict: "PASS",
		},
		{
			name: "majority pass - partial",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
				PassRate: 0.4,
			},
			expectedVerdict: "PARTIAL",
		},
		{
			name: "majority pass - fail",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
				PassRate: 0.2,
			},
			expectedVerdict: "FAIL",
		},
		{
			name: "any pass - pass",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS,
				PassRate: 0.1,
			},
			expectedVerdict: "PASS",
		},
		{
			name: "any pass - fail",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS,
				PassRate: 0.0,
			},
			expectedVerdict: "FAIL",
		},
		{
			name: "min score - pass",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE,
				MinScore: 85.0,
			},
			expectedVerdict: "PASS",
		},
		{
			name: "max score - pass",
			aggregated: &loomv1.AggregatedJudgeMetrics{
				Strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE,
				MaxScore: 85.0,
			},
			expectedVerdict: "PASS",
		},
	}

	agg := NewAggregator(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict := agg.ComputeFinalVerdict(tt.aggregated, nil)
			assert.Equal(t, tt.expectedVerdict, verdict)
		})
	}
}

func TestFormatJudgeFailures(t *testing.T) {
	tests := []struct {
		name           string
		verdicts       []*loomv1.JudgeResult
		expectedOutput string
	}{
		{
			name: "all pass",
			verdicts: []*loomv1.JudgeResult{
				{JudgeName: "Judge 1", Verdict: "PASS", OverallScore: 90, Reasoning: "Good"},
			},
			expectedOutput: "All judges passed",
		},
		{
			name: "one failure",
			verdicts: []*loomv1.JudgeResult{
				{JudgeName: "Judge 1", Verdict: "FAIL", OverallScore: 50, Reasoning: "Missing context"},
			},
			expectedOutput: "1 judge(s) failed: Judge 1: Missing context (score: 50.0)",
		},
		{
			name: "multiple failures",
			verdicts: []*loomv1.JudgeResult{
				{JudgeName: "Judge 1", Verdict: "FAIL", OverallScore: 50, Reasoning: "Missing context"},
				{JudgeName: "Judge 2", Verdict: "PASS", OverallScore: 90, Reasoning: "Good"},
				{JudgeName: "Judge 3", Verdict: "FAIL", OverallScore: 45, Reasoning: "Incorrect logic"},
			},
			expectedOutput: "2 judge(s) failed: Judge 1: Missing context (score: 50.0); Judge 3: Incorrect logic (score: 45.0)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := FormatJudgeFailures(tt.verdicts)
			assert.Equal(t, tt.expectedOutput, output)
		})
	}
}

// mockJudge implements Judge interface for testing
type mockJudge struct {
	id          string
	name        string
	weight      float64
	criticality loomv1.JudgeCriticality
	criteria    []string
	dimensions  []loomv1.JudgeDimension
}

func (m *mockJudge) ID() string                           { return m.id }
func (m *mockJudge) Name() string                         { return m.name }
func (m *mockJudge) Weight() float64                      { return m.weight }
func (m *mockJudge) Criticality() loomv1.JudgeCriticality { return m.criticality }
func (m *mockJudge) Criteria() []string                   { return m.criteria }
func (m *mockJudge) Dimensions() []loomv1.JudgeDimension  { return m.dimensions }
func (m *mockJudge) Config() *loomv1.JudgeConfig {
	return &loomv1.JudgeConfig{
		Id:          m.id,
		Name:        m.name,
		Weight:      m.weight,
		Criticality: m.criticality,
		Criteria:    "",
		Dimensions:  m.dimensions,
	}
}
func (m *mockJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	return nil, nil
}
