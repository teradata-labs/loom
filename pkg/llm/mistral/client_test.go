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
package mistral

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
		name   string
		config Config
		want   string // expected model
	}{
		{
			name: "with defaults",
			config: Config{
				APIKey: "test-key",
			},
			want: "mistral-large-latest",
		},
		{
			name: "with custom model",
			config: Config{
				APIKey: "test-key",
				Model:  "mistral-small-latest",
			},
			want: "mistral-small-latest",
		},
		{
			name: "with open model",
			config: Config{
				APIKey: "test-key",
				Model:  "open-mixtral-8x7b",
			},
			want: "open-mixtral-8x7b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)
			assert.NotNil(t, client)
			assert.Equal(t, "mistral", client.Name())
			assert.Equal(t, tt.want, client.Model())
		})
	}
}

func TestClient_Name(t *testing.T) {
	client := NewClient(Config{APIKey: "test"})
	assert.Equal(t, "mistral", client.Name())
}

func TestClient_Model(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"default model", "", "mistral-large-latest"},
		{"custom model", "mistral-small-latest", "mistral-small-latest"},
		{"open model", "open-mistral-7b", "open-mistral-7b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(Config{
				APIKey: "test",
				Model:  tt.model,
			})
			assert.Equal(t, tt.want, client.Model())
		})
	}
}

func TestClient_Chat_Success(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Mistral endpoint is being used
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer test-key")

		// Parse request body
		var req openai.ChatCompletionRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Equal(t, "mistral-large-latest", req.Model)
		assert.Greater(t, len(req.Messages), 0)

		// Send mock response
		resp := openai.ChatCompletionResponse{
			ID:      "chatcmpl-mistral-123",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   "mistral-large-latest",
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatMessage{
						Role:    "assistant",
						Content: "Hello from Mistral AI!",
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.ChatCompletionUsage{
				PromptTokens:     20,
				CompletionTokens: 10,
				TotalTokens:      30,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with mock server
	// We need to create a modified config that uses the test server
	client := NewClient(Config{
		APIKey: "test-key",
		Model:  "mistral-large-latest",
	})
	// Override the endpoint for testing
	client.openai = openai.NewClient(openai.Config{
		APIKey:   "test-key",
		Model:    "mistral-large-latest",
		Endpoint: server.URL,
	})

	// Test chat
	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "Hello from Mistral AI!", resp.Content)
	assert.Equal(t, "end_turn", resp.StopReason)
	assert.Equal(t, 20, resp.Usage.InputTokens)
	assert.Equal(t, 10, resp.Usage.OutputTokens)
	assert.Greater(t, resp.Usage.CostUSD, 0.0)

	// Verify Mistral provider metadata
	assert.Equal(t, "mistral", resp.Metadata["provider"])
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
			ID:    "chatcmpl-mistral-456",
			Model: "mistral-large-latest",
			Choices: []openai.ChatCompletionChoice{
				{
					Message: openai.ChatMessage{
						Role: "assistant",
						ToolCalls: []openai.ToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: openai.FunctionCall{
									Name:      "get_weather",
									Arguments: `{"location":"Paris"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: openai.ChatCompletionUsage{
				PromptTokens:     30,
				CompletionTokens: 15,
				TotalTokens:      45,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client
	client := NewClient(Config{
		APIKey: "test-key",
		Model:  "mistral-large-latest",
	})
	client.openai = openai.NewClient(openai.Config{
		APIKey:   "test-key",
		Model:    "mistral-large-latest",
		Endpoint: server.URL,
	})

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
		{Role: "user", Content: "What's the weather in Paris?"},
	}

	resp, err := client.Chat(ctx, messages, []shuttle.Tool{mockTool})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_123", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.Equal(t, "Paris", resp.ToolCalls[0].Input["location"])
}

func TestClient_CalculateCost(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		inputTokens  int
		outputTokens int
		wantMin      float64
		wantMax      float64
	}{
		{
			name:         "open-mistral-7b",
			model:        "open-mistral-7b",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.000370, // (1000 * 0.25 + 500 * 0.25) / 1M
			wantMax:      0.000380,
		},
		{
			name:         "open-mixtral-8x7b",
			model:        "open-mixtral-8x7b",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.001040, // (1000 * 0.70 + 500 * 0.70) / 1M
			wantMax:      0.001060,
		},
		{
			name:         "mistral-small-latest",
			model:        "mistral-small-latest",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.002490, // (1000 * 1.00 + 500 * 3.00) / 1M
			wantMax:      0.002510,
		},
		{
			name:         "mistral-large-latest",
			model:        "mistral-large-latest",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.009990, // (1000 * 4.00 + 500 * 12.00) / 1M
			wantMax:      0.010010,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(Config{
				APIKey: "test",
				Model:  tt.model,
			})
			got := client.calculateCost(tt.inputTokens, tt.outputTokens)
			assert.GreaterOrEqual(t, got, tt.wantMin)
			assert.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

func TestClient_APIError(t *testing.T) {
	// Mock server returning error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletionResponse{
			Error: &openai.OpenAIError{
				Message: "Invalid API key",
				Type:    "invalid_request_error",
				Code:    "invalid_api_key",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey: "invalid-key",
	})
	client.openai = openai.NewClient(openai.Config{
		APIKey:   "invalid-key",
		Model:    "mistral-large-latest",
		Endpoint: server.URL,
	})

	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "Invalid API key")
}

func TestClient_ModelVariants(t *testing.T) {
	models := []string{
		"open-mistral-7b",
		"open-mixtral-8x7b",
		"open-mixtral-8x22b",
		"mistral-small-latest",
		"mistral-medium-latest",
		"mistral-large-latest",
		"mistral-tiny-2312",  // Legacy
		"mistral-small-2312", // Legacy
		"mistral-small-2402", // Specific version
		"mistral-large-2402", // Specific version
		"mistral-large-2407", // Specific version
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			client := NewClient(Config{
				APIKey: "test",
				Model:  model,
			})
			assert.Equal(t, model, client.Model())

			// Verify cost calculation works for all models
			cost := client.calculateCost(1000, 500)
			assert.Greater(t, cost, 0.0, "Cost should be positive for model %s", model)
		})
	}
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
