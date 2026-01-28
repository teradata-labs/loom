// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/manager"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Valid MCP transport types
var validTransports = map[string]bool{
	"stdio": true,
	"http":  true,
	"sse":   true,
}

// ListMCPServers lists all configured MCP servers.
func (s *MultiAgentServer) ListMCPServers(ctx context.Context, req *loomv1.ListMCPServersRequest) (*loomv1.ListMCPServersResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.mcpManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "MCP manager not initialized")
	}

	servers := s.mcpManager.ListServers()
	protoServers := make([]*loomv1.MCPServerInfo, len(servers))

	for i, srv := range servers {
		protoServers[i] = &loomv1.MCPServerInfo{
			Name:      srv.Name,
			Enabled:   srv.Enabled,
			Connected: srv.Connected,
			Transport: srv.Transport,
			Status:    determineStatus(srv),
		}

		// Get additional details from config
		if cfg, err := s.mcpManager.GetServerConfig(srv.Name); err == nil {
			protoServers[i].Command = cfg.Command
			protoServers[i].Args = cfg.Args
			protoServers[i].Env = cfg.Env
		}

		// Get tool count if connected
		if srv.Connected {
			if client, err := s.mcpManager.GetClient(srv.Name); err == nil {
				if tools, err := client.ListTools(ctx); err == nil {
					protoServers[i].ToolCount = int32(len(tools))
				}
			}
		}
	}

	return &loomv1.ListMCPServersResponse{
		Servers:    protoServers,
		TotalCount: int32(len(protoServers)),
	}, nil
}

// GetMCPServer retrieves a specific MCP server.
func (s *MultiAgentServer) GetMCPServer(ctx context.Context, req *loomv1.GetMCPServerRequest) (*loomv1.MCPServerInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.mcpManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "MCP manager not initialized")
	}

	servers := s.mcpManager.ListServers()
	for _, srv := range servers {
		if srv.Name == req.ServerName {
			info := &loomv1.MCPServerInfo{
				Name:      srv.Name,
				Enabled:   srv.Enabled,
				Connected: srv.Connected,
				Transport: srv.Transport,
				Status:    determineStatus(srv),
			}

			if cfg, err := s.mcpManager.GetServerConfig(srv.Name); err == nil {
				info.Command = cfg.Command
				info.Args = cfg.Args
				info.Env = cfg.Env
			}

			// Get tool count if connected
			if srv.Connected {
				if client, err := s.mcpManager.GetClient(srv.Name); err == nil {
					if tools, err := client.ListTools(ctx); err == nil {
						info.ToolCount = int32(len(tools))
					}
				}
			}

			return info, nil
		}
	}

	return nil, status.Error(codes.NotFound, "server not found")
}

// AddMCPServer adds a new MCP server configuration.
func (s *MultiAgentServer) AddMCPServer(ctx context.Context, req *loomv1.AddMCPServerRequest) (*loomv1.AddMCPServerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate required fields
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "server name is required")
	}
	if req.Command == "" {
		return nil, status.Error(codes.InvalidArgument, "command is required")
	}

	// Set default transport
	if req.Transport == "" {
		req.Transport = "stdio"
	}

	// Validate transport type
	if !validTransports[req.Transport] {
		return nil, status.Errorf(codes.InvalidArgument, "invalid transport type: %s (must be stdio, http, or sse)", req.Transport)
	}

	// Validate command exists (for stdio transport)
	if req.Transport == "stdio" {
		if _, err := exec.LookPath(req.Command); err != nil {
			// Command not in PATH, check if it's an absolute path
			if !filepath.IsAbs(req.Command) {
				return nil, status.Errorf(codes.InvalidArgument, "command not found: %s (not in PATH and not an absolute path)", req.Command)
			}
			// Check if absolute path exists
			if _, err := os.Stat(req.Command); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "command not found: %s (%v)", req.Command, err)
			}
		}
	}

	// Validate working directory if specified
	if req.WorkingDir != "" {
		if _, err := os.Stat(req.WorkingDir); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "working directory does not exist: %s", req.WorkingDir)
		}
	}

	// Check if server already exists
	if s.mcpManager != nil {
		servers := s.mcpManager.ListServers()
		for _, srv := range servers {
			if srv.Name == req.Name {
				return nil, status.Error(codes.AlreadyExists, "server with this name already exists")
			}
		}
	}

	// Convert proto tool filter to manager tool filter
	toolFilter := manager.ToolFilter{
		All: true, // Default: register all tools
	}
	if req.ToolFilter != nil {
		toolFilter.All = req.ToolFilter.All
		toolFilter.Include = req.ToolFilter.Include
		toolFilter.Exclude = req.ToolFilter.Exclude
	}

	// Build server config
	serverConfig := manager.ServerConfig{
		Command:          req.Command,
		Args:             req.Args,
		Env:              req.Env,
		Transport:        req.Transport,
		URL:              req.Url, // Note: req.Url from proto (lowercase 'rl')
		EnableSessions:   req.EnableSessions,
		EnableResumption: req.EnableResumption,
		Enabled:          req.Enabled,
		ToolFilter:       toolFilter,
	}

	// If auto_start, try to start server FIRST to validate it works
	var serverInfo *loomv1.MCPServerInfo
	if req.AutoStart && s.mcpManager != nil && req.Enabled {
		if err := s.mcpManager.AddServer(ctx, req.Name, serverConfig); err != nil {
			if s.logger != nil {
				s.logger.Error("Failed to start MCP server",
					zap.String("server", req.Name),
					zap.Error(err))
			}
			return &loomv1.AddMCPServerResponse{
				Success: false,
				Message: fmt.Sprintf("Server failed validation: %v", err),
				Server: &loomv1.MCPServerInfo{
					Name:    req.Name,
					Status:  "error",
					Error:   err.Error(),
					Enabled: req.Enabled,
				},
			}, nil
		}

		// Get server info after successful start
		serverInfo = &loomv1.MCPServerInfo{
			Name:      req.Name,
			Enabled:   req.Enabled,
			Connected: true,
			Transport: req.Transport,
			Command:   req.Command,
			Args:      req.Args,
			Env:       req.Env,
			Status:    "running",
		}

		// Get tool count
		if client, err := s.mcpManager.GetClient(req.Name); err == nil {
			if tools, err := client.ListTools(ctx); err == nil {
				serverInfo.ToolCount = int32(len(tools))
			}
		}
	} else {
		// Not auto-starting, just prepare info
		serverInfo = &loomv1.MCPServerInfo{
			Name:      req.Name,
			Enabled:   req.Enabled,
			Connected: false,
			Transport: req.Transport,
			Command:   req.Command,
			Args:      req.Args,
			Env:       req.Env,
			Status:    "stopped",
		}
	}

	// Only write config after successful validation/start
	if err := s.addMCPServerToConfig(req); err != nil {
		// Rollback: stop the server we just started
		if req.AutoStart && s.mcpManager != nil && req.Enabled {
			if rollbackErr := s.mcpManager.StopServer(req.Name); rollbackErr != nil {
				if s.logger != nil {
					s.logger.Error("Failed to rollback server after config write failure",
						zap.String("server", req.Name),
						zap.Error(rollbackErr))
				}
			}
		}
		return nil, status.Errorf(codes.Internal, "failed to update config: %v", err)
	}

	// Re-index tool registry so new MCP tools are discoverable via tool_search
	if s.toolRegistry != nil && req.AutoStart && req.Enabled {
		go func() {
			indexCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if resp, err := s.toolRegistry.IndexAll(indexCtx); err != nil {
				if s.logger != nil {
					s.logger.Warn("Failed to re-index tools after adding MCP server",
						zap.String("server", req.Name),
						zap.Error(err))
				}
			} else if s.logger != nil {
				s.logger.Info("Re-indexed tools after adding MCP server",
					zap.String("server", req.Name),
					zap.Int32("total_tools", resp.TotalCount),
					zap.Int32("mcp_tools", resp.McpCount))
			}
		}()
	}

	return &loomv1.AddMCPServerResponse{
		Success: true,
		Message: "MCP server added successfully",
		Server:  serverInfo,
	}, nil
}

// UpdateMCPServer updates an existing MCP server configuration.
func (s *MultiAgentServer) UpdateMCPServer(ctx context.Context, req *loomv1.UpdateMCPServerRequest) (*loomv1.MCPServerInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.ServerName == "" {
		return nil, status.Error(codes.InvalidArgument, "server name is required")
	}
	if req.Command == "" {
		return nil, status.Error(codes.InvalidArgument, "command is required")
	}

	// Validate transport type
	if req.Transport != "" && !validTransports[req.Transport] {
		return nil, status.Errorf(codes.InvalidArgument, "invalid transport type: %s (must be stdio, http, or sse)", req.Transport)
	}

	// Validate command exists (for stdio transport)
	if req.Transport == "stdio" || req.Transport == "" {
		if _, err := exec.LookPath(req.Command); err != nil {
			if !filepath.IsAbs(req.Command) {
				return nil, status.Errorf(codes.InvalidArgument, "command not found: %s (not in PATH and not an absolute path)", req.Command)
			}
			if _, err := os.Stat(req.Command); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "command not found: %s (%v)", req.Command, err)
			}
		}
	}

	// Validate working directory if specified
	if req.WorkingDir != "" {
		if _, err := os.Stat(req.WorkingDir); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "working directory does not exist: %s", req.WorkingDir)
		}
	}

	// Check if server exists
	servers := s.mcpManager.ListServers()
	var serverExists bool
	var wasConnected bool
	for _, srv := range servers {
		if srv.Name == req.ServerName {
			serverExists = true
			wasConnected = srv.Connected
			break
		}
	}

	if !serverExists {
		return nil, status.Error(codes.NotFound, "server not found")
	}

	// Convert proto tool filter to manager tool filter
	toolFilter := manager.ToolFilter{
		All: true, // Default: register all tools
	}
	if req.ToolFilter != nil {
		toolFilter.All = req.ToolFilter.All
		toolFilter.Include = req.ToolFilter.Include
		toolFilter.Exclude = req.ToolFilter.Exclude
	}

	// Build server config
	serverConfig := manager.ServerConfig{
		Command:          req.Command,
		Args:             req.Args,
		Env:              req.Env,
		Transport:        req.Transport,
		URL:              req.Url, // Note: req.Url from proto (lowercase 'rl')
		EnableSessions:   req.EnableSessions,
		EnableResumption: req.EnableResumption,
		Enabled:          req.Enabled,
		ToolFilter:       toolFilter,
	}

	// If restart requested and server is running
	if req.RestartIfRunning && wasConnected && s.mcpManager != nil {
		// Stop the server
		if err := s.mcpManager.StopServer(req.ServerName); err != nil {
			if s.logger != nil {
				s.logger.Warn("Failed to stop server for restart",
					zap.String("server", req.ServerName),
					zap.Error(err))
			}
		}

		// Wait for graceful shutdown with configurable timeout
		timeout := time.Second // Default 1 second
		if req.TimeoutSeconds > 0 {
			timeout = time.Duration(req.TimeoutSeconds) * time.Second
		}

		select {
		case <-ctx.Done():
			return nil, status.Error(codes.DeadlineExceeded, "context deadline exceeded during server restart")
		case <-time.After(timeout):
			// Continue
		}

		// Start with new config
		if err := s.mcpManager.AddServer(ctx, req.ServerName, serverConfig); err != nil {
			if s.logger != nil {
				s.logger.Error("Failed to restart server after update",
					zap.String("server", req.ServerName),
					zap.Error(err))
			}
			return nil, status.Errorf(codes.Internal, "failed to restart server: %v", err)
		}
	}

	// Update config file after successful validation/restart
	if err := s.updateMCPServerInConfig(req); err != nil {
		// Try to rollback to previous state if we restarted
		if req.RestartIfRunning && wasConnected && s.mcpManager != nil {
			if s.logger != nil {
				s.logger.Warn("Config update failed after restart, manual intervention may be needed",
					zap.String("server", req.ServerName))
			}
		}
		return nil, status.Errorf(codes.Internal, "failed to update config: %v", err)
	}

	return &loomv1.MCPServerInfo{
		Name:      req.ServerName,
		Enabled:   req.Enabled,
		Connected: req.RestartIfRunning && wasConnected,
		Status:    "updated",
	}, nil
}

// DeleteMCPServer removes an MCP server.
func (s *MultiAgentServer) DeleteMCPServer(ctx context.Context, req *loomv1.DeleteMCPServerRequest) (*loomv1.DeleteMCPServerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.ServerName == "" {
		return nil, status.Error(codes.InvalidArgument, "server name is required")
	}

	// Check if server is running
	if s.mcpManager != nil {
		servers := s.mcpManager.ListServers()
		for _, srv := range servers {
			if srv.Name == req.ServerName && srv.Connected && !req.Force {
				return nil, status.Error(codes.FailedPrecondition, "server is running; use force=true to delete")
			}
		}

		// Remove the server completely from the manager (stops it and removes from config)
		if err := s.mcpManager.RemoveServer(req.ServerName); err != nil {
			if s.logger != nil {
				s.logger.Warn("Error removing server from manager during deletion",
					zap.String("server", req.ServerName),
					zap.Error(err))
			}
		}
	}

	// Remove from config file
	if err := s.removeMCPServerFromConfig(req.ServerName); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update config: %v", err)
	}

	// Re-index tool registry so deleted MCP tools are removed from search
	if s.toolRegistry != nil {
		go func() {
			indexCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if resp, err := s.toolRegistry.IndexAll(indexCtx); err != nil {
				if s.logger != nil {
					s.logger.Warn("Failed to re-index tools after deleting MCP server",
						zap.String("server", req.ServerName),
						zap.Error(err))
				}
			} else if s.logger != nil {
				s.logger.Info("Re-indexed tools after deleting MCP server",
					zap.String("server", req.ServerName),
					zap.Int32("total_tools", resp.TotalCount),
					zap.Int32("mcp_tools", resp.McpCount))
			}
		}()
	}

	return &loomv1.DeleteMCPServerResponse{
		Success: true,
		Message: "MCP server deleted successfully",
	}, nil
}

// RestartMCPServer restarts a running MCP server.
func (s *MultiAgentServer) RestartMCPServer(ctx context.Context, req *loomv1.RestartMCPServerRequest) (*loomv1.MCPServerInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mcpManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "MCP manager not initialized")
	}

	if req.ServerName == "" {
		return nil, status.Error(codes.InvalidArgument, "server name is required")
	}

	// Get current config
	cfg, err := s.mcpManager.GetServerConfig(req.ServerName)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	// Stop the server
	if err := s.mcpManager.StopServer(req.ServerName); err != nil {
		if s.logger != nil {
			s.logger.Warn("Error stopping server for restart",
				zap.String("server", req.ServerName),
				zap.Error(err))
		}
	}

	// Wait for graceful shutdown with context awareness
	timeout := 500 * time.Millisecond
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	select {
	case <-ctx.Done():
		return nil, status.Error(codes.DeadlineExceeded, "context deadline exceeded during server restart")
	case <-time.After(timeout):
		// Continue with restart
	}

	// Start the server
	if err := s.mcpManager.AddServer(ctx, req.ServerName, cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to restart server: %v", err)
	}

	return &loomv1.MCPServerInfo{
		Name:      req.ServerName,
		Status:    "running",
		Connected: true,
	}, nil
}

// HealthCheckMCPServers checks health of all MCP servers.
func (s *MultiAgentServer) HealthCheckMCPServers(ctx context.Context, req *loomv1.HealthCheckMCPServersRequest) (*loomv1.HealthCheckMCPServersResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.mcpManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "MCP manager not initialized")
	}

	healthMap := s.mcpManager.HealthCheck(ctx)
	protoHealth := make(map[string]*loomv1.MCPServerHealth)

	for name, healthy := range healthMap {
		protoHealth[name] = &loomv1.MCPServerHealth{
			Status:             healthyToStatus(healthy),
			LastCheckTimestamp: time.Now().Unix(),
		}
	}

	return &loomv1.HealthCheckMCPServersResponse{
		Servers: protoHealth,
	}, nil
}

// TestMCPServerConnection tests an MCP server configuration without persisting it.
// This allows users to validate their configuration before saving.
func (s *MultiAgentServer) TestMCPServerConnection(ctx context.Context, req *loomv1.TestMCPServerConnectionRequest) (*loomv1.TestMCPServerConnectionResponse, error) {
	startTime := time.Now()

	// Validate required fields
	if req.Command == "" {
		return &loomv1.TestMCPServerConnectionResponse{
			Success: false,
			Error:   "command is required",
		}, nil
	}

	// Set default transport
	if req.Transport == "" {
		req.Transport = "stdio"
	}

	// Validate transport type
	if !validTransports[req.Transport] {
		return &loomv1.TestMCPServerConnectionResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid transport type: %s (must be stdio, http, or sse)", req.Transport),
		}, nil
	}

	// Validate command exists (for stdio transport)
	if req.Transport == "stdio" {
		if _, err := exec.LookPath(req.Command); err != nil {
			if !filepath.IsAbs(req.Command) {
				return &loomv1.TestMCPServerConnectionResponse{
					Success: false,
					Error:   fmt.Sprintf("command not found: %s (not in PATH and not an absolute path)", req.Command),
				}, nil
			}
			if _, err := os.Stat(req.Command); err != nil {
				return &loomv1.TestMCPServerConnectionResponse{
					Success: false,
					Error:   fmt.Sprintf("command not found: %s (%v)", req.Command, err),
				}, nil
			}
		}
	}

	// Validate working directory if specified
	if req.WorkingDir != "" {
		if _, err := os.Stat(req.WorkingDir); err != nil {
			return &loomv1.TestMCPServerConnectionResponse{
				Success: false,
				Error:   fmt.Sprintf("working directory does not exist: %s", req.WorkingDir),
			}, nil
		}
	}

	// Check if MCP manager is available
	if s.mcpManager == nil {
		return &loomv1.TestMCPServerConnectionResponse{
			Success: false,
			Error:   "MCP manager not initialized",
		}, nil
	}

	// Create unique test server name
	testServerName := fmt.Sprintf("__test__%d", time.Now().UnixNano())

	// Convert proto tool filter to manager tool filter
	toolFilter := manager.ToolFilter{
		All: true, // Default: register all tools
	}
	if req.ToolFilter != nil {
		toolFilter.All = req.ToolFilter.All
		toolFilter.Include = req.ToolFilter.Include
		toolFilter.Exclude = req.ToolFilter.Exclude
	}

	// Build server config
	serverConfig := manager.ServerConfig{
		Command:    req.Command,
		Args:       req.Args,
		Env:        req.Env,
		Transport:  req.Transport,
		Enabled:    true,
		ToolFilter: toolFilter,
	}

	// Set test timeout
	testTimeout := 10 * time.Second
	if req.TimeoutSeconds > 0 {
		testTimeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()

	// Try to start the server
	s.mu.Lock()
	// Ensure cleanup happens regardless of success or failure
	defer func() {
		// Always stop and remove the test server
		_ = s.mcpManager.StopServer(testServerName)
		s.mu.Unlock()
	}()

	if err := s.mcpManager.AddServer(testCtx, testServerName, serverConfig); err != nil {
		return &loomv1.TestMCPServerConnectionResponse{
			Success:   false,
			Error:     fmt.Sprintf("failed to start server: %v", err),
			LatencyMs: time.Since(startTime).Milliseconds(),
		}, nil
	}

	// Get client and list tools
	client, err := s.mcpManager.GetClient(testServerName)
	if err != nil {
		return &loomv1.TestMCPServerConnectionResponse{
			Success:   false,
			Error:     fmt.Sprintf("failed to get client: %v", err),
			LatencyMs: time.Since(startTime).Milliseconds(),
		}, nil
	}

	// List tools to verify connection
	tools, err := client.ListTools(testCtx)
	if err != nil {
		return &loomv1.TestMCPServerConnectionResponse{
			Success:   false,
			Error:     fmt.Sprintf("failed to list tools: %v", err),
			LatencyMs: time.Since(startTime).Milliseconds(),
		}, nil
	}

	// Note: ServerCapabilities are not currently exposed by the client
	// We just report successful connection and tool count

	latency := time.Since(startTime).Milliseconds()

	return &loomv1.TestMCPServerConnectionResponse{
		Success:   true,
		Message:   fmt.Sprintf("Successfully connected and discovered %d tools", len(tools)),
		ToolCount: int32(len(tools)),
		LatencyMs: latency,
	}, nil
}

// ListMCPServerTools lists all tools from a specific MCP server.
// This queries the MCP server directly through the manager, not the agent's tool registry.
func (s *MultiAgentServer) ListMCPServerTools(ctx context.Context, req *loomv1.ListMCPServerToolsRequest) (*loomv1.ListMCPServerToolsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.mcpManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "MCP manager not initialized")
	}

	if req.ServerName == "" {
		return nil, status.Error(codes.InvalidArgument, "server_name is required")
	}

	// Get MCP client for this server
	client, err := s.mcpManager.GetClient(req.ServerName)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "MCP server not found: %v", err)
	}

	// List tools from the MCP server
	tools, err := client.ListTools(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tools from MCP server: %v", err)
	}

	// Convert MCP tools to proto ToolDefinition
	protoTools := make([]*loomv1.ToolDefinition, len(tools))
	for i, tool := range tools {
		// Convert input schema to JSON
		schemaJSON := "{}"
		if tool.InputSchema != nil {
			if jsonBytes, err := json.Marshal(tool.InputSchema); err == nil {
				schemaJSON = string(jsonBytes)
			}
		}

		protoTools[i] = &loomv1.ToolDefinition{
			Name:            tool.Name,
			Description:     tool.Description,
			InputSchemaJson: schemaJSON,
			Backends:        []string{fmt.Sprintf("mcp:%s", req.ServerName)},
		}
	}

	return &loomv1.ListMCPServerToolsResponse{
		Tools:      protoTools,
		TotalCount: int32(len(protoTools)),
		ServerName: req.ServerName,
	}, nil
}

// Helper functions

// addMCPServerToConfig adds an MCP server to the config file.
func (s *MultiAgentServer) addMCPServerToConfig(req *loomv1.AddMCPServerRequest) error {
	v := viper.New()
	v.SetConfigFile(s.configPath)

	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	serverConfig := map[string]interface{}{
		"command":           req.Command,
		"args":              req.Args,
		"env":               req.Env,
		"transport":         req.Transport,
		"enabled":           req.Enabled,          // CRITICAL: Prevent server being disabled on restart
		"enable_sessions":   req.EnableSessions,   // For streamable-http transport
		"enable_resumption": req.EnableResumption, // For streamable-http transport
	}

	// Add URL if specified (required for http/sse/streamable-http transports)
	if req.Url != "" {
		serverConfig["url"] = req.Url
	}

	if req.WorkingDir != "" {
		serverConfig["working_dir"] = req.WorkingDir
	}

	// Add tool_filter if specified
	if req.ToolFilter != nil {
		filterMap := map[string]interface{}{
			"all": req.ToolFilter.All,
		}
		if len(req.ToolFilter.Include) > 0 {
			filterMap["include"] = req.ToolFilter.Include
		}
		if len(req.ToolFilter.Exclude) > 0 {
			filterMap["exclude"] = req.ToolFilter.Exclude
		}
		serverConfig["tool_filter"] = filterMap
	}

	v.Set(fmt.Sprintf("mcp.servers.%s", req.Name), serverConfig)

	if err := v.WriteConfig(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// updateMCPServerInConfig updates an MCP server in the config file.
func (s *MultiAgentServer) updateMCPServerInConfig(req *loomv1.UpdateMCPServerRequest) error {
	v := viper.New()
	v.SetConfigFile(s.configPath)

	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	serverConfig := map[string]interface{}{
		"command":           req.Command,
		"args":              req.Args,
		"env":               req.Env,
		"transport":         req.Transport,
		"enabled":           req.Enabled,          // CRITICAL: Prevent server being disabled on restart
		"enable_sessions":   req.EnableSessions,   // For streamable-http transport
		"enable_resumption": req.EnableResumption, // For streamable-http transport
	}

	// Add URL if specified (required for http/sse/streamable-http transports)
	if req.Url != "" {
		serverConfig["url"] = req.Url
	}

	if req.WorkingDir != "" {
		serverConfig["working_dir"] = req.WorkingDir
	}

	// Add tool_filter if specified
	if req.ToolFilter != nil {
		filterMap := map[string]interface{}{
			"all": req.ToolFilter.All,
		}
		if len(req.ToolFilter.Include) > 0 {
			filterMap["include"] = req.ToolFilter.Include
		}
		if len(req.ToolFilter.Exclude) > 0 {
			filterMap["exclude"] = req.ToolFilter.Exclude
		}
		serverConfig["tool_filter"] = filterMap
	}

	v.Set(fmt.Sprintf("mcp.servers.%s", req.ServerName), serverConfig)

	if err := v.WriteConfig(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// removeMCPServerFromConfig removes an MCP server from the config file.
func (s *MultiAgentServer) removeMCPServerFromConfig(serverName string) error {
	v := viper.New()
	v.SetConfigFile(s.configPath)

	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Get all servers
	servers := v.GetStringMap("mcp.servers")
	delete(servers, serverName)
	v.Set("mcp.servers", servers)

	if err := v.WriteConfig(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// determineStatus converts manager.ServerInfo to a status string.
func determineStatus(srv manager.ServerInfo) string {
	if srv.Connected {
		return "running"
	} else if srv.Enabled {
		return "stopped"
	}
	return "disabled"
}

// healthyToStatus converts a boolean health status to a string.
func healthyToStatus(healthy bool) string {
	if healthy {
		return "healthy"
	}
	return "unhealthy"
}
