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
│      Coordinator        │ ← User interaction (event-driven)
│      (Entrypoint)       │
└───────┬─────────────────┘
        │
        ├──────┬──────┬──────┬──────┬──────┐
        │      │      │      │      │      │
        ▼      ▼      ▼      ▼      ▼      ▼
     ┌────┐ ┌────┐ ┌────┐ ┌────┐ ┌────┐ ┌────┐
     │World│ │Story│ │Enc.│ │NPC │ │Sess│ │Pub.│
     │Build│ │Design│ │Design│ │Create│ │Plan│ │lish│
     └────┘ └────┘ └────┘ └────┘ └────┘ └────┘
```

**Communication Pattern**: Hub-and-spoke (coordinator is the hub)

### How Hub-and-Spoke Works

This workflow demonstrates an **emergent communication pattern** rather than an enforced workflow pattern. Unlike Loom's formal workflow patterns (`debate`, `pipeline`, `swarm`, etc.), hub-and-spoke is not implemented as runtime enforcement. Instead, it emerges naturally through:

**Loom's Tri-Modal Communication System:**
- **Message Queue**: Direct agent-to-agent messaging via `send_message`/`receive_message` tools
- **Broadcast Bus**: Topic-based pub/sub via `publish`/`subscribe` tools (not used in this workflow)
- **Shared Memory**: Shared state via `shared_memory_write`/`shared_memory_read` tools (not used in this workflow)

**Why the Coordinator Acts as Hub:**
1. **Entrypoint designation**: Users interact with the coordinator first
2. **Event-driven architecture**: Coordinator uses event-driven messaging (responses automatically injected, no polling)
3. **System prompt instructions**: Coordinator's prompt tells it to delegate to specialists via `send_message`
4. **Specialist agent prompts**: All specialists call `receive_message` once, then respond via `send_message`

**Key Architectural Detail - Event-Driven Coordinator:**
The coordinator is fully event-driven. It does NOT call `receive_message` - instead, specialist responses are automatically injected into its conversation. This eliminates polling and ensures instant notification when sub-agents complete their work.

The `communication` field in the workflow YAML is **advisory documentation only** - it communicates intent to humans but is not parsed or enforced by the runtime. This flexibility allows you to implement various communication topologies (hub-and-spoke, peer-to-peer, hierarchical) simply by configuring agent prompts and tool usage.

## Usage

### Starting the Workflow

```bash
# Start the looms server (if not already running)
looms serve

# In another terminal, connect to the coordinator
loom --thread dnd-coordinator
```

### Example Interaction

```
User: I want to create a dark fantasy campaign for level 5 characters,
       about 10 sessions long, with themes of corruption and redemption

Coordinator: Excellent! A dark fantasy campaign focused on corruption
             and redemption sounds compelling. I'll coordinate with my
             specialized agents to create your campaign...

[Coordinator generates campaign_id: campaign_20251231_142530]
[Coordinator sends requests to all specialist agents]
[System automatically injects specialist responses when ready]

Coordinator: Campaign Creation Summary:

             🌍 WORLD: The Ashen Realm
             - Gothic fantasy setting with fallen kingdoms
             - Magic corrupts those who wield it carelessly
             - Three major factions vying for power

             📖 STORYLINE: The Price of Power
             - 3-act structure over 10 sessions
             - Players investigate magical corruption
             - Climax: Stop ritual that would doom the realm

             ⚔️ ENCOUNTERS: 12 designed
             - 8 combat encounters (CR 5-7)
             - 4 social/exploration challenges

             👥 NPCs: 15 characters created
             - Corrupt duke (main antagonist)
             - Redemption-seeking wizard (ally)
             - Various faction leaders and townspeople

             📅 SESSIONS: 10-session breakdown
             - Clear objectives and pacing for each session
             - Mix of combat, roleplay, and exploration

             📄 FINAL DOCUMENT: campaign_20251231_142530.pdf
             Complete campaign saved to artifacts!

             [All artifacts in ~/.loom/artifacts/dnd-campaigns/campaign_20251231_142530/]
```

## Features

### Coordinator Agent
- **Communication Model**: Event-driven (responses automatically injected, no polling)
- Gathers campaign requirements (setting, theme, level, length, tone)
- Delegates work to appropriate specialist agents via `send_message`
- Synthesizes multi-agent results into comprehensive campaign overview
- Generates unique campaign_id for organizing artifacts
- Saves final overview to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/overview.md`
- **Memory**: SQLite with conversational profile (max_history: 1000)
- **Config**: max_turns: 100, max_tool_executions: 200, timeout: 900s
- **Tools**: shell_execute, tool_search, send_message (NO receive_message - fully event-driven)

### World Builder Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Creates fantasy world settings with detailed geography, cultures, and history
- Designs political systems, economies, and social structures
- Develops pantheons, magic systems, and supernatural elements
- Ensures world elements support campaign theme and tone
- Saves world data to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/world.json`
- **Memory**: SQLite with data_intensive profile (max_history: 2000)
- **Config**: max_turns: 80, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

### Storyline Designer Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Designs multi-act story arcs with compelling hooks, twists, and climaxes
- Creates main questlines and meaningful side quests
- Develops dramatic tension, pacing, and emotional beats
- Integrates story with world setting from world-builder
- Generates plot points that accommodate player agency
- Saves story data to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/story.json`
- **Memory**: SQLite with balanced profile (max_history: 1500)
- **Config**: max_turns: 70, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

### Encounter Designer Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Creates combat encounters with appropriate challenge ratings
- Designs environmental hazards and tactical scenarios
- Balances encounter difficulty for party composition
- Integrates encounters with story beats and world setting
- Provides stat blocks, tactics, and treasure rewards
- Saves encounter data to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/encounters.json`
- **Memory**: SQLite with balanced profile (max_history: 1500)
- **Config**: max_turns: 70, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

### NPC Creator Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Generates NPCs with distinct personalities, motivations, and backgrounds
- Creates stat blocks appropriate for NPC roles (ally, enemy, neutral)
- Designs memorable quirks, secrets, and relationships
- Integrates NPCs into world's factions and storyline
- Provides roleplay guidance and voice suggestions
- Saves NPC data to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/npcs.json`
- **Memory**: SQLite with balanced profile (max_history: 1500)
- **Config**: max_turns: 70, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

### Session Planner Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Plans session-by-session breakdown with clear objectives
- Organizes story beats, encounters, and roleplay scenes
- Provides pacing guidance and estimated session length
- Includes hooks to connect sessions and maintain momentum
- Creates session prep notes for DMs
- Saves session plans to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/sessions.json`
- **Memory**: SQLite with balanced profile (max_history: 1500)
- **Config**: max_turns: 70, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

### Campaign Publisher Agent
- **Communication Model**: Request-response (calls `receive_message` once, then responds)
- Compiles all campaign content into organized final document
- Formats content with proper headers, tables, and stat blocks
- Creates table of contents and index
- Generates PDF or markdown formatted campaign book
- Includes DM notes, player handouts, and reference materials
- Saves final document to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/campaign.pdf`
- **Memory**: SQLite with balanced profile (max_history: 1500)
- **Config**: max_turns: 60, max_tool_executions: 150, timeout: 600s
- **Tools**: shell_execute, tool_search, send_message, receive_message

## Configuration

### Memory Profiles

Each agent uses a different memory compression profile optimized for its workload:

- **Coordinator** (`conversational`): Optimized for back-and-forth conversation with user (1000 messages)
- **World Builder** (`data_intensive`): Handles large world generation outputs (2000 messages)
- **Other Specialists** (`balanced`): Mix of creative generation and structured output (1500 messages)

### Tool Discovery

All agents use dynamic tool discovery via `tool_search`:
- Coordinator discovers `send_message` tool
- All specialists discover `send_message` and `receive_message` tools

### Self-Correction and Observability

All agents have:
- **Self-correction**: Enabled for automatic error recovery
- **Observability**: Full tracing and metrics export to Hawk
- **Workflow tags**: All agents tagged with `workflow: dnd-campaign`, `domain: gaming`

## Output Artifacts

The workflow saves detailed artifacts to `~/.loom/artifacts/dnd-campaigns/{campaign_id}/`:

- `overview.md` - Complete campaign overview (coordinator)
- `world.json` - World setting and geography (world-builder)
- `story.json` - Campaign storyline and plot (storyline-designer)
- `encounters.json` - Combat encounters and challenges (encounter-designer)
- `npcs.json` - Non-player characters (npc-creator)
- `sessions.json` - Session-by-session breakdown (session-planner)
- `campaign.pdf` - Final compiled campaign document (campaign-publisher)

## Troubleshooting

### Specialist agents not responding
- Check that all agents are running in the workflow
- Verify agent IDs in send_message calls: must be `dnd-campaign-workflow:world-builder` format
- Check looms server logs for message delivery issues

### Content inconsistencies
- Ensure coordinator is passing campaign_id to all specialists
- Verify artifacts are being saved to correct directory
- Check that agents are reading previous agent artifacts when needed

### Campaign generation timeout
- Default timeout is 3600s (60 minutes)
- Adjust timeout in workflow config if creating very large campaigns
- Consider splitting large campaigns into multiple workflows

## Dependencies

### Required Tools (built into Loom)

All agents have access to:
- `shell_execute` - Execute shell commands
- `tool_search` - Discover available tools dynamically
- `get_error_detail` - Get detailed error information
- `search_conversation` - Search conversation history
- `recall_conversation` - Recall specific conversation segments
- `clear_recalled_context` - Clear recalled context

### Communication Tools

- **Coordinator**: `send_message` only (event-driven, no receive)
- **Specialists**: `send_message` and `receive_message`
- **Note**: This workflow does NOT use broadcast bus or shared_memory tools

## Development

### Testing Individual Agents

```bash
# Test coordinator (event-driven)
loom --thread dnd-coordinator

# Test world builder (request-response)
loom --thread dnd-world-builder

# Test storyline designer (request-response)
loom --thread dnd-storyline-designer

# Test encounter designer (request-response)
loom --thread dnd-encounter-designer

# Test NPC creator (request-response)
loom --thread dnd-npc-creator

# Test session planner (request-response)
loom --thread dnd-session-planner

# Test campaign publisher (request-response)
loom --thread dnd-campaign-publisher
```

### Understanding Event-Driven Coordinator

The coordinator is unique - it does NOT poll for messages. When you provide campaign requirements:
1. It calls `send_message` to delegate work to specialists
2. It tells you it's coordinating the campaign creation
3. Responses from specialists are automatically injected into its conversation
4. It sees the responses and synthesizes them for you
5. Once all content is ready, it sends to publisher for final compilation

This eliminates polling delays and ensures instant coordination.

## Architecture Notes

### Why Event-Driven Coordinator?

Traditional hub-and-spoke patterns require the hub to poll for responses:
```
send_message → poll receive_message → timeout/retry logic
```

This workflow's coordinator is event-driven:
```
send_message → system injects responses automatically → coordinator sees responses
```

Benefits:
- **Zero polling overhead**: No wasted API calls checking for messages
- **Instant notification**: Coordinator sees responses immediately when ready
- **Simpler logic**: No timeout/retry management needed
- **Better UX**: User sees progress updates instead of waiting periods

### Specialist Agent Pattern

All specialist agents follow a simple request-response pattern:
1. Wait for notification of pending message
2. Call `receive_message` ONCE to get the request
3. Process the request (generate world/story/encounters/NPCs/sessions/compilation)
4. Save results to artifact file
5. Call `send_message` to send complete response with artifact path
6. Wait for next notification

This pattern ensures specialists don't poll and waste resources.

### Artifact-Based Communication

Campaign data flows through artifacts, not just messages:
- Each agent saves its output to a JSON artifact file
- Subsequent agents can read previous artifacts if needed
- Coordinator tracks all artifact paths
- Publisher compiles all artifacts into final document

This allows:
- **Persistence**: Campaign content survives across workflow restarts
- **Inspection**: Users can examine intermediate artifacts
- **Reusability**: Artifacts can be loaded into other workflows or tools

## License

Part of the Loom agent framework examples.
