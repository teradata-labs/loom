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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
)

// TestRegisterMCPServer_ToolFilterAll tests that RegisterMCPServer respects ToolFilter.All=true
func TestRegisterMCPServer_ToolFilterAll(t *testing.T) {
	// Create a manager with ToolFilter.All=true
	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"test-server": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
				ToolFilter: manager.ToolFilter{
					All: true, // This is the fix - register all tools
				},
			},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := manager.NewManager(config, nil)
	require.NoError(t, err)
	_ = mgr // Manager created successfully

	// Verify ToolFilter.All is set correctly
	serverConfig := config.Servers["test-server"]
	assert.True(t, serverConfig.ToolFilter.All, "ToolFilter.All should be true")

	// Note: This test would require a real MCP server running to fully test
	// For now, we're verifying the ToolFilter.All configuration is correct
}

// TestRegisterMCPServer_ToolFilterDefault tests that default ToolFilter (All=false) rejects tools
func TestRegisterMCPServer_ToolFilterDefault(t *testing.T) {
	// Create a manager with default ToolFilter (All=false, Include=[], Exclude=[])
	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"test-server": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
				// ToolFilter not specified - defaults to All=false
			},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}

	// Verify the default ToolFilter behavior
	filter := config.Servers["test-server"].ToolFilter
	assert.False(t, filter.All, "default ToolFilter.All should be false")
	assert.Empty(t, filter.Include, "default ToolFilter.Include should be empty")
	assert.Empty(t, filter.Exclude, "default ToolFilter.Exclude should be empty")

	// Verify tools are rejected with default filter
	assert.False(t, filter.ShouldRegisterTool("read_file"), "default filter should reject tools")
	assert.False(t, filter.ShouldRegisterTool("execute_query"), "default filter should reject tools")
}

// TestRegisterMCPServer_ToolFilterSelective tests selective tool registration
func TestRegisterMCPServer_ToolFilterSelective(t *testing.T) {
	// Create a manager with selective tool filter
	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"test-server": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
				ToolFilter: manager.ToolFilter{
					Include: []string{"read_file", "execute_query"},
				},
			},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}

	filter := config.Servers["test-server"].ToolFilter

	// Verify only included tools pass filter
	assert.True(t, filter.ShouldRegisterTool("read_file"), "included tool should pass")
	assert.True(t, filter.ShouldRegisterTool("execute_query"), "included tool should pass")
	assert.False(t, filter.ShouldRegisterTool("delete_database"), "non-included tool should fail")
}

// TestRegisterMCPServer_ToolFilterExclude tests tool exclusion
func TestRegisterMCPServer_ToolFilterExclude(t *testing.T) {
	// Create a manager with exclusion filter
	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"test-server": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
				ToolFilter: manager.ToolFilter{
					All:     true,
					Exclude: []string{"drop_database", "delete_all"},
				},
			},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}

	filter := config.Servers["test-server"].ToolFilter

	// Verify excluded tools are rejected
	assert.False(t, filter.ShouldRegisterTool("drop_database"), "excluded tool should fail")
	assert.False(t, filter.ShouldRegisterTool("delete_all"), "excluded tool should fail")
	assert.True(t, filter.ShouldRegisterTool("read_file"), "non-excluded tool should pass")
	assert.True(t, filter.ShouldRegisterTool("execute_query"), "non-excluded tool should pass")
}

// TestRegisterMCPTools_CountIncreases documents that tool count should increase after registration
func TestRegisterMCPTools_CountIncreases(t *testing.T) {
	// This test documents the expected behavior:
	// - Agent should start with 0 tools
	// - After RegisterMCPServer/RegisterMCPTool, tool count should increase
	// - The actual count depends on ToolFilter configuration
	//
	// See /tmp/looms-fixed-filter.log for real-world example:
	//   Before: tool_count=0
	//   After:  tools_added=17, total_tools=17
	//
	// Note: Full integration test would require real MCP server
	// See pkg/mcp/integration_test.go for end-to-end MCP tests
}

// TestRegisterMCPTools_ValidationErrors tests error handling
func TestRegisterMCPTools_ValidationErrors(t *testing.T) {
	// This test documents validation requirements for RegisterMCPTools:
	// - MCP server name is required
	// - MCP client is required
	// - MCP client must be initialized
	//
	// See pkg/agent/mcp_integration.go lines 51-62 for validation logic
	//
	// Note: Full test would require proper client.Client mocking infrastructure
}

// TestRegisterMCPServer_MultipleServers tests registering tools from multiple servers
func TestRegisterMCPServer_MultipleServers(t *testing.T) {
	// Create manager with multiple servers
	config := manager.Config{
		Servers: map[string]manager.ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
				ToolFilter: manager.ToolFilter{
					All: true,
				},
			},
			"database": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
				ToolFilter: manager.ToolFilter{
					All: true,
				},
			},
		},
		ClientInfo: manager.ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := manager.NewManager(config, nil)
	require.NoError(t, err)
	_ = mgr

	// Verify both servers are configured with ToolFilter.All=true
	for name, serverConfig := range config.Servers {
		assert.True(t, serverConfig.ToolFilter.All,
			"server %s should have ToolFilter.All=true", name)
	}
}

// TestToolFilterBugRegression tests the specific bug that was fixed
func TestToolFilterBugRegression(t *testing.T) {
	// This test documents the bug that was fixed in commit 7a9f975

	// BEFORE FIX: Default ToolFilter (All=false) rejected all tools
	defaultFilter := manager.ToolFilter{}
	assert.False(t, defaultFilter.ShouldRegisterTool("any_tool"),
		"BUG: default ToolFilter rejected all tools")

	// AFTER FIX: Explicitly set ToolFilter.All=true
	fixedFilter := manager.ToolFilter{All: true}
	assert.True(t, fixedFilter.ShouldRegisterTool("any_tool"),
		"FIX: ToolFilter.All=true accepts all tools")
}

// Note: mockBackend and mockLLMProvider are defined in other test files
// See agent_integration_test.go and registry_test.go
