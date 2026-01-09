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
package adapter

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func TestMCPToolAdapter_Name(t *testing.T) {
	tool := protocol.Tool{
		Name:        "read_file",
		Description: "Read a file",
	}

	adapter := NewMCPToolAdapter(nil, tool, "filesystem")
	assert.Equal(t, "filesystem:read_file", adapter.Name())
}

func TestMCPToolAdapter_Name_DifferentServers(t *testing.T) {
	tests := []struct {
		serverName string
		toolName   string
		expected   string
	}{
		{"filesystem", "read_file", "filesystem:read_file"},
		{"github", "create_issue", "github:create_issue"},
		{"postgres", "query", "postgres:query"},
		{"custom-server", "custom-tool", "custom-server:custom-tool"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			tool := protocol.Tool{Name: tt.toolName}
			adapter := NewMCPToolAdapter(nil, tool, tt.serverName)
			assert.Equal(t, tt.expected, adapter.Name())
		})
	}
}

func TestMCPToolAdapter_Description(t *testing.T) {
	tool := protocol.Tool{
		Name:        "read_file",
		Description: "Read a file from disk",
	}

	adapter := NewMCPToolAdapter(nil, tool, "filesystem")
	assert.Equal(t, "Read a file from disk", adapter.Description())
}

func TestMCPToolAdapter_Backend(t *testing.T) {
	tests := []struct {
		serverName string
		expected   string
	}{
		{"filesystem", "mcp:filesystem"},
		{"github", "mcp:github"},
		{"postgres", "mcp:postgres"},
	}

	for _, tt := range tests {
		t.Run(tt.serverName, func(t *testing.T) {
			tool := protocol.Tool{Name: "test"}
			adapter := NewMCPToolAdapter(nil, tool, tt.serverName)
			assert.Equal(t, tt.expected, adapter.Backend())
		})
	}
}

func TestMCPToolAdapter_InputSchema_Nil(t *testing.T) {
	tool := protocol.Tool{
		Name:        "test_tool",
		InputSchema: nil,
	}

	adapter := NewMCPToolAdapter(nil, tool, "test")
	schema := adapter.InputSchema()

	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

func TestMCPToolAdapter_InputSchema_Empty(t *testing.T) {
	tool := protocol.Tool{
		Name:        "test_tool",
		InputSchema: map[string]interface{}{},
	}

	adapter := NewMCPToolAdapter(nil, tool, "test")
	schema := adapter.InputSchema()

	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

func TestMCPToolAdapter_InputSchema_Valid(t *testing.T) {
	tool := protocol.Tool{
		Name: "test_tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type": "string",
				},
				"count": map[string]interface{}{
					"type": "integer",
				},
			},
			"required": []interface{}{"path"},
		},
	}

	adapter := NewMCPToolAdapter(nil, tool, "test")
	schema := adapter.InputSchema()

	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// Verify schema was converted correctly
	schemaJSON, err := json.Marshal(schema)
	require.NoError(t, err)

	var reconstructed map[string]interface{}
	err = json.Unmarshal(schemaJSON, &reconstructed)
	require.NoError(t, err)

	assert.Equal(t, "object", reconstructed["type"])
	assert.NotNil(t, reconstructed["properties"])
}

func TestMCPToolAdapter_InputSchema_Complex(t *testing.T) {
	complexSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"config": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"enabled": map[string]interface{}{
						"type": "boolean",
					},
					"retries": map[string]interface{}{
						"type":    "integer",
						"minimum": float64(1),
						"maximum": float64(10),
					},
				},
				"required": []interface{}{"enabled"},
			},
			"tags": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
		"required": []interface{}{"config"},
	}

	tool := protocol.Tool{
		Name:        "complex_tool",
		InputSchema: complexSchema,
	}

	adapter := NewMCPToolAdapter(nil, tool, "test")
	schema := adapter.InputSchema()

	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

func TestConvertMCPContent_Empty(t *testing.T) {
	content := []protocol.Content{}
	result := convertMCPContent(content)
	assert.Nil(t, result)
}

func TestConvertMCPContent_SingleText(t *testing.T) {
	content := []protocol.Content{
		{
			Type: "text",
			Text: "Hello, world!",
		},
	}

	result := convertMCPContent(content)
	assert.Equal(t, "Hello, world!", result)
}

func TestConvertMCPContent_MultipleText(t *testing.T) {
	content := []protocol.Content{
		{
			Type: "text",
			Text: "Part 1",
		},
		{
			Type: "text",
			Text: "Part 2",
		},
	}

	result := convertMCPContent(content)
	resultSlice, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, resultSlice, 2)

	assert.Equal(t, "text", resultSlice[0]["type"])
	assert.Equal(t, "Part 1", resultSlice[0]["text"])
	assert.Equal(t, "text", resultSlice[1]["type"])
	assert.Equal(t, "Part 2", resultSlice[1]["text"])
}

func TestConvertMCPContent_Image(t *testing.T) {
	content := []protocol.Content{
		{
			Type:     "image",
			Data:     "base64encodeddata",
			MimeType: "image/png",
		},
	}

	result := convertMCPContent(content)
	resultSlice, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, resultSlice, 1)

	assert.Equal(t, "image", resultSlice[0]["type"])
	assert.Equal(t, "base64encodeddata", resultSlice[0]["data"])
	assert.Equal(t, "image/png", resultSlice[0]["mimeType"])
}

func TestConvertMCPContent_Resource(t *testing.T) {
	content := []protocol.Content{
		{
			Type: "resource",
			Resource: &protocol.ResourceRef{
				URI:      "file:///tmp/test.txt",
				MimeType: "text/plain",
			},
		},
	}

	result := convertMCPContent(content)
	resultSlice, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, resultSlice, 1)

	assert.Equal(t, "resource", resultSlice[0]["type"])
	assert.Equal(t, "file:///tmp/test.txt", resultSlice[0]["uri"])
	assert.Equal(t, "text/plain", resultSlice[0]["mimeType"])
}

func TestConvertMCPContent_ResourceWithoutMimeType(t *testing.T) {
	content := []protocol.Content{
		{
			Type: "resource",
			Resource: &protocol.ResourceRef{
				URI: "file:///tmp/test.txt",
			},
		},
	}

	result := convertMCPContent(content)
	resultSlice, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, resultSlice, 1)

	assert.Equal(t, "resource", resultSlice[0]["type"])
	assert.Equal(t, "file:///tmp/test.txt", resultSlice[0]["uri"])
}

func TestConvertMCPContent_Mixed(t *testing.T) {
	content := []protocol.Content{
		{
			Type: "text",
			Text: "Here's an image:",
		},
		{
			Type:     "image",
			Data:     "base64data",
			MimeType: "image/png",
		},
		{
			Type: "text",
			Text: "And a resource:",
		},
		{
			Type: "resource",
			Resource: &protocol.ResourceRef{
				URI:      "file:///tmp/test.txt",
				MimeType: "text/plain",
			},
		},
	}

	result := convertMCPContent(content)
	resultSlice, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, resultSlice, 4)

	// Text
	assert.Equal(t, "text", resultSlice[0]["type"])
	assert.Equal(t, "Here's an image:", resultSlice[0]["text"])

	// Image
	assert.Equal(t, "image", resultSlice[1]["type"])
	assert.Equal(t, "base64data", resultSlice[1]["data"])
	assert.Equal(t, "image/png", resultSlice[1]["mimeType"])

	// Text
	assert.Equal(t, "text", resultSlice[2]["type"])
	assert.Equal(t, "And a resource:", resultSlice[2]["text"])

	// Resource
	assert.Equal(t, "resource", resultSlice[3]["type"])
	assert.Equal(t, "file:///tmp/test.txt", resultSlice[3]["uri"])
	assert.Equal(t, "text/plain", resultSlice[3]["mimeType"])
}

func TestConvertMCPContent_UnknownType(t *testing.T) {
	content := []protocol.Content{
		{
			Type: "unknown",
		},
	}

	result := convertMCPContent(content)
	resultSlice, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, resultSlice, 1)

	assert.Equal(t, "unknown", resultSlice[0]["type"])
	// Should not have other fields
	assert.NotContains(t, resultSlice[0], "text")
	assert.NotContains(t, resultSlice[0], "data")
	assert.NotContains(t, resultSlice[0], "uri")
}

func TestNewMCPToolAdapter(t *testing.T) {
	tool := protocol.Tool{
		Name:        "test_tool",
		Description: "Test description",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
	}

	adapter := NewMCPToolAdapter(nil, tool, "test-server")

	require.NotNil(t, adapter)
	assert.Equal(t, "test-server", adapter.serverName)
	assert.Equal(t, "test_tool", adapter.tool.Name)
	assert.Equal(t, "Test description", adapter.tool.Description)
	assert.Nil(t, adapter.client)
}

func TestMCPToolAdapter_AllMethods(t *testing.T) {
	// Test that all shuttle.Tool interface methods are implemented
	tool := protocol.Tool{
		Name:        "complete_tool",
		Description: "A complete tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"param": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	adapter := NewMCPToolAdapter(nil, tool, "test-server")

	// Name
	name := adapter.Name()
	assert.Equal(t, "test-server:complete_tool", name)

	// Description
	desc := adapter.Description()
	assert.Equal(t, "A complete tool", desc)

	// Backend
	backend := adapter.Backend()
	assert.Equal(t, "mcp:test-server", backend)

	// InputSchema
	schema := adapter.InputSchema()
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// Execute would require a real client, tested in integration tests
}

func TestConvertMCPContent_NilResource(t *testing.T) {
	content := []protocol.Content{
		{
			Type:     "resource",
			Resource: nil, // Nil resource
		},
	}

	result := convertMCPContent(content)
	resultSlice, ok := result.([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, resultSlice, 1)

	assert.Equal(t, "resource", resultSlice[0]["type"])
	// Should not have uri or mimeType fields
	assert.NotContains(t, resultSlice[0], "uri")
	assert.NotContains(t, resultSlice[0], "mimeType")
}

func TestMCPToolAdapter_InputSchema_InvalidJSON(t *testing.T) {
	// Create a schema that can't be properly marshaled/unmarshaled
	// This tests the error handling in InputSchema()
	tool := protocol.Tool{
		Name: "test_tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"valid": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	adapter := NewMCPToolAdapter(nil, tool, "test")
	schema := adapter.InputSchema()

	// Should return a valid schema even if conversion has issues
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// =============================================================================
// Tests for result truncation (#1) and schema caching (#4)
// =============================================================================

func TestTruncateString_NoTruncation(t *testing.T) {
	tool := protocol.Tool{Name: "test"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	// Small string - no truncation
	result, truncated, originalSize := adapter.truncateString("hello world")
	assert.Equal(t, "hello world", result)
	assert.False(t, truncated)
	assert.Equal(t, 11, originalSize)
}

func TestTruncateString_LargeString(t *testing.T) {
	tool := protocol.Tool{Name: "test"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	// Create string larger than default 4096 bytes
	largeString := ""
	for i := 0; i < 200; i++ {
		largeString += "Row " + string(rune('A'+i%26)) + ": some data here\n"
	}

	result, truncated, originalSize := adapter.truncateString(largeString)

	assert.True(t, truncated)
	assert.Greater(t, originalSize, DefaultMaxResultBytes)

	resultStr := result.(string)
	assert.Contains(t, resultStr, "[TRUNCATED:")
	assert.LessOrEqual(t, len(resultStr), originalSize)
}

func TestTruncateResult_TypeSwitch(t *testing.T) {
	tool := protocol.Tool{Name: "test"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	tests := []struct {
		name        string
		input       interface{}
		expectTrunc bool
	}{
		{"small string", "hello", false},
		{"nil", nil, false},
		{"small map", map[string]interface{}{"key": "value"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, truncated, _ := adapter.truncateResult(tt.input)
			assert.Equal(t, tt.expectTrunc, truncated)
			assert.NotNil(t, result)
		})
	}
}

func TestIsSchemaLookupTool(t *testing.T) {
	tests := []struct {
		toolName string
		expected bool
	}{
		{"teradata_get_table_schema", true},
		{"get_schema", true},
		{"describe_table", true},
		{"get_columns", true},
		{"list_columns", true},
		{"execute_sql", false},
		{"read_file", false},
		{"query", false},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			tool := protocol.Tool{Name: tt.toolName}
			adapter := NewMCPToolAdapter(nil, tool, "test")
			assert.Equal(t, tt.expected, adapter.isSchemaLookupTool())
		})
	}
}

func TestBuildSchemaCacheKey(t *testing.T) {
	tool := protocol.Tool{Name: "get_table_schema"}
	adapter := NewMCPToolAdapter(nil, tool, "teradata")

	params1 := map[string]interface{}{"table": "demo.users"}
	params2 := map[string]interface{}{"table": "demo.orders"}
	params3 := map[string]interface{}{"table": "demo.users"}

	key1 := adapter.buildSchemaCacheKey(params1)
	key2 := adapter.buildSchemaCacheKey(params2)
	key3 := adapter.buildSchemaCacheKey(params3)

	// Same params should produce same key
	assert.Equal(t, key1, key3)
	// Different params should produce different key
	assert.NotEqual(t, key1, key2)
	// Key should contain server and tool name
	assert.Contains(t, key1, "teradata")
	assert.Contains(t, key1, "get_table_schema")
}

func TestSchemaCache_SetAndGet(t *testing.T) {
	// Clear cache before test
	ClearSchemaCache()

	globalSchemaCache.set("test-key", "test-result")

	result, ok := globalSchemaCache.get("test-key")
	assert.True(t, ok)
	assert.Equal(t, "test-result", result)

	// Non-existent key
	_, ok = globalSchemaCache.get("nonexistent")
	assert.False(t, ok)
}

func TestSchemaCache_Stats(t *testing.T) {
	ClearSchemaCache()

	// Empty cache
	entries, _ := GetSchemaCacheStats()
	assert.Equal(t, 0, entries)

	// Add entries
	globalSchemaCache.set("key1", "value1")
	globalSchemaCache.set("key2", "value2")

	entries, age := GetSchemaCacheStats()
	assert.Equal(t, 2, entries)
	assert.Less(t, age.Seconds(), float64(1)) // Should be very recent
}

func TestNewMCPToolAdapterWithConfig(t *testing.T) {
	tool := protocol.Tool{Name: "test"}

	// Custom config
	config := TruncationConfig{
		MaxResultBytes: 8192,
		MaxResultRows:  50,
		Enabled:        true,
	}

	adapter := NewMCPToolAdapterWithConfig(nil, tool, "test", config)
	assert.Equal(t, 8192, adapter.truncation.MaxResultBytes)
	assert.Equal(t, 50, adapter.truncation.MaxResultRows)
	assert.True(t, adapter.truncation.Enabled)
}

func TestNewMCPToolAdapterWithConfig_Defaults(t *testing.T) {
	tool := protocol.Tool{Name: "test"}

	// Config with zeros should use defaults
	config := TruncationConfig{
		MaxResultBytes: 0,
		MaxResultRows:  0,
		Enabled:        true,
	}

	adapter := NewMCPToolAdapterWithConfig(nil, tool, "test", config)
	assert.Equal(t, DefaultMaxResultBytes, adapter.truncation.MaxResultBytes)
	assert.Equal(t, DefaultMaxResultRows, adapter.truncation.MaxResultRows)
}

func TestTruncateArrayResult(t *testing.T) {
	tool := protocol.Tool{Name: "test"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	// Small array - no truncation
	smallItems := []map[string]interface{}{
		{"type": "text", "text": "hello"},
		{"type": "text", "text": "world"},
	}

	result, truncated, _ := adapter.truncateArrayResult(smallItems)
	assert.False(t, truncated)
	assert.Equal(t, smallItems, result)
}

func TestClearSchemaCache(t *testing.T) {
	globalSchemaCache.set("key1", "value1")
	globalSchemaCache.set("key2", "value2")

	entries, _ := GetSchemaCacheStats()
	assert.Equal(t, 2, entries)

	ClearSchemaCache()

	entries, _ = GetSchemaCacheStats()
	assert.Equal(t, 0, entries)
}
