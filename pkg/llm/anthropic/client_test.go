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
package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestNewClient(t *testing.T) {
	client := NewClient(Config{
		APIKey: "test-key",
	})

	if client == nil {
		t.Fatal("Expected non-nil client")
	}

	if client.Name() != "anthropic" {
		t.Errorf("Expected name 'anthropic', got %s", client.Name())
	}

	if client.Model() != "claude-sonnet-4-5-20250929" {
		t.Errorf("Expected default model, got %s", client.Model())
	}
}

func TestClient_Chat_SimpleText(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("Expected API key 'test-key', got %s", r.Header.Get("x-api-key"))
		}

		// Return mock response
		resp := MessagesResponse{
			ID:         "msg_123",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-sonnet-4-5-20250929",
			StopReason: "end_turn",
			Content: []ContentBlock{
				{Type: "text", Text: "Hello! How can I help you?"},
			},
			Usage: Usage{
				InputTokens:  10,
				OutputTokens: 20,
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

	// Create mock context
	ctx := &mockContext{Context: context.Background()}

	// Call Chat
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	resp, err := client.Chat(ctx, messages, nil)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if resp.Content != "Hello! How can I help you?" {
		t.Errorf("Expected response content, got %s", resp.Content)
	}

	if resp.Usage.InputTokens != 10 {
		t.Errorf("Expected 10 input tokens, got %d", resp.Usage.InputTokens)
	}

	if resp.Usage.OutputTokens != 20 {
		t.Errorf("Expected 20 output tokens, got %d", resp.Usage.OutputTokens)
	}

	if resp.Usage.TotalTokens != 30 {
		t.Errorf("Expected 30 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestClient_Chat_WithToolCalls(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return mock response with tool call
		resp := MessagesResponse{
			ID:         "msg_123",
			Type:       "message",
			Role:       "assistant",
			Model:      "claude-sonnet-4-5-20250929",
			StopReason: "tool_use",
			Content: []ContentBlock{
				{Type: "text", Text: "I'll execute the tool for you."},
				{
					Type:  "tool_use",
					ID:    "tool_123",
					Name:  "get_weather",
					Input: map[string]interface{}{"city": "San Francisco"},
				},
			},
			Usage: Usage{
				InputTokens:  50,
				OutputTokens: 100,
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

	// Create mock context
	ctx := &mockContext{Context: context.Background()}

	// Create mock tool
	mockTool := &mockTool{
		name:        "get_weather",
		description: "Get weather for a city",
		schema: &shuttle.JSONSchema{
			Type: "object",
			Properties: map[string]*shuttle.JSONSchema{
				"city": {Type: "string"},
			},
			Required: []string{"city"},
		},
	}

	// Call Chat
	messages := []types.Message{
		{Role: "user", Content: "What's the weather in San Francisco?"},
	}

	resp, err := client.Chat(ctx, messages, []shuttle.Tool{mockTool})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(resp.ToolCalls))
	}

	toolCall := resp.ToolCalls[0]
	if toolCall.Name != "get_weather" {
		t.Errorf("Expected tool name 'get_weather', got %s", toolCall.Name)
	}

	if toolCall.ID != "tool_123" {
		t.Errorf("Expected tool ID 'tool_123', got %s", toolCall.ID)
	}

	city, ok := toolCall.Input["city"].(string)
	if !ok || city != "San Francisco" {
		t.Errorf("Expected city 'San Francisco', got %v", toolCall.Input["city"])
	}
}

func TestClient_ConvertMessages(t *testing.T) {
	client := &Client{}

	messages := []types.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "test_tool", Input: map[string]interface{}{"arg": "value"}},
			},
		},
	}

	_, apiMessages := client.convertMessages(messages)

	if len(apiMessages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(apiMessages))
	}

	// Check first message
	if apiMessages[0].Role != "user" {
		t.Errorf("Expected role 'user', got %s", apiMessages[0].Role)
	}

	// Check tool call message
	if len(apiMessages[2].Content) != 1 {
		t.Errorf("Expected 1 content block, got %d", len(apiMessages[2].Content))
	}

	toolUse := apiMessages[2].Content[0]
	if toolUse.Type != "tool_use" {
		t.Errorf("Expected type 'tool_use', got %s", toolUse.Type)
	}
}

func TestContentBlock_MarshalJSON_ToolUseAlwaysHasInput(t *testing.T) {
	// Anthropic API requires tool_use blocks to always have "input" present.
	// Even when the LLM returns a tool call with no arguments, the serialized
	// JSON must include "input": {} — omitting it causes a 400 error.

	tests := []struct {
		name     string
		block    ContentBlock
		wantKey  string // key that must be present
		wantType string // expected type of the key's value
	}{
		{
			name:     "tool_use with nil input gets empty object",
			block:    ContentBlock{Type: "tool_use", ID: "t1", Name: "my_tool", Input: nil},
			wantKey:  "input",
			wantType: "object",
		},
		{
			name:     "tool_use with empty input gets empty object",
			block:    ContentBlock{Type: "tool_use", ID: "t1", Name: "my_tool", Input: map[string]interface{}{}},
			wantKey:  "input",
			wantType: "object",
		},
		{
			name:     "tool_use with populated input preserves it",
			block:    ContentBlock{Type: "tool_use", ID: "t1", Name: "my_tool", Input: map[string]interface{}{"key": "val"}},
			wantKey:  "input",
			wantType: "object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.block)
			if err != nil {
				t.Fatalf("MarshalJSON failed: %v", err)
			}

			var m map[string]interface{}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			val, ok := m[tt.wantKey]
			if !ok {
				t.Fatalf("key %q missing from serialized JSON: %s", tt.wantKey, string(data))
			}

			// Verify it's an object (map)
			if _, isMap := val.(map[string]interface{}); !isMap {
				t.Errorf("expected %q to be an object, got %T: %s", tt.wantKey, val, string(data))
			}
		})
	}

	// Also verify that text blocks do NOT include "input"
	t.Run("text block omits input", func(t *testing.T) {
		block := ContentBlock{Type: "text", Text: "hello"}
		data, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("MarshalJSON failed: %v", err)
		}

		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}

		if _, ok := m["input"]; ok {
			t.Errorf("text block should NOT have 'input' key: %s", string(data))
		}
	})
}

func TestClient_ConvertMessages_WithImages(t *testing.T) {
	client := &Client{}

	messages := []types.Message{
		{
			Role: "user",
			ContentBlocks: []types.ContentBlock{
				{
					Type: "text",
					Text: "What's in this image?",
				},
				{
					Type: "image",
					Image: &types.ImageContent{
						Type: "image",
						Source: types.ImageSource{
							Type:      "base64",
							MediaType: "image/png",
							Data:      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
						},
					},
				},
			},
		},
	}

	_, apiMessages := client.convertMessages(messages)

	if len(apiMessages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(apiMessages))
	}

	// Check message role
	if apiMessages[0].Role != "user" {
		t.Errorf("Expected role 'user', got %s", apiMessages[0].Role)
	}

	// Check content blocks
	if len(apiMessages[0].Content) != 2 {
		t.Errorf("Expected 2 content blocks, got %d", len(apiMessages[0].Content))
	}

	// Check text block
	if apiMessages[0].Content[0].Type != "text" {
		t.Errorf("Expected first block type 'text', got %s", apiMessages[0].Content[0].Type)
	}
	if apiMessages[0].Content[0].Text != "What's in this image?" {
		t.Errorf("Expected text content, got %s", apiMessages[0].Content[0].Text)
	}

	// Check image block
	if apiMessages[0].Content[1].Type != "image" {
		t.Errorf("Expected second block type 'image', got %s", apiMessages[0].Content[1].Type)
	}
	if apiMessages[0].Content[1].Source == nil {
		t.Error("Expected image source to be present")
	} else {
		if apiMessages[0].Content[1].Source.Type != "base64" {
			t.Errorf("Expected source type 'base64', got %s", apiMessages[0].Content[1].Source.Type)
		}
		if apiMessages[0].Content[1].Source.MediaType != "image/png" {
			t.Errorf("Expected media type 'image/png', got %s", apiMessages[0].Content[1].Source.MediaType)
		}
	}
}

func TestClient_ChatStream_ToolInputParsing(t *testing.T) {
	// Simulate Anthropic SSE streaming with tool_use including input_json_delta events.
	// This verifies that tool call inputs are properly accumulated and parsed.
	ssePayload := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":50,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll write that file."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_abc","name":"workspace"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"action\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" \"write\", "}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"path\": \"/tmp/test.txt\", "}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"content\": \"hello world\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	client := NewClient(Config{
		APIKey:   "test-key",
		Endpoint: server.URL,
	})

	var tokens []string
	tokenCB := func(token string) {
		tokens = append(tokens, token)
	}

	messages := []types.Message{
		{Role: "user", Content: "Write a file"},
	}

	resp, err := client.ChatStream(context.Background(), messages, nil, tokenCB)
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}

	// Verify text content was captured
	if resp.Content != "I'll write that file." {
		t.Errorf("Expected text content, got %q", resp.Content)
	}

	// Verify token callback was called
	if len(tokens) == 0 {
		t.Error("Expected token callback to be called")
	}

	// Verify tool call was captured with input
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(resp.ToolCalls))
	}

	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_abc" {
		t.Errorf("Expected tool ID 'toolu_abc', got %q", tc.ID)
	}
	if tc.Name != "workspace" {
		t.Errorf("Expected tool name 'workspace', got %q", tc.Name)
	}

	// Critical check: verify input was parsed from input_json_delta events
	if tc.Input == nil {
		t.Fatal("Tool call input is nil — input_json_delta events were not handled")
	}
	action, _ := tc.Input["action"].(string)
	if action != "write" {
		t.Errorf("Expected action 'write', got %q", action)
	}
	path, _ := tc.Input["path"].(string)
	if path != "/tmp/test.txt" {
		t.Errorf("Expected path '/tmp/test.txt', got %q", path)
	}
	content, _ := tc.Input["content"].(string)
	if content != "hello world" {
		t.Errorf("Expected content 'hello world', got %q", content)
	}

	// Verify stop reason
	if resp.StopReason != "tool_use" {
		t.Errorf("Expected stop_reason 'tool_use', got %q", resp.StopReason)
	}
}

func TestClient_CalculateCost(t *testing.T) {
	client := &Client{}

	// Test with known values
	cost := client.calculateCost(1_000_000, 1_000_000)

	// Expected: $3 + $15 = $18
	expected := 18.0
	if cost != expected {
		t.Errorf("Expected cost $%.2f, got $%.2f", expected, cost)
	}

	// Test with smaller values
	cost = client.calculateCost(1000, 1000)
	expected = 0.018 // $0.003 + $0.015
	if cost != expected {
		t.Errorf("Expected cost $%.6f, got $%.6f", expected, cost)
	}
}

// Mock implementations

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

type mockTool struct {
	name        string
	description string
	schema      *shuttle.JSONSchema
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
	return &shuttle.Result{
		Success: true,
		Data:    map[string]interface{}{"result": "ok"},
	}, nil
}

func (m *mockTool) Backend() string {
	return ""
}
