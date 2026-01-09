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
// Package mcp_test provides integration tests for the MCP implementation.
// These tests require a real MCP server to be available.
//
// Prerequisites:
//
//	npm install -g @modelcontextprotocol/server-filesystem
//
// Run tests:
//
//	go test -v -tags=integration ./pkg/mcp/
package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/adapter"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// skipIfNoNPX skips the test if npx is not available
func skipIfNoNPX(t *testing.T) {
	if _, err := os.Stat("/usr/local/bin/npx"); os.IsNotExist(err) {
		// Try common locations
		if _, err := os.Stat("/usr/bin/npx"); os.IsNotExist(err) {
			t.Skip("npx not found - skipping integration test (install Node.js and npm)")
		}
	}
}

func TestMCPClient_Initialize_Integration(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := zap.NewNop()

	// Create transport
	trans, err := transport.NewStdioTransport(transport.StdioConfig{
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", os.TempDir()},
		Logger:  logger,
	})
	require.NoError(t, err)
	defer trans.Close()

	// Create client
	mcpClient := client.NewClient(client.Config{
		Transport: trans,
		Logger:    logger,
	})
	defer mcpClient.Close()

	// Initialize
	clientInfo := protocol.Implementation{
		Name:    "loom-integration-test",
		Version: "0.1.0",
	}

	err = mcpClient.Initialize(ctx, clientInfo)
	require.NoError(t, err)

	// Verify initialized
	assert.True(t, mcpClient.IsInitialized())
}

func TestMCPClient_ListTools_Integration(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := zap.NewNop()
	mcpClient := setupFilesystemClient(t, ctx, logger)
	defer mcpClient.Close()

	// List tools
	tools, err := mcpClient.ListTools(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tools)

	// Filesystem server should provide tools
	toolNames := make([]string, len(tools))
	for i, tool := range tools {
		toolNames[i] = tool.Name
	}

	t.Logf("Found %d tools: %v", len(tools), toolNames)

	// Should have common filesystem operations
	assert.Contains(t, toolNames, "read_file")
}

func TestMCPClient_CallTool_Integration(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := zap.NewNop()
	mcpClient := setupFilesystemClient(t, ctx, logger)
	defer mcpClient.Close()

	// Create a temporary test file
	tmpFile := filepath.Join(os.TempDir(), "loom-test-file.txt")
	testContent := "Hello from Loom integration test!"
	err := os.WriteFile(tmpFile, []byte(testContent), 0644)
	require.NoError(t, err)
	defer os.Remove(tmpFile)

	// List tools to find read_file
	tools, err := mcpClient.ListTools(ctx)
	require.NoError(t, err)

	var readFileTool *protocol.Tool
	for _, tool := range tools {
		if tool.Name == "read_file" {
			t := tool
			readFileTool = &t
			break
		}
	}

	if readFileTool == nil {
		t.Skip("read_file tool not available")
	}

	// Call the tool
	resultInterface, err := mcpClient.CallTool(ctx, "read_file", map[string]interface{}{
		"path": tmpFile,
	})

	require.NoError(t, err)
	require.NotNil(t, resultInterface)

	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	require.True(t, ok, "Expected *protocol.CallToolResult, got %T", resultInterface)

	assert.False(t, result.IsError)
	require.NotEmpty(t, result.Content)

	// Check content
	assert.Equal(t, "text", result.Content[0].Type)
	assert.Contains(t, result.Content[0].Text, testContent)
}

func TestAdapter_Execute_Integration(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := zap.NewNop()
	mcpClient := setupFilesystemClient(t, ctx, logger)
	defer mcpClient.Close()

	// Create test file
	tmpFile := filepath.Join(os.TempDir(), "loom-adapter-test.txt")
	testContent := "Adapter integration test content"
	err := os.WriteFile(tmpFile, []byte(testContent), 0644)
	require.NoError(t, err)
	defer os.Remove(tmpFile)

	// Get tools
	tools, err := mcpClient.ListTools(ctx)
	require.NoError(t, err)

	var readFileTool protocol.Tool
	for _, tool := range tools {
		if tool.Name == "read_file" {
			readFileTool = tool
			break
		}
	}

	if readFileTool.Name == "" {
		t.Skip("read_file tool not available")
	}

	// Create adapter
	toolAdapter := adapter.NewMCPToolAdapter(mcpClient, readFileTool, "filesystem")

	// Test shuttle.Tool interface
	assert.Equal(t, "filesystem:read_file", toolAdapter.Name())
	assert.Equal(t, "mcp:filesystem", toolAdapter.Backend())
	assert.NotNil(t, toolAdapter.InputSchema())

	// Execute via shuttle.Tool interface
	result, err := toolAdapter.Execute(ctx, map[string]interface{}{
		"path": tmpFile,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Nil(t, result.Error)

	// Check result data
	assert.Contains(t, result.Data, testContent)

	// Check metadata
	require.NotNil(t, result.Metadata)
	assert.Equal(t, "filesystem", result.Metadata["mcp_server"])
	assert.Equal(t, "read_file", result.Metadata["tool_name"])

	// Check execution time was recorded
	assert.Greater(t, result.ExecutionTimeMs, int64(0))
}

func TestAdaptMCPTools_Integration(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := zap.NewNop()
	mcpClient := setupFilesystemClient(t, ctx, logger)
	defer mcpClient.Close()

	// Adapt all MCP tools
	shuttleTools, err := adapter.AdaptMCPTools(ctx, mcpClient, "filesystem")
	require.NoError(t, err)
	require.NotEmpty(t, shuttleTools)

	t.Logf("Adapted %d tools", len(shuttleTools))

	// Verify each tool is properly adapted
	for _, tool := range shuttleTools {
		// Name should be prefixed
		assert.Contains(t, tool.Name(), "filesystem:")

		// Backend should be mcp:filesystem
		assert.Equal(t, "mcp:filesystem", tool.Backend())

		// Description should not be empty
		assert.NotEmpty(t, tool.Description())

		// Schema should exist
		assert.NotNil(t, tool.InputSchema())
	}

	// Try to find and use a specific tool
	var readFileTool *protocol.Tool
	for _, tool := range shuttleTools {
		if tool.Name() == "filesystem:read_file" {
			t.Log("Found filesystem:read_file tool")
			break
		}
	}

	// All adapted tools should be usable via shuttle.Tool interface
	assert.NotNil(t, readFileTool)
}

func TestMCPClient_Ping_Integration(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := zap.NewNop()
	mcpClient := setupFilesystemClient(t, ctx, logger)
	defer mcpClient.Close()

	// Ping the server
	err := mcpClient.Ping(ctx)
	assert.NoError(t, err)
}

func TestMCPClient_ErrorHandling_Integration(t *testing.T) {
	skipIfNoNPX(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := zap.NewNop()
	mcpClient := setupFilesystemClient(t, ctx, logger)
	defer mcpClient.Close()

	// Try to call non-existent tool
	resultInterface, err := mcpClient.CallTool(ctx, "nonexistent_tool", map[string]interface{}{})

	// Should get an error (either from client or server)
	if err == nil {
		// Server returned an error result
		require.NotNil(t, resultInterface)
		result, ok := resultInterface.(*protocol.CallToolResult)
		require.True(t, ok, "Expected *protocol.CallToolResult, got %T", resultInterface)
		assert.True(t, result.IsError)
	}
}

// setupFilesystemClient is a helper to create and initialize a filesystem MCP client
func setupFilesystemClient(t *testing.T, ctx context.Context, logger *zap.Logger) *client.Client {
	trans, err := transport.NewStdioTransport(transport.StdioConfig{
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", os.TempDir()},
		Logger:  logger,
	})
	require.NoError(t, err)

	mcpClient := client.NewClient(client.Config{
		Transport: trans,
		Logger:    logger,
	})

	clientInfo := protocol.Implementation{
		Name:    "loom-integration-test",
		Version: "0.1.0",
	}

	err = mcpClient.Initialize(ctx, clientInfo)
	require.NoError(t, err)

	return mcpClient
}
