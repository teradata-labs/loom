// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package factory provides a factory for creating ExecutionBackend instances
// from YAML configuration files. This enables no-code agent deployment by
// allowing backends to be specified purely through configuration.
package factory

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/backends/mcp"
	"github.com/teradata-labs/loom/pkg/fabric"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"

	// SQL drivers
	_ "github.com/go-sql-driver/mysql"                      // mysql
	_ "github.com/lib/pq"                                   // postgres
	_ "github.com/teradata-labs/loom/internal/sqlitedriver" // sqlite3
)

// LoadFromYAML loads a backend from a YAML configuration file
func LoadFromYAML(path string) (fabric.ExecutionBackend, error) {
	config, err := fabric.LoadBackend(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load backend config: %w", err)
	}

	return NewBackend(config)
}

// NewBackend creates a backend from a proto config
func NewBackend(config *loomv1.BackendConfig) (fabric.ExecutionBackend, error) {
	switch config.Type {
	case "postgres", "mysql", "sqlite":
		return newSQLBackend(config)

	case "file":
		return newFileBackend(config)

	case "rest":
		return newRESTBackend(config)

	case "mcp":
		return newMCPBackend(config)

	default:
		return nil, fmt.Errorf("unsupported backend type: %s (supported: postgres, mysql, sqlite, file, rest, mcp)", config.Type)
	}
}

// newSQLBackend creates a SQL database backend
func newSQLBackend(config *loomv1.BackendConfig) (fabric.ExecutionBackend, error) {
	dbConfig := config.GetDatabase()
	if dbConfig == nil {
		return nil, fmt.Errorf("database config is required for %s backend", config.Type)
	}

	if dbConfig.Dsn == "" {
		return nil, fmt.Errorf("database DSN is required")
	}

	// Determine driver based on type
	driver := config.Type
	if driver == "sqlite" {
		driver = "sqlite3" // go-sqlite3 driver name
	}

	// Open database connection
	db, err := sql.Open(driver, dbConfig.Dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	if dbConfig.MaxConnections > 0 {
		db.SetMaxOpenConns(int(dbConfig.MaxConnections))
	}
	if dbConfig.MaxIdleConnections > 0 {
		db.SetMaxIdleConns(int(dbConfig.MaxIdleConnections))
	}

	// Test connection
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		// #nosec G104 -- best-effort cleanup on initialization failure
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Return generic SQL backend
	return &GenericSQLBackend{
		db:   db,
		name: config.Name,
		typ:  config.Type,
	}, nil
}

// newFileBackend creates a file-based backend
func newFileBackend(config *loomv1.BackendConfig) (fabric.ExecutionBackend, error) {
	// File backend uses DSN as the base directory path
	dbConfig := config.GetDatabase()
	if dbConfig == nil || dbConfig.Dsn == "" {
		return nil, fmt.Errorf("file backend requires database.dsn to specify base directory")
	}

	baseDir := dbConfig.Dsn

	// Create base directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	return &FileBackend{
		baseDir: baseDir,
		name:    config.Name,
	}, nil
}

// newRESTBackend creates a REST API backend
func newRESTBackend(config *loomv1.BackendConfig) (fabric.ExecutionBackend, error) {
	restConfig := config.GetRest()
	if restConfig == nil {
		return nil, fmt.Errorf("rest config is required for rest backend")
	}

	if restConfig.BaseUrl == "" {
		return nil, fmt.Errorf("rest.base_url is required")
	}

	return &RESTBackend{
		name:    config.Name,
		baseURL: restConfig.BaseUrl,
		headers: restConfig.Headers,
		auth:    restConfig.Auth,
		timeout: int(restConfig.TimeoutSeconds),
	}, nil
}

// newMCPBackend creates an MCP-backed backend with subprocess spawning
func newMCPBackend(config *loomv1.BackendConfig) (fabric.ExecutionBackend, error) {
	if config.GetMcp() == nil {
		return nil, fmt.Errorf("MCP configuration is required for MCP backend")
	}

	mcpConfig := config.GetMcp()

	// Create MCP client from subprocess config
	mcpClient, err := createMCPClientFromConfig(mcpConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client: %w", err)
	}

	// Create backend with client
	return NewMCPBackendWithClient(config, mcpClient)
}

// NewMCPBackendWithClient creates an MCP backend with provided client
func NewMCPBackendWithClient(config *loomv1.BackendConfig, mcpClient mcp.MCPClient) (fabric.ExecutionBackend, error) {
	if mcpClient == nil {
		return nil, fmt.Errorf("MCP client is required")
	}

	return mcp.NewMCPBackend(mcp.Config{
		Client:     mcpClient,
		Name:       config.Name,
		ToolPrefix: config.Name,
	})
}

// createMCPClientFromConfig creates an MCP client from protocol buffer config
func createMCPClientFromConfig(config *loomv1.MCPConnection) (mcp.MCPClient, error) {
	if config == nil {
		return nil, fmt.Errorf("MCP config is required")
	}

	// Default transport to stdio if not specified
	transportType := config.Transport
	if transportType == "" {
		transportType = "stdio"
	}

	// Create transport based on type
	var trans transport.Transport
	var err error

	switch transportType {
	case "stdio":
		trans, err = transport.NewStdioTransport(transport.StdioConfig{
			Command: config.Command,
			Args:    config.Args,
			Env:     config.Env,
			Dir:     config.WorkingDir,
			Logger:  zap.NewNop(), // Use no-op logger for factory
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create stdio transport: %w", err)
		}

	case "http", "sse":
		if config.Url == "" {
			return nil, fmt.Errorf("URL is required for HTTP/SSE transport")
		}
		trans, err = transport.NewHTTPTransport(transport.HTTPConfig{
			Endpoint: config.Url,
			Logger:   zap.NewNop(),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP transport: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported transport: %s (supported: stdio, http, sse)", transportType)
	}

	// Create and initialize client
	mcpClient := client.NewClient(client.Config{
		Transport: trans,
		Logger:    zap.NewNop(),
	})

	// Initialize connection with client implementation info
	ctx := context.Background()
	clientImpl := protocol.Implementation{
		Name:    "loom-factory",
		Version: "0.2.0",
	}
	if err := mcpClient.Initialize(ctx, clientImpl); err != nil {
		return nil, fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	return mcpClient, nil
}
