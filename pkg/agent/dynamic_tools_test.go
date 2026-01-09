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
package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap"
)

func TestNewDynamicToolDiscovery(t *testing.T) {
	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"test": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := manager.NewManager(config, nil)
	require.NoError(t, err)

	discovery := NewDynamicToolDiscovery(mgr, nil)
	require.NotNil(t, discovery)
	assert.NotNil(t, discovery.mcpMgr)
	assert.NotNil(t, discovery.logger)
	assert.NotNil(t, discovery.cache)
}

func TestDynamicToolDiscovery_Matches(t *testing.T) {
	discovery := &DynamicToolDiscovery{}

	tests := []struct {
		name   string
		intent string
		tool   protocol.Tool
		want   bool
	}{
		{
			name:   "exact name match",
			intent: "read_file",
			tool: protocol.Tool{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
			},
			want: true,
		},
		{
			name:   "partial name match",
			intent: "read",
			tool: protocol.Tool{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
			},
			want: true,
		},
		{
			name:   "description match",
			intent: "filesystem",
			tool: protocol.Tool{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
			},
			want: true,
		},
		{
			name:   "case insensitive",
			intent: "READ FILE",
			tool: protocol.Tool{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
			},
			want: true,
		},
		{
			name:   "multiple words - all in description",
			intent: "read file from disk",
			tool: protocol.Tool{
				Name:        "read_file",
				Description: "Read a file from disk storage",
			},
			want: true, // At least half the words match
		},
		{
			name:   "multiple words - partial match",
			intent: "read write execute",
			tool: protocol.Tool{
				Name:        "read_file",
				Description: "Read a file from disk",
			},
			want: true, // "read" matches
		},
		{
			name:   "no match",
			intent: "delete database",
			tool: protocol.Tool{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
			},
			want: false,
		},
		{
			name:   "short words ignored",
			intent: "a is to be",
			tool: protocol.Tool{
				Name:        "test_tool",
				Description: "A test tool",
			},
			want: false, // All words too short (<=2 chars)
		},
		{
			name:   "full phrase in description",
			intent: "execute sql query",
			tool: protocol.Tool{
				Name:        "query",
				Description: "Execute SQL query against database",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := discovery.matches(tt.intent, tt.tool)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDynamicToolDiscovery_ClearCache(t *testing.T) {
	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"test": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := manager.NewManager(config, nil)
	require.NoError(t, err)

	discovery := NewDynamicToolDiscovery(mgr, zap.NewNop())

	// Manually add to cache
	discovery.mu.Lock()
	discovery.cache["test"] = nil // Mock tool
	discovery.mu.Unlock()

	assert.Equal(t, 1, discovery.CacheSize())

	discovery.ClearCache()
	assert.Equal(t, 0, discovery.CacheSize())
}

// Integration test - requires npx
func skipIfNoNPX(t *testing.T) {
	if _, err := os.Stat("/usr/local/bin/npx"); os.IsNotExist(err) {
		if _, err := os.Stat("/usr/bin/npx"); os.IsNotExist(err) {
			t.Skip("npx not found - skipping integration test")
		}
	}
}

func TestDynamicToolDiscovery_Integration_Search(t *testing.T) {
	skipIfNoNPX(t)

	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", os.TempDir()},
				Timeout:   "30s",
			},
		},
		ClientInfo: manager.ClientInfo{
			Name:    "loom-test",
			Version: "0.1.0",
		},
	}

	logger := zap.NewNop()
	mgr, err := manager.NewManager(config, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop() }()

	discovery := NewDynamicToolDiscovery(mgr, logger)

	// Search for a tool
	tool, err := discovery.Search(ctx, "read file")
	require.NoError(t, err)
	require.NotNil(t, tool)

	// Should find filesystem:read_file
	assert.Contains(t, tool.Name(), "read_file")
	assert.Contains(t, tool.Backend(), "mcp:filesystem")

	// Search again - should hit cache
	tool2, err := discovery.Search(ctx, "read file")
	require.NoError(t, err)
	assert.Equal(t, tool.Name(), tool2.Name())

	// Verify cache was used
	assert.Equal(t, 1, discovery.CacheSize())
}

func TestDynamicToolDiscovery_Integration_SearchMultiple(t *testing.T) {
	skipIfNoNPX(t)

	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", os.TempDir()},
				Timeout:   "30s",
			},
		},
		ClientInfo: manager.ClientInfo{
			Name:    "loom-test",
			Version: "0.1.0",
		},
	}

	logger := zap.NewNop()
	mgr, err := manager.NewManager(config, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop() }()

	discovery := NewDynamicToolDiscovery(mgr, logger)

	// Search for multiple tools with broad intent
	tools, err := discovery.SearchMultiple(ctx, "file")
	require.NoError(t, err)
	require.NotEmpty(t, tools)

	// Should find multiple file-related tools
	t.Logf("Found %d tools matching 'file'", len(tools))

	// All should be from filesystem server
	for _, tool := range tools {
		assert.Contains(t, tool.Backend(), "mcp:filesystem")
		assert.Contains(t, tool.Name(), "filesystem:")
	}
}

func TestDynamicToolDiscovery_Integration_SearchNotFound(t *testing.T) {
	skipIfNoNPX(t)

	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", os.TempDir()},
				Timeout:   "30s",
			},
		},
		ClientInfo: manager.ClientInfo{
			Name:    "loom-test",
			Version: "0.1.0",
		},
	}

	logger := zap.NewNop()
	mgr, err := manager.NewManager(config, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop() }()

	discovery := NewDynamicToolDiscovery(mgr, logger)

	// Search for something that doesn't exist
	tool, err := discovery.Search(ctx, "send email to mars colony")
	assert.Error(t, err)
	assert.Nil(t, tool)
	assert.Contains(t, err.Error(), "no tool found")
}

func TestDynamicToolDiscovery_Matches_RealWorldExamples(t *testing.T) {
	discovery := &DynamicToolDiscovery{}

	tests := []struct {
		name   string
		intent string
		tools  []protocol.Tool
		want   []string // Expected matching tool names
	}{
		{
			name:   "filesystem operations",
			intent: "read file",
			tools: []protocol.Tool{
				{Name: "read_file", Description: "Read contents of a file"},
				{Name: "write_file", Description: "Write contents to a file"},
				{Name: "list_directory", Description: "List files in a directory"},
				{Name: "execute_query", Description: "Execute SQL query"},
			},
			// Matches all tools with "file" in name or description
			want: []string{"read_file", "write_file", "list_directory"},
		},
		{
			name:   "broad file search",
			intent: "file",
			tools: []protocol.Tool{
				{Name: "read_file", Description: "Read contents of a file"},
				{Name: "write_file", Description: "Write contents to a file"},
				{Name: "delete_file", Description: "Delete a file"},
				{Name: "execute_query", Description: "Execute SQL query"},
			},
			want: []string{"read_file", "write_file", "delete_file"},
		},
		{
			name:   "database operations",
			intent: "sql query",
			tools: []protocol.Tool{
				{Name: "execute_query", Description: "Execute SQL query against database"},
				{Name: "list_tables", Description: "List database tables"},
				{Name: "read_file", Description: "Read file"},
			},
			want: []string{"execute_query"},
		},
		{
			name:   "github operations",
			intent: "create issue",
			tools: []protocol.Tool{
				{Name: "create_issue", Description: "Create a GitHub issue"},
				{Name: "close_issue", Description: "Close a GitHub issue"},
				{Name: "list_issues", Description: "List GitHub issues"},
			},
			// Matches all tools with "issue" in name or description
			want: []string{"create_issue", "close_issue", "list_issues"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var matches []string
			for _, tool := range tt.tools {
				if discovery.matches(tt.intent, tool) {
					matches = append(matches, tool.Name)
				}
			}

			assert.ElementsMatch(t, tt.want, matches)
		})
	}
}
