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
// Package manager provides multi-server orchestration for MCP clients.
package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
	"go.uber.org/zap"
)

// Manager orchestrates multiple MCP server connections.
type Manager struct {
	config  Config
	logger  *zap.Logger
	clients map[string]*client.Client
	mu      sync.RWMutex
	started bool
}

// NewManager creates a new MCP manager.
func NewManager(config Config, logger *zap.Logger) (*Manager, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	return &Manager{
		config:  config,
		logger:  logger,
		clients: make(map[string]*client.Client),
	}, nil
}

// Start initializes connections to all enabled servers.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return fmt.Errorf("manager already started")
	}

	m.logger.Info("Starting MCP manager", zap.Int("server_count", len(m.config.Servers)))

	// Start each enabled server
	var startErrors []error
	for name, serverConfig := range m.config.Servers {
		if !serverConfig.Enabled {
			m.logger.Debug("Skipping disabled server", zap.String("server", name))
			continue
		}

		if err := m.startServer(ctx, name, serverConfig); err != nil {
			m.logger.Error("Failed to start server",
				zap.String("server", name),
				zap.Error(err))
			startErrors = append(startErrors, fmt.Errorf("server %s: %w", name, err))
		} else {
			m.logger.Info("Started server", zap.String("server", name))
		}
	}

	m.started = true

	// Return error if ALL servers failed
	if len(startErrors) > 0 && len(m.clients) == 0 {
		return fmt.Errorf("all servers failed to start: %v", startErrors)
	}

	// Log warnings for partial failures
	if len(startErrors) > 0 {
		m.logger.Warn("Some servers failed to start",
			zap.Int("failed", len(startErrors)),
			zap.Int("successful", len(m.clients)))
	}

	return nil
}

// startServer initializes a single MCP server connection.
func (m *Manager) startServer(ctx context.Context, name string, config ServerConfig) error {
	// Create transport
	var trans transport.Transport
	var err error

	switch config.Transport {
	case "stdio":
		trans, err = transport.NewStdioTransport(transport.StdioConfig{
			Command: config.Command,
			Args:    config.Args,
			Env:     config.Env,
			Logger:  m.logger.With(zap.String("server", name)),
		})
	case "http", "sse":
		// HTTP/SSE transport (sse is alias for backwards compatibility)
		trans, err = transport.NewHTTPTransport(transport.HTTPConfig{
			Endpoint: config.URL,
			Logger:   m.logger.With(zap.String("server", name)),
		})
	default:
		return fmt.Errorf("unsupported transport: %s (supported: stdio, http, sse)", config.Transport)
	}

	if err != nil {
		return fmt.Errorf("failed to create transport: %w", err)
	}

	// Create client
	mcpClient := client.NewClient(client.Config{
		Transport: trans,
		Logger:    m.logger.With(zap.String("server", name)),
	})

	// Initialize with timeout
	initCtx := ctx
	if config.Timeout != "" {
		timeout, err := time.ParseDuration(config.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout: %w", err)
		}
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	clientInfo := protocol.Implementation{
		Name:    m.config.ClientInfo.Name,
		Version: m.config.ClientInfo.Version,
	}

	if err := mcpClient.Initialize(initCtx, clientInfo); err != nil {
		trans.Close()
		return fmt.Errorf("failed to initialize: %w", err)
	}

	// Store client
	m.clients[name] = mcpClient

	return nil
}

// Stop closes all server connections.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return nil
	}

	m.logger.Info("Stopping MCP manager", zap.Int("server_count", len(m.clients)))

	var errors []error
	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			m.logger.Error("Failed to close client",
				zap.String("server", name),
				zap.Error(err))
			errors = append(errors, fmt.Errorf("server %s: %w", name, err))
		}
	}

	m.clients = make(map[string]*client.Client)
	m.started = false

	if len(errors) > 0 {
		return fmt.Errorf("errors closing clients: %v", errors)
	}

	return nil
}

// AddServer dynamically adds and starts a new MCP server.
func (m *Manager) AddServer(ctx context.Context, name string, config ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if server already exists
	if _, exists := m.clients[name]; exists {
		return fmt.Errorf("server %s already exists", name)
	}

	// Add to config
	if m.config.Servers == nil {
		m.config.Servers = make(map[string]ServerConfig)
	}
	m.config.Servers[name] = config

	// Start the server if enabled
	if config.Enabled {
		if err := m.startServer(ctx, name, config); err != nil {
			m.logger.Error("Failed to start new server",
				zap.String("server", name),
				zap.Error(err))
			return fmt.Errorf("failed to start server: %w", err)
		}
		m.logger.Info("Added and started server", zap.String("server", name))
	} else {
		m.logger.Info("Added server (disabled)", zap.String("server", name))
	}

	return nil
}

// StopServer stops a specific MCP server.
func (m *Manager) StopServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, exists := m.clients[name]
	if !exists {
		return fmt.Errorf("server not found: %s", name)
	}

	if err := client.Close(); err != nil {
		m.logger.Error("Failed to close server",
			zap.String("server", name),
			zap.Error(err))
		return fmt.Errorf("failed to close server: %w", err)
	}

	delete(m.clients, name)
	m.logger.Info("Stopped server", zap.String("server", name))

	return nil
}

// RemoveServer stops and completely removes a server from the manager.
// This removes the server from both the clients map and the config.
func (m *Manager) RemoveServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop the client if it's running
	if client, exists := m.clients[name]; exists {
		if err := client.Close(); err != nil {
			m.logger.Error("Failed to close server during removal",
				zap.String("server", name),
				zap.Error(err))
			// Continue with removal even if close fails
		}
		delete(m.clients, name)
	}

	// Remove from config
	delete(m.config.Servers, name)
	m.logger.Info("Removed server completely", zap.String("server", name))

	return nil
}

// GetClient returns a client by server name.
func (m *Manager) GetClient(serverName string) (*client.Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, exists := m.clients[serverName]
	if !exists {
		return nil, fmt.Errorf("server not found: %s", serverName)
	}

	return client, nil
}

// ServerNames returns a list of all active server names.
func (m *Manager) ServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// IsHealthy checks if a server is healthy by pinging it.
func (m *Manager) IsHealthy(ctx context.Context, serverName string) bool {
	client, err := m.GetClient(serverName)
	if err != nil {
		return false
	}

	// Ping the server
	if err := client.Ping(ctx); err != nil {
		m.logger.Warn("Server health check failed",
			zap.String("server", serverName),
			zap.Error(err))
		return false
	}

	return true
}

// HealthCheck checks the health of all servers.
func (m *Manager) HealthCheck(ctx context.Context) map[string]bool {
	m.mu.RLock()
	serverNames := make([]string, 0, len(m.clients))
	for name := range m.clients {
		serverNames = append(serverNames, name)
	}
	m.mu.RUnlock()

	results := make(map[string]bool)
	for _, name := range serverNames {
		results[name] = m.IsHealthy(ctx, name)
	}

	return results
}

// GetServerConfig returns the configuration for a server.
func (m *Manager) GetServerConfig(serverName string) (ServerConfig, error) {
	config, exists := m.config.Servers[serverName]
	if !exists {
		return ServerConfig{}, fmt.Errorf("server not found: %s", serverName)
	}
	return config, nil
}

// ListServers returns information about all servers.
func (m *Manager) ListServers() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info := make([]ServerInfo, 0, len(m.config.Servers))
	for name, config := range m.config.Servers {
		_, connected := m.clients[name]
		info = append(info, ServerInfo{
			Name:      name,
			Enabled:   config.Enabled,
			Connected: connected,
			Transport: config.Transport,
		})
	}

	return info
}

// ServerInfo provides information about a server.
type ServerInfo struct {
	Name      string
	Enabled   bool
	Connected bool
	Transport string
}
