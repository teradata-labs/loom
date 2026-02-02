# Loom Documentation

**Version**: v1.1.0

Welcome to the Loom documentation. Loom is a Go framework for building autonomous LLM agent threads with natural language agent creation, pattern-guided learning, and multi-agent orchestration.

---

## Getting Started

| Guide | Description |
|-------|-------------|
| **[Quick Start](guides/quickstart.md)** | Install Loom and create your first agent in 5 minutes |
| **[Features Overview](guides/features.md)** | Core features and how to use them |
| **[Zero-Code Setup](guides/zero-code-implementation-guide.md)** | Create agents without writing code |

---

## User Guides

### Core Features
- **[Pattern Library](guides/pattern-library-guide.md)** - Use domain patterns to improve agent responses
- **[Learning Agents](guides/learning-agent-guide.md)** - Self-improving agents with DSPy integration
- **[Weaver (Meta-Agent)](guides/meta-agent-usage.md)** - Create agents that work together
- **[Artifacts Management](guides/artifacts-usage.md)** - Centralized file storage with search

### Evaluation & Quality
- **[Judge CLI](guides/judge_cli_guide.md)** - Evaluate agent responses with judges
- **[Multi-Judge Evaluation](guides/multi-judge-evaluation.md)** - Multi-judge strategies and aggregation
- **[Judge-DSPy Integration](guides/judge-dspy-integration.md)** - Optimize judges with DSPy
- **[Judge-DSPy Streaming](guides/judge-dspy-streaming.md)** - Real-time streaming evaluation

### Advanced Features
- **[Human-in-the-Loop](guides/human-in-the-loop.md)** - Add approval workflows to agents
- **[Memory Management](guides/memory-management.md)** - Agent memory layers and caching
- **[Structured Context Pattern](guides/structured-context-pattern.md)** - Organize agent context
- **[Docker Backend](guides/docker-backend.md)** - Run agents in containers

[View all guides →](guides/)

---

## Integration Guides

### MCP (Model Context Protocol)
- **[MCP Integration](guides/integration/mcp-readme.md)** - Connect to MCP servers
- **[MCP Implementation Part 1](guides/integration/mcp-implementation.md)** - Core implementation
- **[MCP Implementation Part 2](guides/integration/mcp-implementation-part2.md)** - Advanced features
- **[MCP Implementation Part 3](guides/integration/mcp-implementation-part3.md)** - Production setup
- **[MCP Executive Summary](guides/integration/mcp-executive-summary.md)** - Overview for decision makers
- **[MCP Troubleshooting](guides/integration/mcp-troubleshooting.md)** - Common issues and solutions

### Observability & Prompts
- **[Observability (Hawk)](guides/integration/observability.md)** - Trace agent operations and track costs
- **[Hawk Embedded Integration](guides/integration/hawk-embedded-integration-guide.md)** - Embed Hawk in your app
- **[Prompt Management (Promptio)](guides/integration/prompt-management.md)** - Version-controlled prompts
- **[Prompt Integration](guides/integration/prompt-integration.md)** - Integrate Promptio

[View all integration guides →](guides/integration/)

---

## Architecture

System design documentation for developers extending Loom.

### Core Systems
- **[System Architecture](architecture/loom-system-architecture.md)** - Overall system design and components
- **[Agent Runtime](architecture/agent-runtime.md)** - How agents execute conversations
- **[Agent System Design](architecture/agent-system-design.md)** - Agent architecture patterns
- **[Data Flows](architecture/data-flows.md)** - Data flow through the system

### Specialized Systems
- **[Pattern System](architecture/pattern-system.md)** - Pattern matching and loading
- **[Multi-Agent System](architecture/multi-agent.md)** - Agent communication and orchestration
- **[Communication System](architecture/communication-system-design.md)** - Inter-agent messaging
- **[Memory Systems](architecture/memory-systems.md)** - Agent memory architecture
- **[Agent Private Memory](architecture/agent-private-memory.md)** - Private memory implementation

### Features & Tools
- **[Weaver (Meta-Agent)](architecture/weaver.md)** - Meta-agent architecture
- **[Learning Agent](architecture/learning-agent.md)** - Self-improvement system
- **[Judge System](architecture/judge-system.md)** - Evaluation framework
- **[Artifacts](architecture/artifacts.md)** - File storage system
- **[Docker Backend](architecture/docker-backend.md)** - Container execution
- **[Observability](architecture/observability.md)** - Tracing and monitoring

[View all architecture docs →](architecture/)

---

## Reference

Complete API specifications and command documentation.

### LLM Providers
- **[LLM Providers Overview](reference/llm-providers.md)** - Supported providers and configuration
- **[Anthropic](reference/llm-anthropic.md)** - Claude configuration
- **[AWS Bedrock](reference/llm-bedrock.md)** - Bedrock configuration
- **[Ollama](reference/llm-ollama.md)** - Local models with Ollama
- **[OpenAI](reference/llm-openai.md)** - GPT models configuration
- **[Azure OpenAI](reference/llm-azure-openai.md)** - Azure OpenAI setup
- **[Mistral](reference/llm-mistral.md)** - Mistral models

### Commands & APIs
- **[CLI Commands](reference/cli.md)** - Complete looms and loom command reference
- **[TUI (Terminal UI)](reference/tui.md)** - Terminal interface usage
- **[Streaming](reference/streaming.md)** - Real-time progress events

### Agent Configuration
- **[Agent Configuration](reference/agent-configuration.md)** - YAML configuration reference
- **[Patterns](reference/patterns.md)** - Pattern YAML format
- **[Pattern Recommendations](reference/pattern-recommendations.md)** - Best practices
- **[Tool Registry](reference/tool-registry.md)** - Available tools
- **[Meta-Agent Tools](reference/meta-agent-tools.md)** - Weaver-specific tools
- **[Presentation Tools](reference/presentation-tools.md)** - Output formatting tools

### Advanced
- **[Backend Interface](reference/backend.md)** - Implementing ExecutionBackend
- **[Prompt Hot-Reload](reference/prompt-hot-reload.md)** - Dynamic prompt updates
- **[Self-Correction](reference/self-correction.md)** - Error recovery mechanisms
- **[SQLite Guidance](reference/sqlite-guidance.md)** - Database best practices
- **[TLS Configuration](reference/tls.md)** - Secure communication setup
- **[Workflow Iterative](reference/workflow-iterative.md)** - Iterative workflow patterns

[View all reference docs →](reference/)

---

## Additional Resources

- **[Go Package Documentation](https://pkg.go.dev/github.com/teradata-labs/loom)** - API reference for Go developers
- **[GitHub Repository](https://github.com/teradata-labs/loom)** - Source code and issues
- **[Examples](../examples/)** - Complete example configurations
- **[Workflow Scheduling](workflow-scheduling.md)** - Cron-based workflows

---

## Documentation Standards

This documentation follows strict quality standards:
- ✅ Features verified to exist in codebase before documentation
- ✅ Code examples tested and working
- ✅ Status indicators for feature maturity
- ✅ Version requirements specified
- ❌ No marketing speak or unverified claims

See [Documentation Standards (Guides)](guides/CLAUDE.md) for guide writing standards.

---

## Need Help?

- **GitHub Issues**: [Report bugs or request features](https://github.com/teradata-labs/loom/issues)
- **Quick Start**: [Get up and running in 5 minutes](guides/quickstart.md)
- **Examples**: [Browse complete examples](../examples/)
