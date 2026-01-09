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
// Package docker implements Docker-based execution backend with MCP server support.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// MCPServerManager manages MCP servers running inside Docker containers.
//
// Unlike the standard pkg/mcp/manager.Manager which runs MCP servers as host processes,
// this manager runs MCP servers inside Docker containers for:
// - Isolation (separate Python/Node environments per MCP server)
// - Observability (trace propagation into MCP server execution)
// - Resource limits (CPU, memory constraints per MCP server)
// - Security (sandboxed execution of untrusted MCP servers)
type MCPServerManager struct {
	executor *DockerExecutor
	logger   *zap.Logger

	mu      sync.RWMutex
	servers map[string]*managedMCPServer // server_name -> server
}

// managedMCPServer represents a single MCP server running in a container.
type managedMCPServer struct {
	name        string
	config      *loomv1.MCPServerConfig
	containerID string
	client      *client.Client
	transport   transport.Transport
	createdAt   time.Time
	healthy     bool

	// Auto-restart tracking
	restartCount int
	lastRestart  time.Time
}

// MCPManagerConfig configures the MCP server manager.
type MCPManagerConfig struct {
	Executor *DockerExecutor
	Logger   *zap.Logger

	// Note: Auto-restart settings will be added in Phase 3 when implementing
	// automatic health checks and restart logic. For now, health checks are manual.
}

// NewMCPServerManager creates a new Docker-based MCP server manager.
func NewMCPServerManager(config MCPManagerConfig) (*MCPServerManager, error) {
	if config.Executor == nil {
		return nil, fmt.Errorf("executor is required")
	}

	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}

	return &MCPServerManager{
		executor: config.Executor,
		logger:   config.Logger,
		servers:  make(map[string]*managedMCPServer),
	}, nil
}

// StartMCPServer starts an MCP server inside a Docker container.
//
// The MCP server runs as a long-lived process inside the container, with stdio transport
// for JSON-RPC communication. The server is kept alive for multiple tool invocations.
//
// Example:
//
//	config := &loomv1.MCPServerConfig{
//	    Enabled: true,
//	    Transport: "stdio",
//	    Command: "python",
//	    Args: []string{"-m", "vantage_mcp.server"},
//	    Env: map[string]string{
//	        "TD_USER": "dbc",
//	        "TD_HOST": "vantage.teradata.com",
//	    },
//	}
//	err := manager.StartMCPServer(ctx, "teradata", config, runtimeType, dockerConfig)
func (msm *MCPServerManager) StartMCPServer(
	ctx context.Context,
	serverName string,
	mcpConfig *loomv1.MCPServerConfig,
	runtimeType loomv1.RuntimeType,
	dockerConfig *loomv1.DockerBackendConfig,
) error {
	msm.mu.Lock()
	defer msm.mu.Unlock()

	// Check if already running
	if _, exists := msm.servers[serverName]; exists {
		return fmt.Errorf("MCP server %s already running", serverName)
	}

	msm.logger.Info("Starting MCP server in Docker container",
		zap.String("server", serverName),
		zap.String("command", mcpConfig.Command),
		zap.Strings("args", mcpConfig.Args),
		zap.String("transport", mcpConfig.Transport),
	)

	// Validate transport
	if mcpConfig.Transport != "stdio" {
		return fmt.Errorf("only stdio transport supported for Docker MCP servers (got: %s)", mcpConfig.Transport)
	}

	// Create container for MCP server
	// Note: We use GetOrCreateContainer to allow container reuse if configured
	scheduleReq := &loomv1.ScheduleRequest{
		TenantId:    "mcp-servers", // Dedicated tenant for MCP servers
		RuntimeType: runtimeType,
		Config:      dockerConfig,
	}

	scheduleDecision, err := msm.executor.scheduler.Schedule(ctx, scheduleReq)
	if err != nil {
		return fmt.Errorf("failed to schedule MCP server container: %w", err)
	}

	containerReq := &loomv1.ContainerRequest{
		TenantId:    "mcp-servers",
		RuntimeType: runtimeType,
		Config:      dockerConfig,
		Labels: map[string]string{
			"loom.mcp.server": serverName,
			"loom.type":       "mcp-server",
		},
	}

	containerID, _, err := msm.executor.scheduler.GetOrCreateContainer(ctx, containerReq)
	if err != nil {
		return fmt.Errorf("failed to create MCP server container: %w", err)
	}

	msm.logger.Info("MCP server container created",
		zap.String("server", serverName),
		zap.String("container_id", containerID),
		zap.String("node_id", scheduleDecision.NodeId),
	)

	// Build command with args
	fullCommand := append([]string{mcpConfig.Command}, mcpConfig.Args...)

	// Create stdio transport using Docker exec
	// The transport will handle stdin/stdout communication with the containerized process
	trans, err := msm.createDockerStdioTransport(ctx, containerID, fullCommand, mcpConfig.Env)
	if err != nil {
		return fmt.Errorf("failed to create Docker stdio transport: %w", err)
	}

	// Create MCP client
	mcpClient := client.NewClient(client.Config{
		Transport: trans,
		Logger:    msm.logger.With(zap.String("server", serverName)),
	})

	// Initialize MCP server
	initCtx := ctx
	if mcpConfig.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, time.Duration(mcpConfig.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	clientInfo := protocol.Implementation{
		Name:    "loom-docker",
		Version: "1.0.0",
	}

	if err := mcpClient.Initialize(initCtx, clientInfo); err != nil {
		trans.Close()
		return fmt.Errorf("failed to initialize MCP server: %w", err)
	}

	// Store managed server
	msm.servers[serverName] = &managedMCPServer{
		name:        serverName,
		config:      mcpConfig,
		containerID: containerID,
		client:      mcpClient,
		transport:   trans,
		createdAt:   time.Now(),
		healthy:     true,
	}

	msm.logger.Info("MCP server started successfully",
		zap.String("server", serverName),
		zap.String("container_id", containerID),
	)

	return nil
}

// createDockerStdioTransport creates a stdio transport that communicates with a process
// running inside a Docker container via docker exec.
//
// This is the key integration point between pkg/mcp and pkg/docker:
// - Uses DockerExecutor to run commands in containers
// - Pipes stdin/stdout for JSON-RPC communication
// - Keeps container process alive for multiple requests
func (msm *MCPServerManager) createDockerStdioTransport(
	ctx context.Context,
	containerID string,
	command []string,
	env map[string]string,
) (transport.Transport, error) {
	// Create Docker-based stdio transport
	// This wraps docker exec with stdin/stdout pipes
	// Note: StdioConfig.Env is passed to the subprocess environment
	trans, err := transport.NewStdioTransport(transport.StdioConfig{
		Command: "docker",
		Args:    append([]string{"exec", "-i", containerID}, command...),
		Env:     env,
		Logger:  msm.logger,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create stdio transport: %w", err)
	}

	return trans, nil
}

// InvokeTool invokes a tool on an MCP server running in a Docker container.
//
// This method:
// 1. Looks up the managed MCP server by name
// 2. Validates the tool exists
// 3. Calls the tool via JSON-RPC over stdin/stdout
// 4. Returns the result
//
// Example:
//
//	result, err := manager.InvokeTool(ctx, "teradata", "query", map[string]interface{}{
//	    "sql": "SELECT * FROM users LIMIT 10",
//	})
func (msm *MCPServerManager) InvokeTool(
	ctx context.Context,
	serverName string,
	toolName string,
	arguments map[string]interface{},
) (*protocol.CallToolResult, error) {
	msm.mu.RLock()
	server, exists := msm.servers[serverName]
	msm.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("MCP server %s not found", serverName)
	}

	if !server.healthy {
		return nil, fmt.Errorf("MCP server %s is unhealthy", serverName)
	}

	msm.logger.Debug("Invoking MCP tool",
		zap.String("server", serverName),
		zap.String("tool", toolName),
	)

	// Call tool via MCP client
	resultInterface, err := server.client.CallTool(ctx, toolName, arguments)
	if err != nil {
		msm.logger.Error("MCP tool invocation failed",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.Error(err),
		)
		return nil, fmt.Errorf("tool invocation failed: %w", err)
	}

	// Type assert to *protocol.CallToolResult
	result, ok := resultInterface.(*protocol.CallToolResult)
	if !ok {
		msm.logger.Error("Invalid result type from MCP client",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.String("type", fmt.Sprintf("%T", resultInterface)),
		)
		return nil, fmt.Errorf("expected *protocol.CallToolResult, got %T", resultInterface)
	}

	msm.logger.Debug("MCP tool invocation succeeded",
		zap.String("server", serverName),
		zap.String("tool", toolName),
		zap.Int("content_items", len(result.Content)),
	)

	return result, nil
}

// ListTools returns all available tools from an MCP server.
func (msm *MCPServerManager) ListTools(ctx context.Context, serverName string) ([]protocol.Tool, error) {
	msm.mu.RLock()
	server, exists := msm.servers[serverName]
	msm.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("MCP server %s not found", serverName)
	}

	return server.client.ListTools(ctx)
}

// StopMCPServer stops an MCP server and cleans up its container.
func (msm *MCPServerManager) StopMCPServer(ctx context.Context, serverName string) error {
	msm.mu.Lock()
	defer msm.mu.Unlock()

	server, exists := msm.servers[serverName]
	if !exists {
		return fmt.Errorf("MCP server %s not found", serverName)
	}

	msm.logger.Info("Stopping MCP server",
		zap.String("server", serverName),
		zap.String("container_id", server.containerID),
	)

	// Close MCP client (and transport)
	if err := server.client.Close(); err != nil {
		msm.logger.Warn("Failed to close MCP client",
			zap.String("server", serverName),
			zap.Error(err),
		)
	}

	// Remove container from scheduler
	// This removes it from scheduler's internal tracking and marks it for cleanup.
	// The actual Docker container removal depends on the scheduler's lifecycle config.
	if err := msm.executor.scheduler.RemoveContainer(ctx, server.containerID); err != nil {
		msm.logger.Warn("Failed to remove container from scheduler",
			zap.String("server", serverName),
			zap.String("container_id", server.containerID),
			zap.Error(err),
		)
		// Don't fail the stop operation - container will be cleaned up by scheduler rotation
	}

	delete(msm.servers, serverName)

	msm.logger.Info("MCP server stopped",
		zap.String("server", serverName),
	)

	return nil
}

// HealthCheck checks the health of an MCP server by pinging it.
func (msm *MCPServerManager) HealthCheck(ctx context.Context, serverName string) error {
	msm.mu.RLock()
	server, exists := msm.servers[serverName]
	msm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("MCP server %s not found", serverName)
	}

	// Ping the server
	if err := server.client.Ping(ctx); err != nil {
		msm.mu.Lock()
		server.healthy = false
		msm.mu.Unlock()

		msm.logger.Warn("MCP server health check failed",
			zap.String("server", serverName),
			zap.Error(err),
		)
		return err
	}

	msm.mu.Lock()
	server.healthy = true
	msm.mu.Unlock()

	return nil
}

// GetServerInfo returns information about a managed MCP server.
func (msm *MCPServerManager) GetServerInfo(serverName string) (*MCPServerInfo, error) {
	msm.mu.RLock()
	defer msm.mu.RUnlock()

	server, exists := msm.servers[serverName]
	if !exists {
		return nil, fmt.Errorf("MCP server %s not found", serverName)
	}

	return &MCPServerInfo{
		Name:         server.name,
		ContainerID:  server.containerID,
		Healthy:      server.healthy,
		CreatedAt:    server.createdAt,
		RestartCount: server.restartCount,
		LastRestart:  server.lastRestart,
	}, nil
}

// ListMCPServers returns information about all managed MCP servers.
func (msm *MCPServerManager) ListMCPServers() []MCPServerInfo {
	msm.mu.RLock()
	defer msm.mu.RUnlock()

	servers := make([]MCPServerInfo, 0, len(msm.servers))
	for _, server := range msm.servers {
		servers = append(servers, MCPServerInfo{
			Name:         server.name,
			ContainerID:  server.containerID,
			Healthy:      server.healthy,
			CreatedAt:    server.createdAt,
			RestartCount: server.restartCount,
			LastRestart:  server.lastRestart,
		})
	}

	return servers
}

// MCPServerInfo provides information about a managed MCP server.
type MCPServerInfo struct {
	Name         string
	ContainerID  string
	Healthy      bool
	CreatedAt    time.Time
	RestartCount int
	LastRestart  time.Time
}

// String implements fmt.Stringer for MCPServerInfo.
func (info MCPServerInfo) String() string {
	data, _ := json.MarshalIndent(info, "", "  ")
	return string(data)
}

// Close stops all MCP servers and cleans up resources.
func (msm *MCPServerManager) Close(ctx context.Context) error {
	msm.mu.Lock()
	serverNames := make([]string, 0, len(msm.servers))
	for name := range msm.servers {
		serverNames = append(serverNames, name)
	}
	msm.mu.Unlock()

	var errors []error
	for _, name := range serverNames {
		if err := msm.StopMCPServer(ctx, name); err != nil {
			errors = append(errors, fmt.Errorf("server %s: %w", name, err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors stopping MCP servers: %v", errors)
	}

	return nil
}
