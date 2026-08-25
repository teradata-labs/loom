# LLM Slot Scheduler: Ending LLM Starvation

**Status**: ⚠️ Partial — implemented: the scheduler core (`pkg/llm/scheduler`: priority classes, in-process park-and-wake, starvation aging, interactive headroom, AIMD + header capacity calibration, `LLMSchedulerService`; PR #352), door admission control (PR #353), and the resource-lease binding (§11). Still 📋 planned: durable parked-conversation persistence (suspend/resume through the session store), weighted fair queuing per tenant, and §11.3's follow-ups. The body of this document is the original design proposal; where it disagrees with the code, the code is the reference.
**Related**: issues #346, #348, #349; PRs #347 (keyed shared limiters), #350 (limiter on every construction path, concurrent dispatch, YAML `rate_limit` loading, retryable 429s), #352 (scheduler core), #353 (door admission)
**Field data**: 512-agent load campaign against Azure OpenAI gpt-4o + teradata-mcp-v2, 2026-08-20/21

---

## 1. Problem

When a fleet of agents shares one LLM quota, loom currently **blocks and dies**. An
agent's conversation loop makes a synchronous LLM call through a shared rate
limiter; if the limiter's FIFO queue keeps it waiting past `QueueTimeout`
(default 5 minutes), the *entire conversation* fails. While waiting, the agent
continues to hold every scarce resource it owns — most importantly Teradata
session handles and MCP session slots. Nothing upstream learns that the LLM is
saturated until agents start dying.

This is polling-with-a-watchdog. The watchdog does not recover the system; it
shoots the workload.

### 1.1 What the field runs measured

Five separate defects compounded, each found and fixed (or scoped) during the
2026-08 load campaign. They matter here because the scheduler design must
prevent the *class*, not the instances:

| Run | Config | Outcome | Root cause |
|---|---|---|---|
| R7a (512 agents, laptop) | provider defaults | 274/512 dead: `queue timeout after 5m0s`; 0 completed | first-wins limiter singleton at 2 rps (#346) |
| R7b (512, laptop) | 100 rps / 1.2M TPM in YAML | 91 completed; 88 queue deaths; 253 hit 40m deadlines; Azure metered **88K TPM actual** (6% of budget) | serial dispatch — one goroutine ran each call to completion (#349); *and* the YAML `rate_limit` block was silently dropped by the loader (#348 follow-up) |
| az512b (512, Azure VM) | 15 rps, working keyed limiter | 503/512 dead on real Azure 429s within 6 min (419 in one minute); **zero retries fired** | 429s never surfaced inside the limiter (`http.Client.Do` returns nil error on any status), so retry machinery was dead code; plus Azure charges TPM as prompt + `max_tokens` *reservation* — 15 rps × ~8K reserved ≈ 7M TPM demanded vs 1.5M quota |
| az512c (512, Azure VM) | 4 rps, `max_tokens` 1024, all PR #350 fixes | 0 errors, 0 429s through completion of the fleet | admission paced below reservation-derived capacity |

Two structural observations survive all four runs:

1. **The MCP/database side was never the limit.** At 512 concurrent agents the
   teradata-mcp server sat at its configured handle ceiling with zero
   rejections in every run. Starvation is an LLM-side phenomenon.
2. **az512c "works" by slowing everyone uniformly.** 4 rps global admission is
   a blunt instrument: it prevents death by making *every* agent equally slow.
   Aggregate throughput is fine; latency for any individual agent is terrible,
   and nothing prioritizes an agent that is one call away from finishing (and
   releasing a database handle) over one that has not started.

### 1.2 The prior art inside this repo

The MCP handle-contention study (rounds R1–R4) already litigated the core
question. R3 added park-and-wake for session handles — agents parked on a
`resource_link`, subscribed, and retried on `notifications/resources/updated` —
and it **starved**, because nothing created churn: parked agents waited on
slots that were never released. R4 fixed it by making the runtime own the
resource lifecycle (auto-release at conversation end), and completion-driven
churn made parking work: 59/59 correct.

The lesson generalizes and is the backbone of this design:

> **Waiting only works when completion creates churn, and completion only
> happens when the runtime — not the agent, not a timeout — owns the resource
> lifecycle.**

## 2. Design principles

1. **Waiting is a state, not an error.** A conversation waiting for LLM
   capacity must never be killed by the *limiter's* clock. The only deadline
   that can fail a conversation is the conversation's own budget (its
   `--timeout`, its token budget, its cost ceiling).
2. **Run-to-completion beats fairness.** Finishing a conversation releases
   *all* of its resources: its LLM share, its Teradata handle, its MCP slot,
   its memory. A scheduler biased toward in-flight work drains the system;
   one that admits everything equally half-runs everyone and finishes no one.
3. **The runtime owns the lifecycle.** Agents never voluntarily release
   resources (measured: 0/192 voluntary handle releases across R1–R3).
   Parking, resource release, wake, and resume are runtime decisions.
4. **Capacity is measured, not configured.** Providers state their quota on
   every response (`x-ratelimit-remaining-*`, `Retry-After`). Static YAML
   numbers are the fallback, not the source of truth.
5. **Backpressure belongs at the door.** Starving an agent mid-task wastes
   held resources and partial work; refusing (or queueing) a conversation at
   admission costs nothing.

## 3. Architecture

```
                        ┌─────────────────────────────────────────────┐
                        │                looms server                 │
                        │                                             │
 loom chat ──────────►  │  Admission Controller                       │
 (new conversation)     │  ├─ per-scope active-conversation cap       │
                        │  ├─ derived from SlotScheduler capacity     │
                        │  └─ queue at door OR RESOURCE_EXHAUSTED     │
                        │        │ admit                              │
                        │        ▼                                    │
                        │  Conversation loop (runConversationLoop)    │
                        │        │ needs LLM call                     │
                        │        ▼                                    │
                        │  SlotScheduler (per provider scope)         │
                        │  ├─ priority classes + aging                │
                        │  ├─ priority inheritance (held handles)     │
                        │  ├─ reservation-aware TPM accounting        │
                        │  ├─ grant ──► SharedRateLimiter.Do(...)     │◄── PR #347/#350
                        │  └─ no slot ──► PARK conversation           │    primitives
                        │        │                 ▲                  │
                        │        ▼                 │ wake             │
                        │  Parked Conversation Store                  │
                        │  (persisted state + waiter registration)    │
                        │                                             │
                        │  Capacity Feedback ◄── provider response    │
                        │  (remaining-tokens, Retry-After, 429s)      │
                        └─────────────────────────────────────────────┘
```

Four components. The first two are the deep work; the last two upgrade
machinery that already exists.

### 3.1 SlotScheduler

One scheduler per provider quota scope — the same scope keys PR #347
introduced (`azure-openai|endpoint|deployment`, `bedrock|region|model`, …).
The scheduler *grants call slots*; the existing `SharedRateLimiter` becomes
its execution arm (pacing within a grant, retrying throttles within a grant).

The correct mental model is an **I/O scheduler** (deadline/elevator), not an
interrupt controller in the strict sense: the scarce resource is
tokens-per-minute, calls take seconds, and interrupt *latency* is irrelevant —
what matters is completion-driven wakeups, priority classes, and starvation
aging. The interrupt-style part is the contract: **requesters never spin and
never die waiting; they park and are woken.**

Priority classes, highest first:

| Class | Who | Why |
|---|---|---|
| `RESOURCE_HOLDER` | conversation holding an external scarce resource (Teradata session handle, MCP slot) — declared by the backend through the lease contract (§11) | priority inheritance: it is blocking other agents who are burning LLM budget trying to acquire what it holds |
| `IN_FLIGHT` | conversation that has completed ≥1 LLM call | run-to-completion: finishing releases everything |
| `NEW` | first call of a conversation | admitted as churn allows |

Within a class: weighted fair queuing per tenant/agent, FIFO within a weight.
**Aging**: a waiter that has waited longer than `starvation_age_s` is promoted
one class, so `NEW` cannot be starved forever by a hot fleet. One outstanding
slot per conversation (the conversation loop is sequential anyway).

Grant lifetime: a slot covers one LLM call *including its throttle retries*.
The grant carries a token reservation (§3.4); the reservation is trued-up from
the response's actual usage when it completes.

### 3.2 Park and wake

When the scheduler has no slot, the conversation **parks** instead of blocking
a goroutine inside the limiter queue:

1. The conversation loop reaches its LLM call and receives `ErrParked` with a
   waiter handle instead of blocking.
2. Conversation state is persisted through the existing session-persistence
   machinery (messages, tool state, budgets). This is a suspend point exactly
   like the existing HITL-approval suspend, which already proves
   `runConversationLoop` can be re-entered mid-conversation.
3. **Resource disposition** — the runtime decides per resource what parking
   means:
   - MCP session handles: marked *reclaimable-under-pressure*, not eagerly
     released. Handles can pin state that cannot be rebuilt (volatile tables,
     transactions). The MCP server may reclaim a reclaimable handle if its own
     budget saturates; the owning conversation then reacquires on resume and
     replays its idempotent setup (this is a documented, bounded cost — and
     conversations holding non-reclaimable state keep `RESOURCE_HOLDER`
     priority precisely so their parks are short).
   - In-process memory: retained (parking is expected to be seconds-to-minutes).
4. The scheduler wakes the waiter when churn creates a slot (a grant
   completes, capacity feedback raises the ceiling, or aging promotes it).
   Wake resumes the loop at the pending LLM call.
5. Crash recovery: parked conversations are durable. On looms restart, parked
   waiters re-register from the store; grants in flight at crash are simply
   lost (the call is retried on resume — LLM calls are idempotent from the
   conversation's perspective).

Why park at all when goroutines are cheap: not to save threads — to make
waiting **observable, schedulable, and non-fatal**, and to give the runtime a
hook to reclaim resources from waiters instead of letting them squat.

### 3.3 Admission control

The front door. `looms` tracks, per provider scope, the number of *active*
(admitted, not parked) conversations. When a new conversation would exceed
`max_active_conversations` for its scope, the server either:

- **queues it at the door** (default): the `loom chat` stream opens and waits;
  the client sees a progress event (`queued: position N, estimated wait`), not
  silence; or
- **rejects with backpressure** (`RESOURCE_EXHAUSTED` + retry-after) when the
  door queue itself exceeds `max_door_queue`.

`max_active_conversations` is derived, not hand-tuned:

```
capacity_calls_per_s = min(measured_remaining_tokens_per_minute,
                           configured_tpm) / 60 / reservation_per_call
max_active = capacity_calls_per_s × p50_call_duration_s × utilization_target
```

With the campaign's measured constants (≈7K reserved tokens/call at
`max_tokens` 1024, ≈3 s median call, 1.5M TPM quota, 0.8 target): ≈85 active
conversations. A 512-agent launch becomes: 85 admitted, 427 queued at the
door, continuous admission as conversations complete — same aggregate
throughput as today's uniform slowdown, zero mid-flight starvation, and door
wait is visible to the caller instead of manifesting as a dead agent.

### 3.4 Capacity feedback and reservation-aware accounting

Two facts the current limiter ignores:

1. **Providers charge quota on reservations, not usage.** Azure debits
   `estimated_prompt + max_tokens` per request at admission time. az512b
   demonstrated the gap: actual usage ≈1.4K tokens/call, reserved ≈8K —
   a 5× invisible oversubscription. The scheduler accounts the same way the
   provider does: debit the reservation at grant, credit back
   `reservation − actual` at completion.
2. **Most providers state their limits on every response — but not all.**
   Measured against our Azure deployment (2026-08-21), Azure returns the full
   telemetry set on every 200:

   ```
   x-ratelimit-limit-requests: 9000        x-ratelimit-limit-tokens: 1500000
   x-ratelimit-remaining-requests: 8999    x-ratelimit-remaining-tokens: 745399
   x-ratelimit-renewalperiod-requests: 10  x-ratelimit-renewalperiod-tokens: 60
   x-ratelimit-reset-requests: 0           x-ratelimit-reset-tokens: 30
   ```

   Per-provider signal matrix:

   | Provider | Per-response signal | Calibration strategy |
   |---|---|---|
   | OpenAI | `x-ratelimit-{limit,remaining,reset}-{requests,tokens}`, `Retry-After` | header-driven |
   | Azure OpenAI | full set incl. renewal periods (verified above) | header-driven |
   | Anthropic | `anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-{limit,remaining,reset}`, `retry-after` | header-driven |
   | LiteLLM proxy | emulates OpenAI-style headers, forwards upstream hints | header-driven |
   | Gemini | no headers; 429 body carries `google.rpc.RetryInfo.retryDelay` | error-body hint + AIMD |
   | Bedrock | none (`ThrottlingException` only; quota via Service Quotas/CloudWatch out-of-band) | AIMD |
   | Ollama | none (local; capacity = host parallelism) | fixed concurrency |

   The feedback loop is therefore layered:
   - **Header-driven** (precise): remaining-token/request headers continuously
     calibrate the scope's effective TPM and window phase (quota changes,
     other consumers on the same deployment, burst windows); `Retry-After`
     schedules the *precise* wake for a throttled grant.
   - **Error-body hints**: Gemini's `retryDelay` is honored like `Retry-After`.
   - **AIMD fallback** (signal-free providers): additive-increase /
     multiplicative-decrease of the effective ceiling driven by observed
     throttle errors — TCP congestion control without ECN. A clean interval
     raises the ceiling by a step; any throttle halves it.
   - In every mode, a 429 storm collapses the admission ceiling immediately
     rather than after N corpses.

This also finally makes `tokens_per_minute` a real gate. Today it is
metrics-only (discovered during #349): the sliding window is logged and never
enforced. Under this design the scheduler *is* the TPM enforcement, with the
provider's own numbers as calibration.

## 4. Proto (law first)

New file `proto/loom/v1/llm_scheduler.proto` — sketch, names final only after
`buf lint`:

```proto
syntax = "proto3";
package loom.v1;

// Priority class of a slot request. Order is semantic: higher classes are
// served first; aging promotes a starved waiter one class.
enum SlotPriorityClass {
  SLOT_PRIORITY_CLASS_UNSPECIFIED = 0;
  SLOT_PRIORITY_CLASS_NEW = 1;              // first call of a conversation
  SLOT_PRIORITY_CLASS_IN_FLIGHT = 2;        // conversation mid-task
  SLOT_PRIORITY_CLASS_RESOURCE_HOLDER = 3;  // holds an external scarce resource
}

// LLMSchedulerConfig configures one provider-scope scheduler.
message LLMSchedulerConfig {
  // Enforced tokens-per-minute for the scope. 0 = calibrate purely from
  // provider response headers (with rate_limit defaults as the floor).
  int64 tokens_per_minute = 1;
  // Reservation per call when the provider charges reservations
  // (prompt estimate + max_tokens). 0 = derive from agent max_tokens.
  int32 reservation_tokens_per_call = 2;
  // Active-conversation ceiling. 0 = derive from capacity (see design doc).
  int32 max_active_conversations = 3;
  // Door-queue depth beyond which new conversations are rejected with
  // RESOURCE_EXHAUSTED instead of queued.
  int32 max_door_queue = 4;
  // Seconds after which a waiting slot request is promoted one class.
  int32 starvation_age_s = 5;
  // Target utilization of measured capacity (0,1]; default 0.8.
  float utilization_target = 6;
}

// SlotState is the observable state of one scheduler scope.
message SlotState {
  string scope = 1;                    // e.g. "azure-openai|<endpoint>|<deployment>"
  int32 active_conversations = 2;
  int32 parked_conversations = 3;
  int32 door_queue_depth = 4;
  int64 effective_tokens_per_minute = 5;  // calibrated, not configured
  int64 reserved_tokens_outstanding = 6;
  google.protobuf.Timestamp next_wake = 7;
}

// LLMSchedulerService is the admin/observability surface. Slot request/grant
// itself is in-process (the conversation loop), not RPC.
service LLMSchedulerService {
  rpc GetSlotState(GetSlotStateRequest) returns (GetSlotStateResponse);
  rpc ListWaiters(ListWaitersRequest) returns (ListWaitersResponse);
  rpc SetSchedulerConfig(SetSchedulerConfigRequest) returns (SetSchedulerConfigResponse);
}
```

`AgentConfig.spec.llm.rate_limit` (`LLMRateLimitConfig`) keeps its role: it is
the per-agent *request shaping* input (and, post-#350, actually loads from
YAML). The scheduler consumes it; it does not replace it.

## 5. What changes where

| Piece | Today | Under this design |
|---|---|---|
| `RateLimiter.Do` queue timeout | kills the conversation at 5m | applies only within a *grant* (a call + its retries); waiting for a grant cannot kill anything |
| `SharedRateLimiter` (PR #347) | the whole policy | execution arm under the scheduler: paces within grants, retries throttles |
| `runConversationLoop` | blocks synchronously on LLM call | requests slot; on `ErrParked` suspends via the session-persistence path (same shape as HITL suspend) and resumes on wake |
| MCP handle auto-release | at conversation end | unchanged; plus *reclaimable-under-pressure* marking while parked, and handle-holding feeds `RESOURCE_HOLDER` priority |
| `tokens_per_minute` | metrics-only, never enforced | enforced by the scheduler, reservation-aware, header-calibrated |
| 429 handling | in-grant blind exponential backoff (working as of PR #350) | in-grant retry with `Retry-After`-scheduled wake; storm collapses admission ceiling |
| `loom chat` under saturation | agents die mid-flight | queued at the door with visible position/estimate, or `RESOURCE_EXHAUSTED` + retry-after |

## 6. Failure modes and invariants

- **No death by waiting.** Invariant: the scheduler never returns a fatal
  error for lack of capacity. Fatal outcomes come only from the
  conversation's own budget/deadline or a non-retryable provider error.
- **Bounded starvation.** Aging guarantees any waiter reaches the top class in
  `≤ 2 × starvation_age_s`. (Trade-off: a saturated system with aging degrades
  toward FIFO — acceptable; aging exists for liveness, not throughput.)
- **Bounded priority inversion.** `RESOURCE_HOLDER` inheritance is one level
  and non-transitive; a holder that parks with non-reclaimable state keeps the
  boost so its park is short.
- **Quota collapse mid-run** (another consumer appears on the deployment):
  header calibration shrinks effective TPM; the admission ceiling drops; new
  conversations queue at the door; in-flight conversations finish on the
  reduced budget. No deaths — throughput degrades, liveness holds.
- **Scheduler crash**: parked conversations are durable; waiters re-register
  on restart; outstanding reservations are rebuilt as zero (worst case: one
  brief over-admission burst absorbed by in-grant 429 retry).
- **Thundering wake**: wakes are granted one slot at a time from the scheduler
  loop; a completion wakes exactly one waiter. No herd re-forms.

## 7. Observability

Per scope: `loom_llm_slots_active`, `loom_llm_parked`, `loom_llm_door_queue`,
`loom_llm_effective_tpm`, `loom_llm_reserved_outstanding`,
`loom_llm_wait_seconds` (histogram, by priority class),
`loom_llm_promotions_total` (aging events), `loom_llm_admission_rejects_total`.
Every park/wake/grant is a span event on the conversation trace, so a slow
agent's timeline shows *where* it waited and *why* it was woken. Scheduler
state queryable live via `LLMSchedulerService.GetSlotState`.

## 8. Phasing

**Phase 0 — shipped groundwork** (PRs #347, #350): keyed per-scope limiters;
limiter attached on every construction path; concurrent admission-paced
dispatch; YAML `rate_limit` actually loads; 429s retryable. az512c validated:
512 agents, zero starvation deaths at conservative static tuning.

**Phase 1 — cheap wins, no new proto** (small PRs):
1. Honor `Retry-After` in `executeWithRetry`; parse and expose
   `x-ratelimit-remaining-*` per response.
2. Make grant-wait non-fatal: split `QueueTimeout` into in-grant retry budget
   vs (removed) wait-to-enter timeout; waiting bounded only by caller context.
3. Two-class priority (IN_FLIGHT > NEW) inside `SharedRateLimiter`.
4. Static `max_active_conversations` admission cap in looms with door
   queueing and a progress event.

**Phase 2 — the scheduler** (proto first, then implementation):
`llm_scheduler.proto`; SlotScheduler with classes/aging/fair share;
reservation-aware TPM enforcement with header calibration; park/wake through
the session-persistence suspend path; `LLMSchedulerService`.

**Phase 3 — cross-system integration**: `RESOURCE_HOLDER` inheritance wired to
MCP handle state; reclaimable-under-pressure handle marking; door-queue
estimates fed from scheduler telemetry.

Each phase is independently shippable and independently validated by re-running
the 512-agent gauntlet (the campaign scripts and measured baselines are
retained; see `docs/architecture/` load-test notes and the session artifacts).

## 9. Open questions

1. **Park threshold**: park immediately when no slot, or spin briefly
   (100–500 ms) to avoid persistence churn under transient contention?
   Proposal: spin ≤ `p50_call_duration`, then park.
2. **Multi-scope conversations** (an agent that talks to two providers): slot
   requests are per-scope; does a conversation hold grants on two scopes
   simultaneously? Proposal: yes, grants are independent; inversion analysis
   covers only external resources, not cross-scope LLM grants.
3. **Fairness unit**: per-agent, per-tenant, or per-conversation weights?
   Multi-tenant deployments (loom-cloud) need per-tenant; default single-tenant
   weight is uniform.
4. **Door-queue persistence**: does a queued-at-door conversation survive a
   looms restart, or is the door queue ephemeral (client retries)? Proposal:
   ephemeral — the client holds the intent; only *admitted* state is durable.
5. **Reservation estimate for prompt tokens**: provider-side estimators differ
   (Azure uses a character heuristic); do we mirror per provider or
   over-reserve uniformly? Needs a small measurement matrix.

## 10. Measured constants (2026-08 campaign)

For deriving defaults; re-measure per deployment:

- Azure gpt-4o GlobalStandard, capacity 1500: 1.5M TPM / 9,000 RPM; quota
  charged as prompt-estimate + `max_tokens` reservation at request time.
- Task profile (12-tool MCP agent, analytic SQL task): ≈7K Azure-metered
  tokens per conversation; ≈1.4K actual tokens per call; ≈8–10 calls per
  conversation; median call ≈3 s.
- Sustainable call rate at 1.5M TPM: ≈17 calls/s at 1024-token reservation;
  ≈3 calls/s at 4096.
- MCP server (teradata-mcp-v2, one instance): 512 concurrent agents, 256
  handle budget, zero rejections, <50 MB RSS — never the constraint.

## 11. Resource leases

**Status**: ✅ Implemented — the tool-result contract (`pkg/shuttle`), the
scheduler mark/unmark lifecycle (`pkg/llm/scheduler`), and the agent's
per-session lease ledger (`pkg/agent/lease_ledger.go`). The MCP adapter
migration and the express lane are 📋 planned (§11.3).

The `RESOURCE_HOLDER` class (§3.1) needs to know *when* a conversation holds
a scarce backend resource — without loom knowing what the resource is. The
binding is backend-agnostic by contract: the **backend** declares what is a
scarce lease, through the tool-result contract, and loom reacts generically.
Kind and ID are backend-defined opaque strings (e.g. `"db-session"` /
`"sess-42"`); loom never interprets them beyond identity.

### 11.1 The tool-result contract (pkg/shuttle) ✅

**Lease events** ride `Result.Metadata` under the well-known key
`loom.lease_events` (`shuttle.MetadataLeaseEvents`). Each entry is a map with
string fields `action` (`"acquired"` | `"released"`), `kind`, and `id`, so
any backend adapter — or a YAML-declared tool that shapes its own metadata —
can emit them without importing loom types. Go emitters use the typed
helpers:

```go
shuttle.AppendLeaseEvent(res, shuttle.LeaseEvent{
    Action: shuttle.LeaseAcquired, Kind: "db-session", ID: "sess-42",
})
events := shuttle.LeaseEventsFrom(res)
```

Emitting `acquired` tells loom "this conversation now holds a scarce backend
resource"; `released` tells it the lease ended. A release matches an acquire
only when both Kind and ID are equal. Parsing is tolerant by contract: tool
results are data, so a malformed entry is skipped, never an error, and the
stored form survives JSON marshal/unmarshal boundaries unchanged.

**Backpressure hint**: the park-and-wake contract from PR #355 (📋 open, unmerged) at the
MCP layer is hoisted here as a generic `Error.Details` slot —
`loom.backpressure` (`shuttle.DetailsBackpressure`), typed as
`shuttle.BackpressureHint{Code, RetryAfterS, WaitParam, MaxWaitS}` with
`(*Error).SetBackpressure` / `(*Error).Backpressure()` accessors and the
same wire field names (`code`, `retry_after_s`, `wait_param`, `max_wait_s`).
A hint declares the failure to be capacity flow control, not a fault: the
identical call, re-issued after a wait, is expected to succeed. This is the
contract only — no wait loop lives at the shuttle layer; the MCP adapter's
freeze loop migrates onto it when PR #355 merges. On this branch the hint is contract-only: it has no producer or consumer yet.

### 11.2 The mark/unmark lifecycle (pkg/agent + pkg/llm/scheduler) ✅

The agent keeps a per-session **lease ledger**: the set of (Kind, ID) pairs
the session's conversation currently holds.

- **After every tool execution**, the conversation loop folds the result's
  lease events into the ledger. While the session holds any lease, the
  turn's `SlotInfo` is marked (`scheduler.MarkResourceHolder`) and the
  conversation's remaining LLM calls classify `RESOURCE_HOLDER`; when the
  last lease is released, the mark is cleared
  (`scheduler.UnmarkResourceHolder`) and the class falls back to the
  call-count progression. Results without lease events never touch scheduler
  state.
- **Cross-turn seeding**: leases outlive turns (a session handle survives
  the think time between turns), but a turn's `SlotInfo` does not — the
  server installs a fresh one per turn. The agent therefore re-marks each
  turn from the ledger at turn start, so a lease-holding session *starts*
  its next turn as `RESOURCE_HOLDER`. Installers that know the lease state
  up front can seed it directly via `scheduler.WithSlotInfoHolding`.
- **Retirement**: the ledger entry is dropped with the session
  (`DeleteSession`, `ClearAllSessions`) — a leaked entry on a deleted
  session would pin `RESOURCE_HOLDER` priority forever.
- **Tolerance**: re-acquire of a held lease is idempotent; double-release
  and release-of-unknown are no-ops; a session's lease count never goes
  negative.
- **Inert when unwired**: with scheduling disabled or no `SlotInfo`
  installed, the ledger still tracks leases and the mark/unmark calls are
  no-ops — unwired deployments behave exactly as before.

### 11.3 Planned follow-ups 📋

- **Trust model**: lease events are taken at the emitting tool's word — any tool that shapes `Result.Metadata` can assert or release any lease. Operator-chosen backends are trusted; deployments running untrusted tool servers must strip these keys at their adapter boundary (see the doc block in `pkg/shuttle/lease.go`).
- **Ledger durability**: the lease ledger is process memory only. After a `looms` restart the ledger is empty — a backend lease that survived the restart (e.g. a remote HTTP-MCP session) loses RESOURCE_HOLDER seeding until its next lease event. Durable seeding is 📋 planned alongside durable park persistence.
- **MCP adapter migration**: PR #355's MCP-level backpressure hint and
  freeze loop move onto the shuttle contract when that PR merges, and MCP
  session handles then declare themselves as lease events instead of
  adapter-private state.
- **Express lane**: reserved scheduler headroom for `RESOURCE_HOLDER`
  acquisitions (analogous to the interactive headroom), so a holder never
  waits behind a saturated general queue.
