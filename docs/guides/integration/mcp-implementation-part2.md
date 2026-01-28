
# MCP Server Manager

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Creating a Manager](#creating-a-manager)
- [Server Lifecycle](#server-lifecycle)
- [Health Monitoring](#health-monitoring)
- [Tool Registration](#tool-registration)
- [Error Handling](#error-handling)


## Overview

The MCP Manager orchestrates multiple MCP server connections, handling startup, health monitoring, and tool registration.


## Creating a Manager

```go
import "github.com/teradata-labs/loom/pkg/mcp/manager"

cfg := &manager.Config{
    Servers: map[string]manager.ServerConfig{
        "filesystem": {
            Command:   "npx",
            Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
            Enabled:   true,
            Transport: "stdio",
            ToolFilter: manager.ToolFilter{All: true},
        },
        "github": {
            Command:   "npx",
            Args:      []string{"-y", "@modelcontextprotocol/server-github"},
            Env:       map[string]string{"GITHUB_TOKEN": os.Getenv("GITHUB_TOKEN")},
            Enabled:   true,
            Transport: "stdio",
            ToolFilter: manager.ToolFilter{All: true},
        },
    },
}

mgr := manager.NewManager(cfg, logger)
```


## Server Lifecycle

### Starting Servers

```go
// Start all enabled servers
err := mgr.Start(ctx)
if err != nil {
    log.Printf("Some servers failed to start: %v", err)
}

// Check which servers are running
for name, status := range mgr.GetServerStatus() {
    log.Printf("Server %s: %s", name, status)
}
```

### Stopping Servers

```go
// Stop all servers gracefully
mgr.Stop()

// Stop a specific server
mgr.StopServer("filesystem")
```

### Restarting a Server

```go
// Restart a server (useful after errors)
err := mgr.RestartServer(ctx, "filesystem")
```


## Health Monitoring

The manager automatically monitors server health:

```go
// Get health status
health := mgr.GetHealth()
for server, status := range health {
    fmt.Printf("%s: healthy=%t, lastPing=%v\n",
        server, status.Healthy, status.LastPing)
}

// Check if a specific server is healthy
if mgr.IsHealthy("filesystem") {
    // Safe to use
}
```

### Health Check Configuration

```yaml
mcp:
  health_check:
    enabled: true
    interval_seconds: 30
    timeout_seconds: 5

  servers:
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
```


## Tool Registration

### Register All Tools from a Server

```go
// Get all tools from all servers
allTools, err := mgr.ListAllTools(ctx)
for server, tools := range allTools {
    for _, tool := range tools {
        fmt.Printf("[%s] %s: %s\n", server, tool.Name, tool.Description)
    }
}

// Register with an agent
agent.RegisterMCPServer(ctx, mgr, "filesystem")
```

### Tool Filtering

```go
// Whitelist specific tools
cfg.Servers["filesystem"] = manager.ServerConfig{
    Command: "npx",
    Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
    ToolFilter: manager.ToolFilter{
        Include: []string{"read_file", "list_directory"},
    },
}

// Blacklist dangerous tools
cfg.Servers["filesystem"] = manager.ServerConfig{
    Command: "npx",
    Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
    ToolFilter: manager.ToolFilter{
        All:     true,
        Exclude: []string{"delete_file", "write_file"},
    },
}
```


## Error Handling

### Connection Errors

```go
client, err := mgr.GetClient("filesystem")
if err != nil {
    if errors.Is(err, manager.ErrServerNotFound) {
        // Server not configured
    } else if errors.Is(err, manager.ErrServerNotRunning) {
        // Server crashed or not started
    }
}
```

### Automatic Restart

Servers that crash are automatically restarted:

```yaml
mcp:
  auto_restart:
    enabled: true
    max_restarts: 3
    restart_delay_seconds: 5

  servers:
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
```

### Circuit Breaker

Prevent repeated failures:

```go
// Circuit breaker trips after 5 failures in 1 minute
// Opens for 30 seconds before trying again
cfg := &manager.Config{
    CircuitBreaker: manager.CircuitBreakerConfig{
        Enabled:     true,
        Threshold:   5,
        Window:      time.Minute,
        OpenTimeout: 30 * time.Second,
    },
}
```
