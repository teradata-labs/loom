# START HERE - Agent Instructions

Quick reference for all Loom agents.

---

## 🔍 Tool Discovery

**Always use `tool_search` first when you need tools.**

```
tool_search(query="database")  # Find DB tools
tool_search(query="")           # List all tools
```

**Critical:**
- Call discovered tools directly by name (e.g., `teradata_execute_query`)
- Don't invoke via shell (no `mcp-server ...` commands)
- Tool names include full namespace (e.g., `filesystem:read_file`)

---

## 📦 Large Result Handling

When tools return >1000 tokens, you get inline metadata:

```
✓ Large json_array stored (500KB, ~125K tokens)
📋 Preview: First 5 items shown
📊 Array: 1000 items, fields: [name, id, value]
💡 query_tool_result(reference_id='ref_xyz', offset=0, limit=100)
⚠️ Large dataset - use filtering
```

**Retrieval by data type:**

| Data Type | How to Retrieve | Example |
|-----------|----------------|---------|
| `json_object` | No params | `query_tool_result(reference_id='ref_123')` |
| `json_array` | offset/limit OR sql | `query_tool_result(ref, offset=0, limit=100)` |
| `sql_result` | SQL query | `query_tool_result(ref, sql='SELECT * WHERE ...')` |
| `csv` | SQL query (auto-converts) | `query_tool_result(ref, sql='SELECT * ...')` |
| `text` | offset/limit (line-based) | `query_tool_result(ref, offset=0, limit=100)` |

**Rules:**
- Read inline metadata (preview, schema, size)
- Use filtering for large datasets (>1000 items)
- json_object retrieval doesn't need offset/limit

**Common errors:**
- `"Must provide offset/limit"` → Add parameters to json_array retrieval
- `"no such column"` → Check inline schema for correct column names
- `"Reference not found"` → DataRef expired (1hr TTL), re-run tool

---

## 🧠 Working Memory (Preventing Hallucination)

**CRITICAL:** Record verified facts as you discover them.

### Why This Matters

Chaining multiple tool calls silently causes you to lose track of findings when generating final reports. This leads to fabricated data.

**Bad pattern (causes hallucination):**
```
1. Query executes → 2195 rows
2. [EMPTY RESPONSE - no narration]
3. Query executes → 17% nulls
4. [EMPTY RESPONSE - no narration]
5. User asks for report
6. You fabricate data because findings are outside context window
```

**Good pattern (prevents hallucination):**
```
1. Query executes → 2195 rows
2. "I found 2,195 rows. Recording this..."
3. record_finding(path="table.row_count", value=2195, category="statistic")
4. Query executes → 17% nulls
5. "17% null rate. Recording..."
6. record_finding(path="table.column.null_rate", value=0.17, category="statistic")
7. User asks for report
8. You reference "Verified Findings" in context with exact numbers
```

### Using record_finding

**After every successful tool execution, do TWO things:**

1. **Narrate what you found** (explanatory text)
2. **Record the finding** (structured storage)

**Examples by category:**

```python
# Statistics (counts, percentages, aggregates)
record_finding(
    path="vantage_sites.row_count",
    value=2195,
    category="statistic"
)

# Schema (structure, columns, types)
record_finding(
    path="vantage_sites.columns",
    value=["site_id", "customer_id", "region"],
    category="schema"
)

# Observations (patterns, insights)
record_finding(
    path="vantage_sites.site_id.uniqueness",
    value="100% unique - likely primary key",
    category="observation"
)

# Distribution (value frequencies)
record_finding(
    path="vantage_sites.region.distribution",
    value={"Americas": 1183, "EMEA": 589, "APJ": 423},
    category="distribution"
)
```

**Path naming conventions:**
- Hierarchical: `table.column.metric` or `table.metric`
- Use snake_case
- Examples: `customers.row_count`, `orders.status.null_rate`

**What happens:**
Recorded findings are automatically injected into your context as a "Verified Findings" summary. This provides structured working memory across tool executions.

### Rules

1. **Never chain tool calls silently** - Always provide explanatory text between tool executions
2. **Record key findings immediately** - Don't wait until the end
3. **Reference verified findings** - When generating reports, cite findings from working memory
4. **Schema validation** - Verify column names exist before querying

---

## 🔄 Agent Communication

### Workflow Agent IDs

**CRITICAL:** Use full namespaced IDs in workflows.

- Standalone: `weather-analyst`
- In workflow: `vacation-workflow:weather-analyst`
- Format: `<workflow-name>:<agent-name>`

```
✅ send_message(to_agent="vacation-workflow:weather-analyst", message="...")
❌ send_message(to_agent="weather-analyst", message="...")  # Fails in workflow
```

### Point-to-Point (Direct)

**Send:**
```
send_message(to_agent="workflow:agent-name", message="Do task X")
```

**Receive (event-driven, instant delivery):**
```
receive_message(timeout_seconds=30)
```

### Pub-Sub (Broadcast)

1. **Subscribe:**
```
subscribe(topic="party-chat")
subscribe(topic="dnd.*")  # Wildcard
```

2. **Publish:**
```
publish(topic="party-chat", message="Found secret door!")
```

3. **Receive (event-driven):**
```
receive_broadcast(timeout_seconds=30, max_messages=10)
```

**Patterns:**
- Exact: `"party-chat"`
- Wildcard: `"dnd.*"` matches dnd.combat, dnd.exploration
- Multi-level: `"game.*.events"`

---

## 📁 Artifacts & Archives

**Artifacts** = Structured data shared between agents (~/archives/)

**Read:**
```
read_artifact(path="analysis/results.json")
```

**Write:**
```
write_artifact(path="analysis/results.json", content=data)
```

**Search:**
```
search_artifacts(pattern="*.json")
search_artifacts(pattern="analysis/**/*.csv")
```

**File types:**
- `.json` → JSON data
- `.txt` → Plain text
- `.csv` → Tabular data
- `.md` → Markdown reports

**Best practices:**
- Organize by workflow: `workflow-name/agent-name/file.json`
- Use semantic names: `analysis-results.json` not `output.json`
- Clean up when done: `delete_artifact(path="temp/data.json")`

---

## 📝 Scratchpad

**Temporary notes for multi-step tasks (not shared).**

**Write:**
```
write_scratchpad(content="Step 1: Found 3 tables\nStep 2: Query results...")
```

**Read:**
```
read_scratchpad()
```

**Clear:**
```
clear_scratchpad()
```

**Use for:**
- Multi-turn task tracking
- Intermediate results
- Reasoning chains
- TODO lists

**Don't use for:**
- Sharing with other agents (use artifacts)
- Permanent storage (expires after session)

---

## 🎯 Quick Reference

**Discovery:**
- `tool_search(query="keyword")` → Find tools

**Working Memory:**
- Narrate findings after every tool execution
- `record_finding(path, value, category)` → Store verified facts
- Categories: "statistic", "schema", "observation", "distribution"

**Large results:**
- Check inline metadata (preview, schema, size)
- `query_tool_result(ref, ...)` → Retrieve with filtering

**Communication:**
- `send_message(to_agent="workflow:name", message)` → Direct
- `publish(topic, message)` → Broadcast
- `receive_message()` / `receive_broadcast()` → Get messages

**Artifacts:**
- `read_artifact(path)` / `write_artifact(path, content)` → Share data
- `search_artifacts(pattern)` → Find files

**Scratchpad:**
- `write_scratchpad(content)` / `read_scratchpad()` → Temp notes

---

## ⚠️ Common Mistakes

1. **Chaining tool calls silently** → Narrate findings and use record_finding after each execution
2. **Not discovering tools** → Use tool_search first
3. **Fabricating data in reports** → Reference verified findings from working memory
4. **Trying to retrieve all data** → Use filtering/pagination
5. **Wrong agent IDs in workflows** → Use full `workflow:agent` format
6. **Polling for messages** → Event-driven, just call receive_message once
7. **Using scratchpad for sharing** → Use artifacts instead

---

**Need more details?** Check tool descriptions with `tool_search` or ask your coordinator.
