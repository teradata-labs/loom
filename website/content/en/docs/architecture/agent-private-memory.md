---
title: "Agent-Private Memory Architecture"
linkTitle: "Agent-Private Memory"
weight: 50
date: 2025-12-16
description: >
  Architectural design for agent-private state in multi-agent workflows
---

## Problem Statement

### User Report
> "The shared memory store is actually causing an issue when 5 agents are talking - they all think they are the same character. Is this something we should consider in terms of loom architecture? like they need their own private memory store in addition to the session store for conversations?"

### Concrete Example: D&D Adventure

In the D&D adventure example with 5 player agents, all agents share the same `namespace="workflow"`:

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

## Current Architecture

### What Works (Conversation Memory)

```
┌─────────────────────────────────────────────────────┐
│ Session Memory (Per Agent)                          │
│ - Message history                                   │
│ - Conversation context                              │
│ - Tool execution history                            │
│ Location: pkg/agent/memory.go:37                    │
│ Status: ✅ Works correctly                          │
└─────────────────────────────────────────────────────┘
```

### What Doesn't Work (Agent Identity)

```
┌─────────────────────────────────────────────────────┐
│ Shared Memory (Workflow-scoped)                     │
│ - namespace="workflow" → ALL agents see same data   │
│ - namespace="global"   → ALL agents see same data   │
│ - namespace="swarm"    → ALL agents in swarm share  │
│ Location: pkg/shuttle/builtin/shared_memory.go      │
│ Status: ❌ No agent-private isolation               │
└─────────────────────────────────────────────────────┘
```

### The Architectural Gap

```
┌─────────────────────────────────────────────────────┐
│ What Agents Need                                    │
├─────────────────────────────────────────────────────┤
│ 1. Session Memory (conversation history)           │
│    ✅ EXISTS - pkg/agent/memory.go                  │
│                                                     │
│ 2. Shared Memory (workflow collaboration)          │
│    ✅ EXISTS - pkg/communication/shared_memory.go   │
│                                                     │
│ 3. Agent-Private Memory (identity, goals, context) │
│    ❌ MISSING - agents can't store private state!   │
└─────────────────────────────────────────────────────┘
```

## Discovered Solution: SESSION Namespace Exists!

### Proto Definition (Already Exists!)

```protobuf
// proto/loom/v1/shared_memory.proto:19-20
enum SharedMemoryNamespace {
  SHARED_MEMORY_NAMESPACE_UNSPECIFIED = 0;
  SHARED_MEMORY_NAMESPACE_GLOBAL = 1;
  SHARED_MEMORY_NAMESPACE_WORKFLOW = 2;
  SHARED_MEMORY_NAMESPACE_SWARM = 3;
  SHARED_MEMORY_NAMESPACE_DEBATE = 4;
  SHARED_MEMORY_NAMESPACE_SESSION = 5;  // ← EXISTS BUT NOT EXPOSED!
}
```

### Storage Layer (Already Supports It!)

```go
// pkg/communication/shared_memory.go:107-116
for _, ns := range []loomv1.SharedMemoryNamespace{
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_GLOBAL,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SWARM,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_DEBATE,
    loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_SESSION,  // ← INITIALIZED!
} {
    store.stats[ns] = &SharedMemoryNamespaceStats{namespace: ns}
    store.data[ns] = make(map[string]*loomv1.SharedMemoryValue)
}
```

### Builtin Tools (NOT Exposed!)

```go
// pkg/shuttle/builtin/shared_memory.go:64-66
"namespace": shuttle.NewStringSchema("Namespace: 'global', 'workflow', or 'agent' (default: global)").
    WithEnum("global", "workflow", "swarm").  // ← SESSION missing!
    WithDefault("global"),
```

**Key Finding**: The SESSION namespace exists in proto and storage, but the builtin `shared_memory_read`/`shared_memory_write` tools don't expose it!

## Solution Options

### Option 1: Expose SESSION Namespace (Quickest Fix)

**Implementation**: Add "session" to builtin tool enum

```diff
// pkg/shuttle/builtin/shared_memory.go:64-66
"namespace": shuttle.NewStringSchema("Namespace: 'global', 'workflow', 'swarm', or 'session' (default: global)").
-   WithEnum("global", "workflow", "swarm").
+   WithEnum("global", "workflow", "swarm", "session").
    WithDefault("global"),
```

**Usage Pattern**: Agents manually prefix keys with agent ID

```yaml
# Agent YAML config includes agent name
apiVersion: loom/v1
kind: Agent
metadata:
  name: player_eldrin  # Agent knows its own ID
  version: "1.0.0"
spec:
  # ... rest of agent spec

# Agent uses SESSION namespace with scoped keys
shared_memory_write(
  key="agent:player_eldrin:character_sheet",  # Manual prefix
  namespace="session",
  value="Name: Eldrin, Race: Elf, Class: Wizard"
)

shared_memory_read(
  key="agent:player_eldrin:character_sheet",
  namespace="session"
)
```

**Pros**:
- ✅ Zero proto changes (SESSION already exists!)
- ✅ Storage layer already supports it (no changes)
- ✅ 1-line code change in builtin tools
- ✅ Backwards compatible (existing workflows unaffected)
- ✅ Can ship today

**Cons**:
- ⚠️ Agents must manually prefix keys with agent ID
- ⚠️ "session" name is misleading (means user session, not agent session)
- ⚠️ No automatic isolation - agents can read each other's keys if they know the pattern
- ⚠️ Error-prone (typos in agent ID prefix)

**Recommendation**: **Immediate workaround** for D&D adventure, but not ideal long-term.

---

### Option 2: Add AGENT Namespace (Proper Solution)

**Implementation**: Add new namespace to proto with automatic key scoping

#### Step 1: Proto Definition

```diff
// proto/loom/v1/shared_memory.proto:21 (ADD)
enum SharedMemoryNamespace {
  ...
  SHARED_MEMORY_NAMESPACE_SESSION = 5;
+ // Agent-private namespace (isolated per agent instance)
+ // Keys are automatically scoped to the agent ID that writes them.
+ SHARED_MEMORY_NAMESPACE_AGENT = 6;
}
```

#### Step 2: Storage Layer Auto-Scoping

```go
// pkg/communication/shared_memory.go:165-170 (MODIFY Put method)
func (s *SharedMemoryStore) Put(ctx context.Context, req *loomv1.PutSharedMemoryRequest) (*loomv1.PutSharedMemoryResponse, error) {
    // ... validation ...

    // AUTO-SCOPE: Automatically prefix keys with agent_id for AGENT namespace
    key := req.Key
    if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT {
        key = fmt.Sprintf("agent:%s:%s", req.AgentId, req.Key)
    }

    // Get namespace data
    nsData := s.data[req.Namespace]

    // ... rest of Put logic uses auto-scoped key ...
}
```

```go
// pkg/communication/shared_memory.go:310-320 (MODIFY Get method)
func (s *SharedMemoryStore) Get(ctx context.Context, req *loomv1.GetSharedMemoryRequest) (*loomv1.GetSharedMemoryResponse, error) {
    // ... validation ...

    // AUTO-SCOPE: Automatically prefix keys with agent_id for AGENT namespace
    key := req.Key
    if req.Namespace == loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_AGENT {
        key = fmt.Sprintf("agent:%s:%s", req.AgentId, req.Key)
    }

    // Read from shared memory
    nsData := s.data[req.Namespace]
    value, exists := nsData[key]

    // ... rest of Get logic ...
}
```

#### Step 3: Builtin Tools Exposure

```diff
// pkg/shuttle/builtin/shared_memory.go:64-66
"namespace": shuttle.NewStringSchema("Namespace: 'global', 'workflow', 'swarm', or 'agent' (default: global)").
-   WithEnum("global", "workflow", "swarm").
+   WithEnum("global", "workflow", "swarm", "agent").
    WithDefault("global"),
```

**Usage Pattern**: Clean and simple

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

**Pros**:
- ✅ Clear semantics ("agent" = private to this agent)
- ✅ Automatic key scoping (no manual prefixing)
- ✅ Built-in isolation guarantee (agents can't accidentally cross-read)
- ✅ Type-safe (impossible to read another agent's private data)
- ✅ Consistent with Loom's design philosophy (agent-centric)

**Cons**:
- ⚠️ Requires proto change (`buf generate`)
- ⚠️ Requires storage layer modification (~20 lines)
- ⚠️ Requires builtin tool update (1 line)
- ⚠️ Needs comprehensive testing (namespace isolation)

**Recommendation**: **Best long-term solution** - proper architectural fix.

---

### Option 3: Session Context Map (Not Recommended)

**Implementation**: Use `Session.Context` for agent-specific data

```go
// pkg/types/session.go (ALREADY EXISTS)
type Session struct {
    ID       string
    Messages []Message
    Context  map[string]interface{}  // Use this for agent-private data
}

// Agent reads/writes to session context
session.Context["agent_private:character_sheet"] = characterSheet
```

**Pros**:
- ✅ No proto changes
- ✅ No shared memory changes
- ✅ Data persists in SessionStore

**Cons**:
- ❌ Not accessible via `shared_memory_read`/`shared_memory_write` tools
- ❌ Agents can't programmatically query via LLM
- ❌ Requires custom tool: `get_my_context(key="character_sheet")`
- ❌ Doesn't leverage existing shared memory infrastructure
- ❌ Breaks tool abstraction (why have two memory systems?)

**Recommendation**: **Not recommended** - violates separation of concerns.

---

## Recommended Implementation Approach

### Phase 1: Immediate Fix (This Week)

**Goal**: Unblock D&D adventure development

**Action**: **Option 1** - Expose SESSION namespace

```bash
# 1. Update builtin tools (1 line change)
vim pkg/shuttle/builtin/shared_memory.go
# Add "session" to WithEnum on line 65

# 2. Test
go test ./pkg/shuttle/builtin -run TestSharedMemory -v

# 3. Update D&D agent configs to use SESSION namespace
# agents/player_*.yaml:
#   Use: namespace="session", key="agent:{agent_name}:character_sheet"
```

**Documentation**: Add convention to docs

```markdown
## Agent-Private Data Convention (Temporary)

Until AGENT namespace is implemented, use SESSION namespace with manual key prefixing:

shared_memory_write(
  key="agent:{your_agent_name}:{key}",
  namespace="session",
  value="..."
)
```

---

### Phase 2: Architectural Fix (Next Sprint)

**Goal**: Proper agent-private memory with automatic isolation

**Action**: **Option 2** - Add AGENT namespace

**Implementation Checklist**:

1. **Proto Changes**
   ```bash
   vim proto/loom/v1/shared_memory.proto
   # Add SHARED_MEMORY_NAMESPACE_AGENT = 6
   buf generate
   ```

2. **Storage Layer Auto-Scoping**
   ```bash
   vim pkg/communication/shared_memory.go
   # Modify Put() to auto-scope AGENT namespace keys
   # Modify Get() to auto-scope AGENT namespace keys
   # Modify Delete() to auto-scope AGENT namespace keys
   # Modify List() to auto-scope AGENT namespace keys
   ```

3. **Builtin Tools Update**
   ```bash
   vim pkg/shuttle/builtin/shared_memory.go
   # Add "agent" to WithEnum
   # Update tool descriptions
   ```

4. **Testing**
   ```bash
   vim pkg/communication/shared_memory_test.go
   # Add TestAgentNamespaceIsolation()
   # Verify agents can't read each other's data
   go test -race ./pkg/communication
   ```

5. **Documentation**
   ```bash
   vim website/content/en/docs/architecture/memory-systems.md
   # Add "Agent-Private Memory" section
   # Document namespace scoping behavior
   # Update examples
   ```

6. **Integration Testing**
   ```bash
   # Test with D&D adventure 5-agent scenario
   cd examples/03-advanced/dnd-adventure
   # Run workflow with AGENT namespace
   # Verify no identity confusion
   ```

**Estimated Effort**: 4-6 hours

---

## Decision Matrix

| Criteria | Option 1: SESSION | Option 2: AGENT | Option 3: Context |
|----------|-------------------|-----------------|-------------------|
| **Implementation Time** | 1 hour | 4-6 hours | 2-3 hours |
| **Proto Changes** | None | Yes | None |
| **Storage Changes** | None | Yes (~20 lines) | None |
| **Tool Changes** | 1 line | 1 line | New tool |
| **Automatic Isolation** | ❌ No | ✅ Yes | ❌ No |
| **Clear Semantics** | ⚠️ Misleading | ✅ Clear | ⚠️ Confusing |
| **Long-term Maintainability** | ⚠️ Workaround | ✅ Proper | ❌ Fragmentation |
| **Backwards Compatible** | ✅ Yes | ✅ Yes | ✅ Yes |
| **Recommendation** | Quick fix | Long-term solution | Avoid |

## Next Steps

1. **Get architectural approval** for Option 2 (AGENT namespace) as long-term solution
2. **Implement Option 1 (SESSION)** as immediate workaround:
   - Update `pkg/shuttle/builtin/shared_memory.go:65`
   - Test with `go test ./pkg/shuttle/builtin -v`
   - Update D&D agent configs to use `namespace="session"`
3. **Schedule Option 2** for next sprint:
   - Create GitHub issue with proto/storage/tool changes
   - Assign to sprint after D&D example is working

## Open Questions

1. **Session vs Agent Semantics**: Should SESSION namespace eventually be deprecated in favor of AGENT?
   - **Answer**: No - keep both. SESSION = user session (multi-agent), AGENT = per-agent private.

2. **Cross-Agent Reads**: Should agents ever be able to read another agent's private data?
   - **Answer**: No for AGENT namespace (strict isolation). Use WORKFLOW for shared data.

3. **Migration Path**: How do we migrate workflows using SESSION workaround to AGENT namespace?
   - **Answer**: Both namespaces coexist. No breaking changes. Document migration pattern.

4. **Key Scoping Implementation**: Should auto-scoping be in storage layer or tool layer?
   - **Answer**: Storage layer (single source of truth, consistent behavior across all APIs).

## References

- **Proto**: `proto/loom/v1/shared_memory.proto:9-21`
- **Storage**: `pkg/communication/shared_memory.go:107-250`
- **Builtin Tools**: `pkg/shuttle/builtin/shared_memory.go:34-398`
- **Agent Types**: `pkg/agent/types.go:26-77`
- **Session Store**: `pkg/agent/session_store.go:17-150`
- **Memory Documentation**: `website/content/en/docs/architecture/memory-systems.md`
