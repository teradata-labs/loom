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
package azureopenai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config with API key",
			config: Config{
				Endpoint:     "https://myresource.openai.azure.com",
				DeploymentID: "gpt-4o-deployment",
				APIKey:       "test-key",
			},
			wantErr: false,
		},
		{
			name: "valid config with Entra token",
			config: Config{
				Endpoint:     "https://myresource.openai.azure.com",
				DeploymentID: "gpt-4o-deployment",
				EntraToken:   "Bearer token",
			},
			wantErr: false,
		},
		{
			name: "missing endpoint",
			config: Config{
				DeploymentID: "gpt-4o-deployment",
				APIKey:       "test-key",
			},
			wantErr: true,
			errMsg:  "endpoint is required",
		},
		{
			name: "missing deployment ID",
			config: Config{
				Endpoint: "https://myresource.openai.azure.com",
				APIKey:   "test-key",
			},
			wantErr: true,
			errMsg:  "deployment ID is required",
		},
		{
			name: "missing authentication",
			config: Config{
				Endpoint:     "https://myresource.openai.azure.com",
				DeploymentID: "gpt-4o-deployment",
			},
			wantErr: true,
			errMsg:  "either APIKey or EntraToken must be provided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
				assert.Equal(t, "azure-openai", client.Name())
				assert.Equal(t, tt.config.DeploymentID, client.Model())
			}
		})
	}
}

func TestClient_Name(t *testing.T) {
	client, err := NewClient(Config{
		Endpoint:     "https://test.openai.azure.com",
		DeploymentID: "test-deployment",
		APIKey:       "test-key",
	})
	require.NoError(t, err)
	assert.Equal(t, "azure-openai", client.Name())
}

func TestClient_Model(t *testing.T) {
	client, err := NewClient(Config{
		Endpoint:     "https://test.openai.azure.com",
		DeploymentID: "my-gpt4-deployment",
		APIKey:       "test-key",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-gpt4-deployment", client.Model())
}

func TestInferModelFromDeployment(t *testing.T) {
	tests := []struct {
		name         string
		deploymentID string
		want         string
	}{
		{"gpt-4o deployment", "gpt-4o-deployment", "gpt-4o"},
		{"gpt-4o-mini deployment", "my-gpt-4o-mini", "gpt-4o-mini"},
		{"gpt-4-turbo deployment", "gpt-4-turbo-prod", "gpt-4-turbo"},
		{"gpt-4 deployment", "prod-gpt-4", "gpt-4"},
		{"gpt-35-turbo deployment", "gpt-35-turbo-test", "gpt-35-turbo"},
		{"unknown deployment", "custom-model-123", "custom-model-123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferModelFromDeployment(tt.deploymentID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClient_Chat_WithAPIKey(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Azure-specific URL structure
		assert.Contains(t, r.URL.Path, "/openai/deployments/")
		assert.Contains(t, r.URL.Path, "/chat/completions")
		assert.Contains(t, r.URL.RawQuery, "api-version=")

		// Verify API key authentication
		assert.Equal(t, "test-api-key", r.Header.Get("api-key"))
		assert.Empty(t, r.Header.Get("Authorization"))

		// Send mock response
		resp := openai.ChatCompletionResponse{
			ID:      "chatcmpl-azure-123",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   "gpt-4o",
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatMessage{
						Role:    "assistant",
						Content: "Hello from Azure OpenAI!",
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.ChatCompletionUsage{
				PromptTokens:     15,
				CompletionTokens: 10,
				TotalTokens:      25,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with mock server
	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "gpt-4o-deployment",
		APIKey:       "test-api-key",
		ModelName:    "gpt-4o",
	})
	require.NoError(t, err)

	// Test chat
	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "Hello from Azure OpenAI!", resp.Content)
	assert.Equal(t, "end_turn", resp.StopReason)
	assert.Equal(t, 15, resp.Usage.InputTokens)
	assert.Equal(t, 10, resp.Usage.OutputTokens)
	assert.Greater(t, resp.Usage.CostUSD, 0.0)
}

func TestClient_Chat_WithEntraToken(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Entra ID authentication
		assert.Equal(t, "Bearer test-entra-token", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("api-key"))

		// Send mock response
		resp := openai.ChatCompletionResponse{
			ID:    "chatcmpl-azure-456",
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionChoice{
				{
					Message: openai.ChatMessage{
						Role:    "assistant",
						Content: "Authenticated with Entra ID",
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.ChatCompletionUsage{
				PromptTokens:     20,
				CompletionTokens: 15,
				TotalTokens:      35,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with Entra ID auth
	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "gpt-4o-deployment",
		EntraToken:   "test-entra-token",
		ModelName:    "gpt-4o",
	})
	require.NoError(t, err)

	// Test chat
	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "Authenticated with Entra ID", resp.Content)
}

func TestClient_Chat_WithTools(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse request
		var req openai.ChatCompletionRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		// Verify tools were sent
		assert.Len(t, req.Tools, 1)
		assert.Equal(t, "get_weather", req.Tools[0].Function.Name)

		// Send tool call response
		resp := openai.ChatCompletionResponse{
			ID:    "chatcmpl-azure-789",
			Model: "gpt-4o",
			Choices: []openai.ChatCompletionChoice{
				{
					Message: openai.ChatMessage{
						Role: "assistant",
						ToolCalls: []openai.ToolCall{
							{
								ID:   "call_abc",
								Type: "function",
								Function: openai.FunctionCall{
									Name:      "get_weather",
									Arguments: `{"location":"Seattle"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: openai.ChatCompletionUsage{
				PromptTokens:     30,
				CompletionTokens: 20,
				TotalTokens:      50,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client
	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "gpt-4o-deployment",
		APIKey:       "test-key",
		ModelName:    "gpt-4o",
	})
	require.NoError(t, err)

	// Create mock tool
	mockTool := &mockShuttleTool{
		name:        "get_weather",
		description: "Get weather",
		schema: &shuttle.JSONSchema{
			Type: "object",
			Properties: map[string]*shuttle.JSONSchema{
				"location": {Type: "string"},
			},
			Required: []string{"location"},
		},
	}

	// Test chat with tools
	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{
		{Role: "user", Content: "What's the weather in Seattle?"},
	}

	resp, err := client.Chat(ctx, messages, []shuttle.Tool{mockTool})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_abc", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.Equal(t, "Seattle", resp.ToolCalls[0].Input["location"])
}

func TestClient_CalculateCost(t *testing.T) {
	tests := []struct {
		name         string
		modelName    string
		inputTokens  int
		outputTokens int
		wantMin      float64
		wantMax      float64
	}{
		{
			name:         "gpt-4o",
			modelName:    "gpt-4o",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.007,
			wantMax:      0.008,
		},
		{
			name:         "gpt-4o-mini",
			modelName:    "gpt-4o-mini",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.0004,
			wantMax:      0.0005,
		},
		{
			name:         "gpt-35-turbo (Azure naming)",
			modelName:    "gpt-35-turbo",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.0012,
			wantMax:      0.0013,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(Config{
				Endpoint:     "https://test.openai.azure.com",
				DeploymentID: "test-deployment",
				APIKey:       "test-key",
				ModelName:    tt.modelName,
			})
			require.NoError(t, err)

			got := client.calculateCost(tt.inputTokens, tt.outputTokens)
			assert.GreaterOrEqual(t, got, tt.wantMin)
			assert.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

func TestClient_DeploymentURL(t *testing.T) {
	// Verify correct Azure URL structure
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check URL pattern
		expectedPath := "/openai/deployments/my-gpt4-deployment/chat/completions"
		assert.Equal(t, expectedPath, r.URL.Path)

		// Check API version query parameter
		assert.Equal(t, "2024-10-21", r.URL.Query().Get("api-version"))

		// Send minimal valid response
		resp := openai.ChatCompletionResponse{
			ID:    "test",
			Model: "gpt-4",
			Choices: []openai.ChatCompletionChoice{
				{
					Message:      openai.ChatMessage{Role: "assistant", Content: "test"},
					FinishReason: "stop",
				},
			},
			Usage: openai.ChatCompletionUsage{
				PromptTokens:     10,
				CompletionTokens: 10,
				TotalTokens:      20,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "my-gpt4-deployment",
		APIKey:       "test-key",
		APIVersion:   "2024-10-21",
	})
	require.NoError(t, err)

	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{{Role: "user", Content: "test"}}

	_, err = client.Chat(ctx, messages, nil)
	assert.NoError(t, err)
}

func TestClient_APIError(t *testing.T) {
	// Mock server returning error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletionResponse{
			Error: &openai.OpenAIError{
				Message: "Invalid deployment",
				Type:    "invalid_request_error",
				Code:    "DeploymentNotFound",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Endpoint:     server.URL,
		DeploymentID: "nonexistent-deployment",
		APIKey:       "test-key",
	})
	require.NoError(t, err)

	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{{Role: "user", Content: "test"}}

	resp, err := client.Chat(ctx, messages, nil)
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "Invalid deployment")
}

// Mock implementations for testing

type mockContext struct {
	context.Context
}

func (m *mockContext) Session() *types.Session {
	return &types.Session{ID: "test-session"}
}

func (m *mockContext) Tracer() observability.Tracer {
	return observability.NewNoOpTracer()
}

func (m *mockContext) ProgressCallback() types.ProgressCallback {
	return nil
}

type mockShuttleTool struct {
	name        string
	description string
	schema      *shuttle.JSONSchema
}

func (m *mockShuttleTool) Name() string {
	return m.name
}

func (m *mockShuttleTool) Description() string {
	return m.description
}

func (m *mockShuttleTool) InputSchema() *shuttle.JSONSchema {
	return m.schema
}

func (m *mockShuttleTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{
		Success: true,
		Data:    map[string]interface{}{"result": "ok"},
	}, nil
}

func (m *mockShuttleTool) Backend() string {
	return ""
}

func TestConvertTools_WithNilProperties(t *testing.T) {
	// Test that object schemas with nil properties are converted to empty properties maps
	// This is required by Azure OpenAI - object types must always have a properties field
	mockTool := &mockShuttleTool{
		name:        "manage_ephemeral_agents",
		description: "Manage ephemeral agents",
		schema: &shuttle.JSONSchema{
			Type: "object",
			Properties: map[string]*shuttle.JSONSchema{
				"command": {
					Type:        "string",
					Description: "Command to execute",
				},
				"metadata": {
					Type:        "object",
					Description: "Optional metadata",
					Properties:  nil, // This should be converted to empty map
				},
			},
			Required: []string{"command"},
		},
	}

	nameMap := make(map[string]string)
	apiTools := convertTools([]shuttle.Tool{mockTool}, nameMap)

	require.Len(t, apiTools, 1)
	tool := apiTools[0]

	// Verify function definition
	assert.Equal(t, "function", tool.Type)
	assert.Equal(t, "manage_ephemeral_agents", tool.Function.Name)
	assert.Equal(t, "Manage ephemeral agents", tool.Function.Description)

	// Verify parameters
	params := tool.Function.Parameters
	assert.Equal(t, "object", params["type"])

	// Verify properties exist
	properties, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)

	// Verify command property
	command, ok := properties["command"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "string", command["type"])

	// Verify metadata property has empty properties map (not nil)
	metadata, ok := properties["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "object", metadata["type"])

	// OpenAI behavior: properties field is OMITTED (not present) when nil
	_, hasProps := metadata["properties"]
	assert.False(t, hasProps, "metadata should NOT have properties field when nil (OpenAI behavior)")

	// Verify required field
	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"command"}, required)
}

func TestConvertTools_TopLevelNilProperties(t *testing.T) {
	// Test that top-level object schema with nil properties gets empty properties map
	mockTool := &mockShuttleTool{
		name:        "empty_object_tool",
		description: "Tool with empty object schema",
		schema: &shuttle.JSONSchema{
			Type:       "object",
			Properties: nil, // Top-level nil properties
		},
	}

	nameMap := make(map[string]string)
	apiTools := convertTools([]shuttle.Tool{mockTool}, nameMap)

	require.Len(t, apiTools, 1)
	tool := apiTools[0]

	// Verify parameters
	params := tool.Function.Parameters
	assert.Equal(t, "object", params["type"])

	// OpenAI behavior: properties field is OMITTED (not present) when nil
	_, hasProps := params["properties"]
	assert.False(t, hasProps, "top-level object should NOT have properties field when nil (OpenAI behavior)")
}
