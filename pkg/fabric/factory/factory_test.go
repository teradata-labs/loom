// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"

	// Import SQLite driver for tests
	_ "github.com/mutecomm/go-sqlcipher/v4"
)

func TestNewBackend_File(t *testing.T) {
	tmpDir := t.TempDir()

	config := &loomv1.BackendConfig{
		Name: "test-file",
		Type: "file",
		Connection: &loomv1.BackendConfig_Database{
			Database: &loomv1.DatabaseConnection{
				Dsn: tmpDir,
			},
		},
	}

	backend, err := NewBackend(config)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	assert.Equal(t, "test-file", backend.Name())

	// Test Ping
	err = backend.Ping(context.Background())
	assert.NoError(t, err)

	// Test write and read
	ctx := context.Background()
	_, err = backend.ExecuteQuery(ctx, "write test.txt hello world")
	require.NoError(t, err)

	result, err := backend.ExecuteQuery(ctx, "read test.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello world", result.Data)
}

func TestNewBackend_SQLite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	config := &loomv1.BackendConfig{
		Name: "test-sqlite",
		Type: "sqlite",
		Connection: &loomv1.BackendConfig_Database{
			Database: &loomv1.DatabaseConnection{
				Dsn:            dbPath,
				MaxConnections: 5,
			},
		},
	}

	backend, err := NewBackend(config)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	assert.Equal(t, "test-sqlite", backend.Name())

	// Test Ping
	err = backend.Ping(context.Background())
	assert.NoError(t, err)

	// Test query execution
	ctx := context.Background()

	// Create table
	_, err = backend.ExecuteQuery(ctx, "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)

	// Insert data
	_, err = backend.ExecuteQuery(ctx, "INSERT INTO users (name) VALUES ('Alice')")
	require.NoError(t, err)

	// Query data
	result, err := backend.ExecuteQuery(ctx, "SELECT * FROM users")
	require.NoError(t, err)
	assert.Equal(t, "rows", result.Type)
	assert.Equal(t, 1, result.RowCount)
}

func TestNewBackend_REST(t *testing.T) {
	config := &loomv1.BackendConfig{
		Name: "test-rest",
		Type: "rest",
		Connection: &loomv1.BackendConfig_Rest{
			Rest: &loomv1.RestConnection{
				BaseUrl:        "https://api.example.com",
				TimeoutSeconds: 10,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
			},
		},
	}

	backend, err := NewBackend(config)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	assert.Equal(t, "test-rest", backend.Name())

	caps := backend.Capabilities()
	assert.True(t, caps.Features["rest"])
}

func TestNewBackend_UnsupportedType(t *testing.T) {
	config := &loomv1.BackendConfig{
		Name: "test",
		Type: "unsupported",
	}

	backend, err := NewBackend(config)
	assert.Error(t, err)
	assert.Nil(t, backend)
	assert.Contains(t, err.Error(), "unsupported backend type")
}

func TestNewBackend_MissingConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *loomv1.BackendConfig
	}{
		{
			name: "file without database config",
			config: &loomv1.BackendConfig{
				Name: "test",
				Type: "file",
			},
		},
		{
			name: "sqlite without DSN",
			config: &loomv1.BackendConfig{
				Name: "test",
				Type: "sqlite",
				Connection: &loomv1.BackendConfig_Database{
					Database: &loomv1.DatabaseConnection{},
				},
			},
		},
		{
			name: "rest without config",
			config: &loomv1.BackendConfig{
				Name: "test",
				Type: "rest",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := NewBackend(tt.config)
			assert.Error(t, err)
			assert.Nil(t, backend)
		})
	}
}

func TestLoadFromYAML_File(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	yamlPath := filepath.Join(tmpDir, "backend.yaml")

	// Create YAML config
	yamlContent := `
apiVersion: loom/v1
kind: Backend
name: test-file-backend
type: file
database:
  dsn: ` + dataDir + `
`
	err := os.WriteFile(yamlPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	// Load backend
	backend, err := LoadFromYAML(yamlPath)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	assert.Equal(t, "test-file-backend", backend.Name())

	// Verify data directory was created
	_, err = os.Stat(dataDir)
	assert.NoError(t, err)
}

func TestFileBackend_Operations(t *testing.T) {
	tmpDir := t.TempDir()

	backend := &FileBackend{
		baseDir: tmpDir,
		name:    "test-file",
	}

	ctx := context.Background()

	// Test write
	result, err := backend.ExecuteQuery(ctx, "write test.txt hello world")
	require.NoError(t, err)
	assert.Contains(t, result.Data.(string), "Wrote")

	// Test read
	result, err = backend.ExecuteQuery(ctx, "read test.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello world", result.Data)

	// Test list
	result, err = backend.ExecuteQuery(ctx, "list")
	require.NoError(t, err)
	assert.Equal(t, 1, result.RowCount)

	// Test ListResources
	resources, err := backend.ListResources(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, resources, 1)
	assert.Equal(t, "test.txt", resources[0].Name)

	// Test GetSchema
	schema, err := backend.GetSchema(ctx, "test.txt")
	require.NoError(t, err)
	assert.Equal(t, "test.txt", schema.Name)
	assert.Equal(t, "file", schema.Type)

	// Test delete custom operation
	_, err = backend.ExecuteCustomOperation(ctx, "delete", map[string]interface{}{
		"filename": "test.txt",
	})
	require.NoError(t, err)

	// Verify file is gone
	resources, err = backend.ListResources(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, resources, 0)
}

func TestGenericSQLBackend_SQLite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	config := &loomv1.BackendConfig{
		Name: "test-db",
		Type: "sqlite",
		Connection: &loomv1.BackendConfig_Database{
			Database: &loomv1.DatabaseConnection{
				Dsn: dbPath,
			},
		},
	}

	backend, err := NewBackend(config)
	require.NoError(t, err)
	defer backend.Close()

	ctx := context.Background()

	// Create table
	_, err = backend.ExecuteQuery(ctx, `
		CREATE TABLE products (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			price REAL
		)
	`)
	require.NoError(t, err)

	// Insert data
	_, err = backend.ExecuteQuery(ctx, "INSERT INTO products (name, price) VALUES ('Widget', 19.99)")
	require.NoError(t, err)

	_, err = backend.ExecuteQuery(ctx, "INSERT INTO products (name, price) VALUES ('Gadget', 29.99)")
	require.NoError(t, err)

	// Query data
	result, err := backend.ExecuteQuery(ctx, "SELECT * FROM products ORDER BY id")
	require.NoError(t, err)
	assert.Equal(t, "rows", result.Type)
	assert.Equal(t, 2, result.RowCount)
	assert.Len(t, result.Rows, 2)

	// Test GetSchema
	schema, err := backend.GetSchema(ctx, "products")
	require.NoError(t, err)
	assert.Equal(t, "products", schema.Name)
	assert.Len(t, schema.Fields, 3)
	assert.Equal(t, "id", schema.Fields[0].Name)
	assert.True(t, schema.Fields[0].PrimaryKey)

	// Test ListResources
	resources, err := backend.ListResources(ctx, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(resources), 1)

	// Find our table
	var found bool
	for _, r := range resources {
		if r.Name == "products" {
			found = true
			break
		}
	}
	assert.True(t, found, "products table should be in resources list")
}

func TestRESTBackend_ExecuteQuery(t *testing.T) {
	backend := &RESTBackend{
		name:    "test-rest",
		baseURL: "https://httpbin.org",
		headers: map[string]string{
			"Content-Type": "application/json",
		},
	}

	ctx := context.Background()

	// Note: This test requires internet connectivity
	// Skip if httpbin.org is unavailable
	t.Run("GET request", func(t *testing.T) {
		result, err := backend.ExecuteQuery(ctx, "GET /get")
		if err != nil {
			t.Skip("httpbin.org unavailable:", err)
		}
		require.NoError(t, err)

		// Skip if httpbin returns non-JSON (service unavailable/rate-limited)
		if result.Type != "json" {
			t.Skip("httpbin.org unavailable or rate-limited (returned non-JSON response)")
		}

		assert.Equal(t, "json", result.Type)
	})

	// Test capabilities
	caps := backend.Capabilities()
	assert.True(t, caps.Features["rest"])
	assert.Contains(t, caps.SupportedOperations, "GET")
	assert.Contains(t, caps.SupportedOperations, "POST")
}

func TestBackend_Capabilities(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		backend  func() *FileBackend
		features map[string]bool
	}{
		{
			name: "file backend",
			backend: func() *FileBackend {
				return &FileBackend{baseDir: tmpDir, name: "test"}
			},
			features: map[string]bool{"filesystem": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := tt.backend()
			caps := backend.Capabilities()

			for feature, expected := range tt.features {
				assert.Equal(t, expected, caps.Features[feature], "feature: %s", feature)
			}
		})
	}
}

// mockMCPClient implements mcp.MCPClient for testing
type mockMCPClient struct {
	tools []protocol.Tool
}

func (m *mockMCPClient) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	return m.tools, nil
}

func (m *mockMCPClient) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error) {
	return &protocol.CallToolResult{
		Content: []protocol.Content{
			{Type: "text", Text: "mock result"},
		},
	}, nil
}

func TestNewMCPBackendWithClient(t *testing.T) {
	mockClient := &mockMCPClient{
		tools: []protocol.Tool{
			{Name: "test_tool", Description: "A test tool"},
		},
	}

	config := &loomv1.BackendConfig{
		Name: "test-mcp",
		Type: "mcp",
	}

	backend, err := NewMCPBackendWithClient(config, mockClient)
	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify backend name
	assert.Equal(t, "test-mcp", backend.Name())

	// Verify backend capabilities (MCP backends have default capabilities)
	caps := backend.Capabilities()
	assert.NotNil(t, caps)
	assert.True(t, caps.SupportsConcurrency)
	assert.False(t, caps.SupportsTransactions)

	// Verify backend can be pinged
	ctx := context.Background()
	err = backend.Ping(ctx)
	require.NoError(t, err)

	// Verify backend can be closed
	err = backend.Close()
	assert.NoError(t, err)
}

func TestNewMCPBackendWithClient_NilClient(t *testing.T) {
	config := &loomv1.BackendConfig{
		Name: "test-mcp",
		Type: "mcp",
	}

	backend, err := NewMCPBackendWithClient(config, nil)
	assert.Error(t, err)
	assert.Nil(t, backend)
	assert.Contains(t, err.Error(), "MCP client is required")
}

func TestNewMCPBackend_RequiresMCPConfig(t *testing.T) {
	config := &loomv1.BackendConfig{
		Name: "test-mcp",
		Type: "mcp",
		// Missing MCP connection config
	}

	backend, err := newMCPBackend(config)
	assert.Error(t, err)
	assert.Nil(t, backend)
	assert.Contains(t, err.Error(), "MCP configuration is required")
}

func TestNewMCPBackend_ValidatesTransport(t *testing.T) {
	config := &loomv1.BackendConfig{
		Name: "test-mcp",
		Type: "mcp",
		Connection: &loomv1.BackendConfig_Mcp{
			Mcp: &loomv1.MCPConnection{
				Transport: "invalid-transport",
				Command:   "python3",
				Args:      []string{"-m", "test"},
			},
		},
	}

	backend, err := newMCPBackend(config)
	assert.Error(t, err)
	assert.Nil(t, backend)
	assert.Contains(t, err.Error(), "unsupported transport")
}

func TestNewMCPBackend_HTTPRequiresURL(t *testing.T) {
	config := &loomv1.BackendConfig{
		Name: "test-mcp",
		Type: "mcp",
		Connection: &loomv1.BackendConfig_Mcp{
			Mcp: &loomv1.MCPConnection{
				Transport: "http",
				// Missing URL
			},
		},
	}

	backend, err := newMCPBackend(config)
	assert.Error(t, err)
	assert.Nil(t, backend)
	assert.Contains(t, err.Error(), "URL is required")
}
