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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// mockShowResponse writes a /api/show response indicating tool support.
// Returns true if it handled the request (path was /api/show).
func mockShowResponse(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/api/show" {
		resp := map[string]string{
			"template": `{{ if .Tools }}tools{{ end }}{{ .Prompt }}`,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return true
	}
	return false
}

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
		if mockShowResponse(w, r) {
			return
		}
		assert.Equal(t, "/api/chat", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Verify request
		var req chatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Equal(t, "llama3.1", req.Model)
		assert.True(t, req.Stream)
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
		if mockShowResponse(w, r) {
			return
		}
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
		if mockShowResponse(w, r) {
			return
		}
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
		if mockShowResponse(w, r) {
			return
		}
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
				if mockShowResponse(w, r) {
					return
				}
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
	// Pin ToolModeNative so the test does not depend on Ollama being reachable
	// (auto-mode probes /api/show and can return false for stub servers).
	client := NewClient(Config{ToolMode: ToolModeNative})

	messages := []llmtypes.Message{
		{Role: "user", Content: "First message"},
		{Role: "assistant", Content: "Second message"},
		{Role: "tool", Content: "Tool output"},
		{Role: "user", Content: "Third message"},
	}

	converted := client.convertMessages(messages, true)

	// In native tool mode, tool results land in a distinct "tool"-role message,
	// so a following user turn does not coalesce (roles differ). Four wire
	// messages preserved in order.
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

// TestClient_ConvertMessages_ToolFallbackCoalesces verifies that when the
// non-native tool fallback path is taken (ToolModePrompt), a tool result and
// an immediately-following user turn are folded into a single user wire
// message. Prevents the "consecutive user messages" problem when a downstream
// provider (e.g. Anthropic via a proxy) enforces role alternation, and lets a
// text_body sidecar (e.g. from manage_skills) share the same wire message.
func TestClient_ConvertMessages_ToolFallbackCoalesces(t *testing.T) {
	client := NewClient(Config{ToolMode: ToolModePrompt})

	messages := []llmtypes.Message{
		{Role: "user", Content: "First message"},
		{Role: "assistant", Content: "Second message"},
		{Role: "tool", Content: "Tool output"},
		{Role: "user", Content: "Third message"},
	}

	converted := client.convertMessages(messages, false)

	assert.Len(t, converted, 3, "tool result and following user turn coalesce in fallback mode")
	assert.Equal(t, "user", converted[0].Role)
	assert.Equal(t, "First message", converted[0].Content)
	assert.Equal(t, "assistant", converted[1].Role)
	assert.Equal(t, "Second message", converted[1].Content)
	assert.Equal(t, "user", converted[2].Role)
	assert.Contains(t, converted[2].Content, "Tool result: Tool output")
	assert.Contains(t, converted[2].Content, "Third message")
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
				if mockShowResponse(w, r) {
					return
				}
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

func TestClient_SupportsNativeTools_ProbeDetectsToolTemplate(t *testing.T) {
	// Mock Ollama /api/show that returns a template with tool markers
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			probes.Add(1)
			resp := map[string]string{
				"template": `{{ if .System }}{{ .System }}{{ end }}{{ if .Tools }}Available tools: {{ .Tools }}{{ end }}{{ .Prompt }}`,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		Endpoint: server.URL,
		Model:    "some-unknown-model:14b",
	})

	// Should detect tool support dynamically despite not being in fallback list
	d := client.resolveToolMode(context.Background())
	assert.True(t, d.native, "should detect tool support from template")
	assert.Equal(t, toolModeSourceTemplate, d.source)
	// A successful probe is cached; later calls return the cached result.
	d = client.resolveToolMode(context.Background())
	assert.True(t, d.native, "second call should return the cached result")
	assert.True(t, d.cached)
	assert.Equal(t, int32(1), probes.Load(), "/api/show should be probed exactly once")
}

func TestClient_SupportsNativeTools_ProbeNoToolTemplate(t *testing.T) {
	// Mock Ollama /api/show that returns a template WITHOUT tool markers
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			resp := map[string]string{
				"template": `{{ if .System }}{{ .System }}{{ end }}{{ .Prompt }}`,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		Endpoint: server.URL,
		Model:    "some-unknown-model:14b",
	})

	assert.False(t, client.resolveToolMode(context.Background()).native, "should detect no tool support")
}

func TestClient_SupportsNativeTools_ProbeReadsCapabilities(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		want         bool
	}{
		// Models on Ollama's MLX runner report a bare template but list "tools".
		{"tools listed", []string{"completion", "vision", "tools", "thinking"}, true},
		{"tools not listed", []string{"completion", "vision"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/show" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"template":     `{{ .Prompt }}`,
						"capabilities": tt.capabilities,
					})
				}
			}))
			defer server.Close()

			client := NewClient(Config{Endpoint: server.URL, Model: "gemma4:26b"})
			d := client.resolveToolMode(context.Background())
			assert.Equal(t, tt.want, d.native)
			assert.Equal(t, toolModeSourceCapabilities, d.source)
			assert.True(t, client.probeDone, "a successful probe should be cached")
		})
	}
}

func TestClient_SupportsNativeTools_ProbeFallsBackToStaticList(t *testing.T) {
	// No server running — probe will fail, should fall back to static list
	client := NewClient(Config{
		Endpoint: "http://localhost:1", // unreachable
		Model:    "qwen3:8b",
		Timeout:  1 * time.Second,
	})

	d := client.resolveToolMode(context.Background())
	assert.True(t, d.native, "qwen3 should match fallback list")
	assert.Equal(t, toolModeSourceFallbackList, d.source)
	assert.Error(t, d.probeErr)
}

func TestClient_SupportsNativeTools_FailedProbeIsRetried(t *testing.T) {
	// Ollama answers /api/show with an error first (as when busy), then
	// lists "tools": the first miss must not stick past the backoff.
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools"}})
		}
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "some-unknown-model:14b"})
	d := client.resolveToolMode(context.Background())
	assert.False(t, d.native, "probe failed and the model isn't in the fallback list")
	assert.Equal(t, toolModeSourceProbeFailed, d.source)
	assert.False(t, client.probeDone, "a failed probe isn't cached as an answer")

	// Inside the backoff window the failure is reused without probing.
	d = client.resolveToolMode(context.Background())
	assert.False(t, d.native)
	assert.True(t, d.cached)
	assert.Equal(t, int32(1), calls.Load(), "no probe during the backoff")

	// After the backoff the next request probes again.
	client.probeMu.Lock()
	client.probeRetryAt = time.Time{}
	client.probeMu.Unlock()
	d = client.resolveToolMode(context.Background())
	assert.True(t, d.native, "the next request after the backoff probes again")
	assert.Equal(t, toolModeSourceCapabilities, d.source)
	assert.Equal(t, int32(2), calls.Load())
}

// TestClient_ChatStream_OneToolModePerRequest pins that a request decides its
// tool mode once: the history, the tools block and the metadata agree, even
// when /api/show fails for one request and succeeds for the next.
func TestClient_ChatStream_OneToolModePerRequest(t *testing.T) {
	var showCalls atomic.Int32
	type sentRequest struct {
		Tools    []json.RawMessage `json:"tools"`
		Messages []ollamaMessage   `json:"messages"`
	}
	sent := make(chan sentRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			if showCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools"}})
		case "/api/chat":
			var req sentRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			sent <- req
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
		}
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "some-unknown-model:14b"})
	client.probeBackoff = 0 // let the second request probe again

	history := []llmtypes.Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", ToolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "get_weather", Input: map[string]interface{}{}}}},
		{Role: "tool", ToolUseID: "call_1", Content: "sunny"},
	}
	tools := []shuttle.Tool{&shuttle.MockTool{MockName: "get_weather", MockSchema: &shuttle.JSONSchema{Type: "object"}}}

	// Request 1: the probe fails, so the whole request is in prompt mode.
	resp, err := client.ChatStream(context.Background(), history, tools, nil)
	require.NoError(t, err)
	req := <-sent
	assert.Equal(t, int32(1), showCalls.Load(), "one probe per request")
	assert.Empty(t, req.Tools, "prompt mode sends no tools")
	for _, m := range req.Messages {
		assert.Empty(t, m.ToolCalls, "prompt mode echoes no tool_calls")
		assert.NotEqual(t, "tool", m.Role, "prompt mode inlines tool results as user text")
	}
	assert.Equal(t, false, resp.Metadata["native_tools"])
	assert.Equal(t, toolModeSourceProbeFailed, resp.Metadata["tool_mode_source"])

	// Request 2: the probe succeeds, so the whole request is native.
	resp, err = client.ChatStream(context.Background(), history, tools, nil)
	require.NoError(t, err)
	req = <-sent
	assert.Equal(t, int32(2), showCalls.Load(), "one probe per request")
	assert.Len(t, req.Tools, 1, "native mode sends tools")
	require.Len(t, req.Messages, 3)
	assert.Len(t, req.Messages[1].ToolCalls, 1, "native mode echoes tool_calls")
	assert.Equal(t, "tool", req.Messages[2].Role)
	assert.Equal(t, true, resp.Metadata["native_tools"])
	assert.Equal(t, toolModeSourceCapabilities, resp.Metadata["tool_mode_source"])

	// Request 3: the answer is cached; no further probe.
	_, err = client.ChatStream(context.Background(), history, tools, nil)
	require.NoError(t, err)
	<-sent
	assert.Equal(t, int32(2), showCalls.Load(), "a successful probe is cached")
}

// TestClient_ChatStream_HungProbeHonorsCallerCancel pins that the /api/show
// probe runs under the caller's context: a hung probe ends when the caller
// gives up, and the request returns the context error.
func TestClient_ChatStream_HungProbeHonorsCallerCancel(t *testing.T) {
	var chatCalls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			// Hang until the client goes away or the test ends. The body is
			// read first so the server can notice the client disconnecting.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		case "/api/chat":
			chatCalls.Add(1)
		}
	}))
	defer server.Close()
	defer close(release) // runs before server.Close

	client := NewClient(Config{Endpoint: server.URL, Model: "some-unknown-model:14b"})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.ChatStream(ctx, []llmtypes.Message{{Role: "user", Content: "hi"}}, probeTestTools(), nil)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 5*time.Second, "the probe must stop when the caller's context ends")
	assert.Equal(t, int32(0), chatCalls.Load(), "no chat request after the caller gave up")

	// The caller's own cancellation isn't a probe failure: no backoff is set.
	client.probeMu.Lock()
	defer client.probeMu.Unlock()
	assert.True(t, client.probeRetryAt.IsZero(), "a cancelled probe isn't remembered as failed")
}

// probeTestTools is one tool, so a request needs a tool-mode decision.
func probeTestTools() []shuttle.Tool {
	return []shuttle.Tool{&shuttle.MockTool{
		MockName:   "weather:get",
		MockSchema: &shuttle.JSONSchema{Type: "object", Properties: map[string]*shuttle.JSONSchema{"city": {Type: "string"}}},
	}}
}

// okChatHandler answers /api/show with the "tools" capability after delay,
// counting the probes, and /api/chat with a finished reply.
func okChatHandler(showCalls *atomic.Int32, delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/show":
			showCalls.Add(1)
			time.Sleep(delay)
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools"}})
		case "/api/chat":
			_ = json.NewEncoder(w).Encode(chatResponse{Model: "m", Message: ollamaMessage{Role: "assistant", Content: "ok"}, Done: true})
		}
	}
}

func TestClient_ChatStream_ConcurrentFirstRequestsShareOneProbe(t *testing.T) {
	var showCalls atomic.Int32
	server := httptest.NewServer(okChatHandler(&showCalls, 200*time.Millisecond))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "some-unknown-model:14b"})
	const sessions = 8
	var wg sync.WaitGroup
	errs := make(chan error, sessions)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, probeTestTools(), nil)
			if err == nil && resp.Metadata["native_tools"] != true {
				err = fmt.Errorf("native_tools = %v, want true", resp.Metadata["native_tools"])
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, int32(1), showCalls.Load(), "one probe for all first requests")
}

func TestClient_ChatStream_ProbeWaiterHonorsItsOwnCancel(t *testing.T) {
	// A request waiting for another request's slow probe can still give up.
	var showCalls atomic.Int32
	server := httptest.NewServer(okChatHandler(&showCalls, 2*time.Second))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "some-unknown-model:14b"})
	go func() {
		_, _ = client.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "hi"}}, probeTestTools(), nil)
	}()
	require.Eventually(t, func() bool { return showCalls.Load() == 1 }, 2*time.Second, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.ChatStream(ctx, []llmtypes.Message{{Role: "user", Content: "hi"}}, probeTestTools(), nil)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second, "the waiter stops at its own deadline")
	assert.Equal(t, int32(1), showCalls.Load(), "the waiter didn't start a probe of its own")
}

func TestClient_ChatStream_NoToolsSkipsProbe(t *testing.T) {
	// A request with no tools and no tool history doesn't wait for /api/show.
	var showCalls atomic.Int32
	server := httptest.NewServer(okChatHandler(&showCalls, time.Second))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "some-unknown-model:14b"})
	start := time.Now()
	resp, err := client.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "Summarize this."}}, nil, nil)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 500*time.Millisecond)
	assert.Equal(t, int32(0), showCalls.Load())
	assert.Equal(t, "no_tools", resp.Metadata["tool_mode_source"])

	// Tool history still needs the decision.
	history := []llmtypes.Message{
		{Role: "user", Content: "What's the weather?"},
		{Role: "assistant", ToolCalls: []llmtypes.ToolCall{{ID: "call_1", Name: "weather:get", Input: map[string]interface{}{}}}},
		{Role: "tool", ToolUseID: "call_1", Content: `{"temp_c":18}`},
	}
	_, err = client.ChatStream(context.Background(), history, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), showCalls.Load())
}

// TestClient_ChatStream_RecordsToolModeOnSpan pins the observability of the
// tool-mode decision: the span carries the chosen mode and its source.
func TestClient_ChatStream_RecordsToolModeOnSpan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/chat":
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
		}
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "qwen3:8b"})
	ctx, span := observability.NewNoOpTracer().StartSpan(context.Background(), "test")
	_, err := client.ChatStream(ctx, []llmtypes.Message{{Role: "user", Content: "hi"}}, probeTestTools(), nil)
	require.NoError(t, err)

	assert.Equal(t, "native", span.Attributes["llm.ollama.tool_mode"])
	assert.Equal(t, toolModeSourceFallbackList, span.Attributes["llm.ollama.tool_mode_source"])
	assert.Equal(t, false, span.Attributes["llm.ollama.tool_mode_cached"])
	assert.Contains(t, span.Attributes["llm.ollama.tool_probe_error"], "status 500")
}

func TestClient_ConvertMessages_EchoesToolCalls(t *testing.T) {
	// The model's own tool calls must go back with the history, or it sees a
	// result it never asked for and calls the tool again. The name is
	// colon-namespaced so the test catches a history name that differs from
	// the sanitized name advertised in the tools block.
	messages := []llmtypes.Message{
		{Role: "user", Content: "What's on my calendar tomorrow?"},
		{Role: "assistant", ToolCalls: []llmtypes.ToolCall{
			{ID: "call_1", Name: "cal:list_events", Input: map[string]interface{}{"start": "2026-10-30"}},
		}},
		{Role: "tool", ToolUseID: "call_1", Content: `{"events":[]}`},
	}

	client := NewClient(Config{Model: "gemma4:26b", ToolMode: ToolModeNative})
	converted := client.convertMessages(messages, true)
	require.Len(t, converted, 3)
	require.Len(t, converted[1].ToolCalls, 1)
	call := converted[1].ToolCalls[0]
	assert.Equal(t, "call_1", call.ID)
	assert.Equal(t, "cal_list_events", call.Function.Name)
	assert.Equal(t, map[string]interface{}{"start": "2026-10-30"}, call.Function.Arguments)
	assert.Equal(t, "tool", converted[2].Role)
	assert.Equal(t, "cal_list_events", converted[2].ToolName)
	assert.Equal(t, "call_1", converted[2].ToolCallID)

	// Prompt-mode models get no tool_calls: they wrote the call as text.
	converted = client.convertMessages(messages, false)
	assert.Empty(t, converted[1].ToolCalls)
}

// TestClient_ConvertMessages_EmptyToolCallIDsPairByPosition covers Ollama
// before v0.12.10, which returns tool calls without IDs: each result is named
// after the call in the same position, not after the last call.
func TestClient_ConvertMessages_EmptyToolCallIDsPairByPosition(t *testing.T) {
	messages := []llmtypes.Message{
		{Role: "user", Content: "weather and time?"},
		{Role: "assistant", ToolCalls: []llmtypes.ToolCall{
			{ID: "", Name: "get_weather"},
			{ID: "", Name: "get_time"},
		}},
		{Role: "tool", Content: "sunny"},
		{Role: "tool", Content: "noon"},
	}

	client := NewClient(Config{ToolMode: ToolModeNative})
	converted := client.convertMessages(messages, true)
	require.Len(t, converted, 4)
	assert.Equal(t, "get_weather", converted[2].ToolName)
	assert.Equal(t, "get_time", converted[3].ToolName)
	assert.Empty(t, converted[2].ToolCallID)
	assert.Empty(t, converted[3].ToolCallID)
}

func TestClient_SupportsNativeTools_ExplicitModes(t *testing.T) {
	client := NewClient(Config{Model: "unknown-model", ToolMode: ToolModeNative})
	d := client.resolveToolMode(context.Background())
	assert.True(t, d.native)
	assert.Equal(t, toolModeSourceConfig, d.source)

	client = NewClient(Config{Model: "llama3.1", ToolMode: ToolModePrompt})
	d = client.resolveToolMode(context.Background())
	assert.False(t, d.native)
	assert.Equal(t, toolModeSourceConfig, d.source)
}

func TestClient_HealthCheck_Succeeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string][]string{
			"tags": {"llama3.1", "qwen3:8b"},
		})
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "llama3.1"})
	require.NoError(t, client.HealthCheck(context.Background()))
}

func TestClient_SlowBodyDoesNotTimeOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		w.Header().Set("Content-Type", "application/json")
		firstChunk := chatResponse{
			Model: "llama3.1",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: "Hello",
			},
			Done:            false,
			PromptEvalCount: 0,
			EvalCount:       0,
		}
		_ = json.NewEncoder(w).Encode(firstChunk)
		flusher.Flush()
		time.Sleep(2 * time.Second)
		secondChunk := chatResponse{
			Model: "llama3.1",
			Message: ollamaMessage{
				Role:    "assistant",
				Content: " world",
			},
			Done:            true,
			PromptEvalCount: 0,
			EvalCount:       0,
		}
		_ = json.NewEncoder(w).Encode(secondChunk)
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "llama3.1", Timeout: 100 * time.Millisecond, ToolMode: ToolModePrompt})

	resp, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "Hello"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello world", resp.Content)
}

func TestClient_StuckHeadersTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"llama3.1","message":{"role":"assistant","content":"hi"},"done":true,"prompt_eval_count":0,"eval_count":0}`))
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, Model: "llama3.1", Timeout: 100 * time.Millisecond, ToolMode: ToolModePrompt})
	_, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "ping"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestClient_UnreachableServerFailsFast(t *testing.T) {
	client := NewClient(Config{
		Endpoint: "http://203.0.113.1:65535", // TEST-NET-3 non-routable
		Model:    "llama3.1",
		Timeout:  60 * time.Second, // much larger than dial timeout
		ToolMode: ToolModePrompt,
	})

	start := time.Now()
	_, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "ping"}}, nil)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 20*time.Second, "should fail via dial timeout, not header timeout")
}

func TestClient_GetDefaultMaxTokens_30B(t *testing.T) {
	assert.Equal(t, 6144, getDefaultMaxTokens("qwen3-coder:30b"))
	assert.Equal(t, 6144, getDefaultMaxTokens("some-model:34b"))
	assert.Equal(t, 8192, getDefaultMaxTokens("llama3.3:70b"))
	assert.Equal(t, 4096, getDefaultMaxTokens("qwen3:8b"))
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
