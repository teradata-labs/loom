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
package shuttle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/tools/registry"
)

// mockMCPClient implements a minimal MCP client for testing
type mockMCPClient struct {
	toolName string
}

func (m *mockMCPClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{
		"result": fmt.Sprintf("Called %s with %v", name, args),
	}, nil
}

// mockMCPManager implements shuttle.MCPManager for testing
type mockMCPManager struct {
	clients map[string]interface{}
}

func newMockMCPManager() *mockMCPManager {
	return &mockMCPManager{
		clients: make(map[string]interface{}),
	}
}

func (m *mockMCPManager) GetClient(serverName string) (interface{}, error) {
	client, ok := m.clients[serverName]
	if !ok {
		return nil, fmt.Errorf("MCP server not found: %s", serverName)
	}
	return client, nil
}

func (m *mockMCPManager) addClient(serverName string, client interface{}) {
	m.clients[serverName] = client
}

// mockMCPIndexer creates indexed MCP tools for testing
type mockMCPIndexer struct {
	tools []*loomv1.IndexedTool
}

func newMockMCPIndexer(tools []*loomv1.IndexedTool) *mockMCPIndexer {
	return &mockMCPIndexer{tools: tools}
}

func (i *mockMCPIndexer) Name() string {
	return "mock-mcp"
}

func (i *mockMCPIndexer) Source() loomv1.ToolSource {
	return loomv1.ToolSource_TOOL_SOURCE_MCP
}

func (i *mockMCPIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	return i.tools, nil
}

// TestDynamicRegistration_MCPTool verifies MCP tools can be dynamically registered.
func TestDynamicRegistration_MCPTool(t *testing.T) {
	ctx := context.Background()

	// Setup: Create executor with empty registry
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	// Create mock MCP client
	mcpManager := newMockMCPManager()
	mcpManager.addClient("test-server", &mockMCPClient{toolName: "test_tool"})

	// Create indexed MCP tool
	inputSchema := &shuttle.JSONSchema{
		Type: "object",
		Properties: map[string]*shuttle.JSONSchema{
			"message": {Type: "string", Description: "Test message"},
		},
		Required: []string{"message"},
	}
	schemaBytes, _ := json.Marshal(inputSchema)

	mcpTool := &loomv1.IndexedTool{
		Id:          "mcp:test-server:test_tool",
		Name:        "test_tool",
		Description: "A test MCP tool",
		Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
		McpServer:   "test-server",
		InputSchema: string(schemaBytes),
	}

	// Create tool registry with MCP indexer
	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{newMockMCPIndexer([]*loomv1.IndexedTool{mcpTool})},
	})
	require.NoError(t, err)
	defer toolReg.Close()

	// Index tools
	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	// Configure executor
	exec.SetToolRegistry(toolReg)
	exec.SetMCPManager(mcpManager)

	// Verify: test_tool is NOT pre-registered
	_, ok := reg.Get("test_tool")
	require.False(t, ok, "test_tool should not be pre-registered")

	// Test: Try to execute MCP tool (should trigger dynamic registration)
	result, err := exec.Execute(ctx, "test_tool", map[string]interface{}{
		"message": "Hello MCP",
	})

	// Verify: Should succeed via dynamic registration
	require.NoError(t, err, "Dynamic registration should succeed for MCP tool")
	require.NotNil(t, result)

	// Verify: Tool is now registered locally
	tool, ok := reg.Get("test_tool")
	require.True(t, ok, "test_tool should be registered after dynamic registration")
	require.Equal(t, "test_tool", tool.Name())
}

// TestDynamicRegistration_MCPToolMissingServer verifies error when MCP server not found.
func TestDynamicRegistration_MCPToolMissingServer(t *testing.T) {
	ctx := context.Background()

	// Setup
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	// MCP manager without the required server
	mcpManager := newMockMCPManager()

	// MCP tool referencing missing server
	mcpTool := &loomv1.IndexedTool{
		Id:          "mcp:missing-server:test_tool",
		Name:        "test_tool",
		Description: "Tool on missing server",
		Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
		McpServer:   "missing-server",
		InputSchema: `{"type":"object"}`,
	}

	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{newMockMCPIndexer([]*loomv1.IndexedTool{mcpTool})},
	})
	require.NoError(t, err)
	defer toolReg.Close()

	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	exec.SetToolRegistry(toolReg)
	exec.SetMCPManager(mcpManager)

	// Test: Try to execute tool from missing server
	_, err = exec.Execute(ctx, "test_tool", map[string]interface{}{})

	// Verify: Should fail with server not found error
	require.Error(t, err)
	require.Contains(t, err.Error(), "MCP server not found")
}

// TestDynamicRegistration_MCPToolNoManager verifies error when MCP manager not configured.
func TestDynamicRegistration_MCPToolNoManager(t *testing.T) {
	ctx := context.Background()

	// Setup: Executor without MCP manager
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	mcpTool := &loomv1.IndexedTool{
		Id:          "mcp:test-server:test_tool",
		Name:        "test_tool",
		Description: "MCP tool",
		Source:      loomv1.ToolSource_TOOL_SOURCE_MCP,
		McpServer:   "test-server",
		InputSchema: `{"type":"object"}`,
	}

	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{newMockMCPIndexer([]*loomv1.IndexedTool{mcpTool})},
	})
	require.NoError(t, err)
	defer toolReg.Close()

	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	exec.SetToolRegistry(toolReg)
	// Note: NOT calling exec.SetMCPManager()

	// Test: Try to execute MCP tool
	_, err = exec.Execute(ctx, "test_tool", map[string]interface{}{})

	// Verify: Should fail because MCP manager not configured
	require.Error(t, err)
	require.Contains(t, err.Error(), "MCP manager not configured")
}

// TestDynamicRegistration_CustomToolNotSupported verifies custom tools return appropriate error.
func TestDynamicRegistration_CustomToolNotSupported(t *testing.T) {
	ctx := context.Background()

	// Setup
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	// Create custom tool
	customTool := &loomv1.IndexedTool{
		Id:          "custom:my_tool",
		Name:        "my_tool",
		Description: "Custom tool",
		Source:      loomv1.ToolSource_TOOL_SOURCE_CUSTOM,
		InputSchema: `{"type":"object"}`,
	}

	// Mock indexer for custom tool
	customIndexer := &mockCustomIndexer{tools: []*loomv1.IndexedTool{customTool}}

	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{customIndexer},
	})
	require.NoError(t, err)
	defer toolReg.Close()

	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	exec.SetToolRegistry(toolReg)

	// Test: Try to execute custom tool
	_, err = exec.Execute(ctx, "my_tool", map[string]interface{}{})

	// Verify: Should fail with "not yet supported" error
	require.Error(t, err)
	require.Contains(t, err.Error(), "custom tools not yet supported")
}

// mockCustomIndexer for testing custom tool source
type mockCustomIndexer struct {
	tools []*loomv1.IndexedTool
}

func (i *mockCustomIndexer) Name() string {
	return "mock-custom"
}

func (i *mockCustomIndexer) Source() loomv1.ToolSource {
	return loomv1.ToolSource_TOOL_SOURCE_CUSTOM
}

func (i *mockCustomIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	return i.tools, nil
}

// TestDynamicRegistration_UnknownSource verifies error for unknown tool sources.
func TestDynamicRegistration_UnknownSource(t *testing.T) {
	ctx := context.Background()

	// Setup
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	// Create tool with invalid source
	unknownTool := &loomv1.IndexedTool{
		Id:          "unknown:test_tool",
		Name:        "test_tool",
		Description: "Tool with unknown source",
		Source:      loomv1.ToolSource(999), // Invalid source value
		InputSchema: `{"type":"object"}`,
	}

	mockIndexer := &mockUnknownIndexer{tools: []*loomv1.IndexedTool{unknownTool}}

	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{mockIndexer},
	})
	require.NoError(t, err)
	defer toolReg.Close()

	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	exec.SetToolRegistry(toolReg)

	// Test: Try to execute tool with unknown source
	_, err = exec.Execute(ctx, "test_tool", map[string]interface{}{})

	// Verify: Should fail with "unknown tool source" error
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool source")
}

// mockUnknownIndexer for testing unknown tool sources
type mockUnknownIndexer struct {
	tools []*loomv1.IndexedTool
}

func (i *mockUnknownIndexer) Name() string {
	return "mock-unknown"
}

func (i *mockUnknownIndexer) Source() loomv1.ToolSource {
	return loomv1.ToolSource(999)
}

func (i *mockUnknownIndexer) Index(ctx context.Context) ([]*loomv1.IndexedTool, error) {
	return i.tools, nil
}
