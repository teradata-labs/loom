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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestLoadAgentConfig(t *testing.T) {
	tests := []struct {
		name        string
		yamlContent string
		wantErr     bool
		errContains string
		validate    func(*testing.T, interface{})
	}{
		{
			name: "valid minimal config",
			yamlContent: `
agent:
  name: test_agent
  llm:
    provider: anthropic
    model: claude-3-5-sonnet-20241022
`,
			wantErr: false,
			validate: func(t *testing.T, v interface{}) {
				config := v.(*loomv1.AgentConfig)
				assert.Equal(t, "test_agent", config.Name)
				assert.Equal(t, "anthropic", config.Llm.Provider)
				assert.Equal(t, "claude-3-5-sonnet-20241022", config.Llm.Model)
				// Check defaults
				assert.Equal(t, int32(4096), config.Llm.MaxTokens)
				assert.Equal(t, float32(0.7), config.Llm.Temperature)
			},
		},
		{
			name: "full config with all fields",
			yamlContent: `
agent:
  name: full_agent
  description: A fully configured agent
  llm:
    provider: bedrock
    model: anthropic.claude-v2
    temperature: 0.5
    max_tokens: 2048
    top_p: 0.9
    top_k: 50
    stop_sequences: ["STOP", "END"]
  system_prompt: You are a helpful assistant
  tools:
    mcp:
      - server: postgres
        tools: [query, schema]
    custom:
      - name: custom_tool
        implementation: /path/to/tool.so
    builtin:
      - calculator
      - web_search
  memory:
    type: sqlite
    path: /tmp/agent.db
    max_history: 100
  behavior:
    max_iterations: 20
    timeout_seconds: 600
    allow_code_execution: true
    allowed_domains:
      - example.com
      - api.example.org
  metadata:
    author: test
    version: "1.0"
`,
			wantErr: false,
			validate: func(t *testing.T, v interface{}) {
				config := v.(*loomv1.AgentConfig)
				assert.Equal(t, "full_agent", config.Name)
				assert.Equal(t, "A fully configured agent", config.Description)
				assert.Equal(t, "bedrock", config.Llm.Provider)
				assert.Equal(t, float32(0.5), config.Llm.Temperature)
				assert.Equal(t, int32(2048), config.Llm.MaxTokens)
				assert.Equal(t, "You are a helpful assistant", config.SystemPrompt)

				// Tools
				require.Len(t, config.Tools.Mcp, 1)
				assert.Equal(t, "postgres", config.Tools.Mcp[0].Server)
				assert.Equal(t, []string{"query", "schema"}, config.Tools.Mcp[0].Tools)

				require.Len(t, config.Tools.Custom, 1)
				assert.Equal(t, "custom_tool", config.Tools.Custom[0].Name)

				assert.Equal(t, []string{"calculator", "web_search"}, config.Tools.Builtin)

				// Memory
				assert.Equal(t, "sqlite", config.Memory.Type)
				assert.Equal(t, "/tmp/agent.db", config.Memory.Path)
				assert.Equal(t, int32(100), config.Memory.MaxHistory)

				// Behavior
				assert.Equal(t, int32(20), config.Behavior.MaxIterations)
				assert.Equal(t, int32(600), config.Behavior.TimeoutSeconds)
				assert.True(t, config.Behavior.AllowCodeExecution)
				assert.Equal(t, []string{"example.com", "api.example.org"}, config.Behavior.AllowedDomains)

				// Metadata
				assert.Equal(t, "test", config.Metadata["author"])
				assert.Equal(t, "1.0", config.Metadata["version"])
			},
		},
		{
			name: "config with environment variables",
			yamlContent: `
agent:
  name: ${AGENT_NAME}
  llm:
    provider: ${LLM_PROVIDER}
    model: ${LLM_MODEL}
`,
			wantErr: false,
			validate: func(t *testing.T, v interface{}) {
				config := v.(*loomv1.AgentConfig)
				assert.Equal(t, "env_test_agent", config.Name)
				assert.Equal(t, "anthropic", config.Llm.Provider)
				assert.Equal(t, "claude-3", config.Llm.Model)
			},
		},
		{
			name: "missing required field - name",
			yamlContent: `
agent:
  llm:
    provider: anthropic
    model: claude-3
`,
			wantErr:     true,
			errContains: "agent name is required",
		},
		{
			name: "missing required field - provider",
			yamlContent: `
agent:
  name: test
  llm:
    model: claude-3
`,
			wantErr:     true,
			errContains: "LLM provider is required",
		},
		{
			name: "missing required field - model",
			yamlContent: `
agent:
  name: test
  llm:
    provider: anthropic
`,
			wantErr:     true,
			errContains: "LLM model is required",
		},
		{
			name: "invalid YAML",
			yamlContent: `
agent:
  name: test
  llm: [invalid
`,
			wantErr:     true,
			errContains: "failed to parse YAML config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variables for the env var test
			if tt.name == "config with environment variables" {
				os.Setenv("AGENT_NAME", "env_test_agent")
				os.Setenv("LLM_PROVIDER", "anthropic")
				os.Setenv("LLM_MODEL", "claude-3")
				defer func() {
					os.Unsetenv("AGENT_NAME")
					os.Unsetenv("LLM_PROVIDER")
					os.Unsetenv("LLM_MODEL")
				}()
			}

			// Create temp file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "agent.yaml")
			err := os.WriteFile(configPath, []byte(tt.yamlContent), 0644)
			require.NoError(t, err)

			// Load config
			config, err := LoadAgentConfig(configPath)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, config)

			if tt.validate != nil {
				tt.validate(t, config)
			}
		})
	}
}

func TestValidateAgentConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      *loomv1.AgentConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "valid config",
			config: &loomv1.AgentConfig{
				Name: "test",
				Llm: &loomv1.LLMConfig{
					Provider:    "anthropic",
					Model:       "claude-3",
					Temperature: 0.7,
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			config: &loomv1.AgentConfig{
				Llm: &loomv1.LLMConfig{
					Provider: "anthropic",
					Model:    "claude-3",
				},
			},
			wantErr:     true,
			errContains: "agent name is required",
		},
		{
			name: "nil LLM config",
			config: &loomv1.AgentConfig{
				Name: "test",
			},
			// LLM config is now optional - agent will use the provider passed to LoadAgentsFromConfig
			wantErr:     false,
			errContains: "",
		},
		{
			name: "invalid provider",
			config: &loomv1.AgentConfig{
				Name: "test",
				Llm: &loomv1.LLMConfig{
					Provider: "invalid",
					Model:    "model",
				},
			},
			wantErr:     true,
			errContains: "unsupported LLM provider",
		},
		{
			name: "valid providers",
			config: &loomv1.AgentConfig{
				Name: "test",
				Llm: &loomv1.LLMConfig{
					Provider: "bedrock",
					Model:    "model",
				},
			},
			wantErr: false,
		},
		{
			name: "temperature out of range - too low",
			config: &loomv1.AgentConfig{
				Name: "test",
				Llm: &loomv1.LLMConfig{
					Provider:    "anthropic",
					Model:       "model",
					Temperature: -0.1,
				},
			},
			wantErr:     true,
			errContains: "temperature must be between 0 and 1",
		},
		{
			name: "temperature out of range - too high",
			config: &loomv1.AgentConfig{
				Name: "test",
				Llm: &loomv1.LLMConfig{
					Provider:    "anthropic",
					Model:       "model",
					Temperature: 1.1,
				},
			},
			wantErr:     true,
			errContains: "temperature must be between 0 and 1",
		},
		{
			name: "invalid memory type",
			config: &loomv1.AgentConfig{
				Name: "test",
				Llm: &loomv1.LLMConfig{
					Provider: "anthropic",
					Model:    "model",
				},
				Memory: &loomv1.MemoryConfig{
					Type: "invalid",
				},
			},
			wantErr:     true,
			errContains: "unsupported memory type",
		},
		{
			name: "valid memory types",
			config: &loomv1.AgentConfig{
				Name: "test",
				Llm: &loomv1.LLMConfig{
					Provider: "anthropic",
					Model:    "model",
				},
				Memory: &loomv1.MemoryConfig{
					Type: "postgres",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentConfig(tt.config)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestSaveAgentConfig(t *testing.T) {
	config := &loomv1.AgentConfig{
		Name:        "test_agent",
		Description: "Test agent",
		Llm: &loomv1.LLMConfig{
			Provider:    "anthropic",
			Model:       "claude-3",
			Temperature: 0.7,
			MaxTokens:   4096,
		},
		SystemPrompt: "You are a test assistant",
		Tools: &loomv1.ToolsConfig{
			Mcp: []*loomv1.MCPToolConfig{
				{
					Server: "postgres",
					Tools:  []string{"query"},
				},
			},
			Builtin: []string{"calculator"},
		},
		Memory: &loomv1.MemoryConfig{
			Type:       "sqlite",
			Path:       "/tmp/test.db",
			MaxHistory: 50,
		},
		Behavior: &loomv1.BehaviorConfig{
			MaxIterations:  10,
			TimeoutSeconds: 300,
		},
		Metadata: map[string]string{
			"author": "test",
		},
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "saved_agent.yaml")

	// Save config
	err := SaveAgentConfig(config, configPath)
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(configPath)
	require.NoError(t, err)

	// Load it back and verify
	loadedConfig, err := LoadAgentConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, config.Name, loadedConfig.Name)
	assert.Equal(t, config.Description, loadedConfig.Description)
	assert.Equal(t, config.Llm.Provider, loadedConfig.Llm.Provider)
	assert.Equal(t, config.Llm.Model, loadedConfig.Llm.Model)
	assert.Equal(t, config.SystemPrompt, loadedConfig.SystemPrompt)
	assert.Equal(t, config.Memory.Type, loadedConfig.Memory.Type)
	assert.Equal(t, config.Metadata["author"], loadedConfig.Metadata["author"])
}

// TestLoadAgentConfig_FileNotFound tests error handling for missing files
func TestLoadAgentConfig_FileNotFound(t *testing.T) {
	_, err := LoadAgentConfig("/nonexistent/path/agent.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config file")
}

// TestExpandEnvVars tests environment variable expansion
func TestExpandEnvVars(t *testing.T) {
	os.Setenv("TEST_VAR", "value")
	defer os.Unsetenv("TEST_VAR")

	// Test basic expansion
	result := expandEnvVars("prefix-${TEST_VAR}-suffix")
	assert.Equal(t, "prefix-value-suffix", result)

	// Test missing variable (expands to empty string)
	result = expandEnvVars("${NONEXISTENT}")
	assert.Equal(t, "", result)
}
