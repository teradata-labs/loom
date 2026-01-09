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
package huggingface

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
				Token: "test-token",
			},
			want: "meta-llama/Meta-Llama-3.1-70B-Instruct",
		},
		{
			name: "with custom model",
			config: Config{
				Token: "test-token",
				Model: "meta-llama/Meta-Llama-3.1-8B-Instruct",
			},
			want: "meta-llama/Meta-Llama-3.1-8B-Instruct",
		},
		{
			name: "with mixtral model",
			config: Config{
				Token: "test-token",
				Model: "mistralai/Mixtral-8x7B-Instruct-v0.1",
			},
			want: "mistralai/Mixtral-8x7B-Instruct-v0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)
			assert.NotNil(t, client)
			assert.Equal(t, "huggingface", client.Name())
			assert.Equal(t, tt.want, client.Model())
		})
	}
}

func TestClient_Name(t *testing.T) {
	client := NewClient(Config{Token: "test"})
	assert.Equal(t, "huggingface", client.Name())
}

func TestClient_Model(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"default model", "", "meta-llama/Meta-Llama-3.1-70B-Instruct"},
		{"llama 8b", "meta-llama/Meta-Llama-3.1-8B-Instruct", "meta-llama/Meta-Llama-3.1-8B-Instruct"},
		{"mixtral", "mistralai/Mixtral-8x7B-Instruct-v0.1", "mistralai/Mixtral-8x7B-Instruct-v0.1"},
		{"qwen", "Qwen/Qwen2.5-72B-Instruct", "Qwen/Qwen2.5-72B-Instruct"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(Config{
				Token: "test",
				Model: tt.model,
			})
			assert.Equal(t, tt.want, client.Model())
		})
	}
}

func TestClient_Chat_Success(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify HuggingFace endpoint is being used
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer test-token")

		// Parse request body
		var req openai.ChatCompletionRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Equal(t, "meta-llama/Meta-Llama-3.1-70B-Instruct", req.Model)
		assert.Greater(t, len(req.Messages), 0)

		// Send mock response
		resp := openai.ChatCompletionResponse{
			ID:      "chatcmpl-hf-123",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   "meta-llama/Meta-Llama-3.1-70B-Instruct",
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatMessage{
						Role:    "assistant",
						Content: "Hello from HuggingFace!",
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.ChatCompletionUsage{
				PromptTokens:     25,
				CompletionTokens: 12,
				TotalTokens:      37,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with mock server
	client := NewClient(Config{
		Token: "test-token",
		Model: "meta-llama/Meta-Llama-3.1-70B-Instruct",
	})
	// Override the endpoint for testing
	client.openai = openai.NewClient(openai.Config{
		APIKey:   "test-token",
		Model:    "meta-llama/Meta-Llama-3.1-70B-Instruct",
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

	assert.Equal(t, "Hello from HuggingFace!", resp.Content)
	assert.Equal(t, "end_turn", resp.StopReason)
	assert.Equal(t, 25, resp.Usage.InputTokens)
	assert.Equal(t, 12, resp.Usage.OutputTokens)
	assert.Greater(t, resp.Usage.CostUSD, 0.0)

	// Verify HuggingFace provider metadata
	assert.Equal(t, "huggingface", resp.Metadata["provider"])
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
			ID:    "chatcmpl-hf-456",
			Model: "meta-llama/Meta-Llama-3.1-70B-Instruct",
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
				PromptTokens:     35,
				CompletionTokens: 18,
				TotalTokens:      53,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client
	client := NewClient(Config{
		Token: "test-token",
		Model: "meta-llama/Meta-Llama-3.1-70B-Instruct",
	})
	client.openai = openai.NewClient(openai.Config{
		APIKey:   "test-token",
		Model:    "meta-llama/Meta-Llama-3.1-70B-Instruct",
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
			name:         "llama-3.1-70b",
			model:        "meta-llama/Meta-Llama-3.1-70B-Instruct",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.001190, // (1000 * 0.80 + 500 * 0.80) / 1M
			wantMax:      0.001210,
		},
		{
			name:         "llama-3.1-8b",
			model:        "meta-llama/Meta-Llama-3.1-8B-Instruct",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.000290, // (1000 * 0.20 + 500 * 0.20) / 1M
			wantMax:      0.000310,
		},
		{
			name:         "mixtral-8x7b",
			model:        "mistralai/Mixtral-8x7B-Instruct-v0.1",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.000890, // (1000 * 0.60 + 500 * 0.60) / 1M
			wantMax:      0.000910,
		},
		{
			name:         "qwen-2.5-72b",
			model:        "Qwen/Qwen2.5-72B-Instruct",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.001190, // (1000 * 0.80 + 500 * 0.80) / 1M
			wantMax:      0.001210,
		},
		{
			name:         "gemma-2-9b",
			model:        "google/gemma-2-9b-it",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.000440, // (1000 * 0.30 + 500 * 0.30) / 1M
			wantMax:      0.000460,
		},
		{
			name:         "unknown-model-default",
			model:        "unknown/model",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.001490, // (1000 * 1.00 + 500 * 1.00) / 1M
			wantMax:      0.001510,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(Config{
				Token: "test",
				Model: tt.model,
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
				Message: "Invalid HuggingFace token",
				Type:    "invalid_request_error",
				Code:    "invalid_token",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		Token: "invalid-token",
	})
	client.openai = openai.NewClient(openai.Config{
		APIKey:   "invalid-token",
		Model:    "meta-llama/Meta-Llama-3.1-70B-Instruct",
		Endpoint: server.URL,
	})

	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "Invalid HuggingFace token")
}

func TestClient_ModelVariants(t *testing.T) {
	models := []string{
		"meta-llama/Meta-Llama-3.1-70B-Instruct",
		"meta-llama/Llama-3.1-70B-Instruct",
		"meta-llama/Meta-Llama-3.1-8B-Instruct",
		"meta-llama/Llama-3.1-8B-Instruct",
		"mistralai/Mixtral-8x7B-Instruct-v0.1",
		"mistralai/Mixtral-8x22B-Instruct-v0.1",
		"Qwen/Qwen2.5-72B-Instruct",
		"Qwen/Qwen2.5-Coder-32B-Instruct",
		"google/gemma-2-9b-it",
		"google/gemma-2-27b-it",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			client := NewClient(Config{
				Token: "test",
				Model: model,
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
