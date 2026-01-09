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
// Package teradata provides a Teradata backend implementation using the official
// Teradata MCP server (https://github.com/Teradata/teradata-mcp-server).
//
// This backend wraps the public Teradata MCP server, which provides tools for:
// - Query execution (Base Tools)
// - RAG (Search Tools)
// - Feature Store operations
// - Data Quality checks
// - DBA operations
// - Security management
// - Vector Store operations
// - ML functions
// - Plotting
// - Backup and Restore
//
// The Teradata MCP server is a Python-based server that connects to Teradata
// databases and exposes operations via the Model Context Protocol.
package teradata

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/teradata-labs/loom/pkg/backends/mcp"
	"github.com/teradata-labs/loom/pkg/fabric"
	mcpclient "github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// Config holds configuration for the Teradata backend.
type Config struct {
	// Host is the Teradata database host (e.g., "teradata.example.com")
	Host string

	// Port is the Teradata database port (default: 1025)
	Port int

	// Username for Teradata authentication
	Username string

	// Password for Teradata authentication
	Password string

	// Database is the default database/schema to use
	Database string

	// MCPServerPath is the path to the teradata-mcp-server executable
	// If empty, assumes "teradata-mcp-server" is in PATH
	MCPServerPath string

	// Logger for backend operations
	Logger *zap.Logger

	// ToolPrefix to use for tool names (default: "teradata")
	ToolPrefix string
}

// Backend implements fabric.ExecutionBackend for Teradata databases using
// the official Teradata MCP server.
type Backend struct {
	config     Config
	mcpBackend fabric.ExecutionBackend
	mcpClient  *mcpclient.Client
	logger     *zap.Logger
}

// NewBackend creates a new Teradata backend that uses the official Teradata MCP server.
//
// Prerequisites:
//   - Python installed
//   - uv package manager installed (pip install uv)
//   - Teradata MCP server installed (uv pip install teradata-mcp-server)
//   - Teradata database accessible from this machine
//
// The backend will spawn the teradata-mcp-server process and communicate with it
// via the Model Context Protocol (stdio transport).
func NewBackend(ctx context.Context, cfg Config) (*Backend, error) {
	// Set defaults
	if cfg.Port == 0 {
		cfg.Port = 1025
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if cfg.ToolPrefix == "" {
		cfg.ToolPrefix = "teradata"
	}
	if cfg.MCPServerPath == "" {
		cfg.MCPServerPath = "teradata-mcp-server"
	}

	// Validate required fields
	if cfg.Host == "" {
		return nil, fmt.Errorf("Host is required")
	}
	if cfg.Username == "" {
		return nil, fmt.Errorf("Username is required")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("Password is required")
	}
	if cfg.Database == "" {
		return nil, fmt.Errorf("Database is required")
	}

	// Check if teradata-mcp-server is available
	if _, err := exec.LookPath(cfg.MCPServerPath); err != nil {
		return nil, fmt.Errorf("teradata-mcp-server not found in PATH: %w (install with: uv pip install teradata-mcp-server)", err)
	}

	cfg.Logger.Info("creating teradata backend",
		zap.String("host", cfg.Host),
		zap.Int("port", cfg.Port),
		zap.String("username", cfg.Username),
		zap.String("database", cfg.Database))

	// Build DATABASE_URI for Teradata MCP server
	// Format: teradata://USERNAME:PASSWORD@HOST:PORT/DATABASE
	databaseURI := fmt.Sprintf("teradata://%s:%s@%s:%d/%s",
		cfg.Username,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Database)

	// Create stdio transport that spawns teradata-mcp-server
	transport, err := transport.NewStdioTransport(transport.StdioConfig{
		Command: cfg.MCPServerPath,
		Args:    []string{}, // teradata-mcp-server takes no args, uses env vars
		Env: map[string]string{
			"DATABASE_URI": databaseURI,
		},
		Logger: cfg.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP transport: %w", err)
	}

	// Create MCP client with the transport
	mcpClient := mcpclient.NewClient(mcpclient.Config{
		Transport: transport,
		Logger:    cfg.Logger,
		Name:      "loom-teradata-backend",
		Version:   "0.1.1",
	})

	// Initialize the MCP client (connects to server)
	clientInfo := protocol.Implementation{
		Name:    "loom-teradata-backend",
		Version: "0.1.1",
	}
	if err := mcpClient.Initialize(ctx, clientInfo); err != nil {
		return nil, fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	// Create MCPBackend wrapper
	mcpBackend, err := mcp.NewMCPBackend(mcp.Config{
		Client:     mcpClient,
		Name:       "teradata",
		ToolPrefix: cfg.ToolPrefix,
		Logger:     cfg.Logger,
		// Map standard ExecutionBackend operations to Teradata MCP server tools
		// The exact tool names will be discovered from the server
		ToolMapping: map[string]string{
			// These will be mapped once we know the exact tool names from the server
			// For now, use generic names - MCPBackend will discover actual tools
		},
	})
	if err != nil {
		mcpClient.Close()
		return nil, fmt.Errorf("failed to create MCP backend: %w", err)
	}

	cfg.Logger.Info("teradata backend created successfully")

	return &Backend{
		config:     cfg,
		mcpBackend: mcpBackend,
		mcpClient:  mcpClient,
		logger:     cfg.Logger,
	}, nil
}

// ExecuteQuery executes a SQL query via the Teradata MCP server.
func (b *Backend) ExecuteQuery(ctx context.Context, query string) (*fabric.QueryResult, error) {
	b.logger.Debug("executing query",
		zap.String("query", query))
	return b.mcpBackend.ExecuteQuery(ctx, query)
}

// GetSchema retrieves schema information for a table.
func (b *Backend) GetSchema(ctx context.Context, resource string) (*fabric.Schema, error) {
	b.logger.Debug("getting schema",
		zap.String("resource", resource))
	return b.mcpBackend.GetSchema(ctx, resource)
}

// ListResources lists available tables/resources.
func (b *Backend) ListResources(ctx context.Context, filters map[string]string) ([]fabric.Resource, error) {
	b.logger.Debug("listing resources",
		zap.Any("filters", filters))
	return b.mcpBackend.ListResources(ctx, filters)
}

// GetMetadata retrieves metadata for a resource.
func (b *Backend) GetMetadata(ctx context.Context, resource string) (map[string]interface{}, error) {
	b.logger.Debug("getting metadata",
		zap.String("resource", resource))
	return b.mcpBackend.GetMetadata(ctx, resource)
}

// Ping checks if the backend is healthy.
func (b *Backend) Ping(ctx context.Context) error {
	b.logger.Debug("pinging backend")
	return b.mcpBackend.Ping(ctx)
}

// Capabilities returns the backend's capabilities.
func (b *Backend) Capabilities() *fabric.Capabilities {
	return b.mcpBackend.Capabilities()
}

// ExecuteCustomOperation executes a custom operation via the MCP server.
// This allows access to Teradata-specific tools like:
// - RAG operations
// - Feature Store operations
// - Data Quality checks
// - Vector Store operations
// - ML functions
// - Plotting
// - Backup/Restore
func (b *Backend) ExecuteCustomOperation(ctx context.Context, op string, params map[string]interface{}) (interface{}, error) {
	b.logger.Debug("executing custom operation",
		zap.String("operation", op),
		zap.Any("params", params))
	return b.mcpBackend.ExecuteCustomOperation(ctx, op, params)
}

// Close closes the backend and releases resources.
func (b *Backend) Close() error {
	b.logger.Info("closing teradata backend")
	if err := b.mcpBackend.Close(); err != nil {
		b.logger.Error("failed to close MCP backend", zap.Error(err))
		return err
	}
	if err := b.mcpClient.Close(); err != nil {
		b.logger.Error("failed to close MCP client", zap.Error(err))
		return err
	}
	b.logger.Info("teradata backend closed")
	return nil
}

// Name returns the backend name.
func (b *Backend) Name() string {
	return "teradata"
}

// ListTools returns all available tools from the Teradata MCP server.
// This includes Base Tools, RAG Tools, Feature Store Tools, Data Quality Tools,
// DBA Tools, Security Tools, Vector Store Tools, ML Functions, Plot Tools, and BAR Tools.
func (b *Backend) ListTools(ctx context.Context) ([]string, error) {
	tools, err := b.mcpClient.ListTools(ctx)
	if err != nil {
		return nil, err
	}

	toolNames := make([]string, len(tools))
	for i, tool := range tools {
		toolNames[i] = tool.Name
	}

	return toolNames, nil
}

// Ensure Backend implements fabric.ExecutionBackend
var _ fabric.ExecutionBackend = (*Backend)(nil)
