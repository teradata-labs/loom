# Loom Examples

YAML-based configuration examples demonstrating Loom's declarative agent, workflow, and pattern capabilities.

## Directory Structure

### Reference Configurations (YAML)

- **[reference/](reference/)** - Reference configurations and templates
  - `agents/` - Agent configuration templates
    - `teradata-agent-with-patterns.yaml` - Pattern-guided Teradata agent
    - `web-search-agent.yaml` - Web search using Tavily API
    - `file-analysis-agent.yaml` - Local file system operations
    - `github-agent.yaml` - GitHub API integration
    - Plus swarm-coordinator, sql_expert, security_analyst, and more
  - `agent-templates/` - Reusable agent templates
  - `backends/` - Backend configurations (file, SQL, MCP, REST API, Docker)
  - `patterns/` - Pattern library examples
  - `workflows/` - Workflow examples (organized by type)
    - `orchestration-patterns/` - Structured workflows (debate, pipeline, parallel, swarm, conditional)
    - `event-driven/` - Dynamic multi-agent workflows with communication patterns

- **[dnd-party/](dnd-party/)** - Simple D&D party example (4 agents + workflow)
  - `agents/` - DM, fighter, rogue, wizard
  - `workflows/` - Dungeon crawl workflow

- **[scheduled-workflows/](scheduled-workflows/)** - Cron-scheduled workflow examples
  - `daily-report.yaml` - Daily scheduled report
  - `hourly-sync.yaml` - Hourly data sync
  - `weekly-backup.yaml` - Weekly backup workflow
  - `frequent-monitor.yaml` - High-frequency monitoring

- **[02-production-ready/transcend/](02-production-ready/transcend/)** - **Complete production example**
  - `agents/` - Customer health agent configuration
  - `patterns/` - Domain-specific patterns (analysis, quality, reports, SQL)
  - `workflows/` - Daily health refresh workflow

- **[03-advanced/dnd-adventure/](03-advanced/dnd-adventure/)** - **Advanced multi-agent D&D game**
  - `agents/` - 7 agents (DM, 4 players, rules checker, campaign creator)
  - `workflows/` - Adventure turn workflow
  - `campaigns/` - Campaign data and configurations

### Server Configurations

Server configuration files are located in `config/`:
- `config/looms.yaml` - Full multi-agent server configuration
- `config/looms-tls-dev.yaml` - TLS development configuration
- `config/looms-tls-manual.yaml` - Manual TLS configuration
- `config/looms-production-cors.yaml` - Production CORS configuration

See [config/README.md](config/README.md) for detailed configuration documentation.

**Test Configurations**: See `tests/config/` for test-specific server configurations

## Quick Start

### 1. Install Loom

```bash
# Build from source
cd ~/Projects/loom
just build

# Or install from release
go install github.com/Teradata-TIO/loom/cmd/loom@latest
go install github.com/Teradata-TIO/loom/cmd/looms@latest
```

### 2. Set up API Key

```bash
# Anthropic (recommended for development)
export ANTHROPIC_API_KEY=sk-ant-...

# OR AWS Bedrock (recommended for production)
export AWS_REGION=us-west-2
aws configure

# OR Ollama (local development)
ollama serve
```

### 3. Run an Example

```bash
# Multi-agent server with configuration
cd examples
looms serve --config config/looms.yaml

# In another terminal, connect to an agent
loom --thread weaver
```

## Example Selection Guide

### I want to...

**Learn Loom basics with YAML configuration**
→ Start with `reference/agents/` examples

**Build a production agent**
→ See `02-production-ready/transcend/` (complete configuration-only example)

**Orchestrate multiple agents**
→ See `dnd-party/`, `03-advanced/dnd-adventure/`, `reference/workflows/orchestration-patterns/`, or `reference/workflows/event-driven/`

**Work with Teradata**
→ See `reference/agents/teradata-agent-with-patterns.yaml` or `02-production-ready/transcend/`

**Use scheduled workflows**
→ See `scheduled-workflows/` for cron examples

**Build a complex multi-agent system**
→ See `03-advanced/dnd-adventure/` (7 agents + workflows)

**Integrate external APIs**
→ See `reference/agents/web-search-agent.yaml` or `reference/agents/github-agent.yaml`

## LLM Provider Setup

All examples support multiple LLM providers:

### Anthropic (Recommended for Development)
```bash
export ANTHROPIC_API_KEY=sk-ant-...
```
- Fast API responses
- Claude Sonnet 4.5 model
- Simple setup

### AWS Bedrock (Recommended for Production)
```bash
export AWS_REGION=us-west-2
export AWS_PROFILE=your-profile
export BEDROCK_MODEL=us.anthropic.claude-sonnet-4-20250514-v1:0
```
- Enterprise features
- VPC integration
- Cost optimization

### Ollama (Local Development)
```bash
ollama serve
export OLLAMA_BASE_URL=http://localhost:11434
export OLLAMA_MODEL=qwen2.5:7b
```
- No API key required
- Runs locally
- Free and private

## Configuration-Only (YAML)

All examples in this directory use YAML configuration:
- No Go code required
- Use existing backends and MCP servers
- See: `reference/agents/`, `reference/workflows/`, `02-production-ready/transcend/`

**Example**: The `transcend/` example is 100% configuration - agents, patterns, and workflows defined entirely in YAML.

## Testing Examples

```bash
# Validate YAML configurations
looms validate examples/reference/agents/*.yaml
looms validate examples/reference/workflows/*.yaml

# Test server startup
looms serve --config tests/config/looms-test.yaml --dry-run
```

## Example Complexity

| Complexity | Examples | Best For |
|------------|----------|----------|
| ⭐ Basic | `reference/agents/`, `dnd-party/` | Learning Loom configuration |
| ⭐⭐ Intermediate | `scheduled-workflows/`, `reference/workflows/` | Workflow orchestration |
| ⭐⭐⭐ Advanced | `02-production-ready/transcend/` | Complete production system |
| ⭐⭐⭐⭐ Expert | `03-advanced/dnd-adventure/` | Complex multi-agent systems |

## Common Patterns

### 1. Simple Agent Configuration
```yaml
# See: reference/agents/file-analysis-agent.yaml
agent_id: file-analyzer
model: claude-sonnet-4-5
system_prompt: "Analyze files and provide insights"
tools:
  - file_read
  - file_write
```

### 2. Agent with Patterns
```yaml
# See: reference/agents/teradata-agent-with-patterns.yaml
pattern_libraries:
  - patterns/libraries/teradata-analytics.yaml
  - patterns/libraries/teradata-ml.yaml
```

### 3. Multi-Agent Workflow
```yaml
# See: reference/workflows/feature-pipeline.yaml
spec:
  type: pipeline
  stages:
    - agent_id: designer
    - agent_id: implementer
    - agent_id: tester
```

### 4. MCP Integration
```yaml
# See: 03-advanced/dnd-adventure/looms.yaml
mcp_servers:
  - name: dnd-mcp
    command: dnd-mcp
    args: [serve, --mode=stdio]
```

## Documentation

- **Architecture**: `website/content/en/docs/concepts/architecture.md`
- **Patterns**: `website/content/en/docs/guides/patterns.md`
- **MCP Integration**: `website/content/en/docs/guides/integration/mcp.md`
- **Observability**: `website/content/en/docs/guides/integration/observability.md`

## Contributing

Found an issue or want to add an example?
- Open an issue: https://github.com/Teradata-TIO/loom/issues
- Submit a PR: https://github.com/Teradata-TIO/loom/pulls

## Next Steps

1. **Start simple**: Explore `reference/agents/` examples
2. **Try workflows**: Check out `reference/workflows/` for orchestration
3. **Scale up**: Use `dnd-party/` or `dnd-adventure/` for multi-agent systems
4. **Production**: See `02-production-ready/transcend/` for a complete example

---

**All examples are YAML-based configurations.** No Go code compilation required!
