
# Quick Start

**Version**: v1.0.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
  - [Step 1: Build from Source](#step-1-build-from-source)
  - [Step 2: Configure API Key](#step-2-configure-api-key)
  - [Step 3: Start Server](#step-3-start-server)
  - [Step 4: Weave Your First Thread](#step-4-weave-your-first-thread)
- [Examples](#examples)
  - [Example 1: File Explorer](#example-1-file-explorer)
  - [Example 2: SQL Analyzer](#example-2-sql-analyzer)
- [Configuration](#configuration)
- [Troubleshooting](#troubleshooting)


## Overview

Get a working Loom agent in 5 minutes. Describe what you need, and the weaver generates the configuration.

## Prerequisites

- **Go 1.21+**: [Download](https://go.dev/dl/)
- **just**: `brew install just` (macOS) or `cargo install just`
- **LLM API Key**: Anthropic, AWS Bedrock, or Ollama

## Quick Start

### Step 1: Build from Source

```bash
git clone https://github.com/teradata-labs/loom
cd loom
just build
```

Expected output:
```
building loom (TUI client)...
building looms (server)...
Build complete. Binaries in ./bin/
```

### Step 2: Configure API Key

```bash
bin/looms config init
bin/looms config set-key anthropic_api_key
```

Expected output:
```
Enter anthropic_api_key (input hidden):
Saved anthropic_api_key to system keyring
```

### Step 3: Start Server

```bash
bin/looms serve
```

Expected output:
```
Loom server starting on :9090
Server ready
```

### Step 4: Weave Your First Thread

In a new terminal:

```bash
bin/looms weave "I need a thread to read and analyze files"
```

Expected output:
```
Weaving thread from requirements...

Thread woven successfully!
  Thread Name: file-analyzer-abc123
  Status: running

Activating thread...
```

The TUI client launches automatically. If you need to reconnect later:

```bash
bin/loom --thread file-analyzer-abc123 --server localhost:9090
```


## Examples

### Example 1: File Explorer

```bash
bin/looms weave "Create a file explorer that can:
- Read files in my project
- Search for patterns
- Analyze code structure"
```

After connecting, try:
```
> Show me all Go files in this project
> What's the structure of the pkg/agent package?
> Search for TODO comments in the codebase
```

### Example 2: SQL Analyzer

```bash
bin/looms weave "Build a SQL query optimizer for PostgreSQL"
```

After connecting:
```
> Analyze this query for performance issues: SELECT * FROM users WHERE email LIKE '%@gmail.com'
> Suggest indexes for a table with columns: id, user_id, created_at, status
```


## Configuration

Config file location: `$LOOM_DATA_DIR/looms.yaml`

```yaml
server:
  host: 0.0.0.0
  port: 9090

llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929
  temperature: 1.0
  max_tokens: 4096

database:
  path: $LOOM_DATA_DIR/loom.db
  driver: sqlite
```

View configuration:
```bash
bin/looms config show
```

Change settings:
```bash
bin/looms config set llm.provider bedrock
bin/looms config set llm.anthropic_model claude-sonnet-4-5-20250929
```


## Troubleshooting

### Connection Refused

Server uses port 9090 by default, client uses 60051.

**Solution:**
```bash
bin/loom --server localhost:9090
```

### API Key Required

```bash
bin/looms config set-key anthropic_api_key
# Or use environment variable:
export ANTHROPIC_API_KEY=sk-ant-xxx
```

### Model Not Found (Ollama)

```bash
ollama pull llama3.1
ollama serve
```

### Thread Not Loading

1. Check server logs for errors
2. Verify config saved: `ls $LOOM_DATA_DIR/agents/`
3. Restart server: `bin/looms serve`


## TUI Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+C` | Quit |
| `Esc` | Quit |
| `Ctrl+N` | New session |
| `Ctrl+L` | Clear screen |
| Mouse wheel | Scroll history |


## Next Steps

- [Zero-Code Configuration](../zero-code-implementation-guide/) - No-code MCP setup
- [Meta-Agent Usage](../meta-agent-usage/) - Advanced thread creation
- [Pattern Library](../pattern-library-guide/) - Using domain patterns
