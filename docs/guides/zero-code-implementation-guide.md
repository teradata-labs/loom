
# Zero-Code Configuration Guide

**Version**: v1.0.0-beta.1

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Common Tasks](#common-tasks)
  - [Configure MCP Servers](#configure-mcp-servers)
  - [Create Agent Configuration](#create-agent-configuration)
  - [Configure Backends](#configure-backends)
  - [Validate Configuration](#validate-configuration)
- [Examples](#examples)
  - [Example 1: Teradata MCP Server](#example-1-teradata-mcp-server)
  - [Example 2: Multi-Backend Setup](#example-2-multi-backend-setup)
- [Troubleshooting](#troubleshooting)


## Overview

Configure Loom agents, backends, and MCP servers using YAML files and CLI commands without writing Go code.

## Prerequisites

- Loom v1.0.0-beta.1+
- API key: `looms config set-key anthropic_api_key`

## Quick Start

Configure an MCP server and start using Loom in 60 seconds:

```bash
# Configure MCP server
looms config set mcp.servers.filesystem.command npx
looms config set mcp.servers.filesystem.args "-y,@modelcontextprotocol/server-filesystem,/home"

# Start server - MCP servers auto-start
looms serve

# Weave a thread
looms weave "I need a file explorer"
```


## Common Tasks

### Configure MCP Servers

**Via CLI (no YAML editing):**

```bash
# Teradata MCP server
looms config set mcp.servers.vantage.command ~/Projects/vantage-mcp/bin/vantage-mcp
looms config set mcp.servers.vantage.env.TD_USER myuser
looms config set-key td_password

# Filesystem MCP server
looms config set mcp.servers.filesystem.command npx
looms config set mcp.servers.filesystem.args "-y,@modelcontextprotocol/server-filesystem,/data"

# GitHub MCP server
looms config set mcp.servers.github.command npx
looms config set mcp.servers.github.args "-y,@modelcontextprotocol/server-github"
looms config set-key github_token
```

**Via YAML** (for complex configurations, edit `$LOOM_DATA_DIR/looms.yaml`):

```yaml
mcp:
  servers:
    vantage:
      command: ~/Projects/vantage-mcp/bin/vantage-mcp
      env:
        TD_USER: myuser
      transport: stdio

    filesystem:
      command: npx
      args:
        - "-y"
        - "@modelcontextprotocol/server-filesystem"
        - "/data"
      transport: stdio
      timeout_seconds: 30
```

### Create Agent Configuration

Create `$LOOM_DATA_DIR/agents/sql-expert.yaml`:

```yaml
name: sql-expert
description: SQL query analysis and optimization

llm:
  provider: anthropic
  model: claude-sonnet-4-5-20250929
  temperature: 0.7
  max_tokens: 4096

system_prompt: |
  Analyze SQL queries for performance issues and suggest optimizations.
  Focus on index usage, join efficiency, and query structure.

tools:
  - name: execute_sql
    description: Execute a SQL query
  - name: explain_query
    description: Get query execution plan

max_turns: 25
max_tool_executions: 50
enable_tracing: true
```

Agents in `$LOOM_DATA_DIR/agents/` auto-load on server startup with hot-reload support.

### Configure Backends

Create `$LOOM_DATA_DIR/backends/postgres.yaml`:

```yaml
apiVersion: loom/v1
kind: Backend
metadata:
  name: postgres_prod
  description: Production PostgreSQL database

spec:
  type: postgres

  connection:
    database:
      dsn: ${DATABASE_URL}
      max_connections: 20
      connection_timeout_seconds: 10

  schema_discovery:
    enabled: true
    cache_ttl_seconds: 3600
    include_tables:
      - users
      - orders
      - products

  health_check:
    enabled: true
    interval_seconds: 30
    query: "SELECT 1"
```

### Validate Configuration

```bash
# Validate a single file
looms validate file $LOOM_DATA_DIR/backends/postgres.yaml

# Validate all files in a directory
looms validate dir $LOOM_DATA_DIR/
```

Expected output:
```
Validating 4 YAML files in $LOOM_DATA_DIR/...

looms.yaml
backends/postgres.yaml
agents/sql-expert.yaml

Summary:
  Valid:   3
  Invalid: 0
```


## Examples

### Example 1: Teradata MCP Server

Complete setup for Teradata connectivity:

```bash
# 1. Configure MCP server
looms config set mcp.servers.vantage.command ~/Projects/vantage-mcp/bin/vantage-mcp
looms config set mcp.servers.vantage.env.TD_USER myuser
looms config set mcp.servers.vantage.env.TD_HOST myhost.teradata.com
looms config set-key td_password

# 2. Start server
looms serve

# 3. Weave a Teradata thread
looms weave "Build a Teradata query optimizer"

# 4. Connect and use
loom --thread teradata-optimizer-abc123 --server localhost:9090
```

### Example 2: Multi-Backend Setup

Configure multiple backends in `$LOOM_DATA_DIR/looms.yaml`:

```yaml
mcp:
  servers:
    # Teradata for analytics
    vantage:
      command: ~/Projects/vantage-mcp/bin/vantage-mcp
      env:
        TD_USER: analyst

    # PostgreSQL for application data
    postgres:
      command: ~/Projects/postgres-mcp/bin/postgres-mcp
      env:
        PGHOST: localhost
        PGDATABASE: myapp

    # Filesystem for logs
    filesystem:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/var/log"]

# Store secrets securely
# looms config set-key td_password
# looms config set-key postgres_password
```


## Troubleshooting

### MCP Server Not Starting

1. Check command exists:
   ```bash
   which npx
   ls ~/Projects/vantage-mcp/bin/
   ```

2. Verify configuration:
   ```bash
   looms config show
   ```

3. Check server logs for MCP errors

### "No 'kind' field found"

Add required fields to YAML:

```yaml
apiVersion: loom/v1
kind: Backend  # Required
metadata:
  name: my-backend
```

Supported kinds: `Project`, `Backend`, `PatternLibrary`, `EvalSuite`

### Agent Not Loading

1. Check file location: `ls $LOOM_DATA_DIR/agents/`
2. Validate YAML syntax: `looms validate file $LOOM_DATA_DIR/agents/myagent.yaml`
3. Check server logs for loading errors
4. Restart server: `looms serve`

### Environment Variables Not Expanding

Ensure proper syntax:
```yaml
connection:
  dsn: ${DATABASE_URL}           # Works
  dsn: $DATABASE_URL             # May not work
  password: ${DB_PASSWORD:-default}  # With default value
```

### Keyring Errors

Store secrets properly:
```bash
# Store
looms config set-key anthropic_api_key

# List available keys
looms config list-keys

# Available keys:
# - anthropic_api_key
# - td_password
# - github_token
# - postgres_password
# - bedrock_access_key_id
# - bedrock_secret_access_key
```
