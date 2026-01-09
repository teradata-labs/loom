// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap/zaptest"
)

// MockMCPClient implements the MCP client interface for testing
type MockMCPClient struct {
	tools      map[string]protocol.Tool
	toolCalls  map[string]func(map[string]interface{}) (interface{}, error)
	listCalled bool
}

func NewMockMCPClient() *MockMCPClient {
	return &MockMCPClient{
		tools:     make(map[string]protocol.Tool),
		toolCalls: make(map[string]func(map[string]interface{}) (interface{}, error)),
	}
}

func (m *MockMCPClient) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	m.listCalled = true
	tools := make([]protocol.Tool, 0, len(m.tools))
	for _, tool := range m.tools {
		tools = append(tools, tool)
	}
	return tools, nil
}

func (m *MockMCPClient) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error) {
	if handler, exists := m.toolCalls[name]; exists {
		return handler(arguments)
	}
	return &protocol.CallToolResult{
		Content: []protocol.Content{
			{Type: "text", Text: `{}`},
		},
	}, nil
}

func (m *MockMCPClient) RegisterTool(name, description string) {
	m.tools[name] = protocol.Tool{
		Name:        name,
		Description: description,
		InputSchema: map[string]interface{}{},
	}
}

func (m *MockMCPClient) RegisterToolHandler(name string, handler func(map[string]interface{}) (interface{}, error)) {
	m.toolCalls[name] = handler
}

func TestNewMCPBackend(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("test_execute_query", "Execute a query")
	mockClient.RegisterTool("test_list_resources", "List resources")

	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "valid config",
			config: Config{
				Client: mockClient,
				Name:   "test",
				Logger: zaptest.NewLogger(t),
			},
			expectError: false,
		},
		{
			name: "missing client",
			config: Config{
				Name: "test",
			},
			expectError: true,
		},
		{
			name: "missing name",
			config: Config{
				Client: mockClient,
			},
			expectError: true,
		},
		{
			name: "custom tool prefix",
			config: Config{
				Client:     mockClient,
				Name:       "test",
				ToolPrefix: "custom",
				Logger:     zaptest.NewLogger(t),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := NewMCPBackend(tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, backend)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, backend)
				if backend != nil {
					assert.Equal(t, tt.config.Name, backend.Name())
				}
			}
		})
	}
}

func TestMCPBackend_ExecuteQuery(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("postgres_execute_query", "Execute SQL query")

	// Mock query result
	mockClient.RegisterToolHandler("postgres_execute_query", func(args map[string]interface{}) (interface{}, error) {
		query := args["query"].(string)
		assert.Contains(t, query, "SELECT")

		resultData := map[string]interface{}{
			"rows": []interface{}{
				map[string]interface{}{"id": 1, "name": "Alice"},
				map[string]interface{}{"id": 2, "name": "Bob"},
			},
			"columns": []interface{}{
				map[string]interface{}{"name": "id", "type": "integer", "nullable": false},
				map[string]interface{}{"name": "name", "type": "text", "nullable": true},
			},
			"metadata": map[string]interface{}{
				"row_count": 2,
			},
		}

		resultJSON, _ := json.Marshal(resultData)
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: string(resultJSON)},
			},
		}, nil
	})

	backend, err := NewMCPBackend(Config{
		Client:     mockClient,
		Name:       "postgres",
		ToolPrefix: "postgres",
		Logger:     zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	ctx := context.Background()
	result, err := backend.ExecuteQuery(ctx, "SELECT * FROM users")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "rows", result.Type)
	assert.Equal(t, 2, result.RowCount)
	assert.Len(t, result.Rows, 2)
	assert.Len(t, result.Columns, 2)
	assert.Equal(t, "id", result.Columns[0].Name)
	assert.Equal(t, "integer", result.Columns[0].Type)
	assert.Equal(t, false, result.Columns[0].Nullable)
	assert.GreaterOrEqual(t, result.ExecutionStats.DurationMs, int64(0))
}

func TestMCPBackend_GetSchema(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("postgres_get_schema", "Get table schema")

	mockClient.RegisterToolHandler("postgres_get_schema", func(args map[string]interface{}) (interface{}, error) {
		resource := args["resource"].(string)
		assert.Equal(t, "users", resource)

		schemaData := map[string]interface{}{
			"name": "users",
			"type": "table",
			"fields": []interface{}{
				map[string]interface{}{
					"name":        "id",
					"type":        "integer",
					"description": "Primary key",
					"nullable":    false,
					"primary_key": true,
				},
				map[string]interface{}{
					"name":        "email",
					"type":        "text",
					"description": "User email",
					"nullable":    false,
				},
			},
			"metadata": map[string]interface{}{
				"row_count": 1000,
			},
		}

		schemaJSON, _ := json.Marshal(schemaData)
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: string(schemaJSON)},
			},
		}, nil
	})

	backend, err := NewMCPBackend(Config{
		Client:     mockClient,
		Name:       "postgres",
		ToolPrefix: "postgres",
		Logger:     zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	ctx := context.Background()
	schema, err := backend.GetSchema(ctx, "users")

	assert.NoError(t, err)
	assert.NotNil(t, schema)
	assert.Equal(t, "users", schema.Name)
	assert.Equal(t, "table", schema.Type)
	assert.Len(t, schema.Fields, 2)
	assert.Equal(t, "id", schema.Fields[0].Name)
	assert.Equal(t, "integer", schema.Fields[0].Type)
	assert.Equal(t, true, schema.Fields[0].PrimaryKey)
	assert.Equal(t, "email", schema.Fields[1].Name)
}

func TestMCPBackend_ListResources(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("postgres_list_resources", "List database resources")

	mockClient.RegisterToolHandler("postgres_list_resources", func(args map[string]interface{}) (interface{}, error) {
		resourcesData := map[string]interface{}{
			"resources": []interface{}{
				map[string]interface{}{
					"name":        "users",
					"type":        "table",
					"description": "User accounts",
					"metadata": map[string]interface{}{
						"row_count": 1000,
					},
				},
				map[string]interface{}{
					"name":        "orders",
					"type":        "table",
					"description": "Customer orders",
					"metadata": map[string]interface{}{
						"row_count": 5000,
					},
				},
			},
		}

		resourcesJSON, _ := json.Marshal(resourcesData)
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: string(resourcesJSON)},
			},
		}, nil
	})

	backend, err := NewMCPBackend(Config{
		Client:     mockClient,
		Name:       "postgres",
		ToolPrefix: "postgres",
		Logger:     zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	ctx := context.Background()
	resources, err := backend.ListResources(ctx, map[string]string{"type": "table"})

	assert.NoError(t, err)
	assert.Len(t, resources, 2)
	assert.Equal(t, "users", resources[0].Name)
	assert.Equal(t, "table", resources[0].Type)
	assert.Equal(t, "User accounts", resources[0].Description)
	assert.Equal(t, "orders", resources[1].Name)
}

func TestMCPBackend_Ping(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("postgres_ping", "Check connection")

	mockClient.RegisterToolHandler("postgres_ping", func(args map[string]interface{}) (interface{}, error) {
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: `{"status":"ok"}`},
			},
		}, nil
	})

	backend, err := NewMCPBackend(Config{
		Client:     mockClient,
		Name:       "postgres",
		ToolPrefix: "postgres",
		Logger:     zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	ctx := context.Background()
	err = backend.Ping(ctx)

	assert.NoError(t, err)
}

func TestMCPBackend_Capabilities(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("test_execute_query", "Execute query")
	mockClient.RegisterTool("test_get_schema", "Get schema")

	backend, err := NewMCPBackend(Config{
		Client:     mockClient,
		Name:       "test",
		ToolPrefix: "test",
		Logger:     zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	caps := backend.Capabilities()

	assert.NotNil(t, caps)
	assert.True(t, caps.SupportsConcurrency)
	assert.False(t, caps.SupportsTransactions) // MCP backends typically don't support transactions
	assert.Contains(t, caps.SupportedOperations, "execute_query")
	assert.Contains(t, caps.SupportedOperations, "get_schema")
}

func TestMCPBackend_CustomOperation(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("custom_analyze_table", "Analyze table")

	mockClient.RegisterToolHandler("custom_analyze_table", func(args map[string]interface{}) (interface{}, error) {
		table := args["table"].(string)
		assert.Equal(t, "users", table)

		analysisData := map[string]interface{}{
			"table":      "users",
			"row_count":  1000,
			"size_bytes": 1024000,
		}

		analysisJSON, _ := json.Marshal(analysisData)
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: string(analysisJSON)},
			},
		}, nil
	})

	backend, err := NewMCPBackend(Config{
		Client:     mockClient,
		Name:       "custom",
		ToolPrefix: "custom",
		Logger:     zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	ctx := context.Background()
	result, err := backend.ExecuteCustomOperation(ctx, "analyze_table", map[string]interface{}{
		"table": "users",
	})

	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Result should be a map
	resultMap, ok := result.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "users", resultMap["table"])
	assert.Equal(t, float64(1000), resultMap["row_count"]) // JSON numbers are float64
}

func TestMCPBackend_CustomToolMapping(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("td_query", "Execute Teradata query")

	mockClient.RegisterToolHandler("td_query", func(args map[string]interface{}) (interface{}, error) {
		return &protocol.CallToolResult{
			Content: []protocol.Content{
				{Type: "text", Text: `{"rows":[],"columns":[]}`},
			},
		}, nil
	})

	// Custom tool mapping
	backend, err := NewMCPBackend(Config{
		Client:     mockClient,
		Name:       "teradata",
		ToolPrefix: "teradata",
		ToolMapping: map[string]string{
			"execute_query": "td_query", // Map execute_query to td_query
		},
		Logger: zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	ctx := context.Background()
	result, err := backend.ExecuteQuery(ctx, "SELECT 1")

	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestMCPBackend_Close(t *testing.T) {
	mockClient := NewMockMCPClient()
	mockClient.RegisterTool("test_ping", "Ping")

	backend, err := NewMCPBackend(Config{
		Client: mockClient,
		Name:   "test",
		Logger: zaptest.NewLogger(t),
	})
	require.NoError(t, err)

	err = backend.Close()
	assert.NoError(t, err)
}

func TestMCPBackend_Interface(t *testing.T) {
	// Verify MCPBackend implements fabric.ExecutionBackend
	var _ fabric.ExecutionBackend = (*MCPBackend)(nil)
}
