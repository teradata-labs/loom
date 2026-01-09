---
title: "Weaver Architecture"
weight: 4
---

# Weaver Architecture

**Version**: v1.0.0
**Status**: ✅ Implemented (simple agent approach)
**Last Updated**: 2025-12-31

## Table of Contents

- [Overview](#overview)
- [Design Philosophy](#design-philosophy)
- [System Context](#system-context)
- [Architecture](#architecture)
- [How It Works](#how-it-works)
- [Agent Configuration](#agent-configuration)
- [Design Rationale](#design-rationale)
- [Performance Characteristics](#performance-characteristics)
- [Future Considerations](#future-considerations)

---

## Overview

The Weaver is Loom's **agent generation system** that creates complete agent and workflow configurations from natural language requirements. Unlike traditional template-based approaches or complex multi-stage pipelines, the Weaver is **just a regular Loom agent** with specialized prompting and tools that enable it to discover, generate, and validate agent configurations.

**Key Capabilities**:
- **Natural Language Generation**: Creates k8s-style agent and workflow YAMLs from descriptions
- **Self-Discovery**: Uses `tool_search` to find relevant examples and tools
- **Validation**: Generates compliant configurations using reference examples
- **Hot-Reload Integration**: Saves to `~/.loom/agents/` and `~/.loom/workflows/` for immediate availability
- **Standardized Toolset**: All generated agents get core discovery tools by default

**Design Philosophy**: **Simplicity over complexity**. The weaver is a standard agent using standard RPCs (`Weave`/`StreamWeave`). No special services, no conflict resolution system, no multi-stage pipeline. It just works.

---

## Design Philosophy

### Why a Simple Agent?

**Previous Approach (v0.x)**: Complex coordinator with 6-stage pipeline, conflict detection, specialized sub-agents, and dedicated RPCs.

**Current Approach (v1.0+)**: Regular Loom agent with good prompting.

**Rationale**:
1. **Simpler is Better**: One agent using standard infrastructure vs. custom coordinator
2. **Works Better**: LLMs are good at following examples; let them do their job
3. **Easier to Maintain**: Standard agent configuration vs. complex pipeline code
4. **No Special Cases**: Uses `Weave`/`StreamWeave` like every other agent
5. **Conflict Resolution Unnecessary**: Good prompting + reference examples = correct output

### Core Principle

> "Give the weaver agent good examples, clear instructions, and standard tools. Let the LLM do what it's good at."

---

## System Context

```
┌─────────────────────────────────────────────────────────────────┐
│                         User Interface                          │
│                                                                 │
│    TUI Client: "Create a SQL optimizer agent"                   │
│                                                                 │
└──────────────────────────────────┬──────────────────────────────┘
                                   │
                                   │ Weave RPC
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                       Loom Server                               │
│                                                                 │
│  Standard Weave/StreamWeave RPCs                                │
│  (No special Weaver service)                                    │
│                                                                 │
└──────────────────────────────────┬──────────────────────────────┘
                                   │
                                   │ Execute agent
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Weaver Agent                                │
│                  (embedded/weaver.yaml)                         │
│                                                                 │
│  System Prompt:                                                 │
│    "You are the Weaver, a meta-agent specialized in            │
│     generating k8s-style agent and workflow configurations."    │
│                                                                 │
│  Tools:                                                         │
│    - shell_execute (read/write YAML files)                     │
│    - search_conversation (find prior context)                  │
│    - recall_conversation (retrieve specific info)              │
│    - clear_recalled_context (clean up)                         │
│                                                                 │
│  Key Instructions:                                              │
│    - Read ~/.loom/START_HERE.md for tips                       │
│    - Use ~/.loom/examples/agent-all-fields-example.yaml        │
│    - Use ~/.loom/examples/workflow-all-fields-example.yaml     │
│    - Give agents standard toolset + tool_search                │
│    - Save to ~/.loom/agents/ and ~/.loom/workflows/            │
│    - Enable memory compression with appropriate workload        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
                                   │
          ┌────────────────────────┴────────────────────┐
          │                                             │
          ▼                                             ▼
┌────────────────────┐                      ┌────────────────────┐
│  ~/.loom/agents/   │                      │  ~/.loom/workflows/│
│  sql-agent.yaml    │                      │  debate-flow.yaml  │
│  file-agent.yaml   │  Hot-reload ──────▶ │  pipeline.yaml     │
│  api-agent.yaml    │                      │  swarm.yaml        │
└────────────────────┘                      └────────────────────┘
           │                                             │
           │                                             │
           └─────────────────┬───────────────────────────┘
                             │
                             │ Immediately available
                             │
                             ▼
                    ┌─────────────────┐
                    │   User can run  │
                    │  generated agent│
                    │   via Weave RPC │
                    └─────────────────┘
```

---

## Architecture

### Component: Weaver Agent

**Location**: `embedded/weaver.yaml`

**Type**: Standard Loom agent (no special service)

**Configuration**:
```yaml
apiVersion: loom/v1
kind: Agent
metadata:
  name: weaver
  description: Meta-agent that generates k8s-style agent and workflow configurations

spec:
  system_prompt: |
    You are the Weaver, a meta-agent specialized in generating other agent
    and workflow configurations. You translate natural language requirements
    into production-ready k8s-style agent and workflow YAML.

    CRITICAL:
      - read ~/.loom/START_HERE.md for tips
      - agent definition MUST comply with ~/.loom/examples/agent-all-fields-example.yaml
      - workflow definition MUST comply with ~/.loom/examples/workflow-all-fields-example.yaml

    Core Responsibilities:
    1. Agent Generation: Create complete agent configurations from requirements
    2. Workflow Design: Design multi-agent workflows using orchestration patterns
    3. Tool Selection: Give agents standard toolset + tool_search for discovery
    4. Best Practices: Apply agent design principles and memory optimization
    5. Validation: Ensure compliance with reference examples

  tools:
    - shell_execute            # Read/write YAML files
    - search_conversation      # Find prior context
    - recall_conversation      # Retrieve specific info
    - clear_recalled_context   # Clean up context

  memory:
    type: sqlite
    max_history: 1000
    memory_compression:
      workload_profile: balanced

  config:
    max_turns: 1000
    max_tool_executions: 50
    enable_self_correction: true
```

### No Special Infrastructure

**What the Weaver DOESN'T have**:
- ❌ Dedicated gRPC service (uses standard `Weave`/`StreamWeave`)
- ❌ Conflict resolution system (good prompting eliminates conflicts)
- ❌ Multi-stage pipeline (LLM does it in one pass)
- ❌ Specialized sub-agents (one agent with good prompts)
- ❌ Custom coordinator (uses standard agent runtime)

**What the Weaver DOES have**:
- ✅ Clear system prompt with instructions
- ✅ Access to reference examples via file system
- ✅ Standard tools for reading/writing YAML
- ✅ Memory management for context
- ✅ Self-correction capabilities

---

## How It Works

### Generation Flow

```
User Request                           Weaver Agent Processing
     │                                         │
     │ "Create a SQL optimizer agent"          │
     │                                         │
     ▼                                         ▼
┌─────────────┐                      ┌─────────────────────────┐
│ Weave RPC   │─────────────────────▶│ 1. Read START_HERE.md  │
└─────────────┘                      └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 2. Read example YAML    │
                                     │    (agent-all-fields)   │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 3. Generate agent YAML  │
                                     │    following example    │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 4. Save to              │
                                     │    ~/.loom/agents/      │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 5. Hot-reload picks up  │
                                     │    new agent            │
                                     └────────────┬────────────┘
                                                  │
                                                  ▼
                                     ┌─────────────────────────┐
                                     │ 6. Return success msg   │
                                     │    to user              │
                                     └─────────────────────────┘
```

### Key Mechanisms

**1. Reference Examples**

The weaver reads complete example configurations:
- `~/.loom/examples/agent-all-fields-example.yaml` - Shows all agent fields
- `~/.loom/examples/workflow-all-fields-example.yaml` - Shows all workflow types

This ensures generated configs are **structurally correct** by construction.

**2. Standard Toolset**

All generated agents get these tools by default:
```yaml
tools:
  - shell_execute         # Execute commands, read files
  - tool_search           # Discover other tools dynamically
  - get_error_detail      # Debug tool errors
  - get_tool_result       # Retrieve tool outputs
  - search_conversation   # Find relevant conversation context
  - recall_conversation   # Retrieve specific prior information
  - clear_recalled_context # Clean up context cache
```

This gives agents **discovery capabilities** - they can find specialized tools as needed.

**3. Hot-Reload Integration**

Generated YAMLs are saved to:
- `~/.loom/agents/` - Agent configurations
- `~/.loom/workflows/` - Workflow definitions

The server's hot-reload system (89-143ms latency) automatically detects and loads new files.

**4. Memory Compression**

All generated agents enable memory compression:
```yaml
memory:
  memory_compression:
    workload_profile: balanced  # or data_intensive, conversational
```

This prevents context window overflow in long conversations.

---

## Agent Configuration

### Weaver System Prompt

The weaver's prompt emphasizes:

1. **Compliance**: Must follow reference examples exactly
2. **Completeness**: All required fields must be present
3. **Best Practices**: Memory compression, adequate max_turns, tool discovery
4. **Validation**: No LLM provider/model unless user specifies
5. **Discovery**: Always give agents tool_search for flexibility

### Critical Rules

The weaver enforces these via system prompt:

```
CRITICAL RULES:
- ALL agents MUST conform to agent-all-fields-example.yaml
- ALL workflows MUST conform to workflow-all-fields-example.yaml
- apiVersion MUST be "loom/v1" (not "loom.dev/v1")
- tools MUST be a simple list (not nested under "builtin:")
- ONLY give agents starter toolset and let them discover others
- config section (NOT "execution:")
- max_tool_executions (NOT "max_tool_calls")
- memory.type MUST be specified
- NO spec.agent.* nesting - all fields go directly under spec:
```

---

## Design Rationale

### Why This Approach Works

**1. LLMs Are Good at Following Examples**

Given a complete reference example, LLMs reliably generate structurally correct YAML. This is more reliable than:
- Template substitution (inflexible)
- Multi-stage pipelines (complex, brittle)
- Conflict detection (unnecessary with good examples)

**2. Simplicity Reduces Failure Modes**

Fewer components = fewer failure modes:
- One agent vs. coordinator + 6 sub-agents
- Standard RPCs vs. custom conflict resolution service
- File-based examples vs. in-memory templates

**3. Self-Discovery Scales Better**

Giving agents `tool_search` means:
- Weaver doesn't need to know all possible tools
- New MCP servers automatically available
- Agents adapt to available tooling

**4. Hot-Reload Provides Instant Feedback**

Saving to `~/.loom/agents/` means:
- No deployment step
- Immediate testing
- Fast iteration

### Trade-offs

**Chosen Approach: Simple Agent**
- ✅ Easy to maintain (one YAML file)
- ✅ Easy to understand (standard agent)
- ✅ Easy to extend (just edit system prompt)
- ✅ Works well in practice
- ❌ No fancy multi-stage pipeline
- ❌ No conflict detection system

**Alternative: Complex Coordinator (v0.x)**
- ❌ Hard to maintain (many components)
- ❌ Hard to understand (custom pipeline)
- ❌ Hard to extend (Go code changes)
- ❌ Unnecessary complexity
- ✅ Sophisticated architecture
- ✅ Conflict detection

**Verdict**: Simple agent approach is superior for this use case.

---

## Performance Characteristics

### Generation Latency

**Typical**: 5-15 seconds (single agent generation)
- LLM inference: 3-10 seconds
- File I/O: 100-500ms
- Hot-reload detection: 89-143ms

**Multi-Agent Workflow**: 15-45 seconds
- Multiple agent definitions
- Workflow configuration
- Pattern selection

**Cost**: $0.02-0.10 per generation (Claude Sonnet 4.5)

### Accuracy

**Measured Success Rate**: >95% (generates valid YAML first try)

**Common Failure Modes**:
1. User requests conflicting requirements
2. User asks for non-existent features
3. File system permissions issues

**Self-Correction**: Agent can retry with corrections if initial generation fails.

---

## Future Considerations

### Potential Enhancements

**1. Pattern Library Awareness**

Weaver could read pattern library metadata to suggest domain-specific patterns.

**Implementation**: Add pattern library path to system prompt, use `shell_execute` to read.

**2. Agent Templates**

Pre-built agent templates for common use cases (SQL expert, API caller, file processor).

**Implementation**: Store templates in `~/.loom/templates/`, reference in system prompt.

**3. Validation Hooks**

Programmatic validation of generated YAML before saving.

**Implementation**: Add `validate_agent_config` tool that checks schema compliance.

**4. Interactive Refinement**

Allow user to iterate on generated config ("make it more verbose", "add error handling").

**Implementation**: Already works via conversation history - weaver remembers prior generation.

### Non-Goals

**Will NOT Add**:
- ❌ Conflict resolution system (unnecessary)
- ❌ Multi-stage pipeline (complexity without benefit)
- ❌ Dedicated gRPC service (standard RPCs work fine)
- ❌ Template engine (examples + LLM is better)

---

## Related Documentation

- **Agent Configuration**: `/reference/agent-config.md` - Agent YAML schema
- **Workflow Orchestration**: `/architecture/orchestration.md` - Workflow patterns
- **Tool System**: `/architecture/tool-system.md` - Tool discovery and execution
- **Hot-Reload**: `/guides/hot-reload.md` - Pattern and agent hot-reload

---

**Key Takeaway**: The weaver is just a well-prompted Loom agent. No magic, no special infrastructure. It uses reference examples, standard tools, and hot-reload to generate agent configurations reliably and simply.
