---
title: "TaskService API Reference"
weight: 20
---

# TaskService API Reference

The `loom.v1.TaskService` provides persistent, dependency-aware task decomposition and kanban-style work management for agents. Defined in `proto/loom/v1/task.proto`.

---

## Service Overview

| Category | RPCs |
|----------|------|
| Task CRUD | CreateTask, GetTask, UpdateTask, DeleteTask, ListTasks |
| Workflow Operations | ClaimTask, ReleaseTask, CloseTask, TransitionTask |
| Dependency Graph | AddDependency, RemoveDependency, GetReadyFront, GetBlockedTasks |
| Board Management | CreateBoard, GetBoard, ListBoards |
| LLM-Assisted | DecomposeTask |
| Agent Context | GetTaskContext |
| Streaming | StreamTaskUpdates |

---

## Enums

### TaskStatus

| Value | Number | Description |
|-------|--------|-------------|
| TASK_STATUS_UNSPECIFIED | 0 | Default / unset |
| TASK_STATUS_OPEN | 1 | Ready to be picked up |
| TASK_STATUS_IN_PROGRESS | 2 | Claimed and being worked on |
| TASK_STATUS_BLOCKED | 3 | Waiting on dependencies |
| TASK_STATUS_DONE | 4 | Completed |
| TASK_STATUS_DEFERRED | 5 | Parked intentionally |
| TASK_STATUS_CANCELLED | 6 | Won't do |

### TaskPriority

| Value | Number | Description |
|-------|--------|-------------|
| TASK_PRIORITY_UNSPECIFIED | 0 | Default |
| TASK_PRIORITY_CRITICAL | 1 | P0: drop everything |
| TASK_PRIORITY_HIGH | 2 | P1: do next |
| TASK_PRIORITY_MEDIUM | 3 | P2: normal work |
| TASK_PRIORITY_LOW | 4 | P3: when time permits |
| TASK_PRIORITY_BACKLOG | 5 | P4: someday |

### TaskCategory

| Value | Number | Description |
|-------|--------|-------------|
| TASK_CATEGORY_UNSPECIFIED | 0 | Default |
| TASK_CATEGORY_RESEARCH | 1 | Information gathering, literature review |
| TASK_CATEGORY_ANALYSIS | 2 | Data analysis, investigation, diagnosis |
| TASK_CATEGORY_IMPLEMENTATION | 3 | Building, coding, creating artifacts |
| TASK_CATEGORY_REVIEW | 4 | Evaluating, auditing, quality checking |
| TASK_CATEGORY_WRITING | 5 | Documentation, reports, communication |
| TASK_CATEGORY_DECISION | 6 | Architectural decisions, trade-off evaluation |
| TASK_CATEGORY_INVESTIGATION | 7 | Debugging, root cause analysis |
| TASK_CATEGORY_PLANNING | 8 | Strategy, roadmap, decomposition |
| TASK_CATEGORY_OTHER | 9 | Uncategorized |

### TaskDependencyType

| Value | Number | Description |
|-------|--------|-------------|
| TASK_DEPENDENCY_TYPE_UNSPECIFIED | 0 | Default |
| TASK_DEPENDENCY_TYPE_BLOCKS | 1 | to_task blocks from_task (from cannot start until to is DONE) |
| TASK_DEPENDENCY_TYPE_PARENT_CHILD | 2 | Hierarchical: from_task is a child of to_task |
| TASK_DEPENDENCY_TYPE_DISCOVERED_FROM | 3 | from_task was discovered while working on to_task |
| TASK_DEPENDENCY_TYPE_RELATED | 4 | Informational: tasks are related but neither blocks the other |

### DecomposeStrategy

| Value | Number | Description |
|-------|--------|-------------|
| DECOMPOSE_STRATEGY_UNSPECIFIED | 0 | Default (falls back to BACKWARD) |
| DECOMPOSE_STRATEGY_BACKWARD | 1 | Walk backward from goal to prerequisites (beads model) |
| DECOMPOSE_STRATEGY_FORWARD | 2 | Plan sequentially from current state toward goal |
| DECOMPOSE_STRATEGY_PARALLEL | 3 | Maximize tasks that can run concurrently |

---

## Core Messages

### Task

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| id | string | 1 | SHA256 content-hash ID (merge-safe across agents/sessions) |
| title | string | 2 | Short title (< 120 chars) |
| description | string | 3 | Detailed description of what needs to be done |
| objective | string | 4 | What "done" looks like |
| approach | string | 5 | How to accomplish the objective |
| acceptance_criteria | string | 6 | Verifiable completion conditions |
| notes | string | 7 | Running progress log. Survives context window compaction |
| status | TaskStatus | 8 | Lifecycle state |
| priority | TaskPriority | 9 | Urgency level |
| category | TaskCategory | 10 | Type of cognitive work |
| tags | repeated string | 11 | Freeform classification tags |
| owner_agent_id | string | 12 | Agent that created this task |
| assignee_agent_id | string | 13 | Agent currently working on this task |
| claimed_by_session | string | 14 | Session ID holding the claim lock |
| created_at | int64 | 15 | Unix milliseconds |
| updated_at | int64 | 16 | Unix milliseconds |
| claimed_at | int64 | 17 | Unix milliseconds |
| closed_at | int64 | 18 | Unix milliseconds |
| close_reason | string | 19 | Completion summary or cancellation reason |
| parent_id | string | 20 | Parent task ID (hierarchical decomposition) |
| child_ids | repeated string | 21 | Child task IDs (populated by server on read) |
| entity_ids | repeated string | 22 | Graph memory entity IDs linked to this task |
| metadata | map<string,string> | 23 | Arbitrary key-value metadata |
| board_id | string | 24 | Board this task belongs to |
| compaction_level | int32 | 25 | 0=full, 1=summarized, 2=archived |
| compacted_summary | string | 26 | Summary created during compaction |
| output_policy | OutputPolicy | 27 | Validation policy checked before close |
| estimated_effort | string | 28 | Freeform effort estimate |

### TaskDependency

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| from_task_id | string | 1 | This task... |
| to_task_id | string | 2 | ...depends on this task |
| type | TaskDependencyType | 3 | Edge classification |
| created_at | int64 | 4 | Unix milliseconds |
| created_by | string | 5 | Agent that created this dependency |
| metadata | map<string,string> | 6 | Arbitrary metadata |

### TaskBoard

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| id | string | 1 | Board identifier |
| name | string | 2 | Human-readable name |
| workflow_id | string | 3 | Optional: tie to a workflow execution |
| lanes | repeated TaskLane | 4 | Kanban columns |
| metadata | map<string,string> | 5 | Arbitrary metadata |
| created_at | int64 | 6 | Unix milliseconds |

### TaskLane

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| name | string | 1 | Lane display name |
| status | TaskStatus | 2 | Status that tasks in this lane have |
| task_ids | repeated string | 3 | Task IDs in display order |
| wip_limit | int32 | 4 | Work-in-progress limit (0 = unlimited) |

### TaskBoardStats

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| total | int32 | 1 | Total tasks on board |
| open | int32 | 2 | OPEN count |
| in_progress | int32 | 3 | IN_PROGRESS count |
| blocked | int32 | 4 | BLOCKED count |
| done | int32 | 5 | DONE count |
| deferred | int32 | 6 | DEFERRED count |
| cancelled | int32 | 7 | CANCELLED count |

### TaskContext

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| current_tasks | repeated Task | 1 | Tasks the agent currently has claimed |
| ready_tasks | repeated Task | 2 | Tasks with all deps satisfied |
| blocked_by_me | repeated Task | 3 | Tasks blocked by agent's current task(s) |
| blocking_me | repeated Task | 4 | Tasks blocking agent's current task(s) |
| stats | TaskBoardStats | 5 | Board-level aggregate stats |
| board_summary | string | 6 | Compact text summary for LLM injection |

### TaskHistoryEntry

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| id | string | 1 | Entry identifier |
| task_id | string | 2 | Task this event relates to |
| action | string | 3 | One of: created, claimed, released, transitioned, closed, updated, dependency_added, dependency_removed |
| old_status | string | 4 | Previous status |
| new_status | string | 5 | New status |
| agent_id | string | 6 | Agent that performed the action |
| session_id | string | 7 | Session that performed the action |
| timestamp | int64 | 8 | Unix milliseconds |
| details_json | string | 9 | JSON blob with action-specific details |

### TaskUpdate

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| task | Task | 1 | Updated task |
| action | string | 2 | Action that triggered the update |
| agent_id | string | 3 | Agent that triggered it |
| timestamp | int64 | 4 | Unix milliseconds |

### BlockerList

| Field | Type | Number | Description |
|-------|------|--------|-------------|
| task_ids | repeated string | 1 | IDs of blocking tasks |

---

## RPCs

### CreateTask

Creates a new task on a board.

**HTTP**: `POST /v1/tasks`

**Request**: `CreateTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Task to create. `id` is generated server-side if empty. |

**Response**: `CreateTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Created task with server-generated fields populated |

---

### GetTask

Retrieves a task by ID, including its dependency edges.

**HTTP**: `GET /v1/tasks/{task_id}`

**Request**: `GetTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task_id | string | Task identifier |

**Response**: `GetTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | The task (with child_ids populated) |
| dependencies | repeated TaskDependency | Edges where this task is from_task (things it depends on) |
| dependents | repeated TaskDependency | Edges where this task is to_task (things depending on it) |

---

### UpdateTask

Updates mutable task fields.

**HTTP**: `PATCH /v1/tasks/{task.id}`

**Request**: `UpdateTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Task with updated fields |
| update_mask | repeated string | Fields to update. Empty = update all mutable fields. |

**Response**: `UpdateTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Updated task |

---

### DeleteTask

Soft-deletes a task.

**HTTP**: `DELETE /v1/tasks/{task_id}`

**Request**: `DeleteTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task_id | string | Task to delete |

**Response**: `DeleteTaskResponse` (empty)

---

### ListTasks

Lists tasks with filtering and pagination.

**HTTP**: `GET /v1/tasks`

**Request**: `ListTasksRequest`

| Field | Type | Description |
|-------|------|-------------|
| board_id | string | Filter by board |
| status | TaskStatus | Filter by status |
| priority | TaskPriority | Filter by priority |
| category | TaskCategory | Filter by category |
| assignee_agent_id | string | Filter by assignee |
| parent_id | string | Filter by parent (list children) |
| query | string | Full-text search |
| limit | int32 | Page size |
| offset | int32 | Page offset |

**Response**: `ListTasksResponse`

| Field | Type | Description |
|-------|------|-------------|
| tasks | repeated Task | Matching tasks |
| total_count | int32 | Total matching (ignoring limit/offset) |

---

### ClaimTask

Atomically claims a task for an agent session. Fails if already claimed or not in OPEN status. Enforces WIP limits.

**HTTP**: `POST /v1/tasks/{task_id}:claim`

**Request**: `ClaimTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task_id | string | Task to claim |
| agent_id | string | Agent claiming the task |
| session_id | string | Session performing the claim |

**Response**: `ClaimTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Claimed task (status=IN_PROGRESS) |
| success | bool | Whether the claim succeeded |
| error | string | Error message if claim failed |

**Error Conditions**:
- Task not in OPEN status
- Task already claimed by another session
- WIP limit exceeded for the board's IN_PROGRESS lane

---

### ReleaseTask

Releases a claim, returning the task to OPEN status.

**HTTP**: `POST /v1/tasks/{task_id}:release`

**Request**: `ReleaseTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task_id | string | Task to release |
| session_id | string | Session releasing (must match claim holder) |

**Response**: `ReleaseTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Released task (status=OPEN) |

---

### CloseTask

Marks a task as DONE with a completion reason. If the task has an `output_policy`, validates output before closing.

**HTTP**: `POST /v1/tasks/{task_id}:close`

**Request**: `CloseTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task_id | string | Task to close |
| close_reason | string | Completion summary |
| output | string | Output to validate against output_policy |

**Response**: `CloseTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Closed task (status=DONE) |
| validation_feedback | string | Feedback if output validation failed |
| validation_passed | bool | Whether validation passed |

**Side Effects**:
- Auto-creates graph memory summarizing completed work
- Auto-unblocks dependents whose remaining BLOCKS dependencies are now all terminal
- Auto-completes parent if all children are terminal (recursive)

---

### TransitionTask

Changes task status with validation.

**HTTP**: `POST /v1/tasks/{task_id}:transition`

**Request**: `TransitionTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| task_id | string | Task to transition |
| new_status | TaskStatus | Target status |
| reason | string | Reason for transition |

**Response**: `TransitionTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| task | Task | Transitioned task |

---

### AddDependency

Creates a dependency edge between tasks. Rejects if the edge would create a cycle in the BLOCKS subgraph.

**HTTP**: `POST /v1/tasks/dependencies`

**Request**: `AddTaskDependencyRequest`

| Field | Type | Description |
|-------|------|-------------|
| dependency | TaskDependency | Edge to create |

**Response**: `AddTaskDependencyResponse`

| Field | Type | Description |
|-------|------|-------------|
| dependency | TaskDependency | Created dependency |

**Side Effects**:
- If type is BLOCKS and the blocker is not terminal, the dependent task (from_task) is auto-transitioned to BLOCKED (from OPEN or IN_PROGRESS).

**Error Conditions**:
- Self-dependency (from_task_id == to_task_id)
- Cycle detection: adding the edge would create a cycle in the BLOCKS subgraph

---

### RemoveDependency

Removes a dependency edge.

**HTTP**: `DELETE /v1/tasks/dependencies/{from_task_id}/{to_task_id}`

**Request**: `RemoveTaskDependencyRequest`

| Field | Type | Description |
|-------|------|-------------|
| from_task_id | string | Dependent task |
| to_task_id | string | Blocker task |

**Response**: `RemoveTaskDependencyResponse` (empty)

**Side Effects**:
- If the dependent task is BLOCKED and all remaining BLOCKS dependencies are now terminal, it auto-transitions to OPEN.

---

### GetReadyFront

Returns tasks with all dependencies satisfied (ready to work on).

**HTTP**: `GET /v1/boards/{board_id}/ready`

**Request**: `GetReadyFrontRequest`

| Field | Type | Description |
|-------|------|-------------|
| board_id | string | Board to query |
| agent_id | string | Optional: filter by agent capability |
| min_priority | TaskPriority | Optional: minimum priority filter |
| max_results | int32 | Maximum tasks to return |

**Response**: `GetReadyFrontResponse`

| Field | Type | Description |
|-------|------|-------------|
| ready_tasks | repeated Task | Tasks ready to be claimed |
| stats | TaskBoardStats | Board-level stats |

---

### GetBlockedTasks

Returns tasks waiting on unfinished dependencies.

**HTTP**: `GET /v1/boards/{board_id}/blocked`

**Request**: `GetBlockedTasksRequest`

| Field | Type | Description |
|-------|------|-------------|
| board_id | string | Board to query |

**Response**: `GetBlockedTasksResponse`

| Field | Type | Description |
|-------|------|-------------|
| blocked_tasks | repeated Task | Blocked tasks |
| blockers | map<string, BlockerList> | For each blocked task ID, the IDs of tasks blocking it |

---

### CreateBoard

Creates a new kanban board.

**HTTP**: `POST /v1/boards`

**Request**: `CreateBoardRequest`

| Field | Type | Description |
|-------|------|-------------|
| board | TaskBoard | Board to create |

**Response**: `CreateBoardResponse`

| Field | Type | Description |
|-------|------|-------------|
| board | TaskBoard | Created board |

---

### GetBoard

Retrieves a board with its lanes and task counts.

**HTTP**: `GET /v1/boards/{board_id}`

**Request**: `GetBoardRequest`

| Field | Type | Description |
|-------|------|-------------|
| board_id | string | Board identifier |

**Response**: `GetBoardResponse`

| Field | Type | Description |
|-------|------|-------------|
| board | TaskBoard | The board |
| stats | TaskBoardStats | Aggregate task counts |

---

### ListBoards

Lists all boards.

**HTTP**: `GET /v1/boards`

**Request**: `ListBoardsRequest` (empty)

**Response**: `ListBoardsResponse`

| Field | Type | Description |
|-------|------|-------------|
| boards | repeated TaskBoard | All boards |

---

### DecomposeTask

Uses an LLM to break a goal into a dependency DAG of subtasks. Creates the tasks and dependencies in the store.

**HTTP**: `POST /v1/tasks:decompose`

**Request**: `DecomposeTaskRequest`

| Field | Type | Description |
|-------|------|-------------|
| goal | string | High-level goal to decompose |
| context | string | Additional context for the LLM |
| board_id | string | Target board for created tasks |
| parent_task_id | string | Optional: create subtasks under this parent |
| max_depth | int32 | Maximum decomposition depth (default: 3) |
| strategy | DecomposeStrategy | Decomposition strategy |
| agent_id | string | Agent performing decomposition (for LLM selection) |

**Response**: `DecomposeTaskResponse`

| Field | Type | Description |
|-------|------|-------------|
| tasks | repeated Task | Created tasks |
| dependencies | repeated TaskDependency | Created dependency edges |
| reasoning | string | LLM's decomposition rationale |
| board | TaskBoard | Board (if board_id was provided and resolved) |

**Error Conditions**:
- Empty goal
- LLM call failure (retried up to 2 times with JSON validation feedback)
- Cyclic dependency in LLM output (caught by Kahn's algorithm before materialization)

---

### GetTaskContext

Returns the current task context for an agent, including claimed tasks, ready front, and board summary.

**HTTP**: `GET /v1/agents/{agent_id}/task-context`

**Request**: `GetTaskContextRequest`

| Field | Type | Description |
|-------|------|-------------|
| agent_id | string | Agent identifier |
| board_id | string | Board to query |
| max_tokens | int32 | Maximum tokens for context block (default: 500) |

**Response**: `TaskContext`

See [TaskContext message](#taskcontext) above.

---

### StreamTaskUpdates

Streams real-time task status changes for a board.

**HTTP**: `GET /v1/boards/{board_id}/updates:stream`

**Request**: `StreamTaskUpdatesRequest`

| Field | Type | Description |
|-------|------|-------------|
| board_id | string | Board to monitor |
| agent_id | string | Optional: filter by agent |

**Response**: `stream TaskUpdate`

Server-streaming RPC. Each message is a `TaskUpdate` containing the updated task, the action that triggered the update, the agent involved, and a timestamp.

---

## Configuration

### TaskBoardConfig (in AgentConfig.memory)

Defined in `proto/loom/v1/agent_config.proto`:

```protobuf
message TaskBoardConfig {
  bool enabled = 1;                     // Whether task board tool is surfaced
  bool auto_decompose = 2;             // Auto-decompose complex goals
  int32 max_depth = 3;                 // Max decomposition depth (default: 3)
  string default_board_id = 4;         // Default board ID
  DecomposeStrategy default_strategy = 5; // Default strategy
  int32 context_budget_tokens = 6;     // Max tokens for context injection (default: 500)
}
```

**Two-axis behavior**:

| Flag | Controls |
|------|----------|
| `taskManager != nil` (server-level) | Task emission (Phase D), stickiness checking |
| `TaskBoardConfig.enabled` (agent-level) | `task_board` tool registration, prompt supplement, context injection |

---

## Event Topics

The Manager publishes lifecycle events to the MessageBus on these topics:

| Topic | Trigger |
|-------|---------|
| `task.created` | CreateTask, CreateTaskIdempotent (new) |
| `task.claimed` | ClaimTask |
| `task.released` | ReleaseTask |
| `task.completed` | CloseTask, auto-complete parent |
| `task.blocked` | TransitionTask to BLOCKED |
| `task.updated` | UpdateTask, TransitionTask (non-BLOCKED) |
| `task.deleted` | DeleteTask |
| `board.updated` | CreateBoard |

---

## Further Reading

- [Task System Architecture](../architecture/task-system.md): Design rationale and trade-offs
- [Task Board User Guide](../guides/task-board.md): Practical usage instructions
