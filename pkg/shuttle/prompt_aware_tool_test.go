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
package shuttle_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockTool is a test implementation of Tool interface
type mockTool struct {
	name        string
	description string
	backend     string
	schema      *shuttle.JSONSchema
	executeFunc func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error)
}

func (m *mockTool) Name() string {
	return m.name
}

func (m *mockTool) Description() string {
	return m.description
}

func (m *mockTool) InputSchema() *shuttle.JSONSchema {
	return m.schema
}

func (m *mockTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, params)
	}
	return &shuttle.Result{
		Success: true,
		Data:    "executed",
	}, nil
}

func (m *mockTool) Backend() string {
	return m.backend
}

// mockRegistry is a test implementation of PromptRegistry
type mockRegistry struct {
	getFunc func(ctx context.Context, key string, vars map[string]interface{}) (string, error)
}

func (m *mockRegistry) Get(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, key, vars)
	}
	return "", fmt.Errorf("prompt not found")
}

func (m *mockRegistry) GetWithVariant(ctx context.Context, key string, variant string, vars map[string]interface{}) (string, error) {
	return m.Get(ctx, key, vars)
}

func (m *mockRegistry) GetMetadata(ctx context.Context, key string) (*prompts.PromptMetadata, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockRegistry) List(ctx context.Context, filters map[string]string) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockRegistry) Reload(ctx context.Context) error {
	return nil
}

func (m *mockRegistry) Watch(ctx context.Context) (<-chan prompts.PromptUpdate, error) {
	ch := make(chan prompts.PromptUpdate)
	close(ch)
	return ch, nil
}

func TestPromptAwareTool_Description(t *testing.T) {
	tests := []struct {
		name         string
		toolDesc     string
		promptDesc   string
		promptErr    error
		expectedDesc string
	}{
		{
			name:         "uses prompt when available",
			toolDesc:     "Native description",
			promptDesc:   "Prompt-based description with details",
			expectedDesc: "Prompt-based description with details",
		},
		{
			name:         "falls back to tool when prompt not found",
			toolDesc:     "Native description",
			promptErr:    fmt.Errorf("prompt not found"),
			expectedDesc: "Native description",
		},
		{
			name:         "falls back to tool when prompt empty",
			toolDesc:     "Native description",
			promptDesc:   "",
			expectedDesc: "Native description",
		},
		{
			name:         "uses prompt with empty native description",
			toolDesc:     "",
			promptDesc:   "Prompt-based description",
			expectedDesc: "Prompt-based description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock tool
			mockTool := &mockTool{
				name:        "test_tool",
				description: tt.toolDesc,
			}

			// Create mock registry
			mockReg := &mockRegistry{
				getFunc: func(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
					if tt.promptErr != nil {
						return "", tt.promptErr
					}
					return tt.promptDesc, nil
				},
			}

			// Wrap with PromptAwareTool
			wrapped := shuttle.NewPromptAwareTool(mockTool, mockReg, "tools.test")

			// Assert description
			assert.Equal(t, tt.expectedDesc, wrapped.Description())
		})
	}
}

func TestPromptAwareTool_NilRegistry(t *testing.T) {
	mockTool := &mockTool{
		name:        "test_tool",
		description: "Native description",
	}

	// Nil registry should return original tool (no wrapping)
	wrapped := shuttle.NewPromptAwareTool(mockTool, nil, "tools.test")
	assert.Equal(t, mockTool, wrapped, "Should return unwrapped tool when registry is nil")
}

func TestPromptAwareTool_Name(t *testing.T) {
	mockTool := &mockTool{
		name:        "my_tool",
		description: "Test tool",
	}

	mockReg := &mockRegistry{
		getFunc: func(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
			return "From registry", nil
		},
	}

	wrapped := shuttle.NewPromptAwareTool(mockTool, mockReg, "tools.my_tool")

	// Name should delegate to wrapped tool
	assert.Equal(t, "my_tool", wrapped.Name())
}

func TestPromptAwareTool_InputSchema(t *testing.T) {
	schema := &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"input": {
				Type:        "string",
				Description: "Test input",
			},
		},
	}

	mockTool := &mockTool{
		name:        "test_tool",
		description: "Test tool",
		schema:      schema,
	}

	mockReg := &mockRegistry{
		getFunc: func(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
			return "From registry", nil
		},
	}

	wrapped := shuttle.NewPromptAwareTool(mockTool, mockReg, "tools.test")

	// InputSchema should delegate to wrapped tool
	assert.Equal(t, schema, wrapped.InputSchema())
}

func TestPromptAwareTool_Execute(t *testing.T) {
	called := false
	mockTool := &mockTool{
		name:        "test_tool",
		description: "Test tool",
		executeFunc: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			called = true
			return &shuttle.Result{
				Success: true,
				Data:    "result",
			}, nil
		},
	}

	mockReg := &mockRegistry{
		getFunc: func(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
			return "From registry", nil
		},
	}

	wrapped := shuttle.NewPromptAwareTool(mockTool, mockReg, "tools.test")

	// Execute should delegate to wrapped tool
	result, err := wrapped.Execute(context.Background(), map[string]interface{}{"input": "test"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "result", result.Data)
	assert.True(t, called, "Execute should be called on wrapped tool")
}

func TestPromptAwareTool_Execute_Error(t *testing.T) {
	mockTool := &mockTool{
		name:        "test_tool",
		description: "Test tool",
		executeFunc: func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "EXECUTION_FAILED",
					Message: "execution failed",
				},
			}, nil
		},
	}

	mockReg := &mockRegistry{
		getFunc: func(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
			return "From registry", nil
		},
	}

	wrapped := shuttle.NewPromptAwareTool(mockTool, mockReg, "tools.test")

	// Execute should propagate errors from wrapped tool
	result, err := wrapped.Execute(context.Background(), map[string]interface{}{"input": "test"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "EXECUTION_FAILED", result.Error.Code)
}

func TestPromptAwareTool_DescriptionCaching(t *testing.T) {
	// Test that Description() calls registry each time (no internal caching)
	callCount := 0
	mockTool := &mockTool{
		name:        "test_tool",
		description: "Native description",
	}

	mockReg := &mockRegistry{
		getFunc: func(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
			callCount++
			return fmt.Sprintf("Call %d", callCount), nil
		},
	}

	wrapped := shuttle.NewPromptAwareTool(mockTool, mockReg, "tools.test")

	// Call Description() multiple times
	desc1 := wrapped.Description()
	desc2 := wrapped.Description()

	// Should call registry each time (caching is registry's responsibility)
	assert.Equal(t, "Call 1", desc1)
	assert.Equal(t, "Call 2", desc2)
	assert.Equal(t, 2, callCount, "Should call registry twice")
}

func TestPromptAwareTool_ContextCancellation(t *testing.T) {
	mockTool := &mockTool{
		name:        "test_tool",
		description: "Native description",
	}

	mockReg := &mockRegistry{
		getFunc: func(ctx context.Context, key string, vars map[string]interface{}) (string, error) {
			// Simulate context cancellation
			return "", context.Canceled
		},
	}

	wrapped := shuttle.NewPromptAwareTool(mockTool, mockReg, "tools.test")

	// Should fall back to native description on context cancellation
	desc := wrapped.Description()
	assert.Equal(t, "Native description", desc)
}
