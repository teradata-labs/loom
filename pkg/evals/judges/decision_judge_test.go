// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package judges

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func decisionJudgeConfig(criteria string) *loomv1.JudgeConfig {
	return &loomv1.JudgeConfig{
		Id:       "dj-1",
		Name:     "decision-judge",
		Type:     loomv1.JudgeType_JUDGE_TYPE_DECISION,
		Criteria: criteria,
	}
}

// scriptAll scripts a Noul per criterion and the quality score.
func scriptAll(m *decisionmock.Decider, probs []float64, qualityLevel string) {
	for i, p := range probs {
		m.AnswerNoul(criterionID(i), p)
	}
	m.AnswerScore(judgeQualityQuestion, map[string]float64{qualityLevel: 1})
}

func TestSplitCriteria(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"single", "Answer is correct", []string{"Answer is correct"}},
		{"newlines", "A\nB\n\nC\n", []string{"A", "B", "C"}},
		{"bullets", "- A\n* B\n• C", []string{"A", "B", "C"}},
		{"numbered", "1. A\n2) B\n10. C", []string{"A", "B", "C"}},
		{"semicolons on one line", "A; B ;C", []string{"A", "B", "C"}},
		{"semicolons inside multi-line stay", "A; still A\nB", []string{"A; still A", "B"}},
		{"empty", "  \n ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitCriteria(tt.in)
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewDecisionJudge_DefaultsAndCriteria(t *testing.T) {
	m := decisionmock.New()
	j, err := NewDecisionJudge(m, decisionJudgeConfig("- correct\n- complete"), nil)
	require.NoError(t, err)
	assert.Equal(t, "dj-1", j.ID())
	assert.Equal(t, "decision-judge", j.Name())
	assert.Equal(t, []string{"correct", "complete"}, j.Criteria())
	assert.Equal(t, 1.0, j.Weight())
	assert.Equal(t, int32(80), j.Config().MinPassingScore)
	assert.Equal(t, loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL, j.Criticality())
	assert.Equal(t, []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY}, j.Dimensions())
	assert.Equal(t, "judge.dj-1", j.Site())

	// No criteria: defaults are used, never an empty request.
	j2, err := NewDecisionJudge(m, decisionJudgeConfig(""), nil)
	require.NoError(t, err)
	assert.Equal(t, defaultJudgeCriteria, j2.Criteria())

	_, err = NewDecisionJudge(nil, decisionJudgeConfig("x"), nil)
	require.Error(t, err)
	_, err = NewDecisionJudge(m, nil, nil)
	require.Error(t, err)
}

func TestDecisionJudge_Evaluate_Verdicts(t *testing.T) {
	tests := []struct {
		name        string
		probs       []float64
		quality     string
		wantVerdict string
		wantIssues  int
		wantOverall float64
	}{
		{"all met, high → PASS", []float64{0.95, 0.9}, "4", "PASS", 0, 92.5},
		{"all met but below min passing → PARTIAL", []float64{0.6, 0.7}, "2", "PARTIAL", 0, 65},
		{"one missed → PARTIAL", []float64{0.9, 0.3}, "2", "PARTIAL", 1, 60},
		{"none met → FAIL", []float64{0.1, 0.2}, "0", "FAIL", 2, 15},
		{"one met but overall < 50 → FAIL", []float64{0.55, 0.05}, "1", "FAIL", 1, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := decisionmock.New().SetModel("mock-jev").SetUsage(100, 0, 0.0007)
			scriptAll(m, tt.probs, tt.quality)
			j, err := NewDecisionJudge(m, decisionJudgeConfig("correct\ncomplete"), nil)
			require.NoError(t, err)

			res, err := j.Evaluate(context.Background(), &loomv1.EvaluationContext{Prompt: "q", Response: "a"})
			require.NoError(t, err)
			assert.Equal(t, tt.wantVerdict, res.Verdict)
			assert.InDelta(t, tt.wantOverall, res.OverallScore, 0.01)
			assert.Len(t, res.Issues, tt.wantIssues)
			assert.Equal(t, "mock-jev", res.JudgeModel)
			assert.Equal(t, "dj-1", res.JudgeId)
			assert.InDelta(t, 0.0007, res.CostUsd, 1e-9)
			assert.Empty(t, res.Error)
			assert.NotNil(t, res.JudgedAt)
			assert.Contains(t, res.DimensionScores, "correctness")
			assert.Contains(t, res.DimensionScores, "completeness")
			assert.Contains(t, res.DimensionScores, "quality")
			assert.NotEmpty(t, res.Reasoning)
			require.Equal(t, 1, m.CallCount(), "one decider call per evaluation")
		})
	}
}

func TestDecisionJudge_Evaluate_RequestShape(t *testing.T) {
	m := decisionmock.New()
	scriptAll(m, []float64{0.9}, "3")
	j, err := NewDecisionJudge(m, decisionJudgeConfig("The SQL is valid Teradata syntax"), nil)
	require.NoError(t, err)
	_, err = j.Evaluate(context.Background(), &loomv1.EvaluationContext{Prompt: "list users", Response: "SELECT * FROM users"})
	require.NoError(t, err)

	calls := m.Calls()
	require.Len(t, calls, 1)
	req := calls[0]
	assert.Equal(t, "judge.dj-1", req.Site)
	assert.Len(t, req.Questions, 2, "one noul per criterion + quality score")
	_, isNoul := req.Questions[criterionID(0)].Kind.(*loomv1.DecisionQuestion_Noul)
	assert.True(t, isNoul)
	_, isScore := req.Questions[judgeQualityQuestion].Kind.(*loomv1.DecisionQuestion_Score)
	assert.True(t, isScore)
	// State carries the request, the response and the criteria text.
	state := req.GetState().GetStructValue().AsMap()
	assert.Equal(t, "list users", state["request"])
	assert.Equal(t, "SELECT * FROM users", state["response"])
	assert.NotEmpty(t, state["criteria"])
}

func TestDecisionJudge_Evaluate_QualityMapsToDimension(t *testing.T) {
	m := decisionmock.New()
	// Level 2 of 0..4 → 50/100.
	scriptAll(m, []float64{0.9}, "2")
	j, err := NewDecisionJudge(m, decisionJudgeConfig("ok"), nil)
	require.NoError(t, err)
	res, err := j.Evaluate(context.Background(), &loomv1.EvaluationContext{Prompt: "q", Response: "a"})
	require.NoError(t, err)
	assert.InDelta(t, 50, res.DimensionScores["quality"], 0.01)
	assert.Equal(t, int32(50), res.QueryQuality)
	assert.Equal(t, int32(90), res.FactualAccuracy)
	assert.Equal(t, int32(100), res.Completeness)
}

func TestDecisionJudge_Evaluate_DeciderError(t *testing.T) {
	m := decisionmock.New().SetError(errors.New("gateway 503"))
	j, err := NewDecisionJudge(m, decisionJudgeConfig("ok"), nil)
	require.NoError(t, err)
	res, err := j.Evaluate(context.Background(), &loomv1.EvaluationContext{Prompt: "q", Response: "a"})
	require.Error(t, err)
	require.NotNil(t, res, "a failed evaluation still returns a result the aggregator can record")
	assert.Equal(t, "FAIL", res.Verdict)
	assert.Contains(t, res.Error, "gateway 503")
}

func TestDecisionJudge_Evaluate_CustomDimension(t *testing.T) {
	m := decisionmock.New()
	scriptAll(m, []float64{0.8}, "3")
	cfg := decisionJudgeConfig("ok")
	cfg.Dimensions = []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_CUSTOM}
	cfg.CustomDimensionName = "brand_voice"
	j, err := NewDecisionJudge(m, cfg, nil)
	require.NoError(t, err)
	res, err := j.Evaluate(context.Background(), &loomv1.EvaluationContext{Prompt: "q", Response: "a"})
	require.NoError(t, err)
	assert.InDelta(t, 0.8, res.DimensionScores["brand_voice"], 0.01)
}

func TestNewDecisionJudgeFromConfig(t *testing.T) {
	llm := &stubLLM{}

	t.Run("missing decision block", func(t *testing.T) {
		_, err := NewDecisionJudgeFromConfig(decisionJudgeConfig("ok"), llm, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "config.decision is required")
	})
	t.Run("unsupported provider", func(t *testing.T) {
		cfg := decisionJudgeConfig("ok")
		cfg.Decision = &loomv1.DecisionConfig{Provider: "nope"}
		_, err := NewDecisionJudgeFromConfig(cfg, llm, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported")
	})
	t.Run("llm provider adapter", func(t *testing.T) {
		cfg := decisionJudgeConfig("ok")
		cfg.Decision = &loomv1.DecisionConfig{Provider: "llm", TimeoutMs: 1000}
		j, err := NewDecisionJudgeFromConfig(cfg, llm, nil)
		require.NoError(t, err)
		assert.NotNil(t, j)
	})
	t.Run("llm provider needs a provider", func(t *testing.T) {
		cfg := decisionJudgeConfig("ok")
		cfg.Decision = &loomv1.DecisionConfig{Provider: "llm"}
		_, err := NewDecisionJudgeFromConfig(cfg, nil, nil)
		require.Error(t, err)
	})
	t.Run("jev without credentials fails cleanly", func(t *testing.T) {
		t.Setenv("AI_GATEWAY_API_KEY", "")
		cfg := decisionJudgeConfig("ok")
		cfg.Decision = &loomv1.DecisionConfig{Provider: "jev"}
		_, err := NewDecisionJudgeFromConfig(cfg, llm, nil)
		require.Error(t, err)
	})
}

func TestNewJudgeFromConfig_Dispatch(t *testing.T) {
	llm := &stubLLM{}

	llmCfg := &loomv1.JudgeConfig{Id: "l", Name: "llm", Type: loomv1.JudgeType_JUDGE_TYPE_HAWK, Criteria: "ok"}
	j, err := NewJudgeFromConfig(llm, llmCfg, nil)
	require.NoError(t, err)
	_, ok := j.(*LLMJudge)
	assert.True(t, ok, "non-decision types build an LLMJudge")

	dCfg := decisionJudgeConfig("ok")
	dCfg.Decision = &loomv1.DecisionConfig{Provider: "llm"}
	j, err = NewJudgeFromConfig(llm, dCfg, nil)
	require.NoError(t, err)
	_, ok = j.(*DecisionJudge)
	assert.True(t, ok, "JUDGE_TYPE_DECISION builds a DecisionJudge")
}

// stubLLM satisfies types.LLMProvider for constructor tests; it is never
// called.
type stubLLM struct{}

func (stubLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: "{}"}, nil
}
func (stubLLM) Name() string  { return "stub" }
func (stubLLM) Model() string { return "stub-model" }
