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
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestInitializeMCPManager(t *testing.T) {
	// Skip these tests - they require real MCP server binaries and are integration tests
	t.Skip("Integration test - requires real MCP server binaries. " +
		"These tests try to start actual MCP processes which hang if binaries don't exist. " +
		"Run manually with real MCP servers for integration testing.")

	tests := []struct {
		name          string
		config        *Config
		wantErr       bool
		wantServerCnt int
	}{
		{
			name: "no servers configured",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{},
				},
			},
			wantErr:       false,
			wantServerCnt: 0,
		},
		{
			name: "nil servers map",
			config: &Config{
				MCP: MCPConfig{
					Servers: nil,
				},
			},
			wantErr:       false,
			wantServerCnt: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewNop()
			manager, err := initializeMCPManager(tt.config, logger)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, manager)
			// Verify correct number of servers via ServerNames()
			serverNames := manager.GetManager().ServerNames()
			assert.Equal(t, tt.wantServerCnt, len(serverNames))

			// Verify each server is tracked
			for serverName := range tt.config.MCP.Servers {
				found := false
				for _, name := range serverNames {
					if name == serverName {
						found = true
						break
					}
				}
				assert.True(t, found, "Server %s should be tracked", serverName)
			}
		})
	}
}

func TestInitializeMCPManager_LogsServerInfo(t *testing.T) {
	// Skip - integration test requiring real MCP server binaries
	t.Skip("Integration test - requires real MCP server binaries")
}

func TestInitializeMCPManager_HandlesNilLogger(t *testing.T) {
	// Skip - integration test requiring real MCP server binaries
	t.Skip("Integration test - requires real MCP server binaries")
}
