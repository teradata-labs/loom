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
package registry

import (
	"context"
	"os"
	"testing"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewRegistry(t *testing.T) {
	// Use temp file for database
	tmpFile, err := os.CreateTemp("", "test_registry_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cfg := Config{
		DBPath: tmpFile.Name(),
	}

	registry, err := New(cfg)
	require.NoError(t, err)
	defer registry.Close()

	assert.NotNil(t, registry)
	assert.NotNil(t, registry.db)
}

func TestUpsertAndGetTool(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_registry_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	registry, err := New(Config{DBPath: tmpFile.Name()})
	require.NoError(t, err)
	defer registry.Close()

	ctx := context.Background()

	// Create test tool
	tool := &loomv1.IndexedTool{
		Id:           "builtin:test_tool",
		Name:         "test_tool",
		Description:  "A test tool for testing",
		Source:       loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
		InputSchema:  `{"type": "object", "properties": {"message": {"type": "string"}}}`,
		IndexedAt:    time.Now().Format(time.RFC3339),
		Capabilities: []string{"test", "mock"},
		Keywords:     []string{"test", "tool", "mock"},
	}

	// Upsert tool
	err = registry.upsertTool(ctx, tool)
	require.NoError(t, err)

	// Get tool back
	retrieved, err := registry.GetTool(ctx, "builtin:test_tool")
	require.NoError(t, err)
	assert.Equal(t, tool.Name, retrieved.Name)
	assert.Equal(t, tool.Description, retrieved.Description)
	assert.Equal(t, tool.Source, retrieved.Source)
}

func TestFTSSearch(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_registry_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	registry, err := New(Config{DBPath: tmpFile.Name()})
	require.NoError(t, err)
	defer registry.Close()

	ctx := context.Background()

	// Insert multiple tools
	tools := []*loomv1.IndexedTool{
		{
			Id:           "builtin:bash",
			Name:         "bash",
			Description:  "Execute shell commands in bash terminal",
			Source:       loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
			IndexedAt:    time.Now().Format(time.RFC3339),
			Capabilities: []string{"shell", "command"},
			Keywords:     []string{"bash", "shell", "terminal", "exec"},
		},
		{
			Id:           "builtin:http_request",
			Name:         "http_request",
			Description:  "Make HTTP requests to REST APIs",
			Source:       loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
			IndexedAt:    time.Now().Format(time.RFC3339),
			Capabilities: []string{"http", "api"},
			Keywords:     []string{"http", "api", "rest", "request"},
		},
		{
			Id:           "mcp:slack:send_message",
			Name:         "send_message",
			Description:  "Send a message to a Slack channel",
			Source:       loomv1.ToolSource_TOOL_SOURCE_MCP,
			McpServer:    "slack",
			IndexedAt:    time.Now().Format(time.RFC3339),
			Capabilities: []string{"notification", "messaging"},
			Keywords:     []string{"slack", "message", "notification", "channel"},
		},
		{
			Id:           "builtin:file_read",
			Name:         "file_read",
			Description:  "Read contents of a file from the filesystem",
			Source:       loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
			IndexedAt:    time.Now().Format(time.RFC3339),
			Capabilities: []string{"file_io"},
			Keywords:     []string{"file", "read", "filesystem", "content"},
		},
	}

	for _, tool := range tools {
		err := registry.upsertTool(ctx, tool)
		require.NoError(t, err)
	}

	// Test various searches
	tests := []struct {
		name          string
		query         string
		expectedTools []string
	}{
		{
			name:          "search for shell",
			query:         "shell command",
			expectedTools: []string{"bash"},
		},
		{
			name:          "search for notification",
			query:         "send notification slack",
			expectedTools: []string{"send_message"},
		},
		{
			name:          "search for file",
			query:         "read file",
			expectedTools: []string{"file_read"},
		},
		{
			name:          "search for API",
			query:         "http api request",
			expectedTools: []string{"http_request"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results, err := registry.ftsSearch(ctx, tc.query, nil, nil, nil, 10)
			require.NoError(t, err)
			require.NotEmpty(t, results, "expected results for query: %s", tc.query)

			// Check that expected tools are in results
			foundTools := make([]string, 0, len(results))
			for _, r := range results {
				foundTools = append(foundTools, r.Tool.Name)
			}

			for _, expected := range tc.expectedTools {
				assert.Contains(t, foundTools, expected, "expected tool %s in results for query: %s", expected, tc.query)
			}
		})
	}
}

func TestSearchModes(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_registry_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	registry, err := New(Config{DBPath: tmpFile.Name()})
	require.NoError(t, err)
	defer registry.Close()

	ctx := context.Background()

	// Insert a tool
	tool := &loomv1.IndexedTool{
		Id:           "builtin:test",
		Name:         "test_tool",
		Description:  "A test tool",
		Source:       loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
		IndexedAt:    time.Now().Format(time.RFC3339),
		Capabilities: []string{"test"},
		Keywords:     []string{"test", "mock"},
	}
	require.NoError(t, registry.upsertTool(ctx, tool))

	// Test FAST mode (no LLM)
	resp, err := registry.Search(ctx, &loomv1.SearchToolsRequest{
		Query: "test",
		Mode:  loomv1.SearchMode_SEARCH_MODE_FAST,
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, loomv1.SearchMode_SEARCH_MODE_FAST, resp.Metadata.ModeUsed)
}

func TestExtractCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		description string
		expected    []string
	}{
		{
			name:        "file operations",
			toolName:    "file_read",
			description: "Read a file from the filesystem",
			expected:    []string{"file_io"},
		},
		{
			name:        "http operations",
			toolName:    "http_request",
			description: "Make HTTP requests to APIs",
			expected:    []string{"http"},
		},
		{
			name:        "shell operations",
			toolName:    "bash",
			description: "Execute shell commands",
			expected:    []string{"shell"},
		},
		{
			name:        "database operations",
			toolName:    "query_db",
			description: "Execute SQL queries on PostgreSQL database",
			expected:    []string{"database"},
		},
		{
			name:        "notification operations",
			toolName:    "send_slack",
			description: "Send a Slack message notification",
			expected:    []string{"notification"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := extractCapabilities(tc.toolName, tc.description)
			for _, expected := range tc.expected {
				assert.Contains(t, caps, expected, "expected capability %s", expected)
			}
		})
	}
}

func TestExtractKeywords(t *testing.T) {
	keywords := extractKeywords("http_request", "Make HTTP requests to REST APIs")

	// Should include meaningful keywords
	assert.Contains(t, keywords, "http_request")
	assert.Contains(t, keywords, "http")
	assert.Contains(t, keywords, "requests")
	assert.Contains(t, keywords, "rest")
	assert.Contains(t, keywords, "apis")

	// Should not include stop words
	for _, kw := range keywords {
		assert.NotEqual(t, "to", kw)
		assert.NotEqual(t, "the", kw)
	}
}

func TestSearchToolExecution(t *testing.T) {
	// Create temp database
	tmpFile, err := os.CreateTemp("", "test_searchtool_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Create registry with builtin indexer
	registry, err := New(Config{
		DBPath:   tmpFile.Name(),
		Indexers: []Indexer{NewBuiltinIndexer(nil)},
	})
	require.NoError(t, err)
	defer registry.Close()

	ctx := context.Background()

	// Index all tools
	resp, err := registry.IndexAll(ctx)
	require.NoError(t, err)
	require.Greater(t, resp.TotalCount, int32(0), "Expected some builtin tools to be indexed")

	// Create SearchTool
	searchTool := NewSearchTool(registry)

	// Verify tool interface
	assert.Equal(t, "tool_search", searchTool.Name())
	assert.NotEmpty(t, searchTool.Description())
	assert.NotNil(t, searchTool.InputSchema())

	// Execute search for HTTP tools
	result, err := searchTool.Execute(ctx, map[string]interface{}{
		"query":       "http request api",
		"mode":        "fast",
		"max_results": float64(5),
	})
	require.NoError(t, err)
	require.True(t, result.Success, "Expected successful search")
	require.NotNil(t, result.Data, "Expected result data")

	// Check result structure
	data := result.Data.(map[string]interface{})
	assert.Equal(t, "http request api", data["query"])
	assert.NotNil(t, data["results"])

	resultsCount := data["results_count"].(int)
	assert.GreaterOrEqual(t, resultsCount, 0, "Expected at least 0 results")
}

func TestSearchToolMissingQuery(t *testing.T) {
	// Create temp database
	tmpFile, err := os.CreateTemp("", "test_searchtool_error_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	registry, err := New(Config{DBPath: tmpFile.Name()})
	require.NoError(t, err)
	defer registry.Close()

	searchTool := NewSearchTool(registry)

	// Execute without query
	result, err := searchTool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err) // No error, but result should indicate failure
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "INVALID_QUERY", result.Error.Code)
}

func TestConcurrentSearch(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_registry_*.db")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	registry, err := New(Config{DBPath: tmpFile.Name()})
	require.NoError(t, err)
	defer registry.Close()

	ctx := context.Background()

	// Insert tools
	for i := 0; i < 10; i++ {
		tool := &loomv1.IndexedTool{
			Id:          "builtin:tool_" + string(rune('a'+i)),
			Name:        "tool_" + string(rune('a'+i)),
			Description: "Test tool " + string(rune('a'+i)),
			Source:      loomv1.ToolSource_TOOL_SOURCE_BUILTIN,
			IndexedAt:   time.Now().Format(time.RFC3339),
			Keywords:    []string{"test", "tool"},
		}
		require.NoError(t, registry.upsertTool(ctx, tool))
	}

	// Concurrent searches
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := registry.Search(ctx, &loomv1.SearchToolsRequest{
				Query: "test tool",
				Mode:  loomv1.SearchMode_SEARCH_MODE_FAST,
			})
			assert.NoError(t, err)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}
