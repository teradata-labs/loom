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
package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected *Client
	}{
		{
			name:   "default config",
			config: Config{},
			expected: &Client{
				endpoint:    "http://localhost:11434",
				model:       "llama3.1",
				maxTokens:   4096,
				temperature: 0.8,
			},
		},
		{
			name: "custom config",
			config: Config{
				Endpoint:    "http://custom:8080",
				Model:       "mistral",
				MaxTokens:   2048,
				Temperature: 0.5,
				Timeout:     30 * time.Second,
			},
			expected: &Client{
				endpoint:    "http://custom:8080",
				model:       "mistral",
				maxTokens:   2048,
				temperature: 0.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)
			assert.Equal(t, tt.expected.endpoint, client.endpoint)
			assert.Equal(t, tt.expected.model, client.model)
			assert.Equal(t, tt.expected.maxTokens, client.maxTokens)
			assert.Equal(t, tt.expected.temperature, client.temperature)
			assert.NotNil(t, client.httpClient)
		})
	}
}

func TestClient_NameAndModel(t *testing.T) {
	client := NewClient(Config{Model: "qwen2.5-coder"})
	assert.Equal(t, "ollama", client.Name())
	assert.Equal(t, "qwen2.5-coder", client.Model())
}

func TestClient_Chat_SimpleText(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Verify request
		var req chatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Equal(t, "llama3.1", req.Model)
		assert.False(t, req.Stream)
		assert.Len(t, req.Messages, 1)
		assert.Equal(t, "user", req.Messages[0].Role)
		assert.Equal(t, "Hello!", req.Messages[0].Content)

		// Return mock response
		resp := chatResponse{
			Model:     "llama3.1",
			CreatedAt: "2024-01-01T00:00:00Z",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: "Hello! How can I help you today?",
			},
			Done:            true,
			TotalDuration:   1000000000,
			LoadDuration:    500000000,
			PromptEvalCount: 10,
			EvalCount:       15,
			EvalDuration:    200000000,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with mock server
	client := NewClient(Config{
		Endpoint: server.URL,
		Model:    "llama3.1",
	})

	// Test chat
	ctx := context.Background()
	messages := []llmtypes.Message{
		{Role: "user", Content: "Hello!"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello! How can I help you today?", resp.Content)
	assert.Empty(t, resp.ToolCalls)
	assert.Equal(t, "stop", resp.StopReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 15, resp.Usage.OutputTokens)
	assert.Equal(t, 25, resp.Usage.TotalTokens)
	assert.Equal(t, 0.0, resp.Usage.CostUSD) // Ollama is free
}

func TestClient_Chat_MultiTurn(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request has multiple messages
		var req chatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Len(t, req.Messages, 3)
		assert.Equal(t, "user", req.Messages[0].Role)
		assert.Equal(t, "assistant", req.Messages[1].Role)
		assert.Equal(t, "user", req.Messages[2].Role)

		// Return mock response
		resp := chatResponse{
			Model:     "llama3.1",
			CreatedAt: "2024-01-01T00:00:00Z",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: "Sure, I'll help with that.",
			},
			Done:            true,
			PromptEvalCount: 30,
			EvalCount:       12,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL})

	ctx := context.Background()
	messages := []llmtypes.Message{
		{Role: "user", Content: "What's the weather?"},
		{Role: "assistant", Content: "I don't have access to weather data."},
		{Role: "user", Content: "Can you check something else?"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
	assert.Equal(t, "Sure, I'll help with that.", resp.Content)
}

func TestClient_Chat_WithToolResult(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request includes tool result as tool message (native format)
		var req chatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Len(t, req.Messages, 2)
		assert.Equal(t, "assistant", req.Messages[0].Role)
		assert.Equal(t, "tool", req.Messages[1].Role)
		assert.Equal(t, "{\"result\": \"success\"}", req.Messages[1].Content)

		// Return mock response
		resp := chatResponse{
			Model:     "llama3.1",
			CreatedAt: "2024-01-01T00:00:00Z",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: "The result shows the data you requested.",
			},
			Done:            true,
			PromptEvalCount: 20,
			EvalCount:       10,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL})

	ctx := context.Background()
	messages := []llmtypes.Message{
		{Role: "assistant", Content: "Let me check that for you."},
		{Role: "tool", Content: "{\"result\": \"success\"}"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
	assert.Equal(t, "The result shows the data you requested.", resp.Content)
}

func TestClient_Chat_OptionsSet(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify options are set correctly
		var req chatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.NotNil(t, req.Options)
		assert.Equal(t, 0.9, req.Options["temperature"])
		// JSON unmarshaling converts all numbers to float64
		assert.Equal(t, float64(2048), req.Options["num_predict"])

		// Return mock response
		resp := chatResponse{
			Model:     "mistral",
			CreatedAt: "2024-01-01T00:00:00Z",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: "Response",
			},
			Done:            true,
			PromptEvalCount: 5,
			EvalCount:       3,
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		Endpoint:    server.URL,
		Model:       "mistral",
		Temperature: 0.9,
		MaxTokens:   2048,
	})

	ctx := context.Background()
	messages := []llmtypes.Message{
		{Role: "user", Content: "Test"},
	}

	_, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
}

func TestClient_Chat_ErrorHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		expectErr  bool
	}{
		{
			name:       "server error",
			statusCode: 500,
			body:       "Internal server error",
			expectErr:  true,
		},
		{
			name:       "bad request",
			statusCode: 400,
			body:       "Bad request",
			expectErr:  true,
		},
		{
			name:       "not found",
			statusCode: 404,
			body:       "Model not found",
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClient(Config{Endpoint: server.URL})

			ctx := context.Background()
			messages := []llmtypes.Message{
				{Role: "user", Content: "Test"},
			}

			_, err := client.Chat(ctx, messages, nil)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "API error")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestClient_ConvertMessages(t *testing.T) {
	client := NewClient(Config{})

	messages := []llmtypes.Message{
		{Role: "user", Content: "First message"},
		{Role: "assistant", Content: "Second message"},
		{Role: "tool", Content: "Tool output"},
		{Role: "user", Content: "Third message"},
	}

	converted := client.convertMessages(messages)

	assert.Len(t, converted, 4)
	assert.Equal(t, "user", converted[0].Role)
	assert.Equal(t, "First message", converted[0].Content)
	assert.Equal(t, "assistant", converted[1].Role)
	assert.Equal(t, "Second message", converted[1].Content)
	assert.Equal(t, "tool", converted[2].Role)
	assert.Equal(t, "Tool output", converted[2].Content)
	assert.Equal(t, "user", converted[3].Role)
	assert.Equal(t, "Third message", converted[3].Content)
}

func TestClient_ImplementsInterface(t *testing.T) {
	var _ llmtypes.LLMProvider = (*Client)(nil)
}

func TestClient_Chat_WithToolCalls(t *testing.T) {
	tests := []struct {
		name              string
		toolCallArguments interface{} // Can be string or map
		expectedParams    map[string]interface{}
		description       string
	}{
		{
			name:              "valid JSON string",
			toolCallArguments: `{"database": "DBC", "debugMode": false}`,
			expectedParams: map[string]interface{}{
				"database":  "DBC",
				"debugMode": false,
			},
			description: "Should parse clean JSON string correctly",
		},
		{
			name:              "JSON with backticks",
			toolCallArguments: "`{\"database\": \"DBC\", \"debugMode\": false}`",
			expectedParams: map[string]interface{}{
				"database":  "DBC",
				"debugMode": false,
			},
			description: "Should strip backticks and parse JSON",
		},
		{
			name:              "JSON with json marker",
			toolCallArguments: "json\n{\"database\": \"DBC\", \"debugMode\": false}",
			expectedParams: map[string]interface{}{
				"database":  "DBC",
				"debugMode": false,
			},
			description: "Should strip json marker and parse JSON",
		},
		{
			name:              "JSON with whitespace",
			toolCallArguments: "  {\"database\": \"DBC\", \"debugMode\": false}  ",
			expectedParams: map[string]interface{}{
				"database":  "DBC",
				"debugMode": false,
			},
			description: "Should trim whitespace and parse JSON",
		},
		{
			name: "JSON as map",
			toolCallArguments: map[string]interface{}{
				"database":  "DBC",
				"debugMode": false,
			},
			expectedParams: map[string]interface{}{
				"database":  "DBC",
				"debugMode": false,
			},
			description: "Should handle pre-parsed map",
		},
		{
			name:              "invalid JSON",
			toolCallArguments: "not valid json",
			expectedParams:    map[string]interface{}{},
			description:       "Should return empty params for invalid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Return mock response with tool call
				resp := chatResponse{
					Model:     "llama3.1",
					CreatedAt: "2024-01-01T00:00:00Z",
					Message: ollamaMessage{
						Role:    "assistant",
						Content: "",
						ToolCalls: []ollamaToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: ollamaFunctionCall{
									Name:      "teradata_execute_sql",
									Arguments: tt.toolCallArguments,
								},
							},
						},
					},
					Done:            true,
					PromptEvalCount: 10,
					EvalCount:       5,
				}

				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			// Create client with mock server
			client := NewClient(Config{
				Endpoint: server.URL,
				Model:    "llama3.1",
			})

			// Test chat with tool calls
			ctx := context.Background()
			messages := []llmtypes.Message{
				{Role: "user", Content: "Execute SQL query"},
			}

			resp, err := client.Chat(ctx, messages, nil)
			require.NoError(t, err, tt.description)
			require.Len(t, resp.ToolCalls, 1, "Should have one tool call")

			toolCall := resp.ToolCalls[0]
			assert.Equal(t, "call_123", toolCall.ID, "Tool call ID should match")
			assert.Equal(t, "teradata_execute_sql", toolCall.Name, "Tool name should match")
			assert.Equal(t, tt.expectedParams, toolCall.Input, tt.description)
		})
	}
}

func TestClient_CleanJSONString(t *testing.T) {
	client := NewClient(Config{})

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "clean JSON",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON with backticks",
			input:    "`{\"key\": \"value\"}`",
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON with json marker",
			input:    "json\n{\"key\": \"value\"}",
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON with whitespace",
			input:    "  {\"key\": \"value\"}  ",
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON with backticks and json marker",
			input:    "`json\n{\"key\": \"value\"}`",
			expected: `{"key": "value"}`, // Backticks stripped first, then json marker
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.cleanJSONString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
