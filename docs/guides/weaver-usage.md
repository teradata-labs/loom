
# Weaver Usage Guide

**Version**: v1.1.0
**Status**: ✅ Implemented

> **What's New in v1.1.0**:
> - ✨ /agent-plan mode - Guided planning with 5 structured phases
> - ✨ Skills recommendations - Intelligent skill suggestions based on problem domain
> - ✨ Custom skill creation - Weaver can create skills with user consent

*Note: This guide is maintained separately. For the comprehensive weaver documentation that includes architecture details and the full 9-stage pipeline description, see [Meta-Agent Usage Guide](./meta-agent-usage.md).*

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
- [Conflict Resolution](#conflict-resolution)
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

**What makes Weaver special:**
- **Intelligent**: Uses LLM-powered analysis (not keyword matching)
- **Metadata-Driven**: Reads self-describing tool and pattern metadata
- **Conflict Detection**: Catches contradictions before generating config
- **Interactive**: Pauses for user decisions on ambiguous requirements

## Prerequisites

- Loom v1.0.0+
- LLM provider configured (Anthropic, Bedrock, or Ollama)
- Running Loom server: `bin/looms serve`

Verify setup:

```bash
# Check LLM configuration
bin/looms config get anthropic_api_key

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

> **New in v1.1.0**: Weaver now offers two planning modes - Quick Start for immediate creation, or /agent-plan mode for structured guidance.

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

> **New in v1.1.0**: Weaver recommends and creates skills to enhance your agents.

Skills are LLM-agnostic prompt injections that provide domain expertise. Weaver automatically recommends relevant skills based on your problem domain.

### Built-in Skills

Weaver knows about these existing skills:

| Skill | Activation | Use Case |
|-------|-----------|----------|
| `sql-optimization` | `/optimize` or "slow query" | Database performance analysis |
| `code-review` | `/review` or "review code" | Security and quality checks |
| `data-quality-check` | `/check` or "validate data" | Data validation and cleaning |
| `multi-agent-coordinator` | `/coordinate` | Task delegation across agents |
| `agent-discovery` | `/find` | Locate specialist agents |
| `request-response-coordinator` | Auto | Synchronous agent communication |

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

**Token cost:** $0.02-0.04 (Claude Sonnet 4)

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

**Token cost:** $0.06-0.10

### With Artifact Context

Provide file context to inform generation:

```
# Upload a CSV file first
User: "/upload sales_data.csv"

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


## Conflict Resolution

The Weaver detects contradictions and pauses for your decision.

**Conflict types:**
1. **Tool Redundancy**: Multiple tools with overlapping functionality
2. **Pattern Overlap**: Patterns addressing similar concerns
3. **Contradictory Requirements**: Conflicting instructions (e.g., "enable caching" + "disable caching")

**Example conflict flow:**

```
User: "Create an agent with aggressive caching and no caching for fresh data"

Weaver detects conflict:

⚠️ Detected 1 conflict that needs your input:

Conflict: Pattern Contradiction (confidence: 85%)
  - aggressive_caching (recommended)
  - no_caching

  These patterns have contradictory approaches to caching.
  Which should we use?

Your choice:
  [1] aggressive_caching (cache everything, faster but may be stale)
  [2] no_caching (always fresh, slower but accurate)
  [3] hybrid_caching (cache with TTL, balanced approach)

User: 3

Weaver: Selected hybrid_caching. Continuing generation...

✓ Agent generated with hybrid caching strategy
```

**Auto-resolution:** If confidence > 90%, Weaver auto-resolves with recommendation (no user input needed).


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
  You are a PostgreSQL query optimizer. Analyze queries for performance
  issues, suggest index improvements, and explain query plans.

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
# Upload CSV first
User: "/upload customer_data.csv"

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
loom python-performance-analyzer

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

### /list - List Woven Agents

```
User: "/list"

Currently Woven Agents:

1. postgres-optimizer (domain: sql) - Status: running, Active Sessions: 2
2. file-analyzer (domain: file) - Status: stopped
3. sql-debate-optimizer (workflow) - Status: running, Active Sessions: 1

Total: 3 agents

Commands:
- Connect to an agent: loom --thread <agent-name>
- Create a new agent: Just describe what you need!
```

### /help - Show Help

```
User: "/help"

# Weaver Help

I'm the Weaver - I create AI agents from natural language descriptions!

How to Use Me:
Just tell me what kind of agent you need, and I'll generate a complete configuration.

Examples:
- "Create a SQL query optimizer for PostgreSQL"
- "I need an agent to monitor REST API health"
- "Build a multi-agent system to analyze customer feedback"

What I Can Do:
- Analyze requirements (domain, capabilities)
- Match existing examples when appropriate
- Design multi-agent workflows for complex tasks
- Generate complete YAML configurations
- Select optimal patterns and tools
- Validate configurations before deployment

Advanced Features:
- Judge evaluation: "with quality evaluation"
- Self-learning: "that improves over time"
- Multi-agent: "coordinate multiple agents"
```


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

# Check server logs
tail -f $LOOM_DATA_DIR/logs/server.log
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

### Conflicts Never Resolve

**Problem:** Weaver keeps detecting conflicts, never generates config.

**Example:**
```
User: "Create an agent that is fast and accurate and handles errors and never fails"

Weaver: Detects 5 conflicts...
```

**Solution:** Simplify requirements or clarify priorities:

```
✅ "Create a PostgreSQL optimizer that prioritizes accuracy over speed"
✅ "Create an API monitor with basic error handling and 3 retries"
```

### Generated Agent Doesn't Load

**Problem:** Configuration saved but agent doesn't appear in `/list`.

**Debugging:**

```bash
# Check if file was created
ls -la $LOOM_DATA_DIR/agents/

# Validate YAML syntax
cat $LOOM_DATA_DIR/agents/your-agent.yaml

# Check server logs for validation errors
grep "validation failed" $LOOM_DATA_DIR/logs/server.log

# Manual validation
bin/looms validate $LOOM_DATA_DIR/agents/your-agent.yaml
```

**Common causes:**
- **YAML syntax error**: Missing quotes, incorrect indentation
- **Invalid backend**: Referenced backend file doesn't exist
- **Unknown tool**: Tool name not registered in shuttle.Registry

### Cannot Connect to Generated Agent

**Problem:** `loom --thread agent-name` fails with "agent not found".

**Solutions:**

```bash
# List available agents
bin/looms agent list

# Check exact agent name (case-sensitive)
ls $LOOM_DATA_DIR/agents/

# Restart server to force reload
pkill looms
bin/looms serve

# Wait 2-3 seconds for hot-reload
sleep 3
bin/loom --thread postgres-optimizer
```


## Next Steps

- **Architecture Details**: See [Weaver Architecture](../architecture/weaver.md) for 6-stage pipeline and sub-agent design
- **Advanced Features**: See [Meta-Agent Usage Guide](./meta-agent-usage.md) for comprehensive documentation
- **Pattern Library**: See [Pattern Library Guide](./pattern-library-guide.md) for available patterns

**Related Guides**:
- [Zero-Code Implementation](./zero-code-implementation-guide.md) - Manual agent configuration
- [Artifact Management](./artifacts-usage.md) - Upload files for context
- [MCP Integration](./mcp-integration.md) - External tool integration
