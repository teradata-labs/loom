---
title: "Loom"
type: docs
bookFlatSection: false
bookCollapseSection: false
---

# Loom

Go framework for building LLM agents that learn from domain patterns and work together.

**Version**: v1.0.0-beta.2
**License**: Apache 2.0

---

## What It Does

Create AI agents that:
- Execute tasks using external tools (databases, APIs, files)
- Learn from domain-specific patterns (not just prompts)
- Work together in multi-agent workflows
- Stream progress in real-time
- Learn and improve themselves over time

---

## Get Started

### 1. Build

```bash
git clone https://github.com/Teradata-TIO/loom.git
cd loom
just build
```

### 2. Configure

```bash
# Set your LLM API key
bin/looms config set-key anthropic_api_key

# Start the server
bin/looms serve
```

### 3. Create an Agent

```bash
# Describe what you need in plain English
bin/looms weave "I need an agent to analyze files"

# Chat with it
bin/loom --thread <thread-name>
```

---

## Documentation

**[Getting Started →](docs/guides/quickstart/)**
Install and create your first agent in 5 minutes.

**[User Guides →](docs/guides/)**
How to use features: patterns, multi-agent workflows, MCP integration, evaluation.

**[Architecture →](docs/architecture/)**
System design for developers extending Loom.

**[Reference →](docs/reference/)**
Complete API specifications and CLI commands.

---

## Architecture

Loom integrates with:
- **LLM Providers**: Anthropic Claude, AWS Bedrock, Ollama, and more
- **Observability**: Hawk platform for tracing and metrics
- **Prompt Management**: Promptio for version-controlled prompts
- **External Tools**: Model Context Protocol (MCP) servers

Key packages:
- `pkg/agent/` - Conversation management with memory layers
- `pkg/metaagent/` - Natural language agent creation
- `pkg/shuttle/` - Tool execution system
- `pkg/patterns/` - Domain knowledge library
- `pkg/orchestration/` - Multi-agent workflows

---

## Repository

**GitHub**: [Teradata-TIO/loom](https://github.com/Teradata-TIO/loom)

**Issues**: [Report bugs or request features](https://github.com/Teradata-TIO/loom/issues)

---

## License

Apache 2.0 - See [LICENSE](https://github.com/Teradata-TIO/loom/blob/main/LICENSE)
