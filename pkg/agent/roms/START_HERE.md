# START HERE - Agent Instructions

This file contains important information for all agents running in the Loom framework.

---

## ⚠️ CRITICAL: Tool Discovery

**ALWAYS use tool_search as your FIRST action when you don't know what tools are available.**

You have access to many tools beyond what's listed in your config:
- **MCP server tools** (database connectors, external APIs, specialized operations)
- **Builtin tools** (shell_execute, recall_conversation, delegate_to_agent, etc.)
- **Dynamically registered tools** (vary by configuration and context)

**How to discover tools:**
```
Use tool_search with queries like:
- "query" - Find database query tools
- "teradata" - Find Teradata-specific tools
- "execute" - Find execution tools
- "list" - Find listing/browsing tools
- "" (empty string) - List ALL available tools
```

**Critical rules:**
1. **Call discovered tools DIRECTLY by name** (e.g., `teradata_execute_query`)
2. **DO NOT invoke MCP tools via shell** (e.g., don't do `mcp-server vantage ...`)
3. **Use tool_search early and often** - don't assume you know what's available
4. **Read tool descriptions** - tool_search returns parameter info and usage examples

---

## 🔄 Agent-to-Agent Communication

**Loom provides two communication modes: Point-to-Point and Pub-Sub, both with event-driven instant delivery.**

### ⚠️ CRITICAL: Use Full Agent IDs in Workflows

**When communicating with agents in a workflow, you MUST use the full namespaced agent ID, not the short name.**

**How workflow agent IDs work:**
- Standalone agents: `weather-analyst` (simple ID)
- Workflow agents: `vacation-planning-workflow:weather-analyst` (namespaced ID)
- Workflow format: `<workflow-name>:<agent-name>`

**Why this matters:**
When a workflow is loaded (e.g., `vacation-planning-workflow.yaml`), Loom creates namespaced copies of each agent:
- Coordinator: `vacation-planning-workflow` (entrypoint)
- Sub-agents: `vacation-planning-workflow:weather-analyst`, `vacation-planning-workflow:vacation-planner`

**Examples:**

✅ **CORRECT - Full namespaced IDs:**
```
send_message(to_agent="vacation-planning-workflow:weather-analyst", message="What's the weather in Paris?")
send_message(to_agent="vacation-planning-workflow:vacation-planner", message="Find destinations")
send_message(to_agent="dnd-workflow:dungeon-master", message="Roll for initiative")
```

❌ **INCORRECT - Short IDs won't work in workflows:**
```
send_message(to_agent="weather-analyst", message="...")  # FAILS - agent not found
send_message(to_agent="vacation-planner", message="...")  # FAILS - agent not found
send_message(to_agent="dungeon-master", message="...")  # FAILS - agent not found
```

**How to find correct agent IDs:**
1. Check your workflow YAML file to see the workflow name
2. Agent IDs follow pattern: `<workflow-name>:<agent-name>`
3. Or use tool_search to list available agents and see their full IDs

**Remember:**
- Short IDs work for standalone agents outside workflows
- Full namespaced IDs are REQUIRED for workflow agents
- When in doubt, use the full `workflow-name:agent-name` format

### Point-to-Point Communication (Direct Messaging)

**For Sub-Agents (executors):**
- You are automatically registered for event-driven notifications
- When a coordinator sends you a message, you are **instantly notified** (no polling!)
- Simply use `receive_message` - you'll be woken up when messages arrive
- You do NOT need to poll or check repeatedly

**For Coordinators:**
- You are also registered for event-driven notifications
- When you send messages to sub-agents and wait for responses, you'll be **instantly notified** when they reply
- Use `receive_message` with a reasonable timeout (30-60s) as a safety fallback
- The timeout is rarely hit - responses typically arrive within 5-15 seconds

### Pub-Sub Communication (Group Broadcast)

**When to use pub-sub:**
- Group conversations (multiple agents talking together)
- Broadcasting status updates to all interested agents
- Multi-agent collaboration where everyone needs to see all messages
- Example: D&D party chat, team coordination, event broadcasting

**How to use pub-sub:**

1. **Subscribe to a topic** (do this first!):
   ```
   subscribe(topic="party-chat")
   subscribe(topic="dnd.*")  # Wildcard: matches dnd.combat, dnd.exploration, etc.
   ```

2. **Publish messages to the topic**:
   ```
   publish(topic="party-chat", message="I found a secret door!")
   publish(topic="team-updates", message="Task completed", metadata={"priority": "high"})
   ```

3. **Receive broadcast messages**:
   ```
   receive_broadcast(timeout_seconds=30, max_messages=10)
   ```
   - Returns messages from ALL your subscribed topics
   - Event-driven: you're instantly notified when messages arrive
   - No need to poll repeatedly!

**Topic Patterns:**
- Exact match: `"party-chat"` - only that topic
- Wildcard: `"dnd.*"` - matches dnd.combat, dnd.exploration, dnd.loot, etc.
- Multi-level: `"game.*.events"` - matches game.combat.events, game.social.events

**Filtering:**
```
subscribe(topic="party-chat", filter_from_agent="dungeon-master")  # Only receive messages from DM
```

**Event-Driven Benefits:**
- Instant notifications when messages arrive (no polling!)
- Efficient: background monitor runs every 1 second (no LLM calls)
- Non-blocking: messages dropped if your buffer is full (won't block publishers)

### 📋 Workflow Communication Pattern: Messages + Artifacts

**Loom uses a simplified two-pattern architecture for workflow communication:**

#### Pattern 1: Messages for Coordination

**Use send_message/receive_message for:**
- Requests and responses between agents
- Small data (<10 KB): requirements, summaries, status updates
- Coordination signals: "task complete", "ready for next step"
- Artifact path references: "I created artifact at dnd-campaigns/123/world.json"

**Example:**
```
Coordinator → send_message(to_agent="workflow:sub-agent",
                            message="Create world for campaign. campaign_id: 20251230-1234")

Sub-agent → Creates artifact: ~/.loom/artifacts/dnd-campaigns/20251230-1234/world.json
Sub-agent → send_message(to_agent="workflow",
                          message="World complete! Artifact: dnd-campaigns/20251230-1234/world.json. Summary: Fantasy realm with 3 kingdoms...")
```

#### Pattern 2: Artifacts for Outputs

**Use ~/.loom/artifacts/ for:**
- All substantial outputs (>10 KB)
- User-facing deliverables (reports, plans, documents)
- Persistent checkpoints (workflow stage outputs)
- Intermediate data shared between pipeline stages

**File Organization:**
```
~/.loom/artifacts/
├── workflow-name/
│   └── campaign-id/
│       ├── world.json          # Stage 1 output
│       ├── story.json          # Stage 2 output
│       ├── encounters.json     # Stage 3 output
│       ├── npcs.json           # Stage 4 output
│       └── campaign-guide.md   # Final deliverable
```

**Benefits:**
- ✅ **Persistent** - Artifacts survive server restarts
- ✅ **Debuggable** - Inspect intermediate workflow stages
- ✅ **Resumable** - Workflows can resume from last checkpoint
- ✅ **Discoverable** - Files are indexed and searchable
- ✅ **User-accessible** - Outputs visible to users

#### Pattern 3: Shared Memory (Advanced, Opt-In Only)

**Shared memory is NOT used in typical workflows.** It's available for advanced use cases only:

**When shared memory is appropriate:**
- Real-time streaming data (>1 MB buffers accessed frequently)
- Multi-agent collaboration on live datasets
- Performance-critical temporary working data
- Avoiding repeated serialization/deserialization

**Why workflows don't use shared memory:**
- ❌ Not persistent (lost on restart)
- ❌ Not debuggable (can't inspect state)
- ❌ Requires lifecycle management (who cleans up keys?)
- ❌ Adds cognitive complexity (where does data live?)

**For 99% of workflows: Use messages + artifacts. Simple, persistent, debuggable.**

### Communication Tools

**send_message**
- Sends a message to another agent in your workflow
- Messages are queued and the recipient is notified instantly
- Parameters:
  - `to_agent`: FULL agent ID of the recipient (e.g., "vacation-planning-workflow:weather-analyst")
  - `message`: Your message content (string)
  - `message_type`: Optional type (defaults to "request")
- **IMPORTANT:** Use full namespaced IDs for workflow agents! See "Use Full Agent IDs in Workflows" above.

**receive_message**
- Receives messages sent to you
- **Event-driven**: You are notified instantly when messages arrive
- Parameters:
  - `timeout_seconds`: Safety timeout (default: 0 for non-blocking, 30-60 recommended for waiting)
- Returns:
  - `has_message`: true/false
  - `message`: Message content if available
  - `from_agent`: Who sent the message
  - `message_id`: Unique message identifier

### Message Flow Example

**Coordinator workflow (e.g., vacation-planning-workflow):**
```
1. Chat with user, gather requirements
2. send_message to "vacation-planning-workflow:weather-analyst" with requirements
3. send_message to "vacation-planning-workflow:vacation-planner" with requirements
4. receive_message with timeout_seconds: 30  # Wait for weather-analyst response
5. receive_message with timeout_seconds: 30  # Wait for vacation-planner response
6. Synthesize responses and reply to user
```

**Sub-agent workflow (e.g., vacation-planning-workflow:weather-analyst):**
```
1. Wait (you'll be notified when messages arrive)
2. receive_message (returns immediately with the message)
3. Process the weather request
4. send_message back to "vacation-planning-workflow" (coordinator) with results
```

**Key Points:**
- Coordinator uses FULL namespaced IDs: `workflow-name:agent-name`
- Sub-agents reply to the coordinator using just the workflow name (e.g., `vacation-planning-workflow`)
- Event-driven notifications ensure instant message delivery in both directions

### Key Benefits

- **Instant delivery**: No polling, no waiting, no delays
- **Efficient**: Monitor checks queue every 1 second (not LLM calls)
- **Reliable**: Timeout provides safety fallback if something goes wrong
- **Bi-directional**: Works for coordinator ← sub-agents and sub-agents ← coordinator

### Important Notes

- Messages are **persistent** - they survive restarts
- Messages have **expiration** (default: 24 hours)
- Messages are **auto-acknowledged** after successful receive
- Use meaningful `message_type` for different kinds of messages (request, response, status, error)

---

## 📚 Documentation

**Full Loom framework documentation is available at: `~/.loom/documentation/`**

This is a complete copy of the project documentation including:
- **Architecture** - System design, memory model, agent lifecycle
- **Guides** - How-to guides, LLM providers, integration, patterns
- **Reference** - API documentation, configuration options, CLI commands

**Quick access:**
```bash
# Browse documentation
ls ~/.loom/documentation/

# Read architecture docs
cat ~/.loom/documentation/architecture/README.md

# Learn about patterns
cat ~/.loom/documentation/guides/patterns/

# Search documentation
grep -r "memory" ~/.loom/documentation/

# Find specific topics
find ~/.loom/documentation -name "*pattern*"
```

**When to read docs:**
- Understanding agent memory (ROM, Kernel, L1, L2, Swap layers)
- Learning about patterns and workflows
- Troubleshooting configuration issues
- Understanding the orchestration system
- Learning best practices for agent design

---

## Scratchpad Directory (~/.loom/scratchpad)

This directory is your persistent workspace for research, notes, and intermediate work.

## Purpose

Use this directory to:
- Save research findings and analysis results
- Store intermediate query results or data samples
- Keep session notes that persist across conversations
- Cache expensive computations or API responses
- Organize work files logically for future reference

## File Naming Convention

IMPORTANT: Always include your agent ID in filenames. If you're part of a workflow, include the workflow ID too.

**Naming pattern:**
`<agent_id>_<workflow_id?>_<description>_<date>.<ext>`

**Good examples:**
- `td-query-agent_research_indexes_2025-12-24.md` - Research notes from single agent
- `data-profiler_wf-123_analysis_customer_table_2025-12-24.json` - Analysis from agent in workflow wf-123
- `schema-analyzer_wf-456_query_metadata_2025-12-24.sql` - Query from coordinator workflow
- `td-query-agent_cache_table_stats_2025-12-24.json` - Cached computation results
- `test-weaver_notes_session_requirements_2025-12-24.md` - Session notes

**Bad examples:**
- `temp.txt` - No agent ID, too generic
- `file1.json` - No agent ID, not descriptive
- `research.md` - No agent ID, no context

## Agent Artifacts
Any agent-generated assets for users to consume or source files for agents to consume are considered **artifacts.** These are located in ~/.loom/artifacts.

## Accessing Files

Use shell_execute to read/write files in this directory:
```bash
# Write notes (replace 'your-agent-id' with your actual agent ID)
cat > ~/.loom/scratchpad/your-agent-id_research_indexes_2025-12-24.md <<'EOF'
# Teradata Index Research
Found that table X has no primary index...
EOF

# Read notes later
cat ~/.loom/scratchpad/your-agent-id_research_indexes_2025-12-24.md

# List all files from your agent
ls -lh ~/.loom/scratchpad/your-agent-id_*

# List all files in scratchpad
ls -lh ~/.loom/scratchpad/

# Find files by topic
find ~/.loom/scratchpad -name "*indexes*"

# Find all files from a specific workflow
find ~/.loom/scratchpad -name "*wf-123*"
```

## Cleanup

This directory is not automatically cleaned. Review and delete old files periodically to avoid clutter.

## Tips

- Include dates in filenames for time-based tracking
- Use markdown (.md) for structured notes
- Use JSON/CSV for data exports
- Create subdirectories for large projects: `mkdir -p ~/.loom/scratchpad/<project_name>`
- Always check if a file exists before assuming it's empty: `ls ~/.loom/scratchpad/`
