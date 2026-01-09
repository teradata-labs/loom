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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestNewJudgeAgentAdapter(t *testing.T) {
	tests := []struct {
		name      string
		judge     Judge
		evalCtx   *loomv1.EvaluationContext
		tracer    observability.Tracer
		expectErr bool
	}{
		{
			name: "valid adapter",
			judge: &testJudgeImpl{
				id:   "test-judge",
				name: "Test Judge",
			},
			evalCtx: &loomv1.EvaluationContext{
				AgentId: "test-agent",
				Prompt:  "test",
			},
			tracer: observability.NewNoOpTracer(),
		},
		{
			name:  "nil judge",
			judge: nil,
			evalCtx: &loomv1.EvaluationContext{
				AgentId: "test-agent",
			},
			expectErr: true,
		},
		{
			name: "nil eval context",
			judge: &testJudgeImpl{
				id:   "test-judge",
				name: "Test Judge",
			},
			evalCtx:   nil,
			expectErr: true,
		},
		{
			name: "nil tracer (should use NoOp)",
			judge: &testJudgeImpl{
				id:   "test-judge",
				name: "Test Judge",
			},
			evalCtx: &loomv1.EvaluationContext{
				AgentId: "test-agent",
			},
			tracer: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := NewJudgeAgentAdapter(tt.judge, tt.evalCtx, tt.tracer)

			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, adapter)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, adapter)
				assert.NotNil(t, adapter.GetAgent())
				assert.Equal(t, tt.judge, adapter.GetJudge())
			}
		})
	}
}

func TestJudgeAgentAdapter_Chat(t *testing.T) {
	// Create mock judge that returns a verdict
	mockJudge := &testJudgeImpl{
		id:   "test-judge",
		name: "Quality Judge",
		verdict: &loomv1.JudgeResult{
			JudgeId:      "test-judge",
			JudgeName:    "Quality Judge",
			Verdict:      "PASS",
			OverallScore: 95.0,
			Reasoning:    "Excellent output quality",
			JudgedAt:     timestamppb.Now(),
		},
	}

	evalCtx := &loomv1.EvaluationContext{
		AgentId:  "test-agent",
		Prompt:   "test prompt",
		Response: "test response",
	}

	adapter, err := NewJudgeAgentAdapter(mockJudge, evalCtx, nil)
	require.NoError(t, err)

	ctx := context.Background()
	response, err := adapter.Chat(ctx, "test-session", "ignored prompt")

	// Verify response
	require.NoError(t, err)
	assert.NotNil(t, response)
	assert.Contains(t, response.Content, "Quality Judge")
	assert.Contains(t, response.Content, "PASS")
	assert.Contains(t, response.Content, "95.0")
	assert.Contains(t, response.Content, "Excellent output quality")

	// Verify metadata
	assert.NotNil(t, response.Metadata)
	assert.Equal(t, "test-judge", response.Metadata["judge_id"])
	assert.Equal(t, "Quality Judge", response.Metadata["judge_name"])
	assert.Equal(t, "PASS", response.Metadata["verdict"])
	assert.Equal(t, 95.0, response.Metadata["score"])

	// Verify judge_result is valid JSON
	resultJSON, ok := response.Metadata["judge_result"].(string)
	require.True(t, ok)

	var result loomv1.JudgeResult
	err = json.Unmarshal([]byte(resultJSON), &result)
	require.NoError(t, err)
	assert.Equal(t, "test-judge", result.JudgeId)
}

func TestJudgeAgentAdapter_Chat_JudgeError(t *testing.T) {
	// Mock judge that returns an error
	mockJudge := &testJudgeImpl{
		id:   "failing-judge",
		name: "Failing Judge",
		err:  assert.AnError,
	}

	evalCtx := &loomv1.EvaluationContext{
		AgentId: "test-agent",
	}

	adapter, err := NewJudgeAgentAdapter(mockJudge, evalCtx, nil)
	require.NoError(t, err)

	ctx := context.Background()
	response, err := adapter.Chat(ctx, "test-session", "")

	// Should return error
	require.Error(t, err)
	assert.NotNil(t, response)
	assert.Contains(t, response.Content, "Judge evaluation failed")
	assert.Contains(t, response.Metadata["error"], err.Error())
}

func TestGetJudgeResult(t *testing.T) {
	tests := []struct {
		name      string
		response  *agent.Response
		expectErr bool
	}{
		{
			name: "valid judge result",
			response: &agent.Response{
				Content: "test",
				Metadata: map[string]interface{}{
					"judge_result": `{"judge_id":"test","overall_score":90.0}`,
				},
			},
		},
		{
			name:      "nil response",
			response:  nil,
			expectErr: true,
		},
		{
			name: "nil metadata",
			response: &agent.Response{
				Content:  "test",
				Metadata: nil,
			},
			expectErr: true,
		},
		{
			name: "missing judge_result",
			response: &agent.Response{
				Content: "test",
				Metadata: map[string]interface{}{
					"other": "data",
				},
			},
			expectErr: true,
		},
		{
			name: "invalid JSON",
			response: &agent.Response{
				Content: "test",
				Metadata: map[string]interface{}{
					"judge_result": "not valid json",
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetJudgeResult(tt.response)

			if tt.expectErr {
				require.Error(t, err)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestNoOpBackend(t *testing.T) {
	backend := &NoOpBackend{}
	ctx := context.Background()

	// Test Name
	assert.Equal(t, "noop", backend.Name())

	// Test ExecuteQuery - should error
	_, err := backend.ExecuteQuery(ctx, "SELECT 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judges don't execute queries")

	// Test GetSchema - should error
	_, err = backend.GetSchema(ctx, "table")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judges don't access schema")

	// Test ListResources - should error
	_, err = backend.ListResources(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judges don't list resources")

	// Test GetMetadata - should return noop marker
	metadata, err := backend.GetMetadata(ctx, "resource")
	require.NoError(t, err)
	assert.Equal(t, "noop", metadata["type"])

	// Test Ping - should succeed
	err = backend.Ping(ctx)
	require.NoError(t, err)

	// Test Capabilities - should return empty capabilities
	caps := backend.Capabilities()
	assert.NotNil(t, caps)

	// Test ExecuteCustomOperation - should error
	_, err = backend.ExecuteCustomOperation(ctx, "op", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "custom operations not supported")

	// Test Close - should succeed
	err = backend.Close()
	require.NoError(t, err)
}

func TestNoOpLLMProvider(t *testing.T) {
	provider := &NoOpLLMProvider{judgeName: "TestJudge"}
	ctx := context.Background()

	// Test Name
	assert.Equal(t, "judge", provider.Name())

	// Test Model
	assert.Equal(t, "judge-TestJudge", provider.Model())

	// Test Chat - should error
	_, err := provider.Chat(ctx, []types.Message{}, []shuttle.Tool{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judges don't call LLM chat")
}

// mockLLMProvider is a minimal LLM provider for testing
type mockLLMProvider struct {
	model string
}

func (m *mockLLMProvider) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{
		Content: "mock response",
	}, nil
}

func (m *mockLLMProvider) Name() string {
	return "mock"
}

func (m *mockLLMProvider) Model() string {
	return m.model
}

// testJudgeImpl is an enhanced mock judge for testing that supports verdict and error injection
type testJudgeImpl struct {
	id          string
	name        string
	weight      float64
	criticality loomv1.JudgeCriticality
	dimensions  []loomv1.JudgeDimension
	verdict     *loomv1.JudgeResult
	err         error
}

func (t *testJudgeImpl) ID() string {
	return t.id
}

func (t *testJudgeImpl) Name() string {
	return t.name
}

func (t *testJudgeImpl) Weight() float64 {
	if t.weight == 0 {
		return 1.0
	}
	return t.weight
}

func (t *testJudgeImpl) Criticality() loomv1.JudgeCriticality {
	return t.criticality
}

func (t *testJudgeImpl) Criteria() []string {
	return []string{"test criteria"}
}

func (t *testJudgeImpl) Dimensions() []loomv1.JudgeDimension {
	return t.dimensions
}

func (t *testJudgeImpl) Config() *loomv1.JudgeConfig {
	return &loomv1.JudgeConfig{
		Id:          t.id,
		Name:        t.name,
		Weight:      t.weight,
		Criticality: t.criticality,
		Dimensions:  t.dimensions,
	}
}

func (t *testJudgeImpl) Evaluate(ctx context.Context, evalCtx *loomv1.EvaluationContext) (*loomv1.JudgeResult, error) {
	if t.err != nil {
		return nil, t.err
	}
	return t.verdict, nil
}

// Ensure NoOpBackend implements fabric.ExecutionBackend
var _ fabric.ExecutionBackend = (*NoOpBackend)(nil)

// Ensure NoOpLLMProvider implements types.LLMProvider
var _ types.LLMProvider = (*NoOpLLMProvider)(nil)

// Ensure mockLLMProvider implements types.LLMProvider
var _ types.LLMProvider = (*mockLLMProvider)(nil)

// Ensure testJudgeImpl implements Judge
var _ Judge = (*testJudgeImpl)(nil)
