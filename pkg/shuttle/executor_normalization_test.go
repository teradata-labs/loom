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
package shuttle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test tool with camelCase schema
type camelCaseTool struct {
	receivedParams map[string]interface{}
}

func (t *camelCaseTool) Name() string {
	return "camel_case_tool"
}

func (t *camelCaseTool) Description() string {
	return "Tool with camelCase parameters"
}

func (t *camelCaseTool) InputSchema() *JSONSchema {
	return &JSONSchema{
		Type: "object",
		Properties: map[string]*JSONSchema{
			"userId": {
				Type:        "string",
				Description: "User identifier",
			},
			"errorId": {
				Type:        "string",
				Description: "Error identifier",
			},
			"referenceId": {
				Type:        "string",
				Description: "Reference identifier",
			},
		},
		Required: []string{"userId"},
	}
}

func (t *camelCaseTool) Execute(ctx context.Context, params map[string]interface{}) (*Result, error) {
	t.receivedParams = params
	return &Result{
		Success: true,
		Data:    params,
	}, nil
}

func (t *camelCaseTool) Backend() string {
	return "test"
}

func TestExecutor_ParameterNormalization_SnakeToCamel(t *testing.T) {
	tool := &camelCaseTool{}
	registry := NewRegistry()
	registry.Register(tool)

	executor := NewExecutor(registry)

	// LLM passes snake_case parameters
	params := map[string]interface{}{
		"user_id":      "user123",
		"error_id":     "err_456",
		"reference_id": "ref_789",
	}

	result, err := executor.Execute(context.Background(), "camel_case_tool", params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify tool received camelCase parameters
	assert.Equal(t, "user123", tool.receivedParams["userId"], "userId should be normalized to camelCase")
	assert.Equal(t, "err_456", tool.receivedParams["errorId"], "errorId should be normalized to camelCase")
	assert.Equal(t, "ref_789", tool.receivedParams["referenceId"], "referenceId should be normalized to camelCase")

	// Verify snake_case params are NOT present
	assert.NotContains(t, tool.receivedParams, "user_id", "snake_case param should not be present")
	assert.NotContains(t, tool.receivedParams, "error_id", "snake_case param should not be present")
	assert.NotContains(t, tool.receivedParams, "reference_id", "snake_case param should not be present")
}

func TestExecutor_ParameterNormalization_CamelToCamel(t *testing.T) {
	tool := &camelCaseTool{}
	registry := NewRegistry()
	registry.Register(tool)

	executor := NewExecutor(registry)

	// LLM passes camelCase parameters (correct format)
	params := map[string]interface{}{
		"userId":      "user123",
		"errorId":     "err_456",
		"referenceId": "ref_789",
	}

	result, err := executor.Execute(context.Background(), "camel_case_tool", params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify tool received the same camelCase parameters
	assert.Equal(t, "user123", tool.receivedParams["userId"])
	assert.Equal(t, "err_456", tool.receivedParams["errorId"])
	assert.Equal(t, "ref_789", tool.receivedParams["referenceId"])
}

func TestExecutor_ParameterNormalization_MixedCase(t *testing.T) {
	tool := &camelCaseTool{}
	registry := NewRegistry()
	registry.Register(tool)

	executor := NewExecutor(registry)

	// LLM passes mixed snake_case and camelCase
	params := map[string]interface{}{
		"userId":       "user123", // camelCase (matches schema)
		"error_id":     "err_456", // snake_case (needs normalization)
		"reference_id": "ref_789", // snake_case (needs normalization)
	}

	result, err := executor.Execute(context.Background(), "camel_case_tool", params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify all parameters are normalized to camelCase
	assert.Equal(t, "user123", tool.receivedParams["userId"])
	assert.Equal(t, "err_456", tool.receivedParams["errorId"])
	assert.Equal(t, "ref_789", tool.receivedParams["referenceId"])
}

func TestExecutor_ParameterNormalization_NoSchema(t *testing.T) {
	// Tool with no schema
	tool := &mockTool{
		name:        "no_schema_tool",
		description: "Tool without schema",
	}
	registry := NewRegistry()
	registry.Register(tool)

	executor := NewExecutor(registry)

	// Parameters should pass through unchanged
	params := map[string]interface{}{
		"user_id": "user123",
		"userId":  "user456",
	}

	result, err := executor.Execute(context.Background(), "no_schema_tool", params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Both parameters should be preserved (no normalization without schema)
	resultData := result.Data.(map[string]interface{})
	assert.Equal(t, "user123", resultData["user_id"])
	assert.Equal(t, "user456", resultData["userId"])
}

func TestExecutor_ParameterNormalization_UnknownParameter(t *testing.T) {
	tool := &camelCaseTool{}
	registry := NewRegistry()
	registry.Register(tool)

	executor := NewExecutor(registry)

	// Include a parameter not in schema
	params := map[string]interface{}{
		"user_id":        "user123",      // Known, will be normalized
		"unknown_param":  "unknown",      // Unknown, will pass through as-is
		"anotherUnknown": "also_unknown", // Unknown, will pass through as-is
	}

	result, err := executor.Execute(context.Background(), "camel_case_tool", params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Known parameter normalized
	assert.Equal(t, "user123", tool.receivedParams["userId"])

	// Unknown parameters passed through unchanged
	assert.Equal(t, "unknown", tool.receivedParams["unknown_param"])
	assert.Equal(t, "also_unknown", tool.receivedParams["anotherUnknown"])
}
