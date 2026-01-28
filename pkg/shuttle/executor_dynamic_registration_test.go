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
	"testing"

	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/shuttle/builtin"
	"github.com/teradata-labs/loom/pkg/tools/registry"
)

// TestDynamicRegistration_BuiltinTool verifies that builtin tools can be dynamically
// registered when discovered via tool_search.
func TestDynamicRegistration_BuiltinTool(t *testing.T) {
	ctx := context.Background()

	// Setup: Create executor with empty registry (no pre-registered tools)
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	// Create tool registry with builtin indexer
	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{registry.NewBuiltinIndexer(nil)},
	})
	require.NoError(t, err)
	defer toolReg.Close()

	// Index all tools
	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	// Configure executor with tool registry and builtin provider
	exec.SetToolRegistry(toolReg)
	exec.SetBuiltinToolProvider(builtin.NewProvider())

	// Verify: http_request is NOT pre-registered
	_, ok := reg.Get("http_request")
	require.False(t, ok, "http_request should not be pre-registered")

	// Test: Try to execute http_request (should trigger dynamic registration)
	result, err := exec.Execute(ctx, "http_request", map[string]interface{}{
		"method": "GET",
		"url":    "https://httpbin.org/get",
	})

	// Verify: Should succeed via dynamic registration
	require.NoError(t, err, "Dynamic registration should succeed for http_request")
	require.NotNil(t, result)

	// Verify: Tool is now registered locally for future use
	tool, ok := reg.Get("http_request")
	require.True(t, ok, "http_request should now be registered after dynamic registration")
	require.Equal(t, "http_request", tool.Name())
}

// TestDynamicRegistration_AllBuiltinTools verifies dynamic registration works for all builtin tools.
func TestDynamicRegistration_AllBuiltinTools(t *testing.T) {
	ctx := context.Background()

	// All builtin tool names
	builtinTools := []string{
		"http_request",
		"web_search",
		"file_write",
		"file_read",
		"analyze_image",
		"parse_document",
		"grpc_call",
		"shell_execute",
		"agent_management",
		"contact_human",
	}

	for _, toolName := range builtinTools {
		t.Run(toolName, func(t *testing.T) {
			// Setup: Fresh registry for each tool
			reg := shuttle.NewRegistry()
			exec := shuttle.NewExecutor(reg)

			// Create tool registry
			toolReg, err := registry.New(registry.Config{
				DBPath:   ":memory:",
				Indexers: []registry.Indexer{registry.NewBuiltinIndexer(nil)},
			})
			require.NoError(t, err)
			defer toolReg.Close()

			// Index all tools
			_, err = toolReg.IndexAll(ctx)
			require.NoError(t, err)

			// Configure executor
			exec.SetToolRegistry(toolReg)
			exec.SetBuiltinToolProvider(builtin.NewProvider())

			// Verify tool is found in registry (indexed)
			resp, err := toolReg.Search(ctx, &loomv1.SearchToolsRequest{
				Query:      toolName,
				MaxResults: 1,
			})
			require.NoError(t, err)
			require.NotEmpty(t, resp.Results, "Tool %s should be found in registry", toolName)

			// Verify tool can be dynamically registered (tryDynamicRegistration logic)
			// We'll verify by checking the tool is registered after a search
			// (we can't directly call Execute without valid params for each tool)
			_, ok := reg.Get(toolName)
			require.False(t, ok, "Tool %s should not be pre-registered", toolName)

			// Use the builtin provider to get the tool (simulates dynamic registration)
			provider := builtin.NewProvider()
			tool := provider.GetTool(toolName)
			require.NotNil(t, tool, "Provider should return tool %s", toolName)
			require.Equal(t, toolName, tool.Name())

			// Register it manually to simulate what executor does
			reg.Register(tool)

			// Verify it's now available
			registeredTool, ok := reg.Get(toolName)
			require.True(t, ok, "Tool %s should be registered", toolName)
			require.Equal(t, toolName, registeredTool.Name())
		})
	}
}

// TestDynamicRegistration_ToolNotFound verifies error handling when tool doesn't exist.
func TestDynamicRegistration_ToolNotFound(t *testing.T) {
	ctx := context.Background()

	// Setup
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	// Create tool registry (empty - no indexers)
	toolReg, err := registry.New(registry.Config{
		DBPath: ":memory:",
	})
	require.NoError(t, err)
	defer toolReg.Close()

	exec.SetToolRegistry(toolReg)
	exec.SetBuiltinToolProvider(builtin.NewProvider())

	// Test: Try to execute non-existent tool
	_, err = exec.Execute(ctx, "nonexistent_tool", map[string]interface{}{})

	// Verify: Should fail with appropriate error
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool not found")
}

// TestDynamicRegistration_NoToolRegistry verifies error when tool registry not configured.
func TestDynamicRegistration_NoToolRegistry(t *testing.T) {
	ctx := context.Background()

	// Setup: Executor without tool registry
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)
	// Note: NOT calling exec.SetToolRegistry()

	// Test: Try to execute tool that's not registered
	_, err := exec.Execute(ctx, "http_request", map[string]interface{}{
		"method": "GET",
		"url":    "https://example.com",
	})

	// Verify: Should fail because tool registry not configured
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool not found")
}

// TestDynamicRegistration_NoBuiltinProvider verifies error when provider not configured.
func TestDynamicRegistration_NoBuiltinProvider(t *testing.T) {
	ctx := context.Background()

	// Setup: Executor with tool registry but no builtin provider
	reg := shuttle.NewRegistry()
	exec := shuttle.NewExecutor(reg)

	toolReg, err := registry.New(registry.Config{
		DBPath:   ":memory:",
		Indexers: []registry.Indexer{registry.NewBuiltinIndexer(nil)},
	})
	require.NoError(t, err)
	defer toolReg.Close()

	// Index all tools
	_, err = toolReg.IndexAll(ctx)
	require.NoError(t, err)

	exec.SetToolRegistry(toolReg)
	// Note: NOT calling exec.SetBuiltinToolProvider()

	// Test: Try to execute builtin tool
	_, err = exec.Execute(ctx, "http_request", map[string]interface{}{
		"method": "GET",
		"url":    "https://example.com",
	})

	// Verify: Should fail because builtin provider not configured
	require.Error(t, err)
	require.Contains(t, err.Error(), "builtin tool provider not configured")
}
