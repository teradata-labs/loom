---
title: "Task Admin Observability"
weight: 16
---

# Task Admin Observability

**Target Audience**: Architects, operators, advanced developers
**Status**: 📋 Planned — nothing in this document is implemented yet.

An operator needs to answer a different question from the one a user asks. A user asks *"what happened on my task?"* — answered by the [task timeline](task-timeline.md), which is a read model over durable per-task records. An operator asks *"is task recording working, for everyone?"* That question is fleet-shaped, and the timeline cannot answer it: it reads one task at a time, under one tenant's row-level security.

This document records how to answer the operator's question, and why the obvious approach does not work.

## The obvious design, and why it fails

The task subsystem already emits counters (`pkg/task/implicit.go`):

| Metric | Labels |
|---|---|
| `task.implicit.created` | `trigger` |
| `task.implicit.skipped` | `reason`: `session_cap`, `board_unavailable`, `create_failed`, `close_failed` |
| `task.implicit.closed` | — |

Those labels are exactly the operator's questions. So the natural design is a dashboard over the metrics plane — cheap, aggregate, no tenant data touched.

**It does not work, because there is no metrics plane.** `Tracer.RecordMetric` has four implementations:

| Implementation | `RecordMetric` behavior |
|---|---|
| `NoOpTracer` | empty method — discards |
| `OTelTracer` | **empty method — discards silently** |
| `EmbeddedTracer` | `logger.Debug` only — not queryable |
| `HawkTracer` | converts each metric into a **span** named `metric.<name>` |

Three of four discard. The fourth turns each counter into a point-in-time span rather than a time series, and Hawk is an optional dependency — so a dashboard built this way shows nothing in most deployments and requires span aggregation in the rest.

**A dashboard over metrics that reach nothing is not a design.** The counters are still worth keeping — they cost nothing and a real sink can be added later — but they cannot be the source.

## The second obvious design, and why it also fails

If the metrics are unusable, query the tasks. This is worse.

Cross-tenant task queries are the most expensive shape in the schema, measured at 1M rows:

| Query | Cost |
|---|---|
| Unscoped list, page one | 446 ms |
| Its `COUNT(*)` half | 262 ms |

Crucially, indexing the tenant predicate **does not help these** — an unscoped query has no tenant predicate to index. And a multi-tenant deployment's data layer is typically row-level-security scoped with no bypass for the runtime role, so a cross-tenant read needs a deliberate, reviewed escape hatch. That is a security decision, not a query-tuning one.

## The design: a rollup

Aggregate at write time, into a table whose row count grows with **time and cardinality**, not with task volume.

```
task_activity_rollup
  day            date        -- UTC bucket
  created_via    text        -- implicit | skill | agent | workflow
  trigger        text        -- tool_call | human_request | subagent_spawn | ...
  outcome        text        -- created | closed | skipped:<reason>
  count          bigint
  PRIMARY KEY (day, created_via, trigger, outcome)
```

**Written** with one `INSERT ... ON CONFLICT DO UPDATE SET count = count + 1` on the paths that already write: task mint, task close, and the emitter's skip branches. One extra statement on a path already inside a transaction.

**Read** by admins directly. No tenant predicate is needed because no row belongs to a tenant — the aggregate carries no user, session, or task identity, which is what makes it safe to read across the fleet without an RLS bypass.

**Bounded by construction.** Roughly `days × created_via × trigger × outcome` rows. With four provenances, five triggers and about eight outcomes that is a few hundred rows per day at most, and only for combinations that actually occur.

### Why not derive it on read

Because that is the second failed design. A nightly aggregation job over the task table would pay the unscoped-scan cost repeatedly for the same answer, and would still need the cross-tenant escape hatch. Writing forward costs one statement per event and needs neither.

### What it deliberately cannot answer

Being identity-free is the property that makes the rollup safe, and it is also its limit. The rollup cannot answer *"what is user X doing"* or *"show me the failing task."* That is correct: those are tenant questions and belong behind tenant scope.

For drill-down, use a **single-session lookup by ID**, which is tenant-scoped and therefore cheap — the session-scoped query measured 1.6 ms at 1M rows where the unscoped list measured 446 ms. An operator holding a session ID from a support ticket gets the existing timeline; nobody gets a fleet-wide list of other people's tasks.

## Panel

One route, `admin/tasks`, with three regions:

1. **Is recording working?** Created versus closed per day. A widening gap means tasks are opening and not closing — the failure that matters most, because it silently accumulates in-progress rows on user boards.
2. **What is being declined, and why?** Skips broken out by reason. `session_cap` is a tuning signal; `board_unavailable` and `create_failed` are defects.
3. **What is driving recording?** Counts by trigger and provenance — which shows whether skills, workflows, or plain tool calls dominate.

Plus a session-ID lookup that opens the existing per-task timeline.

Note that a host application may have no admin task surface to extend. Check before assuming one: a browser-local "recent items" list is a convenience feature, not an observability plane, and is not a precedent to build on.

## Authorization

- The rollup carries no tenant identity, so admin reads need no RLS bypass. This is the design's main security property and must not be eroded by adding a `user_id` column "for filtering".
- Drill-down is by session ID under normal tenant scope. An operator who cannot read a session today does not gain the ability here.
- No endpoint returns a list of tasks across tenants. If that is ever required, it is a separate change with its own review — not an extension of this one.

## What is not being built

- **Cross-tenant task listing.** The expensive shape, and the one needing an RLS bypass.
- **A read-time aggregation job.** Pays the unscoped scan repeatedly for the same answer.
- **Per-user retention or purging.** Rejected on measurement: observed rate is 1–2 implicit tasks per session at 831 bytes per row, so a heavy user needs roughly 15 years to reach a row count that reads in 0.12 ms. The user-facing fix is defaulting the board to active plus recent.
- **A metrics-backed dashboard**, until a sink exists that actually stores metrics.

## Prerequisites

This surface is worth building only once the read path underneath it is sound.
The tenant-scoping prerequisite has since been addressed in the deployment that
owns those tables; the two remaining items are framework-side.

1. ✅ **Tenant predicate made indexable.** A scoped board read cost time
   proportional to the whole table rather than to the caller's own rows, because
   the isolation predicate compared a `uuid` column through a `text` cast and so
   could not use its index. Fixed, with a composite index matching the shipped
   sort order. Measured on synthetic data at 1M rows: 2,360 ms → 0.16 ms for a
   caller owning 194 rows, and 0.12 ms for one owning 194,000 — which is the
   result that shows the table never had a row-count problem.
2. 📋 Give `buildTaskContext` the board fallback its proto already promises.
   Unset, it issues four unscoped queries before every model call.
3. 📋 Add `, id` to the ORDER BY in both task stores. Both sort on non-unique
   keys with offset pagination on top; measured page overlap is two rows
   returned on two adjacent pages, meaning two others returned on neither.
