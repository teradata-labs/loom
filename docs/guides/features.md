
# Loom Features Guide

**Version**: v1.0.0

## Table of Contents

- [Overview](#overview)
- [YAML-Based Configuration](#yaml-based-configuration)
- [MCP Server Support](#mcp-server-support)
- [Hot Reload](#hot-reload)
- [Pattern-Guided Learning](#pattern-guided-learning)
- [Real-Time Streaming](#real-time-streaming)
- [Multiple LLM Providers](#multiple-llm-providers)
- [Session Persistence](#session-persistence)
- [Vision and Document Parsing](#vision-and-document-parsing)
- [Next Steps](#next-steps)


## Overview

This guide covers Loom's major features and how to use them.

## YAML-Based Configuration

Configure agents entirely through YAML without writing code.

### Create an Agent Configuration

Create `config/agent.yaml`:

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-agent
  version: "1.0.0"
  description: "PostgreSQL agent with observability"
spec:
  backend: postgres
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929

  tools:
    - name: execute_query
      enabled: true
    - name: get_schema
      enabled: true
    - name: list_tables
      enabled: true

  observability:
    enabled: true
    hawk_endpoint: http://localhost:9090

  limits:
    max_turns: 25
    max_tool_executions: 50
    timeout_seconds: 300
```

### Start with the Configuration

```bash
looms serve --config config/agent.yaml
```

## MCP Server Support

Connect to Model Context Protocol servers for external tool access.

### Configure MCP Servers

Create `$LOOM_DATA_DIR/mcp.yaml`:

```yaml
servers:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]

  postgres:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-postgres"]
    env:
      DATABASE_URL: ${POSTGRES_URL}

  vantage:
    command: ~/Projects/vantage-mcp/bin/vantage-mcp
    env:
      TD_USER: myuser
```

### Use MCP Servers

MCP servers start automatically when you run:

```bash
looms serve
```

### CLI Configuration Alternative

Configure MCP servers via CLI:

```bash
looms config set mcp.servers.vantage.command ~/Projects/vantage-mcp/bin/vantage-mcp
looms config set mcp.servers.vantage.env.TD_USER myuser
looms config set-key td_password  # Secure keyring storage
```

See [MCP Integration Guide](../integration/mcp-readme/) for details.

## Hot Reload

Update patterns and prompts without restarting the server.

### Supported File Types

- Pattern YAML files (`patterns/**/*.yaml`)
- Prompt templates (`prompts/**/*.yaml`)
- Agent configuration (`config/agent.yaml`)
- Tool definitions (`tools/**/*.yaml`)

### Enable Hot Reload

```bash
looms serve --hot-reload
```

Changes apply immediately when files are saved.

## Pattern-Guided Learning

Use YAML patterns to encode domain knowledge.

### Create a Pattern

Create `patterns/analytics/aggregation.yaml`:

```yaml
name: revenue_aggregation
description: Aggregate revenue metrics by dimension
category: analytics
backend_type: sql

templates:
  basic: |
    SELECT {{.dimension}}, SUM({{.metric}}) as total
    FROM {{.table}}
    GROUP BY {{.dimension}}
    ORDER BY total DESC

examples:
  - name: Revenue by region
    parameters:
      dimension: region
      metric: revenue
      table: sales
```

### Load Patterns

```go
library := patterns.NewLibrary(nil, "./patterns")
pattern, _ := library.Load("revenue_aggregation")
```

See [Pattern Library Guide](./pattern-library-guide/) for details.

## Real-Time Streaming

Stream execution progress to build responsive UIs.

### Use StreamWeave

```go
stream, _ := client.StreamWeave(ctx, &loomv1.WeaveRequest{
    Query: "Analyze customer segments",
})

for {
    progress, err := stream.Recv()
    if err == io.EOF {
        break
    }

    fmt.Printf("[%s] %d%% - %s\n",
        progress.Stage,
        progress.Progress,
        progress.Message)
}
```

### Progress Events

- `pattern_selection` - Matching query to patterns
- `llm_generation` - LLM generating response
- `tool_execution` - Running tools

## Multiple LLM Providers

Use different LLM providers with the same agent code.

### Anthropic Claude

```go
llm := anthropic.NewClient(anthropic.Config{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  "claude-sonnet-4-5-20250929",
})
```

### AWS Bedrock

```go
llm := bedrock.NewClient(bedrock.Config{
    Region:  "us-east-1",
    ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0",
})
```

### Ollama (Local)

```go
llm := ollama.NewClient(ollama.Config{
    BaseURL: "http://localhost:11434",
    Model:   "llama3.2:latest",
})
```

See [LLM Providers Reference](../reference/llm-providers/) for details.

## Session Persistence

Automatically save conversation history.

### Configure Session Storage

```go
store, _ := agent.NewSessionStore("./sessions.db", tracer)
memory := agent.NewMemoryWithStore(store)
```

### Per-Agent Memory

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: sql_expert
  version: "1.0.0"
spec:
  memory:
    type: sqlite
    path: $LOOM_DATA_DIR/memory/sql_expert.db
    max_history: 50
```

## Vision and Document Parsing

Analyze images and parse documents.

### Analyze Images

```yaml
tools:
  - name: analyze_image
    enabled: true
```

Supported formats: JPEG, PNG, GIF, WebP (max 20MB).

### Parse Documents

```yaml
tools:
  - name: parse_document
    enabled: true
```

Supported formats:
- CSV - Auto-delimiter detection, type inference
- PDF - Text extraction, page selection
- Excel (.xlsx) - Multi-sheet support

## Next Steps

- [Quick Start](./quickstart/) - Build your first agent
- [Pattern Library Guide](./pattern-library-guide/) - Create domain patterns
- [Integration Guides](./integration/) - Connect with external services
