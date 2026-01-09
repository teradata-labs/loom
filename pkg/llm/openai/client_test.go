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
package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   *Client
	}{
		{
			name: "with defaults",
			config: Config{
				APIKey: "test-key",
			},
			want: &Client{
				apiKey:      "test-key",
				model:       "gpt-4.1", // Updated default model
				endpoint:    "https://api.openai.com/v1/chat/completions",
				maxTokens:   4096,
				temperature: 1.0,
			},
		},
		{
			name: "with custom config",
			config: Config{
				APIKey:      "custom-key",
				Model:       "gpt-4",
				Endpoint:    "https://custom.api.com/v1/chat",
				MaxTokens:   2000,
				Temperature: 0.5,
				Timeout:     30 * time.Second,
			},
			want: &Client{
				apiKey:      "custom-key",
				model:       "gpt-4",
				endpoint:    "https://custom.api.com/v1/chat",
				maxTokens:   2000,
				temperature: 0.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewClient(tt.config)
			assert.Equal(t, tt.want.apiKey, got.apiKey)
			assert.Equal(t, tt.want.model, got.model)
			assert.Equal(t, tt.want.endpoint, got.endpoint)
			assert.Equal(t, tt.want.maxTokens, got.maxTokens)
			assert.Equal(t, tt.want.temperature, got.temperature)
			assert.NotNil(t, got.httpClient)
		})
	}
}

func TestClient_Name(t *testing.T) {
	client := NewClient(Config{APIKey: "test"})
	assert.Equal(t, "openai", client.Name())
}

func TestClient_Model(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"default model", "", "gpt-4.1"},
		{"custom model", "gpt-4-turbo", "gpt-4-turbo"},
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

func TestClient_ConvertMessages(t *testing.T) {
	client := NewClient(Config{APIKey: "test"})

	tests := []struct {
		name     string
		messages []types.Message
		want     []ChatMessage
	}{
		{
			name: "user message",
			messages: []types.Message{
				{Role: "user", Content: "Hello"},
			},
			want: []ChatMessage{
				{Role: "user", Content: "Hello"},
			},
		},
		{
			name: "system message",
			messages: []types.Message{
				{Role: "system", Content: "You are helpful"},
			},
			want: []ChatMessage{
				{Role: "system", Content: "You are helpful"},
			},
		},
		{
			name: "assistant message with content",
			messages: []types.Message{
				{Role: "assistant", Content: "I can help!"},
			},
			want: []ChatMessage{
				{Role: "assistant", Content: "I can help!"},
			},
		},
		{
			name: "assistant message with tool calls",
			messages: []types.Message{
				{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{
							ID:   "call_123",
							Name: "get_weather",
							Input: map[string]interface{}{
								"location": "San Francisco",
							},
						},
					},
				},
			},
			want: []ChatMessage{
				{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: FunctionCall{
								Name:      "get_weather",
								Arguments: `{"location":"San Francisco"}`,
							},
						},
					},
				},
			},
		},
		{
			name: "tool result message",
			messages: []types.Message{
				{
					Role:      "tool",
					Content:   "Temperature is 72F",
					ToolUseID: "call_123",
				},
			},
			want: []ChatMessage{
				{
					Role:       "tool",
					Content:    "Temperature is 72F",
					ToolCallID: "call_123",
				},
			},
		},
		{
			name: "conversation with multiple message types",
			messages: []types.Message{
				{Role: "system", Content: "You are helpful"},
				{Role: "user", Content: "What's the weather?"},
				{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{ID: "call_1", Name: "get_weather", Input: map[string]interface{}{"location": "NYC"}},
					},
				},
				{Role: "tool", Content: "Sunny, 75F", ToolUseID: "call_1"},
				{Role: "assistant", Content: "It's sunny and 75F in NYC"},
			},
			want: []ChatMessage{
				{Role: "system", Content: "You are helpful"},
				{Role: "user", Content: "What's the weather?"},
				{
					Role: "assistant",
					ToolCalls: []ToolCall{
						{
							ID:   "call_1",
							Type: "function",
							Function: FunctionCall{
								Name:      "get_weather",
								Arguments: `{"location":"NYC"}`,
							},
						},
					},
				},
				{Role: "tool", Content: "Sunny, 75F", ToolCallID: "call_1"},
				{Role: "assistant", Content: "It's sunny and 75F in NYC"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := client.convertMessages(tt.messages)
			assert.Equal(t, len(tt.want), len(got))

			for i := range tt.want {
				assert.Equal(t, tt.want[i].Role, got[i].Role)
				assert.Equal(t, tt.want[i].Content, got[i].Content)
				assert.Equal(t, tt.want[i].ToolCallID, got[i].ToolCallID)

				if len(tt.want[i].ToolCalls) > 0 {
					require.Equal(t, len(tt.want[i].ToolCalls), len(got[i].ToolCalls))
					for j := range tt.want[i].ToolCalls {
						assert.Equal(t, tt.want[i].ToolCalls[j].ID, got[i].ToolCalls[j].ID)
						assert.Equal(t, tt.want[i].ToolCalls[j].Type, got[i].ToolCalls[j].Type)
						assert.Equal(t, tt.want[i].ToolCalls[j].Function.Name, got[i].ToolCalls[j].Function.Name)
						// Compare JSON arguments
						assert.JSONEq(t, tt.want[i].ToolCalls[j].Function.Arguments, got[i].ToolCalls[j].Function.Arguments)
					}
				}
			}
		})
	}
}

func TestClient_ConvertTools(t *testing.T) {
	client := NewClient(Config{APIKey: "test"})

	mockTool := &mockShuttleTool{
		name:        "get_weather",
		description: "Get weather for a location",
		schema: &shuttle.JSONSchema{
			Type: "object",
			Properties: map[string]*shuttle.JSONSchema{
				"location": {
					Type:        "string",
					Description: "The city name",
				},
				"units": {
					Type: "string",
					Enum: []interface{}{"celsius", "fahrenheit"},
				},
			},
			Required: []string{"location"},
		},
	}

	tools := []shuttle.Tool{mockTool}
	got := client.convertTools(tools)

	require.Len(t, got, 1)
	assert.Equal(t, "function", got[0].Type)
	assert.Equal(t, "get_weather", got[0].Function.Name)
	assert.Equal(t, "Get weather for a location", got[0].Function.Description)

	params := got[0].Function.Parameters
	assert.Equal(t, "object", params["type"])
	assert.Contains(t, params, "properties")
	assert.Contains(t, params, "required")

	props := params["properties"].(map[string]interface{})
	assert.Contains(t, props, "location")
	assert.Contains(t, props, "units")
}

func TestClient_ConvertResponse(t *testing.T) {
	client := NewClient(Config{APIKey: "test", Model: "gpt-4o"})

	tests := []struct {
		name string
		resp *ChatCompletionResponse
		want *types.LLMResponse
	}{
		{
			name: "text response",
			resp: &ChatCompletionResponse{
				Model: "gpt-4o",
				Choices: []ChatCompletionChoice{
					{
						Message: ChatMessage{
							Role:    "assistant",
							Content: "Hello! How can I help?",
						},
						FinishReason: "stop",
					},
				},
				Usage: ChatCompletionUsage{
					PromptTokens:     10,
					CompletionTokens: 20,
					TotalTokens:      30,
				},
			},
			want: &types.LLMResponse{
				Content:    "Hello! How can I help?",
				StopReason: "end_turn",
				Usage: types.Usage{
					InputTokens:  10,
					OutputTokens: 20,
					TotalTokens:  30,
				},
			},
		},
		{
			name: "tool call response",
			resp: &ChatCompletionResponse{
				Model: "gpt-4o",
				Choices: []ChatCompletionChoice{
					{
						Message: ChatMessage{
							Role: "assistant",
							ToolCalls: []ToolCall{
								{
									ID:   "call_abc123",
									Type: "function",
									Function: FunctionCall{
										Name:      "get_weather",
										Arguments: `{"location":"San Francisco","units":"fahrenheit"}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
				Usage: ChatCompletionUsage{
					PromptTokens:     50,
					CompletionTokens: 15,
					TotalTokens:      65,
				},
			},
			want: &types.LLMResponse{
				StopReason: "tool_use",
				ToolCalls: []types.ToolCall{
					{
						ID:   "call_abc123",
						Name: "get_weather",
						Input: map[string]interface{}{
							"location": "San Francisco",
							"units":    "fahrenheit",
						},
					},
				},
				Usage: types.Usage{
					InputTokens:  50,
					OutputTokens: 15,
					TotalTokens:  65,
				},
			},
		},
		{
			name: "max_tokens finish reason",
			resp: &ChatCompletionResponse{
				Model: "gpt-4o",
				Choices: []ChatCompletionChoice{
					{
						Message: ChatMessage{
							Role:    "assistant",
							Content: "Truncated response...",
						},
						FinishReason: "length",
					},
				},
				Usage: ChatCompletionUsage{
					PromptTokens:     100,
					CompletionTokens: 4096,
					TotalTokens:      4196,
				},
			},
			want: &types.LLMResponse{
				Content:    "Truncated response...",
				StopReason: "max_tokens",
				Usage: types.Usage{
					InputTokens:  100,
					OutputTokens: 4096,
					TotalTokens:  4196,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := client.convertResponse(tt.resp)

			assert.Equal(t, tt.want.Content, got.Content)
			assert.Equal(t, tt.want.StopReason, got.StopReason)
			assert.Equal(t, tt.want.Usage.InputTokens, got.Usage.InputTokens)
			assert.Equal(t, tt.want.Usage.OutputTokens, got.Usage.OutputTokens)
			assert.Equal(t, tt.want.Usage.TotalTokens, got.Usage.TotalTokens)
			assert.Greater(t, got.Usage.CostUSD, 0.0) // Cost should be calculated

			if len(tt.want.ToolCalls) > 0 {
				require.Equal(t, len(tt.want.ToolCalls), len(got.ToolCalls))
				for i := range tt.want.ToolCalls {
					assert.Equal(t, tt.want.ToolCalls[i].ID, got.ToolCalls[i].ID)
					assert.Equal(t, tt.want.ToolCalls[i].Name, got.ToolCalls[i].Name)
					assert.Equal(t, tt.want.ToolCalls[i].Input, got.ToolCalls[i].Input)
				}
			}
		})
	}
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
			name:         "gpt-4o",
			model:        "gpt-4o",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.007, // (1000 * 2.5 + 500 * 10) / 1M = 0.0075
			wantMax:      0.008,
		},
		{
			name:         "gpt-4o-mini",
			model:        "gpt-4o-mini",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.0004, // (1000 * 0.15 + 500 * 0.6) / 1M = 0.00045
			wantMax:      0.0005,
		},
		{
			name:         "gpt-4",
			model:        "gpt-4",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.059, // (1000 * 30 + 500 * 60) / 1M = 0.06
			wantMax:      0.061,
		},
		{
			name:         "gpt-3.5-turbo",
			model:        "gpt-3.5-turbo",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.0012, // (1000 * 0.5 + 500 * 1.5) / 1M = 0.00125
			wantMax:      0.0013,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(Config{APIKey: "test", Model: tt.model})
			got := client.calculateCost(tt.inputTokens, tt.outputTokens)
			assert.GreaterOrEqual(t, got, tt.wantMin)
			assert.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

func TestClient_Chat_Success(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer test-key")

		// Parse request body
		var req ChatCompletionRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Equal(t, "gpt-4o", req.Model)
		assert.Greater(t, len(req.Messages), 0)

		// Send mock response
		resp := ChatCompletionResponse{
			ID:      "chatcmpl-123",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   "gpt-4o",
			Choices: []ChatCompletionChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: "Hello! How can I help you today?",
					},
					FinishReason: "stop",
				},
			},
			Usage: ChatCompletionUsage{
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
	client := NewClient(Config{
		APIKey:   "test-key",
		Model:    "gpt-4o",
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

	assert.Equal(t, "Hello! How can I help you today?", resp.Content)
	assert.Equal(t, "end_turn", resp.StopReason)
	assert.Equal(t, 20, resp.Usage.InputTokens)
	assert.Equal(t, 10, resp.Usage.OutputTokens)
	assert.Greater(t, resp.Usage.CostUSD, 0.0)
}

func TestClient_Chat_WithTools(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse request
		var req ChatCompletionRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		// Verify tools were sent
		assert.Len(t, req.Tools, 1)
		assert.Equal(t, "get_weather", req.Tools[0].Function.Name)

		// Send tool call response
		resp := ChatCompletionResponse{
			ID:    "chatcmpl-456",
			Model: "gpt-4o",
			Choices: []ChatCompletionChoice{
				{
					Message: ChatMessage{
						Role: "assistant",
						ToolCalls: []ToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: FunctionCall{
									Name:      "get_weather",
									Arguments: `{"location":"Boston"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: ChatCompletionUsage{
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
		APIKey:   "test-key",
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
		{Role: "user", Content: "What's the weather in Boston?"},
	}

	resp, err := client.Chat(ctx, messages, []shuttle.Tool{mockTool})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_123", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.Equal(t, "Boston", resp.ToolCalls[0].Input["location"])
}

func TestClient_Chat_APIError(t *testing.T) {
	// Mock server returning error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Error: &OpenAIError{
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
		APIKey:   "invalid-key",
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
	return nil // No progress callback in tests
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
