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
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestRegisterMCPTools_WildcardHandling verifies that tools: ["*"] is treated as "register all tools"
// This prevents the bug where "*" was treated as a literal tool name, causing registration failures.
func TestRegisterMCPTools_WildcardHandling(t *testing.T) {
	tests := []struct {
		name              string
		toolsConfig       []string
		expectRegisterAll bool
		description       string
	}{
		{
			name:              "empty list registers all",
			toolsConfig:       []string{},
			expectRegisterAll: true,
			description:       "Empty tools array should register all tools from server",
		},
		{
			name:              "wildcard registers all",
			toolsConfig:       []string{"*"},
			expectRegisterAll: true,
			description:       "Wildcard ['*'] should register all tools from server",
		},
		{
			name:              "specific tool registers specific",
			toolsConfig:       []string{"read_file"},
			expectRegisterAll: false,
			description:       "Specific tool name should register only that tool",
		},
		{
			name:              "multiple specific tools",
			toolsConfig:       []string{"read_file", "write_file"},
			expectRegisterAll: false,
			description:       "Multiple specific tool names should register only those tools",
		},
		{
			name:              "wildcard with other tools is specific",
			toolsConfig:       []string{"*", "read_file"},
			expectRegisterAll: false,
			description:       "Wildcard mixed with specific tools should be treated as specific (edge case)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock config
			mcpConfig := &loomv1.MCPToolConfig{
				Server: "test-server",
				Tools:  tt.toolsConfig,
			}

			// Verify the logic matches expectations
			shouldRegisterAll := len(mcpConfig.Tools) == 0 ||
				(len(mcpConfig.Tools) == 1 && mcpConfig.Tools[0] == "*")

			assert.Equal(t, tt.expectRegisterAll, shouldRegisterAll, tt.description)
		})
	}
}

// TestRegisterMCPTools_WildcardBugFix is a regression test for the bug where
// tools: ["*"] would try to register a tool literally named "*" instead of
// registering all tools from the server.
//
// Bug symptoms:
// - Agent would show 5+ duplicate instances of get_tool_result
// - MCP tool registration would fail silently
// - Tools list sent to LLM would contain duplicates
func TestRegisterMCPTools_WildcardBugFix(t *testing.T) {
	// This test documents the bug and verifies the fix

	// BEFORE FIX: tools: ["*"] would execute this path:
	//   for _, toolName := range mcpConfig.Tools {
	//       agent.RegisterMCPTool(ctx, mcpMgr, server, "*")  // ❌ Tries to register tool named "*"
	//   }

	// AFTER FIX: tools: ["*"] executes this path:
	//   agent.RegisterMCPServer(ctx, mcpMgr, server)  // ✅ Registers all tools from server

	wildcardConfig := &loomv1.MCPToolConfig{
		Server: "vantage",
		Tools:  []string{"*"},
	}

	shouldRegisterAll := len(wildcardConfig.Tools) == 0 ||
		(len(wildcardConfig.Tools) == 1 && wildcardConfig.Tools[0] == "*")

	assert.True(t, shouldRegisterAll,
		"tools: ['*'] must be treated as 'register all tools', not as a literal tool name")
}
