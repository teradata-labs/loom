# Lua Script Engine (`pkg/luasandbox`)

**Status**: ✅ engine implemented with tests (`pkg/luasandbox`), on a vendored, patched
golua (`third_party/golua`). ✅ the agent bridge (`Agent.NewLuaHost`) and the builtin
`run_lua` tool (`pkg/agent`), registered only when `tools.lua.enabled` is true (default
false). 📋 Planned: saved scripts and script-backed tools (`manage_lua_scripts`,
`lua_<name>`).

## Overview

An agent often needs several dependent tool calls: run two queries, join them, filter,
retry one that failed. Each call is a full model round trip. The script engine lets the
model send one short Lua program instead. The program runs inside the server process, in
the caller's goroutine, and reaches tools only through a `Host` the embedding code
supplies. This continues the batch-first tool surface (one call doing the work of
several) with control flow added.

The engine is a thin, defensive layer over the pure-Go interpreter
[`github.com/arnodel/golua`](https://github.com/arnodel/golua) v0.3.0 (Lua 5.4,
Apache-2.0), vendored in `third_party/golua` with seven fixes that are proposed upstream
(see "Vendored interpreter" below). Its job is to make an untrusted script safe to run in a shared process:
bounded in time, CPU, memory and tool calls, unable to crash or block the host, and
unable to escape through the Lua standard library.

## Design Goals

1. **Bounded.** Every run ends within its wall budget (plus at most one in-flight tool
   call's timeout). CPU, memory and tool-call budgets are hard limits.
2. **Uncatchable limits.** A script cannot `pcall` its way past a budget kill,
   cancellation or a host failure. Run outcomes say whose fault an ending was:
   `script_error`, `budget_exceeded`, `cancelled`, `host_error` (a `Host` method failed)
   or `engine_error` (the interpreter or this package failed).
3. **No new goroutines, no shared state.** A run executes on the caller's goroutine. Runs
   share nothing mutable, so concurrent runs are race-free.
4. **Never crash the process.** No input, script or host misbehaviour panics the caller or
   exhausts the Go stack.
5. **Interpreter-agnostic API.** No golua type appears in the exported API, so the
   interpreter can be replaced without touching any host.
6. **Fail closed.** Unset limits take defaults, oversized limits take ceilings; there is no
   "unlimited".

## Architecture

### System context

```
┌──────────────────────────────────────────────────────────────────────┐
│                         Server process                               │
│                                                                      │
│  [Model] ─ tool call ─▶ ┌────────────────┐                           │
│                         │  run_lua       │  pkg/agent/lua_tool.go    │
│                         │  + bridge      │  pkg/agent/lua_host.go    │
│                         └───────┬────────┘                           │
│                                 │ luasandbox.Run(ctx, prog, lim, host)│
│                                 ▼                                    │
│                         ┌────────────────┐   Host.CallTool           │
│                         │ pkg/luasandbox │ ─────────────────▶ (Host) │
│                         │  one goroutine │ ◀───────────────── result │
│                         └────────────────┘                           │
│                                                    │                 │
│                                                    ▼                 │
│                                      [Executor: admission chain,     │
│                                       permission checker, tools]     │
└──────────────────────────────────────────────────────────────────────┘
```

The engine never sees the executor. The host decides which tools exist and runs them
through the same guarded path as a direct model call: exact-name match against the
advertised set, `Preflight`, then `Executor.ExecuteWithTool` (see "Agent bridge" below).

### Components

```
┌───────────────────────────────────────────────────────────────────────┐
│                           pkg/luasandbox                              │
│                                                                       │
│  run.go ── Run() ─┬─▶ srcguard.go   nesting scan before parsing       │
│                   ├─▶ stdlib.go     fresh runtime, pure libs only,    │
│                   │                 base loaded per runtime, globals  │
│                   ├─▶ hostfn.go     wrapper for every Go function:    │
│                   │                 cancel check, panic recovery,     │
│                   │                 terminate()                       │
│                   ├─▶ pcall.go      pcall/xpcall that re-raise kills  │
│                   ├─▶ toolslib.go   tools.call/must/list/schema,      │
│                   │                 turn.result, result tables        │
│                   ├─▶ convert.go    Lua⇄Go values, charged, bounded   │
│                   └─▶ output.go     capped print/log capture          │
│                                                                       │
│  limits.go  {Limits} defaults, ceilings, Normalize, Clamp             │
│  policy.go  {Policy} tool visibility by trust tier, hard deny list    │
│  gate.go    Gate (fail-fast concurrency), DeriveCapacity              │
│  types.go   Program, Host (interface), CallResult, RunResult          │
└───────────────────────────────────────────────────────────────────────┘
```

### Run sequence

```
Caller            Run                 golua VM              Host
  │                │                     │                    │
  ├─ Run(ctx) ────▶│ normalize limits    │                    │
  │                │ scan source shape   │                    │
  │                │ new runtime, libs   │                    │
  │                ├─ compile ──────────▶│                    │
  │                ├─ CallContext(limits)▶                    │
  │                │                     ├─ tools.call ──┐    │
  │                │                     │  wrap: ctx ok?│    │
  │                │                     │  budget ok?   │    │
  │                │                     ├───────────────┴───▶│ CallTool(ctx w/ timeout)
  │                │                     │◀──────────── result┤
  │                │                     │  charge memory, build result table
  │                │                     │  ... script continues ...
  │                │◀─ return / kill ────┤                    │
  │                │ classify outcome    │                    │
  │                │ convert return value (bounded)           │
  │◀─ RunResult ───┤ close runtime       │                    │
```

### Termination flow

golua enforces hard limits by panicking with `ContextTerminationError` inside the VM; the
nearest resource context recovers it. Each `pcall` opens a child context, so a stock
`pcall` would absorb the kill and let the script continue. The engine's `pcall`/`xpcall`
re-raise in the parent after the protected call returns, level by level, up to the root:

```
root context (limits)          ← CallContext in Run: records the outcome
  └─ pcall child               ← re-raises (pcall.go: afterProtected)
       └─ xpcall child         ← re-raises
            └─ pcall child     ← kill or cancellation happens here
```

The engine's own reasons for ending a run (cancellation, wall budget seen at a host
call, tool-call budget, host failure) are recorded in the run state before raising, so
classification never depends on parsing golua's message text.

## Key Design Decisions

### Interpreter choice

| Option | Memory cap | Stop a loop | Notes |
|---|---|---|---|
| **golua v0.3.0** (chosen) | yes, per run | wall and CPU-tick limits | quota API marked alpha upstream |
| gopher-lua v1.1.2 | none | Go context | most widely used; `string.rep` allocated 200 MB in 8 ms with nothing able to stop it |
| Shopify go-lua | none | none built in | Lua 5.2 |
| WebAssembly runtime | yes | yes | not Lua; larger dependency |

A script must not be able to take the pod down by allocating memory, and only golua
offers a hard per-run memory limit. Its quota API is labelled alpha, so the engine
pins the version, keeps golua imports inside this package, and turns every measured
behaviour it relies on into a regression test.

### Cancellation

golua has no `context.Context` integration, and its stop API (`SetStopLevel`,
`KillContext`) is unusable from another goroutine. The vendored golua adds an
`Interrupt` (upstream PR #133): a flag any goroutine may set, checked on every CPU
charge of the context it is installed in. The engine installs one in the run's root
context and arms it with `context.AfterFunc(runCtx, ...)`, whose goroutine only sets the
flag. A script is therefore stopped at its next interpreter step when the caller cancels,
even if it only computes. Host functions also check `ctx.Err()` on entry and after any
blocking work, so a cancelled run never starts another tool call.

### Source shape guard

golua's parser and compiler are recursive descent with no depth limit. Measured: 1 MiB of
`((((…` or `- - - …` needs more than 128 MiB of Go stack to compile, and Go aborts the
whole process (not just the goroutine) when a stack passes 1 GiB. Reference Lua limits
syntax nesting to 200 levels. `checkSourceShape` tokenizes with golua's own (iterative)
scanner before parsing and rejects:

- more than 200 brackets and blocks open at once;
- more than 2000 tokens in one expression run (operator chains `a..b..c`, unary chains,
  index and call chains `a.b.c`, `f()()`), which nest in the parse tree without
  brackets. An identifier directly after a complete operand starts a new statement and
  resets the run, so long scripts of short statements pass.

### Race-free runtime setup

Upstream golua's `base.Load` marked package-level function values (`next`, the `ipairs`
iterator) every time a runtime was created: a data race whenever two runs start at once,
or one starts while another iterates a table (found by `TestConcurrentRunsShareNothing`
under `-race`). The vendored golua declares them once at init (upstream PR #131), so the
engine loads the base library per run. The `package` library is not loaded; `string`,
`table`, `math` and `utf8` are loaded per run.

### Vendored interpreter

Building and attacking the engine (an independent adversarial review, every finding
reproduced in a separate process) turned up six defects in golua v0.3.0 that an embedder
cannot fix safely from outside. A seventh came from CodeQL, once the vendored source was
inside the repository. They are fixed in `third_party/golua` and proposed
upstream; when upstream releases them, the directory can be replaced by the module again.

| Defect in v0.3.0 (measured) | Fix | Upstream PR |
|---|---|---|
| A metamethod the VM calls directly (`__index`/`__newindex` functions, operators, comparisons, `__len`, `__concat`, `__close`) runs in a nested interpreter loop that no depth limit counted; ten one-line self-triggering scripts each aborted the process at 1 GB of Go stack | nested loops limited to 1000, a catchable "stack overflow" (under 8 MiB of stack at the limit) | [#132](https://github.com/arnodel/golua/pull/132) |
| Tables charged 8% to 29% of what they allocate; real memory reached 10.5 times the budget | tables charged what they allocate (91% to 100% of Go's own count) | [#135](https://github.com/arnodel/golua/pull/135) |
| No way to stop a running script from another goroutine | `Interrupt`; `PopContext` restores the parent before charging it | [#133](https://github.com/arnodel/golua/pull/133) |
| `base.Load` data race between concurrent runtimes | compliance declared once, at init | [#131](https://github.com/arnodel/golua/pull/131) |
| Parser and compiler recursion unbounded | 400 syntax levels, matching the reference suite's limits test | [#134](https://github.com/arnodel/golua/pull/134) |
| `string.format("%p")` without an argument panicked | returns an error | [#130](https://github.com/arnodel/golua/pull/130) |
| 64-bit integers narrowed with a plain `int(n)`, which wraps where `int` is 32 bits (CodeQL reported 26 sites): on linux/386 integer literals were miscompiled, positions wrapped, and `select` and table growth panicked; on every platform `math.ldexp(2.0, math.maxinteger)` returned 0 | conversions saturate or compare in `int64`, `math.ldexp` bounds its exponent, and every narrowing has its bound check next to it; the Go tests and the reference suite pass on linux/386 | [#136](https://github.com/arnodel/golua/pull/136) |

Before the fixes were in place the engine carried workarounds: function-valued
VM metamethods were refused, capacity planning assumed 12 times the budget, and
cancellation of pure computation waited for the CPU budget. All three are gone.

### Memory accounting at the boundary

golua charges values the VM creates, but not values Go code creates. Every string and
table the engine hands to a script (`args`, tool results, decoded JSON, schemas, metatable copies) is charged its
real cost first (string length plus header, table overhead, per-entry cost). A nested
call's result above `MaxCallResultBytes` is truncated before conversion (text keeps head
and tail; structured data becomes a summary with a JSON preview). Converting Lua values to
Go stops at the first entry that cannot fit and charges CPU per entry visited, so
`json.encode` of a huge table fails fast instead of building uncharged temporaries.

### Agent bridge (`pkg/agent`)

`Agent.NewLuaHost(ctx, LuaHostOptions)` builds the `Host` for one run, and `RunLuaTool`
(`run_lua`) calls it. Every rule below fails closed.

- **Visible set.** The bridge uses the exact tools the model was shown for the provider
  call that requested the run. `dispatchOneCall` records that projection on each tool
  call's context: `advertisedTools(session)` after `recovery.activeTools`, so
  circuit-broken, permission-disabled and not-yet-activated tools are excluded. The
  bridge never re-derives the set, which could widen it, and refuses to start when the
  context carries none.
- **Filtering.** `Policy.Visible(projection, trust)` then applies the engine's hard-deny
  list, the reserved names (`run_lua`, `manage_lua_scripts`, `lua_*`, added by the bridge
  whatever the host configured), `Deny`, `DenyForShared` for scripts the runner did not
  write, and `Allow`. A saved script's `requires` narrows the set further.
- **Admission.** A nested call must match the visible set by exact name; an unknown name
  never reaches `Executor.Execute`, whose dynamic registration could pull in a tool the
  model never saw. `Preflight` runs first with any approval grant hidden
  (`shuttle.ContextWithoutAskGrant`): `Ask` returns `approval_required` and `Deny`
  returns `permission_denied`, so a script neither inherits an approval nor waits for a
  human. The call then runs through `ExecuteWithTool` under a deny grant, so a hook that
  answers `Ask` only at execution time (state changed since `Preflight`) is resolved at
  once from the grant instead of reaching the blocking resolver.
- **Guards.** `NewLuaHost` and `RegisterRunLuaTool` refuse when the executor has neither
  an admission chain nor a permission checker (`Executor.HasAdmissionGuards`), and when
  the call has no session.
- **Not done by the bridge:** tool rows, deduplication, the circuit breaker and the
  per-turn tool budget. Those belong to model calls, and the script is one model call.
  Each nested call gets a `lua.tool_call` span, and the run a `lua.run` span.

`run_lua` takes a fail-fast gate slot (returned on every path), resolves saved scripts
through a `ScriptResolver` function supplied by the host (nil until saved scripts
exist), clamps `timeout_seconds` to the policy, and maps each outcome to an error code.
`Result.Metadata["lua.run"]` carries the run record for audit.

On `looms serve`, the `tools.lua` block (`cmd/looms/lua_tools.go`) builds one policy and
one gate for the server. The gate is sized by `DeriveCapacity` from the process memory
limit, and `max_concurrent_runs` can only lower it. `run_lua` registers on agents that
list it in `tools.builtin`, through serve's own agent loops and through
`RegistryConfig.RunLuaTool` for registry-built agents.

## Guarantees and their tests

| Guarantee | Test |
|---|---|
| Budget kills for loops, table growth, `string.rep`, doubling, `table.concat`, `gsub`, `string.format`, `string.pack`, `__tostring`, huge error values, deep recursion, finalizer and `<close>` loops | `TestBudgetsStopHostileScripts` |
| Kills inside `pcall` still end the run | same, `memory bomb inside pcall`, `loop inside pcall` |
| Cancellation passes three nested `pcall`/`xpcall` levels with nothing printed | `TestCancelPassesThroughEveryPcall` |
| Cancellation during a tool call and during `time.sleep` | `TestCancelDuringToolCall`, `TestCancelDuringSleep` |
| A caller deadline reports as cancellation, a wall budget as a budget | `TestCallerDeadlineIsCancellation`, `TestWallBudgetDuringToolCall` |
| Tool-call budget cannot be dodged with `pcall` | `TestToolCallBudget` |
| Host panics, errors and nil results end the run as `host_error`; panic text stays out of model-facing errors | `TestHostFailuresEndTheRun` |
| Tool results are charged to memory | `TestToolResultsAreChargedToMemory` |
| Dangerous globals absent; pure libraries work under all limits | `TestDangerousGlobalsAreAbsent`, `TestPureLibrariesWorkUnderLimits` |
| Deep nesting rejected before parsing; ordinary code accepted | `TestSourceGuardRejectsDeepNesting`, `TestSourceGuardAcceptsOrdinaryCode` |
| No goroutine outlives a run | `TestNoGoroutineOutlivesARun` |
| Concurrent runs share nothing (run with `-race`) | `TestConcurrentRunsShareNothing` |
| Forbidden golua APIs and libraries never used | `TestForbiddenAPIs` |
| golua limit messages the classifier depends on | `TestGoluaLimitMessagesArePinned` |
| Arbitrary source never panics, hangs or yields an unknown outcome | `FuzzRun` (2.7 million executions in 60 s, no failure; the corpus includes the metamethod-recursion class) |
| Metamethod recursion is a catchable error, not a process crash | `TestMetamethodRecursionIsACatchableError` (each script aborted the process on upstream v0.3.0) |
| Metamethods of every kind work | `TestMetamethodsWork` |
| Cancellation stops pure computation immediately, through nested `pcall` | `TestPureComputeCancellationIsImmediate` |
| Internal panics end the run as `engine_error`, not `host_error` | `TestInternalPanicsAreEngineErrors`, `TestFormatPWithoutArgumentIsAnError` |
| Error text is bounded and carries no heap addresses | `TestErrorTextIsBounded` |
| Converting a huge table fails fast | `TestConversionIsBoundedBeforeAllocating` |
| Nesting limits cannot multiply across levels; long flat scripts pass | `TestSourceGuardBoundsTotalNesting`, `TestSourceGuardAllowsLongFlatScripts` |

## Measured Behaviour

Apple M-series, `CGO_ENABLED=0`, vendored golua (probes on Go 1.25.3, tests on Go 1.26.5):

| Measurement | Value |
|---|---|
| Runtime creation | about 45 µs |
| CPU ticks per second of plain Lua | about 2×10⁸ (call-heavy code about 6×10⁷) |
| Peak process memory of a budget-killed run, tables | 0.8× to 1.25× the budget |
| Peak process memory of a budget-killed run, small strings and closures | 2.0× to 2.7× the budget (golua does not charge string headers or the full closure size) |
| Go-function and nested-loop nesting | capped at 1000 levels ("stack overflow" Lua error) |

`DeriveCapacity` plans for 3× (the worst measured ratio, rounded up): concurrent runs at
their worst-case peak may use at most 40% of the process memory limit. The default
per-run budget is 128 MiB (about 384 MB worst case); a 4 GiB pod runs 4 at once.

## Known Limitations

- **Memory is total allocation.** Garbage collection does not credit the budget, and
  small strings and closures hold up to about 2.7× their charge in real memory. Capacity
  planning uses 3×.
- **No coroutines.** golua runs each coroutine on its own goroutine and suspended ones
  survive `Runtime.Close`, so the library is not loaded. (Upstream golua's coroutine
  tests also report data races under `-race`; the engine never reaches that code.)
- **Lua 5.4 only, golua dialect.** `error` inside `pcall` prefixes a position where
  reference Lua does not; numbers are not coerced to strings for tool names.
- **JSON nulls.** Object fields that are null are dropped; null array elements become
  `json.null` so positions survive. An empty table encodes as `{}`.
- **YOLO counts as a guard.** loom defaults to `tools.permissions.yolo: true`, which
  installs a permission checker that admits every call. `run_lua` then registers, and
  its scripts are exactly as guarded as direct calls: not at all. Configure
  `tools.hooks` (or turn YOLO off) before enabling `tools.lua` on a shared server.

## Upstream Status

The seven fixes above are open as upstream PRs #130 to #136 from the `ilsiepotamus/golua`
fork. Not proposed: `SetStopLevel(HardStop)` on a context that is not current kills it
immediately, raising the termination from the current context, so a `pcall` swallows it
and the stopped context is left killed and current. The engine does not use
`SetStopLevel`. Upstream's coroutine data races (above) are also not addressed.

## Related Work

- Batch-first builtin tools: `execute_query`, `edit_files`, batched `file_read` /
  `file_write` (loom PR #329).
- Admission chain and `AskGrant`: `pkg/shuttle/admission_*.go`, `ask_grant.go`. The bridge
  strips any grant with `shuttle.ContextWithoutAskGrant` for `Preflight`, so an approval
  granted for one call never covers the calls a script makes.
- HITL park and resume: `docs/architecture/hitl-park-and-resume.md`. Scripts do not park;
  a tool that needs approval returns `approval_required` to the script.
