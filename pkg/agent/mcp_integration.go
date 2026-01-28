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
// Package agent provides MCP integration for the Loom agent framework.
package agent

import (
	"context"
	"fmt"

	"github.com/teradata-labs/loom/pkg/mcp/adapter"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// MCPServerConfig configures an MCP server connection
type MCPServerConfig struct {
	// Name is the unique identifier for this MCP server
	// Used for tool namespacing (e.g., "filesystem" -> "filesystem:read_file")
	Name string

	// Client is the initialized MCP client
	Client *client.Client

	// AutoClose determines if the client should be closed when agent is done
	// Default: false (client lifecycle managed externally)
	AutoClose bool
}

// RegisterMCPTools connects to an MCP server and registers all its tools with the agent.
//
// This is a convenience method that:
// 1. Lists all tools from the MCP server
// 2. Converts them to shuttle.Tool instances
// 3. Registers them with the agent
//
// Example usage:
//
//	// Create MCP client
//	trans := transport.NewStdioTransport(config)
//	mcpClient := client.NewClient(client.Config{Transport: trans})
//	mcpClient.Initialize(ctx, clientInfo)
//
//	// Register all MCP tools with agent
//	err := agent.RegisterMCPTools(ctx, MCPServerConfig{
//	    Name:   "filesystem",
//	    Client: mcpClient,
//	})
//
// Tools will be namespaced by server name (e.g., "filesystem:read_file")
func (a *Agent) RegisterMCPTools(ctx context.Context, config MCPServerConfig) error {
	// Validate config
	if config.Name == "" {
		return fmt.Errorf("MCP server name is required")
	}
	if config.Client == nil {
		return fmt.Errorf("MCP client is required")
	}

	// Check if client is initialized
	if !config.Client.IsInitialized() {
		return fmt.Errorf("MCP client must be initialized before registering tools (call client.Initialize first)")
	}

	// Get logger (use agent's tracer logger if available, otherwise create new one)
	logger := zap.NewNop()
	if a.tracer != nil {
		// Tracer might have a logger we can use
		// For now, just use nop logger
		logger = zap.NewNop()
	}

	logger.Info("registering MCP tools",
		zap.String("server", config.Name),
	)

	// List all tools from MCP server
	mcpTools, err := config.Client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("failed to list MCP tools from server %s: %w", config.Name, err)
	}

	// Determine truncation config based on agent memory profile
	truncationConfig := adapter.TruncationConfig{
		MaxResultBytes: 4096, // Default: 4KB
		MaxResultRows:  25,   // Default: 25 rows
		Enabled:        true,
	}

	// Check if this is a data-intensive workload by examining backend type
	if a.backend != nil {
		backendName := a.backend.Name()
		// Data warehouse backends need higher limits
		if backendName == "teradata" || backendName == "teradata-mcp" ||
			backendName == "postgres" || backendName == "snowflake" ||
			backendName == "bigquery" || backendName == "redshift" {
			truncationConfig.MaxResultBytes = 40000 // 40KB for data-intensive (2x base limit)
			truncationConfig.MaxResultRows = 500    // 500 rows
			logger.Info("using data-intensive truncation config",
				zap.Int("max_bytes", truncationConfig.MaxResultBytes),
				zap.Int("max_rows", truncationConfig.MaxResultRows))
		}
	}

	// Convert MCP tools to shuttle.Tool with appropriate truncation
	tools := make([]shuttle.Tool, len(mcpTools))
	for i, mcpTool := range mcpTools {
		mcpAdapter := adapter.NewMCPToolAdapterWithConfig(config.Client, mcpTool, config.Name, truncationConfig)

		// CRITICAL: Inject storage backends for progressive disclosure
		// This enables SQL results to go directly to SQLResultStore (queryable)
		// instead of SharedMemoryStore (unqueryable json_object)
		if a.sqlResultStore != nil {
			mcpAdapter.SetSQLResultStore(a.sqlResultStore)
		}
		if a.sharedMemory != nil {
			mcpAdapter.SetSharedMemory(a.sharedMemory)
		}

		tools[i] = mcpAdapter
	}

	// Register each tool
	for _, tool := range tools {
		a.RegisterTool(tool)
		logger.Debug("registered MCP tool",
			zap.String("tool", tool.Name()),
			zap.String("backend", tool.Backend()),
		)
	}

	// Warn about tool count bloat
	totalTools := a.ToolCount()
	if totalTools > 100 {
		logger.Warn("Large number of tools registered",
			zap.Int("total_tools", totalTools),
			zap.Int("added_tools", len(tools)),
			zap.String("recommendation", "Consider using RegisterMCPServer() or RegisterMCPTool() for selective registration"))
	}

	// Log context usage estimate every 50 tools
	if totalTools%50 == 0 && totalTools > 0 {
		estimatedTokens := totalTools * 170 // ~170 tokens per tool
		logger.Info("Tool registration milestone",
			zap.Int("tool_count", totalTools),
			zap.Int("estimated_context_tokens", estimatedTokens),
			zap.Float64("percent_of_200k", float64(estimatedTokens)/200000*100))
	}

	logger.Info("successfully registered MCP tools",
		zap.String("server", config.Name),
		zap.Int("count", len(tools)),
		zap.Int("total_tools", totalTools),
	)

	// Store client reference for cleanup if AutoClose is enabled
	if config.AutoClose {
		if a.mcpClients == nil {
			a.mcpClients = make(map[string]MCPClientRef)
		}
		a.mcpClients[config.Name] = MCPClientRef{
			Client:     config.Client,
			ServerName: config.Name,
		}
		logger.Debug("stored MCP client for auto-cleanup",
			zap.String("server", config.Name),
		)
	}

	return nil
}

// RegisterMCPServer registers all tools from ONE specific server in the manager.
//
// This provides selective registration at the server level instead of registering
// all servers at once. Useful for controlling context window usage.
//
// Example:
//
//	err := agent.RegisterMCPServer(ctx, mcpMgr, "filesystem")
//	// Only filesystem tools registered, not github, postgres, etc.
func (a *Agent) RegisterMCPServer(ctx context.Context, mcpMgr *manager.Manager, serverName string) error {
	client, err := mcpMgr.GetClient(serverName)
	if err != nil {
		return fmt.Errorf("server %s not found: %w", serverName, err)
	}

	// Get server config for filtering
	serverConfig, err := mcpMgr.GetServerConfig(serverName)
	if err != nil {
		return fmt.Errorf("failed to get server config: %w", err)
	}

	logger := zap.NewNop()
	logger.Info("Registering MCP server tools", zap.String("server", serverName))

	// List all available tools
	allTools, err := client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	// Determine truncation config based on agent backend
	truncationConfig := adapter.TruncationConfig{
		MaxResultBytes: 4096, // Default: 4KB
		MaxResultRows:  25,   // Default: 25 rows
		Enabled:        true,
	}

	// Check if this is a data-intensive workload
	if a.backend != nil {
		backendName := a.backend.Name()
		// Data warehouse backends need higher limits
		if backendName == "teradata" || backendName == "teradata-mcp" ||
			backendName == "postgres" || backendName == "snowflake" ||
			backendName == "bigquery" || backendName == "redshift" {
			truncationConfig.MaxResultBytes = 16384 // 16KB for data-intensive
			truncationConfig.MaxResultRows = 100    // 100 rows
			logger.Info("using data-intensive truncation config",
				zap.Int("max_bytes", truncationConfig.MaxResultBytes),
				zap.Int("max_rows", truncationConfig.MaxResultRows))
		}
	}

	// Filter tools based on config
	var toolsToRegister []protocol.Tool
	for _, tool := range allTools {
		if serverConfig.ToolFilter.ShouldRegisterTool(tool.Name) {
			toolsToRegister = append(toolsToRegister, tool)
		}
	}

	// Register filtered tools with appropriate truncation
	for _, tool := range toolsToRegister {
		mcpAdapter := adapter.NewMCPToolAdapterWithConfig(client, tool, serverName, truncationConfig)

		// CRITICAL: Inject storage backends for progressive disclosure
		if a.sqlResultStore != nil {
			mcpAdapter.SetSQLResultStore(a.sqlResultStore)
		}
		if a.sharedMemory != nil {
			mcpAdapter.SetSharedMemory(a.sharedMemory)
		}

		a.RegisterTool(mcpAdapter)
		logger.Debug("registered MCP tool",
			zap.String("server", serverName),
			zap.String("tool", tool.Name))
	}

	totalTools := a.ToolCount()
	logger.Info("Registered server tools",
		zap.String("server", serverName),
		zap.Int("available", len(allTools)),
		zap.Int("registered", len(toolsToRegister)),
		zap.Int("total_tools", totalTools))

	return nil
}

// RegisterMCPTool registers ONE specific tool from a server.
//
// This provides the finest-grained control over tool registration.
// Useful when you need just 1-2 specific tools from a server.
//
// Example:
//
//	err := agent.RegisterMCPTool(ctx, mcpMgr, "filesystem", "read_file")
//	// Only filesystem:read_file registered
func (a *Agent) RegisterMCPTool(ctx context.Context, mcpMgr *manager.Manager, serverName, toolName string) error {
	client, err := mcpMgr.GetClient(serverName)
	if err != nil {
		return fmt.Errorf("server %s not found: %w", serverName, err)
	}

	logger := zap.NewNop()

	// Determine truncation config based on agent backend
	truncationConfig := adapter.TruncationConfig{
		MaxResultBytes: 4096, // Default: 4KB
		MaxResultRows:  25,   // Default: 25 rows
		Enabled:        true,
	}

	// Check if this is a data-intensive workload
	if a.backend != nil {
		backendName := a.backend.Name()
		// Data warehouse backends need higher limits
		if backendName == "teradata" || backendName == "teradata-mcp" ||
			backendName == "postgres" || backendName == "snowflake" ||
			backendName == "bigquery" || backendName == "redshift" {
			truncationConfig.MaxResultBytes = 16384 // 16KB for data-intensive
			truncationConfig.MaxResultRows = 100    // 100 rows
		}
	}

	// Get tool definition
	tools, err := client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	// Find the specific tool
	for _, tool := range tools {
		if tool.Name == toolName {
			mcpAdapter := adapter.NewMCPToolAdapterWithConfig(client, tool, serverName, truncationConfig)

			// CRITICAL: Inject storage backends for progressive disclosure
			if a.sqlResultStore != nil {
				mcpAdapter.SetSQLResultStore(a.sqlResultStore)
			}
			if a.sharedMemory != nil {
				mcpAdapter.SetSharedMemory(a.sharedMemory)
			}

			shuttleTool := mcpAdapter
			a.RegisterTool(shuttleTool)

			logger.Info("Registered MCP tool",
				zap.String("server", serverName),
				zap.String("tool", toolName),
				zap.Int("total_tools", a.ToolCount()))

			return nil
		}
	}

	return fmt.Errorf("tool %s not found on server %s", toolName, serverName)
}

// RegisterMCPToolsFromManager registers tools from a manager using config-based filtering.
//
// This is the recommended method for production use. It respects the tool filters
// defined in the manager's configuration.
//
// Example config:
//
//	mcp:
//	  servers:
//	    filesystem:
//	      enabled: true
//	      tools:
//	        include: [read_file, write_file]
//	    github:
//	      enabled: true
//	      tools:
//	        all: true
//	        exclude: [delete_repository]
//
// Example usage:
//
//	err := agent.RegisterMCPToolsFromManager(ctx, mcpMgr)
//	// Only tools matching config filters are registered
func (a *Agent) RegisterMCPToolsFromManager(ctx context.Context, mcpMgr *manager.Manager) error {
	logger := zap.NewNop()

	serverNames := mcpMgr.ServerNames()
	if len(serverNames) == 0 {
		return fmt.Errorf("no servers available in manager")
	}

	logger.Info("Registering MCP tools from manager",
		zap.Int("server_count", len(serverNames)))

	var registeredCount int
	for _, serverName := range serverNames {
		if err := a.RegisterMCPServer(ctx, mcpMgr, serverName); err != nil {
			logger.Warn("Skipping server due to error",
				zap.String("server", serverName),
				zap.Error(err))
			continue
		}
		registeredCount++
	}

	if registeredCount == 0 {
		return fmt.Errorf("failed to register tools from any server")
	}

	totalTools := a.ToolCount()
	logger.Info("Successfully registered MCP tools from manager",
		zap.Int("servers", registeredCount),
		zap.Int("total_tools", totalTools))

	return nil
}

// RegisterMCPServers is a convenience method to register multiple MCP servers at once.
//
// Example:
//
//	err := agent.RegisterMCPServers(ctx,
//	    MCPServerConfig{Name: "filesystem", Client: fsClient},
//	    MCPServerConfig{Name: "github", Client: ghClient},
//	    MCPServerConfig{Name: "postgres", Client: pgClient},
//	)
func (a *Agent) RegisterMCPServers(ctx context.Context, configs ...MCPServerConfig) error {
	for _, config := range configs {
		if err := a.RegisterMCPTools(ctx, config); err != nil {
			return fmt.Errorf("failed to register MCP server %s: %w", config.Name, err)
		}
	}
	return nil
}

// CleanupMCPClients closes all MCP clients that were registered with AutoClose=true.
// This should be called when the agent is done to properly cleanup resources.
//
// Example:
//
//	defer agent.CleanupMCPClients()
func (a *Agent) CleanupMCPClients() error {
	if len(a.mcpClients) == 0 {
		return nil
	}

	logger := zap.NewNop()
	if a.tracer != nil {
		logger = zap.NewNop()
	}

	var firstErr error
	for serverName, ref := range a.mcpClients {
		logger.Info("closing MCP client",
			zap.String("server", serverName),
		)

		if err := ref.Client.Close(); err != nil {
			logger.Error("failed to close MCP client",
				zap.String("server", serverName),
				zap.Error(err),
			)
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to close MCP client %s: %w", serverName, err)
			}
		}
	}

	// Clear the map
	a.mcpClients = make(map[string]MCPClientRef)

	return firstErr
}
