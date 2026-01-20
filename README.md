# Loom

A Go framework for building autonomous LLM agent threads with **natural language agent creation**, pattern-guided learning, autonomous agent improvement, and multi-agent orchestration.

[![Go Reference](https://pkg.go.dev/badge/github.com/teradata-labs/loom.svg)](https://pkg.go.dev/github.com/teradata-labs/loom)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

---

**Version**: v1.0.2

> **Note**: Loom is in active development. Expect frequent updates, new features, and improvements. The API is stabilizing but may have minor changes as we refine the framework based on user feedback.

**Quality Metrics** (verified 2026-01-08):
- 2252+ test functions across 244 test files
- 73 packages with test coverage
- 0 race conditions (all tests run with `-race` detector)
- Critical packages: patterns 81.7%, communication 77.9%, fabric 79.2%

---

## What is Loom?

Loom lets you **create AI agent threads by describing what you need in plain English**. No coding required for basic use - just tell Loom what you want:

```bash
# Connect to the weaver agent
loom --thread weaver

# Then describe what you need:
# "Analyze PostgreSQL slow queries and suggest indexes"

# The weaver:
# 1. Analyzes your requirements
# 2. Selects appropriate patterns and tools
# 3. Generates complete YAML configuration
# 4. Activates the agent thread
```

Loom is also a complete Go framework for building agent threads programmatically, with pattern-guided learning, real-time streaming, and observability.

### Why Loom?

| Feature | What It Does |
|---------|--------------|
| **Natural Language Creation** | Describe what you need to the weaver, get a working agent in ~30 seconds |
| **Judge Evaluation System** | Multi-judge evaluation with 6 aggregation strategies and streaming support |
| **Learning Agents** | Self-improving agents with [DSPy optimizer](https://dspy.ai/learn/optimization/optimizers/) & [textgradient](https://arxiv.org/abs/2406.07496) integration and intelligent pattern proposals |
| **Multi-Agent Orchestration** | 6 workflow patterns for coordinating agent teams, automatically selected for you by the weaver |
| **Pattern Library** | 90 reusable YAML patterns across 16 domains that are intelligently selected based on the user's intent and fed to the agent on every agent cycle |
| **8 LLM Providers** | Anthropic, Bedrock, Ollama, OpenAI, Azure OpenAI, Mistral, Gemini, HuggingFace |
| **Multi-Modal** | Vision analysis (`analyze_image`) and document parsing (`parse_document`) (works in progress!) |

Unlike prompt-engineering approaches, Loom uses **pattern-guided learning** where domain knowledge is encoded as reusable YAML patterns. This makes agent threads more reliable, testable, and maintainable. Users of loom can write their own patterns in plain english to make loom agents into specialized domain experts.

---

## Getting Started

### Prerequisites

- Go 1.25+
- One of: Anthropic API key, AWS Bedrock access, Ollama installed, OpenAI API key, etc.

### Automated Installation (Recommended)

**Fastest way to get started** - The automated installer handles everything:

#### macOS / Linux

```bash
# Clone and run quickstart
git clone https://github.com/teradata-labs/loom
cd loom
./quickstart.sh
```

#### Windows

```powershell
# Clone and run quickstart (PowerShell)
git clone https://github.com/teradata-labs/loom
cd loom
.\quickstart.ps1

# If you get "Running scripts is disabled on this system":
powershell -ExecutionPolicy Bypass -File .\quickstart.ps1
```

The installer will:
- ✓ Install prerequisites (Go, Just, Buf)
- ✓ Build Loom binaries
- ✓ Install patterns and documentation
- ✓ Configure your LLM provider interactively
- ✓ Set up web search API keys (optional)

See [QUICKSTART.md](QUICKSTART.md) for detailed installation guide (macOS/Linux) or [docs/installation/WINDOWS.md](docs/installation/WINDOWS.md) for Windows.

### Manual Installation

```bash
# Clone the repository
git clone https://github.com/teradata-labs/loom
cd loom

# Install binaries and patterns
just install
# This installs:
#   - Binaries to ~/.local/bin/ (looms, loom)
#   - Patterns to ~/.loom/patterns/ (94 YAML patterns)
#
# Customize installation directories:
#   export LOOM_BIN_DIR=/usr/local/bin  # Custom binary location
#   export LOOM_DATA_DIR=/custom/loom   # Custom data directory
#   just install

# Or build only (for development)
just build                # Minimal build (no optional dependencies)
```

**What gets installed:**
- `looms` - Multi-agent server with weaver and pattern hot-reload
- `loom` - TUI client for connecting to agents
- `~/.loom/patterns/` - 90 reusable patterns across 16 domains (SQL, Teradata, Postgres, text, code, debugging, vision, REST API, document processing, etc.)

**Alternative: Install from source**
```bash
# Install latest tagged release
go install github.com/teradata-labs/loom/cmd/loom@latest
go install github.com/teradata-labs/loom/cmd/looms@latest

# Or install from specific version
go install github.com/teradata-labs/loom/cmd/loom@v1.0.1
go install github.com/teradata-labs/loom/cmd/looms@v1.0.1

# Note: You'll need to manually install patterns for the weaver to work
just install-patterns
```

### Windows Package Managers (Coming Soon- Under Review by scoop, winget, and chocolatey)

Once published, Windows users will be able to install via package managers:

```powershell
# Scoop (developer-friendly)
scoop install loom-server

# winget (Microsoft official - Windows 10/11)
winget install Teradata.Loom

# Chocolatey (most popular)
choco install loom
```

Package manifests are available in `packaging/windows/`. See [docs/installation/WINDOWS.md](docs/installation/WINDOWS.md) for manual installation.

### macOS Package Manager (Coming Soon)

```bash
# Homebrew (once tap is published)
brew tap teradata-labs/loom
brew install loom loom-server

# Or install directly from URL
brew install https://raw.githubusercontent.com/teradata-labs/loom/main/packaging/macos/homebrew/loom-server.rb
```

**Current Status**: Formulas are ready in `packaging/macos/homebrew/`. Use automated installer or manual build for now.

### Quick Start

```bash
# 1. Set your LLM provider credentials
export ANTHROPIC_API_KEY="your-key"  # or configure Bedrock/Ollama/OpenAI

# 2. Start the Loom server
looms serve  # gRPC on :60051, HTTP/REST on :5006

# 3. Access Swagger UI for API docs
open http://localhost:5006/swagger-ui

# 4. Or use the TUI client
loom --thread weaver
# Then type: "Create a code review assistant that checks for security issues"

# 5. Connect to your newly created thread
loom --thread code-review-assistant
```

**API Access:**
- **gRPC**: `localhost:60051` (native protocol)
- **HTTP/REST**: `http://localhost:5006` (REST API + SSE streaming)
- **Swagger UI**: `http://localhost:5006/swagger-ui` (interactive API docs)
- **OpenAPI Spec**: `http://localhost:5006/openapi.json`

---

## Core Components

### Weaver (Meta-Agent)

The weaver transforms natural language into fully-configured agents:

```bash
# Connect to the weaver agent
loom --thread weaver

# Then describe what you need:
# "Analyze PostgreSQL performance and suggest optimizations"
# "Create a workflow for monitoring API endpoints"
# "Add error handling and retry logic to my existing agent"
```

The weaver:
- Analyzes requirements to determine domain and capabilities
- Selects optimal patterns from the library
- Generates complete YAML configuration
- Validates the configuration
- Activates the agent thread

### Multi-Agent Orchestration

6 workflow patterns for coordinating agent teams (defined in `proto/loom/v1/orchestration.proto`):

| Pattern | Description | Use Case |
|---------|-------------|----------|
| **Pipeline** | Sequential execution, output flows to next stage | ETL, multi-step analysis |
| **Parallel** | Independent tasks execute concurrently | Batch processing |
| **Fork-Join** | Parallel execution with merged results | Aggregation, consensus |
| **Debate** | Agents argue different perspectives | Decision making, validation |
| **Conditional** | Route based on agent decisions | Branching workflows |
| **Swarm** | Dynamic agent collaboration | Complex problem solving |

Additional collaboration patterns in `pkg/collaboration/`:
- Teacher-Student
- Pair Programming

### Judge Evaluation System

Multi-judge evaluation with configurable aggregation strategies:

```bash
# Evaluate agent output with multiple judges
looms judge evaluate \
  --agent=sql-agent \
  --judges=quality-judge,safety-judge,cost-judge \
  --prompt="Generate a query" \
  --response="SELECT * FROM users" \
  --aggregation=weighted-average

# Streaming evaluation for long-running operations
looms judge evaluate-stream \
  --agent=sql-agent \
  --judges=quality,safety,performance \
  --prompt-file=input.txt \
  --response-file=output.txt
```

**Aggregation Strategies** (6 available):
- `weighted-average` - Weight judges by importance
- `all-must-pass` - All judges must approve
- `majority-pass` - Majority consensus required
- `any-pass` - At least one judge approves
- `min-score` / `max-score` - Take minimum/maximum score

**Features**:
- Real-time streaming progress updates
- Configurable retry policies with circuit breakers
- Hawk export for observability
- Fail-fast mode for critical failures
- DSPy integration for judge optimization
- 89% test coverage (`pkg/evals/judges/`)

**Judge Types**:
- Quality judges (accuracy, completeness)
- Safety judges (security, compliance)
- Performance judges (efficiency, cost)
- Custom judges (domain-specific criteria)

See [Judge CLI Guide](docs/guides/judge_cli_guide.md) and [Multi-Judge Evaluation](docs/guides/multi-judge-evaluation.md).

### Learning Agents

Self-improving agents that propose pattern improvements based on experience:

```bash
# Connect to the weaver
loom --thread weaver

# Then request: "Create a learning SQL agent that improves query patterns"

# The learning agent will:
# 1. Execute tasks and collect feedback
# 2. Analyze successful/failed attempts
# 3. Propose pattern improvements
# 4. Integrate with DSPy for optimization
```

**Learning Capabilities**:
- Pattern proposal generation
- Success/failure analysis
- DSPy integration for prompt optimization
- Iterative improvement loops
- A/B testing of pattern variants

**Learning Modes**:
- `observation` - Collect data without changes
- `proposal` - Generate improvement suggestions
- `auto-apply` - Automatically apply validated improvements (library-only)

**DSPy Integration**:
- Teleprompter optimization (now aptly renamed optimizers by DSPy)
- Signature compilation
- Example-based learning
- Metric-driven improvement

See [Learning Agent Guide](docs/guides/learning-agent-guide.md) and [Judge-DSPy Integration](docs/guides/judge-dspy-integration.md).

### Pattern Library

90 reusable YAML patterns across 16 domains, installed to `~/.loom/patterns/` by default:

| Domain | Patterns | Examples |
|--------|----------|----------|
| `teradata/` | 34 | ML models, analytics, data quality, semantic mapping, performance, FastLoad |
| `postgres/` | 12 | Query optimization, index analysis, vacuum recommendations |
| `libraries/` | 11 | Pattern bundles for specific domains |
| `sql/` | 8 | Data validation, profiling, duplicate detection |
| `fun/` | 5 | Entertainment patterns |
| `prompt_engineering/` | 4 | Chain-of-thought, few-shot learning, structured output |
| `documents/` | 4 | PDF extraction, Excel analysis, CSV import (legacy) |
| `document/` | 2 | Multi-format parsing, document analysis |
| `code/` | 2 | Test generation, documentation generation |
| `vision/` | 2 | Chart interpretation, form extraction |
| `text/` | 2 | Summarization, sentiment analysis |
| `rest_api/` | 1 | Health checks, liveness/readiness probes |
| `debugging/` | 1 | Root cause analysis |
| `evaluation/` | 1 | Prompt evaluation |
| `nasa/` | 1 | Space/astronomy patterns |

**Pattern Discovery** - Patterns are searched in priority order:
1. `$LOOM_DATA_DIR/patterns/` (installed patterns - checked first)
2. `./patterns/` (development mode)
3. Upward directory search (for test contexts)

This allows patterns to work from any directory and survive binary updates.

### Communication System

Tri-modal inter-agent communication:

- **Message Queue**: Ordered message passing between agents
- **Shared Memory**: Key-value store for shared state
- **Broadcast Bus**: Pub/sub for event distribution

### Built-in Tools

| Tool | Description |
|------|-------------|
| `analyze_image` | Vision analysis for charts, screenshots, diagrams (work in progress!) |
| `parse_document` | Extract data from PDF, Excel (.xlsx), CSV files (work in progress!) |
| `send_message` / `receive_message` | Inter-agent messaging |
| `shared_memory` | Read/write shared state for multi-agent systems |
| `file_read` / `file_write` | File system operations |
| `http_client` / `grpc_client` | External service calls |
| `record_progress` | Note taking/reminder tool for agents |
| `web_search` | Web search integration (requires a [Tavily](https://www.tavily.com) or [Brave](https://brave.com/search/api/) API key. |

### Artifact Management

Centralized file storage system for agents managing datasets, documents, and generated files:

**Features:**
- Full-text search with SQLite FTS5
- Automatic content type detection and metadata extraction
- Archive support (zip, tar, tar.gz) - archives are stored as-is
- Soft/hard delete with statistics tracking
- Tag-based organization and filtering

**Usage:**
```bash
# Upload a file
looms artifacts upload ./data.csv --tags data,customer

# Upload an archive (directories not supported - create archive first)
tar -czf mydata.tar.gz ./mydata/
looms artifacts upload mydata.tar.gz

# Search artifacts
looms artifacts search "customer data"

# List all artifacts
looms artifacts list

# Get artifact content
looms artifacts get <artifact-id>
```

**Note:** Directory uploads are not supported. To upload multiple files, create a tar/zip archive first.

### MCP Protocol

No-code integration with any MCP server:

```bash
# Configure an MCP server
looms config set mcp.servers.github.command /path/to/github-mcp
looms config set mcp.servers.github.env.GITHUB_TOKEN "${GITHUB_TOKEN}"

# MCP servers auto-start with: looms serve
```

MCP coverage: 50-92% across modules (adapter 60%, manager 50%, protocol 92%).

---

## LLM Provider Support

8 providers implemented (in `pkg/llm/`):

| Provider | Status | Notes |
|----------|--------|-------|
| Anthropic | Tested | Claude 3+, vision support |
| AWS Bedrock | Tested | Claude, Titan models |
| Ollama | Tested | Local models |
| OpenAI | Tested | GPT-4, GPT-4V vision |
| Azure OpenAI | Implemented | Enterprise deployments |
| Google Gemini | Implemented | Gemini Pro, vision support |
| Mistral | Implemented | Mistral models |
| HuggingFace | Implemented | HuggingFace Inference API |

---

## TUI Features

Loom includes a feature-rich terminal UI (`loom`) based on [charmbracelet](https://github.com/charmbracelet)'s excellent TUI framework, [bubbletea](https://github.com/charmbracelet/bubbletea) with [Crush](https://github.com/charmbracelet/crush)-inspired visual design and aesthetics:

### Visual Design
- **Crush-style theming**: Orange/green color scheme with proper visual hierarchy
- **Multi-agent support**: Unlimited agents with distinct colors (6 predefined + golden ratio color generation)
- **Message separators**: Visual dividers between messages for clarity
- **Responsive layout**: Adapts to terminal size with proper padding

### Keyboard Shortcuts
- `ctrl+c` - Quit application
- `ctrl+n` - New session
- `ctrl+l` - Clear messages
- **`ctrl+p` - Toggle compact mode** (reduced padding/spacing)
- `ctrl+u` / `ctrl+d` - Page up/down
- `pgup` / `pgdn` - Scroll viewport

### Model Switching
Mid-session model switching without losing conversation context:
- **17+ models available**: Claude Sonnet 4.5/3.5/Opus, GPT-5/4o, Llama 3.1/3.2, Gemini 2.0 Flash/1.5 Pro, Mistral Large/Small, Qwen 2.5
- **Context preservation**: Full conversation history maintained when switching
- **Provider diversity**: Anthropic, Bedrock, Ollama (free), OpenAI, Azure, Gemini, Mistral, HuggingFace
- **Cost transparency**: Shows pricing per 1M tokens for each model

### Multi-Agent Display
When multiple agents are present in a conversation:
- Each agent gets a distinct color (6 predefined colors, then generated)
- Agent ID shown in message headers: `Agent[agent-1]`
- Color consistency maintained throughout conversation
- Supports 50+ agents with golden ratio-based color generation

### Status Indicators
- Streaming progress with animated indicators
- Tool execution states (pending, success, error)
- Cost tracking per message
- Timestamps for all messages

---

## Architecture

```
+------------------+     +------------------+     +------------------+
|   Applications   |     |     looms CLI    |     |    loom TUI      |
|  (your agents)   |     |   (server mgmt)  |     |    (client)      |
+--------+---------+     +--------+---------+     +--------+---------+
         |                        |                        |
         v                        v                        v
+------------------------------------------------------------------------+
|                              Loom Framework                              |
|  +---------------+  +---------------+  +---------------+  +-----------+ |
|  | Agent Runtime |  | Orchestration |  | Pattern Lib   |  | Shuttle   | |
|  | (pkg/agent)   |  | (6 patterns)  |  | (94 patterns) |  | (tools)   | |
|  +---------------+  +---------------+  +---------------+  +-----------+ |
+------------------------------------------------------------------------+
         |                        |                        |
         v                        v                        v
+------------------+     +------------------+     +------------------+
|     Promptio     |     |       Hawk       |     |   LLM Providers  |
|  (prompt mgmt)   |     |  (observability) |     |  (8 providers)   |
+------------------+     +------------------+     +------------------+
```

See [Architecture Guide](docs/architecture/) for detailed design.

---

## Documentation

### Quick Links

- [Getting Started Guide](docs/guides/quickstart.md)
- [Architecture Overview](docs/architecture/)
- [Features Guide](docs/guides/features.md)
- [API Reference](https://pkg.go.dev/github.com/teradata-labs/loom)

### Guides

- [Backend Implementation](docs/reference/backend.md) - Implementing `ExecutionBackend`
- [Pattern System](docs/reference/patterns.md) - Creating and using patterns
- [HTTP Server & CORS](docs/reference/http-server.md) - REST API, Swagger UI, CORS configuration
- [Observability Setup](docs/guides/integration/observability.md) - Hawk integration
- [Prompt Management](docs/guides/integration/prompt-integration.md) - Promptio integration
- [Streaming](docs/reference/streaming.md) - Real-time progress events
- [Meta-Agent Usage](docs/guides/meta-agent-usage.md) - Advanced weaver usage

### Examples

- [Examples Overview](./examples/README.md) - YAML-based configuration examples
- [Configuration Templates](./examples/reference/) - Agent, workflow, and pattern templates

---

## Roadmap

Upcoming improvements:
- Additional pattern library content
- Performance benchmarks and optimization
- Extended documentation and tutorials
- Community feedback incorporation

---

## Contributing

Contributions welcome! Please:

1. Run `go test -race ./...` before submitting PRs
2. Follow existing code patterns
3. Add tests for new features
4. Update documentation as needed

See [CONTRIBUTING.md](./CONTRIBUTING.md) for detailed guidelines.

---

## Support

- **GitHub Issues**: [Report bugs or request features](https://github.com/teradata-labs/loom/issues)
- **Security Issues or vulnerabilities**: Contact security@teradata.com.  Please do not post security issues or vulnerabilites in GitHub Issues.
- **Documentation**: [Browse the docs](docs/)

---

## License

Apache 2.0 - see [LICENSE](./LICENSE)

---

Built by Teradata Labs
