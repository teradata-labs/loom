# D&D Campaign Builder Workflow

A multi-agent workflow for creating complete D&D 5e campaigns with world settings, storylines, encounters, NPCs, and session plans using event-driven coordination.

## Overview

This workflow orchestrates seven specialized agents to create comprehensive D&D campaigns:

1. **Coordinator** - User-facing agent that gathers campaign requirements and orchestrates the creation process (event-driven, no polling)
2. **World Builder** - Creates fantasy world settings with geography, cultures, and history
3. **Storyline Designer** - Designs compelling narrative arcs, story hooks, and campaign plots
4. **Encounter Designer** - Creates combat encounters, challenges, and tactical scenarios
5. **NPC Creator** - Generates non-player characters with personalities, motivations, and stats
6. **Session Planner** - Plans session-by-session breakdown with scenes and pacing
7. **Campaign Publisher** - Compiles all content into final formatted campaign document

## Architecture

```
                    ┌─────────────────────────┐
  User ────────────>│      Coordinator        │
                    │   (dnd-coordinator)      │
                    └───────┬─────────────────┘
                            │ send_message
        ┌──────┬──────┬─────┼──────┬──────┐
        │      │      │     │      │      │
        ▼      ▼      ▼     ▼      ▼      ▼
     ┌────┐ ┌────┐ ┌────┐ ┌────┐ ┌────┐ ┌────┐
     │World│ │Story│ │Enc.│ │NPC │ │Sess│ │Pub.│
     └──┬─┘ └──┬─┘ └──┬─┘ └──┬─┘ └──┬─┘ └──┬─┘
        │      │      │      │      │      │
        └──────┴──────┴──────┴──────┴──────┘
                    send_message back
                 (auto-injected into
                  coordinator session)
```

**Communication Pattern**: Hub-and-spoke via event-driven message queue

### How It Works

All communication uses Loom's **event-driven message queue**:

1. **Coordinator sends** requirements to sub-agents via `send_message`
2. **Sub-agents receive** messages automatically (auto-injected into their conversation)
3. **Sub-agents process** the request and send results back via `send_message`
4. **Coordinator receives** responses automatically (auto-injected into its session)

**There is NO `receive_message` tool.** Messages are auto-injected by the runtime. This is Loom's event-driven architecture — no polling, no timeouts, instant delivery.

### Critical Naming Requirements

When `spawnWorkflowSubAgents` detects a coordinator agent (via `metadata.role: coordinator` and `metadata.workflow: <name>`), it:

1. Loads the workflow YAML from `~/.loom/workflows/<workflow-name>.yaml`
2. Parses `spec.agents` and spawns sub-agents with namespaced IDs: `workflow-name:role-name`
3. Registers notification channels for message delivery
4. Sets up coordinator notification handler for receiving responses

**Sub-agent metadata must include `workflow: <workflow-name>`** (matching exactly) for the registry search to find them.

## Usage

### Installation

Copy agent configs to `~/.loom/agents/` and workflow config to `~/.loom/workflows/`:

```bash
# Copy agents
cp agents/*.yaml ~/.loom/agents/

# Copy workflow
mkdir -p ~/.loom/workflows
cp workflows/dnd-campaign-workflow.yaml ~/.loom/workflows/
```

### Running the Workflow

```bash
# Terminal 1: Start the looms server
looms serve

# Terminal 2: Chat with the coordinator
loom chat --thread dnd-coordinator --message "Create a dark gothic horror D&D campaign. Party of 4, levels 1-10, 8 sessions."
```

### Example Output

```
Coordinator: I've initiated the creation of your campaign "Shadows of the
             Corrupted Spire." Coordinating with my specialized team...

[MESSAGE FROM dnd-world-builder]: The region of Oakhaven is now a land of
eternal twilight, dominated by the Cathedral of the Bleeding Sun...

[MESSAGE FROM dnd-storyline-designer]: The storyline spans three tiers:
The Whispering Heresy (Levels 1-4), The Sanguine Covenant (Levels 5-8),
and The Eclipse of Hope (Levels 9-10)...

[MESSAGE FROM dnd-npc-creator]: Father Malachi, the High Exarch, is your
primary antagonist. Countess Elara von Hellsing provides the vampiric threat...

Coordinator: Here's your complete campaign overview...
```

## Agent Details

### Coordinator (dnd-coordinator)
- **Role**: User-facing coordinator, delegates work to sub-agents
- **Communication**: Event-driven — sends via `send_message`, receives auto-injected responses
- **Tools**: shell_execute, tool_search, send_message
- **Memory**: SQLite with conversational profile (max_history: 1000)
- **Config**: max_turns: 100, max_tool_executions: 200, timeout: 900s
- **Metadata**: `role: coordinator`, `workflow: dnd-campaign-workflow`

### Sub-Agents (all follow the same pattern)

All sub-agents are event-driven:
- Messages arrive automatically (auto-injected as `[MESSAGE from coordinator ...]`)
- Process the request using their domain expertise
- Send results back via `send_message`
- **No `receive_message` needed** — the runtime handles message delivery

| Agent | Config Name | Artifact Output |
|-------|------------|-----------------|
| World Builder | dnd-world-builder | `world.json` |
| Storyline Designer | dnd-storyline-designer | `story.json` |
| Encounter Designer | dnd-encounter-designer | `encounters.json` |
| NPC Creator | dnd-npc-creator | `npcs.json` |
| Session Planner | dnd-session-planner | `sessions.json` |
| Campaign Publisher | dnd-campaign-publisher | `campaign-guide.md` |

All sub-agents:
- **Tools**: shell_execute, tool_search, send_message, query_tool_result, search_conversation, recall_conversation, clear_recalled_context
- **Memory**: SQLite with data_intensive profile (max_history: 2000)
- **Config**: max_turns: 80, max_tool_executions: 150, timeout: 600s
- **Metadata**: `workflow: dnd-campaign-workflow`

## Output Artifacts

The workflow saves detailed artifacts to `$LOOM_DATA_DIR/artifacts/dnd-campaigns/{campaign_id}/`:

- `overview.md` - Complete campaign overview (coordinator)
- `world.json` - World setting and geography (world-builder)
- `story.json` - Campaign storyline and plot (storyline-designer)
- `encounters.json` - Combat encounters and challenges (encounter-designer)
- `npcs.json` - Non-player characters (npc-creator)
- `sessions.json` - Session-by-session breakdown (session-planner)
- `campaign-guide.md` - Final compiled campaign document (campaign-publisher)

## Troubleshooting

### Sub-agents not receiving messages
- Verify `looms serve` is running (the server manages message delivery)
- Check that sub-agent YAMLs have `workflow: dnd-campaign-workflow` in metadata (must match exactly)
- Check that coordinator YAML has `role: coordinator` and `workflow: dnd-campaign-workflow`
- Look for "Detected workflow coordinator" in server logs
- Look for "registered notification channel" in server logs

### Messages stuck in queue
- Check server logs for "NO NOTIFICATION CHANNEL registered for agent"
- This means the agent ID in `send_message` doesn't match any registered channel
- Verify the coordinator's system prompt uses correct agent IDs

### Content inconsistencies
- Ensure coordinator passes campaign_id to all specialists
- Verify artifacts are being saved to correct directory
- Check that agents are reading previous agent artifacts when needed

## Architecture Notes

### Event-Driven Message Flow

```
1. User → loom chat --thread dnd-coordinator
2. Coordinator calls send_message(to="dnd-world-builder", ...)
3. Message enqueued → notification channel triggered
4. Sub-agent goroutine wakes up, dequeues message
5. Message auto-injected into sub-agent conversation via Chat()
6. Sub-agent processes request, calls send_message back to coordinator
7. Message enqueued for coordinator → monitor detects pending message
8. Response auto-injected into coordinator session via Chat()
9. Coordinator sees [MESSAGE FROM dnd-world-builder]: ...
```

### Key Code References

- `pkg/server/multi_agent.go:spawnWorkflowSubAgents()` — Detects coordinator, spawns sub-agents
- `pkg/server/multi_agent.go:autoSpawnWorkflowSubAgent()` — Creates sub-agent goroutines with notification channels
- `pkg/server/multi_agent.go:runWorkflowSubAgent()` — Sub-agent event loop (wait for notification → dequeue → Chat())
- `pkg/server/multi_agent.go:StartMessageQueueMonitor()` — Polls message queue, notifies registered channels
- `pkg/communication/queue.go` — Message queue with notification channel support

## License

Part of the Loom agent framework examples.
