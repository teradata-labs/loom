---
title: "Task Board Guide"
weight: 15
---

# Task Board Guide

This guide covers how to configure and use the task board system for dependency-aware work management within Loom agents.

---

## Overview

The task board gives agents the ability to:

1. **Decompose** complex goals into dependency-tracked subtasks using an LLM.
2. **Track** work via a kanban board with status transitions.
3. **Remember** what they are working on across context window compaction.
4. **Coordinate** with skills that emit tasks automatically.

---

## Enabling the Task Board

### Server-Level Requirement

The task manager must be initialized at the server level. When running `looms serve`, the task subsystem is wired automatically if the database migrations include the task tables (migration `000003_tasks`).

### Agent Configuration

Add `task_board` to the agent's `memory` config in the agent YAML or via `AgentConfig` proto:

```yaml
memory:
  task_board:
    enabled: true
    default_board_id: "my-agent-board"
    max_depth: 3
    default_strategy: DECOMPOSE_STRATEGY_BACKWARD
    context_budget_tokens: 500
```

**Fields**:

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | false | Whether the `task_board` tool is surfaced to the agent |
| `default_board_id` | "" | Board ID used when none is specified. Empty means the agent creates/uses a board named after itself |
| `max_depth` | 3 | Maximum decomposition depth |
| `default_strategy` | BACKWARD | Default LLM decomposition strategy |
| `context_budget_tokens` | 500 | Max tokens for the task context block injected into the system prompt. 0 disables injection |

**Two-axis split**: Setting `enabled: false` hides the `task_board` tool and context injection from the agent but does NOT prevent skills from emitting tasks (Phase D emission). This allows background task tracking for stickiness checking without cluttering the agent's UI.

---

## Using the task_board Tool

When `enabled: true`, the agent has access to a `task_board` tool with 10 actions.

### Recommended Workflow

```
decompose → ready → claim → work → update notes → close → ready → claim → ...
```

### Actions

#### decompose

Break a complex goal into a dependency DAG of subtasks.

```json
{
  "action": "decompose",
  "goal": "Research and implement a caching layer for the API",
  "context": "The API currently makes redundant database calls on every request",
  "strategy": "backward"
}
```

**Parameters**:
- `goal` (required): The high-level objective to break down
- `context` (optional): Additional information for the LLM
- `strategy` (optional): `backward` (default), `forward`, or `parallel`
- `board_id` (optional): Target board (falls back to config default)
- `parent_id` (optional): Create subtasks under this parent task

**Response** includes tasks created, dependency count, and the LLM's reasoning.

**Strategies explained**:
- `backward`: Start from the goal, work backward identifying prerequisites recursively. Best for complex goals with many unknowns. Produces deep DAGs.
- `forward`: Plan sequentially from current state. Best for well-understood linear workflows. Produces pipeline-like structures.
- `parallel`: Maximize concurrent work. Best for multi-agent scenarios. Produces wide, shallow DAGs.

#### ready

Get the "ready front": tasks with all dependencies satisfied, available to claim.

```json
{
  "action": "ready",
  "board_id": "my-board"
}
```

Returns up to 10 tasks sorted by priority with their ID, title, status, priority, and assignee.

#### claim

Atomically claim a task to work on. Prevents other agents from picking it up.

```json
{
  "action": "claim",
  "task_id": "abc123..."
}
```

Transitions the task from OPEN to IN_PROGRESS and sets the assignee/session. Fails if:
- Task is not in OPEN status
- WIP limit would be exceeded

#### update

Update task notes, approach, description, or status.

```json
{
  "action": "update",
  "task_id": "abc123...",
  "notes": "[PROGRESS] Found that Redis has 200ms cold-start latency",
  "approach": "Using in-memory LRU with Redis as L2 fallback"
}
```

**Key behavior**: Notes are APPENDED (never overwritten). Use structured prefixes:
- `[STARTED]` — Beginning work
- `[PROGRESS]` — Intermediate findings
- `[KEY FINDING]` — Important discovery
- `[BLOCKED]` — Hit a blocker
- `[DECISION]` — Made a choice

#### close

Mark a task as done with a completion reason.

```json
{
  "action": "close",
  "task_id": "abc123...",
  "reason": "Implemented LRU cache with 95% hit rate in testing"
}
```

**Side effects**:
- Creates a graph memory entity summarizing the completed work
- Auto-unblocks dependent tasks whose remaining blockers are all now terminal
- Auto-completes parent task if all siblings are also terminal

#### create

Create a single task manually (without LLM decomposition).

```json
{
  "action": "create",
  "title": "Review cache invalidation edge cases",
  "objective": "Ensure stale data is never served after a write",
  "category": "review",
  "priority": "P1",
  "estimated_effort": "30 min",
  "tags": ["cache", "correctness"],
  "parent_id": "parent-task-id"
}
```

#### list

List tasks with optional filtering.

```json
{
  "action": "list",
  "status": "open",
  "priority": "P1",
  "query": "cache"
}
```

Returns up to 20 tasks matching the filters, with total count.

#### show

Get full task details including dependencies and dependents.

```json
{
  "action": "show",
  "task_id": "abc123..."
}
```

Returns complete task fields plus:
- `dependencies`: tasks that block this one
- `dependents`: tasks blocked by this one
- `child_ids`: subtask IDs

#### add_dep

Add a BLOCKS dependency between tasks.

```json
{
  "action": "add_dep",
  "task_id": "task-that-depends",
  "depends_on": "task-that-blocks"
}
```

Rejects if the edge would create a cycle. If the blocker is not terminal, the dependent task auto-transitions to BLOCKED.

#### board

Get board overview with stats, or list all boards.

```json
{
  "action": "board",
  "board_id": "my-board"
}
```

Without `board_id`, lists all boards. With `board_id`, returns the board info and per-status counts.

---

## Task Context Injection

When `enabled: true` and `context_budget_tokens > 0`, the agent's system prompt is supplemented each turn with a `--- TASK CONTEXT ---` block rebuilt from the database. This block contains:

1. **Current tasks**: Tasks the agent has claimed (IN_PROGRESS), with objective, approach, and recent notes.
2. **Ready front**: Tasks available to claim, with priority and effort estimates.
3. **Recent completions**: Last 3 closed tasks with close reasons (for momentum).
4. **Board stats**: Total/open/in_progress/blocked/done counts.

This context survives context window compaction because it is rebuilt from the database each turn, not carried in conversation history.

**Budget enforcement**: The context block is truncated to `context_budget_tokens * 4` characters (rough 4-chars-per-token estimate). Default is 500 tokens (2000 characters).

---

## Skills That Emit Tasks

### How It Works

When a skill activates (Phase D of the skills overhaul pipeline), the skill task emitter automatically creates tasks on the agent's board. This happens regardless of whether `task_board.enabled` is true -- emission is gated only by `taskManager != nil` at the server level and the skill's `emit_tasks` flag.

### Skill-Level Configuration

In a skill YAML file:

```yaml
name: code-review
title: Code Review
emit_tasks: true  # default when omitted

task_template:
  max_tasks: 5
  steps:
    - title: "Understand the change"
      objective: "Read diff and summarize purpose"
      category: research
      priority: P2
      estimated_effort: "5 min"
      depends_on: []
      tags: [understanding]

    - title: "Check correctness"
      objective: "Verify logic, edge cases, error handling"
      category: review
      priority: P1
      estimated_effort: "10 min"
      depends_on: [0]
      tags: [correctness]

    - title: "Check style and conventions"
      objective: "Verify naming, formatting, Go idioms"
      category: review
      priority: P2
      estimated_effort: "5 min"
      depends_on: [0]
      tags: [style]

    - title: "Write review summary"
      objective: "Produce actionable feedback organized by severity"
      category: writing
      priority: P1
      estimated_effort: "5 min"
      depends_on: [1, 2]
      tags: [output]
```

**Fields in `task_template`**:
- `steps`: Array of task definitions. `depends_on` uses 0-based indices into this array.
- `max_tasks`: Cap on emitted tasks (default: 8).
- `root_title`: Optional parent task title.
- `ephemeral_on_deactivate`: If true, unstarted (OPEN) tasks are deleted when the skill deactivates.

**Per-skill opt-out**: Set `emit_tasks: false` to prevent a skill from creating tasks.

### Decomposer Fallback

If a skill does NOT declare a `task_template`, the emitter falls back to the LLM-driven Decomposer, using the skill's prompt as the goal (forward strategy, max depth 2).

### Idempotency

All skill-emitted tasks carry a `skill_idempotency_key` of the form:
```
skill:<name>|sess:<sessionID>|step:<index>
```

Re-activation of the same skill on the same session returns existing tasks rather than creating duplicates. This prevents duplicate boards when:
- A skill re-triggers on multiple user turns
- The conversation loop retries
- The agent is restarted mid-session

### Agent-Level Master Switch

The skills config has a `tasks_enabled` flag (default: true). When set to false, no skill emits tasks regardless of per-skill settings.

### Sticky-While-Open-Tasks

When a skill has open (non-terminal) tasks on the board, it is treated as "sticky" during eviction. The orchestrator will not evict it to make room for a new skill, even if its confidence is lower. This prevents abandoning in-flight work.

If ALL active skills are sticky, the concurrent skill cap is allowed to overflow for that turn rather than evicting load-bearing skills.

---

## Working with Boards

### Creating a Board

Boards are auto-created when needed (by `resolveBoardForWrite` in the tool or `ensureBoard` in the emitter). To create one explicitly:

```bash
# Via gRPC
grpcurl -d '{"board": {"id": "my-board", "name": "My Project Board"}}' \
  localhost:50051 loom.v1.TaskService/CreateBoard

# Via HTTP gateway
curl -X POST http://localhost:8080/v1/boards \
  -d '{"board": {"id": "my-board", "name": "My Project Board"}}'
```

### Board-less Tasks

Tasks with an empty `board_id` are valid. They are queryable by `owner_agent_id` and participate in the dependency graph normally. They just don't appear in board-level queries (GetReadyFront, GetBlockedTasks with a board filter).

### WIP Limits

Set `wip_limit` on a board's IN_PROGRESS lane to cap how many tasks can be claimed simultaneously:

```json
{
  "board": {
    "id": "focused-board",
    "name": "Focus Board",
    "lanes": [
      {"name": "To Do", "status": "TASK_STATUS_OPEN"},
      {"name": "Doing", "status": "TASK_STATUS_IN_PROGRESS", "wip_limit": 2},
      {"name": "Done", "status": "TASK_STATUS_DONE"}
    ]
  }
}
```

When the WIP limit is reached, `ClaimTask` fails with an error message explaining the limit.

---

## Scheduling Workflows with Task Tracking

### Pattern: Workflow as Board

Use one board per workflow execution. Set `workflow_id` on the board to link it to a workflow run:

```json
{
  "board": {
    "id": "wf-run-12345",
    "name": "Data Pipeline Run #12345",
    "workflow_id": "data-pipeline-v2"
  }
}
```

### Pattern: Decompose then Distribute

1. Decompose a goal with `parallel` strategy to maximize concurrency.
2. Multiple agents can each call `ready` and `claim` tasks from the same board.
3. Closing tasks auto-unblocks dependents, making them available for the next agent.

### Pattern: Progressive Decomposition

1. Decompose at a high level (max_depth: 2) to get top-level phases.
2. When claiming a phase task, decompose it further with `parent_id` set to the phase.
3. Complete subtasks; parent auto-completes when all children are done.

---

## Monitoring

### StreamTaskUpdates

Subscribe to real-time task changes:

```bash
grpcurl -d '{"board_id": "my-board"}' \
  localhost:50051 loom.v1.TaskService/StreamTaskUpdates
```

### Event Bus Topics

If your agent has a MessageBus connected, these events fire:
- `task.created` — New task
- `task.claimed` — Task claimed by agent
- `task.released` — Claim released
- `task.completed` — Task closed as DONE
- `task.blocked` — Task transitioned to BLOCKED
- `task.updated` — Task fields updated
- `task.deleted` — Task soft-deleted
- `board.updated` — Board created

---

## Troubleshooting

### "FOREIGN KEY constraint failed"

The task's `board_id` references a board that does not exist. This should be auto-handled by `resolveBoardForWrite` and `ensureBoard`, but if you see this error:
1. Create the board explicitly via `CreateBoard`.
2. Ensure `default_board_id` in the agent config matches an existing board.

### Tasks not appearing in context

Check:
1. `task_board.enabled` is `true` in agent config.
2. `context_budget_tokens` is > 0 (0 disables injection).
3. Tasks exist on the configured `default_board_id`.

### Skills not emitting tasks

Check:
1. The server has `taskManager` initialized (requires task table migrations).
2. The skill does not have `emit_tasks: false`.
3. The agent-level `tasks_enabled` is not set to `false` in skills config.
4. For decomposer fallback: the skill has no `task_template` AND an LLM provider is available.

### Stuck claimed tasks

If an agent crashed while holding a claim:
```bash
grpcurl -d '{"task_id": "abc123", "session_id": "the-session"}' \
  localhost:50051 loom.v1.TaskService/ReleaseTask
```

---

## Further Reading

- [Task System Architecture](../architecture/task-system.md): Design rationale and internals
- [TaskService API Reference](../reference/task-service.md): Complete RPC specifications
- [Skills Overhaul Architecture](../architecture/skills-overhaul.md): Phase D context
