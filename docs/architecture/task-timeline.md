---
title: "Task Timeline Architecture"
weight: 15
---

# Task Timeline Architecture

The task timeline makes a task the surface where a human can see what the system actually did: which tools ran and what they returned, what the agent said, what a human was asked to approve, and how the task's state moved. It is a **read model with no storage of its own** — every fact it shows was already persisted by a writer that was going to run anyway.

This document records both the design and the design that was rejected, because the rejected one was built first and the reasons it lost are the most useful part of the record.

**Target Audience**: Architects, academics, advanced developers

---

## Contents

- [Problem Statement](#problem-statement)
- [The Rejected Design, and Why](#the-rejected-design-and-why)
- [Design Goals](#design-goals)
- [System Context](#system-context)
- [Architecture Overview](#architecture-overview)
- [Components](#components)
- [Key Interactions](#key-interactions)
- [Data Structures](#data-structures)
- [Design Trade-offs](#design-trade-offs)
- [Performance Characteristics](#performance-characteristics)
- [Concurrency Model](#concurrency-model)
- [Error Handling Philosophy](#error-handling-philosophy)
- [Constraints and Limitations](#constraints-and-limitations)
- [Implementation Status](#implementation-status)
- [Related Work](#related-work)
- [Further Reading](#further-reading)

---

## Problem Statement

A [task](./task-system.md) records *what should happen* — objective, approach, acceptance criteria, dependency edges. It records almost nothing about *what did happen*.

The critical observation is that the evidence **was never missing**. It was unjoinable:

```
┌───────────────────────────────────────────────────────────────────────────┐
│              What was already persisted, and what it lacked               │
│                                                                           │
│  messages                                          keyed by session_id    │
│  ├─ tool_calls_json    full tool name + input          NO task_id  ◀──┐   │
│  ├─ tool_result_json   serialized shuttle.Result:                     │   │
│  │                     success, error, ExecutionTimeMs               │   │
│  ├─ content            agent and user narrative                      │   │
│  └─ agent_id, timestamp                                              │   │
│                                                                       │   │
│  task_history                                      keyed by task_id ✓ │   │
│  └─ action, old_status, new_status, agent_id, session_id              │   │
│                                                                       │   │
│  human_requests (since v1.0.0)                     keyed by session_id│   │
│  ├─ question, request_type, priority                   NO task_id  ◀──┤   │
│  └─ status, response, responded_at, responded_by                      │   │
│                                                                       │   │
│                          the entire gap ──────────────────────────────┘   │
└───────────────────────────────────────────────────────────────────────────┘
```

Every category of thing a human wants to see on a task was already durably recorded, in full, by a writer already on the path. `messages` even carries tool execution *duration* and *success*, because `tool_result_json` is a serialized `shuttle.Result`. The only thing absent from two of the three tables was a `task_id` to join on.

Two visible product consequences followed. The Tera task trace view can only render tasks ordered by dependency depth annotated with lifecycle history, because that was the only per-task chronology that existed. And a pending human-in-the-loop approval could not be shown on the task it blocked — the one gap with no workaround, since a global request queue cannot tell you *which work* is stuck.

A third, subtler problem: `Task.notes` was serving two incompatible readers — injected into the prompt each turn (so it must stay small, and is truncated at the append sites) and simultaneously the only human-readable progress record (so truncation loses the record).

---

## The Rejected Design, and Why

A dedicated `task_activity` table was designed, implemented, benchmarked, and then deleted. It had: 11 event kinds, a per-kind durability split, a bounded ring buffer with drop accounting, batched group-commit writes, keyset pagination, and a three-level retention policy. It was measured at 865 ns per emit on the agent path and ~57 µs per entry at the store.

It was the wrong design, for one reason that subsumes all the others:

> It stored a **truncated, droppable** second copy of facts that were already stored **in full, durably**, a few tables over.

Concretely, its two highest-volume kinds — `TOOL_CALL` and `TOOL_RESULT`, two thirds of its write load — duplicated `messages.tool_calls_json` and `messages.tool_result_json`, capping the payload at 2 KiB and permitting loss under back-pressure, while the original was uncapped and durable. Its `HITL_REQUEST` / `HITL_RESPONSE` kinds duplicated `human_requests`. Its `LIFECYCLE` kind duplicated `task_history`. Its `SKILL_ACTIVATION` kind duplicated a join that `skill_idempotency_key` already expressed.

The cost of that duplication was not theoretical:

| The rejected design needed | The read model needs |
|---|---|
| A new table, 31 columns | Nothing |
| A new proto surface: 1 message, 2 enums, 3 RPCs, committed behind `buf breaking` before any consumer existed | Nothing |
| A bounded ring buffer, a writer goroutine, batching, backpressure policy | Nothing |
| Drop accounting, and a UI that admits "some detail was not recorded" | Nothing — the sources are durable |
| A retention policy, or the table becomes the largest in the database | Nothing — retention follows the source tables |
| ~57 µs of write work per event, on every tool call, forever, whether anyone ever looks | ~2.1 ms once, on read, only when a human opens the task |

The last row is the whole argument. The rejected design paid a permanent write-side cost to answer a question that is asked rarely. The read model pays nothing at write time and answers the question on demand.

Two lessons worth keeping:

1. **"Narrative index, not telemetry store" was doing rhetorical work.** That phrase was written as an explicit non-goal to prevent duplication — and then the duplication was built anyway. A stated non-goal is not a constraint unless something checks it. The check that should have run first: *for each field, which table already holds this?*
2. **Best-effort logging of the interesting case is indefensible when a durable copy exists.** Drops happen when the system is busiest, which is exactly when something worth seeing was happening.

---

## Design Goals

- **No new write path.** The timeline must not add a write anywhere. Attribution is a column on a row that was already being inserted.
- **Cannot be incomplete.** Because the timeline reads durable sources, it has no drop semantics, no buffer to overflow, and no window during which an event is lost. A source can fail, and that is reported — but a source cannot silently omit.
- **No new storage, therefore no new retention.** Nothing grows that was not already growing.
- **Full fidelity on read, truncation on display.** The sources store uncapped payloads. The read model returns them whole and lets the presentation layer excerpt.
- **Honest partiality.** When a source fails, the reader is told which one. An empty result from a failed source must never look like "nothing happened".
- **Attribution costs one column each.** `messages.task_id`, `human_requests.task_id`, `tasks.created_via`. That is the entire schema change.

**Non-goals**:

- **Not an event log.** There is no append-only table and no sequence. If a fact is not already recorded by some writer, the timeline does not show it — the fix is to record it where it belongs, not to add a parallel log.
- **Not a telemetry store.** Spans and Hawk remain the debugging surface.
- **Not LLM context.** The timeline is never injected into the system prompt. Agent-facing working memory stays on `Task.notes`.
- **Not real-time.** There is no streaming RPC. A UI polls or re-reads; the sources are already consistent.
- **Not cross-task.** Ordering is per task.

---

## System Context

```
┌────────────────────────────────────────────────────────────────────────────┐
│                              WRITE SIDE                                     │
│                     (no new writes — only a stamped column)                 │
│                                                                            │
│   ClaimTask ──▶ taskctx.ContextWithAttribution(ctx, {task, session, agent}) │
│                                   │                                        │
│              ┌────────────────────┼─────────────────────┐                  │
│              ▼                    ▼                     ▼                  │
│     SessionStore            HumanRequestStore     task.Manager             │
│     .SaveMessage()          .Store()              .recordHistory()         │
│              │                    │                     │                  │
│      stamps task_id       stamps task_id         already keyed by task     │
│              ▼                    ▼                     ▼                  │
│      ┌──────────────┐    ┌─────────────────┐   ┌──────────────┐           │
│      │  messages    │    │ human_requests  │   │ task_history │           │
│      └──────┬───────┘    └────────┬────────┘   └──────┬───────┘           │
└─────────────┼─────────────────────┼───────────────────┼───────────────────┘
              │                     │                   │
┌─────────────┼─────────────────────┼───────────────────┼───────────────────┐
│             ▼                     ▼                   ▼      READ SIDE     │
│   ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐        │
│   │ SessionStore     │  │ HITLTimeline     │  │ HistorySource    │        │
│   │ .TimelineEvents  │  │ Source           │  │                  │        │
│   └────────┬─────────┘  └────────┬─────────┘  └────────┬─────────┘        │
│            └─────────────────────┼─────────────────────┘                   │
│                                  ▼  (queried concurrently)                 │
│                      ┌────────────────────────┐                            │
│                      │   task.TimelineReader  │                            │
│                      │   merge · sort · filter│                            │
│                      └───────────┬────────────┘                            │
│                                  ▼                                         │
│                          TimelineResult                                    │
│                    {Events, Truncated, PartialSources}                     │
└────────────────────────────────────────────────────────────────────────────┘
```

**Description**: The write side gains one stamped column per table and nothing else. The read side is three projections and a merge. Each projection lives with the table it reads, so no package depends on another's storage.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────┐
│                                                                          │
│  pkg/taskctx  (LEAF — imports only "context")                            │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │  Attribution{TaskID, BoardID, SessionID, AgentID, ParentAgentID}   │ │
│  │  ContextWithAttribution() · AttributionFromContext() · TaskIDFrom() │ │
│  └────────────────────────────────────────────────────────────────────┘ │
│         ▲                    ▲                        ▲                  │
│         │ (aliased)          │                        │                  │
│  ┌──────┴──────┐   ┌─────────┴─────────┐   ┌──────────┴──────────┐      │
│  │  pkg/task   │   │   pkg/shuttle     │   │     pkg/agent       │      │
│  │             │   │  human store      │   │   session store     │      │
│  │ TimelineRdr │   │  stamps task_id   │   │   stamps task_id    │      │
│  │ TimelineSrc │   │  ListByTask()     │   │   TimelineEvents()  │      │
│  │ HistorySrc  │   └───────────────────┘   │   HITLTimelineSrc   │      │
│  └─────────────┘                          └─────────────────────┘      │
│                                                                          │
│  Why taskctx is a leaf: pkg/shuttle must read the attribution, and       │
│  pkg/task -> pkg/communication -> pkg/types -> pkg/shuttle is a cycle.   │
│  Sitting below all of them is what lets every writer stamp a task_id     │
│  without any of them depending on each other.                           │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## Components

### Attribution (`pkg/taskctx/attribution.go`)

**Responsibility**: Carry the claimed task's identity on `context.Context` so writers several frames below the claim can stamp it.

**Rationale**: The writers that record what happened — the session store, the human-request store — are far below the code that knows which task is claimed. Threading a task ID through every signature between them would touch dozens of call sites and be silently wrong wherever a caller forgot. The pattern is already established twice in this codebase: `observability.SpanFromContext` and the agent's `ContextWithProgressCallback`.

**Why it is a leaf package**: `pkg/shuttle` needs to read the attribution, and `pkg/shuttle` cannot import `pkg/task` — the chain `pkg/task → pkg/communication → pkg/types → pkg/shuttle` is an import cycle. This was discovered by hitting it. `pkg/task` re-exports the names so existing callers read naturally.

**Invariants**:
- Present only when a task is genuinely claimed for the current work.
- An empty `TaskID` makes the attribution inert; `AttributionFromContext` reports absence rather than returning a half-populated value.
- Absence is the **normal** case — not every turn runs under a task — and is persisted as `NULL`, never as an empty string, and never as an error.

### TimelineSource (`pkg/task/timeline.go`)

**Responsibility**: Project one existing table into `TimelineEvent`s for a task.

Three implementations, each living with the table it reads:

| Source | Table | Projects |
|---|---|---|
| `SessionStore.TimelineEvents` (`pkg/agent/session_timeline.go`) | `messages` | tool calls (from `tool_calls_json`), tool results with success/error/duration (from `tool_result_json`), assistant and user text |
| `HITLTimelineSource` (`pkg/agent/hitl_timeline.go`) | `human_requests` | the question, and — only once resolved — the outcome |
| `HistorySource` (`pkg/task/timeline_history.go`) | `task_history` | lifecycle transitions; needed no schema change at all |

**Contract**: an unknown task returns an empty slice, never an error. This is what lets `TimelineReader` distinguish "nothing happened" from "the source failed".

### TimelineReader (`pkg/task/timeline.go`)

**Responsibility**: Query sources concurrently, merge, sort, filter, bound.

**Key design decisions**:
- Sources are queried in parallel: they hit different tables and none depends on another, so a read costs the slowest source rather than their sum.
- A failing source is reported in `PartialSources`, not returned as an error. A timeline missing its HITL rows is still worth showing — but the reader must know it is incomplete.
- Reads are capped (`DefaultTimelineLimit` 500, `MaxTimelineLimit` 2000). A long-running task's history is unbounded, so an uncapped read is never performed.

---

## Key Interactions

### Write path — the whole mechanism

```
Agent claims task        SessionStore.SaveMessage        messages
      │                          │                          │
      ├─ ContextWithAttribution ─┤                          │
      │                          ├─ msg.TaskID, else        │
      │                          │  taskctx.TaskIDFrom(ctx) │
      │                          ├─ INSERT ... task_id ────▶│
      │                          │   (the SAME insert that  │
      │                          │    already happened)     │
```

**Properties**: one additional bound parameter. No new statement, no new transaction, no new goroutine, no buffering, no possibility of loss beyond that of the insert itself.

### Read path

```
UI            TimelineReader        messages   human_requests   task_history
 │                  │                   │            │              │
 ├─ Read(task) ────▶│                   │            │              │
 │                  ├── concurrent ────▶│            │              │
 │                  ├───────────────────┼───────────▶│              │
 │                  ├───────────────────┼────────────┼─────────────▶│
 │                  │◀── events ────────┴────────────┴──────────────┤
 │                  ├─ filter (kind, window)                        │
 │                  ├─ sort (time, then source, then source id)     │
 │                  ├─ bound (limit, oldest or newest)              │
 │◀── {Events, Truncated, TotalMatched, PartialSources} ────────────┤
```

### Reconstructing a tool call

A single tool call is recovered from two message rows, correlated by `tool_use_id` — the same correlation the Anthropic and Bedrock APIs already require, so it is guaranteed present:

```
assistant message           tool message
├─ tool_calls_json          ├─ tool_use_id ────┐ correlates
│  └─ [{ID, Name, Input}]   │                  │
│         │                 ├─ tool_result_json│
│         └─ TOOL_CALL      │  └─ shuttle.Result{Success, Error, ExecutionTimeMs}
│            name + input   │         │
│                           │         └─ TOOL_RESULT
│                           │            outcome + duration + payload
```

Duration and success come free: they were already inside the stored `shuttle.Result`. The rejected design would have re-recorded both.

---

## Data Structures

### TimelineEvent

**Invariants**:
- `Summary` is non-empty and stands alone — the collapsed row renders without `Detail`.
- `Detail` is the **full** payload as stored. This model does not truncate, because the source did not. `Excerpt(maxBytes)` is available for display and cuts on a UTF-8 boundary.
- `Success` is a `*bool`: "not applicable", "succeeded", and "failed" are three states and a `bool` expresses two.
- `SourceTable` and `SourceID` are always set, so every fact is traceable to the row that holds it.
- A pending HITL request yields a request event and **no** response event. A timeline implying a human answered when they have not is worse than a gap.

### Ordering

Ties are constant, not exceptional: `messages.timestamp` is second-resolution, and a lifecycle transition usually lands in the same second as the message that caused it. Order falls back to `SourceTable` then `SourceID`, giving a total order that is stable across reads and across processes.

---

## Design Trade-offs

### Decision 1: A read model rather than an event log

**Chosen**: Query the existing durable tables. Store nothing new.

**Rationale**: See [The Rejected Design](#the-rejected-design-and-why). The facts were already persisted in full; the gap was a join key.

**Consequences**: the timeline can only show what some writer already records. That is a feature — it forces new facts to be recorded where they belong rather than in a parallel log — but it means a genuinely unrecorded event (see [Constraints](#constraints-and-limitations)) needs its own writer, not a timeline change.

### Decision 2: Column ownership follows table ownership, not the migrator

**Chosen**: `messages.task_id` is added by `pkg/agent/session_store.go`; `human_requests.task_id` by `pkg/shuttle/human_store_sqlite.go`; only `tasks.created_via` is a migrator migration.

**Rationale**: forced by two facts discovered while testing. First, `messages` and `human_requests` are created by migration 000001, and the migrator **baselines** 000001 on a pre-migration database — stamping it applied without executing it (see `TestBootstrap_PreMigrationDB`). On that path the tables do not exist, so an `ALTER` from a later migration fails the whole migration chain. Second, `messages` has a *second* schema owner in `session_store.go`, and two `ALTER`s adding the same column collide, because SQLite has no `ADD COLUMN IF NOT EXISTS`.

Both stores already use a pragma-guarded idempotent pattern for exactly this (`agent_id`, `session_context`, `tool_use_id`). `tasks` is safe in the migrator because it comes from 000003, which is never baselined.

**Alternatives considered**:
- *All three in one migration*: rejected — it fails on the bootstrap path and collides with the session store. This was attempted and the tests caught it.
- *Go-based migrations in the migrator*: rejected as a much larger change than three columns warrant.

**Consequences**: the schema for these columns is described in two places. Postgres, which has `ADD COLUMN IF NOT EXISTS`, does it in one migration guarded by a table-existence `DO` block.

### Decision 3: Full detail on read, truncation at display

**Chosen**: `Detail` carries the whole stored payload; `Excerpt` truncates on demand.

**Rationale**: the sources are uncapped. Capping in the read model would discard data that exists, to save memory on a path that runs when a human clicks — the wrong trade. The rejected design's 2 KiB storage cap was a write-side concern that does not apply here.

**Consequences**: a task with very large tool results produces a large read. Bounded by `Limit`, and the caller controls page size.

### Decision 4: Report partial failure instead of failing the read

**Chosen**: a failing source lands in `PartialSources`; the read succeeds with what the others returned.

**Rationale**: a timeline missing its approvals is still useful. But because a healthy source legitimately returns empty, an unreported failure would be indistinguishable from "nothing happened" — which would let a UI assert no human was ever asked when in fact the lookup broke.

---

## Performance Characteristics

Measured on Apple M4 Pro against real SQLite through the project's default CGO driver (go-sqlcipher, SQLite 3.33.0), `-count=3`.

### Write side

**No new write.** `task_id` is one additional bound parameter on `INSERT INTO messages` and `INSERT INTO human_requests`, both of which are single statements that already execute. `tasks.created_via` is set on a row already being written. There is no new statement, transaction, goroutine, buffer, or flush.

This is the entire performance story of the write path, and it is why the rejected design's 865 ns emit cost and 1.4 % SQLite writer occupancy are now zero rather than small.

### Read side

Task with **200 tool calls (400 message rows)**, full 400-event timeline:

| | |
|---|---|
| `TimelineReader.Read` | **2.14 ms** (2.139 / 2.145 / 2.227 ms) |
| Allocations | 1.25 MB, 14 970 allocs |

The comparison that decided the design: the rejected table would have paid **~57 µs × 400 = ~22.8 ms** of write work to make this same timeline readable — always, per task, whether or not anyone opened it. The read model pays **2.14 ms**, once, only when someone does.

Allocation count is dominated by JSON unmarshalling per row and could be reduced, but this is a human-triggered read path, not a hot loop, so it has not been optimised.

### Still not measured

- **PostgreSQL.** The migration is written and the projections are storage-agnostic, but no `TimelineEvents` implementation exists for the Postgres message store yet, and nothing has been run against a live Postgres.
- **A task whose messages span many sessions.** The index is `(task_id, timestamp)`, so cost should track the task's own rows rather than the session's, but this has not been measured with a large session behind a small task.

---

## Concurrency Model

**Threading**: `TimelineReader.Read` spawns one goroutine per source and joins with a `sync.WaitGroup`. Each writes only its own slot in a pre-sized results slice, so there is no shared mutable state and no lock.

**Source safety**: `SessionStore.TimelineEvents` takes the store's existing `RLock`. The HITL and history sources hold no state.

**Cancellation**: the caller's context propagates into every source query, so a cancelled read stops all of them.

**Testing**: `go test -tags fts5 -race` across `pkg/task`, `pkg/agent`, `pkg/shuttle`, `pkg/storage`.

---

## Error Handling Philosophy

**Strategy**: distinguish "nothing" from "broken", and never let the two look alike.

| Condition | Behaviour |
|---|---|
| Unknown or empty task ID at a source | empty slice, no error — a task with no activity is normal |
| Empty task ID at `TimelineReader.Read` | `ErrTimelineTaskIDRequired` — an unscoped read would scan every source in full |
| One source fails | listed in `PartialSources`; the read succeeds |
| Malformed `tool_calls_json` on one row | that row yields no tool events but its text still appears; the read succeeds. One corrupt row must not blank the view |
| Missing `tool_result_json` | the event is emitted with `Success == nil` — outcome unknown, not assumed |

---

## Constraints and Limitations

### The timeline can only show what something already records

**Description**: There is no place to put an event that no writer persists.

**Known cases**: skill activation (only inferable from `skill_idempotency_key`), workflow stage boundaries (partially covered — the task-tracked orchestrator creates a task per stage), and an ephemeral agent's report to its spawner (probably in `messages` via `agent_id`, **not yet verified**).

**Correct fix**: give the fact a writer in the table where it belongs, then add a projection. Not a parallel log.

### Second-resolution timestamps

**Description**: `messages.timestamp` is Unix seconds, so many events share a timestamp and ordering within a second depends on the tie-break rather than real time.

**Impact**: two tool calls in the same second may render in `SourceID` order rather than execution order — which for `messages` is insertion order, so it is usually right. Sub-second ordering would need a column change on `messages`.

### No streaming

**Description**: there is no push. A UI polls.

**Rationale**: the sources are already durable and consistent; a stream would be new infrastructure for a view that a human opens occasionally.

### Migrations cannot drop columns

**Description**: the CGO build links go-sqlcipher, which bundles SQLite 3.33.0. `ALTER TABLE ... DROP COLUMN` arrived in 3.35.0, so a rollback that drops a column fails with `near "DROP": syntax error`. The pure-Go fallback driver ships 3.53 and would accept it; a migration must work on both.

**Impact**: repo-wide, not specific to this component — migrations 000005, 000006, and 000008 already encode the convention. `tasks.created_via` is left in place on rollback; it is `NOT NULL DEFAULT ''` with no index, so it is inert.

### `tool_executions` is a dead table

**Description**: `tool_executions` (`tool_name`, `input_json`, `result_json`, `error`, `execution_time_ms`) has existed since migration 000001 and **nothing writes to it**. Its shape is almost exactly what a tool-event log would want, which is part of why the rejected design's duplication was not obvious sooner.

**Recommendation**: either give it a writer and project it, or drop it. Leaving it is schema debt that actively misleads.

---

## Implementation Status

| Piece | Status |
|---|---|
| `pkg/taskctx` attribution (leaf package) | ✅ Implemented |
| `messages.task_id` — schema, migration, stamping, read-back | ✅ Implemented |
| `human_requests.task_id` — schema, migration, stamping | ✅ Implemented |
| `tasks.created_via` (SQLite 000009, Postgres 000014) | ✅ Migration written |
| `TimelineEvent` / `TimelineSource` / `TimelineReader` | ✅ Implemented |
| `messages` projection incl. tool call/result reconstruction | ✅ Implemented, 5 tests |
| `human_requests` projection | ✅ Implemented, 2 tests |
| `task_history` projection | ✅ Implemented |
| Merge, tie-break stability, filters, limits, partial failure | ✅ 9 tests |
| Read benchmark | ✅ 2.14 ms / 400 events |
| Rejected `task_activity` table, proto, store, recorder | ❌ Deleted |
| Stamping `created_via` at the creation sites | 📋 Planned — column exists, writers not yet updated |
| `ClaimTask` establishing the attribution on the agent's context | 📋 Planned — the timeline is only as good as its stamping |
| Postgres message projection | 📋 Planned |
| `TaskService` RPC exposing the timeline | 📋 Planned — deliberately last, so the proto commits to a validated shape |
| Cloud mirror | 📋 Planned |
| Tera UI reading the timeline | 📋 Planned |
| Verify whether subagent reports are already in `messages` | 📋 Open question |
| Fix progress-channel back-pressure (`server.go:187`) | 📋 Separate, pre-existing |

**Note on sequencing**: the proto is deliberately last. The rejected design committed a 31-field message and three RPCs behind `buf breaking` before a single consumer existed. Proto is a one-way door in this repo; it should follow a validated read model, not precede it.

---

## Related Work

### Command-Query Responsibility Segregation

**Reference**: Young, G. (2010). *CQRS Documents.*

**Relationship**: the timeline is a read model assembled from write-side tables owned by other components. It departs from typical CQRS in having no materialised projection — the read is a live query, because the source tables are small enough per task that materialising would add staleness and storage for no gain.

### Event sourcing

**Reference**: Fowler, M. (2005). *Event Sourcing.*

**Relationship**: deliberately **not** event sourcing, and this is the distinction that killed the rejected design. Task state lives in the `tasks` row; the timeline is a derived view. Building an append-only log alongside an authoritative row gives the costs of event sourcing without its benefit.

### "You aren't gonna need it"

**Relationship**: the rejected design was a well-engineered answer to a question that a `JOIN` already answered. The measurement discipline that validated its performance never asked whether it should exist. Cheap infrastructure beats fast infrastructure.

---

## Further Reading

- [Task System Architecture](./task-system.md) — the task model this attributes to
- [Task Service API Reference](../reference/task-service.md)
- [Task Board User Guide](../guides/task-board.md)
- [Skills Overhaul Architecture](./skills-overhaul.md) — skill task emission
- [Observability Architecture](./observability.md) — the span context pattern `taskctx` follows
