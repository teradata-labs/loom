---
title: "Reference"
weight: 20
bookCollapseSection: false
---

# API Reference

Technical reference documentation for Loom APIs, configuration, and components.

**Version**: v1.0.0-beta.2

---

## Core APIs

Complete specifications for Loom's primary interfaces.

- [Agent Configuration](agent-configuration/) - Agent YAML configuration reference with all parameters
- [Backend Reference](backend/) - Backend interface, implementations, and configuration
- [CLI Reference](cli/) - Complete command-line interface documentation
- [Streaming](streaming/) - Token-by-token streaming API and configuration
- [Tool Registry](tool-registry/) - Tool indexing, FTS5 search, and LLM-assisted discovery

---

## LLM Providers

Complete integration guides for all supported LLM providers.

### Provider Overview
- [LLM Providers Overview](llm-providers/) - All supported providers, feature comparison, migration guide

### Production Providers
- [Anthropic](llm-anthropic/) - ✅ Claude integration (Sonnet, Opus, Haiku)
- [AWS Bedrock](llm-bedrock/) - ✅ Bedrock integration (Multi-region, IAM auth)
- [Ollama](llm-ollama/) - ✅ Local models (Free, private, offline)
- [OpenAI](llm-openai/) - ✅ OpenAI integration (GPT-4, GPT-3.5)
- [Azure OpenAI](llm-azure-openai/) - ✅ Azure integration (Managed Identity)
- [Mistral AI](llm-mistral/) - ✅ Mistral integration (Mistral Large, Mixtral)
- [Google Gemini](llm-gemini/) - ✅ Gemini integration (Long context, multimodal)
- [HuggingFace](llm-huggingface/) - ✅ HuggingFace Inference API

### Planned Providers
- [Google Vertex AI](llm-vertex-ai/) - 📋 Planned (GCP infrastructure)

---

## Patterns & Learning

Pattern library and intelligent agent behaviors.

- [Pattern Reference](patterns/) - Pattern library specification, YAML schema, hot reload
- [Pattern Recommendations](pattern-recommendations/) - Pattern selection guide by use case
- [Self-Correction](self-correction/) - Error recovery and retry strategies
- [Meta-Agent Tools](meta-agent-tools/) - Factory tooling and agent composition

---

## Workflows & Orchestration

Multi-agent coordination and workflow patterns.

- [Workflow Iterative](workflow-iterative/) - Iterative workflow patterns (loops, retries, refinement)
- [Presentation Tools](presentation-tools/) - Visualization and output formatting tools

---

## Advanced Features

Advanced configuration and optimization.

- [Prompt Hot Reload](prompt-hot-reload/) - Live prompt updates without restart
- [TUI](tui/) - Terminal UI reference and keybindings
- [TLS](tls/) - TLS/mTLS configuration for secure communication
- [SQLite Guidance](sqlite-guidance/) - SQLite best practices for session storage

---

## Quick Links

### By Task
- **Configure an agent**: [Agent Configuration](agent-configuration/)
- **Choose an LLM provider**: [LLM Providers Overview](llm-providers/)
- **Use CLI commands**: [CLI Reference](cli/)
- **Enable streaming**: [Streaming](streaming/)
- **Create patterns**: [Pattern Reference](patterns/)
- **Set up local inference**: [Ollama](llm-ollama/)
- **Secure with TLS**: [TLS](tls/)
- **Search for tools**: [Tool Registry](tool-registry/)

### By Provider
- **Anthropic Claude**: [llm-anthropic](llm-anthropic/)
- **AWS Bedrock**: [llm-bedrock](llm-bedrock/)
- **Local (free)**: [llm-ollama](llm-ollama/)
- **OpenAI**: [llm-openai](llm-openai/)
- **Azure**: [llm-azure-openai](llm-azure-openai/)
- **Mistral**: [llm-mistral](llm-mistral/)

---

## Documentation Standards

All reference documentation follows these principles:

1. **Complete specifications** - Every parameter, flag, option documented
2. **Exact types and constraints** - Precise types (string, int, float64), defaults, ranges
3. **Working examples** - Copy-paste runnable code with expected output
4. **Error codes** - Structured error documentation with resolutions
5. **No marketing speak** - Technical facts only, no superlatives

See [CLAUDE.md](../../../CLAUDE.md) for full documentation standards.

---

## See Also

- [Guides](../../guides/) - Task-oriented how-to documentation
- [Concepts](../../concepts/) - Architecture and design concepts
- [Development](../../development/) - Contributing and development guides
