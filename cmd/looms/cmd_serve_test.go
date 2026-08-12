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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/litellm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"go.uber.org/zap"
)

func TestServeLiteLLMConstructorsExpandEnvironment(t *testing.T) {
	receivedHeaders := make(chan http.Header, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders <- r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]string{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer server.Close()
	t.Setenv("SERVE_LITELLM_URL", server.URL)
	t.Setenv("SERVE_LITELLM_TOKEN", "expanded-token")

	tests := []struct {
		name   string
		create func() (interface{}, error)
	}{
		{
			name: "static server config",
			create: func() (interface{}, error) {
				return createProviderWithRateLimit(LLMConfig{
					Provider:            "litellm",
					LiteLLMEndpoint:     "${SERVE_LITELLM_URL}",
					LiteLLMExtraHeaders: map[string]string{"X-Tenant": "${SERVE_LITELLM_TOKEN}"},
					LiteLLMModel:        "test-model",
					RateLimit:           LLMRateLimitConfig{Disabled: true},
				}, zap.NewNop())
			},
		},
		{
			name: "proto agent config",
			create: func() (interface{}, error) {
				return createLLMProviderFromProtoConfig(
					&loomv1.LLMConfig{Provider: "litellm", Model: "test-model"},
					&Config{LLM: LLMConfig{
						LiteLLMEndpoint:     "${SERVE_LITELLM_URL}",
						LiteLLMExtraHeaders: map[string]string{"X-Tenant": "${SERVE_LITELLM_TOKEN}"},
					}},
					zap.NewNop(),
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := tt.create()
			require.NoError(t, err)
			client, ok := raw.(*litellm.Client)
			require.True(t, ok)
			response, err := client.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "ping"}}, nil)
			require.NoError(t, err)
			assert.Equal(t, "ok", response.Content)
			assert.Equal(t, "expanded-token", (<-receivedHeaders).Get("X-Tenant"))
		})
	}
}

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
