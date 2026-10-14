# Lua Script API Reference

**Status**: ✅ implemented in `pkg/luasandbox` (engine and Lua-facing API). 📋 Planned:
the builtin `run_lua` tool that exposes it to models. Design and rationale:
[architecture/lua-script-engine.md](../architecture/lua-script-engine.md).

**Interpreter**: `github.com/arnodel/golua` v0.3.0, Lua 5.4.

---

## Go API

### `Run`

```go
func Run(ctx context.Context, p Program, lim Limits, h Host) *RunResult
```

Runs `p` on the caller's goroutine. Never returns nil, never panics. Returns when the
script finishes, a limit fires, or `ctx` ends (seen at the script's next host call or
`pcall` return; CPU and wall budgets bound everything else).

### `Program`

| Field | Type | Meaning |
|---|---|---|
| `Name` | `string` | Chunk name in error messages. Empty means `inline`. |
| `Source` | `string` | Lua source. Rejected above `MaxSourceBytes` or when its nesting is too deep. |
| `Args` | `map[string]any` | Becomes the global `args`. Non-JSON values go through `encoding/json`. |
| `Info` | `map[string]any` | Merged into the global `script`; `script.name` is always `Name`. |

### `Limits`

Zero or negative fields take the default; larger values are lowered to the ceiling
(`Limits.Normalize`). `Limits.Clamp(req)` lets a request lower any field, never raise it.

| Field | Default | Ceiling | Bounds |
|---|---|---|---|
| `Wall` | 120 s | 600 s | the whole run, including tool calls and sleeps |
| `CPUTicks` | 2,000,000,000 | 10,000,000,000 | interpreter steps (about 2×10⁸ per second) |
| `MemoryBytes` | 128 MiB | 512 MiB | allocation, including values the host hands in (real memory can reach about 2.7× for scripts holding many small strings or closures) |
| `MaxToolCalls` | 100 | 1000 | `tools.call` + `tools.must` + `turn.result` |
| `ToolCallTimeout` | 60 s | 300 s | one nested call |
| `MaxCallResultBytes` | 1 MiB | 8 MiB | one nested call's result (truncated above) and arguments (error above) |
| `MaxOutputBytes` | 16 KiB | 256 KiB | captured `print`/`log` output (head and tail kept) |
| `MaxResultBytes` | 256 KiB | 4 MiB | the converted return value (truncated above) |
| `MaxSourceBytes` | 64 KiB | 1 MiB | source text |
| `MaxSleep` | 30 s | 120 s | one `time.sleep` |

### `Host`

```go
type Host interface {
    ListTools(ctx context.Context) ([]ToolInfo, error)
    ToolSchema(ctx context.Context, name string) (map[string]any, error) // ErrToolNotVisible when hidden
    CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error)
    TurnResult(ctx context.Context, messageID int64) (*CallResult, error)
    Progress(ev ProgressEvent) // must not block
}
```

All methods run on the goroutine that called `Run`. A Go error, a panic, or a nil
`*CallResult` from `CallTool` / `TurnResult`, and any error other than
`ErrToolNotVisible` from `ListTools` / `ToolSchema`, ends the run with
`OutcomeHostError`. A panic in `Progress` is recorded in `RunResult.Detail` and
otherwise ignored. A Go panic inside the interpreter or this package ends the run with
`OutcomeEngineError`.

`CallResult{OK: false, Error: ...}` is an ordinary tool failure that the script sees.
`CallResult.Decision` is copied into the call ledger.

### `RunResult`

| Field | Meaning |
|---|---|
| `Outcome` | `ok`, `script_error`, `budget_exceeded`, `cancelled`, `host_error` (a Host method failed), `engine_error` (the interpreter or this package failed) |
| `Value` | first return value as JSON-compatible Go values (`nil`, `bool`, `int64`, `float64`, `string`, `[]any`, `map[string]any`) |
| `Output` | captured `print`/`log` output |
| `Error` | model-facing message: `chunk:line: text` for script errors, the limit for budget errors. At most 2 KiB; heap addresses (`table: 0x...`) removed |
| `Limit` | `cpu`, `memory`, `wall` or `tool_calls` when `Outcome` is `budget_exceeded` |
| `Detail` | diagnostics for server logs only (panic values, stacks), at most 64 KiB. Never show it to a model or user. |
| `Used` | `CPUTicks`, `MemoryBytes` (from the interpreter), `WallMillis` (`int64`) |
| `Calls` | one `CallRecord{Tool, OK, Code, Millis, Decision}` per nested call |
| `Truncated` | `Output`, `Value`, `CallResults` flags |

A caller deadline sooner than `Wall` reports as `cancelled`, not as a wall budget.

### `Gate`

```go
func NewGate(maxTotal, maxPerKey int) *Gate
func (g *Gate) TryAcquire(key string) (release func(), ok bool) // never blocks
func (g *Gate) Resize(maxTotal, maxPerKey int)
func (g *Gate) Stats() (inUse, maxTotal, maxPerKey int)
func DeriveCapacity(memLimit, perRunBytes uint64) (slots int, runBytes uint64)
func ProcessMemoryLimit() uint64 // cgroup limit, else GOMEMLIMIT, else 1 GiB
```

`DeriveCapacity` returns as many slots (2 to 16) as fit when every run sits at its
worst-case peak, `PeakMemoryOverhead` (3) times its budget, within 40% of `memLimit`.
When two slots do not fit, it lowers the per-run budget instead (floor 16 MiB).

| Process memory | Per-run budget asked | Slots | Per-run budget returned |
|---|---|---|---|
| 4 GiB | 64 MiB | 8 | 64 MiB |
| 4 GiB | 128 MiB | 4 | 128 MiB |
| 4 GiB | 256 MiB | 2 | 256 MiB |
| 1 GiB | 64 MiB | 2 | 64 MiB |
| 1 GiB | 128 MiB | 2 | about 68 MiB |

### `Policy`

```go
type Policy struct {
    Limits           Limits
    Allow            []string // non-empty: only these tools
    Deny             []string // removed for every script
    DenyForShared    []string // removed for TrustShared and TrustPublished scripts
    Reserved         []string // the host's own script tools: always denied
    ReservedPrefixes []string // e.g. the prefix of script-backed tools: always denied
}
func (p Policy) Check(name string, trust Trust) (code string, ok bool)
func (p Policy) Visible(advertised []string, trust Trust) []string
func (p Policy) Validate() error
func HardDeny() []string
```

Entries are exact names or `path.Match` patterns. `Check` returns `HARD_DENIED` for
hard-denied, reserved and reserved-prefix names, `TOOL_NOT_VISIBLE` otherwise. Trust
tiers: `TrustInline`, `TrustOwn`, `TrustShared`, `TrustPublished`.

Hard deny list (no policy can allow these): `activate_tool`, `contact_human`,
`delegate_to_agent`, `manage_ephemeral_agents`, `manage_skills`, `query_tool_result`,
`tool_search`.

---

## Lua API

### Libraries

Available: `string`, `table`, `math`, `utf8`, and these base functions: `assert`,
`error`, `getmetatable`, `ipairs`, `next`, `pairs`, `pcall`, `print`, `rawequal`,
`rawget`, `rawlen`, `rawset`, `select`, `setmetatable`, `tonumber`, `tostring`, `type`,
`xpcall`, plus `_G` and `_VERSION`.

Absent (`nil`): `require`, `package`, `load`, `loadstring`, `dofile`, `loadfile`,
`collectgarbage`, `warn`, `coroutine`, `io`, `os`, `debug`, `string.dump`.

`pcall` and `xpcall` catch Lua errors only. A budget kill, cancellation or host failure
inside them ends the whole run.

Metamethods of every kind are available. A metamethod that triggers its own event (an
`__index` function reading the missing key again) ends with a catchable `stack overflow`
error after 1000 levels.

### `tools`

| Call | Returns | Errors (Lua) |
|---|---|---|
| `tools.call(name, args)` | result table (below) | bad name; `args` not a table of named fields; args contain a function or exceed `MaxCallResultBytes` |
| `tools.must(name, args)` | `result.data` | as `tools.call`, plus `"<name>: <code>: <message>"` when the tool fails |
| `tools.list()` | `{{name=, description=}, ...}` | none; read once per run |
| `tools.schema(name)` | JSON Schema table, or `nil` when the tool is not visible | bad name |

`tools.call`, `tools.must` and `turn.result` count toward `MaxToolCalls`; going past it
ends the run.

Result table:

| Field | Present | Meaning |
|---|---|---|
| `ok` | always | `true` when the tool succeeded |
| `ms` | always | duration in milliseconds |
| `data` | when the tool returned data | a string, or a table. A string holding exactly one JSON object or array is decoded. |
| `text` | when the tool returned a string | the string as returned |
| `error` | when `ok` is false | `{code, message, suggestion, retryable}` |
| `truncated`, `bytes` | when the result exceeded `MaxCallResultBytes` | text keeps head and tail; structured data becomes `{truncated=true, bytes=N, preview=<2 KiB of JSON>}` |

A call that runs past `ToolCallTimeout` returns `ok=false` with code
`TOOL_CALL_TIMEOUT` and `retryable=true`.

### `turn`

| Call | Returns |
|---|---|
| `turn.result(message_id)` | same shape as `tools.call`; reads a tool result from earlier in the current turn. `message_id` must be a positive integer. |

### `json`

| Call | Behaviour |
|---|---|
| `json.encode(value)` | string. Sequences (keys `1..n`) become arrays; other tables objects; `{}` becomes `{}`. No HTML escaping. NaN and infinities fail. |
| `json.decode(string)` | value. Integers stay integers. Null object fields are dropped; null array elements become `json.null`. Trailing data is an error. |
| `json.null` | sentinel for null array elements; `tostring(json.null)` is `"null"` |

### `time`

| Call | Returns |
|---|---|
| `time.now()` | RFC 3339 UTC timestamp with milliseconds, e.g. `2026-10-06T14:03:07.120Z` |
| `time.unix()` | seconds since the epoch with millisecond precision (float) |
| `time.sleep(ms)` | nothing. Capped at `MaxSleep`. Cancellation or the wall budget ends the run during the sleep. |

### Output and globals

| Name | Meaning |
|---|---|
| `print(...)`, `log(...)` | tab-separated `tostring` of each argument plus a newline, into `RunResult.Output` |
| `args` | the program's `Args`, always a table |
| `script` | `{name = Program.Name, ...Program.Info}` |

### Return values

The first return value is converted for the caller. Tables are read raw (no
metamethods run). Values that cannot be converted (functions, coroutines, userdata
other than `json.null`, tables nested deeper than 32 levels, which includes tables that
contain themselves) make the run a `script_error`. NaN and infinities become `null`, with
a warning line in the output. Values above `MaxResultBytes` are truncated, keeping
object keys in sorted order.

---

## Source Limits

| Rule | Limit | Error |
|---|---|---|
| Source size | `MaxSourceBytes` | `the script is N bytes; the limit is M` |
| Brackets plus blocks open at once | 200 | `chunk:line: too many syntax levels ...` |
| Unfinished expression tokens across all open levels | 2000 | `chunk:line: expression nesting too deep ...` |

A new statement ends the expression before it (an identifier right after a complete
operand, `if`, `do`, `repeat`, or `function` after a complete statement), so scripts made
of many short statements or blocks are unaffected.

---

## Nested-Call Error Codes

Shared constants for hosts and scripts:

| Constant | Value | Set by |
|---|---|---|
| `CodeToolNotVisible` | `TOOL_NOT_VISIBLE` | host or policy: the tool is not available to this script |
| `CodeHardDenied` | `HARD_DENIED` | policy: the tool can never be called from a script |
| `CodeApprovalRequired` | `approval_required` | host: the tool needs human approval; call it directly |
| `CodePermissionDenied` | `permission_denied` | host: an admission hook denied the call |
| `CodeResourcePending` | `RESOURCE_PENDING` | host: the tool started a long-running job |
| `CodeExecutionFailed` | `execution_failed` | host or engine: the tool failed without a code |
| `CodeToolCallTimeout` | `TOOL_CALL_TIMEOUT` | engine: `ToolCallTimeout` passed |

---

## Examples

Each example below runs as a test in `pkg/luasandbox/examples_test.go`.

### Batch two queries and join them

```lua
-- args: {region = "EU"}
local sales = tools.must("execute_query", {
  statements = {{label = "sales", sql = "SELECT store_id, SUM(amount) AS total FROM sales WHERE region = '" .. args.region .. "' GROUP BY store_id"}}
})
local stores = tools.must("execute_query", {
  statements = {{label = "stores", sql = "SELECT store_id, name FROM stores"}}
})
local nameById = {}
for _, row in ipairs(stores.rows or {}) do nameById[row.store_id] = row.name end
local out = {}
for _, row in ipairs(sales.rows or {}) do
  out[#out + 1] = {store = nameById[row.store_id] or row.store_id, total = row.total}
end
table.sort(out, function(a, b) return a.total > b.total end)
return out
```

### Try, then fall back

```lua
local ok, data = pcall(tools.must, "catalog_lookup", {table = args.table})
if not ok then
  log("catalog_lookup failed, falling back to DBC:", data)
  data = tools.must("execute_query", {statements = {{sql = "SELECT * FROM DBC.ColumnsV WHERE TableName = '" .. args.table .. "'"}}})
end
return data
```

### Poll a long-running job

```lua
local start = tools.must("start_gdp_job", {job = args.job})
for i = 1, 20 do
  local r = tools.call("get_gdp_job_status", {id = start.id})
  if r.ok and r.data.state == "done" then return r.data end
  if r.ok and r.data.state == "failed" then error("job failed: " .. tostring(r.data.reason)) end
  time.sleep(5000)
end
return {state = "still_running", id = start.id}
```

### Post-process a large result from earlier in the turn

```lua
-- args: {message_id = 42, column = "status"}
local r = turn.result(args.message_id)
if not r.ok then error(r.error.message) end
local counts = {}
for _, row in ipairs(r.data.rows or r.data) do
  local k = tostring(row[args.column])
  counts[k] = (counts[k] or 0) + 1
end
local out = {}
for k, v in pairs(counts) do out[#out + 1] = {value = k, count = v} end
return out
```
