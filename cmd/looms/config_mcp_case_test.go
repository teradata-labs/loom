package main

import (
	"os"
	"strings"
	"testing"
)

func TestFixMCPEnvCase(t *testing.T) {
	// Create a test YAML file
	yamlContent := `
mcp:
  servers:
    test-server:
      command: /bin/test
      env:
        TD_USER: testuser
        TD_PASSWORD: testpass
        WORKSPACES_API_URL: http://example.com
        AWS_REGION: us-east-1
`
	tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(yamlContent); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}
	tmpFile.Close()

	// Create a config with lowercased env keys (simulating Viper's behavior)
	config := &Config{
		MCP: MCPConfig{
			Servers: map[string]MCPServerConfig{
				"test-server": {
					Command: "/bin/test",
					Env: map[string]string{
						"td_user":             "testuser",           // lowercased by Viper
						"td_password":         "testpass",           // lowercased by Viper
						"workspaces_api_url":  "http://example.com", // lowercased by Viper
						"aws_region":          "us-east-1",          // lowercased by Viper
						"INJECTED_FROM_SHELL": "keep_me",            // Not in YAML, should be preserved
					},
				},
			},
		},
	}

	// Call fixMCPEnvCase
	if err := fixMCPEnvCase(config, tmpFile.Name()); err != nil {
		t.Fatalf("fixMCPEnvCase failed: %v", err)
	}

	// Verify the case was fixed
	serverConfig := config.MCP.Servers["test-server"]

	// Check that uppercase keys exist
	expectedKeys := []string{"TD_USER", "TD_PASSWORD", "WORKSPACES_API_URL", "AWS_REGION"}
	for _, key := range expectedKeys {
		if _, exists := serverConfig.Env[key]; !exists {
			t.Errorf("Expected key %s with correct case, but it doesn't exist", key)
		}
	}

	// Check that lowercased keys are gone
	lowercaseKeys := []string{"td_user", "td_password", "workspaces_api_url", "aws_region"}
	for _, key := range lowercaseKeys {
		if _, exists := serverConfig.Env[key]; exists {
			t.Errorf("Lowercased key %s should have been removed", key)
		}
	}

	// Verify injected env var from shell is preserved
	if val, exists := serverConfig.Env["INJECTED_FROM_SHELL"]; !exists || val != "keep_me" {
		t.Errorf("Expected INJECTED_FROM_SHELL to be preserved, got exists=%v val=%s", exists, val)
	}

	// Verify values are correct
	if serverConfig.Env["TD_USER"] != "testuser" {
		t.Errorf("Expected TD_USER=testuser, got %s", serverConfig.Env["TD_USER"])
	}
	if serverConfig.Env["WORKSPACES_API_URL"] != "http://example.com" {
		t.Errorf("Expected WORKSPACES_API_URL=http://example.com, got %s", serverConfig.Env["WORKSPACES_API_URL"])
	}
}

func TestFixMCPEnvCase_SecurityValidation(t *testing.T) {
	config := &Config{
		MCP: MCPConfig{
			Servers: map[string]MCPServerConfig{
				"test-server": {
					Command: "/bin/test",
					Env:     map[string]string{},
				},
			},
		},
	}

	tests := []struct {
		name       string
		configFile string
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "empty path",
			configFile: "",
			wantErr:    true,
			errMsg:     "config file path is empty",
		},
		{
			name:       "relative path with parent directory",
			configFile: "../config.yaml",
			wantErr:    true,
			errMsg:     "config file path must be absolute",
		},
		{
			name:       "relative path without parent directory",
			configFile: "config.yaml",
			wantErr:    true,
			errMsg:     "config file path must be absolute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fixMCPEnvCase(config, tt.configFile)
			if (err != nil) != tt.wantErr {
				t.Errorf("fixMCPEnvCase() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("fixMCPEnvCase() error = %v, want error containing %q", err, tt.errMsg)
			}
		})
	}
}
