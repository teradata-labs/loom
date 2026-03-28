
# Weaver: Agent Creation via Conversation

**Version**: v1.2.0
**Status**: ✅ Implemented

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [How the Weaver Works](#how-the-weaver-works)
  - [Deployment](#deployment)
  - [Tools Available to the Weaver](#tools-available-to-the-weaver)
  - [The WEAVER.rom Ground-Truth Reference](#the-weaverrom-ground-truth-reference)
- [Planning Modes](#planning-modes)
  - [Quick Start Mode](#quick-start-mode)
  - [/agent-plan Mode (Guided Planning)](#agent-plan-mode-guided-planning)
- [Creating Agents](#creating-agents)
  - [Single Agent](#single-agent)
  - [Multi-Agent Workflow](#multi-agent-workflow)
- [Creating Skills](#creating-skills)
  - [Skill Activation Modes](#skill-activation-modes)
- [Workflow Types](#workflow-types)
  - [Orchestration Workflows](#orchestration-workflows)
  - [Coordination Workflows](#coordination-workflows)
- [Examples](#examples)
  - [Example 1: SQL Optimizer Agent](#example-1-sql-optimizer-agent)
  - [Example 2: Multi-Agent Debate Workflow](#example-2-multi-agent-debate-workflow)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)


## Overview

The weaver is a **Loom agent** that creates other agent and workflow configurations through conversational interaction. Instead of writing YAML by hand, you chat with the weaver in the TUI and describe what you need. The weaver uses the `agent_management` tool to generate, validate, and save configuration files to your `$LOOM_DATA_DIR`.

The weaver is not a CLI subcommand. There is no `looms weave` command. You interact with the weaver the same way you interact with any other Loom agent: through the TUI client.


## Prerequisites

- Loom v1.2.0+
- LLM provider configured (Anthropic, Bedrock, OpenAI, Azure OpenAI, Gemini, Mistral, Ollama, or HuggingFace)
- Loom server running (`looms serve`)

Verify setup:

```bash
# Start the server (default port 60051)
looms serve

# In another terminal, confirm the weaver is listed
loom agents
```


## Quick Start

1. Start the Loom server if it is not already running:

```bash
looms serve
```

2. Connect to the weaver thread via the TUI:

```bash
loom --thread weaver
```

3. Describe the agent you want:

```
You: Create an agent that analyzes PostgreSQL slow queries and suggests indexes

Weaver: [Uses agent_management to create the agent YAML]
        [Validates the configuration]
        [Saves to $LOOM_DATA_DIR/agents/postgres-slow-query-analyzer.yaml]

        Agent created! The server will hot-reload it automatically.
        Connect to it with: loom --thread postgres-slow-query-analyzer
```

4. Connect to your new agent:

```bash
loom --thread postgres-slow-query-analyzer
```


## How the Weaver Works

### Deployment

When you run `looms serve`, the server automatically deploys the weaver agent to `$LOOM_DATA_DIR/agents/weaver.yaml` (if it does not already exist). The source lives at `embedded/weaver.yaml` in the Loom codebase (embedded into the binary at compile time via `//go:embed`).

The server also deploys a `weaver-creation` skill to `$LOOM_DATA_DIR/skills/weaver-creation.yaml`.

If you already have a customized `weaver.yaml` in your agents directory, the server will not overwrite it.

### Tools Available to the Weaver

The weaver has three tools configured in its agent spec:

| Tool | Purpose |
|------|---------|
| `agent_management` | Create, update, read, list, validate, and delete agent/workflow/skill YAML files. Actions include `create_agent`, `create_workflow`, `create_skill`, `update_agent`, `update_workflow`, `update_skill`, `read`, `list`, `validate`, `delete`. |
| `shell_execute` | Run shell commands. The weaver's working directory defaults to `$LOOM_SANDBOX_DIR` (which itself defaults to `$LOOM_DATA_DIR`). An explicit `working_dir` parameter overrides this. |
| `tool_search` | Discover available tools via FTS search. The weaver uses this to find the right tools for the agents it creates. |

The `agent_management` tool is security-restricted: only the weaver and guide agents can use it. The guide agent is further restricted to read-only access (`list` and `read` only).

### The WEAVER.rom Ground-Truth Reference

The weaver loads a ROM (Read-Only Memory) file at `pkg/agent/roms/WEAVER.rom`. This file contains the ground-truth schema definitions for:

- Agent YAML structure (`apiVersion: loom/v1`, `kind: Agent`)
- The full tool availability matrix (configurable tools, auto-registered tools, workflow-injected tools)
- All 7 orchestration workflow types with their required fields (`debate`, `fork-join`, `pipeline`, `parallel`, `swarm`, `conditional`, `iterative`)
- Event-driven coordination workflow patterns (`hub-and-spoke`, `peer-to-peer-pub-sub`)
- Communication rules (event-driven messaging, no `receive_message` tool)
- Common mistakes to avoid
- Minimal templates for each workflow type

The ROM ensures the weaver generates valid YAML that conforms to the actual Loom schema, rather than relying on LLM training data.


## Planning Modes

When you first interact with the weaver, it offers two approaches:

### Quick Start Mode

For users who know what they need. Describe your agent and the weaver creates it immediately.

```
You: Create a file analyzer that reads and searches code

Weaver: [Analyzes requirements]
        [Discovers tools via tool_search]
        [Creates agent via agent_management]
        [Validates configuration]

        Done! Agent saved to $LOOM_DATA_DIR/agents/file-analyzer.yaml
        Connect with: loom --thread file-analyzer
```

### /agent-plan Mode (Guided Planning)

For complex agents or when you need help defining requirements. The weaver guides you through 5 structured phases:

1. **Problem Understanding** -- What problem are you solving? How will you measure success?
2. **Technical Requirements** -- What database/language/stack? What access level?
3. **Skill Recommendation** -- The weaver recommends existing skills or offers to create new ones
4. **Workflow Design** -- (Multi-agent only) Which pattern fits: pipeline, parallel, debate, coordinator?
5. **Confirmation and Creation** -- Summary of everything, then create on your approval

Activate it by typing `/agent-plan` or choosing option 2 when the weaver offers:

```
You: /agent-plan

Weaver: Let's plan your agent. What specific problem are you solving?
```


## Creating Agents

### Single Agent

Be specific about your requirements for better results:

```
-- Less specific (weaver has to guess more):
You: Create an agent for SQL

-- More specific (weaver generates a more useful agent):
You: Create a PostgreSQL agent that optimizes slow queries, suggests indexes,
     and explains query execution plans. It should use read-only access.
```

The weaver calls `agent_management` with `action="create_agent"` to write the YAML. Validation errors are returned immediately, and the weaver will fix them and retry.

Agents are saved to `$LOOM_DATA_DIR/agents/<agent-name>.yaml` and hot-reloaded by the server.

### Multi-Agent Workflow

The weaver determines workflow type based on your description:

- "debate" or "best approach" --> Debate workflow (agents argue merits, reach consensus)
- "vote" or "independently" --> Swarm workflow (agents vote, majority/supermajority/unanimous)
- "then" or "pipeline" or "sequential" --> Pipeline workflow (output feeds next stage)
- "simultaneously" or "parallel" --> Parallel workflow (agents work on different tasks concurrently)
- "coordinate" or "collaborate" --> Coordination workflow (event-driven, agents communicate freely)

The weaver always creates agents first, then creates the workflow that references them.


## Creating Skills

The weaver can create skills -- LLM-agnostic prompt injections that provide domain expertise to agents. The weaver always asks for your consent before creating a new skill.

```
You: Create an agent for analyzing Python performance

Weaver: I don't have a Python performance skill yet. I can create one that would:
        - Provide specialized knowledge about cProfile, memory_profiler, and py-spy
        - Activate when you mention 'slow python' or use the /perf command
        - Be reusable across any Python-focused agent

        Would you like me to create this skill? (yes to create, skip to continue without it)

You: yes

Weaver: [Uses agent_management with action="create_skill"]
        [Saves to $LOOM_DATA_DIR/skills/python-performance-analysis.yaml]
        [Configures the skill in the agent's spec.skills section]
```

### Skill Activation Modes

| Mode | Behavior |
|------|----------|
| `MANUAL` | Only via slash command (e.g., `/perf`) |
| `AUTO` | Automatically when keywords are detected (e.g., "slow python") |
| `HYBRID` | Both slash command and keyword activation |
| `ALWAYS` | Injected on every agent turn |


## Workflow Types

The weaver can generate two categories of workflows.

### Orchestration Workflows

Deterministic, executor-driven patterns. These use the `spec.type` field in the workflow YAML.

| Type | Description | Key Fields |
|------|-------------|------------|
| `debate` | Agents argue merits, reach consensus | `topic`, `agent_ids`, `rounds`, `merge_strategy` |
| `fork-join` | Same prompt to all agents, results merged | `prompt`, `agent_ids`, `merge_strategy` |
| `pipeline` | Sequential stages, output feeds next | `initial_prompt`, `stages` |
| `parallel` | Each agent gets a unique task | `tasks` (each with `agent_id` and `prompt`) |
| `swarm` | Agents vote on a question | `question`, `agent_ids`, `strategy` |
| `conditional` | Classifier routes to sub-workflows | `condition_agent_id`, `condition_prompt`, `branches`, `default_branch` |
| `iterative` | Pipeline with repeated iterations | `max_iterations`, `pipeline` |

### Coordination Workflows

Event-driven, agent-autonomous patterns. These use the `spec.entrypoint` field.

| Pattern | Description |
|---------|-------------|
| `hub-and-spoke` | One coordinator sends/receives from workers via `send_message` |
| `peer-to-peer-pub-sub` | Agents publish/subscribe to topics via `publish` |

Messages are event-driven and auto-delivered. There is no `receive_message` tool -- agents receive messages as injected context in their conversation.


## Examples

### Example 1: SQL Optimizer Agent

Connect to the weaver and request a single agent:

```bash
loom --thread weaver
```

```
You: Create a PostgreSQL optimizer that analyzes slow queries and suggests indexes

Weaver: I'll create that for you. Let me find the right tools...

        [Uses tool_search to discover available tools]
        [Uses agent_management action="create_agent" with config:]

        apiVersion: loom/v1
        kind: Agent
        metadata:
          name: postgres-optimizer
          description: Analyzes PostgreSQL queries for performance issues
          labels:
            domain: sql
        spec:
          system_prompt: |
            Analyze PostgreSQL queries for performance issues.
            Use EXPLAIN ANALYZE to identify bottlenecks.
            Suggest index improvements and query rewrites.
          tools:
            - shell_execute
            - file_read
            - tool_search

        Agent saved to: $LOOM_DATA_DIR/agents/postgres-optimizer.yaml
        The server will hot-reload it automatically.
        Connect with: loom --thread postgres-optimizer
```

Use the new agent:

```bash
loom --thread postgres-optimizer
```

```
You: Analyze this query: SELECT * FROM orders WHERE customer_id = 123
```

### Example 2: Multi-Agent Debate Workflow

```bash
loom --thread weaver
```

```
You: Build a SQL optimizer where 3 experts debate the best query plan

Weaver: I'll create a debate workflow with 3 specialist agents.

        [Creates agent: index-optimizer]
        [Creates agent: query-rewriter]
        [Creates agent: join-optimizer]
        [Creates workflow: sql-optimizer-debate with type=debate, rounds=3]

        Created 3 agents and 1 workflow:

        Agents:
        1. index-optimizer - Suggests indexing strategies
        2. query-rewriter - Proposes query rewrite optimizations
        3. join-optimizer - Optimizes join operations and order

        Workflow: sql-optimizer-debate
        Type: debate (3 rounds, synthesis merge)

        Connect with: loom --thread sql-optimizer-debate
```

Use the workflow:

```bash
loom --thread sql-optimizer-debate
```

```
You: Optimize this slow query: SELECT * FROM large_table JOIN another_table ON ...
```


## Troubleshooting

### Cannot find the weaver thread

**Problem:** `loom --thread weaver` says agent not found.

**Check:**

```bash
# Verify the weaver was deployed
ls $LOOM_DATA_DIR/agents/weaver.yaml

# If missing, restart the server -- it deploys on startup
looms serve
```

### Weaver creates agents but they do not appear

**Problem:** The weaver says it created an agent, but `loom --thread <name>` fails.

**Check:**

```bash
# Verify the YAML file exists
ls $LOOM_DATA_DIR/agents/

# Check server logs for validation errors
# The server validates YAML on hot-reload

# Force reload by restarting the server
looms serve
```

### Weaver generates invalid YAML

**Problem:** The `agent_management` tool returns validation errors.

The weaver's self-correction is enabled (`enable_self_correction: true`), so it will typically read the error message and retry with a fixed configuration. If it keeps failing:

- Make sure the WEAVER.rom is present (it ships with Loom and is loaded at agent startup)
- Be more specific in your requirements -- vague requests lead to more guesswork
- Try `/agent-plan` mode for structured guidance

### Connection to server fails

**Problem:** `loom --thread weaver` cannot connect.

**Check:**

```bash
# Default server address is 127.0.0.1:60051
# If using a different port, specify it:
loom --server 127.0.0.1:<port> --thread weaver
```


## Next Steps

- [Weaver Usage Guide](./weaver-usage.md) -- Additional examples and skill recommendations
- [Pattern Library Guide](./pattern-library-guide.md) -- Available patterns for agents
- [Zero-Code Implementation](./zero-code-implementation-guide.md) -- Manual agent configuration (without the weaver)
- [MCP Integration](./integration/mcp-readme.md) -- External tool integration
