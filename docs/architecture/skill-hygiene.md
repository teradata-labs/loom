---
title: "Skill Hygiene Architecture"
weight: 15
---

# Skill Hygiene Architecture

End-of-turn task-board hygiene enforcement for skill-emitted tasks. Catches three failure modes — orphan `IN_PROGRESS` tasks, `BLOCKED` tasks the agent never surfaced as a question, and `OPEN` tasks the agent created but never started — before the agent returns control to the user.

**Target Audience**: Architects, academics, advanced developers

---

## Design Goals

- **No silent abandonment**: A turn must not end with an active skill's tasks in a misleading intermediate state. The board's status must match the agent's actual intent.
- **Agent-driven repair preferred**: The default policy injects a structured message and lets the LLM resolve violations itself, because mechanical state changes erase the diagnostic signal that an agent left work hanging.
- **Bounded retries**: The repair loop must terminate. After a configured cap, the auditor falls through to deterministic machine repair so the turn always returns.
- **Skill-scoped**: Tasks the agent created directly via `TaskBoardTool` (no `SkillIdempotencyKey`) are out of scope. Hygiene addresses the specific failure mode where a skill activation produces tasks that the skill itself never closes; general agent task discipline is a separate concern.
- **Non-fatal**: Hygiene failures (audit errors, transition errors) are logged and the agent still returns. A broken hygiene path must not block the user response.

**Non-goals**:
- Hygiene is not a general task-board policy engine. It runs once per turn at a single hook point.
- It does not adjudicate dependency violations or WIP-limit overruns — those belong to `task.Manager`.
- It does not modify or replace the human-in-the-loop flow under `ContactHumanTool`; it only spawns HITL requests as a fallback under `AUTO_FIX`.

---

## System Context

```
┌────────────────────────────────────────────────────────────────────┐
│                        Agent Conversation Loop                     │
│                                                                    │
│  [User]                                                            │
│    │                                                               │
│    │ message                                                       │
│    ▼                                                               │
│  ┌────────────────┐   tool calls    ┌────────────────┐             │
│  │  runConversa-  │────────────────▶│   Tool Exec    │             │
│  │  tionLoop      │◀────────────────│                │             │
│  └────────┬───────┘    results      └────────────────┘             │
│           │                                                        │
│           │ text-only response (no tool calls)                     │
│           ▼                                                        │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │              runEndOfTurnHygiene (hook)                      │  │
│  │  ┌─────────────┐    Audit     ┌────────────────────┐         │  │
│  │  │  Auditor    │────────────▶│  Report (Violations)│         │  │
│  │  └─────────────┘             └─────────┬──────────┘          │  │
│  │                                        │                     │  │
│  │                                        ▼                     │  │
│  │  ┌─────────────┐    Enforce  ┌────────────────────┐          │  │
│  │  │  Enforcer   │────────────▶│ EnforcementOutcome │          │  │
│  │  └──────┬──────┘             └─────────┬──────────┘          │  │
│  │         │                              │                     │  │
│  │         │ REQUIRE_FIX            AUTO_FIX │ WARN_ONLY         │  │
│  │         ▼                              ▼                     │  │
│  │  inject user message            transition tasks /           │  │
│  │  → continue loop                spawn HITL / log             │  │
│  └──────────────────────────────────────────────────────────────┘  │
│           │                                                        │
│           │ retry? continue : return                               │
│           ▼                                                        │
│  [Response] ─────────────────────────────────────────▶ [User]      │
│                                                                    │
│  ┌────────────────┐                                                │
│  │ task.Manager   │ ─── persisted state (SQLite/Postgres)          │
│  │  + TaskStore   │                                                │
│  └────────────────┘                                                │
│  ┌────────────────┐                                                │
│  │skills.Orches-  │ ─── active-skill set per session               │
│  │ trator         │                                                │
│  └────────────────┘                                                │
└────────────────────────────────────────────────────────────────────┘
```

**Description**: The auditor runs only when both the skills subsystem and the task subsystem are wired into the agent. It reads the active-skill set from `skills.Orchestrator` and the per-skill task inventory from `task.Manager`. It produces a `Report`; the enforcer applies the configured `HygienePolicy` and may append a synthetic user message to the session (`REQUIRE_FIX`) or mutate task state directly (`AUTO_FIX`).

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│                  pkg/skills/hygiene                              │
│                                                                  │
│  ┌─────────────────┐                                             │
│  │     Auditor     │                                             │
│  │                 │   uses                                      │
│  │ - Audit()       │────▶ (SkillRunLister)    ── task.Manager    │
│  │ - ResolvePolicy │────▶ (ActiveSkillSource) ── skills.Orches.  │
│  │ - Enabled       │                                             │
│  └────────┬────────┘                                             │
│           │ Report                                               │
│           ▼                                                      │
│  ┌─────────────────┐                                             │
│  │    Enforcer     │                                             │
│  │                 │   uses                                      │
│  │ - Enforce()     │────▶ (TaskMutator)       ── task.Manager    │
│  │ - autoFix()     │────▶ (HITLSpawner)       ── (optional)      │
│  │ - applyNote()   │                                             │
│  └────────┬────────┘                                             │
│           │ EnforcementOutcome                                   │
│           ▼                                                      │
│  ┌─────────────────┐                                             │
│  │      Report     │                                             │
│  │                 │                                             │
│  │ - Violations    │                                             │
│  │ - CountByKind() │                                             │
│  │ - FormatTool-   │                                             │
│  │   Message()     │                                             │
│  └─────────────────┘                                             │
└──────────────────────────────────────────────────────────────────┘
```

**Description**: Three types form the hygiene package. `Auditor` reads state and produces a `Report`. `Enforcer` consumes the `Report` and produces an `EnforcementOutcome`. Both depend on small interfaces (`SkillRunLister`, `ActiveSkillSource`, `TaskMutator`, `HITLSpawner`) so tests can substitute stubs without standing up the full storage stack.

---

## Components

### Auditor

**Responsibility**: Inventory the task board for currently-active skills and classify each task into a `ViolationKind` (or "healthy" and ignore it).

**Interface**: `Audit(ctx, sessionID, cfg) → (*Report, error)`. The `cfg` argument is the per-session `HygieneConfig` from `SkillsConfig.Hygiene`. The auditor resolves nil-or-unspecified fields to defaults (enabled, `REQUIRE_FIX`, `max_retries=2`).

**Invariants**:
- Only inspects tasks emitted by currently-active skills, via `task.Manager.ListBySkillRun(skillName, sessionID)`. This in turn matches the `SkillIdempotencyKey` prefix `skill:<name>|sess:<sessionID>|%`.
- Never mutates task state.
- Returns an empty (but non-nil) `Report` when no violations are found.

### Enforcer

**Responsibility**: Apply the resolved `HygienePolicy` to a `Report`. Produces an `EnforcementOutcome` describing what was done; surfaces an `InjectionMessage` when the policy demands the LLM re-attempt.

**Interface**: `Enforce(ctx, *Report, retryCount, maxRetries) → (*EnforcementOutcome, error)`. `retryCount` is supplied by the agent loop; the enforcer is stateless across calls.

**Invariants**:
- `REQUIRE_FIX` never mutates state. It produces an `InjectionMessage` for the caller to append to the session.
- `AUTO_FIX` mutates state and stamps each modified task with a `[hygiene]`-prefixed note in `Task.Notes`, so audit logs can distinguish machine writes from agent writes.
- When `retryCount >= maxRetries` under `REQUIRE_FIX`, the policy is silently downgraded to `AUTO_FIX` so the loop terminates. The outcome records the fallthrough reason.
- HITL spawning is best-effort: when no `HITLSpawner` is wired, `BLOCKED` violations under `AUTO_FIX` are logged and the task is left in `BLOCKED` (still a violation but at least transparent).

### Report

**Responsibility**: A passive value type carrying the list of `Violation` records, the resolved `Policy`, and helpers for formatting (`FormatToolMessage`) and counting (`CountByKind`).

**Invariants**:
- `FormatToolMessage` is deterministic given a fixed set of violations: skills are alphabetically sorted, violation kinds appear in a fixed order (`IN_PROGRESS`, `BLOCKED`, `OPEN`). This matters for golden-test stability and for the agent: a stable message means the LLM's cache-friendly patterns aren't disturbed by hygiene noise.

---

## Key Interactions

### End-of-Turn Audit and Repair Loop

```
Agent           runEndOfTurn-       Auditor          Enforcer        Session       task.Manager
  │             Hygiene                │                │              │              │
  │ no tool        │                   │                │              │              │
  │ calls          │                   │                │              │              │
  ├──────────────▶ │                   │                │              │              │
  │                ├─ Audit ──────────▶│                │              │              │
  │                │                   ├─ ListBySkillRun ──────────────┼─────────────▶│
  │                │                   │◀────tasks──────┼──────────────┼──────────────┤
  │                │◀─── Report ───────┤                │              │              │
  │                ├─ Enforce ─────────────────────────▶│              │              │
  │                │                                    │              │              │
  │     ┌─ REQUIRE_FIX (retries < cap) ──┐              │              │              │
  │                │                     │              │              │              │
  │                │◀── Outcome.ShouldRetry=true ───────┤              │              │
  │                ├─ AddMessage(synthetic user msg) ──────────────────┼──────────────┤
  │                │                                    │              │              │
  │◀── retry=true ─┤                                    │              │              │
  ├──────── continue conversation loop ────────────────────────────────┼──────────────┤
  │                                                                    │              │
  │     └─ AUTO_FIX or fallthrough ─┐                                  │              │
  │                                 │                                  │              │
  │                                 ├─ TransitionTask / UpdateTask ────┼─────────────▶│
  │                                 ├─ SpawnHITL (optional) ───────────┤              │
  │                                 │                                  │              │
  │◀── retry=false ─────────────────┤                                  │              │
  ├── return Response with hygiene_* metadata ──────────────────────────────────────▶ │
```

**Properties**:
- **Synchronous**: every step blocks the turn return. Hygiene runs in the request path because correctness is the goal; latency is a secondary cost.
- **Bounded**: `maxRetries` (default 2) caps `REQUIRE_FIX` retries. Worst case adds two extra LLM turns per skill that left dirty state.
- **Non-fatal errors**: audit or enforce errors log a warning and the agent returns its existing response. The agent never fails a turn because hygiene itself broke.

---

## Data Structures

### Violation

```
type Violation struct {
    SkillName string         // which active skill owns the dirty task
    Kind      ViolationKind  // IN_PROGRESS_ORPHAN | BLOCKED_NO_HITL | OPEN_UNSTARTED
    Task      *task.Task     // the offending task (snapshot)
    Reason    string         // CloseReason or Notes, when present
}
```

**Invariants**: `SkillName` is always the name of a currently-active skill. `Task.SkillIdempotencyKey` always matches the prefix `skill:<SkillName>|sess:<Report.SessionID>|`.

### EnforcementOutcome

```
type EnforcementOutcome struct {
    Policy            loomv1.HygienePolicy
    ViolationsFound   int
    ViolationsByKind  map[string]int
    Resolved          int      // tasks transitioned under AUTO_FIX
    HITLSpawned       int      // BLOCKED tasks turned into HITL
    FallthroughReason string   // populated when REQUIRE_FIX fell through
    InjectionMessage  string   // synthetic user message under REQUIRE_FIX
    ShouldRetry       bool     // caller should re-enter the loop
}
```

**Invariants**: `ShouldRetry == true` implies `InjectionMessage != ""` and `Policy == REQUIRE_FIX`. `FallthroughReason != ""` implies `ShouldRetry == false` and `Policy == REQUIRE_FIX` (the original, not the downgrade).

---

## Algorithms

### Violation Classification

```
classify(task) -> (ViolationKind, ok)
   if task.Status == IN_PROGRESS:    return IN_PROGRESS_ORPHAN, true
   if task.Status == BLOCKED:        return BLOCKED_NO_HITL,    true
   if task.Status == OPEN and task.ClaimedAt == nil:
                                     return OPEN_UNSTARTED,     true
   otherwise:                        return _,                  false
```

**Rationale**:
- `IN_PROGRESS` is always a violation: the lane semantics say "an agent is on this right now," but the turn is ending, so no agent is on it. This is the highest-confidence dirty state.
- `BLOCKED` is conservatively a violation. The auditor cannot inspect the conversation transcript to verify whether the blocking question was surfaced, so it assumes nothing was surfaced. False positives are tolerable; the cost is one extra LLM turn where the agent re-states the question.
- `OPEN` distinguishes "never claimed" from "claimed-then-released." The latter is not a violation: the agent at least tried. The former is the failure mode the original requirement describes — tasks created and silently abandoned.
- `DEFERRED`, `DONE`, `CANCELLED` are healthy terminal states.

**Complexity**: O(active_skills × tasks_per_skill). For typical agent state (1–3 active skills, ~5 tasks each), the audit is a fixed-cost millisecond-range operation dominated by the SQL prefix scan.

### Fallthrough Logic

The retry-with-fallthrough algorithm is:

```
if policy == REQUIRE_FIX and retries >= max_retries:
    downgrade policy to AUTO_FIX
    record fallthrough_reason
```

This ensures the loop terminates even if the LLM is stuck (returning identical text on every retry). Two retries is enough to recover from transient inattention; further loops typically indicate a deeper failure mode that no amount of retry will fix.

---

## Design Trade-offs

### Decision 1: Default to REQUIRE_FIX, not AUTO_FIX

**Chosen Approach**: `REQUIRE_FIX` is the default policy. The auditor injects a structured user message describing the violations; the agent is expected to resolve them with its own tool calls on the next turn.

**Rationale**: Mechanical state changes destroy diagnostic signal. If `IN_PROGRESS` silently becomes `OPEN` without an audit trail, an operator looking at the kanban board can no longer tell that the agent left work hanging. Forcing the agent to fix its own mistakes preserves both the user-visible audit trail and the agent's learning loop — if the same agent leaves dirty state every turn, that pattern is visible in conversation history, not buried in a `[hygiene]` note.

**Alternatives Considered**:
- `AUTO_FIX` default: cheaper (no extra LLM turn) but masks bugs. Rejected for default.
- `WARN_ONLY` default: lowest cost but doesn't actually fix anything. The original failure mode persists; only logging changes. Rejected.

**Consequences**: Worst-case latency adds two LLM turns per dirty-state turn. For agents that never leave dirty state (the expected case), zero overhead. The cap is configurable per agent.

### Decision 2: Skill-scoped, not session-scoped

**Chosen Approach**: Only tasks carrying `SkillIdempotencyKey` matching an active skill are audited.

**Rationale**: Hygiene addresses one specific failure mode: a skill activation produces tasks that the skill never closes. Tasks the agent creates ad-hoc via `TaskBoardTool` are general agent discipline, governed by the agent's own prompt and the user's expectations. Conflating the two creates false positives: a user may intentionally leave an `OPEN` task on the board across sessions; that is not a hygiene violation, it is a deliberate state.

**Alternatives Considered**:
- Audit all tasks on the session's board: broader coverage, but high false-positive rate for long-lived tasks the user wants to keep around.
- Per-task opt-in: most flexible, but no concrete use case justifies the configuration surface.

**Consequences**: Tasks created without a `SkillIdempotencyKey` are invisible to the auditor. This is intentional. The trade-off is that agent-created tasks rely on the agent's own discipline (prompt + system message), not on the hygiene mechanism.

### Decision 3: HITLSpawner is optional, not required

**Chosen Approach**: `Enforcer.WithHITLSpawner(h)` is optional. When unset, `BLOCKED` tasks under `AUTO_FIX` are logged and left in place.

**Rationale**: `ContactHumanTool` is wired into the tool registry at server startup, not into the agent struct. Passing it down into the hygiene path would either require a circular import or a large refactor. The optional spawner is a small interface (`SpawnHITL(ctx, sessionID, agentID, question, *task.Task) error`) the agent layer can adapt to whatever HITL surface is wired for that deployment.

**Alternatives Considered**:
- Require a spawner: forces every embedding to construct a HITL adapter even when HITL is irrelevant (CLI-only deployments, batch runs).
- Vendor a default spawner: would couple `hygiene` to `shuttle`, breaking layering.

**Consequences**: Embeddings without HITL wiring lose the auto-fix behavior for `BLOCKED` tasks. Under `REQUIRE_FIX` (default) this is still handled — the agent is told to surface the question itself.

---

## Constraints and Limitations

### Constraint 1: No retroactive audit

**Description**: Only tasks emitted by *currently-active* skills are audited. If a skill is evicted from the active set during the turn, its tasks become invisible to the hygiene pass.

**Rationale**: The skills orchestrator already has a stickiness checker that keeps skills active while they have open tasks. Skills only evict when all their tasks are terminal — so by construction, evicted skills have no dirty state.

**Impact**: A pathological eviction sequence (sticky check returns false despite open tasks) would leave dirty state unaudited. The stickiness checker is conservative and treats lookup failures as "not sticky," but this remains a possible blind spot.

### Constraint 2: Conversation-blind classification

**Description**: The auditor cannot read the conversation transcript. It classifies `BLOCKED` as a violation whether or not the agent actually surfaced the question.

**Rationale**: Reading the transcript would couple hygiene to the memory layer's representation and make false-negative classification (missing a real violation) more likely than false-positive classification (flagging a non-violation). A false positive costs one LLM turn; a false negative defeats the purpose.

**Impact**: An agent that already surfaced a `BLOCKED` question may be told to surface it again. The injected message is structured so the agent can simply re-state the question; the cost is one extra turn.

### Constraint 3: Best-effort error handling

**Description**: Audit and enforcement errors are logged and the agent returns its existing response. Hygiene cannot block the turn even when its own machinery fails.

**Rationale**: The user-facing contract is "the agent always returns." A hygiene bug must not become an availability incident.

**Impact**: Persistent hygiene failures (e.g., a broken `task.Manager.ListBySkillRun`) silently degrade enforcement. Observability events (`hygiene.audit_failed`) surface this in dashboards.

---

## Performance Characteristics

### Latency

**Audit**: dominated by one SQL prefix scan per active skill. With FTS5 + the partial unique index on `skill_idempotency_key`, this is a single B-tree range scan, ~1ms for typical task counts.

**Enforcement under REQUIRE_FIX**: O(1) — produces a string, appends a session message. ~0.1ms.

**Enforcement under AUTO_FIX**: O(violations) — each violation triggers one `UpdateTask` (for the note) and one `TransitionTask`, plus optionally one HITL spawn. With ~5 violations, ~10–30ms depending on the storage backend.

**Worst-case retry cost**: `max_retries` extra LLM turns. With the default cap of 2 and Claude Sonnet 4.5 at ~1–3 second turn latency, hygiene adds 2–6 seconds to a dirty-state turn. For clean turns, the cost is the audit alone (~1ms).

### Throughput

The hygiene pass runs once per conversation turn at the no-tool-call return path. It does not run mid-turn or during tool execution, so it has no effect on tool throughput.

### Resource Usage

**Memory**: One `Report` per turn (a few KB at most for typical task counts). Released when the turn returns.

**CPU**: Negligible. Classification is a status-field check; message formatting is a single `strings.Builder` pass.

---

## Concurrency Model

The `Auditor` and `Enforcer` are goroutine-safe. They are constructed once per agent and shared across turns. Internally they hold no state — every call passes in the report and outcome as values, and the underlying `task.Manager` is responsible for its own concurrency control.

The `*retryCount` pointer is owned by the conversation loop and accessed only on the goroutine running the loop, so no synchronization is required around it.

---

## Error Handling Philosophy

**Strategy**: Best-effort with structured logging. Hygiene errors never propagate to the caller as failures.

**Error Propagation**:
- Audit errors return `(nil, err)`. The agent logs and returns `false, nil` from `runEndOfTurnHygiene` so the conversation loop returns its existing response.
- Enforcement errors return `(outcome, err)`. The agent logs and surfaces whatever partial outcome was built into `Response.Metadata`.

**Recovery**: None. Hygiene is a quality check, not a correctness check. If it fails, the user still gets a response.

**Observability**: every failure path emits a zap warning with `session`, `skill`, and the underlying error. Span events under `skills.hygiene.audit` record `hygiene.violation_found`, `hygiene.fixup_injected`, `hygiene.fallthrough_to_autofix`, and structured counts per `ViolationKind`.

---

## Evolution and Extensibility

**Extension Points**:
- `SkillRunLister`, `ActiveSkillSource`, `TaskMutator`, `HITLSpawner` are all interfaces. Custom backends or test doubles slot in without modifying hygiene internals.
- New `ViolationKind` values can be added by extending `classify`; `Report.FormatToolMessage` and `kindAction` need matching cases.
- New `HygienePolicy` values can be added to the proto enum; the `Enforce` switch needs a new branch.

**Stability**: The `HygieneConfig` proto message and the `Auditor`/`Enforcer` interfaces are stable. Implementation details (the exact wording of the injection message, the precise ordering inside `FormatToolMessage`) may change between minor releases — they are not API.

**Migration**: When `HygieneConfig.enabled` is unset on an existing agent config, hygiene defaults to enabled. Operators who need the old (no-hygiene) behavior must explicitly set `enabled: false`. The choice favors safety over backward compatibility because the old behavior is the bug being fixed.

---

## Related Work

- **Workflow orchestration**: BPMN-style boundary events fire when activities end in unexpected states. Hygiene's end-of-turn audit is a degenerate version of this pattern applied to a single hook point.
- **Garbage collection**: AUTO_FIX is analogous to a sweep phase: it reclaims state that the producer never finalized. The choice to default to REQUIRE_FIX over AUTO_FIX mirrors the choice between RAII (force the producer to clean up) and GC (have the runtime clean up).

---

## Further Reading

- [Task System Architecture](./task-system.md) — for the underlying task lifecycle and `SkillIdempotencyKey` scheme.
- [Skills Overhaul](./skills-overhaul.md) — for the active-skill set, stickiness, and eviction model.
