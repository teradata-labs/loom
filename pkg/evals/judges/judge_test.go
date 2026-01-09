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
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestNewAgentJudge(t *testing.T) {
	tests := []struct {
		name      string
		agent     *agent.Agent
		config    *loomv1.JudgeConfig
		tracer    observability.Tracer
		expectErr bool
	}{
		{
			name:   "valid agent judge",
			agent:  createMockAgent(),
			config: &loomv1.JudgeConfig{Name: "test-judge"},
			tracer: observability.NewNoOpTracer(),
		},
		{
			name:      "nil agent",
			agent:     nil,
			config:    &loomv1.JudgeConfig{Name: "test"},
			expectErr: true,
		},
		{
			name:      "nil config",
			agent:     createMockAgent(),
			config:    nil,
			expectErr: true,
		},
		{
			name:   "nil tracer (should use NoOp)",
			agent:  createMockAgent(),
			config: &loomv1.JudgeConfig{Name: "test-judge"},
			tracer: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			judge, err := NewAgentJudge(tt.agent, tt.config, tt.tracer)

			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, judge)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, judge)
				assert.NotEmpty(t, judge.ID())
			}
		})
	}
}

func TestAgentJudge_Getters(t *testing.T) {
	config := &loomv1.JudgeConfig{
		Id:          "test-judge-id",
		Name:        "Quality Judge",
		Criteria:    "output quality",
		Weight:      2.5,
		Criticality: loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL,
		Dimensions:  []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY},
	}

	judge, err := NewAgentJudge(createMockAgent(), config, nil)
	require.NoError(t, err)

	// Test ID
	assert.Equal(t, "test-judge-id", judge.ID())

	// Test Name
	assert.Equal(t, "Quality Judge", judge.Name())

	// Test Criteria
	criteria := judge.Criteria()
	require.Len(t, criteria, 1)
	assert.Equal(t, "output quality", criteria[0])

	// Test Weight
	assert.Equal(t, 2.5, judge.Weight())

	// Test Criticality
	assert.Equal(t, loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL, judge.Criticality())

	// Test Dimensions
	dimensions := judge.Dimensions()
	require.Len(t, dimensions, 1)
	assert.Equal(t, loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY, dimensions[0])
}

func TestAgentJudge_Defaults(t *testing.T) {
	config := &loomv1.JudgeConfig{
		Name: "Test Judge",
		// Weight, MinPassingScore, Criticality not set - should use defaults
	}

	judge, err := NewAgentJudge(createMockAgent(), config, nil)
	require.NoError(t, err)

	// Test default weight
	assert.Equal(t, 1.0, judge.Weight())

	// Test default criticality
	assert.Equal(t, loomv1.JudgeCriticality_JUDGE_CRITICALITY_NON_CRITICAL, judge.Criticality())

	// Test default dimensions
	dimensions := judge.Dimensions()
	require.Len(t, dimensions, 1)
	assert.Equal(t, loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY, dimensions[0])

	// Test default criteria
	criteria := judge.Criteria()
	require.Len(t, criteria, 1)
	assert.Equal(t, "agent evaluation", criteria[0])
}

func TestAgentJudge_Evaluate(t *testing.T) {
	config := &loomv1.JudgeConfig{
		Name:     "Test Judge",
		Criteria: "test criteria",
	}

	judge, err := NewAgentJudge(createMockAgent(), config, nil)
	require.NoError(t, err)

	evalCtx := &loomv1.EvaluationContext{
		AgentId:     "test-agent",
		Prompt:      "test prompt",
		Response:    "test response",
		PatternUsed: "test-pattern",
		ToolsUsed:   []string{"tool1", "tool2"},
		CostUsd:     0.05,
		LatencyMs:   1500,
	}

	ctx := context.Background()
	result, err := judge.Evaluate(ctx, evalCtx)

	// AgentJudge.Evaluate() has TODO - returns placeholder
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, judge.ID(), result.JudgeId)
	assert.Equal(t, "Test Judge", result.JudgeName)
	assert.Equal(t, "agent-judge", result.JudgeModel)
	assert.Equal(t, "PASS", result.Verdict)
	assert.Greater(t, result.OverallScore, 0.0)
}

func TestAgentJudge_BuildEvaluationPrompt(t *testing.T) {
	config := &loomv1.JudgeConfig{
		Name:     "Test Judge",
		Criteria: "output correctness and completeness",
	}

	judge, err := NewAgentJudge(createMockAgent(), config, nil)
	require.NoError(t, err)

	evalCtx := &loomv1.EvaluationContext{
		Prompt:      "Generate SQL query",
		Response:    "SELECT * FROM users",
		PatternUsed: "sql-pattern",
		ToolsUsed:   []string{"sql-validator"},
		CostUsd:     0.02,
		LatencyMs:   500,
	}

	prompt := judge.buildEvaluationPrompt(evalCtx)

	// Verify prompt contains all key information
	assert.Contains(t, prompt, "Generate SQL query")
	assert.Contains(t, prompt, "SELECT * FROM users")
	assert.Contains(t, prompt, "sql-pattern")
	assert.Contains(t, prompt, "sql-validator")
	assert.Contains(t, prompt, "0.0200")
	assert.Contains(t, prompt, "500ms")
	assert.Contains(t, prompt, "output correctness and completeness")
	assert.Contains(t, prompt, "Overall score (0-100)")
	assert.Contains(t, prompt, "Verdict (PASS/FAIL/PARTIAL)")
}

func TestHawkJudge_Getters(t *testing.T) {
	// Note: We can't test Evaluate() without Hawk library,
	// but we can test the getters and constructor validation

	config := &loomv1.JudgeConfig{
		Id:              "hawk-judge-id",
		Name:            "Hawk Quality Judge",
		Criteria:        "quality assessment",
		Weight:          3.0,
		MinPassingScore: 85,
		Criticality:     loomv1.JudgeCriticality_JUDGE_CRITICALITY_SAFETY_CRITICAL,
		Dimensions:      []loomv1.JudgeDimension{loomv1.JudgeDimension_JUDGE_DIMENSION_SAFETY},
	}

	// Create mock LLM provider for HawkJudge
	mockLLM := &mockLLMProvider{model: "claude-3-5-sonnet"}

	judge, err := NewHawkJudge(mockLLM, config, nil)
	require.NoError(t, err)
	require.NotNil(t, judge)

	// Test ID
	assert.Equal(t, "hawk-judge-id", judge.ID())

	// Test Name
	assert.Equal(t, "Hawk Quality Judge", judge.Name())

	// Test Criteria
	criteria := judge.Criteria()
	require.Len(t, criteria, 1)
	assert.Equal(t, "quality assessment", criteria[0])

	// Test Weight
	assert.Equal(t, 3.0, judge.Weight())

	// Test Criticality
	assert.Equal(t, loomv1.JudgeCriticality_JUDGE_CRITICALITY_SAFETY_CRITICAL, judge.Criticality())

	// Test Dimensions
	dimensions := judge.Dimensions()
	require.Len(t, dimensions, 1)
	assert.Equal(t, loomv1.JudgeDimension_JUDGE_DIMENSION_SAFETY, dimensions[0])
}

func TestHawkJudge_Defaults(t *testing.T) {
	config := &loomv1.JudgeConfig{
		Name: "Test Hawk Judge",
		// Weight, MinPassingScore, Criticality, Dimensions not set
	}

	mockLLM := &mockLLMProvider{model: "claude-3-5-sonnet"}

	judge, err := NewHawkJudge(mockLLM, config, nil)
	require.NoError(t, err)

	// Test default weight
	assert.Equal(t, 1.0, judge.Weight())

	// Test default criticality (CRITICAL, not NON_CRITICAL like AgentJudge)
	assert.Equal(t, loomv1.JudgeCriticality_JUDGE_CRITICALITY_CRITICAL, judge.Criticality())

	// Test default dimensions
	dimensions := judge.Dimensions()
	require.Len(t, dimensions, 1)
	assert.Equal(t, loomv1.JudgeDimension_JUDGE_DIMENSION_QUALITY, dimensions[0])

	// Test default criteria
	criteria := judge.Criteria()
	assert.Len(t, criteria, 3)
	assert.Contains(t, criteria, "quality")
	assert.Contains(t, criteria, "correctness")
	assert.Contains(t, criteria, "completeness")
}

func TestNewHawkJudge_Validation(t *testing.T) {
	tests := []struct {
		name        string
		llmProvider types.LLMProvider // Use interface type to properly test nil
		config      *loomv1.JudgeConfig
		expectErr   string
	}{
		{
			name:        "nil LLM provider",
			llmProvider: nil, // Truly nil interface
			config:      &loomv1.JudgeConfig{Name: "test"},
			expectErr:   "LLM provider required",
		},
		{
			name:        "nil config",
			llmProvider: &mockLLMProvider{model: "test"},
			config:      nil,
			expectErr:   "judge config required",
		},
		{
			name:        "empty name",
			llmProvider: &mockLLMProvider{model: "test"},
			config:      &loomv1.JudgeConfig{Name: ""},
			expectErr:   "judge name required",
		},
		{
			name:        "valid config",
			llmProvider: &mockLLMProvider{model: "test"},
			config:      &loomv1.JudgeConfig{Name: "valid-judge"},
			expectErr:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			judge, err := NewHawkJudge(tt.llmProvider, tt.config, nil)

			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
				assert.Nil(t, judge)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, judge)
			}
		})
	}
}

func TestHawkJudge_IDGeneration(t *testing.T) {
	config := &loomv1.JudgeConfig{
		Name: "Test Judge",
		// Id not set - should be auto-generated
	}

	mockLLM := &mockLLMProvider{model: "test"}

	judge1, err := NewHawkJudge(mockLLM, config, nil)
	require.NoError(t, err)

	judge2, err := NewHawkJudge(mockLLM, config, nil)
	require.NoError(t, err)

	// Auto-generated IDs should be different
	assert.NotEmpty(t, judge1.ID())
	assert.NotEmpty(t, judge2.ID())
	assert.NotEqual(t, judge1.ID(), judge2.ID())
}

// createMockAgent creates a minimal agent for testing
func createMockAgent() *agent.Agent {
	backend := &NoOpBackend{}
	llmProvider := &mockLLMProvider{model: "test-model"}
	return agent.NewAgent(backend, llmProvider, agent.WithName("test-agent"))
}
