# Loom Examples

Configuration examples for Loom agents, workflows, backends, and patterns.

**All examples are YAML-based** - No Go code required for these configurations.

## Quick Start

```bash
# 1. Install Loom
go install github.com/teradata-labs/loom/cmd/loom@latest
go install github.com/teradata-labs/loom/cmd/looms@latest

# 2. Set API key
export ANTHROPIC_API_KEY=sk-ant-...

# 3. Start server
cd examples
looms serve --config config/looms.yaml

# 4. Connect to agent (in another terminal)
loom chat --thread=weaver "hi there"
```

## Directory Structure

```
examples/
├── backends/           # Backend integration configurations
├── config/             # Server configurations
├── patterns/           # Pattern libraries (domain knowledge)
└── reference/          # Reference implementations and examples
    ├── agents/         # Agent configurations
    ├── agent-templates/  # Reusable agent templates
    ├── workflows/      # Workflow examples
    └── tools/          # Custom tool configurations
```

## Core Configuration Types

### Backends (`backends/`)

Backend integrations define how agents connect to data sources and services.

**Available backends**:
- **Databases**: PostgreSQL, MySQL, SQLite, MongoDB
- **Teradata**: Vantage via MCP
- **APIs**: REST, GraphQL
- **MCP**: Model Context Protocol servers
- **Files**: Local file system access

**Example**:
```bash
# View available backends
ls backends/

# Use in agent configuration
spec:
  backend:
    config_file: examples/backends/postgres.yaml
```

**See**: [backends/README.md](backends/README.md) for backend documentation (if exists)

---

### Server Configurations (`config/`)

Server configurations define how `looms` (Loom server) runs.

**Available configurations**:
- `looms.yaml` - Full multi-agent server
- `looms-tls-dev.yaml` - TLS development setup
- `looms-tls-manual.yaml` - Manual TLS configuration
- `looms-production-cors.yaml` - Production CORS configuration

**Example**:
```bash
# Start server with configuration
looms serve --config config/looms.yaml

# Validate configuration
looms validate file config/looms.yaml
```

**See**: [config/README.md](config/README.md) for detailed server configuration guide

---

### Patterns (`patterns/`)

Pattern libraries contain domain-specific knowledge and examples that guide agent behavior.

**Available patterns**:
- `sql-optimization.yaml` - SQL query optimization patterns
- `data-quality.yaml` - Data quality validation patterns
- `time-series.yaml` - Time-series analysis patterns
- `ml-analytics.yaml` - Machine learning patterns
- `rest-api.yaml` - REST API interaction patterns

**Example**:
```yaml
# In agent configuration
spec:
  pattern_libraries:
    - examples/patterns/sql-optimization.yaml
```

**Pattern format**:
```yaml
apiVersion: loom/v1
kind: PatternLibrary
metadata:
  name: my-patterns
patterns:
  - name: pattern-name
    trigger: keyword trigger
    template: |
      Pattern content with examples
```

**See**: [patterns/README.md](patterns/README.md) for pattern documentation (if exists)

---

## Reference Examples (`reference/`)

Comprehensive reference implementations and templates.

### Agents (`reference/agents/`)

Complete agent configurations ready to use.

**Example agents**:
- `file-analysis-agent.yaml` - Local file system operations
- `web-search-agent.yaml` - Web search using Tavily API
- `github-agent.yaml` - GitHub API integration
- `sql_expert.yaml` - SQL query expert
- `security_analyst.yaml` - Security analysis
- `teradata-explorer.yaml` - Teradata schema explorer
- `agent-all-fields-reference.yaml` - Complete YAML specification

**Usage**:
```bash
# View agent
cat reference/agents/file-analysis-agent.yaml

# Load agent via server config
agents:
  agents:
    my-agent:
      config_file: examples/reference/agents/file-analysis-agent.yaml
```

**See**: [reference/agents/README.md](reference/agents/README.md)

---

### Agent Templates (`reference/agent-templates/`)

Reusable agent templates with inheritance and variable substitution.

**Available templates**:
- `base-expert.yaml` - Foundation template
- `sql-expert.yaml` - Database expert (extends base-expert)
- `security-analyst.yaml` - Security analyst (extends base-expert)
- `code-reviewer.yaml` - Code review expert (extends base-expert)

**Features**:
- Template inheritance (`extends:`)
- Variable substitution (`{{variable}}`)
- Parameter validation

**Example**:
```go
// Load template programmatically
registry := orchestration.NewTemplateRegistry()
registry.LoadTemplate("reference/agent-templates/sql-expert.yaml")

config, _ := registry.ApplyTemplate("sql-expert", map[string]string{
    "database": "postgres",
    "schema":   "analytics",
})
```

**See**: [reference/agent-templates/README.md](reference/agent-templates/README.md)

---

### Workflows (`reference/workflows/`)

Multi-agent workflow examples demonstrating different coordination patterns.

**Two workflow types**:

#### 1. Orchestration Patterns (`workflows/orchestration-patterns/`)
Structured workflows with predefined patterns:
- **Pipeline** - Sequential stages
- **Debate** - Multi-round structured debate
- **Parallel** - Independent parallel tasks
- **Swarm** - Collective voting/consensus
- **Conditional** - Dynamic routing
- **Fork-join** - Parallel execution with merge
- **Iterative** - Self-correcting pipeline

#### 2. Event-Driven (`workflows/event-driven/`)
Dynamic multi-agent workflows with autonomous coordination:
- **dnd-campaign-builder/** - Hub-and-spoke pattern (7 agents)
- **dungeon-crawler/** - Peer-to-peer pub-sub (4 agents)
- **brainstorm-session/** - Peer-to-peer collaboration (3 agents)

**Example**:
```bash
# Run orchestration pattern
loom workflow run reference/workflows/orchestration-patterns/feature-pipeline.yaml \
  --prompt "Implement user authentication"

# Run event-driven workflow
loom workflow run reference/workflows/event-driven/dnd-campaign-builder/workflows/dnd-campaign-workflow.yaml
```

**See**:
- [reference/workflows/README.md](reference/workflows/README.md)
- [reference/workflows/orchestration-patterns/README.md](reference/workflows/orchestration-patterns/README.md)
- [reference/workflows/event-driven/README.md](reference/workflows/event-driven/README.md)

---

## LLM Provider Setup

All examples support multiple LLM providers.

### Anthropic (Recommended for Development)
```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

### AWS Bedrock (Recommended for Production)
```bash
export AWS_REGION=us-west-2
export AWS_PROFILE=your-profile
export BEDROCK_MODEL=us.anthropic.claude-sonnet-4-20250514-v1:0
```

### Ollama (Local Development)
```bash
ollama serve
export OLLAMA_BASE_URL=http://localhost:11434
export OLLAMA_MODEL=qwen2.5:7b
```

## Common Workflows

### Creating an Agent

```yaml
# my-agent.yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-agent
  description: "My custom agent"

spec:
  backend:
    config_file: examples/backends/postgres.yaml

  system_prompt: |
    You are a helpful assistant.

  llm:
    temperature: 0.7
    max_tokens: 4096

  config:
    max_turns: 20
    max_tool_executions: 50
```

### Starting the Server

```bash
# Full configuration
looms serve --config config/looms.yaml

# Custom configuration
looms serve --config my-config.yaml
```

### Connecting a Client

```bash
# Interactive chat
loom chat agent-name

# Single query
loom query agent-name "Your question here"

# Thread-based conversation
loom --thread my-thread chat agent-name
```

## Validation

Validate configurations before running:

```bash
# Validate server config
looms validate file config/looms.yaml

# Validate backend
looms validate file backends/postgres.yaml

# Validate pattern library
looms validate file patterns/sql-optimization.yaml

# Validate all files in directory
looms validate dir examples/config/
```

## Testing

Test configurations are in `tests/config/`:

```bash
# Test server startup
looms serve --config tests/config/looms-test.yaml --dry-run

# Run configuration tests
cd cmd/looms
go test -tags fts5 -run TestLoadConfig
```

## Further Reading

### Documentation
- [Agent Configuration Reference](reference/agents/agent-all-fields-reference.yaml) - Complete agent YAML spec
- [Workflow Reference](reference/workflows/workflow-all-fields-reference.yaml) - Complete workflow YAML spec
- [Server Configuration Guide](config/README.md) - Comprehensive server setup
- [Agent Templates Guide](reference/agent-templates/README.md) - Template usage and creation

### Architecture
- `docs/architecture/` - System design documentation
- `docs/guides/` - How-to guides
- `docs/reference/` - API reference documentation

## Contributing

Found an issue or want to add an example?
- Open an issue: https://github.com/teradata-labs/loom/issues
- Submit a PR: https://github.com/teradata-labs/loom/pulls

## Questions?

- **Documentation**: See `docs/` directory
- **Examples**: Explore subdirectories in this directory
- **Issues**: GitHub issues tracker
- **Community**: Check README.md for community links
