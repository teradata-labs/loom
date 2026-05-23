# Quick Start

**Version**: v1.2.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
  - [Step 1: Build from Source](#step-1-build-from-source)
  - [Step 2: Configure API Key](#step-2-configure-api-key)
  - [Step 3: Start Server](#step-3-start-server)
  - [Step 4: Launch the TUI](#step-4-launch-the-tui)
- [Examples](#examples)
  - [Example 1: Using the Guide Agent](#example-1-using-the-guide-agent)
  - [Example 2: Connecting to a Specific Agent](#example-2-connecting-to-a-specific-agent)
- [Configuration](#configuration)
- [Troubleshooting](#troubleshooting)

## Overview

Get a working Loom agent running in 5 minutes. The server (`looms`) hosts your agents, and the TUI client (`loom`) connects to interact with them over gRPC.

### What You Get

- ✅ **gRPC server** on port 60051 hosting configured agents
- ✅ **Interactive TUI** with session management, streaming, and cost tracking
- ✅ **CLI chat mode** (`loom chat --thread <name> "message"`) for scripting
- ✅ **MCP bridge** (`loom-mcp`) for Model Context Protocol integration
- ✅ **System keyring** integration for secure API key storage

## Prerequisites

- **Go 1.25+**: [Download](https://go.dev/dl/)
- **just**: `brew install just` (macOS) or `cargo install just`
- **buf**: [Install](https://buf.build/docs/installation) (required for proto code generation)
- **protoc-gen-go** and **protoc-gen-go-grpc**: Install with `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`
- **LLM API Key**: One of the following providers:
  - Anthropic Claude (API key)
  - AWS Bedrock (AWS credentials)
  - OpenAI (API key)
  - Azure OpenAI (API key + endpoint)
  - Google Gemini (API key)
  - Mistral (API key)
  - Hugging Face (API key)
  - Ollama (local, no key required)

> **Note:** All builds and tests require the `-tags fts5` flag. The `just` commands handle this automatically, but if you run `go build` or `go test` directly, you must include `-tags fts5`.

## Quick Start

### Step 1: Build from Source

```bash
git clone https://github.com/teradata-labs/loom
cd loom
just build
```

The build runs proto generation first, then compiles three binaries:

```
Building Loom server (looms)...
✅ Server binary: bin/looms
Building Loom TUI client (loom)...
✅ TUI binary: bin/loom
Building Loom MCP bridge (loom-mcp)...
✅ MCP binary: bin/loom-mcp
✅ All binaries built successfully!
```

You can verify the toolchain is set up correctly with:

```bash
just verify
```

### Step 2: Configure API Key

Run the interactive configuration wizard, then store your API key in the system keyring:

```bash
bin/looms config init
bin/looms config set-key anthropic_api_key
```

The `config init` command walks you through choosing an LLM provider and backends interactively. When complete it prints:

```
✓ Config file created: <your $LOOM_DATA_DIR>/looms.yaml
```

The `set-key` command prompts for the secret with hidden input:

```
Enter anthropic_api_key (input hidden):
✓ Saved anthropic_api_key to system keyring
```

### Step 3: Start Server

```bash
bin/looms serve
```

The server prints an ASCII art logo, then starts listening. You will see a structured zap log line like:

```
{"level":"info","msg":"Server listening","address":"0.0.0.0:60051"}
```

The server listens on `0.0.0.0:60051` by default (configurable in `looms.yaml`). Press Ctrl+C to shut down gracefully.

### Step 4: Launch the TUI

In a new terminal:

```bash
bin/loom
```

This connects to the server at `127.0.0.1:60051` and opens the TUI. With no `--thread` flag, it defaults to the built-in **guide** agent, which helps you discover and select configured agents.

To connect to a specific agent by name:

```bash
bin/loom --thread my-agent-name
```

To connect to a server at a different address:

```bash
bin/loom --server 10.0.0.5:60051 --thread my-agent-name
```

## Examples

### Example 1: Using the Guide Agent

Launch the TUI with no arguments to enter the guide:

```bash
bin/loom
```

The guide agent helps you discover available agents. Try typing:

```
What agents are available?
```

The guide lists configured agents and lets you pick one to start a conversation.

### Example 2: Connecting to a Specific Agent

If you have an agent named `sql-analyzer` configured in `$LOOM_DATA_DIR/agents/`, connect directly:

```bash
bin/loom --thread sql-analyzer
```

Once connected, interact with the agent:

```
Analyze this query for performance issues: SELECT * FROM users WHERE email LIKE '%@gmail.com'
```

The agent uses its configured tools, patterns, and LLM provider to respond.


## Configuration

Config file location: `$LOOM_DATA_DIR/looms.yaml` (default: `~/.loom/looms.yaml`)

The `config init` command generates a file like this:

```yaml
server:
  host: 0.0.0.0
  port: 60051

llm:
  provider: anthropic
  anthropic_model: claude-sonnet-4-5-20250929
  temperature: 1.0
  max_tokens: 4096
  timeout_seconds: 60

database:
  path: ./loom.db
  driver: sqlite

observability:
  enabled: false
  provider: hawk

logging:
  level: info
  format: text
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

> **Note:** The interactive `config init` wizard offers Anthropic, Bedrock, and Ollama. To use other supported providers (OpenAI, Azure OpenAI, Gemini, Mistral, Hugging Face), set them manually with `config set`.


## Troubleshooting

### Connection Refused

The TUI defaults to connecting at `127.0.0.1:60051`. Make sure the server is running and listening on that address.

```bash
# Verify the server is running
bin/looms serve

# Then in another terminal, connect explicitly
bin/loom --server 127.0.0.1:60051
```

### API Key Required

```bash
bin/looms config set-key anthropic_api_key
# Or use environment variable:
export ANTHROPIC_API_KEY=sk-ant-xxx
```

### Model Not Found (Ollama)

```bash
# Pull the model specified in your looms.yaml (ollama_model field)
ollama pull qwen2.5:7b
ollama serve
```

### Thread Not Loading

1. Check server logs for errors
2. Verify agent config exists: `ls $LOOM_DATA_DIR/agents/`
3. Restart server: `bin/looms serve`


## TUI Keyboard Shortcuts

### Global Shortcuts

| Key | Action |
|-----|--------|
| `Ctrl+C` or `Ctrl+Q` | Quit |
| `Ctrl+G` or `Ctrl+/` | Help / more info |
| `Ctrl+K` or `Ctrl+P` | Command palette |
| `Ctrl+O` or `Ctrl+S` | Sessions |
| `Ctrl+E` | Agents dialog |
| `Ctrl+W` | Workflows dialog |
| `Ctrl+Z` | Suspend |

### Chat Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+N` | New session |
| `Ctrl+F` | Add attachment |
| `Ctrl+D` | Toggle details |
| `Ctrl+Space` | Toggle tasks |
| `Esc` | Cancel current operation |
| `Tab` | Change focus |
| Mouse wheel | Scroll history |


## Next Steps

- [Zero-Code Configuration](../zero-code-implementation-guide/) - YAML-only agent deployment (no Go code)
- [Meta-Agent Usage](../meta-agent-usage/) - Advanced thread creation
- [Pattern Library](../pattern-library-guide/) - Using domain patterns
- [TUI Guide](../tui-guide/) - Detailed TUI usage and navigation
- [CLI Reference](../../reference/cli/) - Full CLI command reference
