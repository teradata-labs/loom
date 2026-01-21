# spawn_agent Tool Design

## Overview
The `spawn_agent` tool enables any agent to dynamically spawn sub-agents for:
- **Interactive workflows** - Spawn party members on demand (D&D scenario)
- **Context isolation** - Create fresh agent with clean context for specific tasks
- **Parallel delegation** - Spawn multiple specialists to work concurrently
- **Dynamic scaling** - Create agents as needed, not all upfront

## Tool API

### Tool Name
`spawn_agent`

### Input Parameters

```typescript
{
  "agent_id": string,              // REQUIRED: Agent config to spawn (e.g., "fighter", "analyst")
  "workflow_id"?: string,          // OPTIONAL: Workflow namespace (e.g., "dungeon-crawl-workflow")
                                   //           Creates ID: "workflow_id:agent_id"
  "initial_message"?: string,      // OPTIONAL: First message to send to spawned agent
  "auto_subscribe"?: string[],     // OPTIONAL: Topics to auto-subscribe (e.g., ["party-chat"])
  "metadata"?: map<string, string> // OPTIONAL: Metadata for tracking (e.g., {"role": "fighter"})
}
```

### Return Value

```typescript
{
  "success": true,
  "sub_agent_id": string,          // Full agent ID (with workflow prefix if provided)
  "session_id": string,            // New session ID for the sub-agent
  "status": "spawned",             // Status: "spawned", "running", "error"
  "subscribed_topics": string[]    // Topics the agent auto-subscribed to
}
```

### Error Responses

```typescript
{
  "success": false,
  "error": {
    "code": "AGENT_NOT_FOUND",     // Agent config doesn't exist
    "message": "Agent 'fighter' not found in registry",
    "suggestion": "Check ~/.loom/agents/ for available agents"
  }
}

{
  "success": false,
  "error": {
    "code": "SPAWN_FAILED",
    "message": "Failed to create agent session: ...",
    "retryable": true
  }
}
```

## Server-Side Architecture

### 1. Sub-Agent Tracking

Add to `MultiAgentServer`:

```go
// Track spawned sub-agents for lifecycle management
type spawnedAgent struct {
    parentSessionID  string
    subAgentID       string
    subSessionID     string
    workflowID       string
    spawnedAt        time.Time
    subscriptions    []string  // Topics subscribed to
    metadata         map[string]string
}

// MultiAgentServer additions
type MultiAgentServer struct {
    // ... existing fields ...

    spawnedAgents   map[string]*spawnedAgent  // subSessionID -> spawnedAgent
    spawnedAgentsMu sync.RWMutex
}
```

### 2. Spawn Flow

```
1. Parent agent calls spawn_agent("fighter", ...)
2. SpawnAgentTool validates agent_id exists in registry
3. Tool calls server.SpawnSubAgent(ctx, req)
4. Server creates new agent instance from registry
5. Server creates new session for sub-agent
6. Server auto-subscribes to specified topics
7. Server tracks spawned agent: parent -> sub mapping
8. Server sends initial_message if provided
9. Server starts background goroutine to monitor sub-agent
10. Return success response with session_id
```

### 3. Lifecycle Management

#### Parent Session Ends
```go
// When parent session ends (user disconnects, timeout, etc.)
func (s *MultiAgentServer) cleanupSession(sessionID string) {
    // Find all spawned agents
    spawned := s.getSpawnedAgentsByParent(sessionID)

    // Gracefully shutdown each sub-agent
    for _, sub := range spawned {
        s.shutdownSubAgent(sub.subSessionID, "parent session ended")
    }
}
```

#### Sub-Agent Ends
```go
// When sub-agent reaches max turns, timeout, or error
func (s *MultiAgentServer) monitorSubAgent(subSessionID string) {
    // Background goroutine monitors agent health
    for {
        session := s.sessionStore.Get(subSessionID)
        if session.IsComplete() || session.HasError() {
            s.cleanupSubAgent(subSessionID)
            return
        }
        time.Sleep(5 * time.Second)
    }
}
```

### 4. Communication Patterns

#### Pattern A: Pub/Sub (Peer-to-Peer)
```
DM spawns:     spawn_agent("fighter", auto_subscribe=["party-chat"])
DM subscribes: subscribe("party-chat")
DM publishes:  publish("party-chat", "A goblin appears!")
Fighter hears: receive_broadcast() -> automatic notification
Fighter acts:  publish("party-chat", "I attack!")
DM hears:      receive_broadcast() -> automatic notification
```

#### Pattern B: Message Queue (Direct)
```
DM spawns:    spawn_agent("analyst")
DM sends:     send_message("analyst", "Analyze this data: ...")
Analyst gets: receive_message() -> processes task
Analyst sends: send_message("dm", "Result: ...")
DM gets:      receive_message()
```

#### Pattern C: Hybrid
```
DM spawns:    spawn_agent("fighter", auto_subscribe=["party-chat"])
DM broadcasts: publish("party-chat", "Combat round 1!")  # Everyone hears
DM directs:    send_message("fighter", "Attack goblin #3")  # Only fighter hears
```

## Implementation Plan

### Phase 1: Core spawn_agent Tool
- [ ] Create `pkg/shuttle/builtin/spawn_agent.go`
- [ ] Implement `SpawnAgentTool` with proper validation
- [ ] Add to builtin registry

### Phase 2: Server Integration
- [ ] Add `SpawnSubAgent()` RPC handler to MultiAgentServer
- [ ] Implement sub-agent tracking data structures
- [ ] Add parent-to-child relationship management

### Phase 3: Auto-Subscribe
- [ ] Implement auto-subscription to pub/sub topics on spawn
- [ ] Register notification channels for event-driven messaging
- [ ] Test pub/sub communication between parent and spawned agents

### Phase 4: Lifecycle Management
- [ ] Implement cleanup on parent session end
- [ ] Add monitoring for sub-agent health
- [ ] Handle timeout and error conditions

### Phase 5: Testing
- [ ] Unit tests for SpawnAgentTool
- [ ] Integration tests for spawn + pub/sub
- [ ] Race condition testing with -race flag
- [ ] Test dungeon-crawl workflow end-to-end

## Usage Examples

### Example 1: D&D Workflow (Interactive)

```yaml
# DM agent system prompt
When user starts a conversation:
1. spawn_agent("fighter", workflow_id="dungeon-crawl", auto_subscribe=["party-chat"])
2. spawn_agent("wizard", workflow_id="dungeon-crawl", auto_subscribe=["party-chat"])
3. spawn_agent("rogue", workflow_id="dungeon-crawl", auto_subscribe=["party-chat"])
4. subscribe("party-chat")
5. publish("party-chat", "You enter a dark dungeon...")
6. receive_broadcast() to hear party responses
```

### Example 2: Parallel Analysis

```python
# Main agent spawns specialists
spawn_agent("sql-analyst", initial_message="Analyze query performance")
spawn_agent("security-analyst", initial_message="Check for SQL injection")
spawn_agent("cost-analyst", initial_message="Estimate compute cost")

# Collect results
results = []
for i in range(3):
    msg = receive_message()
    results.append(msg)
```

### Example 3: Context Isolation

```python
# Agent's context is getting bloated (2000+ messages)
# Spawn fresh agent to continue work
spawn_agent("self", initial_message="Continue the analysis with fresh context: ...")
```

## Security & Limits

### Spawn Limits
- Max spawned agents per parent: 10 (configurable)
- Max nesting depth: 3 levels (prevent spawn bombs)
- Rate limiting: Max 5 spawns per minute per parent

### Permissions
- Only allow spawning agents from trusted registry
- No arbitrary code execution via agent configs
- Spawned agents inherit parent's resource limits

### Resource Management
- Sub-agents count toward parent's token budget
- Parent cost tracking includes all spawned agents
- Memory limits apply to parent + all children combined

## Future Enhancements

1. **Spawn pools** - Pre-spawn N agents, reuse them for tasks
2. **Sub-agent templates** - Inline agent config without separate file
3. **Shared context** - Spawn with specific memory shared from parent
4. **Explicit shutdown** - `shutdown_agent(sub_agent_id)` tool
5. **Agent discovery** - `list_spawned_agents()` to see what's running
