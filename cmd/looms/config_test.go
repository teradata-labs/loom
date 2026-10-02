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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_WithAgents(t *testing.T) {
	// Load the test config
	config, err := LoadConfig("../../tests/config/looms-test.yaml")
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
	assert.Equal(t, "./examples/backends/sqlite.yaml", sqliteAgent.BackendPath)
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

func TestBuildProtoStorageConfig_DefaultSQLite(t *testing.T) {
	cfg := &Config{
		Storage: StorageBackendConfig{
			Backend: "sqlite",
			SQLite: SQLiteConfig{
				Path: "/tmp/test.db",
			},
			Migration: MigrationStorageConfig{
				AutoMigrate: true,
			},
		},
	}

	proto := cfg.BuildProtoStorageConfig()
	require.NotNil(t, proto)
	assert.Equal(t, int32(1), int32(proto.Backend)) // SQLITE = 1
	require.NotNil(t, proto.Sqlite)
	assert.Equal(t, "/tmp/test.db", proto.Sqlite.Path)
	assert.True(t, proto.Migration.AutoMigrate)
}

func TestBuildProtoStorageConfig_Postgres(t *testing.T) {
	cfg := &Config{
		Storage: StorageBackendConfig{
			Backend: "postgres",
			Postgres: PostgresConfig{
				Host:     "db.example.com",
				Port:     5433,
				Database: "loomdb",
				User:     "loom",
				Password: "secret",
				SSLMode:  "verify-full",
				Schema:   "myschema",
				Pool: PostgresPoolConfig{
					MaxConnections: 50,
					MinConnections: 10,
				},
			},
		},
	}

	proto := cfg.BuildProtoStorageConfig()
	require.NotNil(t, proto)
	assert.Equal(t, int32(2), int32(proto.Backend)) // POSTGRES = 2
	require.NotNil(t, proto.Postgres)
	assert.Equal(t, "db.example.com", proto.Postgres.Host)
	assert.Equal(t, int32(5433), proto.Postgres.Port)
	assert.Equal(t, "loomdb", proto.Postgres.Database)
	assert.Equal(t, "loom", proto.Postgres.User)
	assert.Equal(t, "secret", proto.Postgres.Password)
	assert.Equal(t, "verify-full", proto.Postgres.SslMode)
	assert.Equal(t, "myschema", proto.Postgres.Schema)
	require.NotNil(t, proto.Postgres.Pool)
	assert.Equal(t, int32(50), proto.Postgres.Pool.MaxConnections)
	assert.Equal(t, int32(10), proto.Postgres.Pool.MinConnections)
}

func TestBuildProtoStorageConfig_PostgresDSN(t *testing.T) {
	cfg := &Config{
		Storage: StorageBackendConfig{
			Backend: "postgres",
			Postgres: PostgresConfig{
				DSN: "postgres://user:pass@host:5432/db?sslmode=disable",
			},
		},
	}

	proto := cfg.BuildProtoStorageConfig()
	require.NotNil(t, proto)
	require.NotNil(t, proto.Postgres)
	assert.Equal(t, "postgres://user:pass@host:5432/db?sslmode=disable", proto.Postgres.Dsn)
}

func TestBuildProtoStorageConfig_BackwardCompat(t *testing.T) {
	// When storage.backend is empty but database.path is set, should use database.path
	cfg := &Config{
		Database: DatabaseConfig{
			Path:   "/old/path/loom.db",
			Driver: "sqlite",
		},
		Storage: StorageBackendConfig{
			Backend: "", // empty = default to sqlite
		},
	}

	proto := cfg.BuildProtoStorageConfig()
	require.NotNil(t, proto)
	require.NotNil(t, proto.Sqlite)
	assert.Equal(t, "/old/path/loom.db", proto.Sqlite.Path)
}

func TestResolveStoragePath(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected string
	}{
		{
			name: "storage.sqlite.path takes priority",
			config: Config{
				Storage:  StorageBackendConfig{SQLite: SQLiteConfig{Path: "/new/path.db"}},
				Database: DatabaseConfig{Path: "/old/path.db"},
			},
			expected: "/new/path.db",
		},
		{
			name: "falls back to database.path",
			config: Config{
				Database: DatabaseConfig{Path: "/old/path.db"},
			},
			expected: "/old/path.db",
		},
		{
			name:     "empty when nothing set",
			config:   Config{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.resolveStoragePath())
		})
	}
}

func TestValidate_ObservabilityMode(t *testing.T) {
	// Base config with valid LLM + storage so only observability is under test.
	validBase := func() *Config {
		return &Config{
			Server:  ServerConfig{Port: 60051},
			LLM:     LLMConfig{Provider: "ollama", OllamaEndpoint: "http://localhost:11434", OllamaModel: "test"},
			Storage: StorageBackendConfig{Backend: "sqlite", SQLite: SQLiteConfig{Path: "/tmp/test.db"}},
		}
	}

	// Regression: an enabled config with no explicit mode and no Hawk endpoint
	// must validate (it resolves to embedded, matching the tracer builder in
	// cmd_serve.go). Previously Validate() defaulted empty mode to "service" and
	// then rejected it for a missing hawk_endpoint.
	t.Run("enabled, empty mode, no endpoint -> valid (embedded)", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("enabled, empty mode, hawk endpoint set -> valid (service)", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true, HawkEndpoint: "http://localhost:9090"}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("enabled, mode=service, no endpoint -> error", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true, Mode: "service"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hawk_endpoint is required")
	})

	t.Run("enabled, mode=embedded, sqlite storage, no path -> error", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true, Mode: "embedded", StorageType: "sqlite"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sqlite_path is required")
	})

	t.Run("enabled, mode=otel, endpoint set -> valid", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true, Mode: "otel", OTLPEndpoint: "http://collector:4318/v1/traces"}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("enabled, mode=otel, no endpoint -> error", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true, Mode: "otel"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "otlp_endpoint is required")
	})

	t.Run("enabled, empty mode, otlp endpoint set -> valid (auto otel)", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true, OTLPEndpoint: "http://collector:4318/v1/traces"}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("enabled, invalid mode -> error", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: true, Mode: "bogus"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be 'embedded', 'service', 'otel', or 'none'")
	})

	t.Run("disabled -> valid regardless of mode", func(t *testing.T) {
		cfg := validBase()
		cfg.Observability = ObservabilityConfig{Enabled: false, Mode: "service"}
		assert.NoError(t, cfg.Validate())
	})
}

// TestValidate_DoorKnobs (review finding 5, PR #353): negative door knobs
// are startup errors, not a silent disable (max_active_conversations) or an
// unbounded queue (max_door_queue).
func TestValidate_DoorKnobs(t *testing.T) {
	validBase := func() *Config {
		return &Config{
			Server:  ServerConfig{Port: 60051},
			LLM:     LLMConfig{Provider: "ollama", OllamaEndpoint: "http://localhost:11434", OllamaModel: "test"},
			Storage: StorageBackendConfig{Backend: "sqlite", SQLite: SQLiteConfig{Path: "/tmp/test.db"}},
		}
	}

	t.Run("zero knobs -> valid (gate off, unbounded queue)", func(t *testing.T) {
		cfg := validBase()
		assert.NoError(t, cfg.Validate())
	})

	t.Run("positive knobs -> valid", func(t *testing.T) {
		cfg := validBase()
		cfg.LLM.MaxActiveConversations = 8
		cfg.LLM.MaxDoorQueue = 64
		assert.NoError(t, cfg.Validate())
	})

	t.Run("negative max_active_conversations -> error", func(t *testing.T) {
		cfg := validBase()
		cfg.LLM.MaxActiveConversations = -1
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "llm.max_active_conversations")
	})

	t.Run("negative max_door_queue -> error", func(t *testing.T) {
		cfg := validBase()
		cfg.LLM.MaxDoorQueue = -5
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "llm.max_door_queue")
	})
}

func TestValidate_StorageBackend(t *testing.T) {
	// Helper to create a config with valid LLM settings
	validBase := func() *Config {
		return &Config{
			Server:  ServerConfig{Port: 60051},
			LLM:     LLMConfig{Provider: "ollama", OllamaEndpoint: "http://localhost:11434", OllamaModel: "test"},
			Storage: StorageBackendConfig{Backend: "sqlite", SQLite: SQLiteConfig{Path: "/tmp/test.db"}},
		}
	}

	t.Run("sqlite valid", func(t *testing.T) {
		cfg := validBase()
		assert.NoError(t, cfg.Validate())
	})

	t.Run("postgres missing host and dsn", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "postgres"
		cfg.Storage.Postgres = PostgresConfig{}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dsn or host")
	})

	t.Run("postgres missing database", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "postgres"
		cfg.Storage.Postgres = PostgresConfig{Host: "localhost"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database name")
	})

	t.Run("postgres valid with host+database+user", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "postgres"
		cfg.Storage.Postgres = PostgresConfig{Host: "localhost", Database: "testdb", User: "testuser"}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("postgres valid with DSN", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "postgres"
		cfg.Storage.Postgres = PostgresConfig{DSN: "postgres://user:pass@localhost/db"}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("unsupported backend", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "mysql"
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported storage backend")
	})

	t.Run("postgres requires user", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "postgres"
		cfg.Storage.Postgres = PostgresConfig{Host: "localhost", Database: "testdb"}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires user")
	})

	t.Run("postgres ssl_mode valid", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "postgres"
		cfg.Storage.Postgres = PostgresConfig{
			Host:     "localhost",
			Database: "testdb",
			User:     "loom",
			SSLMode:  "require",
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("postgres ssl_mode invalid", func(t *testing.T) {
		cfg := validBase()
		cfg.Storage.Backend = "postgres"
		cfg.Storage.Postgres = PostgresConfig{
			Host:     "localhost",
			Database: "testdb",
			User:     "loom",
			SSLMode:  "invalid",
		}
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid ssl_mode")
	})
}

func TestInsecureAdmin_DefaultFalse(t *testing.T) {
	// Reset viper to avoid contamination from other tests
	viper.Reset()
	setDefaults()

	// Verify the default is false
	assert.False(t, viper.GetBool("server.insecure_admin"),
		"server.insecure_admin should default to false for secure-by-default behavior")
}

// TestMinimalTools_DefaultFalse locks in the opt-in contract for the
// --minimal-tools flag / tools.minimal viper key. Anything other than
// explicit false would silently break every existing user by suppressing
// shell_execute, workspace, tool_search, graph_memory, and task_board.
func TestMinimalTools_DefaultFalse(t *testing.T) {
	viper.Reset()
	setDefaults()

	assert.False(t, viper.GetBool("tools.minimal"),
		"tools.minimal MUST default to false: this flag is opt-in only and "+
			"flipping it on by default would suppress all auto-injected tools "+
			"for every existing agent (shell_execute, workspace, tool_search, "+
			"graph_memory, task_board)")
}

func TestInsecureAdmin_ConfigField(t *testing.T) {
	tests := []struct {
		name     string
		config   ServerConfig
		expected bool
	}{
		{
			name:     "default is false (secure by default)",
			config:   ServerConfig{},
			expected: false,
		},
		{
			name:     "explicitly set to true allows insecure admin",
			config:   ServerConfig{InsecureAdmin: true},
			expected: true,
		},
		{
			name:     "explicitly set to false keeps secure default",
			config:   ServerConfig{InsecureAdmin: false},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.InsecureAdmin)
		})
	}
}

func TestEnvVar_StoragePostgresDSN(t *testing.T) {
	// Verify that LOOM_STORAGE_POSTGRES_DSN env var overrides storage.postgres.dsn.
	// This tests the SetEnvKeyReplacer(".", "_") mapping for nested config keys.
	viper.Reset()

	// Write a minimal config that sets storage.backend=postgres with empty DSN.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "looms.yaml")
	err := os.WriteFile(cfgPath, []byte(`
server:
  port: 60051
storage:
  backend: postgres
  postgres:
    dsn: ""
`), 0o644)
	require.NoError(t, err)

	const testDSN = "postgresql://user:pass@host:5432/db?sslmode=require"
	t.Setenv("LOOM_STORAGE_POSTGRES_DSN", testDSN)

	cfg, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, testDSN, cfg.Storage.Postgres.DSN,
		"LOOM_STORAGE_POSTGRES_DSN should override storage.postgres.dsn")
}

func TestEnvVar_StoragePostgresDSN_NoYAMLKey(t *testing.T) {
	// Verify that LOOM_STORAGE_POSTGRES_DSN works even when the YAML
	// does not contain a dsn key at all (relies on SetDefault).
	viper.Reset()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "looms.yaml")
	err := os.WriteFile(cfgPath, []byte(`
server:
  port: 60051
storage:
  backend: postgres
`), 0o644)
	require.NoError(t, err)

	const testDSN = "postgresql://user:pass@host:5432/db?sslmode=require"
	t.Setenv("LOOM_STORAGE_POSTGRES_DSN", testDSN)

	cfg, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, testDSN, cfg.Storage.Postgres.DSN,
		"LOOM_STORAGE_POSTGRES_DSN should work even without dsn key in YAML")
}

// TestEnvVar_LLMSecrets_NoYAMLKey pins that every env-only LLM credential and
// the other default-less LLM / web-search string keys reach the config when
// the YAML has no entry for them. viper's AutomaticEnv
// does not register keys, so Unmarshal silently drops an env var whose key has
// no config entry, default, or bound flag. The LongMemEval AKS rig depends on
// LOOM_LLM_BEDROCK_BEARER_TOKEN arriving this way.
func TestEnvVar_LLMSecrets_NoYAMLKey(t *testing.T) {
	tests := []struct {
		env string
		get func(*Config) string
	}{
		{"LOOM_LLM_BEDROCK_BEARER_TOKEN", func(c *Config) string { return c.LLM.BedrockBearerToken }},
		{"LOOM_LLM_BEDROCK_ACCESS_KEY_ID", func(c *Config) string { return c.LLM.BedrockAccessKeyID }},
		{"LOOM_LLM_BEDROCK_SECRET_ACCESS_KEY", func(c *Config) string { return c.LLM.BedrockSecretAccessKey }},
		{"LOOM_LLM_BEDROCK_SESSION_TOKEN", func(c *Config) string { return c.LLM.BedrockSessionToken }},
		{"LOOM_LLM_ANTHROPIC_API_KEY", func(c *Config) string { return c.LLM.AnthropicAPIKey }},
		{"LOOM_LLM_OPENAI_API_KEY", func(c *Config) string { return c.LLM.OpenAIAPIKey }},
		{"LOOM_LLM_AZURE_OPENAI_API_KEY", func(c *Config) string { return c.LLM.AzureOpenAIAPIKey }},
		{"LOOM_LLM_AZURE_OPENAI_ENTRA_TOKEN", func(c *Config) string { return c.LLM.AzureOpenAIEntraToken }},
		{"LOOM_LLM_MISTRAL_API_KEY", func(c *Config) string { return c.LLM.MistralAPIKey }},
		{"LOOM_LLM_GEMINI_API_KEY", func(c *Config) string { return c.LLM.GeminiAPIKey }},
		{"LOOM_LLM_HUGGINGFACE_TOKEN", func(c *Config) string { return c.LLM.HuggingFaceToken }},
		{"LOOM_LLM_LITELLM_API_KEY", func(c *Config) string { return c.LLM.LiteLLMAPIKey }},
		{"LOOM_LLM_BEDROCK_PROFILE", func(c *Config) string { return c.LLM.BedrockProfile }},
		{"LOOM_LLM_LITELLM_ENDPOINT", func(c *Config) string { return c.LLM.LiteLLMEndpoint }},
		{"LOOM_LLM_LITELLM_MODEL", func(c *Config) string { return c.LLM.LiteLLMModel }},
		{"LOOM_TOOLS_WEB_SEARCH_BRAVE_API_KEY", func(c *Config) string { return c.Tools.WebSearch.BraveAPIKey }},
		{"LOOM_TOOLS_WEB_SEARCH_TAVILY_API_KEY", func(c *Config) string { return c.Tools.WebSearch.TavilyAPIKey }},
		{"LOOM_TOOLS_WEB_SEARCH_SERPAPI_KEY", func(c *Config) string { return c.Tools.WebSearch.SerpAPIKey }},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			viper.Reset()
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "looms.yaml")
			require.NoError(t, os.WriteFile(cfgPath, []byte(`
server:
  port: 60051
llm:
  provider: bedrock
  bedrock_region: us-east-1
`), 0o644))

			// Fake value: not a real credential shape, so secret scanning stays quiet.
			const fake = "fake-test-credential-not-a-secret"
			t.Setenv(tt.env, fake)

			cfg, err := LoadConfig(cfgPath)
			require.NoError(t, err)
			assert.Equal(t, fake, tt.get(cfg),
				"%s must populate the config with no YAML key", tt.env)
		})
	}
}

// TestEnvOnlyKeys_UnsetStayEmpty pins that registering empty defaults for the
// env-only keys changes nothing when neither YAML nor env sets them: strings
// stay "", headers stay empty, and YAML-provided headers still win.
func TestEnvOnlyKeys_UnsetStayEmpty(t *testing.T) {
	for _, env := range []string{
		"LOOM_LLM_BEDROCK_BEARER_TOKEN", "LOOM_LLM_BEDROCK_PROFILE",
		"LOOM_LLM_LITELLM_ENDPOINT", "LOOM_LLM_LITELLM_MODEL", "LOOM_LLM_LITELLM_EXTRA_HEADERS",
		"LOOM_TOOLS_WEB_SEARCH_BRAVE_API_KEY", "LOOM_TOOLS_WEB_SEARCH_TAVILY_API_KEY",
		"LOOM_TOOLS_WEB_SEARCH_SERPAPI_KEY",
	} {
		t.Setenv(env, "")
		require.NoError(t, os.Unsetenv(env))
	}

	t.Run("no yaml keys", func(t *testing.T) {
		viper.Reset()
		cfgPath := filepath.Join(t.TempDir(), "looms.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("llm:\n  provider: litellm\n"), 0o644))
		cfg, err := LoadConfig(cfgPath)
		require.NoError(t, err)
		assert.Empty(t, cfg.LLM.BedrockProfile)
		assert.Empty(t, cfg.LLM.LiteLLMEndpoint)
		assert.Empty(t, cfg.LLM.LiteLLMModel)
		assert.Empty(t, cfg.LLM.LiteLLMExtraHeaders)
	})

	t.Run("yaml values are untouched", func(t *testing.T) {
		viper.Reset()
		cfgPath := filepath.Join(t.TempDir(), "looms.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte(`
llm:
  provider: litellm
  bedrock_profile: yaml-profile
  litellm_endpoint: http://yaml:4000
  litellm_model: yaml-model
  litellm_extra_headers:
    X-Team: yaml
`), 0o644))
		cfg, err := LoadConfig(cfgPath)
		require.NoError(t, err)
		assert.Equal(t, "yaml-profile", cfg.LLM.BedrockProfile)
		assert.Equal(t, "http://yaml:4000", cfg.LLM.LiteLLMEndpoint)
		assert.Equal(t, "yaml-model", cfg.LLM.LiteLLMModel)
		// viper lowercases map keys read from YAML (pre-existing behaviour).
		assert.Equal(t, map[string]string{"x-team": "yaml"}, cfg.LLM.LiteLLMExtraHeaders)
	})
}

// TestEnvVar_LiteLLMExtraHeaders pins LOOM_LLM_LITELLM_EXTRA_HEADERS, a map
// key set from a string env var. Before the default + decode hook it was
// silently dropped with no YAML key and silently wiped the YAML headers when
// both were set.
func TestEnvVar_LiteLLMExtraHeaders(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		env     string
		want    map[string]string
		wantErr string
	}{
		{
			name: "key=value pairs, no yaml key",
			env:  "X-Team=abc, X-Token=dG9rZW4=",
			want: map[string]string{"X-Team": "abc", "X-Token": "dG9rZW4="},
		},
		{
			name: "json object, no yaml key",
			env:  `{"X-Team":"a,b","X-Trace":"t"}`,
			want: map[string]string{"X-Team": "a,b", "X-Trace": "t"},
		},
		{
			name: "env overrides yaml",
			yaml: "  litellm_extra_headers:\n    X-Team: yaml\n",
			env:  "X-Team=env",
			want: map[string]string{"X-Team": "env"},
		},
		{
			name:    "malformed value fails loudly",
			env:     "not-a-pair",
			wantErr: "want key=value",
		},
		{
			name:    "malformed json fails loudly",
			env:     `{"X-Team":1}`,
			wantErr: "JSON object of strings",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Reset()
			cfgPath := filepath.Join(t.TempDir(), "looms.yaml")
			require.NoError(t, os.WriteFile(cfgPath, []byte("llm:\n  provider: litellm\n"+tt.yaml), 0o644))
			t.Setenv("LOOM_LLM_LITELLM_EXTRA_HEADERS", tt.env)

			cfg, err := LoadConfig(cfgPath)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LLM.LiteLLMExtraHeaders)
		})
	}
}

func TestStringToStringMapHook(t *testing.T) {
	mapType := reflect.TypeOf(map[string]string(nil))
	strType := reflect.TypeOf("")

	got, err := stringToStringMapHook(strType, mapType, "  ")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{}, got, "blank decodes to an empty map")

	got, err = stringToStringMapHook(strType, reflect.TypeOf([]string(nil)), "a,b")
	require.NoError(t, err)
	assert.Equal(t, "a,b", got, "non-map targets pass through untouched")

	got, err = stringToStringMapHook(reflect.TypeOf(0), mapType, 7)
	require.NoError(t, err)
	assert.Equal(t, 7, got, "non-string sources pass through untouched")

	_, err = stringToStringMapHook(strType, mapType, "=v")
	require.Error(t, err, "an empty key is rejected")
}

func TestEnvVar_PatternsDir_NoYAMLKey(t *testing.T) {
	viper.Reset()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "looms.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("server:\n  port: 60051\n"), 0o644))
	t.Setenv("LOOM_PATTERNS_DIR", "/opt/loom/patterns")

	cfg, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "/opt/loom/patterns", cfg.PatternsDir)
}

func TestEnvVar_NestedKeys(t *testing.T) {
	// Verify SetEnvKeyReplacer works for other nested keys too.
	viper.Reset()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "looms.yaml")
	err := os.WriteFile(cfgPath, []byte(`
server:
  port: 60051
`), 0o644)
	require.NoError(t, err)

	t.Setenv("LOOM_SERVER_PORT", "9999")
	t.Setenv("LOOM_LOGGING_LEVEL", "debug")

	cfg, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, 9999, cfg.Server.Port,
		"LOOM_SERVER_PORT should override server.port")
	assert.Equal(t, "debug", cfg.Logging.Level,
		"LOOM_LOGGING_LEVEL should override logging.level")
}

func TestEnvVar_SkipEmbeddedAgentsWithoutYAMLKey(t *testing.T) {
	viper.Reset()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "looms.yaml")
	err := os.WriteFile(cfgPath, []byte("server:\n  port: 60051\n"), 0o644)
	require.NoError(t, err)
	t.Setenv("LOOM_SKIP_EMBEDDED_AGENTS", "true")

	cfg, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	assert.True(t, cfg.SkipEmbeddedAgents)
}

func TestGenerateExampleConfig_ContainsInsecureAdmin(t *testing.T) {
	exampleConfig := GenerateExampleConfig()
	assert.Contains(t, exampleConfig, "insecure_admin",
		"example config should document the insecure_admin option")
}

// TestLoadConfig_ToolsHooks_DocumentedShape loads the HLD §5.2 config shape —
// the binding list directly at tools.hooks — and asserts it decodes to one
// fully-populated binding (loom#300 review finding 14: the squashed field must
// not require a tools.hooks.hooks nesting).
func TestLoadConfig_ToolsHooks_DocumentedShape(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "looms.yaml")
	yaml := `
tools:
  hooks:
    - kind: gated-allowlist
      scope: execute_sql
      state_key: approved_grants
      source_tool: render_grant_read_sql
      stmt_param: stmt
      read_pattern: "(?i)\\s*select"
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))

	config, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	require.Len(t, config.Tools.Hooks.Bindings, 1, "the documented tools.hooks list must decode")
	b := config.Tools.Hooks.Bindings[0]
	assert.Equal(t, "gated-allowlist", b.Kind)
	assert.Equal(t, "execute_sql", b.Scope)
	assert.Equal(t, "approved_grants", b.StateKey)
	assert.Equal(t, "render_grant_read_sql", b.SourceTool)
	assert.Equal(t, "stmt", b.StmtParam)
}
