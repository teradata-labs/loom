
# Loom Features Guide

**Version**: v1.2.0

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

✅ Configure agents and servers entirely through YAML without writing code.

Loom uses two configuration layers:

1. **Server config** (`$LOOM_DATA_DIR/looms.yaml`) - LLM provider, MCP servers, observability, logging
2. **Agent configs** (`$LOOM_DATA_DIR/agents/*.yaml`) - Individual agent definitions

### Create an Agent Configuration

Create `$LOOM_DATA_DIR/agents/my-agent.yaml`:

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-agent
  version: "1.0.0"
  description: "PostgreSQL agent with observability"
spec:
  system_prompt: |
    Generate efficient SQL queries, optimize existing queries, and provide data insights.
    Always use proper formatting, explicit JOIN syntax, and CTEs for complex queries.

  tools:
    - execute_query
    - get_schema
    - list_tables

  config:
    max_turns: 25
    max_tool_executions: 50
```

Observability and LLM provider settings are configured in the server config (`$LOOM_DATA_DIR/looms.yaml`):

```yaml
llm:
  provider: anthropic
  model: claude-sonnet-4-5-20250929

observability:
  enabled: true
  provider: hawk
  hawk_endpoint: http://localhost:9090
```

### Start with the Configuration

```bash
looms serve --config $LOOM_DATA_DIR/looms.yaml
```

## MCP Server Support

✅ Connect to Model Context Protocol servers for external tool access.

### Configure MCP Servers

Add MCP servers to the `mcp:` section of `$LOOM_DATA_DIR/looms.yaml`:

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

    postgres:
      command: npx
      args:
        - "-y"
        - "@modelcontextprotocol/server-postgres"
      env:
        DATABASE_URL: ${POSTGRES_URL}
      transport: stdio

    vantage:
      command: ~/Projects/vantage-mcp/bin/vantage-mcp
      env:
        TD_USER: myuser
      transport: stdio
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

See [MCP Integration Guide](./integration/mcp-readme/) for details.

## Hot Reload

✅ Update patterns, prompts, skills, artifacts, and agent configs without restarting the server.

### Supported File Types

- Pattern YAML files (`patterns/**/*.yaml`)
- Prompt templates (`prompts/**/*.yaml`)
- Skill YAML files (`skills/**/*.yaml`)
- Agent configurations (`$LOOM_DATA_DIR/agents/*.yaml`)
- Workflow configurations (`$LOOM_DATA_DIR/workflows/*.yaml`)
- Artifact files (`$LOOM_DATA_DIR/artifacts/`)

### How Hot Reload Works

Hot reload is enabled by default. The server watches for file changes and reloads automatically:

```bash
looms serve
```

For artifacts, hot reload can be controlled via `looms.yaml`:

```yaml
artifacts:
  hot_reload: true   # default: true
```

Changes apply immediately when files are saved.

## Pattern-Guided Learning

✅ Use YAML patterns to encode domain knowledge.

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

✅ Stream execution progress to build responsive UIs.

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
        progress.GetStage(),
        progress.GetProgress(),
        progress.GetMessage())
}
```

### Progress Stages

The `ExecutionStage` enum defines the following stages:

- `EXECUTION_STAGE_PATTERN_SELECTION` - Matching query to patterns
- `EXECUTION_STAGE_SCHEMA_DISCOVERY` - Discovering backend schema
- `EXECUTION_STAGE_LLM_GENERATION` - LLM generating response (supports token streaming via `PartialContent`)
- `EXECUTION_STAGE_TOOL_EXECUTION` - Running tools
- `EXECUTION_STAGE_GUARDRAIL_CHECK` - Running guardrail checks
- `EXECUTION_STAGE_SELF_CORRECTION` - Agent self-correcting
- `EXECUTION_STAGE_HUMAN_IN_THE_LOOP` - Waiting for human input
- `EXECUTION_STAGE_COMPLETED` - Execution finished
- `EXECUTION_STAGE_FAILED` - Execution failed

## Multiple LLM Providers

✅ Use different LLM providers with the same agent code. Eight providers are supported.

### Anthropic Claude

```go
llm := anthropic.NewClient(anthropic.Config{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  "claude-sonnet-4-5-20250929",
})
```

### AWS Bedrock

```go
llm, err := bedrock.NewClient(bedrock.Config{
    Region:  "us-east-1",
    ModelID: "anthropic.claude-sonnet-4-5-20250929-v1:0",
})
```

### Ollama (Local)

```go
llm := ollama.NewClient(ollama.Config{
    Endpoint: "http://localhost:11434",
    Model:    "llama3.2:latest",
})
```

### Other Supported Providers

- ✅ **OpenAI** (`pkg/llm/openai`) - GPT-4o and other OpenAI models
- ✅ **Azure OpenAI** (`pkg/llm/azureopenai`) - Azure-hosted OpenAI models
- ✅ **Mistral** (`pkg/llm/mistral`) - Mistral AI models
- ✅ **Google Gemini** (`pkg/llm/gemini`) - Gemini models
- ✅ **HuggingFace** (`pkg/llm/huggingface`) - HuggingFace Inference API

See [LLM Providers Reference](../reference/llm-providers/) for details.

## Session Persistence

✅ Automatically save conversation history with SQLite-backed session stores.

### Configure Session Storage (Go API)

```go
store, err := agent.NewSessionStore("./sessions.db", tracer)
if err != nil {
    log.Fatal(err)
}
memory := agent.NewMemoryWithStore(store)
```

### Server-Side Session Storage

When running via `looms serve`, session persistence is automatic. The server stores sessions in SQLite at `$LOOM_DATA_DIR/loom.db` by default. Configure the storage backend in `looms.yaml`:

```yaml
storage:
  # SQLite (default)
  migration:
    auto_migrate: true

  # Or PostgreSQL (optional)
  # postgres:
  #   host: localhost
  #   port: 5432
  #   database: loom
```

## Vision and Document Parsing

✅ Analyze images and parse documents using built-in tools.

### Analyze Images

Enable the `analyze_image` tool in your agent configuration:

```yaml
tools:
  - analyze_image
```

Supported formats: JPEG, PNG, GIF, WebP (max 20MB). Requires a multi-modal LLM provider (e.g., Anthropic Claude, Gemini).

### Parse Documents

Enable the `parse_document` tool in your agent configuration:

```yaml
tools:
  - parse_document
```

Supported formats:
- **CSV** - Auto-delimiter detection, type inference, up to 10,000 rows
- **PDF** - Text extraction, up to 100 pages
- **Excel (.xlsx)** - Multi-sheet support, up to 10,000 rows per sheet

## Next Steps

- [Quick Start](./quickstart/) - Build your first agent
- [Pattern Library Guide](./pattern-library-guide/) - Create domain patterns
- [Integration Guides](./integration/) - Connect with external services
