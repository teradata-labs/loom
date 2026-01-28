
# MCP Integration Guide

**Version**: v1.0.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
  - [CLI Configuration](#cli-configuration)
  - [YAML Configuration](#yaml-configuration)
- [Common Tasks](#common-tasks)
  - [Add an MCP Server](#add-an-mcp-server)
  - [Configure Tool Filtering](#configure-tool-filtering)
  - [Use MCP Tools in Agents](#use-mcp-tools-in-agents)
- [Examples](#examples)
  - [Example 1: Filesystem Server](#example-1-filesystem-server)
  - [Example 2: Multiple MCP Servers](#example-2-multiple-mcp-servers)
- [Troubleshooting](#troubleshooting)


## Overview

Connect Loom agents to MCP (Model Context Protocol) servers for file access, database queries, API integrations, and more.

## Prerequisites

- Loom v1.0.0-beta.1+
- Node.js (for npx-based MCP servers) or custom MCP server binary
- API key configured: `looms config set-key anthropic_api_key`

## Quick Start

```bash
# Configure filesystem MCP server
looms config set mcp.servers.filesystem.command npx
looms config set mcp.servers.filesystem.args "-y,@modelcontextprotocol/server-filesystem,/data"

# Start server - MCP servers auto-start
looms serve

# Weave a thread that uses file tools
looms weave "I need to explore files in /data"
```


## Configuration

### CLI Configuration

No YAML editing required:

```bash
# Filesystem server
looms config set mcp.servers.filesystem.command npx
looms config set mcp.servers.filesystem.args "-y,@modelcontextprotocol/server-filesystem,/home"

# GitHub server
looms config set mcp.servers.github.command npx
looms config set mcp.servers.github.args "-y,@modelcontextprotocol/server-github"
looms config set-key github_token

# Custom binary server (e.g., Teradata)
looms config set mcp.servers.vantage.command ~/Projects/vantage-mcp/bin/vantage-mcp
looms config set mcp.servers.vantage.env.TD_USER myuser
looms config set-key td_password
```

### YAML Configuration

For complex setups, edit `$LOOM_DATA_DIR/looms.yaml`:

```yaml
mcp:
  servers:
    filesystem:
      command: npx
      args:
        - "-y"
        - "@modelcontextprotocol/server-filesystem"
        - "/data"
      transport: stdio

    github:
      command: npx
      args:
        - "-y"
        - "@modelcontextprotocol/server-github"
      env:
        GITHUB_TOKEN: ${GITHUB_TOKEN}
      transport: stdio

    vantage:
      command: ~/Projects/vantage-mcp/bin/vantage-mcp
      env:
        TD_USER: myuser
        TD_HOST: myhost.teradata.com
      transport: stdio
      enabled: true  # Set to true to activate this server
```

### Transport Types

Loom supports three MCP transport types:

#### 1. stdio (Local Servers)
✅ **Recommended for local servers**

Runs MCP servers as subprocesses, communicating via stdin/stdout.

```yaml
mcp:
  servers:
    local-tools:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
      transport: stdio
      enabled: true
```

**Use stdio when:**
- Running MCP servers locally
- Need process isolation
- Want automatic lifecycle management

#### 2. streamable-http (Remote Servers - Modern)
✅ **Recommended for remote servers**

Modern MCP transport (2025-03-26 spec) with session management and stream resumption.

```yaml
mcp:
  servers:
    remote-api:
      transport: streamable-http
      url: https://api.example.com/mcp
      enabled: true
      enable_sessions: true    # Session IDs for state management
      enable_resumption: true  # Fault tolerance with event replay
```

**Use streamable-http when:**
- Connecting to remote MCP servers
- Need session management (Mcp-Session-Id headers)
- Want fault tolerance with stream resumption
- Deploying in production environments

**Features:**
- Single unified endpoint
- Session management via `Mcp-Session-Id` headers
- Stream resumption with `Last-Event-ID`
- Better error handling (HTTP 404 for expired sessions)

#### 3. http/sse (Remote Servers - Legacy)
⚠️ **Deprecated - Use streamable-http instead**

Legacy HTTP/SSE transport for backwards compatibility.

```yaml
mcp:
  servers:
    legacy-server:
      transport: http  # or "sse"
      url: http://legacy-server.example.com:8080/mcp
      enabled: true
```

**Migration to streamable-http:**
```yaml
# Before (legacy)
transport: http
url: http://server.example.com/mcp

# After (modern)
transport: streamable-http
url: http://server.example.com/mcp
enabled: true
enable_sessions: true
enable_resumption: true
```

### Enabling/Disabling Servers

Control which MCP servers start:

```yaml
mcp:
  servers:
    production-db:
      # ... config ...
      enabled: true   # Server will start

    development-db:
      # ... config ...
      enabled: false  # Server will NOT start
```

CLI commands:
```bash
# Enable a server
looms config set mcp.servers.myserver.enabled true

# Disable a server
looms config set mcp.servers.myserver.enabled false
```

> **Note:** Servers default to disabled (`enabled: false`) for safety.
> You must explicitly set `enabled: true` for each server you want to use.


## Common Tasks

### Add an MCP Server

```bash
# Using npx (Node.js MCP servers)
looms config set mcp.servers.postgres.command npx
looms config set mcp.servers.postgres.args "-y,@modelcontextprotocol/server-postgres"
looms config set mcp.servers.postgres.env.DATABASE_URL "postgresql://user:pass@localhost/db"

# Using custom binary
looms config set mcp.servers.custom.command /path/to/mcp-server
```

### Configure Tool Filtering

In agent YAML configuration:

```yaml
# Register all tools from a server
tools:
  mcp:
    - server: "vantage"
      tools: ["*"]

# Register specific tools only
tools:
  mcp:
    - server: "filesystem"
      tools:
        - "read_file"
        - "list_directory"
```

### Use MCP Tools in Agents

Programmatic usage:

```go
import "github.com/teradata-labs/loom/pkg/mcp/manager"

// Create MCP manager
mcpMgr := manager.NewManager(&manager.Config{
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

// Start servers
mcpMgr.Start(ctx)

// Register tools with agent
agent.RegisterMCPServer(ctx, mcpMgr, "filesystem")
```


## Examples

### Example 1: Filesystem Server

```bash
# Configure
looms config set mcp.servers.filesystem.command npx
looms config set mcp.servers.filesystem.args "-y,@modelcontextprotocol/server-filesystem,/home/user/projects"

# Start server
looms serve
```

Expected log output:
```
MCP server started: server="filesystem", pid=12345
MCP client initialized: server="filesystem"
MCP server registered: tools_added=5
```

Available tools:
- `read_file` - Read file contents
- `write_file` - Write to files
- `list_directory` - List directory contents
- `create_directory` - Create directories
- `search_files` - Search for files

### Example 2: Multiple MCP Servers

```yaml
# $LOOM_DATA_DIR/looms.yaml
mcp:
  servers:
    # File operations
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
      transport: stdio

    # GitHub integration
    github:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: ${GITHUB_TOKEN}
      transport: stdio

    # Teradata database
    vantage:
      command: ~/Projects/vantage-mcp/bin/vantage-mcp
      env:
        TD_USER: analyst
        TD_HOST: vantage.example.com
      transport: stdio
```

Agent configuration:
```yaml
# $LOOM_DATA_DIR/agents/multi-tool-agent.yaml
tools:
  mcp:
    - server: "filesystem"
      tools: ["*"]
    - server: "github"
      tools: ["*"]
    - server: "vantage"
      tools: ["*"]
```


## Troubleshooting

### MCP Server Not Starting

1. Verify command exists:
   ```bash
   which npx
   ls ~/Projects/vantage-mcp/bin/
   ```

2. Check server logs:
   ```bash
   looms serve > /tmp/looms.log 2>&1 &
   grep "MCP server" /tmp/looms.log
   ```

3. Test MCP server manually:
   ```bash
   npx -y @modelcontextprotocol/server-filesystem /data
   ```

### Agent Has 0 Tools

Check tool registration logs:
```bash
grep "tools_added" /tmp/looms.log
```

**If tools_added=0:**
- Verify `ToolFilter.All = true` in server config
- Check agent YAML has `tools.mcp` section
- Ensure MCP server is enabled

### macOS Kills MCP Binary

**Symptom:**
```
MCP server exited with error: signal: killed
```

**Solution:** Use symlinks instead of copied binaries:
```bash
# Wrong (macOS kills copied binaries)
cp /path/to/mcp-server ~/.local/bin/

# Correct (use symlinks)
ln -s /path/to/mcp-server ~/.local/bin/mcp-server
```

### Connection Timeout

Increase timeout in config:
```yaml
mcp:
  servers:
    slow_server:
      command: /path/to/server
      timeout_seconds: 60  # Default is 30
```

### Environment Variables Not Set

Verify environment:
```bash
looms config show | grep -A5 "mcp"
```

Store secrets in keyring:
```bash
looms config set-key github_token
looms config set-key td_password
```
