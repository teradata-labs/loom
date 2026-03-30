
> **Status**: ✅ Fully implemented. The AGENT namespace provides automatic agent-private memory isolation with key scoping. See `pkg/communication/shared_memory.go` for the implementation.

## Problem Statement

### User Report
> "The shared memory store is actually causing an issue when 5 agents are talking - they all think they are the same character. Is this something we should consider in terms of loom architecture? like they need their own private memory store in addition to the session store for conversations?"

### Concrete Example: D&D Adventure

In a D&D adventure example with 5 player agents, all agents sharing the same `namespace="workflow"` leads to identity confusion:

```yaml
# Agent 1: Player Eldrin (Elf Wizard)
shared_memory_write(
  key="character_sheet",
  namespace="workflow",
  value="Name: Eldrin, Race: Elf, Class: Wizard"
)

# Agent 2: Player Luna (Human Rogue) - OVERWRITES Eldrin's data!
shared_memory_write(
  key="character_sheet",
  namespace="workflow",
  value="Name: Luna, Race: Human, Class: Rogue"
)

# Agent 3: Player Thorgrim (Dwarf Fighter) - Reads Luna's data!
shared_memory_read(
  key="character_sheet",
  namespace="workflow"
)
# Returns: "Name: Luna, Race: Human, Class: Rogue"
# Expected: Thorgrim's own character sheet!
```

**Result**: All agents experience identity confusion - they can't distinguish their own state from other agents' state.

## Architecture

### Memory Namespace Hierarchy

```
┌─────────────────────────────────────────────────┐
│ Session Memory (Per Agent)                      │
│ - Message history                               │
│ - Conversation context                          │
│ - Tool execution history                        │
│ Location: pkg/agent/memory.go                   │
│ Status: ✅ Implemented                          │
└─────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────┐
│ Shared Memory (Namespace-scoped)                │
│ - namespace="global"   → ALL agents see data    │
│ - namespace="workflow" → Workflow agents share   │
│ - namespace="swarm"    → Swarm agents share     │
│ - namespace="debate"   → Debate agents share    │
│ - namespace="session"  → User session scoped    │
│ - namespace="agent"    → Per-agent isolation     │
│ Location: pkg/communication/shared_memory.go    │
│ Status: ✅ Implemented (including AGENT)        │
└─────────────────────────────────────────────────┘
```

### Three-Layer Memory Model

```
┌─────────────────────────────────────────────────┐
│ What Agents Need                                │
├─────────────────────────────────────────────────┤
│ 1. Session Memory (conversation history)        │
│    ✅ Implemented - pkg/agent/memory.go         │
│                                                 │
│ 2. Shared Memory (workflow collaboration)       │
│    ✅ Implemented - pkg/communication/          │
│                     shared_memory.go            │
│                                                 │
│ 3. Agent-Private Memory (identity, goals)       │
│    ✅ Implemented - AGENT namespace with        │
│       automatic key scoping via scopeKey()      │
└─────────────────────────────────────────────────┘
```

## Implementation: AGENT Namespace

### Proto Definition

```protobuf
// proto/loom/v1/shared_memory.proto:22-37
enum SharedMemoryNamespace {
  SHARED_MEMORY_NAMESPACE_UNSPECIFIED = 0;
  SHARED_MEMORY_NAMESPACE_GLOBAL = 1;
  SHARED_MEMORY_NAMESPACE_WORKFLOW = 2;
  SHARED_MEMORY_NAMESPACE_SWARM = 3;
  SHARED_MEMORY_NAMESPACE_DEBATE = 4;
  SHARED_MEMORY_NAMESPACE_SESSION = 5;
  // Agent-private namespace (isolated per agent instance)
  // Keys are automatically scoped to the agent ID that writes them.
  // This provides strict isolation - agents cannot read each other's private data.
  SHARED_MEMORY_NAMESPACE_AGENT = 6;
}
```

### Storage Layer: Automatic Key Scoping

The `scopeKey()` function in `pkg/communication/shared_memory.go:140-145` automatically prefixes keys with the agent ID for the AGENT namespace:

```go
// scopeKey automatically prefixes the key with agent ID for AGENT namespace.
// For other namespaces, returns the key unchanged.
// This ensures strict isolation - agents cannot access each other's private data.
func scopeKey(namespace loomv1.SharedMemoryNamespace, agentID string, key string) string {
    if namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT {
        return fmt.Sprintf("agent:%s:%s", agentID, key)
    }
    return key
}
```

This function is called in all storage operations: `Put`, `Get`, `Delete`, `List`, and `Watch`. For the AGENT namespace, `List` and `Watch` additionally filter results so agents only see their own keys (with the prefix stripped from returned keys).

### Storage Layer Initialization

All namespaces, including AGENT, are initialized at store creation (`pkg/communication/shared_memory.go:122-132`):

```go
for _, ns := range []loomv1.SharedMemoryNamespace{
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_DEBATE,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SESSION,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT,
} {
    store.stats[ns] = &SharedMemoryNamespaceStats{namespace: ns}
    store.data[ns] = make(map[string]*loomv1.SharedMemoryValue)
}
```

### Builtin Tools

Both `shared_memory_write` and `shared_memory_read` expose the `agent` namespace (`pkg/shuttle/builtin/shared_memory.go:52-54`):

```go
"namespace": shuttle.NewStringSchema("Memory namespace. Use 'agent' for private data.").
    WithEnum("global", "workflow", "swarm", "agent").
    WithDefault("global"),
```

### Usage Pattern

Agents use `namespace="agent"` for private data with automatic key scoping:

```yaml
# Agent uses "agent" namespace - automatic scoping!
shared_memory_write(
  key="character_sheet",  # NO manual prefixing needed
  namespace="agent",
  value="Name: Eldrin, Race: Elf, Class: Wizard"
)

shared_memory_read(
  key="character_sheet",
  namespace="agent"
)
# Automatically returns THIS agent's data only
# Other agents cannot read this agent's "character_sheet"
```

**Properties**:
- ✅ Automatic key scoping (no manual prefixing by the agent)
- ✅ Built-in isolation guarantee (agents cannot accidentally cross-read)
- ✅ Consistent with other namespaces (uses the same tools and storage layer)
- ✅ Backwards compatible (existing workflows using global/workflow/swarm are unaffected)

## Design Decisions

### Decision 1: Storage-Layer Scoping vs. Tool-Layer Scoping

**Chosen Approach**: Storage layer auto-scoping via `scopeKey()`.

**Rationale**: Placing the scoping logic in the storage layer guarantees consistent behavior regardless of how the AGENT namespace is accessed (builtin tools, gRPC API, or direct Go calls). A single source of truth prevents bugs where one access path forgets to scope.

**Alternative Considered**: Scoping in the builtin tool layer. Rejected because it would only protect one access path and leave the gRPC API and programmatic access unprotected.

### Decision 2: AGENT Namespace vs. SESSION Namespace with Manual Prefixing

**Chosen Approach**: Dedicated AGENT namespace with value `6` in the proto enum.

**Rationale**: Manual key prefixing (e.g., `agent:{id}:character_sheet`) is error-prone - agents can typo the prefix or accidentally read other agents' data. The AGENT namespace makes isolation a structural guarantee rather than a convention.

**Alternative Considered**: Reusing the SESSION namespace (value `5`) with a convention of prefixing keys with agent IDs. This was considered as a quick workaround but rejected as the long-term solution because:
- "session" semantics are misleading for agent-private data
- No automatic isolation - agents could read each other's keys if they guessed the pattern
- Error-prone manual prefixing

### Decision 3: Session Context Map

**Not Chosen**: Using `Session.Context` (`pkg/types/types.go:263`) for agent-specific data.

**Rationale**: The `Session.Context map[string]interface{}` field exists but is not accessible via `shared_memory_read`/`shared_memory_write` tools. Using it for agent-private data would require a separate tool, breaking the unified memory abstraction and creating two parallel memory systems.

## Test Coverage

The AGENT namespace has dedicated tests in `pkg/communication/shared_memory_test.go`:

- `TestAgentNamespaceIsolation` - Verifies agents cannot access each other's private data
- `TestAgentNamespaceList` - Verifies `List` only returns keys for the requesting agent
- `TestAgentNamespaceDelete` - Verifies `Delete` only affects the requesting agent's data
- `TestAgentNamespaceWatch` - Verifies `Watch` only notifies for the requesting agent's updates
- `TestAgentNamespaceConcurrentAccess` - Verifies concurrent access from different agents is race-free

## References

- **Proto**: `proto/loom/v1/shared_memory.proto:22-37`
- **Storage**: `pkg/communication/shared_memory.go` (707 lines)
- **Builtin Tools**: `pkg/shuttle/builtin/shared_memory.go` (377 lines)
- **Agent Types**: `pkg/agent/types.go`
- **Session Store**: `pkg/agent/session_store.go`
- **Memory Documentation**: `docs/architecture/memory-systems.md`
