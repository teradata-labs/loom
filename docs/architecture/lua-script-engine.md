# Lua Script Engine (`pkg/luasandbox`)

**Status**: ✅ engine implemented with tests (`pkg/luasandbox`). 📋 Planned: the agent
bridge and a builtin tool that runs scripts (next PR; tool name not final), saved scripts
and script-backed tools (the PR after). Everything is off by default and nothing registers a tool yet.

## Overview

An agent often needs several dependent tool calls: run two queries, join them, filter,
retry one that failed. Each call is a full model round trip. The script engine lets the
model send one short Lua program instead. The program runs inside the server process, in
the caller's goroutine, and reaches tools only through a `Host` the embedding code
supplies. This continues the batch-first tool surface (one call doing the work of
several) with control flow added.

The engine is a thin, defensive layer over the pure-Go interpreter
[`github.com/arnodel/golua`](https://github.com/arnodel/golua) v0.3.0 (Lua 5.4,
Apache-2.0). Its job is to make an untrusted script safe to run in a shared process:
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
│                         │  Host tool     │  (script tool, planned)   │
│                         │  (agent side)  │                           │
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
through the same guarded path as a direct model call (planned bridge: exact-name match
against the advertised set, `Preflight`, then `Executor.ExecuteWithTool`).

### Components

```
┌───────────────────────────────────────────────────────────────────────┐
│                           pkg/luasandbox                              │
│                                                                       │
│  run.go ── Run() ─┬─▶ srcguard.go   nesting scan before parsing       │
│                   ├─▶ stdlib.go     fresh runtime, pure libs only,    │
│                   │                 shared base functions, globals    │
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

### Cancellation without Go-context support

golua has no `context.Context` integration, and its stop API (`SetStopLevel`,
`KillContext`) panicked and wedged a probe when called from a host function. The engine
uses two mechanisms only:

1. golua hard limits bound pure computation (`Millis` = time until the run deadline,
   `Cpu`, `Memory`).
2. Every host function checks `ctx.Err()` on entry and after any blocking work, and calls
   `TerminateContext` on the VM goroutine. The re-raising `pcall` carries it to the root.

There is no watcher goroutine. Cancellation is seen at the next host call or `pcall`
return; a script that only computes is stopped by its CPU or wall budget instead.

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

golua's `base.Load` marks package-level function values (`next`, the `ipairs` iterator)
every time a runtime is created. Two runs starting at once, or one starting while another
iterates a table, is a data race (found by `TestConcurrentRunsShareNothing` under
`-race`). The engine loads the base library once into a template runtime and copies an
allowlist of its function values into each run; the values receive their runtime as an
argument, so sharing them is safe once nothing writes to them. The `package` library is
not loaded at all; `string`, `table`, `math` and `utf8` are loaded per runtime because
their loaders create fresh function values and keep their state in the runtime.

### Metamethods the VM calls directly are not available

golua calls some metamethods straight from the VM (`__index` and `__newindex` when they are
functions, arithmetic and bitwise operators, `__concat`, `__len`, `__eq`, `__lt`, `__le`,
`__close`). Each such call runs a nested interpreter loop on the Go stack, and none of
golua's depth limits count it: its 1000-level guard counts only Go library functions. A
metamethod that triggers its own event recurses until the Go stack passes 1 GiB, and Go
aborts the whole process. Measured: ten one-line scripts, one per event, each crashed the
process within a second under the default 256 MiB budget, because memory charges grew
slower than the stack.

The engine therefore replaces `setmetatable` and `getmetatable`. Those events may not be
functions (`__index` and `__newindex` may be tables, whose chains golua bounds at 100
links). The installed metatable is a copy, and `getmetatable` returns a copy, so a script
cannot add a forbidden metamethod after the check. Metamethods reached through a Go
library function (`__tostring` via `tostring`, `__pairs` via `pairs`, `__call`, `__gc`)
pass golua's guard and stay available. Prototype objects (`Class.__index = Class`) work
unchanged.

A patched interpreter that counts nested VM loops would lift this restriction.

### Memory accounting at the boundary

golua charges values the VM creates, but not values Go code creates, and it undercharges
small tables: 16 bytes per entry and nothing for an empty table, where Go spends about 56
bytes per hash slot plus doubling growth. Every string and table the engine hands to a
script (`args`, tool results, decoded JSON, schemas, metatable copies) is charged its
real cost first (string length plus header, table overhead, per-entry cost). A nested
call's result above `MaxCallResultBytes` is truncated before conversion (text keeps head
and tail; structured data becomes a summary with a JSON preview). Converting Lua values to
Go stops at the first entry that cannot fit and charges CPU per entry visited, so
`json.encode` of a huge table fails fast instead of building uncharged temporaries.

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
| Metamethod recursion cannot overflow the Go stack | `TestMetamethodRecursionCannotCrashTheProcess` (each script crashed the process before the restriction) |
| Allowed metatable patterns (prototypes, table chains, `__tostring`, `__call`, `__pairs`) work; metatables cannot be changed after the check | `TestAllowedMetatablePatterns`, `TestMetatablesAreCopied`, `TestMetamethodRules` |
| Interpreter panics end the run as `engine_error`, not `host_error` | `TestInterpreterPanicsAreEngineErrors` |
| Error text is bounded and carries no heap addresses | `TestErrorTextIsBounded` |
| Converting a huge table fails fast | `TestConversionIsBoundedBeforeAllocating` |
| Nesting limits cannot multiply across levels; long flat scripts pass | `TestSourceGuardBoundsTotalNesting`, `TestSourceGuardAllowsLongFlatScripts` |

## Measured Behaviour

Apple M-series, `CGO_ENABLED=0`, golua v0.3.0 (probes on Go 1.25.3, tests on Go 1.26.5):

| Measurement | Value |
|---|---|
| Runtime creation | about 45 µs |
| CPU ticks per second of plain Lua | about 2×10⁸ |
| Peak process memory of a budget-killed run, values the engine creates | 1.4× to 2.1× the budget |
| Peak process memory of a budget-killed run, values the VM creates | 3× (arrays) to 10.5× (tables of tables) the budget; growth by doubling and golua's per-entry undercharge; `GOMEMLIMIT` does not lower it |
| Go-function nesting (pcall, `gsub` and `sort` callbacks) | capped by golua at 1000 levels ("stack overflow" Lua error) |
| CPU budget as time | about 10 s for arithmetic loops, about 36 s for call-heavy loops, at the default 2×10⁹ ticks |

`DeriveCapacity` plans for 12× (the worst measured ratio, rounded up): concurrent runs at
their worst-case peak may use at most 40% of the process memory limit. The default
per-run budget is 64 MiB (about 770 MB worst case).

## Known Limitations

- **Cancellation of pure computation** waits for the CPU or wall budget (10 to 40 seconds
  of CPU at the defaults). Fixing this needs an interrupt hook in golua.
- **Memory is total allocation, and golua undercounts tables.** Garbage collection does
  not credit the budget, and a script building many small tables holds up to about 10×
  its budget in real memory. Capacity planning uses 12×.
- **No function-valued VM metamethods** (see above).
- **No coroutines.** golua runs each coroutine on its own goroutine and suspended ones
  survive `Runtime.Close`, so the library is not loaded.
- **Lua 5.4 only, golua dialect.** `error` inside `pcall` prefixes a position where
  reference Lua does not; numbers are not coerced to strings for tool names.
- **JSON nulls.** Object fields that are null are dropped; null array elements become
  `json.null` so positions survive. An empty table encodes as `{}`.

## Upstream Issues Worth Reporting to golua

1. `base.Load` writes package-level `GoFunction` safety flags on every runtime: a data
   race for any program that creates runtimes concurrently.
2. The parser and compiler recurse without a depth limit; deeply nested source can
   exhaust the Go stack.
3. There is no way to interrupt a running VM from another goroutine.
4. Metamethods called from the VM run nested interpreter loops that no depth limit
   counts; self-triggering metamethods abort the process.
5. Table memory is undercharged (16 bytes per entry, nothing per table), so the memory
   limit admits about 10× its value in real memory.
6. `string.format("%p")` without an argument panics (index out of range).

## Related Work

- Batch-first builtin tools: `execute_query`, `edit_files`, batched `file_read` /
  `file_write` (loom PR #329).
- Admission chain and `AskGrant`: `pkg/shuttle/admission_*.go`, `ask_grant.go`. The engine's
  planned bridge strips any grant with `shuttle.ContextWithoutAskGrant`, so an approval
  granted for one call never covers the calls a script makes.
- HITL park and resume: `docs/architecture/hitl-park-and-resume.md`. Scripts do not park;
  a tool that needs approval returns `approval_required` to the script.
