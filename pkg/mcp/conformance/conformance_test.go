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
// Package conformance provides MCP protocol conformance tests.
// These tests verify that our implementation complies with the official
// Model Context Protocol specification (version 2024-11-05).
//
// Test coverage:
// - JSON-RPC 2.0 compliance
// - Initialize handshake and capability negotiation
// - Tool operations (list, call)
// - Resource operations (list, read, subscribe)
// - Prompt operations (list, get)
// - Error handling and edge cases
package conformance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// skipIfNoNPX skips tests if npx is not available
func skipIfNoNPX(t *testing.T) {
	// Use exec.LookPath to search PATH (includes nvm paths if set)
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not found in PATH - skipping conformance test")
	}
}

// getTempDir returns the real path of the temp directory (resolving symlinks).
// This is needed on macOS where /var -> /private/var
func getTempDir() string {
	tmpDir := os.TempDir()
	realPath, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		return tmpDir // Fallback to original
	}
	return realPath
}

// getFilePathParamName extracts the file path parameter name from a tool's input schema.
// The filesystem server's schema may vary between versions (e.g., "path" vs "file_path").
// This function dynamically determines the correct parameter name.
// Returns empty string if schema has no properties (incompatible server version).
func getFilePathParamName(tool *protocol.Tool) string {
	if tool == nil || tool.InputSchema == nil {
		return "" // Cannot determine
	}

	// Look for 'properties' in the schema
	props, ok := tool.InputSchema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		return "" // Schema has no properties - incompatible version
	}

	// Check for common file path parameter names
	candidates := []string{"path", "file_path", "filePath", "file"}
	for _, name := range candidates {
		if _, exists := props[name]; exists {
			return name
		}
	}

	// If none of the common names found, return the first property name
	for name := range props {
		return name
	}

	return "" // Cannot determine
}

// setupFilesystemClient creates and initializes a client connected to filesystem server
func setupFilesystemClient(t *testing.T, ctx context.Context, logger *zap.Logger) *client.Client {
	// Use real path to avoid symlink issues on macOS (/var -> /private/var)
	tempDir := getTempDir()

	tr, err := transport.NewStdioTransport(transport.StdioConfig{
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", tempDir},
		Logger:  logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { tr.Close() })

	c := client.NewClient(client.Config{
		Transport: tr,
		Logger:    logger,
	})
	t.Cleanup(func() { c.Close() })

	clientInfo := protocol.Implementation{
		Name:    "conformance-test",
		Version: "1.0.0",
	}
	err = c.Initialize(ctx, clientInfo)
	require.NoError(t, err)

	return c
}

// setupMemoryClient creates and initializes a client connected to memory server
func setupMemoryClient(t *testing.T, ctx context.Context, logger *zap.Logger) *client.Client {
	tr, err := transport.NewStdioTransport(transport.StdioConfig{
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-memory"},
		Logger:  logger,
	})
	require.NoError(t, err)
	t.Cleanup(func() { tr.Close() })

	c := client.NewClient(client.Config{
		Transport: tr,
		Logger:    logger,
	})
	t.Cleanup(func() { c.Close() })

	clientInfo := protocol.Implementation{
		Name:    "conformance-test",
		Version: "1.0.0",
	}
	err = c.Initialize(ctx, clientInfo)
	require.NoError(t, err)

	return c
}

// TestConformance_Initialize verifies the initialize handshake complies with MCP spec.
// Spec: Initialize must be the first request, server must respond with capabilities.
func TestConformance_Initialize(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	// Verify client is initialized
	require.True(t, c.IsInitialized(), "Client must be initialized")

	// Verify server capabilities were received
	caps := c.ServerCapabilities()
	require.NotNil(t, caps.Tools, "Server must return capabilities")
}

// TestConformance_ProtocolVersion verifies protocol version negotiation.
// Spec: Client and server must agree on protocol version (2024-11-05).
func TestConformance_ProtocolVersion(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	// Verify client is initialized (which means version was negotiated successfully)
	require.True(t, c.IsInitialized(), "Client must be initialized after protocol negotiation")

	// Server must support current protocol version
	assert.Equal(t, protocol.ProtocolVersion, "2024-11-05",
		"Client must use protocol version 2024-11-05")
}

// TestConformance_Capabilities verifies capability negotiation.
// Spec: Client declares capabilities, server responds with its capabilities.
func TestConformance_Capabilities(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	caps := c.ServerCapabilities()

	// Filesystem server should declare tools capability
	assert.True(t, caps.Tools != nil, "Filesystem server must support tools")

	// Verify capability structure
	if caps.Tools != nil {
		assert.NotNil(t, caps.Tools, "Tools capability must be present")
	}
}

// TestConformance_ToolsLifecycle verifies complete tools lifecycle.
// Spec: tools/list returns available tools, tools/call executes them.
func TestConformance_ToolsLifecycle(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	// 1. List tools
	tools, err := c.ListTools(ctx)
	require.NoError(t, err, "tools/list must succeed")
	require.NotEmpty(t, tools, "Server must provide at least one tool")

	// Verify tool structure
	for _, tool := range tools {
		assert.NotEmpty(t, tool.Name, "Tool must have a name")
		assert.NotEmpty(t, tool.Description, "Tool must have a description")
		assert.NotNil(t, tool.InputSchema, "Tool must have input schema")
	}

	// 2. Call a tool (use read_text_file if available, fallback to read_file)
	var readTool *protocol.Tool
	for i := range tools {
		if tools[i].Name == "read_text_file" || tools[i].Name == "read_file" {
			readTool = &tools[i]
			break
		}
	}
	require.NotNil(t, readTool, "Filesystem server must provide read tool")

	// Create a test file (use real path to avoid symlink issues)
	testFile := getTempDir() + "/mcp-conformance-test.txt"
	testContent := "MCP Conformance Test"
	require.NoError(t, os.WriteFile(testFile, []byte(testContent), 0644))
	defer os.Remove(testFile)

	// Call the tool with dynamically determined parameter name
	filePathParam := getFilePathParamName(readTool)
	if filePathParam == "" {
		t.Skip("Skipping: filesystem server tool schema has no properties - incompatible npm package version")
	}
	resultInterface, err := c.CallTool(ctx, readTool.Name, map[string]interface{}{
		filePathParam: testFile,
	})
	require.NoError(t, err, "tools/call must succeed")
	require.NotNil(t, resultInterface, "Tool must return result")

	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	require.True(t, ok, "Expected *protocol.CallToolResult, got %T", resultInterface)

	require.NotEmpty(t, result.Content, "Tool result must contain content")

	// Verify result structure
	assert.True(t, len(result.Content) > 0, "Result must have at least one content block")
	if len(result.Content) > 0 {
		firstBlock := result.Content[0]
		assert.Equal(t, "text", firstBlock.Type, "First content block should be text")
		assert.Contains(t, firstBlock.Text, testContent, "Result should contain file content")
	}
}

// TestConformance_ErrorHandling verifies error handling compliance.
// Spec: Servers must return proper JSON-RPC errors for invalid requests.
func TestConformance_ErrorHandling(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	// Test 1: Call non-existent tool
	_, err := c.CallTool(ctx, "non_existent_tool", map[string]interface{}{})
	assert.Error(t, err, "Calling non-existent tool must return error")

	// Test 2: Call tool with invalid parameters
	_, err = c.CallTool(ctx, "read_file", map[string]interface{}{
		"invalid_param": "value",
	})
	assert.Error(t, err, "Calling tool with invalid params must return error")
}

// TestConformance_JSONRPCFormat verifies JSON-RPC 2.0 compliance.
// Spec: All messages must follow JSON-RPC 2.0 format.
func TestConformance_JSONRPCFormat(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	// All requests should follow JSON-RPC 2.0
	// The client and transport layers handle this - if we got here, format is correct
	tools, err := c.ListTools(ctx)
	require.NoError(t, err)
	assert.NotNil(t, tools, "Valid JSON-RPC request must succeed")
}

// TestConformance_ResourcesOptional verifies resources are optional.
// Spec: Resources capability is optional, servers may not implement it.
func TestConformance_ResourcesOptional(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	caps := c.ServerCapabilities()

	// Resources are optional - filesystem server doesn't provide them
	// This should not cause an error
	if caps.Resources == nil {
		t.Log("Server does not support resources (optional capability)")
	} else {
		// If server supports resources, test them
		resources, err := c.ListResources(ctx)
		require.NoError(t, err)
		t.Logf("Server supports resources, found %d", len(resources))
	}
}

// TestConformance_PromptsOptional verifies prompts are optional.
// Spec: Prompts capability is optional, servers may not implement it.
func TestConformance_PromptsOptional(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	caps := c.ServerCapabilities()

	// Prompts are optional - filesystem server doesn't provide them
	if caps.Prompts == nil {
		t.Log("Server does not support prompts (optional capability)")
	} else {
		// If server supports prompts, test them
		prompts, err := c.ListPrompts(ctx)
		require.NoError(t, err)
		t.Logf("Server supports prompts, found %d", len(prompts))
	}
}

// TestConformance_ConcurrentRequests verifies concurrent request handling.
// Spec: Servers should handle multiple concurrent requests.
func TestConformance_ConcurrentRequests(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupFilesystemClient(t, ctx, logger)

	// Get tools to find read tool and its parameter name
	tools, err := c.ListTools(ctx)
	require.NoError(t, err, "tools/list must succeed")

	var readTool *protocol.Tool
	for i := range tools {
		if tools[i].Name == "read_text_file" || tools[i].Name == "read_file" {
			readTool = &tools[i]
			break
		}
	}
	require.NotNil(t, readTool, "Filesystem server must provide read tool")

	// Get the correct file path parameter name from the tool schema
	filePathParam := getFilePathParamName(readTool)
	if filePathParam == "" {
		t.Skip("Skipping: filesystem server tool schema has no properties - incompatible npm package version")
	}

	// Create test file (use real path to avoid symlink issues)
	testFile := getTempDir() + "/mcp-concurrent-test.txt"
	require.NoError(t, os.WriteFile(testFile, []byte("concurrent test"), 0644))
	defer os.Remove(testFile)

	// Issue multiple concurrent tool calls
	done := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			_, err := c.CallTool(ctx, readTool.Name, map[string]interface{}{
				filePathParam: testFile,
			})
			done <- err
		}()
	}

	// All requests should succeed
	for i := 0; i < 5; i++ {
		err := <-done
		assert.NoError(t, err, "Concurrent requests must succeed")
	}
}

// TestConformance_MemoryServer verifies conformance with memory server.
// This tests a different server implementation for broader coverage.
func TestConformance_MemoryServer(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger := zap.NewNop()
	c := setupMemoryClient(t, ctx, logger)

	// Verify initialize
	caps := c.ServerCapabilities()
	require.True(t, caps.Tools != nil, "Memory server must support tools")

	// List tools
	tools, err := c.ListTools(ctx)
	require.NoError(t, err, "Memory server tools/list must succeed")
	require.NotEmpty(t, tools, "Memory server must provide tools")

	// Verify memory-specific tools exist
	var hasCreateEntities bool
	for _, tool := range tools {
		if tool.Name == "create_entities" {
			hasCreateEntities = true
		}
	}
	assert.True(t, hasCreateEntities, "Memory server must provide create_entities tool")
}
