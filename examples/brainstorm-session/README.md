# Brainstorming Session - Peer-to-Peer Pub-Sub Workflow

A collaborative brainstorming workflow demonstrating **peer-to-peer pub-sub communication** where three agents work together as equals to generate and evaluate innovative ideas.

## Overview

This workflow showcases Loom's **Broadcast Bus** communication pattern where agents communicate as peers via topic-based pub-sub messaging.

### Agents

1. **Facilitator** - Introduces the topic, asks questions, synthesizes insights
2. **Creative** - Generates wild, innovative, outside-the-box ideas
3. **Analyst** - Evaluates ideas critically but constructively

All agents are equal participants who communicate via the `brainstorm-chat` topic.

## Communication Pattern: Peer-to-Peer Pub-Sub

```yaml
communication:
  pattern: "peer-to-peer-pub-sub"
  topic: "brainstorm-chat"
  entrypoint: facilitator
```

### How It Works

1. User chats with the facilitator (workflow entrypoint)
2. Facilitator subscribes to `brainstorm-chat` and publishes opening message
3. **Sub-agents auto-spawn automatically** when they receive the broadcast
   - `brainstorm-session:creative` spawns and subscribes
   - `brainstorm-session:analyst` spawns and subscribes
4. Sub-agents process messages and publish responses back to the topic
5. Facilitator receives responses via `receive_broadcast(timeout_seconds=30)`
6. **No hierarchy** - all agents are equal peers in the conversation
7. Conversation continues with all participants contributing

**Key Innovation**: Sub-agents spawn on-demand when they have pending messages. No manual spawn management needed!

### Tri-Modal Communication

This workflow uses Loom's **Broadcast Bus** (one of three communication modes):

- **Message Queue**: Direct 1:1 messages (not used here)
- **Broadcast Bus**: Topic-based pub-sub (USED: subscribe, publish, receive_broadcast)
- **Shared Memory**: Key-value store (not used here)

## Running the Workflow

### Prerequisites

- Loom CLI installed
- LLM provider configured (Anthropic Claude recommended)
- Agents and workflow loaded

### Load the Workflow

```bash
# From the loom-public-c2 directory
cd examples/brainstorm-session

# Start loom server (if not already running)
loom-server

# In another terminal, start a chat session with the facilitator
loom chat brainstorm-session:facilitator
```

### Example Session

```
You: Let's brainstorm ideas for improving team collaboration in remote work environments

[Facilitator subscribes to brainstorm-chat and introduces the topic]
[Creative generates innovative ideas]
[Analyst evaluates feasibility and suggests improvements]
[Discussion continues for 4-6 exchanges]
[Facilitator summarizes key insights]
```

## What Makes This Pub-Sub?

Unlike hub-and-spoke workflows where specialists only talk to a coordinator:

- ✅ All agents subscribe to the same topic
- ✅ Any agent can broadcast to all others
- ✅ All agents receive all messages (group chat)
- ✅ No central coordinator routing messages
- ✅ Agents respond to each other directly
- ✅ **Sub-agents auto-spawn when they receive messages** (no manual lifecycle management)

## How Auto-Spawning Works

The Loom server's message queue monitor detects when workflow sub-agents have pending pub-sub messages and automatically:

1. Spawns the agent with a new session
2. Registers notification channels for event-driven processing
3. Calls the agent's Chat() method to process pending messages
4. The agent subscribes to topics and publishes responses
5. Other agents receive the responses via `receive_broadcast()`

This means you don't need `manage_ephemeral_agents` tool - just publish and wait!

## Use Cases

This pattern works well for:

- Collaborative brainstorming
- Team discussions and debates
- Multi-perspective analysis
- Peer review sessions
- Group decision-making

## Files

```
brainstorm-session/
├── agents/
│   ├── facilitator.yaml  # Starts discussion, synthesizes
│   ├── creative.yaml     # Generates innovative ideas
│   └── analyst.yaml      # Evaluates ideas critically
├── workflows/
│   └── brainstorm.yaml   # Workflow definition
└── README.md             # This file
```

## Configuration

### Workflow Timeout

- Default: 300 seconds (5 minutes)
- Configurable in `workflows/brainstorm.yaml`

### Agent Turns

- Max workflow turns: 150
- Each agent can take up to 50 turns
- Typical session: 4-6 exchanges

## Observability

All agents export traces and metrics:

```yaml
observability:
  export_traces: true
  export_metrics: true
  tags:
    workflow: brainstorm-session
    pattern: pub-sub
```

## Extending the Workflow

Add more agents to the brainstorming session:

1. Create a new agent YAML in `agents/`
2. Add it to the workflow's `agents` list
3. Ensure the agent subscribes to `brainstorm-chat`
4. Use `publish()` and `receive_broadcast()` tools

The pub-sub pattern scales naturally to any number of peer agents!
