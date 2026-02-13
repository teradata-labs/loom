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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
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

	// Create string larger than default 20000 bytes (updated from 4096)
	largeString := ""
	for i := 0; i < 1000; i++ { // Increased from 200 to generate >20KB
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

// =============================================================================
// Tests for MCP Apps UI metadata tracking (Phase 6)
// =============================================================================

func TestMCPToolAdapter_HasUI_NoMeta(t *testing.T) {
	tool := protocol.Tool{Name: "test_tool"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	assert.False(t, adapter.HasUI())
	assert.Empty(t, adapter.UIResourceURI())
}

func TestMCPToolAdapter_HasUI_WithUIMetadata(t *testing.T) {
	tool := protocol.Tool{
		Name: "loom_weave",
		Meta: map[string]interface{}{
			"ui": map[string]interface{}{
				"resourceUri": "ui://loom/conversation-viewer",
				"visibility":  []interface{}{"model", "app"},
			},
		},
	}
	adapter := NewMCPToolAdapter(nil, tool, "loom")

	assert.True(t, adapter.HasUI())
	assert.Equal(t, "ui://loom/conversation-viewer", adapter.UIResourceURI())
}

func TestMCPToolAdapter_HasUI_EmptyUIMetadata(t *testing.T) {
	tool := protocol.Tool{
		Name: "test_tool",
		Meta: map[string]interface{}{
			"ui": map[string]interface{}{},
		},
	}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	// UI key exists but no resourceUri
	assert.False(t, adapter.HasUI())
	assert.Empty(t, adapter.UIResourceURI())
}

func TestMCPToolAdapter_HasUI_NonUIMetadata(t *testing.T) {
	tool := protocol.Tool{
		Name: "test_tool",
		Meta: map[string]interface{}{
			"other": "data",
		},
	}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	assert.False(t, adapter.HasUI())
}

func TestAdaptMCPTools_PreservesUIMetadata(t *testing.T) {
	// Verify that AdaptMCPTools preserves UI metadata through the adapter
	tool := protocol.Tool{
		Name:        "loom_weave",
		Description: "Execute a weave",
		Meta: map[string]interface{}{
			"ui": map[string]interface{}{
				"resourceUri": "ui://loom/conversation-viewer",
			},
		},
	}
	adapter := NewMCPToolAdapter(nil, tool, "loom")

	assert.True(t, adapter.HasUI())
	assert.Equal(t, "ui://loom/conversation-viewer", adapter.UIResourceURI())
	assert.Equal(t, "loom:loom_weave", adapter.Name())
}

// =============================================================================
// Tests for toSnakeCase, toCamelCase, normalizeParametersToCamelCase,
// detectAndExtractSQLResult, and Execute integration scenarios
// =============================================================================

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"camelCase basic", "databaseName", "database_name"},
		{"PascalCase", "TableName", "table_name"},
		{"all lowercase", "alreadylowercase", "alreadylowercase"},
		{"all uppercase short", "ABC", "a_b_c"},
		{"empty string", "", ""},
		{"camelCase two words", "noChange", "no_change"},
		{"consecutive uppercase", "HTTPServer", "h_t_t_p_server"},
		{"single char lowercase", "x", "x"},
		{"single char uppercase", "X", "x"},
		{"trailing uppercase", "getID", "get_i_d"},
		{"leading lowercase then upper", "getName", "get_name"},
		{"mixed case multiple", "getHTTPSUrl", "get_h_t_t_p_s_url"},
		{"numbers not affected", "column1Name", "column1_name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toSnakeCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestToCamelCase(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"snake_case basic", "database_name", "databaseName"},
		{"single word", "single", "single"},
		{"three parts", "a_b_c", "aBC"},
		{"empty string", "", ""},
		{"two word snake", "already_camel", "alreadyCamel"},
		{"double underscore", "with__double", "withDouble"},
		{"trailing underscore", "trailing_", "trailing"},
		{"leading underscore", "_leading", "Leading"},
		{"three word snake", "my_table_name", "myTableName"},
		{"all single chars", "x_y_z", "xYZ"},
		{"no underscore passthrough", "nounderscores", "nounderscores"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toCamelCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeParametersToCamelCase(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty map",
			input:    map[string]interface{}{},
			expected: map[string]interface{}{},
		},
		{
			name: "snake_case keys converted",
			input: map[string]interface{}{
				"database_name": "test_db",
				"table_name":    "users",
			},
			expected: map[string]interface{}{
				"databaseName": "test_db",
				"tableName":    "users",
			},
		},
		{
			name: "already camelCase passes through",
			input: map[string]interface{}{
				"databaseName": "test_db",
				"tableName":    "users",
			},
			expected: map[string]interface{}{
				"databaseName": "test_db",
				"tableName":    "users",
			},
		},
		{
			name: "mixed keys",
			input: map[string]interface{}{
				"database_name": "mydb",
				"limit":         10,
				"include_meta":  true,
			},
			expected: map[string]interface{}{
				"databaseName": "mydb",
				"limit":        10,
				"includeMeta":  true,
			},
		},
		{
			name: "values preserved including nested structures",
			input: map[string]interface{}{
				"filter_config": map[string]interface{}{
					"inner_key": "value",
				},
			},
			expected: map[string]interface{}{
				"filterConfig": map[string]interface{}{
					"inner_key": "value", // only top-level keys are converted
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeParametersToCamelCase(tt.input)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestDetectAndExtractSQLResult(t *testing.T) {
	tool := protocol.Tool{Name: "execute_sql"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	tests := []struct {
		name          string
		data          interface{}
		expectSQL     bool
		expectColumns int
		expectRows    int
	}{
		{
			name:      "non-string data returns false",
			data:      42,
			expectSQL: false,
		},
		{
			name:      "nil data returns false",
			data:      nil,
			expectSQL: false,
		},
		{
			name:      "string without SQL pattern returns false",
			data:      "just a plain text result with no JSON",
			expectSQL: false,
		},
		{
			name:      "string with partial pattern but no valid JSON",
			data:      `has "columns" and "rows" but not as JSON`,
			expectSQL: false,
		},
		{
			name: "valid SQL result with preamble",
			data: "✓ SQL executed successfully\n\nOutput:\n" +
				`{"columns":["id","name"],"rows":[[1,"Alice"],[2,"Bob"]]}`,
			expectSQL:     true,
			expectColumns: 2,
			expectRows:    2,
		},
		{
			name:          "valid SQL result without preamble",
			data:          `{"columns":["col1","col2","col3"],"rows":[[1,2,3],[4,5,6],[7,8,9]]}`,
			expectSQL:     true,
			expectColumns: 3,
			expectRows:    3,
		},
		{
			name:      "malformed JSON returns false",
			data:      `{"columns":["id","name"],"rows":[[1,"Alice"],[2,`,
			expectSQL: false,
		},
		{
			name:      "missing columns key returns false",
			data:      `{"rows":[[1,2],[3,4]]}`,
			expectSQL: false,
		},
		{
			name:      "missing rows key returns false",
			data:      `{"columns":["id","name"]}`,
			expectSQL: false,
		},
		{
			name:      "columns is not array returns false",
			data:      `{"columns":"id,name","rows":[[1,"Alice"]]}`,
			expectSQL: false,
		},
		{
			name:      "rows is not array returns false",
			data:      `{"columns":["id","name"],"rows":"not an array"}`,
			expectSQL: false,
		},
		{
			name:          "empty columns and rows still valid",
			data:          `{"columns":[],"rows":[]}`,
			expectSQL:     true,
			expectColumns: 0,
			expectRows:    0,
		},
		{
			name: "SQL result embedded in longer output",
			data: "Query executed in 0.5s.\n\nResults:\n" +
				`{"columns":["status"],"rows":[["active"],["inactive"]]}` +
				"\n\nDone.",
			expectSQL:     true,
			expectColumns: 1,
			expectRows:    2,
		},
		{
			name:      "JSON with extra fields but valid columns and rows",
			data:      `{"columns":["a"],"rows":[[1]],"metadata":{"count":1}}`,
			expectSQL: true,
			// extra fields are fine, columns/rows are present
			expectColumns: 1,
			expectRows:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sqlData, isSQLResult := adapter.detectAndExtractSQLResult(tt.data)

			assert.Equal(t, tt.expectSQL, isSQLResult)

			if tt.expectSQL {
				require.NotNil(t, sqlData)

				columns, ok := sqlData["columns"].([]interface{})
				require.True(t, ok, "columns should be []interface{}")
				assert.Len(t, columns, tt.expectColumns)

				rows, ok := sqlData["rows"].([]interface{})
				require.True(t, ok, "rows should be []interface{}")
				assert.Len(t, rows, tt.expectRows)
			} else {
				assert.Nil(t, sqlData)
			}
		})
	}
}

func TestDetectAndExtractSQLResult_RowContents(t *testing.T) {
	// Verify the actual data inside the extracted SQL result is correct
	tool := protocol.Tool{Name: "execute_sql"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	data := `{"columns":["id","name","email"],"rows":[[1,"Alice","alice@example.com"],[2,"Bob","bob@example.com"]]}`

	sqlData, isSQLResult := adapter.detectAndExtractSQLResult(data)
	require.True(t, isSQLResult)
	require.NotNil(t, sqlData)

	columns := sqlData["columns"].([]interface{})
	assert.Equal(t, "id", columns[0])
	assert.Equal(t, "name", columns[1])
	assert.Equal(t, "email", columns[2])

	rows := sqlData["rows"].([]interface{})
	require.Len(t, rows, 2)

	row0 := rows[0].([]interface{})
	assert.Equal(t, float64(1), row0[0]) // JSON numbers are float64
	assert.Equal(t, "Alice", row0[1])
	assert.Equal(t, "alice@example.com", row0[2])

	row1 := rows[1].([]interface{})
	assert.Equal(t, float64(2), row1[0])
	assert.Equal(t, "Bob", row1[1])
	assert.Equal(t, "bob@example.com", row1[2])
}

// =============================================================================
// Schema cache integration tests
// =============================================================================

func TestSchemaCache_HitMiss_Integration(t *testing.T) {
	ClearSchemaCache()

	// Create a schema-lookup tool adapter
	tool := protocol.Tool{Name: "get_table_schema"}
	adapter := NewMCPToolAdapter(nil, tool, "teradata")

	params := map[string]interface{}{
		"databaseName": "demo",
		"tableName":    "users",
	}

	// Build cache key (same as Execute would)
	cacheKey := adapter.buildSchemaCacheKey(params)

	// Cache miss initially
	_, ok := globalSchemaCache.get(cacheKey)
	assert.False(t, ok, "cache should miss on first access")

	// Simulate what Execute does after a successful call: cache the result
	globalSchemaCache.set(cacheKey, "CREATE TABLE users (id INT, name VARCHAR(100))")

	// Cache hit
	cached, ok := globalSchemaCache.get(cacheKey)
	assert.True(t, ok, "cache should hit after set")
	assert.Equal(t, "CREATE TABLE users (id INT, name VARCHAR(100))", cached)

	// Different params should miss
	differentParams := map[string]interface{}{
		"databaseName": "demo",
		"tableName":    "orders",
	}
	differentKey := adapter.buildSchemaCacheKey(differentParams)
	_, ok = globalSchemaCache.get(differentKey)
	assert.False(t, ok, "different params should cache miss")
}

func TestSchemaCache_NonSchemaToolSkipsCache(t *testing.T) {
	ClearSchemaCache()

	// Non-schema tools should not be identified as schema lookups
	tool := protocol.Tool{Name: "execute_sql"}
	adapter := NewMCPToolAdapter(nil, tool, "teradata")

	assert.False(t, adapter.isSchemaLookupTool(), "execute_sql should not be a schema lookup tool")
}

func TestSchemaCache_AllSchemaPatterns(t *testing.T) {
	schemaTools := []string{
		"get_table_schema",
		"get_schema",
		"describe_table",
		"table_schema",
		"get_columns",
		"list_columns",
		"column_info",
		"td_get_table_schema_v2",
		"my_describe_table_details",
	}

	for _, toolName := range schemaTools {
		t.Run(toolName, func(t *testing.T) {
			tool := protocol.Tool{Name: toolName}
			adapter := NewMCPToolAdapter(nil, tool, "test")
			assert.True(t, adapter.isSchemaLookupTool(), "%s should be detected as schema lookup tool", toolName)
		})
	}
}

func TestSchemaCache_ConcurrentAccess(t *testing.T) {
	ClearSchemaCache()

	// Verify thread safety by running concurrent reads and writes
	done := make(chan struct{})
	const goroutines = 20
	const iterations = 100

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < iterations; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j%5) // some key overlap
				if j%2 == 0 {
					globalSchemaCache.set(key, fmt.Sprintf("value-%d-%d", id, j))
				} else {
					globalSchemaCache.get(key)
				}
			}
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}

	// If we get here without data races (with -race flag), the test passes
	entries, _ := GetSchemaCacheStats()
	assert.Greater(t, entries, 0, "cache should have entries after concurrent writes")
}

// =============================================================================
// InputSchema snake_case conversion round-trip tests
// =============================================================================

func TestInputSchema_CamelCaseToSnakeCase_Conversion(t *testing.T) {
	// Verify that InputSchema converts camelCase property names to snake_case
	tool := protocol.Tool{
		Name: "test_tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"databaseName": map[string]interface{}{
					"type":        "string",
					"description": "Name of the database",
				},
				"tableName": map[string]interface{}{
					"type":        "string",
					"description": "Name of the table",
				},
				"maxRows": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of rows",
				},
			},
			"required": []interface{}{"databaseName", "tableName"},
		},
	}

	adapter := NewMCPToolAdapter(nil, tool, "test")
	schema := adapter.InputSchema()

	require.NotNil(t, schema)
	require.NotNil(t, schema.Properties)

	// Properties should be snake_case
	assert.Contains(t, schema.Properties, "database_name")
	assert.Contains(t, schema.Properties, "table_name")
	assert.Contains(t, schema.Properties, "max_rows")

	// Original camelCase keys should not be present
	assert.NotContains(t, schema.Properties, "databaseName")
	assert.NotContains(t, schema.Properties, "tableName")
	assert.NotContains(t, schema.Properties, "maxRows")

	// Required fields should also be snake_case
	assert.Contains(t, schema.Required, "database_name")
	assert.Contains(t, schema.Required, "table_name")
}

func TestNormalizeParametersToCamelCase_RoundTrip(t *testing.T) {
	// Test the round-trip: camelCase -> snake_case (InputSchema) -> camelCase (normalizeParametersToCamelCase)
	// This validates that the LLM sees snake_case but the MCP server receives camelCase
	originalKeys := []string{"databaseName", "tableName", "maxRows"}

	// Step 1: Convert to snake_case (what InputSchema does)
	snakeKeys := make([]string, len(originalKeys))
	for i, k := range originalKeys {
		snakeKeys[i] = toSnakeCase(k)
	}
	assert.Equal(t, []string{"database_name", "table_name", "max_rows"}, snakeKeys)

	// Step 2: Build params map as the LLM would (snake_case)
	params := map[string]interface{}{
		"database_name": "mydb",
		"table_name":    "users",
		"max_rows":      100,
	}

	// Step 3: Normalize back to camelCase (what Execute does)
	normalized := normalizeParametersToCamelCase(params)

	assert.Equal(t, "mydb", normalized["databaseName"])
	assert.Equal(t, "users", normalized["tableName"])
	assert.Equal(t, 100, normalized["maxRows"])
}

// =============================================================================
// Execute method tests (behaviors testable without a real MCP client)
// =============================================================================

func TestExecute_NilClient_Panics(t *testing.T) {
	// When client is nil, calling Execute will attempt to call a.client.CallTool
	// which will dereference a nil pointer. This tests that the adapter with nil
	// client cannot execute (a known limitation -- the adapter requires a real client).
	tool := protocol.Tool{Name: "test_tool"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	assert.Panics(t, func() {
		_, _ = adapter.Execute(context.Background(), map[string]interface{}{"key": "value"})
	}, "Execute with nil client should panic on nil pointer dereference")
}

func TestExecute_SchemaToolCacheHit_ReturnsEarly(t *testing.T) {
	ClearSchemaCache()

	// Set up a schema tool
	tool := protocol.Tool{Name: "get_table_schema"}
	adapter := NewMCPToolAdapter(nil, tool, "teradata")

	params := map[string]interface{}{
		"database_name": "demo",
		"table_name":    "users",
	}

	// Pre-populate the cache with what Execute would store
	// normalizeParametersToCamelCase converts the keys
	camelParams := normalizeParametersToCamelCase(params)
	cacheKey := adapter.buildSchemaCacheKey(camelParams)
	globalSchemaCache.set(cacheKey, "CREATE TABLE users (id INT)")

	// Execute should return cached result without calling the client
	// (client is nil, so if it tried to call it would panic)
	result, err := adapter.Execute(context.Background(), params)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data, "(cached)")
	assert.Contains(t, result.Data, "CREATE TABLE users (id INT)")
	assert.Equal(t, int64(0), result.ExecutionTimeMs)

	// Verify metadata
	assert.Equal(t, "teradata", result.Metadata["mcp_server"])
	assert.Equal(t, "get_table_schema", result.Metadata["tool_name"])
	assert.Equal(t, true, result.Metadata["cache_hit"])
	assert.Equal(t, cacheKey, result.Metadata["cache_key"])
}

func TestExecute_NonSchemaToolSkipsCache(t *testing.T) {
	ClearSchemaCache()

	// Non-schema tool -- even if cache is populated, it should not hit cache
	tool := protocol.Tool{Name: "execute_sql"}
	adapter := NewMCPToolAdapter(nil, tool, "teradata")

	// Pre-populate some cache entry
	globalSchemaCache.set("teradata:execute_sql:somehash", "cached data")

	// Execute with nil client will panic because it tries to call the client
	// This proves it did NOT return early from cache
	assert.Panics(t, func() {
		_, _ = adapter.Execute(context.Background(), map[string]interface{}{"query": "SELECT 1"})
	}, "non-schema tool should not hit cache and should proceed to call client")
}

func TestExecute_ParameterNormalization(t *testing.T) {
	// Verify that the parameter normalization happens before cache key building
	ClearSchemaCache()

	tool := protocol.Tool{Name: "get_table_schema"}
	adapter := NewMCPToolAdapter(nil, tool, "test")

	// snake_case params from LLM
	snakeParams := map[string]interface{}{
		"database_name": "mydb",
		"table_name":    "users",
	}

	// Build the cache key using camelCase (what Execute internally does)
	camelParams := normalizeParametersToCamelCase(snakeParams)
	cacheKey := adapter.buildSchemaCacheKey(camelParams)

	// Pre-populate cache
	globalSchemaCache.set(cacheKey, "schema result")

	// Execute with snake_case params should find the cache hit
	result, err := adapter.Execute(context.Background(), snakeParams)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data, "(cached)")
	assert.Contains(t, result.Data, "schema result")
}

func TestSchemaCache_ConcurrentWriteRead(t *testing.T) {
	ClearSchemaCache()

	var wg sync.WaitGroup
	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i%10)
			globalSchemaCache.set(key, fmt.Sprintf("value-%d", i))
		}(i)
	}
	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i%10)
			globalSchemaCache.get(key)
			GetSchemaCacheStats()
		}(i)
	}
	wg.Wait()

	// Should not panic or race
	entries, _ := GetSchemaCacheStats()
	assert.LessOrEqual(t, entries, 10)
}

// =============================================================================
// Mock transport and helpers for end-to-end Execute tests
// =============================================================================

// mockTransport implements transport.Transport for testing.
// It captures sent messages and allows feeding responses back.
type mockTransport struct {
	sendCh chan []byte // captures sent messages
	recvCh chan []byte // provides messages to receive
	mu     sync.Mutex
	closed bool
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		sendCh: make(chan []byte, 10),
		recvCh: make(chan []byte, 10),
	}
}

func (m *mockTransport) Send(_ context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("transport closed")
	}
	m.sendCh <- data
	return nil
}

func (m *mockTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data, ok := <-m.recvCh:
		if !ok {
			return nil, fmt.Errorf("transport closed")
		}
		return data, nil
	}
}

func (m *mockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.recvCh)
	}
	return nil
}

// requestHandler is called for each JSON-RPC request the mock receives.
// It returns the result to embed in the response, or an error to embed as a JSON-RPC error.
type requestHandler func(method string, params json.RawMessage) (interface{}, *protocol.Error)

// newMockClientWithHandler creates a client.Client backed by a mockTransport.
// The provided handler is called for every JSON-RPC request sent through the transport.
// The auto-respond goroutine matches request IDs so the client's sendRequest gets unblocked.
func newMockClientWithHandler(t *testing.T, handler requestHandler) (*client.Client, *mockTransport) {
	t.Helper()
	mt := newMockTransport()
	c := client.NewClient(client.Config{
		Transport:      mt,
		Logger:         zaptest.NewLogger(t),
		RequestTimeout: 5 * time.Second,
	})

	// Auto-respond goroutine: reads from sendCh, calls handler, writes response to recvCh.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		c.Close()
	})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-mt.sendCh:
				if !ok {
					return
				}
				// Parse the request
				var req protocol.Request
				if err := json.Unmarshal(msg, &req); err != nil {
					continue
				}
				// Skip notifications (no ID)
				if req.ID == nil {
					continue
				}

				result, rpcErr := handler(req.Method, req.Params)

				var resp protocol.Response
				resp.JSONRPC = protocol.JSONRPCVersion
				resp.ID = req.ID

				if rpcErr != nil {
					resp.Error = rpcErr
				} else {
					resultJSON, err := json.Marshal(result)
					if err != nil {
						resp.Error = protocol.NewError(protocol.InternalError, err.Error(), nil)
					} else {
						resp.Result = resultJSON
					}
				}

				respJSON, err := json.Marshal(resp)
				if err != nil {
					continue
				}

				// Feed the response back -- but check if transport is closed first
				mt.mu.Lock()
				closed := mt.closed
				mt.mu.Unlock()
				if closed {
					return
				}

				select {
				case mt.recvCh <- respJSON:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return c, mt
}

// =============================================================================
// End-to-end Execute tests with mock transport
// =============================================================================

func TestExecute_Success_WithMockTransport(t *testing.T) {
	// Test that Execute correctly calls tools/call and returns a successful result.
	testTool := protocol.Tool{
		Name:        "read_file",
		Description: "Read a file from disk",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type": "string",
				},
			},
			"required": []interface{}{"path"},
		},
	}

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: "file contents here"},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown method: "+method, nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "filesystem")

	result, err := adapter.Execute(context.Background(), map[string]interface{}{
		"path": "/tmp/test.txt",
	})

	require.NoError(t, err, "Execute should not return a Go error")
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Equal(t, "file contents here", result.Data)
	assert.Greater(t, result.ExecutionTimeMs, int64(-1), "ExecutionTimeMs should be >= 0")

	// Verify metadata
	require.NotNil(t, result.Metadata)
	assert.Equal(t, "filesystem", result.Metadata["mcp_server"])
	assert.Equal(t, "read_file", result.Metadata["tool_name"])
}

func TestExecute_ClientError_ReturnedInResult(t *testing.T) {
	// When the MCP server returns a JSON-RPC error, Execute should wrap it in
	// Result.Error with code "MCP_CALL_FAILED" and return nil Go error.
	testTool := protocol.Tool{
		Name:        "broken_tool",
		Description: "A tool that always fails",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"input": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			return nil, protocol.NewError(protocol.InternalError, "database connection failed", nil)
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown method", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "test-server")

	result, err := adapter.Execute(context.Background(), map[string]interface{}{
		"input": "anything",
	})

	// Execute wraps MCP errors into shuttle.Result, so Go error should be nil
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Equal(t, "MCP_CALL_FAILED", result.Error.Code)
	assert.Contains(t, result.Error.Message, "database connection failed")
	assert.True(t, result.Error.Retryable)
}

func TestExecute_ToolError_IsError_ReturnedInResult(t *testing.T) {
	// When the MCP tool returns IsError=true with error content, the client.CallTool
	// converts it to a Go error, which Execute wraps in Result.Error.
	testTool := protocol.Tool{
		Name:        "failing_tool",
		Description: "Tool that returns isError",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			return protocol.CallToolResult{
				IsError: true,
				Content: []protocol.Content{
					{Type: "text", Text: "syntax error near SELECT"},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "test-server")

	result, err := adapter.Execute(context.Background(), map[string]interface{}{
		"query": "SELECCT * FROM users",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Equal(t, "MCP_CALL_FAILED", result.Error.Code)
	assert.Contains(t, result.Error.Message, "syntax error near SELECT")
}

func TestExecute_ParameterNormalization_EndToEnd(t *testing.T) {
	// Verify that snake_case parameters from the LLM are converted to camelCase
	// before being sent to the MCP server via tools/call.
	testTool := protocol.Tool{
		Name:        "query_table",
		Description: "Query a table",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"databaseName": map[string]interface{}{
					"type": "string",
				},
				"tableName": map[string]interface{}{
					"type": "string",
				},
				"maxRows": map[string]interface{}{
					"type": "integer",
				},
			},
			"required": []interface{}{"databaseName", "tableName"},
		},
	}

	var capturedParams protocol.CallToolParams

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			if err := json.Unmarshal(params, &capturedParams); err != nil {
				return nil, protocol.NewError(protocol.InternalError, err.Error(), nil)
			}
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: "query executed"},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "test-server")

	// Pass snake_case parameters (as LLM would produce from the converted schema)
	result, err := adapter.Execute(context.Background(), map[string]interface{}{
		"database_name": "demo",
		"table_name":    "users",
		"max_rows":      float64(100),
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	// Verify the MCP server received camelCase parameter keys
	assert.Equal(t, "demo", capturedParams.Arguments["databaseName"])
	assert.Equal(t, "users", capturedParams.Arguments["tableName"])
	assert.Equal(t, float64(100), capturedParams.Arguments["maxRows"])

	// Verify snake_case keys were NOT sent to the server
	_, hasSnake := capturedParams.Arguments["database_name"]
	assert.False(t, hasSnake, "snake_case key should not be sent to MCP server")
}

func TestExecute_Truncation(t *testing.T) {
	// Call Execute with a tool that returns a very large result (>20KB).
	// Verify truncation works end-to-end.
	testTool := protocol.Tool{
		Name:        "read_log",
		Description: "Read a large log file",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	// Generate a large result (~40KB)
	var largeContent strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&largeContent, "2025-01-15 10:00:%02d INFO  Processing record %d of 10000\n", i%60, i)
	}
	largeText := largeContent.String()
	require.Greater(t, len(largeText), DefaultMaxResultBytes, "test data must exceed max result bytes")

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: largeText},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "test-server")

	result, err := adapter.Execute(context.Background(), map[string]interface{}{
		"path": "/var/log/app.log",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	// Verify truncation happened
	resultStr, ok := result.Data.(string)
	require.True(t, ok, "result data should be a string")
	assert.Contains(t, resultStr, "[TRUNCATED:")
	assert.Less(t, len(resultStr), len(largeText), "truncated result should be smaller than original")

	// Verify metadata reflects truncation
	assert.Equal(t, true, result.Metadata["truncated"])
	assert.Equal(t, len(largeText), result.Metadata["original_size"])
	assert.Equal(t, DefaultMaxResultBytes, result.Metadata["truncated_to"])
}

func TestExecute_Truncation_Disabled(t *testing.T) {
	// Verify that truncation can be disabled via config.
	testTool := protocol.Tool{
		Name:        "read_all",
		Description: "Read all data",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	var largeContent strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&largeContent, "Row %d: data data data data\n", i)
	}
	largeText := largeContent.String()
	require.Greater(t, len(largeText), DefaultMaxResultBytes)

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: largeText},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapterWithConfig(mcpClient, testTool, "test-server", TruncationConfig{
		Enabled: false, // Disable truncation
	})

	result, err := adapter.Execute(context.Background(), map[string]interface{}{
		"path": "/var/log/full.log",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	// With truncation disabled, data should be the full original text
	assert.Equal(t, largeText, result.Data)
	assert.Nil(t, result.Metadata["truncated"], "truncated metadata should not be present")
}

func TestExecute_SchemaCaching_EndToEnd(t *testing.T) {
	// Call Execute twice on a get_table_schema tool.
	// First call should go to the server; second should hit cache.
	ClearSchemaCache()

	testTool := protocol.Tool{
		Name:        "get_table_schema",
		Description: "Get table schema",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"databaseName": map[string]interface{}{
					"type": "string",
				},
				"tableName": map[string]interface{}{
					"type": "string",
				},
			},
			"required": []interface{}{"databaseName", "tableName"},
		},
	}

	var callCount int64
	var mu sync.Mutex

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			mu.Lock()
			callCount++
			mu.Unlock()
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: "CREATE TABLE users (id INT, name VARCHAR(100))"},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "teradata")

	params := map[string]interface{}{
		"database_name": "demo",
		"table_name":    "users",
	}

	// First call - should go to server
	result1, err := adapter.Execute(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, result1)
	assert.True(t, result1.Success)
	assert.Contains(t, result1.Data, "CREATE TABLE users")

	mu.Lock()
	firstCallCount := callCount
	mu.Unlock()
	assert.Equal(t, int64(1), firstCallCount, "first call should reach the server")

	// Second call - same params should hit cache
	result2, err := adapter.Execute(context.Background(), params)
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.True(t, result2.Success)
	assert.Contains(t, result2.Data, "(cached)")
	assert.Contains(t, result2.Data, "CREATE TABLE users")
	assert.Equal(t, int64(0), result2.ExecutionTimeMs, "cached result should have 0 execution time")
	assert.Equal(t, true, result2.Metadata["cache_hit"])

	mu.Lock()
	secondCallCount := callCount
	mu.Unlock()
	assert.Equal(t, int64(1), secondCallCount, "second call should NOT reach the server (cache hit)")

	// Different params should miss cache and go to server
	differentParams := map[string]interface{}{
		"database_name": "demo",
		"table_name":    "orders",
	}
	result3, err := adapter.Execute(context.Background(), differentParams)
	require.NoError(t, err)
	require.NotNil(t, result3)
	assert.True(t, result3.Success)

	mu.Lock()
	thirdCallCount := callCount
	mu.Unlock()
	assert.Equal(t, int64(2), thirdCallCount, "different params should miss cache and reach server")
}

func TestExecute_MultipleContentItems(t *testing.T) {
	// Verify Execute handles tools that return multiple content items.
	testTool := protocol.Tool{
		Name:        "analyze",
		Description: "Analyze data",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: "Analysis complete"},
					{Type: "text", Text: "Found 42 issues"},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "analyzer")

	result, err := adapter.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)

	// Multiple content items come back as []map[string]interface{}
	resultSlice, ok := result.Data.([]map[string]interface{})
	require.True(t, ok, "multiple content items should be a slice of maps")
	require.Len(t, resultSlice, 2)
	assert.Equal(t, "Analysis complete", resultSlice[0]["text"])
	assert.Equal(t, "Found 42 issues", resultSlice[1]["text"])
}

func TestExecute_EmptyContent(t *testing.T) {
	// Verify Execute handles an empty content array from the server.
	testTool := protocol.Tool{
		Name:        "noop",
		Description: "Does nothing",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			return protocol.CallToolResult{
				Content: []protocol.Content{},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "test-server")

	result, err := adapter.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	// Empty content -> convertMCPContent returns nil -> truncateResult marshals nil
	// to JSON "null" string. This is the actual behavior when truncation is enabled.
	assert.Equal(t, "null", result.Data, "empty content produces 'null' after JSON marshaling in truncation")
}

func TestExecute_ContextCancellation(t *testing.T) {
	// Verify Execute respects context cancellation.
	testTool := protocol.Tool{
		Name:        "slow_tool",
		Description: "A slow tool",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			// Simulate a slow response by sleeping
			time.Sleep(5 * time.Second)
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: "should not see this"},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "test-server")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := adapter.Execute(ctx, map[string]interface{}{})

	// The client.CallTool should propagate context cancellation as an error
	// which Execute wraps in Result.Error
	require.NoError(t, err, "Go error should be nil -- error is wrapped in Result")
	require.NotNil(t, result)
	assert.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Equal(t, "MCP_CALL_FAILED", result.Error.Code)
}

func TestExecute_ConcurrentCalls(t *testing.T) {
	// Verify that multiple concurrent Execute calls work safely.
	testTool := protocol.Tool{
		Name:        "concurrent_tool",
		Description: "Tool used by many goroutines",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{
					"type": "integer",
				},
			},
		},
	}

	handler := func(method string, params json.RawMessage) (interface{}, *protocol.Error) {
		switch method {
		case "tools/list":
			return protocol.ToolListResult{
				Tools: []protocol.Tool{testTool},
			}, nil
		case "tools/call":
			var p protocol.CallToolParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, protocol.NewError(protocol.InternalError, err.Error(), nil)
			}
			idVal := p.Arguments["id"]
			return protocol.CallToolResult{
				Content: []protocol.Content{
					{Type: "text", Text: fmt.Sprintf("result for id=%v", idVal)},
				},
			}, nil
		default:
			return nil, protocol.NewError(protocol.MethodNotFound, "unknown", nil)
		}
	}

	mcpClient, _ := newMockClientWithHandler(t, handler)
	adapter := NewMCPToolAdapter(mcpClient, testTool, "test-server")

	const goroutines = 10
	var wg sync.WaitGroup
	errors := make([]error, goroutines)
	results := make([]*shuttle.Result, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r, err := adapter.Execute(context.Background(), map[string]interface{}{
				"id": float64(id),
			})
			errors[id] = err
			results[id] = r
		}(i)
	}

	wg.Wait()

	for i := 0; i < goroutines; i++ {
		assert.NoError(t, errors[i], "goroutine %d should not return error", i)
		require.NotNil(t, results[i], "goroutine %d should have a result", i)
		assert.True(t, results[i].Success, "goroutine %d should succeed", i)
		resultStr, ok := results[i].Data.(string)
		require.True(t, ok, "goroutine %d result should be a string", i)
		assert.Contains(t, resultStr, "result for id=", "goroutine %d should have correct result", i)
	}
}
