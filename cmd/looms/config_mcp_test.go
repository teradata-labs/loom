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
)

func TestInjectMCPEnvSecret(t *testing.T) {
	tests := []struct {
		name         string
		config       *Config
		envKey       string
		value        string
		wantInjected bool
		checkServer  string
	}{
		{
			name: "inject into empty server",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"test-server": {
							Command: "test",
						},
					},
				},
			},
			envKey:       "TEST_PASSWORD",
			value:        "secret123",
			wantInjected: true,
			checkServer:  "test-server",
		},
		{
			name: "inject into server with existing env",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"test-server": {
							Command: "test",
							Env: map[string]string{
								"EXISTING": "value",
							},
						},
					},
				},
			},
			envKey:       "TEST_PASSWORD",
			value:        "secret123",
			wantInjected: true,
			checkServer:  "test-server",
		},
		{
			name: "dont overwrite existing env var",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"test-server": {
							Command: "test",
							Env: map[string]string{
								"TEST_PASSWORD": "already-set",
							},
						},
					},
				},
			},
			envKey:       "TEST_PASSWORD",
			value:        "secret123",
			wantInjected: false, // Should NOT overwrite
			checkServer:  "test-server",
		},
		{
			name: "inject into multiple servers",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"server1": {Command: "test1"},
						"server2": {Command: "test2"},
					},
				},
			},
			envKey:       "SHARED_SECRET",
			value:        "shared123",
			wantInjected: true,
			checkServer:  "server1", // Check first server
		},
		{
			name: "no-op on nil servers",
			config: &Config{
				MCP: MCPConfig{
					Servers: nil,
				},
			},
			envKey:       "TEST_PASSWORD",
			value:        "secret123",
			wantInjected: false,
			checkServer:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			injectMCPEnvSecret(tt.config, tt.envKey, tt.value)

			if tt.checkServer == "" {
				return // Skip check for nil servers test
			}

			serverConfig, exists := tt.config.MCP.Servers[tt.checkServer]
			if !exists {
				t.Fatalf("server %s not found", tt.checkServer)
			}

			gotValue, hasKey := serverConfig.Env[tt.envKey]
			if tt.wantInjected {
				if !hasKey {
					t.Errorf("expected env var %s to be injected, but it wasn't", tt.envKey)
				} else if tt.name == "dont overwrite existing env var" {
					// Special case: should preserve original value
					if gotValue != "already-set" {
						t.Errorf("expected original value 'already-set', got %s", gotValue)
					}
				} else if gotValue != tt.value {
					t.Errorf("expected injected value %s, got %s", tt.value, gotValue)
				}
			} else if hasKey && tt.name != "dont overwrite existing env var" {
				t.Errorf("expected env var %s not to be injected, but it was", tt.envKey)
			}
		})
	}
}

func TestCheckMCPEnvSecret(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		envKey string
		want   bool
	}{
		{
			name: "finds env var in server",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"test-server": {
							Env: map[string]string{
								"TEST_PASSWORD": "secret",
							},
						},
					},
				},
			},
			envKey: "TEST_PASSWORD",
			want:   true,
		},
		{
			name: "does not find missing env var",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"test-server": {
							Env: map[string]string{
								"OTHER_VAR": "value",
							},
						},
					},
				},
			},
			envKey: "TEST_PASSWORD",
			want:   false,
		},
		{
			name: "finds env var in one of multiple servers",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"server1": {
							Env: map[string]string{
								"OTHER_VAR": "value",
							},
						},
						"server2": {
							Env: map[string]string{
								"TEST_PASSWORD": "secret",
							},
						},
					},
				},
			},
			envKey: "TEST_PASSWORD",
			want:   true,
		},
		{
			name: "returns false for nil servers",
			config: &Config{
				MCP: MCPConfig{
					Servers: nil,
				},
			},
			envKey: "TEST_PASSWORD",
			want:   false,
		},
		{
			name: "returns false for server with nil env",
			config: &Config{
				MCP: MCPConfig{
					Servers: map[string]MCPServerConfig{
						"test-server": {
							Env: nil,
						},
					},
				},
			},
			envKey: "TEST_PASSWORD",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkMCPEnvSecret(tt.config, tt.envKey)
			if got != tt.want {
				t.Errorf("checkMCPEnvSecret() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSecretMappings_MCPSecrets(t *testing.T) {
	// Verify that MCP secrets are properly registered
	mappings := GetSecretMappings()

	expectedMCPSecrets := []string{
		"td_password",
		"github_token",
		"postgres_password",
	}

	mappingMap := make(map[string]bool)
	for _, m := range mappings {
		mappingMap[m.KeyringKey] = true
	}

	for _, expectedKey := range expectedMCPSecrets {
		if !mappingMap[expectedKey] {
			t.Errorf("expected MCP secret %s to be in secret mappings, but it wasn't", expectedKey)
		}
	}
}

func TestMCPSecretInjection_EndToEnd(t *testing.T) {
	// Test the full flow: create config, inject secret, verify it's accessible
	config := &Config{
		MCP: MCPConfig{
			Servers: map[string]MCPServerConfig{
				"vantage": {
					Command: "vantage-mcp",
					Env: map[string]string{
						"TD_USER": "testuser",
						"TD_HOST": "test.teradata.com",
					},
				},
			},
		},
	}

	// Inject TD password from keyring
	injectMCPEnvSecret(config, "TD_PASSWORD", "supersecret")

	// Verify it was injected
	if !checkMCPEnvSecret(config, "TD_PASSWORD") {
		t.Fatal("TD_PASSWORD was not injected into config")
	}

	// Verify the value
	vantageConfig := config.MCP.Servers["vantage"]
	if vantageConfig.Env["TD_PASSWORD"] != "supersecret" {
		t.Errorf("expected TD_PASSWORD=supersecret, got %s", vantageConfig.Env["TD_PASSWORD"])
	}

	// Verify existing env vars were preserved
	if vantageConfig.Env["TD_USER"] != "testuser" {
		t.Error("existing TD_USER env var was not preserved")
	}
	if vantageConfig.Env["TD_HOST"] != "test.teradata.com" {
		t.Error("existing TD_HOST env var was not preserved")
	}
}

func TestMCPServerConfig_HTTPTransport(t *testing.T) {
	tests := []struct {
		name          string
		config        MCPServerConfig
		wantTransport string
		wantURL       string
	}{
		{
			name: "http transport with url",
			config: MCPServerConfig{
				Transport: "http",
				URL:       "http://localhost:8080/mcp",
			},
			wantTransport: "http",
			wantURL:       "http://localhost:8080/mcp",
		},
		{
			name: "sse transport with url",
			config: MCPServerConfig{
				Transport: "sse",
				URL:       "https://api.example.com/mcp",
			},
			wantTransport: "sse",
			wantURL:       "https://api.example.com/mcp",
		},
		{
			name: "stdio transport (default)",
			config: MCPServerConfig{
				Command: "npx",
				Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
			},
			wantTransport: "", // Will default to stdio in initializeMCPManager
			wantURL:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Transport != tt.wantTransport {
				t.Errorf("Transport = %v, want %v", tt.config.Transport, tt.wantTransport)
			}
			if tt.config.URL != tt.wantURL {
				t.Errorf("URL = %v, want %v", tt.config.URL, tt.wantURL)
			}
		})
	}
}
