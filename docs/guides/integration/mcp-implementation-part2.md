
# MCP Server Manager

**Version**: v1.2.0
**Status**: ✅ Implemented

## Table of Contents

- [Overview](#overview)
- [Creating a Manager](#creating-a-manager)
- [Server Lifecycle](#server-lifecycle)
- [Health Monitoring](#health-monitoring)
- [Tool Registration](#tool-registration)
- [Server Inspection](#server-inspection)
- [Error Handling](#error-handling)


## Overview

The MCP Manager orchestrates multiple MCP server connections, handling startup, health monitoring, and tool registration.


## Creating a Manager

```go
import "github.com/teradata-labs/loom/pkg/mcp/manager"

cfg := manager.Config{
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

mgr, err := manager.NewManager(cfg, logger)
if err != nil {
    log.Fatalf("invalid config: %v", err)
}
```


## Server Lifecycle

### Starting Servers

```go
// Start all enabled servers.
// Returns an error only if ALL servers fail to start.
// Partial failures are logged but do not return an error.
err := mgr.Start(ctx)
if err != nil {
    log.Fatalf("All servers failed to start: %v", err)
}

// Check which servers are running
for _, info := range mgr.ListServers() {
    log.Printf("Server %s: enabled=%t connected=%t transport=%s",
        info.Name, info.Enabled, info.Connected, info.Transport)
}
```

### Stopping Servers

```go
// Stop all servers gracefully
if err := mgr.Stop(); err != nil {
    log.Printf("Errors stopping servers: %v", err)
}

// Stop a specific server (keeps it in config)
if err := mgr.StopServer("filesystem"); err != nil {
    log.Printf("Failed to stop server: %v", err)
}
```

### Adding a Server Dynamically

```go
// Add and start a new server at runtime
err := mgr.AddServer(ctx, "new-server", manager.ServerConfig{
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
    Enabled:   true,
    Transport: "stdio",
    ToolFilter: manager.ToolFilter{All: true},
})
if err != nil {
    log.Printf("Failed to add server: %v", err)
}
```


## Health Monitoring

The manager provides health checking by pinging each connected server:

```go
// Check health of all connected servers (pings each one)
health := mgr.HealthCheck(ctx)
for server, healthy := range health {
    fmt.Printf("%s: healthy=%t\n", server, healthy)
}

// Check if a specific server is healthy
if mgr.IsHealthy(ctx, "filesystem") {
    // Safe to use
}
```


## Tool Registration

### Access Tools via Client

```go
// Get a specific server's client
c, err := mgr.GetClient("filesystem")
if err != nil {
    log.Fatalf("server not found: %v", err)
}

// List tools from that server
tools, err := c.ListTools(ctx)
if err != nil {
    log.Fatalf("failed to list tools: %v", err)
}
for _, tool := range tools {
    fmt.Printf("%s: %s\n", tool.Name, tool.Description)
}
```

### Tool Filtering

```go
// Whitelist specific tools
cfg.Servers["filesystem"] = manager.ServerConfig{
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
    Enabled:   true,
    Transport: "stdio",
    ToolFilter: manager.ToolFilter{
        Include: []string{"read_file", "list_directory"},
    },
}

// Blacklist dangerous tools
cfg.Servers["filesystem"] = manager.ServerConfig{
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
    Enabled:   true,
    Transport: "stdio",
    ToolFilter: manager.ToolFilter{
        All:     true,
        Exclude: []string{"delete_file", "write_file"},
    },
}
```


## Server Inspection

### List Active Server Names

```go
// ServerNames returns names of all connected (active) servers
names := mgr.ServerNames()
for _, name := range names {
    fmt.Println(name)
}
```

### Get Server Configuration

```go
// GetServerConfig returns the configuration for a named server
cfg, err := mgr.GetServerConfig("filesystem")
if err != nil {
    log.Printf("Server not found: %v", err)
} else {
    fmt.Printf("Transport: %s, Enabled: %t\n", cfg.Transport, cfg.Enabled)
}
```


## Error Handling

### Connection Errors

Errors from `GetClient` and other manager methods are plain `fmt.Errorf` values.
Check the error message for context:

```go
client, err := mgr.GetClient("filesystem")
if err != nil {
    // err message is "server not found: filesystem" if the server
    // is not configured or not connected
    log.Printf("Cannot get client: %v", err)
}
```

### Stopping and Removing Servers

```go
// Stop a specific server (keeps it in config)
err := mgr.StopServer("filesystem")
if err != nil {
    log.Printf("Failed to stop: %v", err)
}

// Remove a server completely (stops it and removes from config)
err = mgr.RemoveServer("filesystem")
if err != nil {
    log.Printf("Failed to remove: %v", err)
}
```
