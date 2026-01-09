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

	if client.Model() != "claude-3-5-sonnet-20241022" {
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
			Model:      "claude-3-5-sonnet-20241022",
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
			Model:      "claude-3-5-sonnet-20241022",
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
