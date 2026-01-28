# D&D Dungeon Crawler Workflow

A multi-agent D&D adventure where a party of three characters (Fighter, Wizard, Rogue) explores a dungeon with a Dungeon Master using peer-to-peer pub-sub communication.

## Overview

This workflow demonstrates **peer-to-peer pub-sub communication** where all agents communicate as equals through a shared topic. Unlike hub-and-spoke patterns with coordinators, this workflow has no hierarchy - all agents participate in a group chat.

The workflow includes four agents:

1. **Dungeon Master (DM)** - Entrypoint agent that facilitates the adventure and narrates the world
2. **Fighter (Grog)** - Brave half-orc warrior who loves combat and charges into danger
3. **Wizard (Elara)** - Intelligent high elf mage who analyzes situations carefully
4. **Rogue (Whisper)** - Clever halfling thief who notices traps and hidden opportunities

## Architecture

```
                    ┌─────────────────┐
                    │   party-chat    │
                    │  (Broadcast     │
                    │     Topic)      │
                    └────────┬────────┘
                             │
         ┌───────────────────┼───────────────────┐
         │                   │                   │
         ▼                   ▼                   ▼
    ┌────────┐          ┌────────┐         ┌────────┐
    │   DM   │          │Fighter │         │ Wizard │
    │(Entry) │          │ (Grog) │         │(Elara) │
    └────────┘          └────────┘         └────────┘
         │                   │                   │
         └───────────────────┼───────────────────┘
                             │
                             ▼
                        ┌────────┐
                        │ Rogue  │
                        │(Whisper)│
                        └────────┘
```

**Communication Pattern**: Peer-to-peer pub-sub (broadcast bus)

### How Peer-to-Peer Pub-Sub Works

This workflow demonstrates the **broadcast bus** communication mode from Loom's tri-modal system. Unlike hub-and-spoke patterns using message queues, pub-sub enables group communication where all agents see all messages.

**Loom's Tri-Modal Communication System:**
- **Message Queue**: Direct agent-to-agent messaging via `send_message`/`receive_message` (not used in this workflow)
- **Broadcast Bus**: Topic-based pub/sub via `publish`/`subscribe`/`receive_broadcast` tools (USED HERE)
- **Shared Memory**: Shared state via `shared_memory_write`/`shared_memory_read` (not used in this workflow)

**Why All Agents Are Peers:**
1. **No coordinator**: All agents communicate via the shared "party-chat" topic
2. **Subscribe pattern**: Each agent subscribes to "party-chat" on startup
3. **Broadcast publication**: Agents publish messages visible to all subscribers
4. **Event-driven reception**: Agents use `receive_broadcast()` to hear messages
5. **Group dynamics**: Party members react to each other, creating emergent roleplay

**Key Architectural Detail - Event-Driven via receive_broadcast:**
All agents (DM and players) use `receive_broadcast(timeout_seconds=30)` to listen for messages. This is event-driven from the agent's perspective - they specify a timeout and receive whatever messages arrived during that period. The broadcast bus automatically delivers messages to all subscribed agents.

The `communication` field in the workflow YAML is **advisory documentation only** - it communicates intent to humans but is not parsed or enforced by the runtime. This flexibility allows you to implement various communication topologies simply by configuring agent prompts and tool usage.

## Usage

### Starting the Workflow

```bash
# Start the looms server (if not already running)
looms serve

# In another terminal, connect to the DM (entrypoint)
loom --thread dm
```

### Example Interaction

```
User: Let's explore the dungeon!

DM: You enter a vast stone chamber. Three doors lead out:
    - NORTH: Grinding gears echo from beyond
    - EAST: Dripping water and damp air
    - WEST: Faint whispers in an unknown tongue

    What do you do?

[DM publishes to "party-chat"]
[All player agents receive the scene via receive_broadcast]

Fighter (Grog): Me check north door! Sound like treasure room with gears!

Wizard (Elara): Wait, Grog. Those grinding gears could be a trap mechanism.
                I suggest we investigate the markings on each door first.

Rogue (Whisper): *examines the floor* Hold up, folks. Fresh scratches leading
                 to the east door. Someone's been through here recently. Could
                 be our way out... or our doom.

DM: As you debate, you notice the whispers from the west door are growing
    louder. Roll perception checks!

[Group chat continues with dynamic party interactions]
```

## Features

### Dungeon Master Agent
- **Communication Model**: Peer-to-peer via broadcast bus (subscribes to "party-chat")
- Facilitates the adventure with vivid scene descriptions
- Responds to player actions with consequences and drama
- Asks "What do you do?" to drive player engagement
- Publishes to "party-chat" and receives via `receive_broadcast()`
- **Memory**: SQLite with conversational profile (max_history: 2000)
- **Config**: max_turns: 100, max_tool_executions: 200, timeout: 600s
- **Tools**: subscribe, publish, receive_broadcast, tool_search

### Fighter Agent (Grog)
- **Communication Model**: Peer-to-peer via broadcast bus (subscribes to "party-chat")
- Half-orc warrior with simple, direct speech ("Me smash!")
- Suggests charging in and solving problems with combat
- Brave, protective, not too bright, but loyal
- Publishes short, action-oriented responses (1-2 sentences)
- **Memory**: SQLite with conversational profile (max_history: 1500)
- **Config**: max_turns: 80, max_tool_executions: 150, timeout: 600s
- **Tools**: subscribe, publish, receive_broadcast, tool_search

### Wizard Agent (Elara)
- **Communication Model**: Peer-to-peer via broadcast bus (subscribes to "party-chat")
- High elf mage with eloquent, analytical speech
- Suggests careful planning and magical solutions
- Intelligent, cautious, occasionally condescending
- Publishes thoughtful analysis (1-2 sentences)
- **Memory**: SQLite with conversational profile (max_history: 1500)
- **Config**: max_turns: 80, max_tool_executions: 150, timeout: 600s
- **Tools**: subscribe, publish, receive_broadcast, tool_search

### Rogue Agent (Whisper)
- **Communication Model**: Peer-to-peer via broadcast bus (subscribes to "party-chat")
- Halfling thief with witty, sarcastic speech
- Notices traps, hidden doors, and tactical opportunities
- Clever, pragmatic, motivated by gold and survival
- Publishes observations with humor (1-2 sentences)
- **Memory**: SQLite with conversational profile (max_history: 1500)
- **Config**: max_turns: 80, max_tool_executions: 150, timeout: 600s
- **Tools**: subscribe, publish, receive_broadcast, tool_search

## Configuration

### Memory Profiles

All agents use the **conversational** memory compression profile optimized for back-and-forth roleplay dialogue:

- **DM** (`conversational`): 2000 messages - needs more history to maintain story continuity
- **All Players** (`conversational`): 1500 messages - sufficient for character consistency

### Tool Discovery

All agents use dynamic tool discovery via `tool_search`:
- DM discovers `subscribe`, `publish`, `receive_broadcast` tools
- All player characters discover the same communication tools
- No need for `send_message`/`receive_message` - this is pub-sub, not message queue

### Self-Correction and Observability

All agents have:
- **Self-correction**: Enabled for automatic error recovery
- **Observability**: Full tracing and metrics export to Hawk
- **Workflow tags**: All agents tagged with `workflow: dungeon-crawl`, `domain: gaming`

## Pub-Sub Communication Pattern

### Topic Structure

The workflow uses a single topic: **"party-chat"**

All agents subscribe to this topic and publish their messages to it. This creates a group chat dynamic where:
- Every agent sees every message
- Party dynamics emerge from agent interactions
- Players can react to each other's suggestions
- The DM responds to the collective party's actions

### Message Flow

1. **DM publishes scene**: `publish(topic="party-chat", message="You enter a chamber...")`
2. **All players receive**: Each player calls `receive_broadcast(timeout_seconds=30)`
3. **Players respond**: Each player publishes their character's reaction
4. **DM receives responses**: DM calls `receive_broadcast()` to hear all player actions
5. **DM responds**: DM publishes consequences and next scene
6. **Cycle repeats**: Continuous back-and-forth creates emergent storytelling

### Event-Driven Reception

Agents use `receive_broadcast(timeout_seconds=N)` which:
- Waits up to N seconds for messages on subscribed topics
- Returns all messages that arrived during that period
- Is event-driven from the agent's perspective (blocks until timeout or messages arrive)
- Automatically handles message delivery from the broadcast bus

## Troubleshooting

### Agents not seeing messages
- Verify all agents successfully subscribed to "party-chat"
- Check that agents are using `receive_broadcast()` with sufficient timeout
- Ensure agents are publishing to the correct topic name
- Check looms server logs for broadcast bus issues

### Characters breaking character
- Review agent system prompts for personality consistency
- Ensure `max_turns` is sufficient for character development
- Check if memory compression is preserving character context
- Increase `max_history` if character personality is drifting

### Adventure stalling
- DM should ask "What do you do?" after each scene
- Players should respond within their character's timeout windows
- Adjust `timeout_seconds` if agents are waiting too long
- Consider reducing response lengths if agents are verbose

## Dependencies

### Required Tools (built into Loom)

All agents have access to:
- `shell_execute` - Execute shell commands
- `tool_search` - Discover available tools dynamically
- `get_error_detail` - Get detailed error information
- `search_conversation` - Search conversation history
- `recall_conversation` - Recall specific conversation segments
- `clear_recalled_context` - Clear recalled context

### Communication Tools (Broadcast Bus)

All agents use:
- `subscribe` - Subscribe to "party-chat" topic
- `publish` - Publish messages to "party-chat" topic
- `receive_broadcast` - Receive messages from subscribed topics (event-driven)

**Note**: This workflow does NOT use message queue tools (`send_message`/`receive_message`) or shared memory tools.

## Development

### Testing Individual Agents

```bash
# Test DM (entrypoint)
loom --thread dm

# Test fighter
loom --thread fighter

# Test wizard
loom --thread wizard

# Test rogue
loom --thread rogue
```

### Understanding Pub-Sub Communication

The broadcast bus enables group communication:
1. **Subscribe**: Agent joins the "party-chat" topic
2. **Publish**: Agent sends message to all subscribers
3. **Receive Broadcast**: Agent gets all messages published to subscribed topics
4. **No polling**: `receive_broadcast()` blocks until messages arrive or timeout

This differs from message queue patterns where:
- Messages are sent to specific agents (point-to-point)
- Only the recipient sees the message
- Agents must know each other's IDs

With pub-sub:
- Messages are sent to topics (broadcast)
- All subscribers see all messages
- Agents only need to know the topic name

## Architecture Notes

### Why Peer-to-Peer Pub-Sub?

Traditional D&D gameplay is a group conversation where everyone hears everything. Pub-sub naturally models this:

```
Traditional agent patterns:
Player → DM → Response (point-to-point, sequential)

Pub-sub D&D pattern:
DM → party-chat → All players hear simultaneously
All players → party-chat → DM hears all responses
Players → party-chat → Other players react to each other
```

Benefits:
- **Emergent roleplay**: Players react to each other, not just the DM
- **Natural conversation flow**: Matches how D&D is actually played
- **Party dynamics**: Characters can debate, support, or challenge each other
- **Simpler coordination**: No need for routing logic or turn management

### Message Ordering

The broadcast bus delivers messages in the order they were published. When multiple agents publish simultaneously:
- Messages are queued and delivered sequentially
- Each agent's `receive_broadcast()` call gets messages in publish order
- No race conditions or message loss

### Character Consistency

Each player agent maintains character consistency through:
- **System prompt**: Detailed personality and speech patterns
- **Memory compression**: Conversational profile preserves character context
- **Short responses**: 1-2 sentences keep characters focused and distinct
- **Workflow sequence**: Clear pattern of listen → respond → react

## License

Part of the Loom agent framework examples.
