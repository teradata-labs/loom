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
package manager

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNewManager(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"test": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
			},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, nil)
	require.NoError(t, err)
	require.NotNil(t, mgr)
	assert.NotNil(t, mgr.logger)
	assert.NotNil(t, mgr.clients)
	assert.False(t, mgr.started)
}

func TestNewManager_InvalidConfig(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{},
	}

	mgr, err := NewManager(config, nil)
	require.Error(t, err)
	assert.Nil(t, mgr)
	assert.Contains(t, err.Error(), "invalid config")
}

func TestManager_ListServers(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
			},
			"github": {
				Enabled:   false,
				Transport: "stdio",
				Command:   "npx",
			},
			"postgres": {
				Enabled:   true,
				Transport: "http",
				URL:       "http://localhost:8080",
			},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	servers := mgr.ListServers()
	require.Len(t, servers, 3)

	// Find each server
	var fs, gh, pg *ServerInfo
	for i := range servers {
		switch servers[i].Name {
		case "filesystem":
			fs = &servers[i]
		case "github":
			gh = &servers[i]
		case "postgres":
			pg = &servers[i]
		}
	}

	require.NotNil(t, fs)
	assert.True(t, fs.Enabled)
	assert.False(t, fs.Connected)
	assert.Equal(t, "stdio", fs.Transport)

	require.NotNil(t, gh)
	assert.False(t, gh.Enabled)
	assert.False(t, gh.Connected)

	require.NotNil(t, pg)
	assert.True(t, pg.Enabled)
	assert.False(t, pg.Connected)
	assert.Equal(t, "http", pg.Transport)
}

func TestManager_ServerNames(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"server1": {Enabled: true, Transport: "stdio", Command: "echo"},
			"server2": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	names := mgr.ServerNames()
	assert.Empty(t, names) // No servers started yet

	// Note: We can't actually start servers in unit tests without real MCP servers
	// Integration tests will cover Start/Stop
}

func TestManager_GetClient_NotStarted(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"test": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	client, err := mgr.GetClient("test")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "server not found")
}

func TestManager_GetClient_NonExistent(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"test": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	client, err := mgr.GetClient("nonexistent")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "server not found")
}

func TestManager_GetServerConfig(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem"},
				ToolFilter: ToolFilter{
					Include: []string{"read_file", "write_file"},
				},
			},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	serverConfig, err := mgr.GetServerConfig("filesystem")
	require.NoError(t, err)
	assert.True(t, serverConfig.Enabled)
	assert.Equal(t, "stdio", serverConfig.Transport)
	assert.Equal(t, "npx", serverConfig.Command)
	assert.Len(t, serverConfig.ToolFilter.Include, 2)

	_, err = mgr.GetServerConfig("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server not found")
}

func TestManager_Stop_NotStarted(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"test": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	err = mgr.Stop()
	assert.NoError(t, err) // Should be idempotent
}

func TestManager_Start_AlreadyStarted(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"test": {Enabled: true, Transport: "stdio", Command: "echo"},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	// Manually mark as started
	mgr.mu.Lock()
	mgr.started = true
	mgr.mu.Unlock()

	ctx := context.Background()
	err = mgr.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

// Integration test - requires npx
func skipIfNoNPX(t *testing.T) {
	if _, err := os.Stat("/usr/local/bin/npx"); os.IsNotExist(err) {
		if _, err := os.Stat("/usr/bin/npx"); os.IsNotExist(err) {
			t.Skip("npx not found - skipping integration test")
		}
	}
}

func TestManager_Integration_Lifecycle(t *testing.T) {
	skipIfNoNPX(t)

	config := Config{
		Servers: map[string]ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", os.TempDir()},
				Timeout:   "30s",
			},
		},
		ClientInfo: ClientInfo{
			Name:    "loom-test",
			Version: "0.1.0",
		},
	}

	logger := zap.NewNop()
	mgr, err := NewManager(config, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Start manager
	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop() }()

	// Verify server is connected
	names := mgr.ServerNames()
	assert.Contains(t, names, "filesystem")

	// Get client
	client, err := mgr.GetClient("filesystem")
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.True(t, client.IsInitialized())

	// Health check
	healthy := mgr.IsHealthy(ctx, "filesystem")
	assert.True(t, healthy)

	// Health check all
	health := mgr.HealthCheck(ctx)
	assert.Contains(t, health, "filesystem")
	assert.True(t, health["filesystem"])

	// Stop manager
	err = mgr.Stop()
	require.NoError(t, err)

	// Verify stopped
	assert.Empty(t, mgr.ServerNames())
}

func TestManager_Integration_PartialFailure(t *testing.T) {
	skipIfNoNPX(t)

	config := Config{
		Servers: map[string]ServerConfig{
			"filesystem": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", os.TempDir()},
				Timeout:   "30s",
			},
			"invalid": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "nonexistent-command-12345",
				Timeout:   "5s",
			},
		},
		ClientInfo: ClientInfo{
			Name:    "loom-test",
			Version: "0.1.0",
		},
	}

	logger := zap.NewNop()
	mgr, err := NewManager(config, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Start should succeed even if one server fails
	err = mgr.Start(ctx)
	require.NoError(t, err) // Partial failure is OK
	defer func() { _ = mgr.Stop() }()

	// Filesystem should be connected
	names := mgr.ServerNames()
	assert.Contains(t, names, "filesystem")
	assert.NotContains(t, names, "invalid") // Failed server not in list

	// Verify filesystem is healthy
	healthy := mgr.IsHealthy(ctx, "filesystem")
	assert.True(t, healthy)

	// Invalid server health check should return false
	healthy = mgr.IsHealthy(ctx, "invalid")
	assert.False(t, healthy)
}

func TestManager_Integration_DisabledServers(t *testing.T) {
	config := Config{
		Servers: map[string]ServerConfig{
			"enabled": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "echo",
			},
			"disabled": {
				Enabled:   false,
				Transport: "stdio",
				Command:   "npx",
			},
		},
		ClientInfo: ClientInfo{Name: "test", Version: "0.1.0"},
	}

	mgr, err := NewManager(config, zap.NewNop())
	require.NoError(t, err)

	servers := mgr.ListServers()
	require.Len(t, servers, 2)

	// Both servers should be in list
	var enabled, disabled *ServerInfo
	for i := range servers {
		if servers[i].Name == "enabled" {
			enabled = &servers[i]
		} else if servers[i].Name == "disabled" {
			disabled = &servers[i]
		}
	}

	require.NotNil(t, enabled)
	assert.True(t, enabled.Enabled)

	require.NotNil(t, disabled)
	assert.False(t, disabled.Enabled)
}

func TestManager_Integration_MultipleServers(t *testing.T) {
	skipIfNoNPX(t)

	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	config := Config{
		Servers: map[string]ServerConfig{
			"fs1": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", tmpDir1},
				Timeout:   "30s",
			},
			"fs2": {
				Enabled:   true,
				Transport: "stdio",
				Command:   "npx",
				Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", tmpDir2},
				Timeout:   "30s",
			},
		},
		ClientInfo: ClientInfo{
			Name:    "loom-test",
			Version: "0.1.0",
		},
	}

	logger := zap.NewNop()
	mgr, err := NewManager(config, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = mgr.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop() }()

	names := mgr.ServerNames()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "fs1")
	assert.Contains(t, names, "fs2")

	// Both should be healthy
	health := mgr.HealthCheck(ctx)
	assert.True(t, health["fs1"])
	assert.True(t, health["fs2"])
}
