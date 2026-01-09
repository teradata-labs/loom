# Agent Configuration Examples

This directory contains example agent configurations demonstrating different use cases and integrations.

## What is an Agent Configuration?

An agent configuration YAML file defines:
- **Backend**: What data source or API the agent can access
- **LLM Provider**: Which language model to use (Anthropic, Bedrock, Ollama)
- **Patterns**: Domain knowledge and best practices to guide the agent
- **System Prompt**: Core instructions and behavior
- **Observability**: Tracing and metrics configuration

## Examples

### teradata-agent-with-patterns.yaml
**Use Case**: Teradata SQL analytics with pattern-guided learning

**Features**:
- Teradata MCP backend integration
- Pattern libraries for analytics, ML, and data quality
- Mandatory Teradata SQL syntax rules
- Self-correction enabled

**Prerequisites**:
- Teradata MCP server configured (see `config/backends/vantage-mcp.yaml`)
- Pattern libraries in place

**Usage**:
```bash
# Start the agent server with this configuration
looms serve --config examples/reference/agents/teradata-agent-with-patterns.yaml

# Or register and use via CLI
looms agent create -f examples/reference/agents/teradata-agent-with-patterns.yaml
loom chat --agent teradata-agent-with-patterns
```

### web-search-agent.yaml
**Use Case**: Web search and information retrieval using Tavily API

**Features**:
- Tavily search API integration
- Structured result parsing
- Citation tracking
- Rate limiting

**Prerequisites**:
- Tavily API key: `export TAVILY_API_KEY=tvly-...`

**Usage**:
```bash
looms agent create -f examples/reference/agents/web-search-agent.yaml
loom chat --agent web-search
```

### file-analysis-agent.yaml
**Use Case**: Local file system analysis and operations

**Features**:
- File backend with read/write/list operations
- Pattern matching and search
- Content analysis
- No external dependencies

**Prerequisites**: None (uses local filesystem)

**Usage**:
```bash
looms agent create -f examples/reference/agents/file-analysis-agent.yaml
loom chat --agent file-analysis
```

### github-agent.yaml
**Use Case**: GitHub repository analysis and operations

**Features**:
- GitHub REST API integration
- Repository analysis
- Issue and PR management
- Code search

**Prerequisites**:
- GitHub token: `export GITHUB_TOKEN=ghp_...`

**Usage**:
```bash
looms agent create -f examples/reference/agents/github-agent.yaml
loom chat --agent github
```

## Configuration Structure

All agent configurations follow this structure:

```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: agent-name
  version: 1.0.0
  description: What this agent does
  labels:
    backend: backend-type
    maturity: alpha|beta|stable

spec:
  # Backend configuration
  backend:
    name: backend-identifier
    config_file: path/to/backend/config.yaml

  # LLM configuration
  llm:
    provider: anthropic|bedrock|ollama
    model: model-name
    api_key_env: ENV_VAR_NAME
    temperature: 0.0-1.0
    max_tokens: 1024-8192

  # Optional: Pattern libraries
  pattern_libraries:
    - path/to/patterns.yaml

  # System prompt
  system_prompt: |
    Instructions for the agent...

  # Optional: Configuration
  config:
    max_turns: 10
    max_tool_executions: 20
    enable_tracing: true
    enable_self_correction: false

  # Optional: Observability
  observability:
    hawk_endpoint: http://localhost:9090
    export_traces: true
    export_metrics: true
```

## Creating Custom Agents

1. **Choose a backend**: See `config/backends/` for available backends
2. **Select LLM provider**: Anthropic (fastest), Bedrock (enterprise), Ollama (local)
3. **Write system prompt**: Clear instructions for agent behavior
4. **Add patterns (optional)**: Domain knowledge to guide responses
5. **Configure observability (optional)**: Hawk integration for tracing

Example minimal agent:
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: my-agent
  version: 1.0.0

spec:
  backend:
    name: file
    config_file: config/backends/file.yaml

  llm:
    provider: ollama
    model: qwen2.5:7b
    temperature: 0.7
    max_tokens: 2048

  system_prompt: |
    You are a helpful assistant that can read and write files.
```

## Best Practices

### System Prompts
- ✅ Be specific about syntax rules and constraints
- ✅ Include mandatory rules at the top
- ✅ Provide examples of good/bad behavior
- ❌ Don't use role-playing ("You are a helpful assistant...")
- ❌ Don't include marketing speak

### Backend Selection
- **File backend**: Testing, local development
- **SQLite backend**: Embedded databases, demos
- **PostgreSQL backend**: Production SQL databases
- **MCP backends**: Specialized integrations (Teradata, GitHub, etc.)
- **REST API backend**: Custom HTTP APIs

### LLM Selection
- **Anthropic Claude**: Best for development (fast, accurate)
- **AWS Bedrock**: Best for production (enterprise features)
- **Ollama**: Best for local/offline development

### Patterns
- Use patterns for domain-specific knowledge
- Keep patterns modular and reusable
- Version patterns alongside agents
- Test patterns with real queries

### Observability
- Enable tracing in development
- Use Hawk for production monitoring
- Tag agents with environment and purpose
- Monitor token usage and costs

## Related Documentation

- [Pattern Library Guide](../config/patterns/README.md) - Creating and using patterns
- [Backend Configuration](../config/backends/README.md) - Available backends
- [Workflow Orchestration](../config/workflows/README.md) - Multi-agent workflows

## Next Steps

1. Copy an example and modify it for your use case
2. Test with `looms agent create -f your-agent.yaml`
3. Iterate on system prompt and configuration
4. Add patterns if needed
5. Deploy with observability enabled
