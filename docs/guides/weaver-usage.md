
# Weaver Usage Guide

**Version**: v1.2.0
**Status**: ✅ Implemented

> **What's New in v1.2.0**:
> - ✨ /agent-plan mode - Guided planning with 5 structured phases
> - ✨ Skills recommendations - Intelligent skill suggestions based on problem domain
> - ✨ Custom skill creation - Weaver can create skills with user consent

*Note: This guide is maintained separately. For the full weaver documentation that includes architecture details, see [Meta-Agent Usage Guide](./meta-agent-usage.md).*

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Planning Modes](#planning-modes)
  - [Quick Start Mode](#quick-start-mode)
  - [/agent-plan Mode (Guided Planning)](#agent-plan-mode-guided-planning)
- [Skills Recommendations](#skills-recommendations)
  - [Built-in Skills](#built-in-skills)
  - [Creating Custom Skills](#creating-custom-skills)
- [Creating Agents](#creating-agents)
  - [Single Agent](#single-agent)
  - [Multi-Agent Workflow](#multi-agent-workflow)
  - [With Artifact Context](#with-artifact-context)
- [Examples](#examples)
  - [Example 1: SQL Optimizer](#example-1-sql-optimizer)
  - [Example 2: Multi-Agent Debate](#example-2-multi-agent-debate)
  - [Example 3: With File Context](#example-3-with-file-context)
  - [Example 4: Using /agent-plan Mode](#example-4-using-agent-plan-mode)
- [Special Commands](#special-commands)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)


## Overview

The Weaver creates complete agent configurations from natural language requirements. Instead of writing YAML by hand, describe what you need in plain English and the Weaver generates:

- Agent configuration (tools, patterns, prompts)
- Multi-agent workflows (debate, swarm, pipeline patterns)
- Backend connections (SQL, REST API, files)
- Hot-reloaded configurations (available immediately)

**Key characteristics:**
- **Intelligent**: Uses LLM-powered analysis (not keyword matching)
- **Metadata-Driven**: Reads self-describing tool and pattern metadata
- **Interactive**: Pauses for user decisions on ambiguous requirements

## Prerequisites

- Loom v1.2.0+
- LLM provider configured (Anthropic, Bedrock, OpenAI, Azure OpenAI, Gemini, Mistral, Ollama, or HuggingFace)
- Running Loom server: `bin/looms serve`

Verify setup:

```bash
# Check LLM configuration (retrieve key from system keyring)
bin/looms config get-key anthropic_api_key

# Start server if not running
bin/looms serve
```

## Quick Start

Create your first agent:

```bash
# Start Loom TUI
bin/loom

# In the conversation:
User: "Create an agent to analyze PostgreSQL slow queries"

# Wait 20-30 seconds...
# Weaver generates configuration

# Expected output:
🤖 Single Agent Generated

Analysis:
- Domain: sql
- Complexity: single
- Intent: analyze
- Suggested Name: postgres-slow-query-analyzer

Configuration saved to: $LOOM_DATA_DIR/agents/postgres-slow-query-analyzer.yaml
The server will hot-reload the agent automatically
Connect to the agent: loom --thread postgres-slow-query-analyzer
```

The agent is now available immediately (hot-reloaded).


## Planning Modes

> **New in v1.2.0**: Weaver now offers two planning modes - Quick Start for immediate creation, or /agent-plan mode for structured guidance.

When you first interact with Weaver, it will offer you a choice:

```
Weaver: "I can help you create that! Would you like me to:

1. Quick Start - Create immediately based on your description
2. /agent-plan Mode - Guided planning with structured questions

Which approach would you prefer?"
```

### Quick Start Mode

**Best for:** Users who know exactly what they need.

Simply describe your agent and Weaver creates it immediately:

```bash
User: "Create a SQL query optimizer for PostgreSQL"

# Weaver analyzes requirements
# Recommends relevant skills
# Creates agent configuration
# Done in 20-30 seconds
```

**When to use:**
- ✅ You have clear requirements
- ✅ You want fast turnaround
- ✅ You're familiar with agent capabilities

### /agent-plan Mode (Guided Planning)

**Best for:** Complex agents or when you need help defining requirements.

Weaver guides you through 5 structured phases:

#### Phase 1: Problem Understanding
```
Weaver: "What specific problem are you solving?"
User: "Our PostgreSQL queries are getting slower as data grows"

Weaver: "How will you know the agent is working well?"
User: "If it can identify why queries are slow and suggest fixes"
```

#### Phase 2: Technical Requirements
```
Weaver: "What database are you using?"
User: "PostgreSQL 14"

Weaver: "Do you need read-only access or write access too?"
User: "Read-only for now"
```

#### Phase 3: Skill Recommendation
```
Weaver: "Based on your needs, I recommend:

🎯 sql-optimization (/optimize) - RECOMMENDED
   Analyzes query execution plans and suggests improvements
   Includes index recommendations and query rewrites

Would you like me to include this skill? (yes/skip)"

User: "yes"
```

#### Phase 4: Workflow Design
(Only for multi-agent scenarios)

```
Weaver: "You mentioned multiple agents. Which pattern fits best?

1. Pipeline - Sequential processing (output of one feeds next)
2. Parallel - All agents work simultaneously, results merged
3. Debate - Agents propose solutions, debate merits, reach consensus
4. Coordinator - One orchestrator delegates to specialists"
```

#### Phase 5: Confirmation & Creation
```
Weaver: "Here's what I'm creating:

Agent Name: postgresql-query-optimizer
Purpose: Analyze slow PostgreSQL queries and recommend optimizations
Database: PostgreSQL (read-only)
Tools: execute_sql, tool_search
Skills: sql-optimization (/optimize)
Success: Identify bottlenecks and suggest fixes

Ready to create? (yes to proceed, no to revise)"

User: "yes"
```

**When to use:**
- ✅ You're new to Loom agents
- ✅ Requirements are unclear
- ✅ You want skill recommendations
- ✅ Building complex multi-agent systems

**How to activate:**
```bash
# Option 1: Choose when Weaver offers
User: "Create a SQL agent"
Weaver: "Quick Start or /agent-plan Mode?"
User: "2"  # Choose /agent-plan

# Option 2: Use the command directly
User: "/agent-plan"
```


## Skills Recommendations

> **New in v1.2.0**: Weaver recommends and creates skills to enhance your agents.

Skills are LLM-agnostic prompt injections that provide domain expertise. Weaver automatically recommends relevant skills based on your problem domain.

### Built-in Skills

Weaver knows about these existing skills:

| Skill | Activation | Use Case | Status |
|-------|-----------|----------|--------|
| `sql-optimization` | `/optimize` or "slow query" | Database performance analysis | ✅ Implemented |
| `code-review` | `/review` or "review code" | Security and quality checks | ✅ Implemented |
| `data-quality-check` | `/dq` or "validate data" | Data validation and cleaning | ✅ Implemented |
| `multi-agent-coordinator` | `/coordinate` | Task delegation across agents | ⚠️ Catalog only |
| `agent-discovery` | `/find-agent` | Locate specialist agents | ⚠️ Catalog only |
| `request-response-coordinator` | `/ask-agent` | Synchronous agent communication | ⚠️ Catalog only |

**Example recommendation:**

```
User: "Create an agent for PostgreSQL optimization"

Weaver: "I recommend the sql-optimization skill because:
• Provides specialized knowledge about query optimization
• Automatically activates with /optimize or when you mention 'slow query'
• Includes EXPLAIN plan analysis and index recommendations
• Reusable across any SQL-focused agent

This skill is already available. Would you like me to configure it? (yes/skip)"
```

### Creating Custom Skills

If no existing skill matches your needs, Weaver can create one.

**Weaver asks before creating:**

```
User: "Create an agent for analyzing Python performance"

Weaver: "I don't have a Python performance skill yet. I recommend creating one because:
• It would provide specialized knowledge about Python profiling and optimization
• Automatically activates when you mention 'slow python' or use /perf command
• Includes cProfile, memory_profiler, and py-spy guidance
• Reusable across any Python-focused agent you create

Would you like me to create this skill? (yes to create, skip to continue without it)"

User: "yes"

Weaver: [Creates skill at $LOOM_DATA_DIR/skills/python-performance-analysis.yaml]
        [Configures it in the agent]
        ✅ Skill created and configured!
```

**User control:**
- ✅ Weaver ALWAYS asks before creating skills
- ✅ You choose "yes" to create or "skip" to continue without
- ✅ Clear explanation of what the skill does and why it's beneficial
- ✅ Skills saved to `$LOOM_DATA_DIR/skills/` for reuse

**Skill activation modes:**
- `MANUAL` - Only via slash command (e.g., `/perf`)
- `AUTO` - Automatically when keywords detected (e.g., "slow python")
- `HYBRID` - Both slash command and keywords
- `ALWAYS` - Injected on every agent turn


## Creating Agents

### Single Agent

**Basic creation:**

```
User: "Create a file analyzer that can read and search code"

Weaver generates:
- Name: file-analyzer
- Domain: file
- Tools: read_file, list_files, search_files
- Patterns: file_analysis, code_search
- Backend: examples/backends/file.yaml
```

**Expected response time:** 20-30 seconds

**Approximate token cost:** ~$0.02-0.04 (Claude Sonnet 4, varies by model and prompt length)

**Be specific for better results:**

```
❌ Vague: "Create an agent for SQL"

✅ Specific: "Create a PostgreSQL agent that optimizes slow queries, suggests indexes, and retries failed queries"
```

### Multi-Agent Workflow

**Trigger keywords** for multi-agent workflows:
- "debate" → Debate pattern (3-5 expert agents, consensus)
- "vote" / "independently" → Swarm pattern (5-7 evaluators, voting)
- "then" / "pipeline" → Pipeline pattern (sequential stages)
- "writes" + "reviews" → Pair programming pattern

**Example:**

```
User: "Create a SQL optimizer where 3 experts debate the best query plan"

Weaver generates:
- Workflow pattern: debate
- Agents: 3 (index-expert, query-rewrite-expert, join-expert)
- Orchestration: 5 debate rounds, consensus merge
- Configuration: $LOOM_DATA_DIR/workflows/sql-debate-optimizer.yaml
```

**Expected response time:** 45-60 seconds (longer for workflow design)

**Approximate token cost:** ~$0.06-0.10 (varies by model and workflow complexity)

### With Artifact Context

Provide file context to inform generation. Attach files by dragging them into the TUI or pasting a file path:

```
# Attach a CSV file (drag into TUI or paste file path)
[Attached: sales_data.csv]

# Generate agent with context
User: "Create an agent to analyze this sales data"

Weaver:
1. Reads sales_data.csv artifact
2. Detects CSV format, columns (date, product, quantity, revenue)
3. Suggests tools: read_csv, aggregate_data, handle_nulls
4. Generates agent with CSV-specific validation patterns
```

**Benefits:**
- More specific tool selection
- Custom validation patterns based on actual data
- Data quality suggestions (handle NULLs, outliers)

**Privacy note:** Artifact content sent to LLM for analysis. Avoid sensitive data.


## Examples

### Example 1: SQL Optimizer

**Requirements:**

```
User: "Create a PostgreSQL optimizer that analyzes slow queries and suggests indexes"
```

**Generation output:**

```
🤖 Single Agent Generated

Analysis:
- Domain: sql
- Complexity: single
- Intent: optimize
- Suggested Name: postgres-optimizer

Capabilities: execute_sql, explain_plan, suggest_indexes

Configuration Preview:
name: postgres-optimizer
description: PostgreSQL query optimizer that analyzes performance
domain: sql
backend:
  type: postgres
  connection_string: ${POSTGRES_URL}
tools:
  - execute_sql
  - get_schema
  - explain_plan
patterns:
  - sql_optimization
  - missing_index_detection
system_prompt: |
  Analyze PostgreSQL queries for performance issues. Suggest index
  improvements and explain query plans.

Next Steps:
1. Configuration saved to: $LOOM_DATA_DIR/agents/postgres-optimizer.yaml
2. The server will hot-reload the agent automatically
3. Connect to the agent: loom --thread postgres-optimizer
```

**Usage:**

```bash
# Connect to generated agent
bin/loom --thread postgres-optimizer

# Start optimizing
User: "Analyze this query: SELECT * FROM orders WHERE customer_id = 123"
```

### Example 2: Multi-Agent Debate

**Requirements:**

```
User: "Build a SQL optimizer where 3 experts debate the best approach"
```

**Generation output:**

```
🔄 Multi-Agent Workflow Generated

Workflow: sql-optimizer-debate
Agents: 3

Analysis:
- Domain: sql
- Complexity: multi-agent
- Workflow Type: debate
- Judge: enabled (performance, correctness)

Agents:
1. index-optimizer
   Role: Suggests indexing strategies
   Tools: analyze_index_usage, suggest_indexes

2. query-rewriter
   Role: Proposes query rewrite optimizations
   Tools: rewrite_query, analyze_complexity

3. join-optimizer
   Role: Optimizes join operations and order
   Tools: analyze_joins, suggest_join_order

Orchestration:
  Type: debate
  Max Rounds: 5
  Merge Strategy: consensus

Next Steps:
1. Configuration saved to: $LOOM_DATA_DIR/workflows/sql-optimizer-debate.yaml
2. The server will hot-reload the workflow automatically
3. Connect to the workflow: loom --thread sql-optimizer-debate
```

**Usage:**

```bash
bin/loom --thread sql-optimizer-debate

User: "Optimize this slow query: SELECT * FROM large_table JOIN another_table..."

# 3 experts analyze independently
# Each proposes optimization
# Agents debate merits (5 rounds)
# Consensus optimization generated
```

### Example 3: With File Context

**Requirements:**

```
# Attach CSV (drag into TUI or paste file path)
[Attached: customer_data.csv]

# Generate with context
User: "Create an agent to process this customer data"
```

**Weaver analysis:**

```
Reading artifact: customer_data.csv (500KB)
Content type: text/csv
Columns: id, name, email, created_at, status (10,000 rows)
Notes: NULL values in 'email' column (2%), duplicate IDs detected (5 rows)

Generating agent with context...
```

**Generation output:**

```
🤖 Single Agent Generated

Analysis:
- Domain: data
- Complexity: single
- Intent: process
- Suggested Name: customer-data-processor

Capabilities: read_csv, validate_data, handle_duplicates, handle_nulls

Configuration includes:
- CSV parsing with null handling
- Duplicate detection validation
- Email format validation
- Data quality reporting

Patterns:
- csv_processing
- data_validation
- null_handling
- duplicate_detection
```

### Example 4: Using /agent-plan Mode

**Scenario:** You want to create a Python performance analyzer but aren't sure of all the requirements.

**Full conversation:**

```
User: "I need help with slow Python code"

Weaver: "I can help you create that! Would you like me to:

1. Quick Start - Create immediately based on your description
2. /agent-plan Mode - Guided planning with structured questions

Which approach would you prefer?"

User: "2"

Weaver: "Let's plan this out. What specific problem are you solving?"

User: "Our Python ETL scripts are taking too long to process data"

Weaver: "How will you know the agent is working well?"

User: "If it can identify bottlenecks and suggest specific optimizations"

Weaver: "What Python version are you using?"

User: "Python 3.11"

Weaver: "Do you need the agent to profile running code, or analyze static code files?"

User: "Both would be helpful"

Weaver: "I don't have a Python performance skill yet. I recommend creating one because:
• It would provide specialized knowledge about Python profiling and optimization
• Automatically activates when you mention 'slow python' or use /perf command
• Includes cProfile, memory_profiler, and py-spy guidance
• Reusable across any Python-focused agent you create

Would you like me to create this skill? (yes to create, skip to continue without it)"

User: "yes"

Weaver: "Great! Here's what I'm creating:

Agent Name: python-performance-analyzer
Purpose: Identify bottlenecks in Python code and suggest optimizations
Python Version: 3.11
Tools: file_read, shell_execute, tool_search
Skills: python-performance-analysis (/perf) - CREATED
Capabilities:
  • Profile running Python scripts
  • Analyze static code for performance issues
  • Suggest specific optimizations
Success: Identify bottlenecks and provide actionable fixes

Ready to create? (yes to proceed, no to revise)"

User: "yes"

Weaver: [Creates skill at $LOOM_DATA_DIR/skills/python-performance-analysis.yaml]
        [Creates agent at $LOOM_DATA_DIR/agents/python-performance-analyzer.yaml]

✅ Created python-performance-analyzer!

To use it:
loom --thread python-performance-analyzer

The python-performance-analysis skill activates when you:
- Use /perf command
- Mention 'slow python' or 'bottleneck'

Example: "This function is slow: def process_data()..."

Come back anytime if you need changes!
```

**Key benefits:**
- ✅ Guided through requirement gathering
- ✅ Discovered need for custom skill
- ✅ Understood skill benefits before consenting
- ✅ Agent configured with optimal tools and skills


## Special Commands

### /agent-plan - Guided Planning Mode

Start structured planning mode with 5 phases:

```
User: "/agent-plan"

Weaver: "Let's plan your agent. What specific problem are you solving?"
# Proceeds through 5-phase guided planning
```

See [/agent-plan Mode](#agent-plan-mode-guided-planning) for details.

### /agents - Open Agents Dialog

Opens the agents selection dialog (also accessible via `ctrl+e`). The dialog shows all available agents and lets you switch between them.

```
User: "/agents"

# Opens interactive agent picker dialog
# Select an agent to switch to it
# Press esc to close
```

To list agents from the CLI without opening the TUI:

```bash
loom agents
```

### /help - Show Help

The `/help` command opens a generic help dialog listing available slash commands and keyboard shortcuts. It is not weaver-specific; it shows the same help content regardless of which agent is active.


## Troubleshooting

### Generation Takes Too Long

**Problem:** Weaver stuck for >2 minutes.

**Possible causes:**
1. **LLM timeout**: Check network connection to LLM provider
2. **Large metadata**: Many tools/patterns increase processing time
3. **Artifact analysis**: Large artifact files (>100KB) slow generation

**Solutions:**
```bash
# Check LLM connectivity
curl https://api.anthropic.com/v1/messages

# Reduce artifact size
# Upload summarized data instead of full file

# Check server logs (logs go to stdout by default)
# If you configured logging.file in looms.yaml, check that file instead
```

### Weaver Generates Wrong Domain

**Problem:** Requirements for SQL agent, but Weaver selects "file" domain.

**Example:**
```
User: "Create an agent for database optimization"
Weaver: Generates file-based agent
```

**Solution:** Be more specific about the database type:

```
❌ Vague: "database optimization"
✅ Specific: "PostgreSQL query optimization"
✅ Specific: "MySQL slow query analysis"
```

### Generated Agent Doesn't Load

**Problem:** Configuration saved but agent doesn't appear in agent list.

**Debugging:**

```bash
# Check if file was created
ls -la $LOOM_DATA_DIR/agents/

# Validate YAML syntax
cat $LOOM_DATA_DIR/agents/your-agent.yaml

# Check server logs for validation errors
# (logs go to stdout by default; check logging.file path if configured)

# Manual validation
looms validate file $LOOM_DATA_DIR/agents/your-agent.yaml
```

**Common causes:**
- **YAML syntax error**: Missing quotes, incorrect indentation
- **Invalid backend**: Referenced backend file doesn't exist
- **Unknown tool**: Tool name not registered in shuttle.Registry

### Cannot Connect to Generated Agent

**Problem:** `loom --thread agent-name` fails with "agent not found".

**Solutions:**

```bash
# List available agents (uses the TUI client, not the server binary)
loom agents

# Check exact agent name (case-sensitive)
ls $LOOM_DATA_DIR/agents/

# Restart server to force reload
pkill looms
looms serve

# Wait 2-3 seconds for hot-reload
sleep 3
loom --thread postgres-optimizer
```


## Next Steps

- **Architecture Details**: See [Weaver Architecture](../architecture/weaver.md) for design philosophy and tool-based approach
- **Advanced Features**: See [Meta-Agent Usage Guide](./meta-agent-usage.md) for full documentation
- **Pattern Library**: See [Pattern Library Guide](./pattern-library-guide.md) for available patterns

**Related Guides**:
- [Zero-Code Implementation](./zero-code-implementation-guide.md) - Manual agent configuration
- [Artifact Management](./artifacts-usage.md) - Upload files for context
- [MCP Integration](./integration/mcp-readme.md) - External tool integration
