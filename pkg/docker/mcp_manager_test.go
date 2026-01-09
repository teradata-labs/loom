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
package docker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap/zaptest"
)

// TestMCPServerManager_StartStop tests basic MCP server lifecycle.
//
// This test requires a Docker daemon running.
func TestMCPServerManager_StartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor
	executor := &DockerExecutor{
		dockerClient: scheduler.dockerClient,
		scheduler:    scheduler,
		logger:       logger,
	}

	// Create MCP manager
	manager, err := NewMCPServerManager(MCPManagerConfig{
		Executor: executor,
		Logger:   logger,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	// Test: Start echo MCP server (simple Python server that echoes input)
	mcpConfig := &loomv1.MCPServerConfig{
		Enabled:   true,
		Transport: "stdio",
		Command:   "python3",
		Args: []string{"-c", `
import sys
import json

# Read initialize request
line = sys.stdin.readline()
req = json.loads(line)
print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": {"protocolVersion": "1.0", "serverInfo": {"name": "echo", "version": "1.0"}}}))
sys.stdout.flush()

# Echo loop
while True:
    line = sys.stdin.readline()
    if not line:
        break
    sys.stdout.write(line)
    sys.stdout.flush()
`},
		TimeoutSeconds: 10,
	}

	dockerConfig := &loomv1.DockerBackendConfig{
		Name:        "mcp-echo-server",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
	}

	// Start MCP server
	err = manager.StartMCPServer(ctx, "echo", mcpConfig, loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig)
	assert.NoError(t, err)

	// Verify server is listed
	servers := manager.ListMCPServers()
	assert.Len(t, servers, 1)
	assert.Equal(t, "echo", servers[0].Name)
	assert.True(t, servers[0].Healthy)

	// Get server info
	info, err := manager.GetServerInfo("echo")
	assert.NoError(t, err)
	assert.Equal(t, "echo", info.Name)
	assert.NotEmpty(t, info.ContainerID)

	// Stop MCP server
	err = manager.StopMCPServer(ctx, "echo")
	assert.NoError(t, err)

	// Verify server is removed
	servers = manager.ListMCPServers()
	assert.Len(t, servers, 0)
}

// TestMCPServerManager_HealthCheck tests MCP server health checking.
func TestMCPServerManager_HealthCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor
	executor := &DockerExecutor{
		dockerClient: scheduler.dockerClient,
		scheduler:    scheduler,
		logger:       logger,
	}

	// Create MCP manager
	manager, err := NewMCPServerManager(MCPManagerConfig{
		Executor: executor,
		Logger:   logger,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	// Start echo MCP server
	mcpConfig := &loomv1.MCPServerConfig{
		Enabled:   true,
		Transport: "stdio",
		Command:   "python3",
		Args: []string{"-c", `
import sys
import json

# Initialize
line = sys.stdin.readline()
req = json.loads(line)
print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": {"protocolVersion": "1.0", "serverInfo": {"name": "echo", "version": "1.0"}}}))
sys.stdout.flush()

# Ping loop
while True:
    line = sys.stdin.readline()
    if not line:
        break
    req = json.loads(line)
    if req.get("method") == "ping":
        print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": {}}))
        sys.stdout.flush()
`},
		TimeoutSeconds: 10,
	}

	dockerConfig := &loomv1.DockerBackendConfig{
		Name:        "mcp-echo-server",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
	}

	err = manager.StartMCPServer(ctx, "echo", mcpConfig, loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig)
	require.NoError(t, err)

	// Health check should succeed
	err = manager.HealthCheck(ctx, "echo")
	assert.NoError(t, err)

	// Verify healthy status
	info, err := manager.GetServerInfo("echo")
	assert.NoError(t, err)
	assert.True(t, info.Healthy)
}

// TestMCPServerManager_MultipleServers tests managing multiple MCP servers.
func TestMCPServerManager_MultipleServers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor
	executor := &DockerExecutor{
		dockerClient: scheduler.dockerClient,
		scheduler:    scheduler,
		logger:       logger,
	}

	// Create MCP manager
	manager, err := NewMCPServerManager(MCPManagerConfig{
		Executor: executor,
		Logger:   logger,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	// Simple echo server script (reusable)
	echoScript := `
import sys
import json

line = sys.stdin.readline()
req = json.loads(line)
print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": {"protocolVersion": "1.0", "serverInfo": {"name": "echo", "version": "1.0"}}}))
sys.stdout.flush()

while True:
    line = sys.stdin.readline()
    if not line:
        break
    sys.stdout.write(line)
    sys.stdout.flush()
`

	// Start server 1
	mcpConfig1 := &loomv1.MCPServerConfig{
		Enabled:        true,
		Transport:      "stdio",
		Command:        "python3",
		Args:           []string{"-c", echoScript},
		TimeoutSeconds: 10,
	}
	dockerConfig1 := &loomv1.DockerBackendConfig{
		Name:        "mcp-server-1",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
	}
	err = manager.StartMCPServer(ctx, "server1", mcpConfig1, loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig1)
	require.NoError(t, err)

	// Start server 2
	mcpConfig2 := &loomv1.MCPServerConfig{
		Enabled:        true,
		Transport:      "stdio",
		Command:        "python3",
		Args:           []string{"-c", echoScript},
		TimeoutSeconds: 10,
	}
	dockerConfig2 := &loomv1.DockerBackendConfig{
		Name:        "mcp-server-2",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
	}
	err = manager.StartMCPServer(ctx, "server2", mcpConfig2, loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig2)
	require.NoError(t, err)

	// Verify both servers are listed
	servers := manager.ListMCPServers()
	assert.Len(t, servers, 2)

	// Verify individual server info
	info1, err := manager.GetServerInfo("server1")
	assert.NoError(t, err)
	assert.Equal(t, "server1", info1.Name)

	info2, err := manager.GetServerInfo("server2")
	assert.NoError(t, err)
	assert.Equal(t, "server2", info2.Name)

	// Containers should be different
	assert.NotEqual(t, info1.ContainerID, info2.ContainerID)

	// Stop server 1
	err = manager.StopMCPServer(ctx, "server1")
	assert.NoError(t, err)

	// Verify only server2 remains
	servers = manager.ListMCPServers()
	assert.Len(t, servers, 1)
	assert.Equal(t, "server2", servers[0].Name)

	// Stop server 2
	err = manager.StopMCPServer(ctx, "server2")
	assert.NoError(t, err)

	// Verify no servers remain
	servers = manager.ListMCPServers()
	assert.Len(t, servers, 0)
}

// TestMCPServerManager_InvalidTransport tests error handling for unsupported transports.
func TestMCPServerManager_InvalidTransport(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor
	executor := &DockerExecutor{
		dockerClient: scheduler.dockerClient,
		scheduler:    scheduler,
		logger:       logger,
	}

	// Create MCP manager
	manager, err := NewMCPServerManager(MCPManagerConfig{
		Executor: executor,
		Logger:   logger,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	// Try to start server with HTTP transport (not supported for Docker)
	mcpConfig := &loomv1.MCPServerConfig{
		Enabled:   true,
		Transport: "http", // Invalid for Docker MCP servers
		Command:   "python3",
		Args:      []string{"-m", "http.server"},
	}
	dockerConfig := &loomv1.DockerBackendConfig{
		Name:        "mcp-http-server",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
	}

	// Should fail with unsupported transport error
	err = manager.StartMCPServer(ctx, "http-server", mcpConfig, loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "only stdio transport supported")
}

// TestMCPServerManager_DuplicateServer tests error handling when starting duplicate servers.
func TestMCPServerManager_DuplicateServer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	ctx := context.Background()
	logger := zaptest.NewLogger(t)

	// Create scheduler
	scheduler, err := NewLocalScheduler(ctx, LocalSchedulerConfig{
		Logger: logger,
	})
	require.NoError(t, err)
	defer scheduler.Close()

	// Create executor
	executor := &DockerExecutor{
		dockerClient: scheduler.dockerClient,
		scheduler:    scheduler,
		logger:       logger,
	}

	// Create MCP manager
	manager, err := NewMCPServerManager(MCPManagerConfig{
		Executor: executor,
		Logger:   logger,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	// Start server
	mcpConfig := &loomv1.MCPServerConfig{
		Enabled:   true,
		Transport: "stdio",
		Command:   "python3",
		Args: []string{"-c", `
import sys
import json
line = sys.stdin.readline()
req = json.loads(line)
print(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": {"protocolVersion": "1.0", "serverInfo": {"name": "echo", "version": "1.0"}}}))
sys.stdout.flush()
`},
		TimeoutSeconds: 10,
	}
	dockerConfig := &loomv1.DockerBackendConfig{
		Name:        "mcp-echo-server",
		RuntimeType: loomv1.RuntimeType_RUNTIME_TYPE_PYTHON,
		ImageSource: &loomv1.DockerBackendConfig_BaseImage{
			BaseImage: "python:3.11-slim",
		},
	}

	err = manager.StartMCPServer(ctx, "echo", mcpConfig, loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig)
	require.NoError(t, err)

	// Try to start same server again (should fail)
	err = manager.StartMCPServer(ctx, "echo", mcpConfig, loomv1.RuntimeType_RUNTIME_TYPE_PYTHON, dockerConfig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	// Verify only one server exists
	servers := manager.ListMCPServers()
	assert.Len(t, servers, 1)
}

// TestMCPServerManager_GetServerInfo_NotFound tests error handling for non-existent servers.
func TestMCPServerManager_GetServerInfo_NotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create minimal manager (no Docker needed for this test)
	manager := &MCPServerManager{
		logger:  logger,
		servers: make(map[string]*managedMCPServer),
	}

	// Get info for non-existent server
	_, err := manager.GetServerInfo("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
