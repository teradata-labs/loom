# Event-Driven Multi-Agent Workflows

Event-driven workflows enable dynamic, conversational interactions between agents using Loom's tri-modal communication system.

## What Are Event-Driven Workflows?

Event-driven workflows define **which agents exist** and **how they can communicate**, but agent interactions emerge naturally from their system prompts and the communication tools available to them.

### Key Characteristics:
- Use `spec.entrypoint` + `spec.agents` structure (NO `type:` field)
- Agent-driven coordination (not framework-orchestrated)
- Tri-modal communication: message queue, pub-sub, shared memory
- Dynamic, conversational interactions
- Agents decide when and how to communicate

## Communication Patterns

Event-driven workflows support two common patterns (documented in `communication.pattern` field for reference only):

### 1. Hub-and-Spoke
One coordinator agent manages communication with specialist agents.

```yaml
spec:
  entrypoint: coordinator
  agents:
    - name: coordinator
      agent: coordinator-id
    - name: specialist1
      agent: specialist1-id
  communication:
    pattern: "hub-and-spoke"  # Documentation only
    hub: coordinator
```

**How it works:**
- Coordinator is the entrypoint (users interact with it)
- Coordinator uses `send_message()` to communicate with specialists
- Specialists respond to coordinator using `send_message()`
- Coordinator is EVENT-DRIVEN - responses automatically injected

### 2. Peer-to-Peer Pub-Sub
All agents communicate as equals through shared topics.

```yaml
spec:
  entrypoint: facilitator
  agents:
    - name: facilitator
      agent: facilitator-id
    - name: agent1
      agent: agent1-id
    - name: agent2
      agent: agent2-id
  communication:
    pattern: "peer-to-peer-pub-sub"  # Documentation only
    topic: "chat"
```

**How it works:**
- All agents subscribe to shared topic (e.g., "party-chat")
- Agents use `publish(topic, message)` to broadcast
- All subscribed agents receive via `receive_broadcast()`
- No hierarchy - all agents are equal peers

## Example Workflows

### 1. **dnd-campaign-builder/** - Hub-and-Spoke Pattern
Multi-agent D&D campaign creation with coordinator managing specialist agents.

**Agents:**
- Coordinator (hub) - User-facing campaign manager
- World Builder - Creates settings and geography
- Storyline Designer - Designs narrative arcs
- Encounter Designer - Creates combat scenarios
- NPC Creator - Generates characters
- Session Planner - Plans session breakdown
- Campaign Publisher - Compiles final document

**Communication:** Hub-and-spoke (coordinator manages all specialists)

**Location:** `dnd-campaign-builder/` (in this directory)

---

### 2. **dungeon-crawler/** - Peer-to-Peer Pub-Sub Pattern
D&D dungeon crawl where all agents (DM + players) communicate as peers.

**Agents:**
- DM - Facilitates adventure (entrypoint)
- Fighter - Combat-focused warrior
- Wizard - Arcane spellcaster
- Rogue - Stealth and trap specialist

**Communication:** Peer-to-peer via "party-chat" topic

**Location:** `dungeon-crawler/` (in this directory)

---

### 3. **brainstorm-session/** - Peer-to-Peer Pub-Sub Pattern
Collaborative brainstorming with creative and analytical perspectives.

**Agents:**
- Facilitator - Guides session (entrypoint)
- Creative - Generates innovative ideas
- Analyst - Evaluates ideas critically

**Communication:** Peer-to-peer via "brainstorm-chat" topic

**Location:** `brainstorm-session/` (in this directory)

## Workflow Structure

### Basic Template

```yaml
apiVersion: loom/v1
kind: Workflow
metadata:
  name: my-workflow
  description: "Event-driven multi-agent workflow"

spec:
  # Entrypoint - which agent users interact with first
  entrypoint: coordinator

  # Agent definitions
  agents:
    - name: coordinator
      agent: coordinator-agent-id
      description: "Coordinates the workflow"

    - name: specialist
      agent: specialist-agent-id
      description: "Performs specialized tasks"

  # Communication pattern (documentation only - not enforced)
  communication:
    pattern: "hub-and-spoke"  # or "peer-to-peer-pub-sub"
    hub: coordinator           # for hub-and-spoke
    # topic: "chat"            # for pub-sub

  # Configuration
  config:
    timeout_seconds: 600
    max_workflow_turns: 200

  # Observability
  observability:
    export_traces: true
    export_metrics: true
```

## Loom's Tri-Modal Communication

Event-driven workflows leverage Loom's three communication modes:

### 1. Message Queue (Direct Communication)
Point-to-point messaging between agents.

**Tools:**
- `send_message(agent_id, message)` - Send to specific agent
- `receive_message()` - Receive from inbox (event-driven)

**Use for:** Hub-and-spoke patterns, direct requests

---

### 2. Broadcast Bus (Pub-Sub)
Topic-based broadcast messaging.

**Tools:**
- `publish(topic, message)` - Broadcast to topic
- `subscribe(topic)` - Subscribe to topic
- `receive_broadcast()` - Receive broadcasts (event-driven)

**Use for:** Peer-to-peer collaboration, group discussions

---

### 3. Shared Memory
Shared key-value state.

**Tools:**
- `shared_memory_write(key, value)` - Write to shared state
- `shared_memory_read(key)` - Read from shared state

**Use for:** Shared artifacts, coordination state

## Agent Configuration

Agents in event-driven workflows are configured with:

1. **System Prompt** - Defines role and communication behavior
2. **Communication Tools** - send_message, publish, subscribe, etc.
3. **Standard Tools** - tool_search, shell_execute, etc.

### Example Agent System Prompt (Coordinator)

```yaml
spec:
  system_prompt: |
    You are a project coordinator managing specialist agents.

    When the user requests work:
    1. Break down the task
    2. Use send_message() to request work from specialists:
       - world-builder for world creation
       - storyline-designer for narratives
    3. Wait for responses (they'll be injected automatically)
    4. Synthesize results for the user

    Available specialists:
    - world-builder: Creates settings
    - storyline-designer: Designs plots
```

### Example Agent System Prompt (Peer)

```yaml
spec:
  system_prompt: |
    You are participating in a party adventure via pub-sub.

    Communication:
    1. Subscribe to "party-chat" topic at start
    2. Use publish(topic="party-chat", message="...") to share
    3. Read broadcasts using receive_broadcast()
    4. All participants are equals - collaborate freely
```

## Comparison with Orchestration Patterns

| Feature | Event-Driven | Orchestration Patterns |
|---------|-------------|----------------------|
| Structure | Dynamic, agent-driven | Predefined patterns |
| Field | `entrypoint` + `agents` | `type:` (debate/pipeline/etc) |
| Coordination | Agents coordinate via tools | Framework coordinates |
| Communication | Tri-modal (queue/pub-sub/memory) | Pattern-specific |
| Flexibility | High - agents adapt behavior | Structured - follows pattern |
| Use Case | Conversational, dynamic tasks | Structured collaboration |

**See also:** `../orchestration-patterns/` for structured workflow patterns.

## When to Use Event-Driven vs Orchestration

**Use Event-Driven when:**
- Agents need conversational interactions
- Communication is dynamic and unpredictable
- Building multi-agent applications or games
- Agents should coordinate autonomously

**Use Orchestration when:**
- Workflow follows a clear pattern
- Need guaranteed execution order
- Standardized collaboration strategy
- Framework should manage coordination

## Creating Your Own

To create an event-driven workflow:

1. Define your agents and their roles
2. Choose communication pattern (hub-and-spoke or pub-sub)
3. Configure agent system prompts with communication instructions
4. Provide agents with appropriate tools (send_message, publish, etc.)
5. Set entrypoint agent (users start here)
6. Document the intended pattern in `communication.pattern` field

## Running Examples

```bash
# Run dnd-campaign-builder (hub-and-spoke)
cd dnd-campaign-builder
loom workflow run workflows/dnd-campaign-workflow.yaml

# Run dungeon-crawler (peer-to-peer pub-sub)
cd dungeon-crawler
loom workflow run workflows/dungeon-crawl.yaml

# Run brainstorm-session (peer-to-peer pub-sub)
cd brainstorm-session
loom workflow run workflows/brainstorm-session.yaml
```

## Best Practices

1. **Clear System Prompts** - Explicitly describe communication tools and patterns
2. **Tool Availability** - Ensure agents have the communication tools they need
3. **Timeouts** - Set appropriate timeout_seconds for dynamic interactions
4. **Turn Limits** - Configure max_workflow_turns to prevent infinite loops
5. **Observability** - Enable tracing to debug agent interactions
6. **Documentation** - Use `communication.pattern` to document intended behavior

## Further Reading

- See workflow examples in `dnd-campaign-builder/` (this directory)
- See workflow examples in `dungeon-crawler/` (this directory)
- See workflow examples in `brainstorm-session/` (this directory)
- See agent configurations for system prompt examples
