
# Zero-Code Configuration Guide

**Version**: v1.2.0

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

✅ Configure Loom agents, backends, and MCP servers using YAML files and CLI commands without writing Go code.

## Prerequisites

- Loom v1.2.0+
- API key: `looms config set-key anthropic_api_key`

## Quick Start

Configure an MCP server and start using Loom in 60 seconds:

```bash
# Configure MCP server
looms config set mcp.servers.filesystem.command npx
looms config set mcp.servers.filesystem.args "-y,@modelcontextprotocol/server-filesystem,/home"

# Start server - MCP servers auto-start
looms serve

# In another terminal, connect to the weaver
loom --thread weaver
# Then type your request in the TUI: "I need a file explorer"
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
```

### Create Agent Configuration

Create `$LOOM_DATA_DIR/agents/sql-expert.yaml`.

Agent YAML supports two formats: k8s-style (recommended) or legacy `agent:` wrapper.

**K8s-style format (recommended):**

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: sql-expert
  description: SQL query analysis and optimization
spec:
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
    temperature: 0.7
    max_tokens: 4096

  system_prompt: |
    Analyze SQL queries for performance issues and suggest optimizations.
    Focus on index usage, join efficiency, and query structure.

  tools:
    builtin:
      - execute_sql
      - explain_query
    mcp:
      - server: vantage
        tools:
          - query_system

  config:
    max_turns: 25
    max_tool_executions: 50
```

**Legacy format (also supported):**

```yaml
agent:
  name: sql-expert
  description: SQL query analysis and optimization

  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
    temperature: 0.7
    max_tokens: 4096

  system_prompt: |
    Analyze SQL queries for performance issues and suggest optimizations.

  behavior:
    max_turns: 25
    max_tool_executions: 50
```

Agents in `$LOOM_DATA_DIR/agents/` auto-load on server startup with hot-reload support.

### Configure Backends

Create `$LOOM_DATA_DIR/backends/postgres.yaml`.

Backend YAML uses a flat format (no `metadata`/`spec` wrapper):

```yaml
apiVersion: loom/v1
kind: Backend
name: postgres_prod
description: Production PostgreSQL database
type: postgres

database:
  dsn: ${DATABASE_URL}
  max_connections: 20
  max_idle_connections: 5
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
  timeout_seconds: 5
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
Validating 3 YAML files in $LOOM_DATA_DIR/...

✅ looms.yaml
✅ backends/postgres.yaml
✅ agents/sql-expert.yaml

Summary:
  Valid:   3
  Invalid: 0
  Total:   3
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

# 3. In another terminal, connect to the weaver
loom --thread weaver
# Then type your request in the TUI: "Build a Teradata query optimizer"
# The weaver will create the agent and tell you the thread name to connect to
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

Add the required `kind` field to your YAML. The exact format depends on the kind:

**For Agent, Workflow, Skill, AgentTemplate** (k8s-style with metadata/spec):

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-agent
spec:
  # ...
```

**For Backend, PatternLibrary, EvalSuite, Project** (flat format, no metadata/spec):

```yaml
apiVersion: loom/v1
kind: Backend
name: my-backend
# ...
```

Supported kinds: `Agent`, `Workflow`, `Skill`, `AgentTemplate`, `Project`, `Backend`, `PatternLibrary`, `EvalSuite`

### Agent Not Loading

1. Check file location: `ls $LOOM_DATA_DIR/agents/`
2. Validate YAML syntax: `looms validate file $LOOM_DATA_DIR/agents/myagent.yaml`
3. Check server logs for loading errors
4. Restart server: `looms serve`

### Environment Variables Not Expanding

Loom uses `os.Expand` for environment variable expansion. Supported syntax:

```yaml
database:
  dsn: ${DATABASE_URL}           # Works: braced variable
  dsn: $DATABASE_URL             # Works: unbraced variable
```

**Not supported:** `${VAR:-default}` (default value syntax). The `os.Expand` function does not support shell-style defaults. If a variable is unset, it expands to an empty string. Set all required environment variables before starting the server.

### Keyring Errors

Store secrets properly:
```bash
# Store
looms config set-key anthropic_api_key

# List available keys
looms config list-keys

# Available keys (partial list):
# - anthropic_api_key
# - bedrock_access_key_id
# - bedrock_secret_access_key
# - bedrock_session_token
# - hawk_api_key
# - openai_api_key
# - azure_openai_api_key
# - azure_openai_entra_token
# - mistral_api_key
# - gemini_api_key
# - huggingface_token
# - brave_search_api_key
# - tavily_api_key
# - serpapi_key
# - td_password
# - github_token
# - postgres_password
# - database_url
```
