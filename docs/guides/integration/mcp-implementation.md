
# MCP Protocol Reference

**Version**: v1.2.0

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
- ✅ JSON-RPC 2.0 foundation
- ✅ Tools, Resources, and Prompts support
- ✅ Multiple transport options (stdio, HTTP/SSE, streamable-http)
- ✅ Multi-server management via `Manager`
- ✅ Dynamic server add/remove at runtime
- ✅ Health checking via `Ping`
- ✅ Sampling (server-initiated LLM requests)
- ✅ Resource subscriptions


## Protocol Layer ✅

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

| Method | Direction | Description |
|--------|-----------|-------------|
| `initialize` | Client -> Server | Initialize client-server connection |
| `notifications/initialized` | Client -> Server | Signal handshake complete (notification, no response) |
| `tools/list` | Client -> Server | List available tools |
| `tools/call` | Client -> Server | Execute a tool |
| `resources/list` | Client -> Server | List available resources |
| `resources/read` | Client -> Server | Read a resource |
| `resources/subscribe` | Client -> Server | Subscribe to resource changes |
| `resources/unsubscribe` | Client -> Server | Unsubscribe from resource changes |
| `prompts/list` | Client -> Server | List available prompts |
| `prompts/get` | Client -> Server | Get a prompt |
| `ping` | Client -> Server | Health check |
| `sampling/createMessage` | Server -> Client | Server requests LLM completion from client |


## Transport Layer

### Stdio Transport (Default) ✅

```yaml
mcp:
  servers:
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
      transport: stdio  # Default
```

Communication via stdin/stdout of spawned process.

### HTTP/SSE Transport (Legacy) ✅

```yaml
mcp:
  servers:
    remote:
      url: http://localhost:8080/mcp
      transport: http  # also accepts "sse" (both map to the same HTTP/SSE transport)
```

> **Note:** The `http` and `sse` transport values are interchangeable and both
> use the legacy HTTP/SSE transport. For new deployments, prefer `streamable-http`.

### Streamable HTTP Transport (MCP 2025-03-26 spec) ✅

```yaml
mcp:
  servers:
    remote:
      url: http://localhost:8080/mcp
      transport: streamable-http
      enable_sessions: true
      enable_resumption: false
```


## Core Features ✅

### Tools ✅

Tools are functions the agent can call:

```go
// List tools from a server
tools, err := client.ListTools(ctx)
for _, tool := range tools {
    fmt.Printf("Tool: %s - %s\n", tool.Name, tool.Description)
}

// Call a tool (returns interface{} to avoid import cycles; actual type is *protocol.CallToolResult)
raw, err := client.CallTool(ctx, "read_file", map[string]interface{}{
    "path": "/data/file.txt",
})
result := raw.(*protocol.CallToolResult)
for _, c := range result.Content {
    fmt.Println(c.Text)
}
```

### Resources ✅

Resources are data sources:

```go
// List resources
resources, err := client.ListResources(ctx)

// Read a resource (returns *protocol.ReadResourceResult)
result, err := client.ReadResource(ctx, "file:///data/config.yaml")
for _, c := range result.Contents {
    fmt.Println(c.Text)
}

// Subscribe/unsubscribe to resource changes (if server supports it)
err = client.SubscribeResource(ctx, "file:///data/config.yaml")
err = client.UnsubscribeResource(ctx, "file:///data/config.yaml")
```

### Prompts ✅

Prompts are templates:

```go
// List prompts
prompts, err := client.ListPrompts(ctx)

// Get a prompt (returns *protocol.GetPromptResult)
result, err := client.GetPrompt(ctx, "sql_query", map[string]interface{}{
    "table": "users",
})
for _, msg := range result.Messages {
    fmt.Printf("[%s] %v\n", msg.Role, msg.Content)
}
```


## Client API ✅

### Creating a Client

```go
import (
    "github.com/teradata-labs/loom/pkg/mcp/client"
    "github.com/teradata-labs/loom/pkg/mcp/protocol"
    "github.com/teradata-labs/loom/pkg/mcp/transport"
)

// Create a transport first
trans, err := transport.NewStdioTransport(transport.StdioConfig{
    Command: "npx",
    Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
    Logger:  logger,
})
if err != nil {
    return err
}

// Create client with transport
c := client.NewClient(client.Config{
    Transport: trans,
    Logger:    logger,
})
defer c.Close()

// Initialize connection (MCP handshake)
err = c.Initialize(ctx, protocol.Implementation{
    Name:    "my-app",
    Version: "1.0.0",
})
```

### Using the Manager

```go
import "github.com/teradata-labs/loom/pkg/mcp/manager"

mgr, err := manager.NewManager(manager.Config{
    Servers: map[string]manager.ServerConfig{
        "filesystem": {
            Command:   "npx",
            Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
            Enabled:   true,
            Transport: "stdio",
            ToolFilter: manager.ToolFilter{All: true},
        },
    },
}, logger) // logger is *zap.Logger; pass nil for no-op logging
if err != nil {
    log.Fatalf("invalid config: %v", err)
}

// Start all enabled servers (partial failures are warnings, not fatal)
if err := mgr.Start(ctx); err != nil {
    log.Printf("All servers failed: %v", err)
}
defer mgr.Stop()

// Get client for a specific server
client, err := mgr.GetClient("filesystem")

// List all servers (returns []manager.ServerInfo with Name, Enabled, Connected, Transport)
servers := mgr.ListServers()
for _, s := range servers {
    fmt.Printf("Server: %s connected=%v transport=%s\n", s.Name, s.Connected, s.Transport)
}

// Dynamically add a new server at runtime
err = mgr.AddServer(ctx, "github", manager.ServerConfig{
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-github"},
    Enabled:   true,
    Transport: "stdio",
    ToolFilter: manager.ToolFilter{All: true},
})

// Health check a server (sends MCP ping)
healthy := mgr.IsHealthy(ctx, "filesystem")

// Health check all servers
health := mgr.HealthCheck(ctx) // map[string]bool

// Stop and remove a server
err = mgr.RemoveServer("github")
```


## Configuration Reference ✅

### Server Configuration

```yaml
mcp:
  servers:
    <server_name>:
      # Required (for stdio transport)
      command: string          # Executable path or command

      # Optional
      args: [string]           # Command arguments
      env:                     # Environment variables
        KEY: value
      transport: string        # "stdio" | "http" | "sse" | "streamable-http"
      enabled: bool            # Enable/disable server
      timeout: string          # Operation timeout (e.g., "30s", "1m")
      url: string              # Server URL (required for http/sse/streamable-http)
      enable_sessions: bool    # Enable session management (streamable-http only)
      enable_resumption: bool  # Enable stream resumption (streamable-http only)

      # Tool filtering
      tools:
        all: bool              # Register all tools
        include: [string]      # Whitelist specific tools
        exclude: [string]      # Blacklist specific tools
```

> **Note:** The `timeout` and `tools` filter fields are parsed from config but
> are only honored when using the `manager.Manager` API
> (`RegisterMCPToolsFromManager`). When using `RegisterMCPTools` directly with
> a single `MCPServerConfig`, tool filtering must be done manually and timeout
> is controlled by the context passed to the client.

### Environment Variable Expansion

> **Warning:** The `${VAR}` syntax in `env:` values is **not** expanded by Loom.
> Environment variables set in the `env:` map are passed as literal strings.
> The spawned MCP process inherits the parent process environment
> (`os.Environ()`), so variables like `GITHUB_TOKEN` that are already set in
> the shell are available to the child process without being listed in `env:`.
> Only use `env:` to set values that are **not** already in the environment
> or to override existing values with literal strings.

```yaml
mcp:
  servers:
    github:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        # Literal value - NOT expanded from shell:
        GITHUB_TOKEN: ghp_abc123
        # The child process also inherits all parent env vars,
        # so if GITHUB_TOKEN is already set in your shell, you
        # can omit this env block entirely.
```

### Keyring Integration ✅

Secrets can be stored in the system keyring and are automatically injected
into MCP server environment variables at startup. You do **not** need to
list them in the `env:` block.

```bash
# Store GitHub token in keyring (prompts for value interactively)
looms config set-key github_token
```

When the server starts, Loom reads the keyring value and injects it as
`GITHUB_TOKEN` into the MCP subprocess environment. The following keyring
keys are supported for MCP:

| Keyring Key | Injected Env Var |
|---|---|
| `github_token` | `GITHUB_TOKEN` |
| `td_password` | `TD_PASSWORD` |
| `postgres_password` | `POSTGRES_PASSWORD` |

No `env:` entry is needed in config for these -- they are injected automatically.
