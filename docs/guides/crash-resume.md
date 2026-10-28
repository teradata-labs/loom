# Crash Resume Guide

> **Note:** Unreleased. This lands in the first release after v1.4.0.

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [What Resume Does](#what-resume-does)
- [Common Tasks](#common-tasks)
- [Examples](#examples)
- [Behavior Changes When Enabled](#behavior-changes-when-enabled)
- [Limitations](#limitations)
- [Troubleshooting](#troubleshooting)
- [Next Steps](#next-steps)

## Overview

✅ **Available** - tests in `pkg/agent/resume_session_recorded_test.go`,
`pkg/agent/session_lease_test.go`, `pkg/server/session_resume_test.go`,
`pkg/storage/sqlite/event_log_store_test.go` and
`pkg/storage/postgres/event_log_store_test.go`

When `looms serve` crashes in the middle of an agent's turn, the turn is left
unfinished: some tool calls may have run, others not, and the user has no
reply. With the execution log enabled, `loom sessions resume` finishes that
turn without running a tool call a second time when its result was recorded.

## Prerequisites

- `looms serve` with `execution_log.enabled: true` **while the turn ran**.
  Turns that ran with it off cannot be resumed.
- A storage backend with the `execution_events` migration: SQLite migration
  000011 or Postgres migration 000026 (`storage.migration.auto_migrate`
  applies them on startup).
- The `loom` client, pointed at the server (`--server`, plus `--tls` options
  and `loom login` if your server uses them).

## Quick Start

1. Enable the log in `$LOOM_DATA_DIR/looms.yaml`:

   ```yaml
   execution_log:
     enabled: true
   ```

2. Restart the server. The startup log shows:

   ```
   Execution log enabled: agents record write-ahead events for crash resume
   ```

3. After a crash and restart, finish the interrupted turn:

   ```bash
   loom sessions resume sess_abc123def456
   ```

   Expected output:

   ```
   Resumed session sess_abc123def456.

   <the turn's reply>
   ```

## What Resume Does

While the log is on, each agent built by `looms serve` records an event before
and after every LLM call and every tool call. On resume, the server looks at
the session's last turn:

| State of the turn | What resume does |
|-------------------|------------------|
| The user's message has no reply and no tool calls were saved | Re-runs the turn's LLM call |
| A tool call's result was recorded but its row was not saved | Saves the recorded result; the tool does not run again |
| A tool call was never started | Runs it |
| Every tool call has a result, but there is no reply | Asks the LLM for the reply |
| A tool call started but its result was never recorded | Stops and reports an **execution gap** (see below) |
| The turn already has its reply | Nothing (`Nothing to resume`) |

An execution gap means the process died while that call was running, or just
after it ran but before its result was recorded. The two look the same, so
resume does not guess: retrying could repeat a side effect (an email, a
payment, a write), and skipping could lose one.

## Common Tasks

### Resume a Session After a Crash

```bash
loom sessions resume sess_abc123def456
```

The command waits up to 10 minutes for the resumed turn. Raise it for long
tool calls:

```bash
loom sessions resume sess_abc123def456 --timeout 30m
```

The same operation is available over gRPC (`LoomService.ResumeSession`) and
HTTP (`POST /v1/sessions/{session_id}:resume`).

### Settle an Execution Gap

1. Run resume. It names the call:

   ```
   Execution gap: tool "send_email" (call toolu_01, step 4, position 1) started before the crash, but its result was never recorded, so its outcome is unknown.
   Check what the call did, then record it:
     loom sessions resolve-gap sess_abc123def456 --outcome succeeded --result '<json>'
     loom sessions resolve-gap sess_abc123def456 --outcome failed
   and run 'loom sessions resume sess_abc123def456' again.
   ```

2. Check the tool's effect yourself (was the email sent?).
3. Record what you found. If the call took effect, pass its result as JSON:

   ```bash
   loom sessions resolve-gap sess_abc123def456 --outcome succeeded --result '{"message_id": "m-123"}'
   ```

   If it did not:

   ```bash
   loom sessions resolve-gap sess_abc123def456 --outcome failed
   ```

   Expected output:

   ```
   Recorded: tool "send_email" (call toolu_01, step 4, position 1) failed.
   Run 'loom sessions resume sess_abc123def456' to continue the turn.
   ```

4. Run `loom sessions resume` again. The LLM sees the result you recorded and
   decides what to do next (for `failed`, it can call the tool again itself).

## Examples

### Example 1: Crash After a Database Write

A turn asks `run_sql` to insert rows, the insert commits and its result is
recorded, then the server is killed before the tool row is saved. After
restart:

```bash
loom sessions resume sess_orders_42
```

```
Resumed session sess_orders_42.

Inserted 3 rows into orders. ...
```

`run_sql` did not run again: its recorded result was saved to the session and
the turn continued from there.

### Example 2: Crash During a Payment Call

The server dies while `charge_card` is running. Resume cannot know whether
the charge went through:

```bash
loom sessions resume sess_checkout_7
```

```
Execution gap: tool "charge_card" (call toolu_9, step 2, position 0) started before the crash, but its result was never recorded, so its outcome is unknown.
...
```

The operator checks the payment provider, finds the charge, and records it:

```bash
loom sessions resolve-gap sess_checkout_7 --outcome succeeded --result '{"charge_id": "ch_123", "status": "succeeded"}'
loom sessions resume sess_checkout_7
```

## Behavior Changes When Enabled

- **One driver per session.** A turn, a resume, or a gap resolution holds a
  lease on its session, renewed while it runs (default TTL 2 minutes). A
  second request on the same session while the lease is held fails with
  `session is being driven by another process` instead of running alongside
  it. A server that crashed while holding a lease blocks the session until the
  lease expires.
- **Busy and lost-lease errors are gRPC `ABORTED`.** `Weave`, `StreamWeave`,
  `ResumeSession` and `ResolveExecutionGap` all report them as `ABORTED`, which
  means "retry later". A `Weave` retry that carries the same idempotency key
  runs the turn; the busy rejection is not cached under the key.
- **A lost lease stops the turn.** If a turn cannot renew its lease (database
  unavailable or a renewal call that hangs, or another process took the lease
  over after it expired), the turn stops before
  its next tool call and returns `session lease lost`.
- **Messages between agents wait for a busy session.** In a workflow, a
  worker's reply to its coordinator is delivered by running a turn on the
  coordinator's session, which the user's own turn may hold. The delivery
  retries for about 2.5 minutes (long enough to outlast a lease left by a
  crashed server). Queue messages that still cannot be delivered go back to
  the queue and are offered again, up to the queue's retry limit of 3
  deliveries, after which the queue marks them failed. Broadcast messages have
  no queue behind them: if the session is still busy after the wait, the
  message is dropped and logged at error level.
- **A resume counts as a conversation.** `ResumeSession` waits at the same door
  as `Weave` when `max_active_conversations` is reached, and appears in traces
  as `server.ResumeSession`.
- **More database writes.** Two writes per LLM call and per tool call, plus
  lease writes per turn. Events of finished turns are deleted once the reply is
  saved.

## Limitations

- Turns that ran with the log off are refused, not resumed.
- Calls with an unknown outcome are never retried automatically; an operator
  settles them.
- An LLM call interrupted by the crash is made again on resume, so its tokens
  are paid twice. Recorded LLM responses are not replayed.
- Only agents built by `looms serve` record the log. Ephemeral sub-agents,
  `looms workflow run` agents, and agents embedded through the Go API (unless
  built with `agent.WithEventLog`) do not.
- A resume emits no progress events. The resumed turn's progress callbacks are
  not attached, so a client watching a stream sees only the final response.
- Lease loss is detected by renewal, not enforced on writes. A turn that stalls
  (a paused process, a database call that has not yet timed out) can still
  write for up to a renewal interval after another process could have taken
  its lease. The renewal deadline bounds that window; it does not close it.
- Resume matches a saved tool batch to the log by tool-call id, name and
  input. A provider that reuses the same tool-call ids across responses can
  make two identical batches indistinguishable.
- A parked human-approval request whose batch includes `query_tool_result`
  cannot be resumed after the session is reloaded from storage: the decision is
  refused as not matching the parked batch. This predates crash resume.
- `ResolveExecutionGap` records the result you give as if the tool had returned
  it, and the agent treats it as real tool output. The stored result carries
  `manually_resolved: true` and `resolved_by` (the caller's user ID), so it can
  be told apart from a real execution. Any user who can open the session can
  call it; there is no separate operator role. Do not expose it to users you
  would not trust to set a tool's result.
- Per-user isolation: on Postgres, row-level security keeps each user's events
  and leases separate; on SQLite the store filters by user ID.

## Planned

- 📋 Restrict `ResolveExecutionGap` to an operator role, using a role claim in
  the caller's JWT. This needs a decision on whether operators may act on other
  users' sessions (today every call is scoped to the session owner's user ID),
  and a way for `loom sessions resolve-gap` to obtain such a token. Until then
  the access rule above applies.

## Troubleshooting

### `the interrupted turn was not recorded in the execution log`

The turn ran with `execution_log.enabled` off (or before the server
restarted with it on). Resume cannot tell which tool calls already ran. Check
the tools' effects manually and continue the conversation with a new message.

### `tools ran for a batch whose assistant message was not saved`

The log shows tool calls ran, but the session never saved the LLM message
that asked for them, so resume cannot rebuild the turn safely. This applies
wherever the session's last saved message is: an earlier batch may look
finished while a later one ran and was never saved. Asking the LLM again
could run that later batch's tools a second time. Check the tools' effects and
continue with a new message.

### `session is being driven by another process`

Another turn or resume holds the session's lease. Wait for it to finish, or
for the lease to expire (2 minutes) if its server crashed, then retry. Over
gRPC this is `ABORTED`.

### `session lease lost`

The turn could not renew its lease, so it stopped before its next tool call.
Check the database, then run `loom sessions resume` for the session. If a tool
was running when the lease was lost, resume may report an execution gap.

### `Nothing to resume`

The session's last turn already has its reply.

### Server exits with `execution_log.enabled is set, but the storage backend does not provide an execution log`

The configured storage backend has no execution log support. Use the SQLite
or Postgres backend.

## Next Steps

- [CLI reference: loom sessions](../reference/cli.md#loom-sessions)
- [Human-in-the-Loop Guide](human-in-the-loop.md) for turns that wait on a
  person rather than a crash
- [SQLite guidance](../reference/sqlite-guidance.md) for multi-user limits on
  SQLite
