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

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_WithAgents(t *testing.T) {
	// Load the example test config
	config, err := LoadConfig("../../examples/looms-test.yaml")
	require.NoError(t, err)
	require.NotNil(t, config)

	// Verify agent configuration loaded
	assert.NotEmpty(t, config.Agents.Agents)
	assert.Len(t, config.Agents.Agents, 1)

	// Verify sqlite-agent configuration
	sqliteAgent, ok := config.Agents.Agents["sqlite-agent"]
	require.True(t, ok, "sqlite-agent should exist in config")
	assert.Equal(t, "SQLite Test Agent", sqliteAgent.Name)
	assert.Equal(t, "Test agent for SQLite queries", sqliteAgent.Description)
	assert.Equal(t, "./examples/reference/backends/sqlite.yaml", sqliteAgent.BackendPath)
	assert.Equal(t, "You are a helpful SQLite assistant.", sqliteAgent.SystemPrompt)
	assert.Equal(t, 10, sqliteAgent.MaxTurns)
	assert.Equal(t, 20, sqliteAgent.MaxToolExecutions)
	assert.False(t, sqliteAgent.EnableTracing)
}

func TestGenerateExampleConfig(t *testing.T) {
	// Verify example config contains agent configuration
	exampleConfig := GenerateExampleConfig()
	assert.Contains(t, exampleConfig, "agents:")
	assert.Contains(t, exampleConfig, "sql-agent:")
	assert.Contains(t, exampleConfig, "backend_path:")
	assert.Contains(t, exampleConfig, "mcp:")
	assert.Contains(t, exampleConfig, "python-tools:")
}

func TestInferType(t *testing.T) {
	tests := []struct {
		name          string
		key           string
		value         string
		existingValue interface{}
		expected      interface{}
	}{
		{
			name:          "infer int from existing int value",
			key:           "server.port",
			value:         "8080",
			existingValue: 9090,
			expected:      8080,
		},
		{
			name:          "infer bool from existing bool value",
			key:           "server.enable_reflection",
			value:         "false",
			existingValue: true,
			expected:      false,
		},
		{
			name:          "infer float from existing float value",
			key:           "llm.temperature",
			value:         "0.5",
			existingValue: 1.0,
			expected:      0.5,
		},
		{
			name:          "infer int from key name containing port",
			key:           "custom.port",
			value:         "3000",
			existingValue: nil,
			expected:      3000,
		},
		{
			name:          "infer int from key name containing timeout",
			key:           "llm.timeout_seconds",
			value:         "120",
			existingValue: nil,
			expected:      120,
		},
		{
			name:          "infer int from key name containing max_tokens",
			key:           "llm.max_tokens",
			value:         "2048",
			existingValue: nil,
			expected:      2048,
		},
		{
			name:          "infer bool from key name containing enabled",
			key:           "observability.enabled",
			value:         "true",
			existingValue: nil,
			expected:      true,
		},
		{
			name:          "infer bool from key name containing enable_",
			key:           "server.enable_reflection",
			value:         "true",
			existingValue: nil,
			expected:      true,
		},
		{
			name:          "infer float from key name containing temperature",
			key:           "llm.temperature",
			value:         "0.7",
			existingValue: nil,
			expected:      0.7,
		},
		{
			name:          "default to string when no inference possible",
			key:           "llm.provider",
			value:         "bedrock",
			existingValue: nil,
			expected:      "bedrock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary viper instance
			v := newTestViper(t, tt.key, tt.existingValue)

			result := inferType(tt.key, tt.value, v)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "short secret",
			input:    "short",
			expected: "***",
		},
		{
			name:     "normal secret",
			input:    "sk-ant-1234567890abcdef",
			expected: "sk-a...cdef",
		},
		{
			name:     "long secret",
			input:    "very-long-secret-key-with-many-characters",
			expected: "very...ters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := maskSecret(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCapitalizeWords(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single word",
			input:    "file",
			expected: "File",
		},
		{
			name:     "hyphenated words",
			input:    "mcp-python",
			expected: "Mcp Python",
		},
		{
			name:     "underscored words",
			input:    "my_backend_name",
			expected: "My Backend Name",
		},
		{
			name:     "mixed separators",
			input:    "mcp-python_tools",
			expected: "Mcp Python Tools",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := capitalizeWords(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "contains substring",
			s:        "server.port",
			substr:   "port",
			expected: true,
		},
		{
			name:     "does not contain substring",
			s:        "server.host",
			substr:   "port",
			expected: false,
		},
		{
			name:     "case insensitive match",
			s:        "Server.Port",
			substr:   "port",
			expected: true,
		},
		{
			name:     "empty substring",
			s:        "anything",
			substr:   "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSplitAndTrim(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		delim    string
		expected []string
	}{
		{
			name:     "comma separated with spaces",
			input:    "1, 2, 3",
			delim:    ",",
			expected: []string{"1", "2", "3"},
		},
		{
			name:     "comma separated without spaces",
			input:    "1,2,3",
			delim:    ",",
			expected: []string{"1", "2", "3"},
		},
		{
			name:     "single value",
			input:    "1",
			delim:    ",",
			expected: []string{"1"},
		},
		{
			name:     "empty string",
			input:    "",
			delim:    ",",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitAndTrim(tt.input, tt.delim)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Helper function to create a test viper instance with optional existing value
func newTestViper(t *testing.T, key string, existingValue interface{}) *viper.Viper {
	v := viper.New()
	v.SetConfigType("yaml")

	if existingValue != nil {
		v.Set(key, existingValue)
	}

	return v
}
