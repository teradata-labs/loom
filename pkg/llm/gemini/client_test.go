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
package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
		want   string // expected model
	}{
		{
			name: "with defaults",
			config: Config{
				APIKey: "test-key",
			},
			want: "gemini-3-flash-preview",
		},
		{
			name: "with custom model",
			config: Config{
				APIKey: "test-key",
				Model:  "gemini-2.5-pro",
			},
			want: "gemini-2.5-pro",
		},
		{
			name: "with gemini 3 pro",
			config: Config{
				APIKey: "test-key",
				Model:  "gemini-3-pro-preview",
			},
			want: "gemini-3-pro-preview",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)
			assert.NotNil(t, client)
			assert.Equal(t, "gemini", client.Name())
			assert.Equal(t, tt.want, client.Model())
		})
	}
}

func TestClient_Name(t *testing.T) {
	client := NewClient(Config{APIKey: "test"})
	assert.Equal(t, "gemini", client.Name())
}

func TestClient_Model(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"default model", "", "gemini-3-flash-preview"},
		{"gemini 3 pro", "gemini-3-pro-preview", "gemini-3-pro-preview"},
		{"gemini 3 flash", "gemini-3-flash-preview", "gemini-3-flash-preview"},
		{"gemini 2.5 pro", "gemini-2.5-pro", "gemini-2.5-pro"},
		{"gemini 2.5 flash", "gemini-2.5-flash", "gemini-2.5-flash"},
		{"gemini 2.5 flash-lite", "gemini-2.5-flash-lite", "gemini-2.5-flash-lite"},
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
		// Verify Gemini endpoint is being used
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Contains(t, r.URL.Path, "gemini-3-flash-preview")
		assert.Contains(t, r.URL.RawQuery, "key=test-key")

		// Parse request body
		var req GenerateContentRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		assert.Greater(t, len(req.Contents), 0)

		// Send mock response
		resp := GenerateContentResponse{
			Candidates: []Candidate{
				{
					Content: Content{
						Role: "model",
						Parts: []Part{
							{Text: "Hello from Gemini!"},
						},
					},
					FinishReason: "STOP",
					Index:        0,
				},
			},
			UsageMetadata: UsageMetadata{
				PromptTokenCount:     25,
				CandidatesTokenCount: 12,
				TotalTokenCount:      37,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with mock server
	client := NewClient(Config{
		APIKey: "test-key",
		Model:  "gemini-3-flash-preview",
	})

	// Override endpoint for testing by modifying the httpClient's base URL
	// We'll intercept at the HTTP layer by creating a custom transport
	originalTransport := http.DefaultTransport
	client.httpClient.Transport = &mockTransport{
		baseURL:  server.URL,
		original: originalTransport,
	}

	// Test chat
	ctx := &mockContext{Context: context.Background()}
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "Hello from Gemini!", resp.Content)
	assert.Equal(t, "end_turn", resp.StopReason)
	assert.Equal(t, 25, resp.Usage.InputTokens)
	assert.Equal(t, 12, resp.Usage.OutputTokens)
	// Default model (gemini-3-flash-preview) is free during preview, so cost is 0
	assert.GreaterOrEqual(t, resp.Usage.CostUSD, 0.0)

	// Verify Gemini provider metadata
	assert.Equal(t, "gemini", resp.Metadata["provider"])
}

func TestClient_Chat_WithTools(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse request
		var req GenerateContentRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)

		// Verify tools were sent
		assert.Len(t, req.Tools, 1)
		assert.Len(t, req.Tools[0].FunctionDeclarations, 1)
		assert.Equal(t, "get_weather", req.Tools[0].FunctionDeclarations[0].Name)

		// Send tool call response
		resp := GenerateContentResponse{
			Candidates: []Candidate{
				{
					Content: Content{
						Role: "model",
						Parts: []Part{
							{
								FunctionCall: &FunctionCall{
									Name: "get_weather",
									Args: map[string]interface{}{
										"location": "Paris",
									},
								},
							},
						},
					},
					FinishReason: "STOP",
					Index:        0,
				},
			},
			UsageMetadata: UsageMetadata{
				PromptTokenCount:     35,
				CandidatesTokenCount: 18,
				TotalTokenCount:      53,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client
	client := NewClient(Config{
		APIKey: "test-key",
		Model:  "gemini-3-flash-preview",
	})
	client.httpClient.Transport = &mockTransport{
		baseURL:  server.URL,
		original: http.DefaultTransport,
	}

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
			name:         "gemini-3-flash-preview (free)",
			model:        "gemini-3-flash-preview",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.0, // Free during preview
			wantMax:      0.0,
		},
		{
			name:         "gemini-2.5-flash",
			model:        "gemini-2.5-flash",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.001540, // (1000 * 0.30 + 500 * 2.50) / 1M
			wantMax:      0.001560,
		},
		{
			name:         "gemini-2.5-flash-lite",
			model:        "gemini-2.5-flash-lite",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.000290, // (1000 * 0.10 + 500 * 0.40) / 1M
			wantMax:      0.000310,
		},
		{
			name:         "gemini-2.5-pro",
			model:        "gemini-2.5-pro",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.008120, // (1000 * 1.875 + 500 * 12.50) / 1M
			wantMax:      0.008140,
		},
		{
			name:         "gemini-3-pro-preview",
			model:        "gemini-3-pro-preview",
			inputTokens:  1000,
			outputTokens: 500,
			wantMin:      0.010490, // (1000 * 3.00 + 500 * 15.00) / 1M
			wantMax:      0.010510,
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
		resp := GenerateContentResponse{
			Error: &APIError{
				Code:    400,
				Message: "Invalid API key",
				Status:  "INVALID_ARGUMENT",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey: "invalid-key",
		Model:  "gemini-3-flash-preview",
	})
	client.httpClient.Transport = &mockTransport{
		baseURL:  server.URL,
		original: http.DefaultTransport,
	}

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
		"gemini-3-pro-preview",
		"gemini-3-flash-preview",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
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
			assert.GreaterOrEqual(t, cost, 0.0, "Cost should be non-negative for model %s", model)
		})
	}
}

func TestConvertMessages(t *testing.T) {
	tests := []struct {
		name     string
		messages []types.Message
		want     int // expected number of Gemini contents
	}{
		{
			name: "user message",
			messages: []types.Message{
				{Role: "user", Content: "Hello"},
			},
			want: 1,
		},
		{
			name: "system message converted to user",
			messages: []types.Message{
				{Role: "system", Content: "You are helpful"},
			},
			want: 1,
		},
		{
			name: "conversation with assistant",
			messages: []types.Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
			},
			want: 2,
		},
		{
			name: "with tool calls",
			messages: []types.Message{
				{Role: "user", Content: "What's the weather?"},
				{
					Role: "assistant",
					ToolCalls: []types.ToolCall{
						{
							ID:   "call_123",
							Name: "get_weather",
							Input: map[string]interface{}{
								"location": "Paris",
							},
						},
					},
				},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents := convertMessages(tt.messages)
			assert.Len(t, contents, tt.want)
		})
	}
}

// TestConvertMessages_EmptyPartsSkipped verifies that messages which would
// produce empty Parts arrays are skipped instead of triggering the Gemini
// "at least one parts field" error.
func TestConvertMessages_EmptyPartsSkipped(t *testing.T) {
	tests := []struct {
		name        string
		messages    []types.Message
		wantLen     int
		wantNoPanic bool
	}{
		{
			name: "empty user content skipped",
			messages: []types.Message{
				{Role: "user", Content: "hello"},
				{Role: "user", Content: ""}, // empty — must be skipped
			},
			wantLen: 1,
		},
		{
			name: "empty assistant (no content, no tool calls) skipped",
			messages: []types.Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "", ToolCalls: nil}, // empty — must be skipped
			},
			wantLen: 1,
		},
		{
			name: "assistant with only tool calls kept",
			messages: []types.Message{
				{Role: "user", Content: "what is 2+2"},
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []types.ToolCall{
						{ID: "c1", Name: "calc", Input: map[string]interface{}{"expr": "2+2"}},
					},
				},
			},
			wantLen: 2,
		},
		{
			name: "content blocks with no supported types falls back to text",
			messages: []types.Message{
				{
					Role:    "user",
					Content: "fallback text",
					ContentBlocks: []types.ContentBlock{
						{Type: "unsupported_type"}, // no matching block → fallback
					},
				},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents := convertMessages(tt.messages)
			assert.Len(t, contents, tt.wantLen, "unexpected number of Gemini content items")
			// Verify no content has empty Parts (this is what triggers the API error)
			for i, c := range contents {
				assert.NotEmpty(t, c.Parts, "content[%d] has empty Parts — Gemini will reject this", i)
			}
		})
	}
}

func TestThoughtSignature_RoundTrip(t *testing.T) {
	// Simulates the full round-trip:
	// 1. Gemini returns a function call with a thoughtSignature at Part level
	// 2. We parse it and store on ToolCall.ThoughtSignature
	// 3. On next turn, we echo it back at the Part level in conversation history

	const fakeSignature = "encrypted-opaque-thought-token-abc123"

	// Step 1: Mock server returns function call with thoughtSignature
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GenerateContentResponse{
			Candidates: []Candidate{
				{
					Content: Content{
						Role: "model",
						Parts: []Part{
							{
								FunctionCall: &FunctionCall{
									Name: "get_weather",
									Args: map[string]any{"location": "Tokyo"},
								},
								ThoughtSignature: fakeSignature, // At Part level, NOT inside FunctionCall
							},
						},
					},
					FinishReason: "STOP",
				},
			},
			UsageMetadata: UsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 5,
				TotalTokenCount:      15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Model: "gemini-3-pro-preview"})
	client.httpClient.Transport = &mockTransport{
		baseURL:  server.URL,
		original: http.DefaultTransport,
	}

	// Step 2: Parse response - ThoughtSignature should be captured on ToolCall
	ctx := &mockContext{Context: context.Background()}
	resp, err := client.Chat(ctx, []types.Message{
		{Role: "user", Content: "What's the weather in Tokyo?"},
	}, []shuttle.Tool{&mockShuttleTool{
		name:        "get_weather",
		description: "Get weather",
		schema:      &shuttle.JSONSchema{Type: "object"},
	}})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, fakeSignature, resp.ToolCalls[0].ThoughtSignature,
		"ThoughtSignature must be captured from Part level in response")

	// Step 3: Build conversation history with the tool call and its result,
	// then verify the thought signature is echoed back at Part level
	messages := []types.Message{
		{Role: "user", Content: "What's the weather in Tokyo?"},
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:               "get_weather",
					Name:             "get_weather",
					Input:            map[string]any{"location": "Tokyo"},
					ThoughtSignature: fakeSignature, // Preserved from step 2
				},
			},
		},
		{
			Role:      "tool",
			Content:   "Sunny, 25°C",
			ToolUseID: "get_weather",
		},
	}

	contents := convertMessages(messages)
	require.Len(t, contents, 3) // user, model (with function call), function (with result)

	// Verify the model message has thoughtSignature at Part level
	modelContent := contents[1]
	assert.Equal(t, "model", modelContent.Role)
	require.Len(t, modelContent.Parts, 1)
	assert.NotNil(t, modelContent.Parts[0].FunctionCall, "Part should have FunctionCall")
	assert.Equal(t, fakeSignature, modelContent.Parts[0].ThoughtSignature,
		"ThoughtSignature must be echoed back at Part level, NOT inside FunctionCall")
}

func TestThoughtSignature_EmptyWhenAbsent(t *testing.T) {
	// For non-Gemini-3 models or responses without thought signatures,
	// ThoughtSignature should be empty and omitted from JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GenerateContentResponse{
			Candidates: []Candidate{
				{
					Content: Content{
						Role: "model",
						Parts: []Part{
							{
								FunctionCall: &FunctionCall{
									Name: "get_weather",
									Args: map[string]any{"location": "Berlin"},
								},
								// No ThoughtSignature
							},
						},
					},
					FinishReason: "STOP",
				},
			},
			UsageMetadata: UsageMetadata{TotalTokenCount: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Model: "gemini-3-flash-preview"})
	client.httpClient.Transport = &mockTransport{
		baseURL:  server.URL,
		original: http.DefaultTransport,
	}

	ctx := &mockContext{Context: context.Background()}
	resp, err := client.Chat(ctx, []types.Message{
		{Role: "user", Content: "Weather?"},
	}, []shuttle.Tool{&mockShuttleTool{
		name:        "get_weather",
		description: "Get weather",
		schema:      &shuttle.JSONSchema{Type: "object"},
	}})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Empty(t, resp.ToolCalls[0].ThoughtSignature,
		"ThoughtSignature should be empty when not present in response")

	// Verify it doesn't appear in the serialized JSON when empty
	messages := []types.Message{
		{Role: "assistant", ToolCalls: resp.ToolCalls},
	}
	contents := convertMessages(messages)
	jsonBytes, err := json.Marshal(contents[0])
	require.NoError(t, err)
	assert.NotContains(t, string(jsonBytes), "thoughtSignature",
		"thoughtSignature should be omitted from JSON when empty (omitempty)")
}

func TestThoughtSignature_ParallelFunctionCalls(t *testing.T) {
	// Per Gemini docs: only the first function call gets a thought signature
	// in parallel call scenarios
	const firstSig = "sig-for-first-call"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GenerateContentResponse{
			Candidates: []Candidate{
				{
					Content: Content{
						Role: "model",
						Parts: []Part{
							{
								FunctionCall: &FunctionCall{
									Name: "get_weather",
									Args: map[string]any{"location": "Tokyo"},
								},
								ThoughtSignature: firstSig, // Only first gets signature
							},
							{
								FunctionCall: &FunctionCall{
									Name: "get_weather",
									Args: map[string]any{"location": "London"},
								},
								// No signature on second parallel call
							},
						},
					},
					FinishReason: "STOP",
				},
			},
			UsageMetadata: UsageMetadata{TotalTokenCount: 20},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(Config{APIKey: "test-key", Model: "gemini-3-pro-preview"})
	client.httpClient.Transport = &mockTransport{
		baseURL:  server.URL,
		original: http.DefaultTransport,
	}

	ctx := &mockContext{Context: context.Background()}
	resp, err := client.Chat(ctx, []types.Message{
		{Role: "user", Content: "Weather in Tokyo and London?"},
	}, []shuttle.Tool{&mockShuttleTool{
		name:        "get_weather",
		description: "Get weather",
		schema:      &shuttle.JSONSchema{Type: "object"},
	}})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 2)

	assert.Equal(t, firstSig, resp.ToolCalls[0].ThoughtSignature,
		"First parallel call should have thought signature")
	assert.Empty(t, resp.ToolCalls[1].ThoughtSignature,
		"Second parallel call should not have thought signature")
}

func TestConvertTools(t *testing.T) {
	mockTool := &mockShuttleTool{
		name:        "get_weather",
		description: "Get weather information",
		schema: &shuttle.JSONSchema{
			Type: "object",
			Properties: map[string]*shuttle.JSONSchema{
				"location": {
					Type:        "string",
					Description: "City name",
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
	nameMap := make(map[string]string)
	declarations := convertTools(tools, nameMap)

	require.Len(t, declarations, 1)
	assert.Equal(t, "get_weather", declarations[0].Name)
	assert.Equal(t, "Get weather information", declarations[0].Description)
	assert.Equal(t, "object", declarations[0].Parameters.Type)
	assert.Len(t, declarations[0].Parameters.Properties, 2)
	assert.Contains(t, declarations[0].Parameters.Required, "location")
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

// mockTransport is a custom HTTP transport for testing that redirects requests to a test server.
type mockTransport struct {
	baseURL  string
	original http.RoundTripper
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the host with our test server, but keep the original path
	req.URL.Scheme = "http"
	req.URL.Host = t.baseURL[7:] // Remove "http://"
	// Keep original path (contains model name like /v1beta/models/gemini-3-flash-preview:generateContent)

	if t.original != nil {
		return t.original.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)

}
