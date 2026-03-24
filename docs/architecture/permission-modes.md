---
title: "Permission Modes Architecture"
weight: 6
---

# Permission Modes Architecture

Runtime permission control system that enables dynamic switching between execution strategies (AUTO_ACCEPT, PLAN, ASK_BEFORE) without agent restart.

**Target Audience**: Architects, academics, advanced developers

---

## Design Goals

**Primary goals:**
- **Runtime flexibility**: Switch permission modes per-request, not per-agent
- **Plan-before-execute**: Create reviewable execution plans for critical operations
- **Minimal overhead**: <1ms mode switching latency
- **Backward compatible**: Existing agents work without changes

**Non-goals:**
- Complex multi-stage approval workflows (use external orchestration)
- Per-tool granular permissions (use tool allow/deny lists instead)
- Async approval (plans are synchronous approve/reject)

---

## System Context

```
┌─────────────────────────────────────────────────────────┐
│                  External Environment                    │
│                                                          │
│  [Client/Canvas AI]                                     │
│         │                                                │
│         │ WeaveRequest(permission_mode=PLAN)           │
│         │                                                │
│         ▼                                                │
│  ┌──────────────┐         ┌──────────────┐            │
│  │ Loom Server  │────────▶│    Agent     │            │
│  │  (gRPC/HTTP) │         │   Runtime    │            │
│  └──────┬───────┘         └──────┬───────┘            │
│         │                        │                     │
│         │ ApprovePlan           │ CreatePlan          │
│         │ ExecutePlan           │ ExecuteTools        │
│         │                        │                     │
│         ▼                        ▼                     │
│  ┌──────────────┐         ┌──────────────┐            │
│  │   Planner    │────────▶│  Executor    │            │
│  │ (Plan Mgmt)  │         │ (Tool Calls) │            │
│  └──────────────┘         └──────┬───────┘            │
│                                  │                     │
│                                  │                     │
│                                  ▼                     │
│                           ┌──────────────┐            │
│                           │ Permission   │            │
│                           │   Checker    │            │
│                           └──────────────┘            │
└─────────────────────────────────────────────────────────┘
```

**Description**: Client specifies permission mode in WeaveRequest. Server updates agent's permission checker. Agent execution loop branches based on mode: PLAN mode creates plans, AUTO_ACCEPT executes immediately, ASK_BEFORE requests approval.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────┐
│                    Permission Modes System                    │
│                                                               │
│  ┌─────────────────────────────────────────────────────┐    │
│  │              Agent Execution Loop                    │    │
│  │  ┌─────────────────────────────────────────────┐   │    │
│  │  │  1. LLM generates tool calls                 │   │    │
│  │  └──────────────┬──────────────────────────────┘   │    │
│  │                 │                                    │    │
│  │                 ▼                                    │    │
│  │  ┌─────────────────────────────────────────────┐   │    │
│  │  │  2. Check: permissionChecker.InPlanMode()?  │   │    │
│  │  └──────────────┬──────────────────────────────┘   │    │
│  │                 │                                    │    │
│  │        ┌────────┴────────┐                          │    │
│  │        │ Yes             │ No                       │    │
│  │        ▼                 ▼                          │    │
│  │  ┌──────────┐      ┌──────────┐                   │    │
│  │  │  PLAN    │      │ AUTO/ASK │                   │    │
│  │  │  Branch  │      │  Branch  │                   │    │
│  │  └────┬─────┘      └────┬─────┘                   │    │
│  └───────┼─────────────────┼──────────────────────────┘    │
│          │                 │                                │
│          │                 │                                │
│  ┌───────▼────────┐  ┌─────▼──────────┐                   │
│  │ ExecutionPlan- │  │    Executor    │                   │
│  │     ner        │  │                │                   │
│  │                │  │ CheckPermission│                   │
│  │ CreatePlan()   │  │ Execute()      │                   │
│  │ ApprovePlan()  │  └────────────────┘                   │
│  │ ExecutePlan()  │                                        │
│  └────────────────┘                                        │
│                                                             │
│  ┌─────────────────────────────────────────────────┐      │
│  │         PermissionChecker (State Machine)        │      │
│  │                                                   │      │
│  │   mode: PermissionMode (AUTO_ACCEPT|ASK|PLAN)   │      │
│  │                                                   │      │
│  │   SetMode() ─────▶ Updates mode field            │      │
│  │   InPlanMode() ───▶ Returns mode == PLAN         │      │
│  │   CheckPermission() ─▶ Mode-specific logic       │      │
│  └─────────────────────────────────────────────────┘      │
└──────────────────────────────────────────────────────────────┘
```

**Description**: Permission mode is stored in PermissionChecker. Agent execution loop checks `InPlanMode()` before tool execution. If true, tools are converted to ExecutionPlan and returned for approval. If false, tools execute via Executor which calls `CheckPermission()` for each tool.

---

## Components

### PermissionChecker

**Responsibility**: Determines whether a tool can execute based on current permission mode

**Interface**:
```go
type PermissionChecker struct {
    mode           loomv1.PermissionMode
    allowedTools   map[string]bool
    disabledTools  map[string]bool
    defaultAction  string
    timeoutSeconds int
}

func (pc *PermissionChecker) SetMode(mode loomv1.PermissionMode)
func (pc *PermissionChecker) InPlanMode() bool
func (pc *PermissionChecker) CheckPermission(ctx context.Context, toolName string, params map[string]interface{}) error
```

**Implementation**:
- Mode stored as enum value (0-3)
- `InPlanMode()` is O(1) comparison
- `CheckPermission()` branches based on mode
- Thread-safe (read-only after initialization except SetMode which is called once per request)

**Invariants**:
- Mode must be one of: UNSPECIFIED, ASK_BEFORE, AUTO_ACCEPT, PLAN
- Disabled tools blocked regardless of mode
- Allowed tools (whitelist) bypass mode checks

### ExecutionPlanner

**Responsibility**: Manages execution plans per session

**Interface**:
```go
type ExecutionPlanner struct {
    sessionID string
    plans     map[string]*loomv1.ExecutionPlan
    mu        sync.RWMutex
}

func (ep *ExecutionPlanner) CreatePlan(query string, toolCalls []ToolCall, reasoning string) (*ExecutionPlan, error)
func (ep *ExecutionPlanner) ApprovePlan(planID string, approved bool, feedback string) (*ExecutionPlan, error)
func (ep *ExecutionPlanner) ExecutePlan(planID string, executor func(tool PlannedToolExecution) error) (*ExecutionPlan, error)
```

**Implementation**:
- In-memory storage per session (plans do not persist across server restarts)
- RWMutex for concurrent read/write
- Plan lifecycle: PENDING → APPROVED/REJECTED → EXECUTING → COMPLETED/FAILED

**Invariants**:
- Plan IDs are unique UUIDs
- Plans cannot be executed until APPROVED
- Plans can only transition to EXECUTING from APPROVED state
- Tool executions are sequential (no parallelization within plan)

### Agent Execution Loop

**Responsibility**: Orchestrates tool execution with permission checks

**Critical Code Path** (pkg/agent/agent.go:1886-1975):

```go
// Check if in PLAN permission mode - if so, create plan instead of executing
if len(llmResp.ToolCalls) > 0 && a.permissionChecker != nil && a.permissionChecker.InPlanMode() {
    // Initialize planner for this session
    a.ensurePlannerForSession(session.ID)

    // Create execution plan from tool calls
    plan, err := a.planner.CreatePlan(userQuery, llmResp.ToolCalls, llmResp.Content)
    if err != nil {
        return nil, fmt.Errorf("failed to create execution plan: %w", err)
    }

    // Emit plan created progress event
    emitPlanCreated(ctx, plan)

    // Add deferred tool_result messages (prevents conversation state corruption)
    for _, toolCall := range llmResp.ToolCalls {
        deferredResult := Message{
            Role:    "tool",
            Content: fmt.Sprintf("Tool execution deferred to plan %s", plan.PlanId),
            ToolUseID: toolCall.ID,
            // ...
        }
        session.AddMessage(ctx, deferredResult)
    }

    // Return response with plan info (actual execution happens after approval)
    return &Response{
        Content: fmt.Sprintf("I've created an execution plan with %d steps. Please review and approve.", len(plan.Tools)),
        Metadata: map[string]interface{}{
            "plan_id":     plan.PlanId,
            "plan_status": plan.Status.String(),
            "stop_reason": "plan_created",
        },
    }, nil
}

// Execute tool calls (normal AUTO_ACCEPT or ASK_BEFORE mode)
if len(llmResp.ToolCalls) > 0 {
    for _, toolCall := range llmResp.ToolCalls {
        // Executor calls CheckPermission before executing
        result := a.executor.Execute(ctx, toolCall)
        // ...
    }
}
```

**Properties**:
- **Early return**: PLAN mode returns before tool execution loop
- **Deferred tool results**: Prevents LLM API errors (every tool_use must have tool_result)
- **Sequential execution**: Tools execute one at a time in both modes
- **Error propagation**: Plan creation errors bubble up, don't fall through to execution

---

## Key Interactions

### Interaction 1: Mode Switching (Runtime)

```
Client            Server           Agent            PermissionChecker
  │                 │                │                      │
  ├─ WeaveRequest ─▶│                │                      │
  │ (mode=PLAN)     │                │                      │
  │                 ├─ SetMode() ───▶│                      │
  │                 │                ├─ SetMode(PLAN) ─────▶│
  │                 │                │                      │
  │                 │                │◀──── mode = PLAN ────┤
  │                 │                │                      │
  │                 │◀─ Chat() ──────┤                      │
  │◀─ Response ─────┤                │                      │
  │  (with plan)    │                │                      │
```

**Description**: Client sends permission_mode in WeaveRequest. Server extracts mode and calls `agent.SetPermissionMode()`. Agent updates PermissionChecker.mode field. Subsequent tool executions check this mode.

**Properties**:
- Synchronous: Mode change takes effect immediately
- Per-request: Mode can change on every WeaveRequest
- Session-scoped: Mode persists in session for duration of request

### Interaction 2: Plan Creation and Approval

```
Agent        Planner        Client        Executor
  │             │              │              │
  ├─ InPlanMode() = true       │              │
  │             │              │              │
  ├─ CreatePlan()────▶         │              │
  │             │              │              │
  │◀──── Plan ────────┤        │              │
  │             │              │              │
  ├───── Return plan to client ──────────────▶│
  │             │              │              │
  │             │              │              │
  │             │         [User reviews plan] │
  │             │              │              │
  │             │◀─ ApprovePlan(approved=true)┤
  │             │              │              │
  │             ├─ Update status: APPROVED    │
  │             │              │              │
  │             │◀─ ExecutePlan() ─────────────┤
  │             │              │              │
  │             ├───────────── Execute tools ─┼────────▶
  │             │              │              │
  │             │              │        [Tools run]
  │             │              │              │
  │             │              │◀──── Results ┤
  │             │              │              │
  │◀──── Plan (COMPLETED) ─────┤              │
```

**Description**: When InPlanMode() returns true, agent creates plan instead of executing. Plan returned to client with PENDING status. Client approves plan. Planner transitions plan to APPROVED. Client calls ExecutePlan. Planner executes tools sequentially via executor. Results captured in plan. Plan status updated to COMPLETED or FAILED.

---

## Data Structures

### PermissionMode Enum

**Purpose**: Defines execution strategies

**Schema**:
```protobuf
enum PermissionMode {
  PERMISSION_MODE_UNSPECIFIED = 0;  // Use agent default
  PERMISSION_MODE_ASK_BEFORE = 1;   // Request approval per tool
  PERMISSION_MODE_AUTO_ACCEPT = 2;  // Execute immediately
  PERMISSION_MODE_PLAN = 3;         // Create plan first
}
```

**Invariants**:
- Values are immutable once defined (backward compatibility)
- UNSPECIFIED (0) triggers default behavior (AUTO_ACCEPT)
- Enum values are sequential for efficient switching

### ExecutionPlan

**Purpose**: Represents a plan of tool executions awaiting approval

**Schema**:
```protobuf
message ExecutionPlan {
  string plan_id = 1;
  string session_id = 2;
  string query = 3;
  repeated PlannedToolExecution tools = 4;
  string reasoning = 5;
  PlanStatus status = 6;
  int64 created_at = 7;
  int64 updated_at = 8;
}
```

**Invariants**:
- `plan_id` is globally unique (UUID v4)
- `tools` array preserves execution order
- `status` can only transition forward (no rollbacks)
- Timestamps are Unix seconds (int64)

**Operations**:
- **Create**: Initialize with PENDING status
- **Approve**: Transition to APPROVED or REJECTED
- **Execute**: Transition APPROVED → EXECUTING → COMPLETED/FAILED
- **Read**: Retrieve by ID or list by session

---

## Algorithms

### Permission Check Algorithm

**Problem**: Determine if a tool can execute based on current mode

**Approach**: O(1) mode lookup with early exits

```
CheckPermission(toolName, params):
  1. If toolName in disabledTools → return error (applies to all modes)
  2. If toolName in allowedTools → return success (whitelist bypass)
  3. Switch on mode:
     - AUTO_ACCEPT → return success
     - ASK_BEFORE → return "approval required" error (callback not implemented)
     - PLAN → return ErrToolExecutionDeferred (special error)
  4. Default → return error
```

**Complexity**: O(1) time, O(n) space where n = number of disabled/allowed tools

**Trade-offs**:
- **Chosen**: Hash map lookups for disabled/allowed lists
  - ✅ O(1) lookup time
  - ❌ O(n) memory for tool lists
- **Alternative**: Linear scan through lists
  - ✅ O(1) memory (just slices)
  - ❌ O(n) lookup time

### Plan Execution Algorithm

**Problem**: Execute approved plan's tools sequentially with error handling

**Approach**: Sequential execution with early termination on failure

```
ExecutePlan(planID, executor):
  1. Verify plan.status == APPROVED (else error)
  2. Transition to EXECUTING
  3. For each tool in plan.tools:
     a. Update tool.status = EXECUTING
     b. Call executor(tool)
     c. If success:
        - Capture result in tool.result
        - Update tool.status = COMPLETED
     d. If error:
        - Capture error in tool.error
        - Update tool.status = FAILED
        - STOP (do not execute remaining tools)
  4. If all succeeded → plan.status = COMPLETED
  5. If any failed → plan.status = FAILED
  6. Return updated plan
```

**Complexity**: O(n) where n = number of tools

**Scaling**: Sequential (no parallelization). For 10 tools averaging 2s each, total time = 20s.

---

## Design Trade-offs

### Decision 1: In-Memory Plan Storage

**Chosen Approach**: Plans stored in-memory (map[string]*ExecutionPlan)

**Rationale**:
- Plans are ephemeral (approve → execute → discard)
- No need to persist across server restarts
- Fast lookups (O(1) with map)
- Avoids database round-trips

**Alternatives Considered**:
1. **Database persistence** (SQLite/Postgres)
   - ✅ Survives server restarts
   - ✅ Multi-server deployments
   - ❌ Adds latency (50-100ms per plan operation)
   - ❌ Schema migrations required
   - Rejected: Plans are short-lived, not worth persistence cost

2. **Redis caching**
   - ✅ Fast lookups
   - ✅ Shared across servers
   - ❌ External dependency
   - ❌ Serialization overhead
   - Rejected: Adds complexity for minimal benefit

**Consequences**:
- Plans lost on server restart (acceptable - user recreates plan)
- Single-server only (no plan sharing across replicas)
- Fast plan operations (<1ms lookup)

### Decision 2: Default Permission Checker Initialization

**Chosen Approach**: Every agent gets default PermissionChecker with AUTO_ACCEPT mode

**Rationale**:
- Prevents nil pointer dereference at line 1886
- Backward compatible (AUTO_ACCEPT was implicit default before)
- Enables runtime mode switching for all agents

**Implementation** (pkg/agent/agent.go:136-146):
```go
// If no permission checker provided, create default one
if a.permissionChecker == nil {
    a.permissionChecker = shuttle.NewPermissionChecker(shuttle.PermissionConfig{
        Mode:           loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT, // Safe default
        DefaultAction:  "deny",
        TimeoutSeconds: 300,
    })
}

// Set permission checker on executor
a.executor.SetPermissionChecker(a.permissionChecker)
```

**Alternatives Considered**:
1. **Require explicit permission checker in NewAgent()**
   - ✅ Forces intentional configuration
   - ❌ Breaking change (all existing code must be updated)
   - ❌ Verbose (boilerplate for every agent creation)
   - Rejected: Breaks backward compatibility

2. **Check for nil on every use**
   - ✅ No initialization overhead
   - ❌ Scattered nil checks throughout code
   - ❌ Easy to forget checks (bugs)
   - Rejected: Error-prone, defensive programming overhead

3. **Panic if nil** (fail-fast)
   - ✅ Forces explicit configuration
   - ❌ Runtime crashes for legacy code
   - ❌ Poor developer experience
   - Rejected: Too aggressive

**Consequences**:
- All agents can use permission modes without changes
- Default is AUTO_ACCEPT (safe, matches previous implicit behavior)
- 6 lines of code enable entire feature

### Decision 3: Sequential Tool Execution in Plans

**Chosen Approach**: Tools in plans execute sequentially (one after another)

**Rationale**:
- Deterministic execution order
- Simpler error handling (stop on first failure)
- Dependencies between steps (step N depends on step N-1 result)
- Matches LLM reasoning (agent generates sequential steps)

**Alternatives Considered**:
1. **Parallel execution with dependency graph**
   - ✅ Faster for independent tools
   - ❌ Complex dependency resolution
   - ❌ Race conditions in shared state
   - ❌ Doesn't match LLM step generation (LLMs generate sequential reasoning)
   - Rejected: Premature optimization, adds significant complexity

2. **Async with promises/futures**
   - ✅ Non-blocking
   - ❌ More complex error handling
   - ❌ Out-of-order results confusing for users
   - Rejected: Overkill for typical plan sizes (1-5 tools)

**Consequences**:
- Plan execution time = sum of individual tool times
- For 5 tools @ 2s each = 10s total
- Acceptable for typical use cases (user approving plan implies willingness to wait)
- Can add parallelization later if needed (not a one-way door)

---

## Constraints and Limitations

### Constraint 1: Single Session per Plan

**Description**: Plans are scoped to a single session

**Rationale**: Plans contain session-specific context (conversation history, user query)

**Impact**: Cannot share plans across sessions or users

**Workarounds**: Create new plan for each session with same query

### Constraint 2: No Plan Persistence

**Description**: Plans do not survive server restarts

**Rationale**: In-memory storage for performance (see Trade-off #1)

**Impact**: Pending plans lost on server restart

**Workarounds**:
- Recreate plan after restart
- Or implement database persistence if needed (future enhancement)

### Constraint 3: ASK_BEFORE Mode Partially Implemented

**Description**: ASK_BEFORE mode returns error, callback mechanism not implemented

**Rationale**: PLAN mode provides similar approval workflow, ASK_BEFORE is lower priority

**Impact**: Cannot request approval for individual tools mid-execution

**Workarounds**: Use PLAN mode for approval workflow

---

## Performance Characteristics

### Latency

**Mode switching**: <1ms
- Direct field assignment
- No I/O or network calls

**Plan creation**: 10-50ms
- Proportional to number of tools
- Mostly object allocation and UUID generation

**Plan approval**: <1ms
- Status update only

**Plan execution**: Variable
- Depends on tool execution times
- Typical: 2-20s for 1-10 tools

### Throughput

**Plans per session**: Unlimited (limited by memory)
- Typical: 1-3 plans per conversation
- Each plan: ~1-5KB memory

**Concurrent mode switches**: Unlimited
- No shared state between requests
- Mode stored per-agent, per-request

### Resource Usage

**Memory per plan**: ~1-5KB
- Depends on tool count and parameter sizes
- Typical plan with 3 tools: ~2KB

**Memory per agent**: +16 bytes for permission checker pointer
- Negligible overhead

---

## Concurrency Model

### Threading

Permission checker is read-mostly:
- Initialized once in NewAgent()
- SetMode() called once per request
- CheckPermission() called many times (read-only after mode set)

No locks needed:
- SetMode() happens before any CheckPermission() calls
- Go happens-before guarantees prevent races

### Synchronization

ExecutionPlanner uses RWMutex:
- Read lock for GetPlan/ListPlans
- Write lock for CreatePlan/ApprovePlan/ExecutePlan
- Prevents concurrent modifications to plan state

### Race Conditions

**Prevented**:
- Mode switching race: SetMode() called before execution loop starts
- Plan approval race: RWMutex prevents concurrent approve/execute

**Not applicable**:
- Tool execution concurrency: Tools execute sequentially within plan

---

## Error Handling Philosophy

### Strategy

Fail-fast for configuration errors:
- Invalid permission mode → error immediately
- Missing plan ID → NotFound error

Graceful degradation for runtime errors:
- Tool execution failure → mark step as FAILED, don't fail entire plan
- Plan creation error → return error, don't crash agent

### Error Propagation

```
Tool Execution Error
  ↓
Executor returns error result
  ↓
Planner captures in tool.error field
  ↓
Plan status → FAILED (but plan object preserved)
  ↓
User can inspect failure details in plan
```

### Recovery

Plans are immutable after failure:
- Cannot retry failed plan
- User must create new plan to retry

Rationale:
- Preserves audit trail (what failed and why)
- Prevents unintended side effects from partial execution

---

## Security Considerations

### Threat Model

**Threat**: Malicious client bypasses permission checks by manipulating mode

**Mitigation**: Server enforces mode, client cannot directly call Executor

**Threat**: Privilege escalation via allowed_tools list

**Mitigation**: Allowed tools configured server-side, not client-provided

**Threat**: Plan tampering (modify plan after approval)

**Mitigation**: Plans are immutable after approval (status transition is one-way)

### Trust Boundaries

```
Untrusted → Trusted
Client → Server → Agent → PermissionChecker → Executor
```

Validation happens at server boundary:
- permission_mode validated against enum values
- plan_id validated (UUID format, exists)
- tool names validated (in registry)

---

## Evolution and Extensibility

### Extension Points

1. **Add new permission modes**:
   - Add enum value to proto
   - Add case to CheckPermission() switch
   - Implement mode-specific logic

2. **Implement ASK_BEFORE callback**:
   - Add callback interface to PermissionConfig
   - Call callback in CheckPermission() for ASK_BEFORE mode
   - Return callback result (approve/deny)

3. **Add plan persistence**:
   - Replace in-memory map with database store
   - Implement PlanStore interface
   - Inject store into ExecutionPlanner

### Stability

**Stable** (won't change):
- Permission mode enum values (backward compatibility)
- ExecutionPlan message structure (versioned proto)
- RPC signatures (buf breaking protects these)

**May change**:
- Plan storage (in-memory → database)
- Execution algorithm (sequential → parallel)
- Permission checking logic (add more modes)

### Migration

**Version 1.1.0 → 1.2.0** (example):
- Add PERMISSION_MODE_SCHEDULE (defer to cron)
- New field: `ExecutionPlan.schedule_time`
- Backward compatible: Existing plans unaffected

---

## Related Work

### Pattern: Command Pattern (Gang of Four, 1994)

**Reference**: Design Patterns: Elements of Reusable Object-Oriented Software

**Relationship**: ExecutionPlan is command pattern application
- Plan = Command object
- Tools = Receiver operations
- ApprovePlan = validation before execution
- ExecutePlan = command invocation

### Pattern: Strategy Pattern (Gang of Four, 1994)

**Reference**: Design Patterns: Elements of Reusable Object-Oriented Software

**Relationship**: PermissionMode is strategy pattern
- Mode = Strategy
- AUTO_ACCEPT/PLAN/ASK_BEFORE = Concrete strategies
- CheckPermission = Context
- SetMode = Runtime strategy switching

### System: Temporal Workflow Engine

**Reference**: https://temporal.io/

**Relationship**: Similar plan-approve-execute pattern
- Temporal workflows = our plans
- Temporal activities = our tools
- Temporal signals = our approval mechanism
- Difference: We're synchronous, Temporal is async/distributed

---

## Further Reading

- [Permission Modes User Guide](/docs/guides/permission-modes.md) - How to use permission modes
- [Agent Architecture](/docs/architecture/agent-system.md) - Broader agent system design
- [Tool System Architecture](/docs/architecture/tool-system.md) - How tools are executed
- [Proto API Reference](/docs/reference/proto-api.md) - Complete proto definitions

---

**Created:** 2026-03-22
**Consolidates:** docs/plans/permission-modes.md, docs/plans/permission-modes-progress.md
**Status:** ✅ Implemented and tested
