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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

func TestNewOrchestrator(t *testing.T) {
	orch := NewOrchestrator(nil)
	require.NotNil(t, orch)
	assert.NotNil(t, orch.registry)
	assert.NotNil(t, orch.aggregator)
	assert.NotNil(t, orch.tracer)
	assert.NotNil(t, orch.logger)
}

func TestNewOrchestrator_WithConfig(t *testing.T) {
	config := &Config{
		Registry:   NewRegistry(),
		Aggregator: NewAggregator(nil),
		Tracer:     observability.NewNoOpTracer(),
	}

	orch := NewOrchestrator(config)
	require.NotNil(t, orch)
	assert.Equal(t, config.Registry, orch.registry)
	assert.Equal(t, config.Aggregator, orch.aggregator)
}

func TestOrchestrator_Evaluate_EmptyJudges(t *testing.T) {
	orch := NewOrchestrator(nil)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test prompt",
			Response: "test response",
		},
		JudgeIds:      []string{},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	ctx := context.Background()
	_, err := orch.Evaluate(ctx, req)

	// Should fail because no judges found
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no judges found")
}

func TestOrchestrator_Evaluate_Synchronous(t *testing.T) {
	// Create orchestrator with registry
	orch := NewOrchestrator(nil)

	// Register mock judges
	judge1 := &testJudge{
		id:          "judge1",
		name:        "Quality Judge",
		weight:      2.0,
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:         "judge1",
			JudgeName:       "Quality Judge",
			Verdict:         "PASS",
			OverallScore:    90.0,
			Reasoning:       "Good quality",
			ExecutionTimeMs: 100,
			CostUsd:         0.01,
			DimensionScores: map[string]float64{"quality": 90.0},
		},
	}

	judge2 := &testJudge{
		id:          "judge2",
		name:        "Safety Judge",
		weight:      2.5,
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_SAFETY_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:         "judge2",
			JudgeName:       "Safety Judge",
			Verdict:         "PASS",
			OverallScore:    85.0,
			Reasoning:       "Safe output",
			ExecutionTimeMs: 120,
			CostUsd:         0.02,
			DimensionScores: map[string]float64{"safety": 85.0},
		},
	}

	_ = orch.registry.Register(judge1)
	_ = orch.registry.Register(judge2)

	// Create evaluation request
	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test prompt",
			Response: "test response",
		},
		JudgeIds:      []string{"judge1", "judge2"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	// Verify
	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.Len(t, result.Verdicts, 2)
	assert.NotNil(t, result.Aggregated)
	assert.NotNil(t, result.Metadata)
	assert.Equal(t, int32(2), result.Metadata.TotalJudges)
	assert.Equal(t, int32(2), result.Metadata.PassedJudges)
	assert.Greater(t, result.FinalScore, 85.0)
}

func TestOrchestrator_Evaluate_AllMustPass_OneFails(t *testing.T) {
	orch := NewOrchestrator(nil)

	// Register judges - one passes, one fails
	judge1 := &testJudge{
		id:   "judge1",
		name: "Judge 1",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "judge1",
			JudgeName:    "Judge 1",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	judge2 := &testJudge{
		id:   "judge2",
		name: "Judge 2",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "judge2",
			JudgeName:    "Judge 2",
			Verdict:      "FAIL",
			OverallScore: 50.0,
		},
	}

	_ = orch.registry.Register(judge1)
	_ = orch.registry.Register(judge2)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:      []string{"judge1", "judge2"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err)
	assert.False(t, result.Passed, "Should fail because all-must-pass and one failed")
	assert.Equal(t, int32(1), result.Metadata.PassedJudges)
	assert.Equal(t, int32(1), result.Metadata.FailedJudges)
}

func TestOrchestrator_Evaluate_Hybrid_CriticalFailsEarly(t *testing.T) {
	orch := NewOrchestrator(nil)

	// Critical judge that fails
	criticalJudge := &testJudge{
		id:          "critical",
		name:        "Critical Judge",
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:      "critical",
			JudgeName:    "Critical Judge",
			Verdict:      "FAIL",
			OverallScore: 40.0,
			Reasoning:    "Critical failure",
		},
	}

	// Non-critical judge (should not run in all-must-pass mode)
	nonCriticalJudge := &testJudge{
		id:          "non-critical",
		name:        "Non-Critical Judge",
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:      "non-critical",
			JudgeName:    "Non-Critical Judge",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	_ = orch.registry.Register(criticalJudge)
	_ = orch.registry.Register(nonCriticalJudge)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:      []string{"critical", "non-critical"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_HYBRID,
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err)
	assert.False(t, result.Passed)
	// In hybrid mode with all-must-pass, only critical judge verdict is returned immediately
	assert.Len(t, result.Verdicts, 1, "Only critical judge should have run")
	assert.Equal(t, "critical", result.Verdicts[0].JudgeId)
}

func TestOrchestrator_Evaluate_Timeout(t *testing.T) {
	orch := NewOrchestrator(nil)

	// Judge that takes too long
	slowJudge := &testJudge{
		id:    "slow",
		name:  "Slow Judge",
		delay: 200 * time.Millisecond,
		verdict: &loomv1.JudgeResult{
			JudgeId:      "slow",
			JudgeName:    "Slow Judge",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	_ = orch.registry.Register(slowJudge)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:       []string{"slow"},
		Aggregation:    loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode:  loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		TimeoutSeconds: 1, // Very short timeout (1 second, but judge takes 200ms)
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	// Should succeed because judge finishes within timeout
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestOrchestrator_Evaluate_FailFast(t *testing.T) {
	orch := NewOrchestrator(nil)

	// First judge fails
	failingJudge := &testJudge{
		id:   "failing",
		name: "Failing Judge",
		err:  assert.AnError, // Return error
	}

	passingJudge := &testJudge{
		id:   "passing",
		name: "Passing Judge",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "passing",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	_ = orch.registry.Register(failingJudge)
	_ = orch.registry.Register(passingJudge)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:      []string{"failing", "passing"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		FailFast:      true,
	}

	ctx := context.Background()
	_, err := orch.Evaluate(ctx, req)

	// Should fail fast when judge returns error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail-fast enabled")
}

func TestOrchestrator_Evaluate_MajorityPass(t *testing.T) {
	orch := NewOrchestrator(nil)

	// 3 judges: 2 pass, 1 fails
	for i := 0; i < 3; i++ {
		verdict := "PASS"
		score := 90.0
		if i == 2 {
			verdict = "FAIL"
			score = 40.0
		}

		judge := &testJudge{
			id:   string(rune('A' + i)),
			name: string(rune('A' + i)),
			verdict: &loomv1.JudgeResult{
				JudgeId:      string(rune('A' + i)),
				Verdict:      verdict,
				OverallScore: score,
			},
		}
		_ = orch.registry.Register(judge)
	}

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:      []string{"A", "B", "C"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err)
	assert.True(t, result.Passed, "Majority (2/3) passed")
	assert.InDelta(t, 0.666, result.Aggregated.PassRate, 0.01)
}

func TestOrchestrator_ExecuteAsyncJudges(t *testing.T) {
	orch := NewOrchestrator(nil)

	// Create judges with different speeds
	fastJudge := &testJudge{
		id:   "fast",
		name: "Fast Judge",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "fast",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	slowJudge := &testJudge{
		id:    "slow",
		name:  "Slow Judge",
		delay: 50 * time.Millisecond,
		verdict: &loomv1.JudgeResult{
			JudgeId:      "slow",
			Verdict:      "PASS",
			OverallScore: 85.0,
		},
	}

	_ = orch.registry.Register(fastJudge)
	_ = orch.registry.Register(slowJudge)

	judges := []Judge{fastJudge, slowJudge}
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test",
		Response: "test",
	}

	ctx := context.Background()

	// executeAsyncJudges should run without fail-fast
	verdicts, err := orch.executeAsyncJudges(ctx, judges, evalCtx, nil)

	require.NoError(t, err)
	assert.Len(t, verdicts, 2)
	assert.Equal(t, "PASS", verdicts[0].Verdict)
	assert.Equal(t, "PASS", verdicts[1].Verdict)
}

func TestOrchestrator_ExecuteAsyncJudges_OneFailure(t *testing.T) {
	orch := NewOrchestrator(nil)

	// One passing, one failing judge
	passingJudge := &testJudge{
		id:   "passing",
		name: "Passing Judge",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "passing",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	failingJudge := &testJudge{
		id:   "failing",
		name: "Failing Judge",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "failing",
			Verdict:      "FAIL",
			OverallScore: 40.0,
		},
	}

	_ = orch.registry.Register(passingJudge)
	_ = orch.registry.Register(failingJudge)

	judges := []Judge{passingJudge, failingJudge}
	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test",
		Response: "test",
	}

	ctx := context.Background()

	// Should continue even if one fails (no fail-fast)
	verdicts, err := orch.executeAsyncJudges(ctx, judges, evalCtx, nil)

	require.NoError(t, err)
	assert.Len(t, verdicts, 2)

	// Verify both judges executed
	judgeIds := []string{verdicts[0].JudgeId, verdicts[1].JudgeId}
	assert.Contains(t, judgeIds, "passing")
	assert.Contains(t, judgeIds, "failing")
}

func TestExtractJudgeResultFromAgentResult(t *testing.T) {
	tests := []struct {
		name        string
		agentResult *loomv1.AgentResult
		expectErr   bool
		expectID    string
	}{
		{
			name: "valid judge result",
			agentResult: &loomv1.AgentResult{
				Metadata: map[string]string{
					"judge_result": `{"judge_id":"test-judge","judge_name":"Test Judge","verdict":"PASS","overall_score":95.0}`,
				},
			},
			expectErr: false,
			expectID:  "test-judge",
		},
		{
			name:        "nil agent result",
			agentResult: nil,
			expectErr:   true,
		},
		{
			name: "nil metadata",
			agentResult: &loomv1.AgentResult{
				Metadata: nil,
			},
			expectErr: true,
		},
		{
			name: "missing judge_result key",
			agentResult: &loomv1.AgentResult{
				Metadata: map[string]string{
					"other": "data",
				},
			},
			expectErr: true,
		},
		{
			name: "invalid JSON",
			agentResult: &loomv1.AgentResult{
				Metadata: map[string]string{
					"judge_result": "not valid json",
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractJudgeResultFromAgentResult(tt.agentResult)

			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.expectID, result.JudgeId)
			}
		})
	}
}

func TestOrchestrator_AllPassed(t *testing.T) {
	orch := NewOrchestrator(nil)

	tests := []struct {
		name     string
		verdicts []*loomv1.JudgeResult
		expected bool
	}{
		{
			name: "all pass",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS"},
				{Verdict: "PASS"},
			},
			expected: true,
		},
		{
			name: "one fail",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS"},
				{Verdict: "FAIL"},
			},
			expected: false,
		},
		{
			name: "all fail",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "FAIL"},
				{Verdict: "FAIL"},
			},
			expected: false,
		},
		{
			name:     "empty",
			verdicts: []*loomv1.JudgeResult{},
			expected: true, // vacuously true
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := orch.allPassed(tt.verdicts)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestOrchestrator_BuildExplanation(t *testing.T) {
	orch := NewOrchestrator(nil)

	tests := []struct {
		name       string
		verdicts   []*loomv1.JudgeResult
		aggregated *loomv1.AggregatedJudgeMetrics
		strategy   loomv1.AggregationStrategy
		contains   []string
	}{
		{
			name: "weighted average",
			verdicts: []*loomv1.JudgeResult{
				{JudgeName: "Judge 1", Verdict: "PASS"},
				{JudgeName: "Judge 2", Verdict: "PASS"},
			},
			aggregated: &loomv1.AggregatedJudgeMetrics{
				WeightedAverageScore: 87.5,
			},
			strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
			contains: []string{"87.5", "2/2"},
		},
		{
			name: "all must pass - success",
			verdicts: []*loomv1.JudgeResult{
				{JudgeName: "Judge 1", Verdict: "PASS"},
				{JudgeName: "Judge 2", Verdict: "PASS"},
			},
			aggregated: &loomv1.AggregatedJudgeMetrics{},
			strategy:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
			contains:   []string{"All 2 judges passed"},
		},
		{
			name: "all must pass - failure",
			verdicts: []*loomv1.JudgeResult{
				{JudgeName: "Judge 1", Verdict: "PASS"},
				{JudgeName: "Judge 2", Verdict: "FAIL"},
			},
			aggregated: &loomv1.AggregatedJudgeMetrics{},
			strategy:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ALL_MUST_PASS,
			contains:   []string{"1/2 judges failed"},
		},
		{
			name: "majority pass",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS"},
				{Verdict: "PASS"},
				{Verdict: "FAIL"},
			},
			aggregated: &loomv1.AggregatedJudgeMetrics{
				PassRate: 0.666,
			},
			strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAJORITY_PASS,
			contains: []string{"2/3", "67%"},
		},
		{
			name: "any pass - success",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS"},
				{Verdict: "FAIL"},
			},
			aggregated: &loomv1.AggregatedJudgeMetrics{},
			strategy:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_ANY_PASS,
			contains:   []string{"At least one judge passed", "1/2"},
		},
		{
			name: "min score",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS", OverallScore: 90.0},
				{Verdict: "PASS", OverallScore: 75.0},
			},
			aggregated: &loomv1.AggregatedJudgeMetrics{
				MinScore: 75.0,
			},
			strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MIN_SCORE,
			contains: []string{"75.0", "strictest"},
		},
		{
			name: "max score",
			verdicts: []*loomv1.JudgeResult{
				{Verdict: "PASS", OverallScore: 90.0},
				{Verdict: "FAIL", OverallScore: 60.0},
			},
			aggregated: &loomv1.AggregatedJudgeMetrics{
				MaxScore: 90.0,
			},
			strategy: loomv1.AggregationStrategy_AGGREGATION_STRATEGY_MAX_SCORE,
			contains: []string{"90.0", "best"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			explanation := orch.buildExplanation(tt.verdicts, tt.aggregated, tt.strategy)
			assert.NotEmpty(t, explanation)

			for _, expected := range tt.contains {
				assert.Contains(t, explanation, expected)
			}
		})
	}
}

func TestOrchestrator_CollectSuggestions(t *testing.T) {
	orch := NewOrchestrator(nil)

	verdicts := []*loomv1.JudgeResult{
		{
			JudgeName:   "Judge 1",
			Suggestions: []string{"Improve accuracy", "Add more context"},
		},
		{
			JudgeName:   "Judge 2",
			Suggestions: []string{"Reduce cost"},
		},
		{
			JudgeName:   "Judge 3",
			Suggestions: nil, // No suggestions
		},
	}

	suggestions := orch.collectSuggestions(verdicts)

	assert.Len(t, suggestions, 3)
	assert.Contains(t, suggestions, "Improve accuracy")
	assert.Contains(t, suggestions, "Add more context")
	assert.Contains(t, suggestions, "Reduce cost")
}

// testJudge is a mock judge for testing
type testJudge struct {
	id          string
	name        string
	weight      float64
	criticality loomv1.JudgeCriticality
	criteria    []string
	dimensions  []loomv1.JudgeDimension
	config      *loomv1.JudgeConfig
	verdict     *loomv1.JudgeResult
	err         error
	delay       time.Duration
}

func (j *testJudge) ID() string   { return j.id }
func (j *testJudge) Name() string { return j.name }
func (j *testJudge) Weight() float64 {
	if j.weight == 0 {
		return 1.0
	}
	return j.weight
}
func (j *testJudge) Criticality() loomv1.JudgeCriticality { return j.criticality }
func (j *testJudge) Criteria() []string                   { return j.criteria }
func (j *testJudge) Dimensions() []loomv1.JudgeDimension  { return j.dimensions }
func (j *testJudge) Config() *loomv1.JudgeConfig {
	if j.config != nil {
		return j.config
	}
	return &loomv1.JudgeConfig{
		Id:          j.id,
		Name:        j.name,
		Weight:      j.weight,
		Criticality: j.criticality,
	}
}
func (j *testJudge) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	if j.delay > 0 {
		time.Sleep(j.delay)
	}
	if j.err != nil {
		return nil, j.err
	}
	return j.verdict, nil
}

// Phase 7: Retry and Circuit Breaker Integration Tests

func TestOrchestrator_JudgeWithRetryConfig(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator(nil)

	// Create a judge with retry config that eventually succeeds
	attemptCount := int32(0)
	judge := &retryMockJudge{
		id:   "retryable-judge",
		name: "Retryable Judge",
		config: &loomv1.JudgeConfig{
			Id:   "retryable-judge",
			Name: "Retryable Judge",
			RetryConfig: &loomv1.RetryConfig{
				MaxAttempts:      2,
				InitialBackoffMs: 10,
				MaxBackoffMs:     50,
			},
		},
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			count := atomic.AddInt32(&attemptCount, 1)
			if count == 1 {
				return nil, &HTTPError{StatusCode: 500, Message: "Internal Server Error"}
			}
			return &loomv1.JudgeResult{
				JudgeId:      "retryable-judge",
				JudgeName:    "Retryable Judge",
				Verdict:      "PASS",
				OverallScore: 90.0,
			}, nil
		},
	}

	_ = orch.registry.Register(judge)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:      []string{"retryable-judge"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Passed)
	assert.Len(t, result.Verdicts, 1)
	assert.Equal(t, "PASS", result.Verdicts[0].Verdict)
	assert.Equal(t, int32(2), atomic.LoadInt32(&attemptCount), "Should have retried once")
}

func TestOrchestrator_JudgeWithCircuitBreaker(t *testing.T) {
	t.Parallel()

	// NOTE: This test validates that circuit breaker wrapping works,
	// but circuit breakers are per-RetryableJudge instance.
	// In the orchestrator, each evaluate call creates a new wrapper,
	// so circuit breaker state is not shared across evaluate calls.
	// This is actually the correct behavior - circuit breakers should
	// be managed at a higher level (e.g., judge registry) if shared
	// state is needed.

	// For now, we'll just verify that the configuration is properly
	// applied and the judge fails as expected
	orch := NewOrchestrator(nil)

	// Create a judge with circuit breaker config that always fails
	judge := &retryMockJudge{
		id:   "failing-judge",
		name: "Failing Judge",
		config: &loomv1.JudgeConfig{
			Id:   "failing-judge",
			Name: "Failing Judge",
			RetryConfig: &loomv1.RetryConfig{
				MaxAttempts:      1,
				InitialBackoffMs: 10,
				CircuitBreaker: &loomv1.CircuitBreakerConfig{
					FailureThreshold: 2,
					ResetTimeoutMs:   100,
					SuccessThreshold: 1,
					Enabled:          true,
				},
			},
		},
		evaluateFunc: func(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
			return nil, &HTTPError{StatusCode: 500, Message: "Internal Server Error"}
		},
	}

	_ = orch.registry.Register(judge)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:      []string{"failing-judge"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		FailFast:      true, // Enable fail-fast so errors are returned
	}

	ctx := context.Background()

	// First call should fail (judge always returns error)
	_, err := orch.Evaluate(ctx, req)
	require.Error(t, err)

	// Second call should also fail (same reason)
	_, err = orch.Evaluate(ctx, req)
	require.Error(t, err)

	// Verify that the judge was wrapped with retry config
	wrapped := orch.wrapJudgeWithRetry(judge)
	_, ok := wrapped.(*RetryableJudge)
	assert.True(t, ok, "Judge should be wrapped with RetryableJudge when circuit breaker config is present")
}

func TestOrchestrator_WrapJudgeWithRetry(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator(nil)

	tests := []struct {
		name       string
		judge      Judge
		expectWrap bool
	}{
		{
			name: "judge with retry config is wrapped",
			judge: &testJudge{
				id:   "retry-judge",
				name: "Retry Judge",
				config: &loomv1.JudgeConfig{
					Id:   "retry-judge",
					Name: "Retry Judge",
					RetryConfig: &loomv1.RetryConfig{
						MaxAttempts: 3,
					},
				},
			},
			expectWrap: true,
		},
		{
			name: "judge without retry config is not wrapped",
			judge: &testJudge{
				id:   "no-retry-judge",
				name: "No Retry Judge",
				config: &loomv1.JudgeConfig{
					Id:          "no-retry-judge",
					Name:        "No Retry Judge",
					RetryConfig: nil,
				},
			},
			expectWrap: false,
		},
		{
			name: "judge with max_attempts=0 is not wrapped",
			judge: &testJudge{
				id:   "disabled-retry-judge",
				name: "Disabled Retry Judge",
				config: &loomv1.JudgeConfig{
					Id:   "disabled-retry-judge",
					Name: "Disabled Retry Judge",
					RetryConfig: &loomv1.RetryConfig{
						MaxAttempts: 0,
					},
				},
			},
			expectWrap: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wrapped := orch.wrapJudgeWithRetry(tt.judge)
			if tt.expectWrap {
				_, ok := wrapped.(*RetryableJudge)
				assert.True(t, ok, "Judge should be wrapped with RetryableJudge")
			} else {
				assert.Equal(t, tt.judge, wrapped, "Judge should not be wrapped")
			}
		})
	}
}

func TestOrchestrator_MultipleJudgesWithMixedRetryConfigs(t *testing.T) {
	t.Parallel()

	orch := NewOrchestrator(nil)

	// Judge 1: No retry config (always succeeds)
	judge1 := &testJudge{
		id:   "no-retry",
		name: "No Retry Judge",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "no-retry",
			JudgeName:    "No Retry Judge",
			Verdict:      "PASS",
			OverallScore: 95.0,
		},
	}

	// Judge 2: With retry config (succeeds on first try)
	judge2 := &testJudge{
		id:   "with-retry",
		name: "With Retry Judge",
		config: &loomv1.JudgeConfig{
			Id:   "with-retry",
			Name: "With Retry Judge",
			RetryConfig: &loomv1.RetryConfig{
				MaxAttempts:      3,
				InitialBackoffMs: 10,
			},
		},
		verdict: &loomv1.JudgeResult{
			JudgeId:      "with-retry",
			JudgeName:    "With Retry Judge",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	_ = orch.registry.Register(judge1)
	_ = orch.registry.Register(judge2)

	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test",
			Response: "test",
		},
		JudgeIds:      []string{"no-retry", "with-retry"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Passed)
	assert.Len(t, result.Verdicts, 2)
	assert.Equal(t, int32(2), result.Metadata.PassedJudges)
}

// TestOrchestrator_HawkExport_ExportToHawkTrue tests that verdicts are exported when export_to_hawk=true.
func TestOrchestrator_HawkExport_ExportToHawkTrue(t *testing.T) {
	// Setup mock Hawk server
	var exportedVerdictCount int32
	mockServer := createMockHawkServer(t, &exportedVerdictCount)
	defer mockServer.Close()

	// Create Hawk exporter
	exporter, err := observability.NewHawkJudgeExporter(&observability.HawkJudgeExporterConfig{
		Endpoint:      mockServer.URL,
		APIKey:        "test-api-key",
		BatchSize:     10,
		FlushInterval: 100 * time.Millisecond,
		BufferSize:    50,
	})
	if err != nil && err.Error() == "hawk judge exporter not compiled in (rebuild with -tags hawk)" {
		t.Skip("Hawk not compiled in - skipping test")
	}
	require.NoError(t, err)
	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Create orchestrator with HawkExporter
	orch := NewOrchestrator(&Config{
		HawkExporter: exporter,
	})

	// Register mock judges
	judge1 := &testJudge{
		id:          "judge1",
		name:        "Quality Judge",
		weight:      2.0,
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:         "judge1",
			JudgeName:       "Quality Judge",
			Verdict:         "PASS",
			OverallScore:    90.0,
			DimensionScores: map[string]float64{"quality": 90.0},
		},
	}

	judge2 := &testJudge{
		id:          "judge2",
		name:        "Safety Judge",
		weight:      2.0,
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:         "judge2",
			JudgeName:       "Safety Judge",
			Verdict:         "PASS",
			OverallScore:    85.0,
			DimensionScores: map[string]float64{"safety": 85.0},
		},
	}

	_ = orch.registry.Register(judge1)
	_ = orch.registry.Register(judge2)

	// Evaluate with ExportToHawk = true
	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test prompt",
			Response: "test response",
		},
		JudgeIds:      []string{"judge1", "judge2"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		ExportToHawk:  true, // IMPORTANT: Enable export
	}

	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.Verdicts, 2)
	assert.True(t, result.Metadata.ExportedToHawk, "ExportedToHawk should be true")

	// Wait for async export
	time.Sleep(200 * time.Millisecond)

	// Verify verdicts were exported
	assert.Greater(t, atomic.LoadInt32(&exportedVerdictCount), int32(0), "Verdicts should be exported to Hawk")
}

// TestOrchestrator_HawkExport_ExportToHawkFalse tests that verdicts are NOT exported when export_to_hawk=false.
func TestOrchestrator_HawkExport_ExportToHawkFalse(t *testing.T) {
	// Setup mock Hawk server
	var exportedVerdictCount int32
	mockServer := createMockHawkServer(t, &exportedVerdictCount)
	defer mockServer.Close()

	// Create Hawk exporter
	exporter, err := observability.NewHawkJudgeExporter(&observability.HawkJudgeExporterConfig{
		Endpoint:      mockServer.URL,
		APIKey:        "test-api-key",
		BatchSize:     10,
		FlushInterval: 100 * time.Millisecond,
		BufferSize:    50,
	})
	if err != nil && err.Error() == "hawk judge exporter not compiled in (rebuild with -tags hawk)" {
		t.Skip("Hawk not compiled in - skipping test")
	}
	require.NoError(t, err)
	ctx := context.Background()
	exporter.Start(ctx)
	defer func() { _ = exporter.Stop(ctx) }()

	// Create orchestrator with HawkExporter
	orch := NewOrchestrator(&Config{
		HawkExporter: exporter,
	})

	// Register mock judge
	judge1 := &testJudge{
		id:          "judge1",
		name:        "Quality Judge",
		weight:      2.0,
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:      "judge1",
			JudgeName:    "Quality Judge",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	_ = orch.registry.Register(judge1)

	// Evaluate with ExportToHawk = false
	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test prompt",
			Response: "test response",
		},
		JudgeIds:      []string{"judge1"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		ExportToHawk:  false, // IMPORTANT: Disable export
	}

	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Metadata.ExportedToHawk, "ExportedToHawk should be false")

	// Wait to ensure no export happens
	time.Sleep(200 * time.Millisecond)

	// Verify no verdicts were exported
	assert.Equal(t, int32(0), atomic.LoadInt32(&exportedVerdictCount), "No verdicts should be exported")
}

// TestOrchestrator_HawkExport_NoExporter tests that evaluation works even without HawkExporter configured.
func TestOrchestrator_HawkExport_NoExporter(t *testing.T) {
	// Create orchestrator WITHOUT HawkExporter
	orch := NewOrchestrator(&Config{
		HawkExporter: nil, // No exporter
	})

	// Register mock judge
	judge1 := &testJudge{
		id:          "judge1",
		name:        "Quality Judge",
		weight:      2.0,
		criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		verdict: &loomv1.JudgeResult{
			JudgeId:      "judge1",
			JudgeName:    "Quality Judge",
			Verdict:      "PASS",
			OverallScore: 90.0,
		},
	}

	_ = orch.registry.Register(judge1)

	// Evaluate with ExportToHawk = true (should not fail even without exporter)
	req := &loomv1.EvaluateRequest{
		Context: &loomv1.EvaluationContext{
			AgentId:  "test-agent",
			Prompt:   "test prompt",
			Response: "test response",
		},
		JudgeIds:      []string{"judge1"},
		Aggregation:   loomv1.AggregationStrategy_AGGREGATION_STRATEGY_WEIGHTED_AVERAGE,
		ExecutionMode: loomv1.ExecutionMode_EXECUTION_MODE_SYNCHRONOUS,
		ExportToHawk:  true,
	}

	ctx := context.Background()
	result, err := orch.Evaluate(ctx, req)

	require.NoError(t, err, "Evaluation should succeed even without HawkExporter")
	require.NotNil(t, result)
	assert.False(t, result.Metadata.ExportedToHawk, "ExportedToHawk should be false when no exporter configured")
}

// Helper function to create mock Hawk server for testing.
func createMockHawkServer(t *testing.T, exportedVerdictCount *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/judge-verdicts", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)

		verdicts, ok := payload["verdicts"].([]interface{})
		require.True(t, ok)

		atomic.AddInt32(exportedVerdictCount, int32(len(verdicts)))

		w.WriteHeader(http.StatusOK)
	}))
}
