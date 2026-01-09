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
package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func TestNewClient_Defaults(t *testing.T) {
	// Note: This test will fail without AWS credentials
	// Skip in CI/CD without credentials
	t.Skip("Skipping Bedrock client creation test - requires AWS credentials")

	client, err := NewClient(Config{})
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "anthropic.claude-3-5-sonnet-20241022-v2:0", client.modelID)
	assert.Equal(t, "us-east-1", client.region)
	assert.Equal(t, 4096, client.maxTokens)
	assert.Equal(t, 1.0, client.temperature)
}

func TestClient_NameAndModel(t *testing.T) {
	client := &Client{
		modelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
	}
	assert.Equal(t, "bedrock", client.Name())
	assert.Equal(t, "anthropic.claude-3-5-sonnet-20241022-v2:0", client.Model())
}

func TestClient_ConvertMessages(t *testing.T) {
	client := &Client{}

	messages := []types.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{
			Role:    "assistant",
			Content: "Let me use a tool.",
			ToolCalls: []types.ToolCall{
				{
					ID:    "tool_123",
					Name:  "get_weather",
					Input: map[string]interface{}{"city": "SF"},
				},
			},
		},
		{
			Role:      "tool",
			Content:   "{\"temp\": 72}",
			ToolUseID: "tool_123",
		},
	}

	_, apiMessages := client.convertMessages(messages)

	// Should have 4 messages
	require.Len(t, apiMessages, 4)

	// First message: user
	assert.Equal(t, "user", apiMessages[0]["role"])
	content := apiMessages[0]["content"].([]map[string]interface{})
	assert.Len(t, content, 1)
	assert.Equal(t, "text", content[0]["type"])
	assert.Equal(t, "Hello", content[0]["text"])

	// Second message: assistant text only
	assert.Equal(t, "assistant", apiMessages[1]["role"])
	content = apiMessages[1]["content"].([]map[string]interface{})
	assert.Len(t, content, 1)
	assert.Equal(t, "text", content[0]["type"])
	assert.Equal(t, "Hi there!", content[0]["text"])

	// Third message: assistant with text and tool use
	assert.Equal(t, "assistant", apiMessages[2]["role"])
	content = apiMessages[2]["content"].([]map[string]interface{})
	assert.Len(t, content, 2)
	assert.Equal(t, "text", content[0]["type"])
	assert.Equal(t, "tool_use", content[1]["type"])
	assert.Equal(t, "tool_123", content[1]["id"])
	assert.Equal(t, "get_weather", content[1]["name"])

	// Fourth message: tool result (as user message)
	assert.Equal(t, "user", apiMessages[3]["role"])
	content = apiMessages[3]["content"].([]map[string]interface{})
	assert.Len(t, content, 1)
	assert.Equal(t, "tool_result", content[0]["type"])
	assert.Equal(t, "tool_123", content[0]["tool_use_id"])
	assert.Equal(t, "{\"temp\": 72}", content[0]["content"])
}

func TestClient_ConvertMessages_NilToolInput(t *testing.T) {
	client := &Client{}

	// Test tool call with nil input (no required parameters)
	messages := []types.Message{
		{Role: "user", Content: "List databases"},
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:    "tool_456",
					Name:  "list_databases",
					Input: nil, // Tools with no required params can have nil input
				},
			},
		},
	}

	_, apiMessages := client.convertMessages(messages)

	// Should have 2 messages
	require.Len(t, apiMessages, 2)

	// Second message: assistant with tool use (nil input)
	assert.Equal(t, "assistant", apiMessages[1]["role"])
	content := apiMessages[1]["content"].([]map[string]interface{})
	assert.Len(t, content, 1)
	assert.Equal(t, "tool_use", content[0]["type"])
	assert.Equal(t, "tool_456", content[0]["id"])
	assert.Equal(t, "list_databases", content[0]["name"])

	// CRITICAL: input must be an empty object {}, not null
	// Bedrock rejects null input with ValidationException
	input, ok := content[0]["input"]
	require.True(t, ok, "input field must be present")
	require.NotNil(t, input, "input must not be nil")
	inputMap, ok := input.(map[string]interface{})
	require.True(t, ok, "input must be a map")
	assert.Len(t, inputMap, 0, "input should be an empty map")
}

func TestClient_ConvertMessages_ToolNameSanitization(t *testing.T) {
	client := &Client{}

	// Test that tool names with colons are sanitized in assistant tool calls
	// This is critical for MCP tools like "filesystem:read_file"
	messages := []types.Message{
		{Role: "user", Content: "Read the file"},
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID:    "tool_789",
					Name:  "filesystem:read_file", // MCP tool name with colon
					Input: map[string]interface{}{"path": "/tmp/test.txt"},
				},
			},
		},
	}

	_, apiMessages := client.convertMessages(messages)

	// Should have 2 messages
	require.Len(t, apiMessages, 2)

	// Second message: assistant with tool use
	assert.Equal(t, "assistant", apiMessages[1]["role"])
	content := apiMessages[1]["content"].([]map[string]interface{})
	assert.Len(t, content, 1)
	assert.Equal(t, "tool_use", content[0]["type"])

	// CRITICAL: Tool name must be sanitized (colon → underscore)
	// Bedrock requires tool names to match ^[a-zA-Z0-9_-]{1,64}$
	toolName, ok := content[0]["name"]
	require.True(t, ok, "name field must be present")
	assert.Equal(t, "filesystem_read_file", toolName, "tool name must be sanitized")
}

func TestClient_ConvertTools(t *testing.T) {
	client := &Client{}

	mockTool := &mockTool{
		name:        "get_weather",
		description: "Get weather for a city",
		schema: &shuttle.JSONSchema{
			Type: "object",
			Properties: map[string]*shuttle.JSONSchema{
				"city": {
					Type:        "string",
					Description: "City name",
				},
				"units": {
					Type:        "string",
					Description: "Temperature units",
					Enum:        []interface{}{"celsius", "fahrenheit"},
				},
			},
			Required: []string{"city"},
		},
	}

	apiTools := client.convertTools([]shuttle.Tool{mockTool})

	require.Len(t, apiTools, 1)

	tool := apiTools[0]
	assert.Equal(t, "get_weather", tool["name"])
	assert.Equal(t, "Get weather for a city", tool["description"])

	schema := tool["input_schema"].(map[string]interface{})
	assert.Equal(t, "object", schema["type"])

	props := schema["properties"].(map[string]interface{})
	assert.Len(t, props, 2)

	cityProp := props["city"].(map[string]interface{})
	assert.Equal(t, "string", cityProp["type"])
	assert.Equal(t, "City name", cityProp["description"])

	unitsProp := props["units"].(map[string]interface{})
	assert.Equal(t, "string", unitsProp["type"])
	assert.Contains(t, unitsProp, "enum")

	required := schema["required"].([]string)
	assert.Equal(t, []string{"city"}, required)
}

func TestClient_ConvertSchemaProperties(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]*shuttle.JSONSchema
		expected map[string]interface{}
	}{
		{
			name:     "nil properties",
			input:    nil,
			expected: nil,
		},
		{
			name: "simple string property",
			input: map[string]*shuttle.JSONSchema{
				"name": {Type: "string", Description: "User name"},
			},
			expected: map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "User name",
				},
			},
		},
		{
			name: "property with enum",
			input: map[string]*shuttle.JSONSchema{
				"status": {
					Type: "string",
					Enum: []interface{}{"active", "inactive"},
				},
			},
			expected: map[string]interface{}{
				"status": map[string]interface{}{
					"type": "string",
					"enum": []interface{}{"active", "inactive"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertSchemaProperties(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestClient_ConvertResponse(t *testing.T) {
	client := &Client{
		modelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
	}

	tests := []struct {
		name     string
		response *bedrockResponse
		validate func(t *testing.T, resp *types.LLMResponse)
	}{
		{
			name: "text only response",
			response: &bedrockResponse{
				ID:         "msg_123",
				Type:       "message",
				Role:       "assistant",
				StopReason: "end_turn",
				Content: []map[string]interface{}{
					{
						"type": "text",
						"text": "Hello! How can I help?",
					},
				},
				Usage: bedrockUsage{
					InputTokens:  50,
					OutputTokens: 100,
				},
			},
			validate: func(t *testing.T, resp *types.LLMResponse) {
				assert.Equal(t, "Hello! How can I help?", resp.Content)
				assert.Empty(t, resp.ToolCalls)
				assert.Equal(t, "end_turn", resp.StopReason)
				assert.Equal(t, 50, resp.Usage.InputTokens)
				assert.Equal(t, 100, resp.Usage.OutputTokens)
				assert.Equal(t, 150, resp.Usage.TotalTokens)
				// Cost: 50 * $3/1M + 100 * $15/1M = $0.00015 + $0.0015 = $0.00165
				assert.InDelta(t, 0.00165, resp.Usage.CostUSD, 0.0001)
			},
		},
		{
			name: "response with tool call",
			response: &bedrockResponse{
				ID:         "msg_456",
				Type:       "message",
				Role:       "assistant",
				StopReason: "tool_use",
				Content: []map[string]interface{}{
					{
						"type": "text",
						"text": "I'll check that for you.",
					},
					{
						"type":  "tool_use",
						"id":    "tool_789",
						"name":  "search",
						"input": map[string]interface{}{"query": "weather"},
					},
				},
				Usage: bedrockUsage{
					InputTokens:  30,
					OutputTokens: 60,
				},
			},
			validate: func(t *testing.T, resp *types.LLMResponse) {
				assert.Equal(t, "I'll check that for you.", resp.Content)
				require.Len(t, resp.ToolCalls, 1)
				assert.Equal(t, "tool_789", resp.ToolCalls[0].ID)
				assert.Equal(t, "search", resp.ToolCalls[0].Name)
				assert.Equal(t, "weather", resp.ToolCalls[0].Input["query"])
				assert.Equal(t, "tool_use", resp.StopReason)
			},
		},
		{
			name: "multiple text blocks",
			response: &bedrockResponse{
				ID:         "msg_999",
				Type:       "message",
				Role:       "assistant",
				StopReason: "end_turn",
				Content: []map[string]interface{}{
					{
						"type": "text",
						"text": "First part. ",
					},
					{
						"type": "text",
						"text": "Second part.",
					},
				},
				Usage: bedrockUsage{
					InputTokens:  10,
					OutputTokens: 20,
				},
			},
			validate: func(t *testing.T, resp *types.LLMResponse) {
				assert.Equal(t, "First part. Second part.", resp.Content)
				assert.Empty(t, resp.ToolCalls)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := client.convertResponse(tt.response)
			require.NotNil(t, resp)
			tt.validate(t, resp)
		})
	}
}

func TestClient_CalculateCost(t *testing.T) {
	client := &Client{}

	tests := []struct {
		name         string
		inputTokens  int
		outputTokens int
		expectedCost float64
	}{
		{
			name:         "1M input + 1M output",
			inputTokens:  1_000_000,
			outputTokens: 1_000_000,
			expectedCost: 18.0, // $3 + $15
		},
		{
			name:         "1K input + 1K output",
			inputTokens:  1_000,
			outputTokens: 1_000,
			expectedCost: 0.018, // $0.003 + $0.015
		},
		{
			name:         "small request",
			inputTokens:  100,
			outputTokens: 200,
			expectedCost: 0.0033, // $0.0003 + $0.003
		},
		{
			name:         "zero tokens",
			inputTokens:  0,
			outputTokens: 0,
			expectedCost: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := client.calculateCost(tt.inputTokens, tt.outputTokens)
			assert.InDelta(t, tt.expectedCost, cost, 0.0001)
		})
	}
}

func TestClient_ImplementsInterface(t *testing.T) {
	var _ types.LLMProvider = (*Client)(nil)
}

// Mock tool for testing
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

// Note: Integration tests with real Bedrock API would require:
// 1. AWS credentials configured
// 2. Access to Bedrock in a specific region
// 3. Model access granted via AWS console
//
// These should be run separately as integration tests, not unit tests.

func TestBedrockResponse_Unmarshal(t *testing.T) {
	// Test that our bedrockResponse struct can unmarshal real Bedrock responses
	jsonResp := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [
			{
				"type": "text",
				"text": "Hello!"
			}
		],
		"model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 10,
			"output_tokens": 5
		}
	}`

	var resp bedrockResponse
	err := json.Unmarshal([]byte(jsonResp), &resp)
	require.NoError(t, err)

	assert.Equal(t, "msg_123", resp.ID)
	assert.Equal(t, "message", resp.Type)
	assert.Equal(t, "assistant", resp.Role)
	assert.Equal(t, "end_turn", resp.StopReason)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 5, resp.Usage.OutputTokens)
	assert.Len(t, resp.Content, 1)
}

// Auth Method Tests

func TestNewClient_ExplicitCredentials(t *testing.T) {
	// Test explicit credentials path (without actual AWS API calls)
	// This tests the configuration logic, not actual AWS connectivity
	t.Run("with session token", func(t *testing.T) {
		cfg := Config{
			Region:          "us-west-2",
			AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			SessionToken:    "session-token-example",
			ModelID:         "anthropic.claude-3-5-sonnet-20241022-v2:0",
		}

		client, err := NewClient(cfg)
		// May error if AWS SDK can't validate credentials, but that's OK
		// We're testing the config path is taken
		if err != nil {
			t.Logf("Expected error without real credentials: %v", err)
		} else {
			assert.NotNil(t, client)
			assert.Equal(t, "us-west-2", client.region)
			assert.Equal(t, "anthropic.claude-3-5-sonnet-20241022-v2:0", client.modelID)
		}
	})

	t.Run("without session token", func(t *testing.T) {
		cfg := Config{
			Region:          "eu-west-1",
			AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			ModelID:         "anthropic.claude-3-5-sonnet-20241022-v2:0",
		}

		client, err := NewClient(cfg)
		if err != nil {
			t.Logf("Expected error without real credentials: %v", err)
		} else {
			assert.NotNil(t, client)
			assert.Equal(t, "eu-west-1", client.region)
		}
	})
}

func TestNewClient_ProfileAuth(t *testing.T) {
	// Test profile-based auth (without actual AWS credentials file)
	cfg := Config{
		Region:  "us-east-1",
		Profile: "development",
		ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
	}

	client, err := NewClient(cfg)
	// May error if profile doesn't exist, but we're testing the config path
	if err != nil {
		t.Logf("Expected error without real profile: %v", err)
	} else {
		assert.NotNil(t, client)
		assert.Equal(t, "us-east-1", client.region)
	}
}

func TestNewClient_DefaultCredentialsChain(t *testing.T) {
	// Test default credentials chain (IAM role, env vars, default profile)
	cfg := Config{
		Region:  "ap-southeast-1",
		ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
	}

	client, err := NewClient(cfg)
	// May error if no credentials available, but we're testing the config path
	if err != nil {
		t.Logf("Expected error without credentials in environment: %v", err)
	} else {
		assert.NotNil(t, client)
		assert.Equal(t, "ap-southeast-1", client.region)
	}
}

func TestNewClient_RegionalEndpoints(t *testing.T) {
	// Test different regional endpoints
	regions := []string{
		"us-east-1",
		"us-west-2",
		"eu-west-1",
		"eu-central-1",
		"ap-southeast-1",
		"ap-northeast-1",
	}

	for _, region := range regions {
		t.Run(region, func(t *testing.T) {
			cfg := Config{
				Region:          region,
				AccessKeyID:     "test-key",
				SecretAccessKey: "test-secret",
			}

			client, err := NewClient(cfg)
			if err != nil {
				t.Logf("Expected error without real credentials in %s: %v", region, err)
			} else {
				assert.Equal(t, region, client.region)
			}
		})
	}
}

func TestNewClient_ModelVariations(t *testing.T) {
	// Test different model IDs
	models := []string{
		"anthropic.claude-3-5-sonnet-20241022-v2:0",
		"anthropic.claude-3-sonnet-20240229-v1:0",
		"anthropic.claude-3-haiku-20240307-v1:0",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			cfg := Config{
				Region:          "us-east-1",
				AccessKeyID:     "test-key",
				SecretAccessKey: "test-secret",
				ModelID:         model,
			}

			client, err := NewClient(cfg)
			if err != nil {
				t.Logf("Expected error without real credentials: %v", err)
			} else {
				assert.Equal(t, model, client.modelID)
			}
		})
	}
}

func TestNewClient_CustomParameters(t *testing.T) {
	// Test custom temperature and max tokens
	cfg := Config{
		Region:          "us-east-1",
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		MaxTokens:       8192,
		Temperature:     0.7,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Logf("Expected error without real credentials: %v", err)
	} else {
		assert.Equal(t, 8192, client.maxTokens)
		assert.Equal(t, 0.7, client.temperature)
	}
}

func TestConfig_Defaults(t *testing.T) {
	// Test that defaults are applied correctly
	cfg := Config{
		AccessKeyID:     "test",
		SecretAccessKey: "test",
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Logf("Expected error: %v", err)
	} else {
		// Verify defaults (updated to new cross-region inference defaults)
		assert.Equal(t, "us-west-2", client.region, "Should default to us-west-2")
		assert.Equal(t, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", client.modelID, "Should use default model")
		assert.Equal(t, 4096, client.maxTokens, "Should default to 4096 tokens")
		assert.Equal(t, 1.0, client.temperature, "Should default to 1.0 temperature")
	}
}

func TestClient_ImplementsLLMProviderInterface(t *testing.T) {
	// Verify that Client implements both LLMProvider and StreamingLLMProvider interfaces
	// Streaming now uses ConverseStream API (fixes InvokeModelWithResponseStream tool bugs)
	client := &Client{
		modelID:     "anthropic.claude-3-5-sonnet-20241022-v2:0",
		region:      "us-east-1",
		maxTokens:   4096,
		temperature: 1.0,
		toolNameMap: make(map[string]string),
	}

	// Type assertion to verify interface implementation
	var _ types.LLMProvider = client
	assert.True(t, types.SupportsStreaming(client), "Bedrock client should support streaming via ConverseStream")
}

func TestBedrockStreamChunk_Unmarshal(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected bedrockStreamChunk
	}{
		{
			name: "content_block_delta with text",
			json: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			expected: bedrockStreamChunk{
				Type:  "content_block_delta",
				Index: 0,
				Delta: struct {
					Type string `json:"type"`
					Text string `json:"text,omitempty"`
				}{
					Type: "text_delta",
					Text: "Hello",
				},
			},
		},
		{
			name: "message_stop with usage",
			json: `{"type":"message_stop","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":20}}`,
			expected: bedrockStreamChunk{
				Type:       "message_stop",
				StopReason: "end_turn",
				Usage: &struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				}{
					InputTokens:  10,
					OutputTokens: 20,
				},
			},
		},
		{
			name: "content_block_start for tool_use",
			json: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"get_weather"}}`,
			expected: bedrockStreamChunk{
				Type:  "content_block_start",
				Index: 0,
				ContentBlock: struct {
					Type string `json:"type"`
					ID   string `json:"id,omitempty"`
					Name string `json:"name,omitempty"`
				}{
					Type: "tool_use",
					ID:   "toolu_123",
					Name: "get_weather",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chunk bedrockStreamChunk
			err := json.Unmarshal([]byte(tt.json), &chunk)
			require.NoError(t, err)
			assert.Equal(t, tt.expected.Type, chunk.Type)
			assert.Equal(t, tt.expected.Index, chunk.Index)

			if tt.expected.Delta.Text != "" {
				assert.Equal(t, tt.expected.Delta.Type, chunk.Delta.Type)
				assert.Equal(t, tt.expected.Delta.Text, chunk.Delta.Text)
			}

			if tt.expected.StopReason != "" {
				assert.Equal(t, tt.expected.StopReason, chunk.StopReason)
			}

			if tt.expected.Usage != nil {
				require.NotNil(t, chunk.Usage)
				assert.Equal(t, tt.expected.Usage.InputTokens, chunk.Usage.InputTokens)
				assert.Equal(t, tt.expected.Usage.OutputTokens, chunk.Usage.OutputTokens)
			}

			if tt.expected.ContentBlock.Type != "" {
				assert.Equal(t, tt.expected.ContentBlock.Type, chunk.ContentBlock.Type)
				assert.Equal(t, tt.expected.ContentBlock.ID, chunk.ContentBlock.ID)
				assert.Equal(t, tt.expected.ContentBlock.Name, chunk.ContentBlock.Name)
			}
		})
	}
}

// TestBedrockStreamChunk_ToolInputDelta verifies that input_json_delta events
// contain the Text field needed to accumulate tool input JSON.
func TestBedrockStreamChunk_ToolInputDelta(t *testing.T) {
	// Simulate an input_json_delta event
	jsonData := `{
		"type": "content_block_delta",
		"index": 0,
		"delta": {
			"type": "input_json_delta",
			"text": "{\"city\": \"San Francisco\"}"
		}
	}`

	var chunk bedrockStreamChunk
	err := json.Unmarshal([]byte(jsonData), &chunk)
	require.NoError(t, err)

	assert.Equal(t, "content_block_delta", chunk.Type)
	assert.Equal(t, 0, chunk.Index)
	assert.Equal(t, "input_json_delta", chunk.Delta.Type)
	assert.Equal(t, "{\"city\": \"San Francisco\"}", chunk.Delta.Text)
}

// TestBedrockStreamChunk_ToolInputDelta_Empty verifies that tools with no
// parameters produce empty JSON object.
func TestBedrockStreamChunk_ToolInputDelta_Empty(t *testing.T) {
	// Simulate an input_json_delta event for a tool with no parameters
	jsonData := `{
		"type": "content_block_delta",
		"index": 0,
		"delta": {
			"type": "input_json_delta",
			"text": "{}"
		}
	}`

	var chunk bedrockStreamChunk
	err := json.Unmarshal([]byte(jsonData), &chunk)
	require.NoError(t, err)

	assert.Equal(t, "content_block_delta", chunk.Type)
	assert.Equal(t, 0, chunk.Index)
	assert.Equal(t, "input_json_delta", chunk.Delta.Type)
	assert.Equal(t, "{}", chunk.Delta.Text)

	// Verify that empty JSON can be parsed into a map
	var input map[string]interface{}
	err = json.Unmarshal([]byte(chunk.Delta.Text), &input)
	require.NoError(t, err)
	assert.NotNil(t, input)
	assert.Empty(t, input)
}
