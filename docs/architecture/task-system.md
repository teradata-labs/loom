---
title: "Task System Architecture"
weight: 14
---

# Task System Architecture

The task system provides persistent, dependency-aware work decomposition and kanban-style tracking for agents. Tasks are domain-agnostic units of cognitive work (research, analysis, writing, decisions, implementation, review) that survive context window compaction and span multiple agent turns or sessions.

**Target Audience**: Architects, academics, advanced developers

---

## Design Goals

- **Persistence beyond context window**: Tasks are stored in the database and rebuilt into agent context each turn, so the agent never forgets its work even after aggressive memory compaction.
- **Dependency-aware scheduling**: A directed acyclic graph (DAG) of BLOCKS edges determines task ordering. The ready-front algorithm surfaces only tasks whose blockers are all satisfied.
- **LLM-driven decomposition**: Complex goals are broken into task DAGs via an LLM call, using backward (prerequisites-first), forward (sequential), or parallel decomposition strategies.
- **Skill integration**: The skills overhaul (Phase D) emits tasks automatically when skills activate, with idempotency guarantees preventing duplicate boards on re-activation.
- **Two-axis independence**: Task emission (creating tasks on the board) and task surfacing (showing the `task_board` tool to the agent) are controlled by separate flags, enabling background task tracking without UI clutter.

**Non-goals**:
- The task system is not a general-purpose project management tool. It is scoped to cognitive work within agent conversations.
- It does not provide real-time collaborative editing or conflict resolution between human users and agents editing the same task simultaneously.

---

## System Context

```
┌────────────────────────────────────────────────────────────────────┐
│                       External Environment                         │
│                                                                    │
│  [User/Client]                                                     │
│       │                                                            │
│       │ gRPC / HTTP                                                │
│       ▼                                                            │
│  ┌──────────────────┐                                              │
│  │   TaskService    │──── gRPC server exposing CRUD + workflow RPCs │
│  │   (gRPC + GW)   │                                              │
│  └────────┬─────────┘                                              │
│           │                                                        │
│           ▼                                                        │
│  ┌──────────────────┐      ┌──────────────────┐                   │
│  │    Manager       │─────▶│    TaskStore     │                   │
│  │  (business logic)│      │  (sqlite/pg)     │                   │
│  └────────┬─────────┘      └──────────────────┘                   │
│           │                                                        │
│           ├──── publishes ────▶ [MessageBus]                       │
│           │                                                        │
│           ├──── remembers ────▶ [GraphMemoryStore]                 │
│           │                                                        │
│           └──── traced by ────▶ [Tracer/Hawk]                      │
│                                                                    │
│  [Agent Runtime]                                                   │
│       │                                                            │
│       ├──── task_board tool ───▶ Manager                           │
│       │                                                            │
│       └──── skill task emitter ───▶ Manager                        │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
```

**Description**: The task system is consumed by three paths: (1) external clients via gRPC TaskService, (2) agents via the `task_board` tool, and (3) the skill orchestrator via the task emitter during Phase D activation.

---

## Architecture Overview

```
┌───────────────────────────────────────────────────────────────────────────┐
│                           Task Subsystem                                  │
│                                                                           │
│  ┌───────────────────────┐       ┌───────────────────────┐               │
│  │   task.Manager        │       │   task.Decomposer     │               │
│  │                       │       │                       │               │
│  │  - CreateTask[Idemp.] │       │  - Decompose(goal)    │               │
│  │  - ClaimTask          │◀──────│  - buildPrompt()      │               │
│  │  - CloseTask          │       │  - callWithRetry()    │               │
│  │  - TransitionTask     │       │  - materialize()      │               │
│  │  - AddDependency      │       │  - detectLocalCycles  │               │
│  │  - CompactClosedTasks │       └───────────┬───────────┘               │
│  │  - detectCycle (DFS)  │                   │                           │
│  │  - tryAutoComplete    │                   │ uses LLMProvider           │
│  │  - tryUnblockDep.     │                   ▼                           │
│  │  - checkWIPLimit      │       ┌───────────────────────┐               │
│  └───────────┬───────────┘       │   types.LLMProvider   │               │
│              │                   └───────────────────────┘               │
│              │                                                            │
│              ▼                                                            │
│  ┌───────────────────────┐                                               │
│  │   task.TaskStore      │◀─── interface                                 │
│  │   (interface)         │                                               │
│  └───────────┬───────────┘                                               │
│              │                                                            │
│     ┌────────┴────────┐                                                  │
│     ▼                 ▼                                                  │
│  ┌────────────┐  ┌────────────┐                                          │
│  │  SQLite    │  │ PostgreSQL │                                          │
│  │  Store     │  │  Store     │                                          │
│  └────────────┘  └────────────┘                                          │
│                                                                           │
│  ┌───────────────────────────────────────────────────────────────────┐   │
│  │                    Agent Integration Layer                         │   │
│  │                                                                   │   │
│  │  ┌────────────────┐    ┌─────────────────┐    ┌──────────────┐   │   │
│  │  │ TaskBoardTool  │    │ skills/tasks/   │    │ buildTask    │   │   │
│  │  │ (shuttle.Tool) │    │ Emitter         │    │ Context()    │   │   │
│  │  └────────┬───────┘    └────────┬────────┘    └──────┬───────┘   │   │
│  │           │                     │                    │            │   │
│  │           └─────────────────────┴────────────────────┘            │   │
│  │                            │                                      │   │
│  │                            ▼                                      │   │
│  │                    task.Manager                                    │   │
│  └───────────────────────────────────────────────────────────────────┘   │
└───────────────────────────────────────────────────────────────────────────┘
```

**Description**: The Manager is the central coordination point. It wraps TaskStore with business logic (cycle detection, auto-status propagation, WIP limits, compaction, event publishing). The Decomposer uses an LLM to break goals into task DAGs. The agent layer integrates via three surfaces: the `task_board` tool (direct agent interaction), the skill task Emitter (Phase D automatic emission), and `buildTaskContext()` (system prompt injection each turn).

---

## Components

### Manager (`pkg/task/manager.go`)

**Responsibility**: Business logic layer over TaskStore. Handles task lifecycle, dependency graph operations, event publishing, auto-status propagation, WIP limits, compaction, and graph memory integration.

**Interface**: All operations accept `context.Context` for tracing and cancellation. Creates, claims, releases, closes, and transitions tasks. Manages dependencies with cycle detection. Publishes lifecycle events to the MessageBus.

**Key Design Decisions**:
- Cycle detection uses DFS from `toTaskID` following outgoing BLOCKS edges, checking if `fromTaskID` is reachable. O(V+E) worst case.
- Auto-completion cascades: when a task closes, if all siblings under the same parent are terminal, the parent auto-closes recursively.
- Auto-unblock: when a blocker completes, all its dependents are checked. If their remaining BLOCKS dependencies are all satisfied, they transition from BLOCKED to OPEN.
- Graph memory integration: on task close, a memory entity is created summarizing the completed work (salience 0.6, type "experience"). Best-effort.
- Event publishing is best-effort via `communication.MessageBus`. Failures are logged, not returned.

**Invariants**:
- The dependency graph is always a DAG (no cycles in BLOCKS edges).
- A task can only be claimed if it is in OPEN status.
- WIP limits are enforced at claim time per-lane on the board.
- Terminal states are DONE, DEFERRED, CANCELLED. A task in terminal state cannot be claimed.

### TaskStore (`pkg/task/store.go`)

**Responsibility**: Storage interface defining the persistence contract.

**Implementations**: `pkg/storage/sqlite/task_store.go`, `pkg/storage/postgres/task_store.go`

**Key Methods**:
- `CreateTask`, `GetTask`, `UpdateTask`, `DeleteTask`, `ListTasks`
- `ClaimTask` (atomic: sets status=IN_PROGRESS, assignee, session, claimed_at)
- `ReleaseTask` (atomic: clears assignee/session, sets status=OPEN)
- `CloseTask` (atomic: sets status=DONE, close_reason, closed_at)
- `TransitionTask` (validates allowed transitions)
- `AddDependency`, `RemoveDependency`, `GetDependencies`, `GetDependents`
- `GetReadyFront`, `GetBlockedTasks`
- `GetTaskByIdempotencyKey` (partial unique index on non-empty `skill_idempotency_key`)
- `HasOpenSkillTasks` (prefix match on idempotency key for stickiness checking)

**Schema Invariants**:
- `tasks.board_id` foreign-keys `task_boards(id)`. Empty board_id is NULL (board-less tasks).
- `skill_idempotency_key` has a partial unique index: `UNIQUE WHERE skill_idempotency_key != ''`.
- Soft-delete: `DeleteTask` does not remove rows (used by history/audit trail).

### Decomposer (`pkg/task/decomposer.go`)

**Responsibility**: Uses an LLM to break a high-level goal into a dependency DAG of subtasks.

**Algorithm**:
1. Build a strategy-specific prompt (backward/forward/parallel).
2. Call LLM with retry (up to 2 retries with CONTINUE-mode feedback on JSON parse failures).
3. Parse structured JSON output, validate dependency indices, detect local cycles via Kahn's algorithm (topological sort).
4. Materialize: create tasks in store, wire BLOCKS dependencies via Manager.
5. On failure during materialization: best-effort rollback (delete already-created tasks).

**Strategies**:
- `BACKWARD` (default, "beads" model): Start from goal, recursively identify prerequisites. Produces a deep DAG where leaves are immediately actionable.
- `FORWARD`: Sequential planning from current state. Produces a mostly-linear pipeline.
- `PARALLEL`: Maximize concurrency. Produces a wide, shallow DAG.

**Cycle Detection**: Pre-materialization cycle check uses Kahn's algorithm (O(V+E)) on the parsed index-based graph. This prevents creating orphaned tasks from a partial materialization of a cyclic output.

### TaskBoardTool (`pkg/agent/task_board_tool.go`)

**Responsibility**: Agent-facing shuttle.Tool providing 10 actions for task management within conversations.

**Actions**: `decompose`, `ready`, `claim`, `update`, `close`, `create`, `list`, `show`, `add_dep`, `board`

**Design Decision — `resolveBoardForWrite`**: Write operations (create, decompose) must ensure the target board exists before creating tasks, because `tasks.board_id` has a foreign key constraint. The resolution chain is: (1) check input board_id, (2) fall back to config default, (3) auto-create if missing. This prevents FK-failure errors that would surface as opaque error messages to the LLM.

### Skill Task Emitter (`pkg/skills/tasks/emitter.go`)

**Responsibility**: Converts skill activations into task records on the agent's kanban board. Called during Phase D of the skills overhaul pipeline.

**Decision Tree**:
1. If `AgentTasksEnabled=false` OR `Skill.EffectiveEmitTasks()=false` → no-op.
2. If skill has `TaskTemplate` with steps → materialize each step directly.
3. Otherwise → call Decomposer with skill prompt as goal (forward strategy, max depth 2).

**Idempotency**: All emitted tasks carry a `SkillIdempotencyKey` of the form `skill:<name>|sess:<sessionID>|step:<index>`. On re-activation, existing tasks are returned instead of duplicates being created. The decomposer fallback additionally plants a "marker" task that gates re-decomposition (since LLM output is non-deterministic across runs).

**ensureBoard**: Before emitting tasks, the emitter guarantees the referenced board exists. A concurrent-safe probe-then-create pattern handles races between multiple goroutines.

---

## Key Interactions

### Task Lifecycle Flow

```
            ┌─────────────────────────────────────────────────────┐
            │              Task State Machine                       │
            │                                                       │
            │  ┌───────┐    ClaimTask    ┌─────────────┐           │
            │  │ OPEN  │────────────────▶│ IN_PROGRESS │           │
            │  └───┬───┘                 └──────┬──────┘           │
            │      │                            │                  │
            │      │ AddDependency              │ CloseTask        │
            │      │ (blocker not done)         │                  │
            │      ▼                            ▼                  │
            │  ┌─────────┐               ┌──────────┐             │
            │  │ BLOCKED │               │   DONE   │             │
            │  └────┬────┘               └──────────┘             │
            │       │                                              │
            │       │ tryUnblock                                   │
            │       │ (all blockers done)                          │
            │       ▼                                              │
            │  ┌───────┐                 ┌──────────────┐          │
            │  │ OPEN  │                 │  DEFERRED    │          │
            │  └───────┘                 └──────────────┘          │
            │                            ┌──────────────┐          │
            │                            │  CANCELLED   │          │
            │                            └──────────────┘          │
            └─────────────────────────────────────────────────────┘
```

**Transitions**:
- OPEN → IN_PROGRESS: via `ClaimTask` (atomic, checks WIP limit)
- IN_PROGRESS → OPEN: via `ReleaseTask` (releases claim)
- OPEN/IN_PROGRESS → BLOCKED: automatic when a BLOCKS dependency is added whose blocker is not terminal
- BLOCKED → OPEN: automatic when all BLOCKS dependencies become terminal
- IN_PROGRESS → DONE: via `CloseTask`
- Any → DEFERRED/CANCELLED: via `TransitionTask`

**Properties**:
- Claim is atomic: status, assignee, session, and timestamp are set in a single store operation.
- Auto-unblock is cascading: closing one task checks all its dependents.
- Auto-complete is recursive: closing the last child completes the parent, which may cascade up.

### Skill Phase D Emission

```
Agent.runConversationLoop()        SkillOrchestrator        Emitter           Manager
       │                                │                      │                 │
       ├─── MatchSkills ───────────────▶│                      │                 │
       │◀── candidates ─────────────────┤                      │                 │
       │                                │                      │                 │
       ├─── ActivateSkill ─────────────▶│                      │                 │
       │                                │                      │                 │
       ├─── EmitForActivation ─────────────────────────────────▶│                 │
       │                                │                      │                 │
       │                                │          ensureBoard ─┼────────────────▶│
       │                                │                      │                 │
       │                                │        (template path)│                 │
       │                                │   CreateTaskIdempotent┼────────────────▶│
       │                                │          AddDependency┼────────────────▶│
       │                                │                      │                 │
       │◀──────────────────── EmitResult ──────────────────────┤                 │
       │                                │                      │                 │
```

**Properties**:
- Phase D fires for NEWLY-activated skills only (not skills already in the active set).
- Emission is unconditional when `taskManager != nil` (regardless of `taskBoardConfig.Enabled`).
- Idempotent: re-activation of the same (skill, session) returns existing tasks.
- Default cap: 8 tasks per activation (`DefaultMaxTasks`).

### Context Injection Each Turn

```
Agent.buildTaskContext()          Manager.ListTasks()          Store
       │                                │                      │
       ├─── ListTasks(IN_PROGRESS) ────▶│─────────────────────▶│
       │◀── claimed tasks ──────────────┤◀─────────────────────┤
       │                                │                      │
       ├─── GetReadyFront ─────────────▶│─────────────────────▶│
       │◀── ready tasks ────────────────┤◀─────────────────────┤
       │                                │                      │
       ├─── ListTasks(DONE, limit 3) ──▶│─────────────────────▶│
       │◀── recent completions ─────────┤◀─────────────────────┤
       │                                │                      │
       │── format "--- TASK CONTEXT ---" block                 │
       │── inject into system prompt                           │
       │                                                       │
```

**Properties**:
- Rebuilt from DB each turn (survives context compaction).
- Rough token budget enforcement: `context_budget_tokens * 4` characters.
- Shows current tasks with objective/approach/notes, ready front with priority/effort, recent completions, and board stats.

---

## Data Structures

### Task

**Purpose**: Domain-agnostic unit of cognitive work.

**Key Fields**:
- `ID`: SHA256 content-hash (merge-safe across agents/sessions)
- `Title`, `Description`, `Objective`, `Approach`, `AcceptanceCriteria`: Work definition
- `Notes`: Running progress log appended at milestones; survives compaction via DB rebuild
- `Status`: enum {OPEN, IN_PROGRESS, BLOCKED, DONE, DEFERRED, CANCELLED}
- `Priority`: enum {CRITICAL(P0), HIGH(P1), MEDIUM(P2), LOW(P3), BACKLOG(P4)}
- `Category`: enum {RESEARCH, ANALYSIS, IMPLEMENTATION, REVIEW, WRITING, DECISION, INVESTIGATION, PLANNING, OTHER}
- `BoardID`: FK to TaskBoard (nullable for board-less tasks)
- `ParentID`: Hierarchical decomposition (epic → task → subtask)
- `CompactionLevel`: 0=full, 1=summarized, 2=archived
- `SkillIdempotencyKey`: Dedup key for skill-emitted tasks (partial unique index)
- `OutputPolicy`: Validation rules checked before CloseTask allows DONE transition

**Invariants**:
- A task with non-empty `BoardID` must reference an existing board row.
- SkillIdempotencyKey uniqueness is enforced only for non-empty values.
- CompactionLevel monotonically increases (0→1→2, never decreases).

### TaskDependency

**Purpose**: Directed edge in the task dependency graph.

**Fields**:
- `FromTaskID`: The dependent task (this task depends on...)
- `ToTaskID`: The blocker (...this task)
- `Type`: {BLOCKS, PARENT_CHILD, DISCOVERED_FROM, RELATED}

**Invariants**:
- Only BLOCKS edges affect scheduling (ready-front computation, auto-blocking).
- PARENT_CHILD, DISCOVERED_FROM, RELATED are informational.
- No cycles in the BLOCKS subgraph (enforced at AddDependency time).

### TaskBoard

**Purpose**: Kanban board grouping tasks into status-mapped lanes.

**Fields**:
- `ID`, `Name`, `WorkflowID` (optional workflow binding)
- `Lanes`: array of {Name, Status, TaskIDs, WIPLimit}

### TaskContext

**Purpose**: Agent's current awareness of work state, injected into system prompt.

**Fields**:
- `CurrentTasks`: Tasks claimed by this agent (IN_PROGRESS)
- `ReadyTasks`: Tasks with all deps satisfied (ready to claim)
- `BlockedByMe`: Tasks I'm blocking
- `BlockingMe`: Tasks blocking my current work
- `Stats`: Board-level counts by status
- `BoardSummary`: Compact text for LLM context injection

---

## Algorithms

### Cycle Detection (DFS)

**Problem**: Adding a dependency edge must not create a cycle in the BLOCKS subgraph.

**Approach**: Starting from `toTaskID`, follow all outgoing BLOCKS dependencies (edges where `toTaskID` is the `from_task_id`). If DFS reaches `fromTaskID`, a cycle would be created.

**Complexity**: O(V+E) where V = reachable tasks, E = BLOCKS edges from those tasks.

**Edge Case**: Self-dependency is caught before DFS (fromTaskID == toTaskID).

### Pre-Materialization Cycle Detection (Kahn's Algorithm)

**Problem**: The Decomposer's LLM output may contain cycles. Creating tasks then failing on AddDependency leaves orphaned rows.

**Approach**: Topological sort on the parsed index-based graph before any store writes. If `processed != n`, the output contains a cycle and is rejected entirely.

**Complexity**: O(V+E) where V = task count in decomposition, E = dependency edges.

### Ready-Front Computation

**Problem**: Find tasks with no unsatisfied BLOCKS dependencies.

**Approach**: Query tasks in OPEN status whose ID does not appear as `from_task_id` in any BLOCKS dependency where `to_task_id` references a non-terminal task. Implemented as a SQL query in the store layer.

**Complexity**: O(1) per query (SQL index on status + join on dependencies).

### Auto-Unblock Cascade

**Problem**: When a blocker completes, dependents may become ready.

**Approach**: `tryUnblockDependents` queries all BLOCKS edges where the completed task is `to_task_id`. For each dependent, checks if ALL remaining BLOCKS dependencies are terminal. If yes, transitions to OPEN.

**Complexity**: O(D * B) where D = dependents of the blocker, B = average BLOCKS edges per dependent.

---

## Design Trade-offs

### Decision 1: Separate Manager and Store layers

**Chosen Approach**: Manager wraps Store with business logic; Store is a pure CRUD interface.

**Rationale**: Enables multiple store implementations (SQLite for development, PostgreSQL for production) without duplicating cycle detection, auto-completion cascading, event publishing, or WIP enforcement logic.

**Alternatives Considered**:
- Single implementation with embedded business logic: Rejected because it would require duplicating complex logic across SQLite and PostgreSQL implementations.
- Repository pattern with domain events: Similar to chosen approach but more complex; the current split is sufficient for Loom's needs.

**Consequences**: Slightly more indirection (Manager delegates to Store), but business logic is tested once against any store implementation.

### Decision 2: Idempotency via SkillIdempotencyKey

**Chosen Approach**: A partial unique index on a freeform string key composed as `skill:<name>|sess:<sessionID>|step:<index>`.

**Rationale**: Skill activations may fire multiple times per session (re-triggers, conversation loop retries). Without idempotency, duplicate task boards accumulate. The key format is human-readable for debugging.

**Alternatives Considered**:
- Content-hash dedup: Rejected because LLM-generated tasks are non-deterministic. Same skill prompt produces different descriptions each time.
- Session-level lock: Rejected because it would serialize all skill emissions per session unnecessarily.
- Upsert on (board_id, title): Rejected because titles are not guaranteed unique within a board.

**Consequences**: First-activation-wins semantics for the decomposer fallback. The marker task pattern is needed because decomposer output is non-deterministic but we cannot per-step key it before it exists.

### Decision 3: Two-axis split (emission vs. surfacing)

**Chosen Approach**: Task emission (Phase D) fires whenever `taskManager != nil`, regardless of `taskBoardConfig.Enabled`. Tool surfacing, prompt supplement, and context injection are gated by `taskBoardConfig.Enabled`.

**Rationale**: Skills that emit tasks need the data to exist in the store even if the agent's UI doesn't show the kanban tool. Background task tracking enables stickiness checking (a skill stays active while it has open tasks) without requiring the agent to interact with the board directly.

**Alternatives Considered**:
- Single flag gates both: Rejected because it would force agents that need stickiness to also surface the kanban UI, which is noisy for simple agents.
- Per-skill emission flag only: Rejected because the agent layer needs a master switch to disable emission globally during testing or for lightweight agents.

**Consequences**: Two configuration points that operators must understand. Mitigated by documentation and the default (emission on, surfacing off).

### Decision 4: Compaction levels for long-running tasks

**Chosen Approach**: Three levels (0=full, 1=summarized, 2=archived). Level 1 replaces description/notes/approach with a compact LLM-generated summary.

**Rationale**: Long-running agents accumulate hundreds of closed tasks. Injecting full text into context would exceed token budgets. Compaction preserves task existence and summary while freeing tokens. The `buildTaskContext()` function already caps output via `context_budget_tokens`.

**Alternatives Considered**:
- Hard delete old tasks: Rejected because audit trail and historical context are valuable.
- External archive (separate table): Adds complexity without benefit; the compacted field approach keeps queries simple.

**Consequences**: LLM summarization adds latency to the compaction process. Mitigated by running compaction asynchronously (not on the critical path).

### Decision 5: DFS-based cycle detection on live graph

**Chosen Approach**: At AddDependency time, DFS from the target node following outgoing BLOCKS edges.

**Rationale**: The dependency graph is typically small (tens to low hundreds of nodes per board). DFS is simple, correct, and adequate for this scale.

**Alternatives Considered**:
- Maintain a topological order index: O(1) cycle check at write time, but requires maintenance on every graph mutation. Over-engineered for typical board sizes.
- Reject and let the user fix: Poor UX — the agent would create a broken dependency and get an opaque error.

**Consequences**: O(V+E) worst case per AddDependency call. Acceptable for boards with < 1000 tasks.

---

## Constraints and Limitations

### Board Size

**Description**: Boards are designed for tens to low hundreds of tasks. Boards with > 1000 tasks may see degraded ready-front query performance.

**Rationale**: The ready-front query joins tasks with dependencies. At large scale, this join becomes expensive without specialized indexing.

**Workaround**: Use multiple boards to partition work. The `ListBoards` endpoint enables board-per-workflow patterns.

### Compaction Requires LLM

**Description**: The `CompactClosedTasks` method requires a summarize function (typically an LLM call). Without an LLM, compaction is unavailable.

**Rationale**: Summarization quality matters — a bad summary loses critical context. LLM-driven summarization produces useful compact representations.

**Workaround**: Agents without LLM access can skip compaction; old tasks simply remain at level 0 and get truncated by the token budget in `buildTaskContext()`.

### Single-Writer Claim Semantics

**Description**: ClaimTask is atomic but advisory — there is no session heartbeat or automatic release on agent crash.

**Rationale**: Adding lease/heartbeat infrastructure would significantly increase complexity for a benefit that is rare in practice (agents don't crash mid-task often).

**Workaround**: Operators can manually release stuck claims via the gRPC `ReleaseTask` endpoint.

---

## Concurrency Model

**Threading**: The Manager is safe for concurrent use from multiple goroutines. All shared state lives in the TaskStore (database), which handles serialization.

**Synchronization**: No in-memory locks in Manager. Atomicity is delegated to the store layer (SQL transactions for claim/release/close).

**Race Conditions**:
- Concurrent claims: handled by store-level atomic compare-and-swap (status must be OPEN).
- Concurrent ensureBoard: probe-then-create pattern with retry on duplicate key. Both `Emitter.ensureBoard` and `TaskBoardTool.resolveBoardForWrite` implement this pattern.
- Concurrent dependency writes: cycle detection runs at read time; a race between two concurrent AddDependency calls could theoretically both pass cycle detection. The store-level unique constraint on (from_task_id, to_task_id) prevents duplicate edges.

**Testing**: All task system code is tested with `go test -race -tags fts5`.

---

## Error Handling Philosophy

**Strategy**: Best-effort for auxiliary operations; strict for core operations.

**Core Operations** (return errors to caller):
- CreateTask, ClaimTask, CloseTask, TransitionTask, AddDependency
- Cycle detection failures, WIP limit violations, store errors

**Auxiliary Operations** (log and continue):
- Event publishing to MessageBus
- Graph memory creation on task completion
- History recording
- Auto-completion cascade failures

**Error Propagation**: Manager methods return errors from store operations. The `task_board` tool wraps errors into `shuttle.Result` with error codes (INVALID_PARAMETER, NOT_FOUND, CLAIM_ERROR, etc.) that the LLM can interpret.

---

## Related Work

### Beads (gastownhall/beads)

**Reference**: https://github.com/gastownhall/beads

**Relationship**: The backward decomposition strategy is directly inspired by the beads model of working backward from goals to prerequisites. The proto service comment cites this influence explicitly.

### Kanban (Lean Manufacturing)

**Reference**: Ohno, T. (1988). *Toyota Production System: Beyond Large-Scale Production.*

**Relationship**: WIP limits, status lanes, and the pull-based ready-front model are kanban principles applied to cognitive work.

### Topological Sort for Cycle Detection

**Reference**: Kahn, A. B. (1962). Topological sorting of large networks. *Communications of the ACM*, 5(11), 558-562.

**Relationship**: Pre-materialization cycle detection in the Decomposer uses Kahn's algorithm to reject cyclic LLM output before any tasks are persisted.

---

## Further Reading

- [Task Service API Reference](../reference/task-service.md): Complete RPC specifications
- [Task Board User Guide](../guides/task-board.md): Practical usage instructions
- [Skills Overhaul Architecture](skills-overhaul.md): Phase D integration context
- [Graph Memory Architecture](graph-memory.md): Memory entities created on task completion
