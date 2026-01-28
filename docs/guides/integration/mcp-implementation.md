
# MCP Protocol Reference

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Protocol Layer](#protocol-layer)
- [Transport Layer](#transport-layer)
- [Core Features](#core-features)
- [Client API](#client-api)
- [Configuration Reference](#configuration-reference)


## Overview

Loom implements MCP (Model Context Protocol) 2024-11-05 specification for connecting to external tool servers.

**Key Capabilities:**
- JSON-RPC 2.0 foundation
- Tools, Resources, and Prompts support
- Multiple transport options (stdio, HTTP, WebSocket)
- Multi-server management


## Protocol Layer

### JSON-RPC 2.0 Messages

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "read_file",
    "arguments": {"path": "/data/file.txt"}
  }
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [{"type": "text", "text": "file contents..."}]
  }
}
```

### Supported Methods

| Method | Description |
|--------|-------------|
| `initialize` | Initialize client-server connection |
| `tools/list` | List available tools |
| `tools/call` | Execute a tool |
| `resources/list` | List available resources |
| `resources/read` | Read a resource |
| `prompts/list` | List available prompts |
| `prompts/get` | Get a prompt |
| `ping` | Health check |


## Transport Layer

### Stdio Transport (Default)

```yaml
mcp:
  servers:
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
      transport: stdio  # Default
```

Communication via stdin/stdout of spawned process.

### HTTP Transport

```yaml
mcp:
  servers:
    remote:
      url: http://localhost:8080/mcp
      transport: http
```

### WebSocket Transport

```yaml
mcp:
  servers:
    realtime:
      url: ws://localhost:8080/mcp
      transport: websocket
```


## Core Features

### Tools

Tools are functions the agent can call:

```go
// List tools from a server
tools, err := client.ListTools(ctx)
for _, tool := range tools {
    fmt.Printf("Tool: %s - %s\n", tool.Name, tool.Description)
}

// Call a tool
result, err := client.CallTool(ctx, "read_file", map[string]interface{}{
    "path": "/data/file.txt",
})
```

### Resources

Resources are data sources:

```go
// List resources
resources, err := client.ListResources(ctx)

// Read a resource
content, err := client.ReadResource(ctx, "file:///data/config.yaml")
```

### Prompts

Prompts are templates:

```go
// List prompts
prompts, err := client.ListPrompts(ctx)

// Get a prompt
prompt, err := client.GetPrompt(ctx, "sql_query", map[string]string{
    "table": "users",
})
```


## Client API

### Creating a Client

```go
import "github.com/teradata-labs/loom/pkg/mcp/client"

cfg := &client.Config{
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
    Transport: "stdio",
}

c, err := client.New(cfg, logger)
if err != nil {
    return err
}
defer c.Close()

// Initialize connection
info, err := c.Initialize(ctx)
```

### Using the Manager

```go
import "github.com/teradata-labs/loom/pkg/mcp/manager"

mgr := manager.NewManager(&manager.Config{
    Servers: map[string]manager.ServerConfig{
        "filesystem": {
            Command:   "npx",
            Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
            Enabled:   true,
            Transport: "stdio",
            ToolFilter: manager.ToolFilter{All: true},
        },
    },
}, logger)

// Start all servers
mgr.Start(ctx)
defer mgr.Stop()

// Get client for a server
client, err := mgr.GetClient("filesystem")

// List all tools from all servers
tools, err := mgr.ListAllTools(ctx)
```


## Configuration Reference

### Server Configuration

```yaml
mcp:
  servers:
    <server_name>:
      # Required
      command: string          # Executable path or command

      # Optional
      args: [string]           # Command arguments
      env:                     # Environment variables
        KEY: value
      transport: string        # "stdio" | "http" | "websocket"
      enabled: bool            # Enable/disable server
      timeout_seconds: int     # Request timeout (default: 30)

      # Tool filtering
      tool_filter:
        all: bool              # Register all tools
        include: [string]      # Whitelist specific tools
        exclude: [string]      # Blacklist specific tools
```

### Environment Variable Expansion

```yaml
mcp:
  servers:
    github:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: ${GITHUB_TOKEN}  # From environment
```

### Keyring Integration

Secrets can be stored in system keyring:

```bash
looms config set-key github_token
```

Referenced in config:
```yaml
env:
  # GITHUB_TOKEN loaded from keyring automatically
```
