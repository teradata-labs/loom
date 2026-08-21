# Code-Mode Tool Orchestration (`run_code_with_tools`)

| | |
|---|---|
| Status | Draft v2 for review (v1 review findings applied) |
| Author | Ilsun Park (OCTO Advanced R&D) |
| Date | 2026-08-20 (v1: 2026-08-19) |
| Repo state verified | `pkg/shuttle`, `pkg/tools/registry`, `pkg/agent`, `pkg/storage`, `proto/loom/v1`, `prompts/tools`, `go.mod` against `origin/main` @ `4046c659` (2026-08-20) |
| Tracking | TBD (relates to TER-263 lazy skill/tool loading) |

## Table of contents

1. [Summary](#1-summary)
2. [Motivation](#2-motivation)
3. [Verified current state](#3-verified-current-state)
4. [Design invariants](#4-design-invariants)
5. [Architecture](#5-architecture)
6. [Package layout](#6-package-layout)
7. [Component specifications](#7-component-specifications)
8. [Guest API contract (model-facing)](#8-guest-api-contract-model-facing)
9. [Security model](#9-security-model)
10. [Error codes](#10-error-codes)
11. [Failure and partial-completion semantics](#11-failure-and-partial-completion-semantics)
12. [Design trade-offs](#12-design-trade-offs)
13. [Test requirements](#13-test-requirements)
14. [Non-goals (v1)](#14-non-goals-v1)
15. [Resolved questions](#15-resolved-questions)
16. [References](#16-references)
17. [Change footprint](#17-change-footprint)

## 1. Summary

Add a builtin tool, `run_code_with_tools`, that lets the model submit a short program which orchestrates existing Loom tools inside a sandboxed guest runtime. Tool bindings inside the guest re-enter `shuttle.Executor.Execute`, so admission, permissions, audit stamping, parameter normalization, large-parameter offload, and metering apply unchanged to every in-script call. The guest calls advertised tools as named functions, fans out with `parallel()`, and reaches registry-only tools through a `call_tool()` escape hatch — all through the same executor entry point. Intermediate tool results stay inside the guest; only the script's declared result and captured stdout return toward the model, size-capped by the runtime and subject to the same render-time large-result offload as any other tool result (§7.5).

The guest runtime is an interface. V1 ships a Starlark implementation (pure Go, CGO-free). A wazero-backed implementation and container escalation are follow-on runtimes behind the same interface.

## 2. Motivation

Two structural costs of the current one-tool-call-per-inference loop:

1. Every tool invocation costs a full inference pass. A 20-call workflow is 20 model round trips.
2. Every intermediate result enters model context whether or not it matters. Fetch-then-aggregate workloads pay context for data the model only needed in reduced form.

Published measurements for the pattern (see §16 for sources):

- Anthropic Programmatic Tool Calling: average token usage on complex research tasks dropped from 43,588 to 27,297 (−37%); GAIA-style benchmark accuracy 46.5% → 51.2%; internal knowledge retrieval 25.6% → 28.5%.
- 75-tool project-management benchmark: −38% billed input tokens, accuracy unchanged.
- Negative result: on τ²-bench (one or two sequential calls per turn) scores were unchanged and cost rose ~8%. Code-mode is a fan-out/aggregation optimization, not a universal win. The tool description must steer the model accordingly (§8.4).
- Complementary deferred tool loading: ~85% context reduction and large MCP-eval accuracy gains when definitions load on demand. Loom already has this half (`pkg/tools/registry` FTS5 search + `tryDynamicRegistration`); code-mode compounds it.

Calibration caveat: those numbers come from harnesses whose models were steered for the pattern, and Anthropic's PTC runs real Python. Transfer to Loom depends on Loom-configured models writing first-try-valid Starlark despite the dialect gap (§8.3); the eval harness (§17 phasing) — not this spec — is the gate for recommending code-mode by default.

Loom-specific motivation: the executor boundary is where budget-aware execution, consumption metering, and the governance audit trail live. Code-mode must strengthen that boundary (single choke point sees more calls), not bypass it.

## 3. Verified current state

Facts a reviewer or implementing agent can check directly:

- `pkg/shuttle/tool.go` — `Tool` interface: `Name() string`, `Description() string`, `InputSchema() *JSONSchema`, `Execute(ctx, map[string]interface{}) (*Result, error)`, `Backend() string`. `Result` carries `Success`, `Data`, `Error{Code, Message, Retryable, Suggestion}`, `Metadata`, `ExecutionTimeMs`, `CacheHit`, `DataReference`.
- `pkg/shuttle/executor.go` — `Executor.Execute(ctx, toolName, params)` pipeline, in order: local registry lookup with `tryDynamicRegistration` fallback (FTS registry search, MCP/builtin wrapper registration); `normalizeParametersToSchema`; `admit` (PermissionChecker unconditionally when set, then admission `Chain`); deferred `stampAdmissionDecision` making `admission.decision` a reserved, chain-sourced metadata key on every exit path; `handleLargeParameters` / `dereferenceLargeParameters` (shared-memory offload keyed by session); tool body; authoritative timing; `admissionChain.Observe`. Error shapes: a tool-body error becomes `Result{Success:false, Error:{Code:"execution_failed"}}` with `err == nil`; a lookup/infrastructure failure returns `(nil, err)`. `ExecuteWithTool(ctx, tool, params)` exists as a pre-resolved variant with the same admission pipeline.
- `pkg/shuttle/executor.go:286-287` — **results pass through the executor whole**: "Large-result offload is a pure render condition of ContextCompilation." The executor never sets `DataReference` on results; render-time offload (below) is what bounds inline visibility to the model.
- `pkg/agent` large-result path — `storage.DefaultSharedMemoryThreshold = 16384` bytes (`pkg/storage/shared_memory.go:36-41`). At render time, a tool result exceeding the threshold appears to the model as an offload stub addressable by the `query_tool_result` builtin (`pkg/agent/builtin_tools.go`) within the producing turn (pages or SQL over the payload). `SetOffloadExemptTools` exists but is unwired (nil default) as of v1.4.0.
- `pkg/shuttle/instrumented_executor.go` — `InstrumentedExecutor` with the identical `Execute` signature, emitting a `SpanToolExecute` span plus metrics per call. Note: `StartSpan`'s returned context is discarded (`_, span := ...`), so a nested call's span does **not** parent under its caller's span; correlation is attribute-based (§7.6).
- `pkg/shuttle/permission_checker.go` — `CheckPermission` never blocks: an approval-required, non-allow-listed tool is denied immediately ("permission request mechanism is not yet implemented", lines 163-170). `Advertisable` (lines 102-137) mirrors `CheckPermission`'s decision tree without side effects and already hides hard-disabled and approval-gated tools from the model. `pkg/agent/agent.go:910` (`applyPermissionToolFilter`) already computes advertised = registered ∩ `Advertisable`.
- `pkg/shuttle/admission_hook.go:44-51` — `AdmissionRequest` carries `Ctx`, so admission hooks can read caller markers with no executor change.
- `pkg/tools/registry/search_tool.go` — builtin `tool_search` with an advertisability predicate (`SetToolFilter`) so the model never discovers tools it cannot run.
- `pkg/agent/registry.go:962-992` — builtin gating convention: `tool_search` registers only when explicitly listed in `tools.builtin`; it is wrapped in `shuttle.NewPromptAwareTool(tool, agent.prompts, "tools.tool_search")` for externalized descriptions.
- `pkg/shuttle/builtin/` — builtin pattern: struct implementing `Tool`, description sourced from PromptRegistry with a code fallback, config knobs as setters. `shell_execute.go` establishes conventions this spec reuses: default timeout 300s, max 600s, `max_output_bytes` default 1 MiB. (shell_execute puts no upper bound on `max_output_bytes`; this spec does not copy that gap — §7.5.)
- `prompts/tools/` — descriptions live in **grouped domain files** (`shell.yaml` holds `shell_execute`, `tool_search.yaml` holds `tool_search`, …), each file a `prompts:` list of `id:`/`content:` entries; the lookup key is `tools.<tool_name>`.
- Agent configuration is **proto-mirrored**: `proto/loom/v1/agent_config.proto` `message AgentConfig` (`ToolsConfig tools = 5` with `mcp = 1`, `custom = 2`, `builtin = 3`; `MemoryConfig` carries `GraphMemoryConfig graph_memory = 8`, `TaskBoardConfig task_board = 9`), with YAML loader structs in `pkg/agent/config_loader.go` (`ToolsConfigYAML` at line 163). `pkg/config` is project/path loading only — it is **not** where agent config lives.
- `proto/loom/v1/agent_config.proto` `BehaviorConfig` — `allow_code_execution = 3` ("security setting"; currently plumbed through config but enforced by no tool) and `max_tool_executions = 6` (conversation-level loop budget, default 50, enforced in the agent loop — it counts loop-dispatched executions, so guest-originated calls do not decrement it).
- `go.mod` — no wasm runtime, no script engine. Pure-Go/CGO-free posture (`modernc.org/sqlite`). `golang.org/x/sync` present. Docker client present (`pkg/docker`).

## 4. Design invariants

These are the review criteria. An implementation that violates any of them is wrong regardless of how well it benchmarks.

- **I1 — Single execution path.** Every guest-originated tool call goes through the same `Execute` entry point as a model-originated call. No direct `tool.Execute` calls from the bridge, no second admission path.
- **I2 — Governance-transparent.** An admission denial inside a script produces the same `permission_denied` result, the same `admission.decision` audit stamp, and the same `Observe` call as it would outside a script. The script sees the denial as a failed call; it does not crash the host and it is not retried automatically.
- **I3 — Context isolation.** Full tool result data (including `DataReference` payloads, dereferenced) is visible to the guest. None of it reaches model context except the script's declared result and captured stdout, both size-capped by the runtime — and what the model sees *inline* is further bounded by the existing render-time offload: a result above `storage.DefaultSharedMemoryThreshold` arrives as a pageable stub, never inline (§7.5).
- **I4 — Provenance.** Every guest-originated call is distinguishable in traces and in the admission request context from a model-originated call.
- **I5 — No new capabilities.** The guest has no I/O, no network, no filesystem, no clock beyond what a bound tool provides. The tool bindings are the complete capability surface (`load()` disabled; `json`/`math` stdlib modules are pure; the `time` module stays off).
- **I6 — Runtime-pluggable.** The Starlark engine is one implementation of `GuestRuntime`. Nothing outside `pkg/shuttle/codemode` may import Starlark types.
- **I7 — Bounded blast radius.** A single run is bounded in wall clock, interpreter steps, tool-call count, and output bytes, with an optional coarse heap guard. No model-supplied knob can exceed its host-configured cap. (Heap is only *guarded*, not accounted, in the Starlark tier — stated plainly in §9.)
- **I8 — Single-threaded guest.** All Starlark evaluation and all Starlark value construction happen on the interpreter goroutine. `parallel()` fans out host-side facade calls only; it never invokes guest callables from worker goroutines (§7.2).

## 5. Architecture

### 5.1 Component view

```
 model                      host (Go)                                    guest (Starlark v1)
 ─────                      ─────────                                    ───────────────────
 tool_use:
 run_code_with_tools ─────▶ RunCodeTool.Execute
   {code, tools?, ...}        │
                              ├─ enumerate live tool list (at Execute
                              │  time, not agent construction),
                              │  filter through Advertisable
                              ├─ Bridge.Bind: one guest fn per tool,
                              │  plus bind / parallel / call_tool
                              ▼
                            GuestRuntime.Run(code, bindings) ──────────▶ get_expenses(user_id=..)
                              ▲                                          call_tool("x", a=1)
                              │  per call:                               parallel([bind(..), ..])
                              │  ctx = WithCaller(ctx, runID)                      │
                              │  ExecutorFacade.Execute(ctx, ...) ◀─────── trap ───┘
                              │  (admission, audit stamp, param
                              │   offload, spans — all existing code)
                              │  Result.Data (deref'd) ────────────────▶ back into guest value
                              │
                              ├─ caps: wall clock, steps, tool-call
                              │  budget, output bytes, heap guard
                              │
                              └─ on completion: {result global, stdout,
                                 call log}, size-capped
                              │
                              ▼
                            shuttle.Result ────────────────────────────▶ model context
                                                                         (render-time offload:
                                                                          ≥16 KiB → stub +
                                                                          query_tool_result)
```

The bridge is the only component that touches both worlds, and it owns no policy: admission, permissions, audit, and metering all live behind `ExecutorFacade.Execute`, which is the same entry point a model-originated call uses.

### 5.2 Parallel fan-out sequence (threading contract)

```
model     RunCodeTool     Bridge         Starlark thread        workers (≤ max_parallel)   Executor
  │            │             │                  │                          │                  │
  ├─ tool_use ▶│             │                  │                          │                  │
  │            ├─ Bind ─────▶│                  │                          │                  │
  │            ├─ Run ───────┼─────────────────▶│                          │                  │
  │            │             │                  ├─ p = [bind(f, kw=..)     │                  │
  │            │             │◀─ snapshot ──────┤      for ...]            │                  │
  │            │             │   (name + kwargs │  (converted to Go on     │                  │
  │            │             │    per element)  │   interpreter thread)    │                  │
  │            │             │                  ├─ parallel(p) ───────────▶│                  │
  │            │             │◀─ batch budget ──┤                          │                  │
  │            │             │   pre-check      │                          ├─ Execute(p1) ───▶│
  │            │             │                  │                          ├─ Execute(p2) ───▶│
  │            │             │                  │                          │  (admit, stamp,  │
  │            │             │                  │                          │   body, Observe  │
  │            │             │                  │                          │   per call)      │
  │            │             │                  │                          │◀─ *Result ───────┤
  │            │             │◀─ Go values, gathered in input order ───────┤                  │
  │            │             ├─ convert ───────▶│ (interpreter thread)     │                  │
  │            │             │                  ├─ filter / aggregate      │                  │
  │            │             │                  ├─ result = {...}          │                  │
  │            │◀─ RunOutcome┼──────────────────┤                          │                  │
  │◀─ Result ──┤  {result, stdout, call log}    │                          │                  │
```

The two thread-boundary crossings are explicit: kwargs convert Starlark→Go at `bind()` time on the interpreter thread; results convert Go→Starlark after the gather, again on the interpreter thread. Worker goroutines only ever hold Go values and the facade.

## 6. Package layout

New package `pkg/shuttle/codemode` (child of shuttle: it needs `Tool`, `Result`, `JSONSchema`; shuttle must not import it back).

```
pkg/shuttle/codemode/
    runtime.go            GuestRuntime interface, RunConfig, RunOutcome
    bridge.go             Bridge: bindings, bind/parallel/call_tool, budget, call log
    starlark_runtime.go   Starlark GuestRuntime implementation (+ heap watchdog)
    convert.go            Go <-> Starlark value conversion
    tool.go               RunCodeTool (shuttle.Tool)
    codemode_test.go      see §13
pkg/shuttle/caller.go     WithCaller / CallerFromContext (~20 LOC, only shuttle change)
prompts/tools/code_mode.yaml           prompt id: run_code_with_tools
proto/loom/v1/agent_config.proto       CodeModeConfig message + ToolsConfig field (§7.7)
pkg/agent/config_loader.go             CodeModeConfigYAML + proto mapping (§7.7)
pkg/agent/registry.go                  registration at the builtin-gating site (§7.7)
```

## 7. Component specifications

### 7.1 `runtime.go`

```go
// ExecutorFacade is the only view of the executor the bridge gets.
// Satisfied by both *shuttle.Executor and *shuttle.InstrumentedExecutor.
type ExecutorFacade interface {
    Execute(ctx context.Context, toolName string, params map[string]interface{}) (*shuttle.Result, error)
}

// ToolBinding is one guest-callable function. Call must be safe to invoke
// from host worker goroutines: it holds tool name + facade only, never
// Starlark state (I8).
type ToolBinding struct {
    Name        string              // guest identifier, see §8.1
    Description string              // one line, for the generated preamble
    Schema      *shuttle.JSONSchema // doc generation only; validation stays in the executor
    Call        func(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error)
}

type RunConfig struct {
    Timeout              time.Duration // wall clock; default 300s, cap 600s (shell_execute convention)
    MaxSteps             int64         // Starlark execution steps; default 10_000_000
    MaxOutputBytes       int64         // stdout + serialized result; default 1 MiB, hard cap 4 MiB
    MaxParallel          int           // fan-out width for parallel(); default 8
    MaxToolCalls         int           // total facade calls per run (bindings + call_tool + parallel); default 128
    MemoryHighWaterBytes int64         // coarse process-heap guard; 0 = disabled (default), see §9
}

type RunOutcome struct {
    ResultValue interface{}    // JSON-marshalable value of the `result` global; nil if unset
    Stdout      string         // captured print() output, truncated at MaxOutputBytes
    Truncated   bool
    Steps       int64          // thread.ExecutionSteps() at exit
    ToolCalls   []CallRecord   // ordered log: tool name, duration ms, success, error code
    Err         *shuttle.Error // nil on success; codes in §10
}

type GuestRuntime interface {
    Name() string // "starlark"
    Run(ctx context.Context, code string, bindings []ToolBinding, cfg RunConfig) (*RunOutcome, error)
}
```

`Run`'s second return value is reserved for host-infrastructure failures only; everything script-shaped (compile errors, limits, cancellation) arrives as `RunOutcome.Err`.

`CallRecord` deliberately excludes params and result data: it is a context-cheap summary the model can reason about without re-importing the data the mode exists to keep out. It records order only — a script that needs to *act* on which fan-out items failed must encode that into `result` (§8.3).

### 7.2 `bridge.go`

`NewBridge(exec ExecutorFacade, sharedMemory *storage.SharedMemoryStore) *Bridge`.

`Bridge.Bindings(ctx, tools []shuttle.Tool, opts BindOptions) []ToolBinding` produces one binding per tool with these behaviors:

- **Advertisability filter.** Every binding — the advertised set, `tools`-param additions, and `call_tool` targets — passes the same predicate (`PermissionChecker.Advertisable`, supplied via `BindOptions.Advertisable func(string) bool`). This is the predicate `tool_search` already uses, so the guest surface always equals the discoverable surface. Note this predicate *already* hides approval-gated tools while no interactive approval mechanism exists (§3, §15 Q2), so no separate `requires_approval` exclusion is needed. `BindOptions.AllowApprovalTools` is reserved for the day an interactive mechanism lands; it is inert until then.
- **Exclusions.** Always exclude `run_code_with_tools` itself (no recursion), including via `tools` and `call_tool`.
- **Provenance (I4).** Each `Call` wraps ctx with `shuttle.WithCaller(ctx, runID)` before invoking the facade. `runID` is unique per `run_code_with_tools` invocation.
- **Dereference (I3).** If `Result.DataReference` is set, the bridge fetches the payload from shared memory (session-scoped, same key derivation as `dereferenceLargeParameters`) and hands the guest the full data. The executor pipeline never sets `DataReference` on results (§3), so this shim only fires for tools that natively return references; it is cheap insurance, not a hot path.
- **Result shaping.** The guest receives a dict per call: `{"ok": bool, "data": <converted>, "error": {"code","message","retryable"} | None, "ms": int}`. Failed calls return `ok=False`; they never abort the script (I2). A facade-level error (`err != nil`, e.g. tool-not-found) maps to `ok=False` with `error.code="executor_error"` and the error text as message — the bridge never dereferences a nil `Result`. Scripts that want abort-on-failure write it explicitly.
- **`bind(tool_fn, **kwargs)` builtin.** Returns an opaque *pending call* value: the bridge snapshots the tool name and the kwargs — converted Starlark→Go immediately, on the interpreter thread. A pending call holds no Starlark state.
- **`parallel(pending_calls)` builtin.** Accepts a list of pending-call values **only**; passing any other callable (a lambda, a bound tool function) is a programming error that fails the run with a clear message (`CODEMODE_RUNTIME`: "parallel() accepts only bind(...) values"). This is the normative encoding of I8: `starlark.Thread` is not safe for concurrent use, so worker goroutines must never touch the interpreter. The bridge pre-checks the whole batch against the remaining tool-call budget (a batch larger than the remainder fails the run with `CODEMODE_CALL_LIMIT` *before dispatching anything*), then runs the facade calls through an `errgroup` bounded by `MaxParallel`, gathers Go-valued results in input order, and converts them to Starlark values back on the interpreter thread. Individual failures appear as `ok=False` entries; `parallel` itself only errors on context cancellation. This is the `asyncio.gather` equivalent and the main source of the measured wins.
- **`call_tool(name, **kwargs)` builtin.** Dynamic dispatch for tools that were not bound at run start — the second half of one-run discover-then-call (§15 Q1): a script calls `tool_search` (a normal advertised binding), then `call_tool` on what it found. The name passes the advertisability predicate first; a hidden or unknown-to-policy name returns the same `ok=False` / `error.code="permission_denied"` shape the executor produces, so probing cannot distinguish "hidden" from "denied". Allowed names go through the facade, where `tryDynamicRegistration` resolves registry-only tools and the full admission pipeline applies (I1).
- **Call log and budget.** Every facade call — named binding, `call_tool`, or `parallel` element — appends a `CallRecord` under a mutex (the log is written from worker goroutines) and counts against `MaxToolCalls`. Exceeding the budget fails the run with `CODEMODE_CALL_LIMIT`.

### 7.3 `starlark_runtime.go`

Dependency: `go.starlark.net` (latest tagged release; pure Go).

- Execute with `starlark.ExecFileOptions` and `syntax.FileOptions{Set: true, TopLevelControl: true, GlobalReassign: true, While: false, Recursion: false}`. `TopLevelControl` and `GlobalReassign` are on because models write top-level loops and reassign `result`; `While` and `Recursion` stay off, which preserves Starlark's termination guarantee — every program that stays under the step budget also *halts* (§9, §12).
- Step budget via `thread.SetMaxExecutionSteps(cfg.MaxSteps)`; cancellation wired to `ctx` (goroutine watching `ctx.Done()` calling `thread.Cancel`).
- `print` writes to a size-capped buffer (`MaxOutputBytes`); overflow sets `Truncated` and further writes are dropped, execution continues.
- The script communicates its result by assigning a global named `result`. After execution, the runtime converts `result` (if defined) to Go via `convert.go`. No other globals are read.
- Predeclared environment: the tool bindings, `bind`/`parallel`/`call_tool`, and the `json` and `math` stdlib modules (`go.starlark.net/lib/json`, `lib/math`) — `json.decode` is load-bearing because many tools return JSON-shaped strings in `Data` (§15 Q4). `load()` is disabled; the `time` module stays off (I5: no ambient clock).
- **Heap watchdog (optional).** When `MemoryHighWaterBytes > 0`, a goroutine samples the Go heap (via `runtime/metrics`) every 100ms and calls `thread.Cancel("memory high-water")` past the mark, surfacing as `CODEMODE_MEM_LIMIT`. This is a coarse, process-level guard — the heap is shared with every other session, so false positives are possible under concurrent load; per-run memory *accounting* is a wazero-tier property (§9). The sampler function is injectable for tests.
- Compile errors, `fail()` calls, step-limit exhaustion, memory high-water, and cancellation map to distinct `shuttle.Error` codes (§10). Compile errors include the source position.

### 7.4 `convert.go`

| Go (from `Result.Data` / JSON) | Starlark | Notes |
|---|---|---|
| `nil` | `None` | |
| `bool` | `bool` | |
| `float64` with integral value | `int` | JSON numbers arrive as float64; prefer int when exact |
| `float64` otherwise | `float` | |
| `string` | `string` | |
| `[]interface{}` | `list` | recursive |
| `map[string]interface{}` | `dict` | recursive, string keys |
| other (e.g. structs in `Data`) | JSON round trip first | `json.Marshal` then convert; marshal failure → binding returns `ok=False`, code `CODEMODE_CONVERT` |

Reverse direction (guest → host, for params and `result`): the mirror mapping; Starlark `tuple` → list; non-string dict keys rejected with `CODEMODE_CONVERT`; a Starlark int outside int64 range rejected with `CODEMODE_CONVERT` (Starlark ints are big ints; JSON is not).

All conversions that touch Starlark values run on the interpreter goroutine (I8). Worker goroutines see only the Go side.

### 7.5 `tool.go` — `RunCodeTool`

- `Name()`: `run_code_with_tools`. `Backend()`: `""`.
- `InputSchema()`:
  - `code` (string, required) — Starlark source.
  - `tools` (array of string, optional) — extra tool names to bind beyond the advertised set, for tools already discovered via `tool_search` in a *previous* turn. Each name passes the advertisability predicate at bind time (hidden names are dropped and reported). Names are bound *thin* — no schema/description preamble — and resolve through the normal executor path on first call, so `tryDynamicRegistration` handles registry-only tools and an unresolvable name surfaces as `ok=False` on that call, not as a run failure.
  - `timeout_seconds` (number, default 300, max 600).
  - `max_output_bytes` (number, default 1 MiB, clamped host-side to a 4 MiB hard cap).
- `Execute()`: enumerate bindings **now** — the agent's live tool list at Execute time (the advertised set grows over a session via lazy tools and dynamic registration) ∩ advertisability predicate, plus thin `tools` bindings, minus `run_code_with_tools` — then `GuestRuntime.Run` → map `RunOutcome` to `shuttle.Result`:
  - `Success`: true iff `Outcome.Err == nil`. Script-level tool failures do not fail the run; they are visible in `data.calls`.
  - `Data`: `{"result": ResultValue, "stdout": Stdout, "calls": []CallRecord, "truncated": bool}`.
  - `Metadata`: `runtime`, `steps`, `tool_call_count`, `dropped_bindings` (list of `{name, reason}` with reason ∈ `not_advertisable | reserved_name | collision`). The keys `used_code_mode` and `tool_calls_saved` are **reserved here** for the pattern-effectiveness follow-up (§15 Q3) and must not be repurposed.
  - Large results compose with the existing render pipeline, deliberately: a `Data` payload whose rendered size exceeds `storage.DefaultSharedMemoryThreshold` (16 KiB) reaches the model as an offload stub addressable via `query_tool_result` in the same turn — a script may return a large table precisely so the model can page or SQL-query it. The prompt guidance (§8.4) tells the model to aggregate in-script when it wants the answer inline. `MaxOutputBytes` is the runtime's memory bound, not an inline-visibility promise.
- Description from `prompts/tools/code_mode.yaml` (§8.4), registered under prompt id `run_code_with_tools` and wrapped with `shuttle.NewPromptAwareTool(tool, prompts, "tools.run_code_with_tools")`, matching the `tool_search` convention; code fallback string in the same shape as other builtins.

### 7.6 `pkg/shuttle/caller.go`

```go
type callerKey struct{}

// WithCaller marks ctx as originating from a code-mode run.
func WithCaller(ctx context.Context, runID string) context.Context
// CallerFromContext returns the run ID and true when the call is guest-originated.
func CallerFromContext(ctx context.Context) (string, bool)
```

Admission hooks read it via `AdmissionRequest.Ctx` (already present, §3). The instrumented executor adds `tool.caller` as a span attribute when present (one guarded line; the only touch to existing executor-adjacent code, and it is additive).

Trace-shape reality: because `InstrumentedExecutor` discards `StartSpan`'s returned context (§3), guest-call spans appear as *siblings* of the run's span, not children. The `tool.caller=runID` attribute is therefore the correlation key, and it is sufficient. Re-parenting spans under the run span is explicitly out of scope (§14) — do not "fix" span propagation as a side effect of this change.

### 7.7 Agent wiring and config

Proto first (repo law), then generated code, then YAML mirror, then wiring:

1. **Proto** — new message in `proto/loom/v1/agent_config.proto`, new field in `ToolsConfig` (next free number is 4):

```proto
// ToolsConfig gains:
  // Code-mode orchestration knobs. Enablement is NOT here: listing
  // "run_code_with_tools" in builtin (above) is the switch, matching
  // the tool_search convention. This block only tunes the runtime.
  CodeModeConfig code_mode = 4;

// CodeModeConfig configures the run_code_with_tools guest runtime.
message CodeModeConfig {
  // Guest runtime. Only "starlark" is accepted in v1. Default: "starlark".
  string runtime = 1;

  // Wall-clock limit per run in seconds. Default: 300. Hard cap: 600.
  int32 timeout_seconds = 2;

  // Starlark execution-step budget per run. Default: 10000000.
  int64 max_steps = 3;

  // Cap on stdout + serialized result bytes. Default: 1048576 (1 MiB).
  // Hard cap: 4 MiB.
  int64 max_output_bytes = 4;

  // Fan-out width for parallel(). Default: 8.
  int32 max_parallel = 5;

  // Total tool calls per run (named bindings + call_tool + parallel
  // elements). Default: 128.
  int32 max_tool_calls = 6;

  // Coarse process-heap high-water guard in bytes. 0 = disabled (default).
  // See the security model in docs/architecture/code-mode-orchestration.md §9.
  int64 memory_high_water_bytes = 7;

  // Bind approval-gated tools once an interactive approval mechanism
  // exists. Inert today: the permission path denies rather than blocks.
  // Default: false.
  bool allow_approval_tools = 8;
}
```

2. **Generate** — `buf generate` (and `buf lint` / `buf breaking` as usual).
3. **YAML mirror** — `CodeModeConfigYAML` in `pkg/agent/config_loader.go` with a `code_mode` field on `ToolsConfigYAML` (line 163), mapped in the existing YAML→proto conversion alongside the `GraphMemoryConfig` mappings.
4. **Registration** — one site, in `pkg/agent/registry.go` at the builtin-gating block where `tool_search` registers (lines 962-992): when `"run_code_with_tools"` appears in `config.Tools.Builtin` **and** `config.Behavior.AllowCodeExecution` is true, construct the `Bridge` over the agent's (instrumented) executor and shared-memory store, construct the configured runtime, wrap with `PromptAwareTool`, and `agent.RegisterTool`. Listed-but-flag-false logs a warning and skips registration: `allow_code_execution` acts as the deny-wins kill-switch (this is the flag's first real enforcement — see §12). The advertisability predicate and live tool lister come from the agent itself (`a.permissionChecker.Advertisable`, the same source as `applyPermissionToolFilter`), so no `cmd/looms` changes are needed; server paths that build agents through the registry inherit the tool. Programmatic (non-config) constructions can use the exported `codemode.NewRunCodeTool` + `agent.RegisterTool`.

```yaml
tools:
  builtin:
    - run_code_with_tools   # the enablement switch (tool_search convention)
  code_mode:                # optional knobs; omitted = defaults below
    runtime: starlark
    timeout_seconds: 300
    max_steps: 10000000
    max_output_bytes: 1048576
    max_parallel: 8
    max_tool_calls: 128
    memory_high_water_bytes: 0
    allow_approval_tools: false
behavior:
  allow_code_execution: true   # required; deny-wins kill-switch
```

Conversation-budget relationship: `behavior.max_tool_executions` (default 50) counts loop-dispatched executions, so one code-mode run consumes **one** unit of it regardless of how many guest calls it makes; `max_tool_calls` is the in-run analogue. `Metadata.tool_call_count` exists so a future change can charge guest calls against the conversation budget if a deployment wants that; v1 does not.

## 8. Guest API contract (model-facing)

### 8.1 Naming

Binding names are the tool names passed through the same `toLowerUnderscore` normalization the executor already applies to parameters, with characters outside `[a-z0-9_]` mapped to `_` and a leading digit prefixed with `t_`.

- **Deterministic order.** Tools are sorted by original name (bytewise) before binding, so collision resolution is stable run-to-run: the first name in sorted order wins, the loser is dropped and reported in `metadata.dropped_bindings` with reason `collision`.
- **Reserved names.** `bind`, `parallel`, `call_tool`, `result`, and every identifier in the Starlark universe (`print`, `fail`, `len`, …) are reserved; a tool whose normalized name collides with one is dropped and reported with reason `reserved_name`.

### 8.2 Call semantics

Every bound tool is `fn(**kwargs) -> dict` with the shape in §7.2. Kwargs only; positional args are a runtime error. The executor normalizes parameter keys against the tool schema for script-originated calls exactly as for direct calls.

### 8.3 Program contract

- **The language is Starlark, not Python.** No f-strings (`%` or `.format()`), no `try`/`except` (errors arrive as `ok=False` values — this is why the error model works), no `while`, no recursion, no imports, no classes, and a smaller builtin universe than Python's (e.g. no `sum()`). Comprehensions, `lambda`, `enumerate`, `range`, `sorted`, and `zip` are available.
- Assign the value to return to the model to a global named `result`. It must be composed of convertible types (§7.4).
- `print()` is for diagnostics; it is capped and truncable. `result` is not silently truncated — an oversized `result` fails the run with `CODEMODE_OUTPUT_LIMIT` rather than returning a corrupted value.
- A `result` larger than 16 KiB reaches the model as a pageable offload stub, not inline (§7.5). Aggregate in-script when the answer should be inline; return a table deliberately when the model should page or SQL-query it.
- `parallel([bind(get_expenses, user_id=m["id"], quarter="Q3") for m in team])` is the fan-out idiom. `parallel` accepts only `bind(...)` values.
- The `calls` log records order and outcome only. A script that needs to act on *which* fan-out items failed must carry that in `result` (e.g. a `failed` list), because positional correlation is all the log offers.

### 8.4 Prompt guidance (`prompts/tools/code_mode.yaml`)

Grouped-domain-file convention (§3), prompt id `run_code_with_tools`, lookup key `tools.run_code_with_tools`. The description must state:

- Use this tool when a task needs three or more dependent or parallelizable tool calls, or when raw results need filtering/aggregation before reasoning. Do **not** use it for single lookups — it costs more than a direct call (τ²-bench result, §2).
- The language is Starlark, not Python: no f-strings, no try/except, no while, no imports; not all Python builtins exist. Tool failures return `ok=False` rather than raising. Assign the answer to `result`.
- Advertised tools are callable as snake_case functions. The description does **not** repeat their schemas — those are already in context on the tool definitions themselves; it lists callable names only, plus any name that changed under normalization. (Re-listing descriptions would re-spend the tokens the mode saves.)
- `tool_search` + `call_tool(name, **kwargs)` cover discover-then-call in one run.
- One short worked example, roughly:

```python
# Starlark, not Python: no f-strings, no try/except, no while, no imports.
team = get_team(team_id="eng")
if not team["ok"]:
    fail("get_team: " + team["error"]["message"])
members = team["data"]["members"]
rows = parallel([bind(get_expenses, user_id=m["id"], quarter="Q3") for m in members])
totals = {}
failed = []
for i, r in enumerate(rows):
    if r["ok"]:
        t = 0
        for e in r["data"]["expenses"]:
            t += e["amount"]
        totals[members[i]["name"]] = t
    else:
        failed.append(members[i]["name"])
result = {"totals": totals, "failed": failed}
```

## 9. Security model

- **Tier 1 (this spec): Starlark in-process.** Isolation is capability-based, not memory-based: the language has no ambient I/O, `load()` is disabled, the `time` module is off, and every capability is a binding that re-enters the governed executor (I1, I5). CPU is bounded twice over — step budget plus wall clock — and with `while`/recursion disabled every program terminates independent of the budget. Tool-call volume is bounded by `MaxToolCalls` (I7).
- **Memory is NOT bounded in tier 1.** A single expression — `"x" * (1 << 32)`, `[0] * n` — allocates gigabytes within a handful of interpreter steps; step limits bound time, not space, and upstream go.starlark.net has no allocation accounting. On a multi-session `looms` server, one model-authored line can OOM the process and take down every session. Mitigations, in order of strength: the runtime is opt-in per agent (`tools.builtin` listing) and killable per deployment (`behavior.allow_code_execution=false`); the optional heap high-water watchdog (§7.3) converts most runaway allocation into `CODEMODE_MEM_LIMIT` at the cost of coarse, process-level accuracy; the wazero tier gives hard per-run memory limits. This — not CPU — is the primary reason the hard-isolation tier exists.
- **Tier 1b (follow-on, same interface):** wazero guest for hard memory/CPU limits, aligned with the Tera sandbox decision. No design changes required outside a new `GuestRuntime` implementation.
- **Tier 2 (existing):** container execution via `pkg/docker` / `shell_execute` for workloads needing real Python. Out of scope here.
- **Anti-probing.** `call_tool` on a hidden name returns the exact `permission_denied` shape the executor produces for a denied call, so a script cannot distinguish "exists but hidden" from "denied" (§7.2).
- The generated code is model-authored and must be treated as untrusted regardless of tier. Nothing in the bridge may take a path around `admit`.

## 10. Error codes

| Code | Meaning | `Retryable` |
|---|---|---|
| `CODEMODE_COMPILE` | Starlark parse/resolve error (message includes position) | false |
| `CODEMODE_RUNTIME` | uncaught `fail()`, eval error, or `parallel()` misuse | false |
| `CODEMODE_TIMEOUT` | wall-clock limit | false |
| `CODEMODE_STEP_LIMIT` | step budget exhausted | false |
| `CODEMODE_CALL_LIMIT` | tool-call budget exhausted (incl. batch pre-check, §7.2) | false |
| `CODEMODE_MEM_LIMIT` | heap high-water watchdog fired | false |
| `CODEMODE_OUTPUT_LIMIT` | `result` exceeds `MaxOutputBytes` | false |
| `CODEMODE_CONVERT` | value not convertible at a boundary | false |
| `CODEMODE_CANCELLED` | host context cancelled | true |

Per-call tool errors are not run errors; they surface in call dicts and `calls` records with the tool's own error code (including `permission_denied`), or `executor_error` when the facade itself errored (§7.2).

## 11. Failure and partial-completion semantics

A script that dies mid-way may have completed side-effecting calls. V1 policy: no automatic retry of the run or of individual calls; the `calls` log gives the model an exact record of what executed, and the prompt guidance tells it to prefer idempotent usage. Blast-radius controls, layered: the advertisability filter (what can be named), the admission chain (per-call policy), `MaxToolCalls` with whole-batch pre-check (no partial `parallel` batch is dispatched past the budget), and wall clock. Deployments needing more should express it as admission hooks — the bridge adds no policy of its own. Resumable runs are explicitly out of scope (§14).

## 12. Design trade-offs

**Guest language: Starlark, over goja (JS), wazero, and containers.**

- *Starlark (chosen, tier 1)*: pure Go, CGO-free (matches the `modernc.org/sqlite` posture); deterministic; guaranteed termination with `while`/recursion off; step metering built in (`SetMaxExecutionSteps`); no exception mechanism, which makes the `ok=False` error-as-value contract the *only* error path rather than a convention; tiny capability surface (I5 falls out of the language). Cost: a dialect gap against models' Python priors — every compile-error retry spends a model round trip, which is the resource the feature saves. Mitigations: the §8.4 dialect warning and worked example; the eval harness gates any default-on recommendation (§2).
- *goja (pure-Go JavaScript)*: highest model fluency, but exceptions would re-complicate the denial story (a denied call must not look like a crash — I2), interruption is cooperative rather than step-metered, and the semantic surface (prototypes, getters, `this`) is much larger for the same governance guarantees. Rejected for tier 1; nothing prevents a goja `GuestRuntime` later if the fluency gap dominates in evals.
- *wazero (wasm)*: real memory/CPU hard limits — the correct answer to the tier-1 memory gap — but brings a guest-language toolchain and cold-start weight. Deferred to tier 1b behind the same interface, not rejected.
- *Containers*: real Python, full isolation, heaviest; already exists as tier 2 (`pkg/docker`/`shell_execute`).

**Error model: values, not exceptions.** A raised exception aborts a 20-wide fan-out by default and pushes failure handling back into model context. `ok=False` keeps partial results, matches `shuttle.Result` semantics 1:1, and is enforced by the language (no `try` exists to misuse).

**kwargs-only calls.** Tool schemas name their parameters; positional call syntax would silently misalign on reordering and defeat `normalizeParametersToSchema`.

**`CallRecord` minimalism.** Params and payloads stay out of the log because the executor side already owns the full audit trail (spans, admission stamps); duplicating them into model context would re-import the data the mode exists to exclude.

**In-run caps and admission chain, not one or the other.** `MaxToolCalls`/`MaxParallel` are deterministic, deployment-independent floors; the admission chain remains the place for policy (rate limits, budgets, tenancy). Both apply to every guest call.

**`allow_code_execution` as kill-switch.** `BehaviorConfig.allow_code_execution` has existed as a "security setting" since the field was added but is enforced by no tool today (§3). Code-mode makes it real: listed-but-disallowed does not register. This is deliberately the flag's first enforcement; making `shell_execute` honor it too is a recommended follow-up outside this spec's footprint.

## 13. Test requirements

All in `pkg/shuttle/codemode`, using the existing `shuttle.MockTool` where possible, run with `-tags fts5 -race` (repo law; the parallel and call-log tests exist partly *to* fail under the race detector if I8 breaks). Each is an acceptance criterion.

1. **Denial passthrough (I1/I2).** Executor with a deny-listed tool; script calls it; run succeeds, call dict has `ok=False`, `error.code=="permission_denied"`, and the executor-side result carried the `admission.decision` stamp.
2. **Normalization inside scripts.** Script passes `userId=`; mock tool schema declares `user_id`; tool receives normalized key.
3. **DataReference round trip (I3).** Mock tool natively returns a `DataReference`; guest sees full payload; `RunCodeTool` result does not contain it.
4. **Parallel ordering and bounding.** 20 pending calls, `MaxParallel=4`; results in input order; observed concurrency never exceeds 4; one failing call yields `ok=False` in place without aborting.
5. **Parallel type discipline (I8).** `parallel([lambda: ...])` and `parallel([bound_tool_fn])` fail the run with the `CODEMODE_RUNTIME` misuse message; concurrent call-log appends are race-clean under `-race`.
6. **Step limit and timeout.** Unbounded computation (nested `range` loops) → `CODEMODE_STEP_LIMIT`; sleeping mock tool + short timeout → `CODEMODE_TIMEOUT`; in both, `calls` reflects completed calls.
7. **Tool-call budget.** Budget k, script attempts k+1 sequential calls → `CODEMODE_CALL_LIMIT` with exactly k `calls` entries; a `parallel` batch larger than the remaining budget dispatches zero calls.
8. **Heap watchdog.** Injected memory-sampler above the high-water mark → `CODEMODE_MEM_LIMIT`.
9. **Recursion exclusion.** `run_code_with_tools` absent from bindings even when requested via `tools`; `call_tool("run_code_with_tools", ...)` returns `ok=False`.
10. **Provenance (I4).** Admission hook asserts `CallerFromContext` returns the run ID for guest calls and false for a direct call.
11. **Output caps.** stdout overflow truncates with flag; oversized `result` fails with `CODEMODE_OUTPUT_LIMIT`; a `max_output_bytes` request above the 4 MiB hard cap is clamped.
12. **Advertisability everywhere.** A predicate-hidden tool: absent from named bindings; dropped-and-reported when requested via `tools`; `call_tool` on it returns the uniform `permission_denied` shape.
13. **Naming.** Two tools colliding after normalization bind deterministically (sorted order) with the loser reported; a tool named `parallel` is dropped with reason `reserved_name`.
14. **Facade error mapping.** Executor returning `(nil, err)` (unknown tool) yields `ok=False`, `error.code=="executor_error"`, no panic.
15. **Convert table** property tests for §7.4 both directions, including big-int and non-string-key rejection.
16. **json module.** A mock tool returning a JSON string in `Data` is `json.decode`d and aggregated in-script.

## 14. Non-goals (v1)

Resumable/checkpointed runs; automatic retries; guest access to memory/session APIs beyond bound tools; per-run memory *accounting* (wazero tier); re-parenting guest-call spans under the run span (§7.6); interactive approval flows inside a run (`allow_approval_tools` is inert until an approval mechanism exists at all); wazero and container runtimes (interface only); exposing code-mode over the gRPC surface as anything other than a normal tool; multi-language guests; streaming partial results to the model mid-run.

## 15. Resolved questions

Resolved during v1 review (2026-08-19/20); recorded here with rationale rather than reopened.

1. **Discover-then-call in one run?** Resolved: yes, via two pieces — `tool_search` binds like any advertised tool, and `call_tool(name, **kwargs)` provides dynamic dispatch for what it finds (§7.2). A bindable `tool_search` alone would not have sufficed: bindings are frozen at run start, so a discovered tool has no named guest function. The `tools` param remains for tools discovered in a previous turn.
2. **`requires_approval` tools inside scripts?** Resolved, with a corrected premise: the original question assumed a mid-script HITL *block*, but no blocking approval mechanism exists on this path — `CheckPermission` denies immediately (TODO at `permission_checker.go:163-170`), and `Advertisable` already hides approval-gated tools. So the default advertisability filter handles it with no separate exclusion, and `allow_approval_tools` is reserved-but-inert until an interactive mechanism lands (at which point mid-run blocking becomes a real UX question to answer *then*).
3. **`pattern_effectiveness` signal for code-mode usage?** Resolved: follow-up, as recommended — but the metadata keys `used_code_mode` and `tool_calls_saved` are reserved now (§7.5) so the follow-up needs no schema change.
4. **Starlark stdlib modules?** Resolved: `json` and `math` on in v1 — `json.decode` is load-bearing for string-shaped tool outputs, both modules are pure, and neither adds a capability (I5). `time` stays off (ambient clock).

## 16. References

- Anthropic, "Introducing advanced tool use on the Claude Developer Platform" (PTC, Tool Search, measured results): https://www.anthropic.com/engineering/advanced-tool-use
- Anthropic, "Code execution with MCP": https://www.anthropic.com/engineering/code-execution-with-mcp
- Cloudflare, "Code Mode": https://blog.cloudflare.com/code-mode/
- Wang et al., "Executable Code Actions Elicit Better LLM Agents" (CodeAct): https://arxiv.org/abs/2402.01030
- go.starlark.net (Starlark in Go): https://github.com/google/starlark-go
- Starlark language specification: https://github.com/bazelbuild/starlark/blob/master/spec.md
- goja (pure-Go JavaScript, considered alternative — §12): https://github.com/dop251/goja

## 17. Change footprint

| Area | Files | Estimate |
|---|---|---|
| Proto | `proto/loom/v1/agent_config.proto` (`CodeModeConfig`, `ToolsConfig.code_mode = 4`) + `buf generate` | ~45 proto lines |
| Config mirror | `pkg/agent/config_loader.go` (`CodeModeConfigYAML` + mapping) | ~50 LOC |
| New: `pkg/shuttle/codemode` | 5 source files | 1,100–1,400 LOC |
| New: tests | `codemode_test.go` (+ fixtures) | 600–900 LOC |
| Modified: `pkg/shuttle` | `caller.go` (new), 1 guarded line in `instrumented_executor.go` | ~25 LOC |
| Modified: `pkg/agent` | `registry.go` (registration at the builtin-gating site) | 40–60 LOC |
| New: prompts | `prompts/tools/code_mode.yaml` | 1 file |
| Dependency | `go.starlark.net` | pure Go, no CGO |

Explicitly untouched: `executor.go`, all `admission_*`, `permission_checker.go`, both registries' internals, MCP client code, `cmd/looms`.

Phasing: spike = §7.1–7.4 minimal + tests 1/4/5/6/7 (~2 days); productionize = remainder of §7, §8.4, full test matrix (rest of one week); wazero runtime and the eval harness are separate specs — and the eval harness gates any recommendation to enable code-mode by default (§2).
