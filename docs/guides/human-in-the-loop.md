
# Human-in-the-Loop Guide

**Version**: v1.3.0

## Table of Contents

- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
  - [Keeping a Held Stream Alive](#keeping-a-held-stream-alive)
  - [Which Progress Event Is an Answerable Card](#which-progress-event-is-an-answerable-card)
- [Common Tasks](#common-tasks)
  - [Request Approval Before Actions](#request-approval-before-actions)
  - [Request User Input](#request-user-input)
  - [Request Code Review](#request-code-review)
  - [Handle Timeouts](#handle-timeouts)
- [Examples](#examples)
  - [Example 1: Database Deletion Approval](#example-1-database-deletion-approval)
  - [Example 2: Multi-Choice Decision](#example-2-multi-choice-decision)
- [Workflow HITL Gates (Durable Checkpoint/Resume)](#workflow-hitl-gates-durable-checkpointresume)
- [Troubleshooting](#troubleshooting)
  - [The Turn Hangs After a Human Approves](#the-turn-hangs-after-a-human-approves)
- [Next Steps](#next-steps)


## Overview

✅ **Available** - Feature fully working with tests (see `pkg/shuttle/human_tool_test.go`, `pkg/shuttle/human_store_sqlite_test.go`)

Request human approval, input, or decision-making during agent execution using the ContactHumanTool.

## Prerequisites

- Loom v1.3.0+
- Agent with the `contact_human` tool enabled

## Quick Start

The ContactHumanTool is included in the builtin tool registry:

```go
import "github.com/teradata-labs/loom/pkg/shuttle/builtin"

tools := builtin.All(promptRegistry)  // Includes contact_human
```

Request human input:

```go
tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{})

result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "Should I delete table 'users'?",
    "request_type": "approval",
    "priority":     "high",
    "timeout_seconds": float64(300),
})

if result.Success {
    response := result.Data.(map[string]interface{})
    if response["status"] == "approved" {
        // Proceed with deletion
    }
}
```

## Configuration

### ContactHumanConfig

```go
type ContactHumanConfig struct {
    Store        HumanRequestStore    // Storage backend (default: in-memory)
    Notifier     Notifier             // Notification mechanism (default: no-op)
    Timeout      time.Duration        // Default timeout (default: 5 minutes)
    PollInterval time.Duration        // Check interval (default: 1 second)
    Tracer       observability.Tracer // Tracer for observability (default: NoOpTracer)
    Logger       *zap.Logger          // Logger for structured logging (default: NoOp logger)
}
```

### Keeping a Held Stream Alive

✅ Available

> **Note:** Added after v1.4.0 — available in the next release, not in v1.4.0
> itself.

A hold blocks the turn until a human answers, and emits nothing in between. A
streaming caller therefore sees no bytes for the whole window — up to 300s for
an approval hold, up to your configured `Timeout` for `contact_human` — which
can be long enough for a load balancer or proxy to trip its idle timeout and
drop the stream the decision has to come back on.

Under `looms serve` this is handled for you: every hold emits a keep-alive on
the progress stream every 30s, with no configuration.

When you supply your own `Notifier`, opt in by also implementing `Heartbeater`:

```go
type webhookNotifier struct{ url string }

func (n *webhookNotifier) Notify(ctx context.Context, req *shuttle.HumanRequest) error {
    return postJSON(ctx, n.url, req) // the card a human acts on
}

// Heartbeat is called every 30s while the request is still pending.
func (n *webhookNotifier) Heartbeat(ctx context.Context) error {
    return pingKeepAlive(ctx, n.url) // no payload — just traffic
}
```

Both hold types — an `ask` admission verdict and `contact_human` — use it on the
same terms:

- **Opt in, or nothing changes.** A `Notifier` without `Heartbeat` is never
  called and behaves exactly as before.
- **Never affects the outcome.** An error returned from `Heartbeat` is ignored,
  and a heartbeat never delays a decision.
- **Keep it quick and non-blocking.** `Heartbeat` is called from the waiting
  turn. An implementation that blocks stalls the hold, so use a timeout and
  return — never wait on a slow endpoint.

### Which Progress Event Is an Answerable Card

✅ Available

If you are building a client that renders HITL prompts, gate on a **non-empty
`hitl_request.request_id`**. That id is what
`AnswerClarificationQuestion` submits against. Three shapes arrive on
`EXECUTION_STAGE_HUMAN_IN_THE_LOOP` and only one is answerable:

| `hitl_request` | `request_id` | What it is | Show a prompt? |
|----------------|--------------|------------|----------------|
| absent | — | Keep-alive; carries no state and may be dropped under load | No |
| present | empty | Early "a hold is coming" hint, sent before the request is stored | No |
| present | non-empty | The answerable card | Yes |

```go
// Correct: only an id-bearing event can accept an answer.
if p.GetStage() == loomv1.ExecutionStage_EXECUTION_STAGE_HUMAN_IN_THE_LOOP &&
    p.GetHitlRequest().GetRequestId() != "" {
    showPrompt(p.GetHitlRequest())
}
```

Prompting on either of the other two gives the human a box with no id to submit
against: the answer goes nowhere and the hold sits pending until it times out.
Loom's TUI gates on the id; a client written against `hitl_request != nil` needs
updating.

### Request Types

| Type | Description | Use Case |
|------|-------------|----------|
| `approval` | Yes/no decision | "Delete this data?" |
| `decision` | Choose between options | "Which approach: A, B, or C?" |
| `input` | Request information | "What email address?" |
| `review` | Quality check | "Review this code" |

### Priority Levels

| Priority | Suggested Timeout | Description |
|----------|-------------------|-------------|
| `low` | 24+ hours | Non-urgent |
| `normal` | 1-4 hours | Standard |
| `high` | 15-60 minutes | Needs attention |
| `critical` | 5-15 minutes | Urgent |

## Common Tasks

### Request Approval Before Actions

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "Delete all test data from the users table?",
    "request_type": "approval",
    "priority":     "high",
    "context": map[string]interface{}{
        "table_name": "users",
        "row_count":  1000000,
    },
    "timeout_seconds": float64(300),
})

if !result.Success {
    if result.Error.Code == "TIMEOUT" {
        return fmt.Errorf("approval timed out")
    }
    return fmt.Errorf("approval failed: %s", result.Error.Message)
}

response := result.Data.(map[string]interface{})
if response["status"] != "approved" {
    return fmt.Errorf("rejected by %s", response["responded_by"])
}

// Proceed with approved action
```

### Request User Input

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "What is the customer's preferred contact method?",
    "request_type": "input",
    "priority":     "normal",
    "context": map[string]interface{}{
        "customer_id": "CUST-12345",
        "options":     []string{"email", "phone", "SMS"},
    },
})

if result.Success {
    response := result.Data.(map[string]interface{})
    contactMethod := response["response"].(string)
}
```

### Request Code Review

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":     "Review this SQL query before execution",
    "request_type": "review",
    "priority":     "high",
    "context": map[string]interface{}{
        "query":          "DELETE FROM orders WHERE created_at < '2023-01-01'",
        "estimated_rows": 10000,
    },
    "timeout_seconds": float64(1800),  // 30 minutes for review — also raise ContactHumanConfig.Timeout: the per-request value is capped at the configured maximum
})
```

### Handle Timeouts

```go
result, err := tool.Execute(ctx, map[string]interface{}{
    "question":        "Approve this transaction?",
    "request_type":    "approval",
    "timeout_seconds": float64(300),
})

if !result.Success && result.Error.Code == "TIMEOUT" {
    // Human didn't respond in time
    // Default to safe action (cancel operation)
    log.Warn("Human approval timed out, canceling operation")
    return nil
}
```

## Examples

### Example 1: Database Deletion Approval

```go
func deleteTableWithApproval(ctx context.Context, tableName string, rowCount int) error {
    tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
        Timeout: 10 * time.Minute,
    })

    priority := "normal"
    if rowCount > 1000000 {
        priority = "high"
    }

    result, err := tool.Execute(ctx, map[string]interface{}{
        "question": fmt.Sprintf("Delete table '%s' with %d rows?", tableName, rowCount),
        "request_type": "approval",
        "priority": priority,
        "context": map[string]interface{}{
            "table":     tableName,
            "row_count": rowCount,
            "operation": "DROP TABLE",
        },
    })
    if err != nil {
        return err
    }

    if !result.Success {
        return fmt.Errorf("approval request failed")
    }

    response := result.Data.(map[string]interface{})
    if response["status"] == "approved" {
        return executeDeleteTable(tableName)
    }

    return fmt.Errorf("deletion rejected by %s", response["responded_by"])
}
```

### Example 2: Multi-Choice Decision

```go
func selectDatabaseWithHuman(ctx context.Context, workload string) (string, error) {
    tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{})

    result, err := tool.Execute(ctx, map[string]interface{}{
        "question":     "Which database should I use for this workload?",
        "request_type": "decision",
        "priority":     "normal",
        "context": map[string]interface{}{
            "workload_type":  workload,
            "options":        []string{"PostgreSQL", "Teradata", "BigQuery"},
            "recommendation": "Teradata for OLAP workloads",
        },
        "timeout_seconds": float64(3600), // capped at ContactHumanConfig.Timeout — raise both together
    })
    if err != nil {
        return "", err
    }

    if !result.Success {
        return "", fmt.Errorf("decision request failed")
    }

    response := result.Data.(map[string]interface{})
    return response["response"].(string), nil
}
```

## Workflow HITL Gates (Durable Checkpoint/Resume)

✅ **Available** - Feature fully working with tests (see `pkg/orchestration/hitl_gate_test.go`)

`contact_human` blocks an agent *mid-turn*. Workflow **HITL gates** are the
complementary mechanism for pipelines: a declarative approval gate on a
**stage boundary** that fires deterministically — no LLM decides whether to
ask. Because gates only fire between stages, the workflow suspends into a
small, durable `WorkflowCheckpoint` (proto) instead of a blocked goroutine:
the process can exit, and any process holding the checkpoint can resume the
run later.

### Declaring a gate

Gates are supported on `pipeline` and `iterative` patterns only:

```yaml
spec:
  type: pipeline
  initial_prompt: "Create a partitioned orders table in the sales database"
  stages:
    - agent_id: ddl-designer
      prompt_template: "Design Teradata DDL for: {{previous}}"
      hitl_gate:
        prompt_template: "Review this DDL before execution:\n{{output}}"
        request_type: approval          # approval | decision | input | review
        timeout_seconds: 1800           # advisory; hosts enforce expiry in suspend mode
        revise_target_stage_id: ddl-designer  # stage to restart on REVISE (default: this stage)
        max_revisions: 3                # revision budget before the run fails
        on_timeout: fail                # fail | reject | approve
    - agent_id: ddl-executor
      prompt_template: "Execute exactly this DDL: {{previous}}"
```

### Suspend / resume lifecycle

1. The gated stage completes (including its output validation/retries).
2. The executor emits a progress event carrying `HITLGateRequest`, then —
   with no inline handler configured — returns a `*WorkflowSuspended` error
   wrapping the checkpoint. Detect it with `errors.As`.
3. The host persists the checkpoint and shows `pending_gate` to a human.
4. `Orchestrator.ResumeWorkflow(ctx, pattern, checkpoint, decision)`:
   - **APPROVE** — continues at the next stage.
   - **REVISE** — jumps back to `revise_target_stage_id` with the feedback
     threaded into that stage's prompt (`{{revision_feedback}}` placeholder,
     or an appended `## REVISION FEEDBACK` section). The gate fires again
     after the re-run; `max_revisions` bounds the loop.
   - **REJECT** — returns `*GateRejected` without executing anything.
5. The checkpoint records a SHA-256 fingerprint of the workflow definition;
   resuming with a modified definition is refused (`fingerprint … changed
   since suspension`) so edits can never bypass a pending review.

### CLI

```bash
# Non-interactive (or with --suspend-to): a gate writes a checkpoint and exits
looms workflow run create-table.yaml --suspend-to review.pb

# Review, then resume with exactly one decision
looms workflow resume create-table.yaml review.pb --approve
looms workflow resume create-table.yaml review.pb --revise "Use MULTISET and add COMPRESS"
looms workflow resume create-table.yaml review.pb --reject

# On an interactive terminal, `run` prompts inline instead ([a]/[r]/[j]/[s])
looms workflow run create-table.yaml
```

### Embedding hosts (inline decisions)

A host that wants to decide gates in-process (chat bridge, terminal, tests)
sets an `orchestration.HITLHandler` in `orchestration.Config`; returning
`orchestration.ErrSuspendWorkflow` from the handler converts the gate into a
durable suspension. Leave the handler nil to always suspend.

### What a checkpoint contains (and does not)

The checkpoint holds completed stage outputs, cost history, iteration and
revision counters, the pending gate request, and agent-written
workflow-namespace SharedMemory entries. It never contains in-flight
agent-loop state — gates only fire at stage boundaries. Checkpoints are host
state: persist them server-side, not on untrusted clients.

See `examples/reference/workflows/orchestration-patterns/hitl-gated-pipeline.yaml`
for a runnable example.

## Troubleshooting

### Request Times Out Immediately

Increase timeout:

```go
tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
    Timeout: 30 * time.Minute,
})

// Or per-request
result, _ := tool.Execute(ctx, map[string]interface{}{
    "question":        "...",
    "timeout_seconds": float64(1800),
})
```

### No Notification Received

Under `looms serve` the progress-stream bridge is wired for you. When embedding
the tool directly the default notifier is no-op — configure one:

```go
notifier := shuttle.NewJSONNotifier("https://myapp.com/webhook/hitl")
tool := shuttle.NewContactHumanTool(shuttle.ContactHumanConfig{
    Notifier: notifier,
})
```

### The Turn Hangs After a Human Approves

Two causes, both fixed but worth recognising in an older client or a custom
consumer:

1. **The stream was dropped during the hold.** A hold that emits nothing for its
   whole window can trip a proxy or load balancer idle timeout, so the approval
   comes back to a stream that no longer exists. Use a notifier that implements
   [`Heartbeater`](#keeping-a-held-stream-alive) — `looms serve` already does.
2. **The prompt had no request id.** A client that shows its dialog for any
   HITL-stage event also shows one for a keep-alive or the early hint, and that
   dialog has no id to submit against — the human answers, the answer goes
   nowhere, and the hold times out. Gate on a non-empty
   `hitl_request.request_id`; see
   [which one is answerable](#which-progress-event-is-an-answerable-card).

### Request Not Found After Restart

The default in-memory store loses data on restart. For persistence, use the SQLite store (`SQLiteHumanRequestStore`), which is the default when running `looms serve`. The server initializes it automatically at `$LOOM_DATA_DIR/hitl.db`:

```go
store, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{
    Path:   "/path/to/hitl.db",
    Tracer: tracer,
})
```

You can also manage requests via the CLI:

```bash
# List all pending requests
looms hitl list

# List requests for a specific session
looms hitl list --session sess-123

# Show details of a specific request
looms hitl show req-abc123

# Approve a request
looms hitl respond req-abc123 --status approved --message "Yes, proceed"

# Reject a request
looms hitl respond req-abc123 --status rejected --message "No, do not proceed"
```

### Multiple Responses Rejected

Only the first response is accepted. `RespondToRequest` (available on both `InMemoryHumanRequestStore` and `SQLiteHumanRequestStore`) returns an error if the request has already been responded to:

```go
// Using the concrete SQLiteHumanRequestStore (or InMemoryHumanRequestStore)
req, _ := store.Get(ctx, requestID)
if req.Status != "pending" {
    return fmt.Errorf("already responded: status=%s", req.Status)
}

err := store.RespondToRequest(ctx, requestID, "approved", "Yes", "alice", nil)
if err != nil {
    // Returns error if request not found or already responded
    return err
}
```

## Next Steps

- [Agent Configuration Reference](/docs/reference/agent-configuration.md) - Configure agents with `contact_human` in the builtin tools list
- [Observability Guide](/docs/guides/integration/observability.md) - Trace HITL requests with Hawk
- [Streaming Reference](/docs/reference/streaming.md) - Stream HITL events to clients
