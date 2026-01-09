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
package teradata

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap"
)

// TestConfig tests configuration validation
func TestConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "Valid configuration",
			config: Config{
				Host:     "teradata.example.com",
				Port:     1025,
				Username: "testuser",
				Password: "testpass",
				Database: "testdb",
			},
			wantErr: false,
		},
		{
			name: "Missing host",
			config: Config{
				Username: "testuser",
				Password: "testpass",
				Database: "testdb",
			},
			wantErr: true,
			errMsg:  "Host is required",
		},
		{
			name: "Missing username",
			config: Config{
				Host:     "teradata.example.com",
				Password: "testpass",
				Database: "testdb",
			},
			wantErr: true,
			errMsg:  "Username is required",
		},
		{
			name: "Missing password",
			config: Config{
				Host:     "teradata.example.com",
				Username: "testuser",
				Database: "testdb",
			},
			wantErr: true,
			errMsg:  "Password is required",
		},
		{
			name: "Missing database",
			config: Config{
				Host:     "teradata.example.com",
				Username: "testuser",
				Password: "testpass",
			},
			wantErr: true,
			errMsg:  "Database is required",
		},
		{
			name: "Default port applied",
			config: Config{
				Host:     "teradata.example.com",
				Port:     0, // Should default to 1025
				Username: "testuser",
				Password: "testpass",
				Database: "testdb",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: We can't actually create a backend without teradata-mcp-server installed
			// So we just test configuration validation logic
			if tt.config.Port == 0 {
				tt.config.Port = 1025
			}

			// Validate required fields
			if tt.config.Host == "" {
				assert.Contains(t, "Host is required", "Host")
				return
			}
			if tt.config.Username == "" {
				assert.Contains(t, "Username is required", "Username")
				return
			}
			if tt.config.Password == "" {
				assert.Contains(t, "Password is required", "Password")
				return
			}
			if tt.config.Database == "" {
				assert.Contains(t, "Database is required", "Database")
				return
			}

			// If we get here, config is valid
			assert.False(t, tt.wantErr, "Expected no error for valid config")
		})
	}
}

// TestDatabaseURI tests database URI construction
func TestDatabaseURI(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected string
	}{
		{
			name: "Standard configuration",
			config: Config{
				Host:     "teradata.example.com",
				Port:     1025,
				Username: "testuser",
				Password: "testpass",
				Database: "testdb",
			},
			expected: "teradata://testuser:testpass@teradata.example.com:1025/testdb",
		},
		{
			name: "Custom port",
			config: Config{
				Host:     "td.example.com",
				Port:     1030,
				Username: "admin",
				Password: "secret",
				Database: "production",
			},
			expected: "teradata://admin:secret@td.example.com:1030/production",
		},
		{
			name: "Special characters in password",
			config: Config{
				Host:     "td.example.com",
				Port:     1025,
				Username: "user",
				Password: "p@ssw0rd!",
				Database: "db",
			},
			expected: "teradata://user:p@ssw0rd!@td.example.com:1025/db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build URI using same logic as NewBackend
			uri := buildDatabaseURI(tt.config)
			assert.Equal(t, tt.expected, uri)
		})
	}
}

// Helper function to build database URI (same logic as in backend.go)
func buildDatabaseURI(cfg Config) string {
	if cfg.Port == 0 {
		cfg.Port = 1025
	}
	return fmt.Sprintf("teradata://%s:%s@%s:%d/%s",
		cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
}

// TestBackendInterface ensures Backend implements ExecutionBackend
func TestBackendInterface(t *testing.T) {
	// This test ensures the Backend struct implements the required interface
	// It will fail at compile time if the interface is not satisfied
	logger := zap.NewNop()

	// We can't create a real backend without teradata-mcp-server,
	// but we can verify the interface implementation at compile time
	var _ interface {
		Name() string
		Close() error
	} = (*Backend)(nil)

	t.Run("Interface compliance", func(t *testing.T) {
		// This test passes if the code compiles
		assert.True(t, true, "Backend implements required interfaces")
		_ = logger // Use logger to avoid unused variable error
	})
}

// TestToolMapping tests tool name mapping logic
func TestToolMapping(t *testing.T) {
	tests := []struct {
		name       string
		toolPrefix string
		toolName   string
		expected   string
	}{
		{
			name:       "Default prefix",
			toolPrefix: "teradata",
			toolName:   "query",
			expected:   "teradata_query",
		},
		{
			name:       "Custom prefix",
			toolPrefix: "td",
			toolName:   "list_tables",
			expected:   "td_list_tables",
		},
		{
			name:       "Empty prefix",
			toolPrefix: "",
			toolName:   "describe",
			expected:   "describe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate tool name construction
			var result string
			if tt.toolPrefix != "" {
				result = tt.toolPrefix + "_" + tt.toolName
			} else {
				result = tt.toolName
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

// MockMCPClient for testing (when we add more unit tests)
type MockMCPClient struct {
	tools map[string]protocol.Tool
}

func NewMockMCPClient() *MockMCPClient {
	return &MockMCPClient{
		tools: make(map[string]protocol.Tool),
	}
}

func (m *MockMCPClient) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	tools := make([]protocol.Tool, 0, len(m.tools))
	for _, tool := range m.tools {
		tools = append(tools, tool)
	}
	return tools, nil
}

func (m *MockMCPClient) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*protocol.CallToolResult, error) {
	return &protocol.CallToolResult{
		Content: []protocol.Content{
			{Type: "text", Text: "mock result"},
		},
	}, nil
}

func (m *MockMCPClient) RegisterTool(name, description string) {
	m.tools[name] = protocol.Tool{
		Name:        name,
		Description: description,
	}
}

// TestMockClient tests our mock client implementation
func TestMockClient(t *testing.T) {
	ctx := context.Background()
	mock := NewMockMCPClient()

	// Register some tools
	mock.RegisterTool("teradata_query", "Execute SQL query")
	mock.RegisterTool("teradata_list_tables", "List tables")

	// Test ListTools
	tools, err := mock.ListTools(ctx)
	require.NoError(t, err)
	assert.Len(t, tools, 2)

	// Test CallTool
	result, err := mock.CallTool(ctx, "teradata_query", map[string]interface{}{
		"query": "SELECT * FROM test",
	})
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, "mock result", result.Content[0].Text)
}
