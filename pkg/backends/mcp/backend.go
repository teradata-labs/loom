// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package mcp provides an ExecutionBackend implementation that wraps MCP servers.
//
// MCPBackend translates fabric.ExecutionBackend calls into MCP tool calls,
// allowing any MCP server to act as a Loom backend. This enables integration
// with 100+ community MCP servers without writing custom backend code.
//
// Example usage:
//
//	// Connect to MCP server
//	mcpClient := client.NewClient(client.Config{...})
//	mcpClient.Initialize(ctx, clientInfo)
//
//	// Create backend wrapping the MCP server
//	backend := mcp.NewMCPBackend(mcp.Config{
//	    Client:     mcpClient,
//	    Name:       "postgres",
//	    ToolPrefix: "postgres",  // Tools: postgres_execute_query, postgres_list_tables, etc.
//	})
//
//	// Use as normal ExecutionBackend
//	result, _ := backend.ExecuteQuery(ctx, "SELECT * FROM users")
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"go.uber.org/zap"
)

// MCPClient defines the interface for MCP client operations needed by MCPBackend
// CallTool returns interface{} to avoid import cycles (actual type is *protocol.CallToolResult)
type MCPClient interface {
	ListTools(ctx context.Context) ([]protocol.Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]interface{}) (interface{}, error)
}

// MCPBackend implements fabric.ExecutionBackend by wrapping an MCP server.
// It translates ExecutionBackend method calls into MCP tool calls.
type MCPBackend struct {
	client     MCPClient
	name       string
	toolPrefix string
	logger     *zap.Logger

	// Cached tools for validation
	tools map[string]protocol.Tool

	// Configuration
	config Config
}

// Config configures the MCP backend
type Config struct {
	// Client is the MCP client connected to the server
	Client MCPClient

	// Name is the backend identifier (e.g., "postgres", "teradata")
	Name string

	// ToolPrefix is prepended to tool names (e.g., "postgres" → "postgres_execute_query")
	// If empty, uses Name
	ToolPrefix string

	// Logger for tracing and debugging
	Logger *zap.Logger

	// ToolMapping maps ExecutionBackend methods to MCP tool names
	// If nil, uses default mapping with ToolPrefix
	ToolMapping map[string]string

	// Capabilities override (optional)
	Capabilities *fabric.Capabilities
}

// NewMCPBackend creates a new MCP-backed ExecutionBackend
func NewMCPBackend(config Config) (*MCPBackend, error) {
	if config.Client == nil {
		return nil, fmt.Errorf("client is required")
	}

	if config.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	if config.ToolPrefix == "" {
		config.ToolPrefix = config.Name
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	b := &MCPBackend{
		client:     config.Client,
		name:       config.Name,
		toolPrefix: config.ToolPrefix,
		logger:     config.Logger,
		tools:      make(map[string]protocol.Tool),
		config:     config,
	}

	// List and cache available tools
	if err := b.refreshTools(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to list MCP tools: %w", err)
	}

	return b, nil
}

// Name returns the backend identifier
func (b *MCPBackend) Name() string {
	return b.name
}

// ExecuteQuery executes a query by calling the MCP tool: {prefix}_execute_query
func (b *MCPBackend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	start := time.Now()

	toolName := b.getToolName("execute_query")

	b.logger.Debug("executing query via MCP",
		zap.String("tool", toolName),
		zap.String("query", query))

	// Call MCP tool
	result, err := b.client.CallTool(ctx, toolName, map[string]interface{}{
		"query": query,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s failed: %w", toolName, err)
	}

	// Convert MCP result to QueryResult
	queryResult, err := b.convertToQueryResult(result)
	if err != nil {
		return nil, err
	}

	// Add execution stats
	queryResult.ExecutionStats.DurationMs = time.Since(start).Milliseconds()

	return queryResult, nil
}

// GetSchema retrieves schema by calling: {prefix}_get_schema
func (b *MCPBackend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	toolName := b.getToolName("get_schema")

	b.logger.Debug("getting schema via MCP",
		zap.String("tool", toolName),
		zap.String("resource", resource))

	result, err := b.client.CallTool(ctx, toolName, map[string]interface{}{
		"resource": resource,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s failed: %w", toolName, err)
	}

	return b.convertToSchema(result)
}

// ListResources lists resources by calling: {prefix}_list_resources
func (b *MCPBackend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	toolName := b.getToolName("list_resources")

	b.logger.Debug("listing resources via MCP",
		zap.String("tool", toolName),
		zap.Any("filters", filters))

	// Convert filters to interface{} for MCP
	mcpFilters := make(map[string]interface{})
	for k, v := range filters {
		mcpFilters[k] = v
	}

	result, err := b.client.CallTool(ctx, toolName, map[string]interface{}{
		"filters": mcpFilters,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s failed: %w", toolName, err)
	}

	return b.convertToResources(result)
}

// GetMetadata retrieves metadata by calling: {prefix}_get_metadata
func (b *MCPBackend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	toolName := b.getToolName("get_metadata")

	b.logger.Debug("getting metadata via MCP",
		zap.String("tool", toolName),
		zap.String("resource", resource))

	result, err := b.client.CallTool(ctx, toolName, map[string]interface{}{
		"resource": resource,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s failed: %w", toolName, err)
	}

	return b.convertToMetadata(result)
}

// Ping checks backend health by calling: {prefix}_ping
func (b *MCPBackend) Ping(ctx context.Context) error {
	toolName := b.getToolName("ping")

	b.logger.Debug("pinging via MCP", zap.String("tool", toolName))

	_, err := b.client.CallTool(ctx, toolName, nil)
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}

	return nil
}

// Capabilities returns backend capabilities
func (b *MCPBackend) Capabilities() *fabric.Capabilities {
	if b.config.Capabilities != nil {
		return b.config.Capabilities
	}

	// Default capabilities for MCP backends
	return &fabric.Capabilities{
		SupportsTransactions: false, // Most MCP servers don't support transactions
		SupportsConcurrency:  true,
		SupportsStreaming:    false,
		MaxConcurrentOps:     10,
		SupportedOperations:  b.listAvailableOperations(),
		Features:             map[string]bool{},
		Limits:               map[string]int64{},
	}
}

// ExecuteCustomOperation executes a custom operation by calling: {prefix}_{op}
func (b *MCPBackend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	toolName := b.getToolName(op)

	b.logger.Debug("executing custom operation via MCP",
		zap.String("tool", toolName),
		zap.String("operation", op),
		zap.Any("params", params))

	result, err := b.client.CallTool(ctx, toolName, params)
	if err != nil {
		return nil, fmt.Errorf("MCP tool %s failed: %w", toolName, err)
	}

	// Return raw content for custom operations
	return b.convertToGeneric(result)
}

// Close releases resources
func (b *MCPBackend) Close() error {
	b.logger.Debug("closing MCP backend", zap.String("name", b.name))
	// MCP client lifecycle is managed externally
	return nil
}

// Helper methods

func (b *MCPBackend) getToolName(operation string) string {
	// Check custom mapping first
	if b.config.ToolMapping != nil {
		if toolName, exists := b.config.ToolMapping[operation]; exists {
			return toolName
		}
	}

	// Default: {prefix}_{operation}
	return fmt.Sprintf("%s_%s", b.toolPrefix, operation)
}

func (b *MCPBackend) refreshTools(ctx context.Context) error {
	tools, err := b.client.ListTools(ctx)
	if err != nil {
		return err
	}

	b.tools = make(map[string]protocol.Tool)
	for _, tool := range tools {
		b.tools[tool.Name] = tool
	}

	b.logger.Info("refreshed MCP tools",
		zap.String("backend", b.name),
		zap.Int("count", len(b.tools)),
		zap.Strings("tools", b.listToolNames()))

	return nil
}

func (b *MCPBackend) listToolNames() []string {
	names := make([]string, 0, len(b.tools))
	for name := range b.tools {
		names = append(names, name)
	}
	return names
}

func (b *MCPBackend) listAvailableOperations() []string {
	operations := []string{}

	// Map known tools to operations
	knownOps := map[string]string{
		b.getToolName("execute_query"):  "execute_query",
		b.getToolName("get_schema"):     "get_schema",
		b.getToolName("list_resources"): "list_resources",
		b.getToolName("get_metadata"):   "get_metadata",
		b.getToolName("ping"):           "ping",
	}

	for toolName, op := range knownOps {
		if _, exists := b.tools[toolName]; exists {
			operations = append(operations, op)
		}
	}

	return operations
}

// Conversion methods

func (b *MCPBackend) convertToQueryResult(resultInterface interface{}) (*fabric.QueryResult, error) {
	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	if !ok {
		return nil, fmt.Errorf("expected *protocol.CallToolResult, got %T", resultInterface)
	}

	if len(result.Content) == 0 {
		return &fabric.QueryResult{Type: "empty"}, nil
	}

	// Extract text content and parse as JSON
	var data interface{}
	for _, content := range result.Content {
		if content.Type == "text" {
			if err := json.Unmarshal([]byte(content.Text), &data); err != nil {
				// If not JSON, return as raw text
				return &fabric.QueryResult{
					Type: "text",
					Data: content.Text,
				}, nil
			}
			break
		}
	}

	// Try to extract rows and columns
	queryResult := &fabric.QueryResult{
		Type: "rows",
	}

	if dataMap, ok := data.(map[string]interface{}); ok {
		// Extract rows
		if rowsData, ok := dataMap["rows"].([]interface{}); ok {
			queryResult.Rows = make([]map[string]interface{}, len(rowsData))
			for i, row := range rowsData {
				if rowMap, ok := row.(map[string]interface{}); ok {
					queryResult.Rows[i] = rowMap
				}
			}
			queryResult.RowCount = len(queryResult.Rows)
		}

		// Extract columns
		if colsData, ok := dataMap["columns"].([]interface{}); ok {
			queryResult.Columns = make([]fabric.Column, len(colsData))
			for i, col := range colsData {
				if colMap, ok := col.(map[string]interface{}); ok {
					queryResult.Columns[i] = fabric.Column{
						Name: colMap["name"].(string),
						Type: colMap["type"].(string),
					}
					if nullable, ok := colMap["nullable"].(bool); ok {
						queryResult.Columns[i].Nullable = nullable
					}
				}
			}
		}

		// Extract metadata
		if metadata, ok := dataMap["metadata"].(map[string]interface{}); ok {
			queryResult.Metadata = metadata
		}
	}

	return queryResult, nil
}

func (b *MCPBackend) convertToSchema(resultInterface interface{}) (*fabric.Schema, error) {
	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	if !ok {
		return nil, fmt.Errorf("expected *protocol.CallToolResult, got %T", resultInterface)
	}

	if len(result.Content) == 0 {
		return nil, fmt.Errorf("empty schema result")
	}

	var data map[string]interface{}
	for _, content := range result.Content {
		if content.Type == "text" {
			if err := json.Unmarshal([]byte(content.Text), &data); err != nil {
				return nil, fmt.Errorf("failed to parse schema: %w", err)
			}
			break
		}
	}

	schema := &fabric.Schema{
		Name:     getStringOrEmpty(data, "name"),
		Type:     getStringOrEmpty(data, "type"),
		Metadata: make(map[string]interface{}),
	}

	// Extract fields
	if fieldsData, ok := data["fields"].([]interface{}); ok {
		schema.Fields = make([]fabric.Field, len(fieldsData))
		for i, f := range fieldsData {
			if fieldMap, ok := f.(map[string]interface{}); ok {
				schema.Fields[i] = fabric.Field{
					Name:        getStringOrEmpty(fieldMap, "name"),
					Type:        getStringOrEmpty(fieldMap, "type"),
					Description: getStringOrEmpty(fieldMap, "description"),
					Nullable:    getBoolOrFalse(fieldMap, "nullable"),
					PrimaryKey:  getBoolOrFalse(fieldMap, "primary_key"),
				}
			}
		}
	}

	if metadata, ok := data["metadata"].(map[string]interface{}); ok {
		schema.Metadata = metadata
	}

	return schema, nil
}

func (b *MCPBackend) convertToResources(resultInterface interface{}) ([]fabric.Resource, error) {
	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	if !ok {
		return nil, fmt.Errorf("expected *protocol.CallToolResult, got %T", resultInterface)
	}

	if len(result.Content) == 0 {
		return []fabric.Resource{}, nil
	}

	var data map[string]interface{}
	for _, content := range result.Content {
		if content.Type == "text" {
			if err := json.Unmarshal([]byte(content.Text), &data); err != nil {
				return nil, fmt.Errorf("failed to parse resources: %w", err)
			}
			break
		}
	}

	resourcesData, ok := data["resources"].([]interface{})
	if !ok {
		return []fabric.Resource{}, nil
	}

	resources := make([]fabric.Resource, len(resourcesData))
	for i, r := range resourcesData {
		if resMap, ok := r.(map[string]interface{}); ok {
			resources[i] = fabric.Resource{
				Name:        getStringOrEmpty(resMap, "name"),
				Type:        getStringOrEmpty(resMap, "type"),
				Description: getStringOrEmpty(resMap, "description"),
				Metadata:    make(map[string]interface{}),
			}
			if metadata, ok := resMap["metadata"].(map[string]interface{}); ok {
				resources[i].Metadata = metadata
			}
		}
	}

	return resources, nil
}

func (b *MCPBackend) convertToMetadata(resultInterface interface{}) (map[string]interface{}, error) {
	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	if !ok {
		return nil, fmt.Errorf("expected *protocol.CallToolResult, got %T", resultInterface)
	}

	if len(result.Content) == 0 {
		return map[string]interface{}{}, nil
	}

	var data map[string]interface{}
	for _, content := range result.Content {
		if content.Type == "text" {
			if err := json.Unmarshal([]byte(content.Text), &data); err != nil {
				return nil, fmt.Errorf("failed to parse metadata: %w", err)
			}
			return data, nil
		}
	}

	return map[string]interface{}{}, nil
}

func (b *MCPBackend) convertToGeneric(resultInterface interface{}) (interface{}, error) {
	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	if !ok {
		return nil, fmt.Errorf("expected *protocol.CallToolResult, got %T", resultInterface)
	}

	if len(result.Content) == 0 {
		return nil, nil
	}

	// Try to parse as JSON first
	for _, content := range result.Content {
		if content.Type == "text" {
			var data interface{}
			if err := json.Unmarshal([]byte(content.Text), &data); err == nil {
				return data, nil
			}
			// Return as string if not JSON
			return content.Text, nil
		}
	}

	return nil, nil
}

// Helper functions

func getStringOrEmpty(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getBoolOrFalse(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
