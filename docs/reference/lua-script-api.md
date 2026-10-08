# Lua Script API Reference

**Status**: ✅ implemented, off by default (`tools.lua.enabled`): the engine and Lua-facing
API (`pkg/luasandbox`), the builtin `run_lua` tool, and saved scripts (`manage_lua_scripts`,
`lua_<name>` tools, the `LoomService` Lua script RPCs). Design and rationale:
[architecture/lua-script-engine.md](../architecture/lua-script-engine.md).

**Interpreter**: `github.com/arnodel/golua` v0.3.0, Lua 5.4.

---

## The `run_lua` tool

### Enabling it

Three things must all hold for an agent to have `run_lua`:

1. The server sets `tools.lua.enabled: true` (env `LOOM_TOOLS_LUA_ENABLED`). Default
   `false`.
2. The agent lists `run_lua` in `tools.builtin`.
3. The agent's executor has an admission chain or a permission checker
   (`tools.hooks` or `tools.permissions`). Otherwise `run_lua` is not registered and
   serve logs one warning naming the reason. loom's default YOLO permission checker
   counts, and admits every call; configure `tools.hooks` before enabling Lua on a
   shared server.

```yaml
tools:
  lua:
    enabled: true
    limits:                       # zero = engine default; capped at the engine ceilings
      wall_seconds: 120
      memory_bytes: 134217728     # 128 MiB per run, counted as total allocation
      max_tool_calls: 100
      max_concurrent_runs: 0      # 0 = derived from the process memory limit; a value only lowers it
      max_concurrent_runs_per_key: 2
    tools:
      allow: []                   # empty = every tool the model can see
      deny: [shell_execute, shell_execute_sandbox, agent_management, project_manager, git_contribute, propose_skill_edit]
      deny_for_shared: [http_request, web_browse, web_search, file_write, files, workspace, shell_execute]
```

`shell_execute` is denied to scripts by default. It runs real programs with the server's
permissions, and a script could run many of them without the model or a person seeing each
one. Scripts get it only when both of these hold:

- `tools.shell_execute.mode` is `jailed`, so every program a command starts is checked on
  its real arguments; and
- a `kind: command-policy` hook binding governs `shell_execute`, so each call is judged
  before it runs (see [shell-command-policy.md](shell-command-policy.md)).

Then removing `shell_execute` from `deny` gives it to the runner's own scripts. A command
that needs approval returns `approval_required` to the script, and the model makes the call
itself so a person can approve it. If either condition is missing, serve keeps
`shell_execute` hidden from scripts whatever `deny` says, and logs a warning. Shared scripts
never get it: `deny_for_shared` lists it by default.

A malformed tool pattern in `tools.lua.tools` aborts `looms serve`. Hosts embedding loom
call `Agent.RegisterRunLuaTool(agent.RunLuaToolOptions{...})` themselves, or suppress
loom's tool with `agent.WithoutBuiltinTool("run_lua")` and register their own.

### Parameters

| Parameter | Type | Meaning |
|---|---|---|
| `script` | string | Lua source to run. Exactly one of `script` and `name`. |
| `name` | string | Name of a saved script (see "Saved scripts" below). |
| `args` | object | Becomes the global table `args`. |
| `timeout_seconds` | integer | Lowers the wall budget for this run; it can never raise it. |

Any other parameter is `INVALID_PARAMS`.

### What a script can call

Only the tools the model was shown for the provider call that requested the run, minus
the engine's hard-deny list, the reserved names (`run_lua`, `manage_lua_scripts`,
`lua_*`), `tools.lua.tools.deny`, and `deny_for_shared` for scripts the runner did not
write; `allow`, when set, narrows further. Each call runs through the agent's admission
chain like a direct call. A call that needs approval returns `approval_required` without
running, and the model should make it directly.

### Results

| Run outcome | `Success` | `Data` or `Error.Code` |
|---|---|---|
| returned a value | true | the value |
| returned nothing | true | `{"output": "<print capture>", "calls": N}` |
| Lua error | false | `SCRIPT_ERROR`: position and message, the last 512 bytes of output, calls made |
| a budget fired | false | `BUDGET_EXCEEDED`: the limit and the usage so far |
| the turn was cancelled | false | `CANCELLED` |
| every run slot busy | false | `LUA_BUSY` (retryable) |
| refused (no session, no projection, no guard, policy unavailable) | false | `POLICY_DENIED` |
| saved-script lookup | false | `SCRIPT_NOT_FOUND`, `AMBIGUOUS_SCRIPT`, `SCRIPT_NOT_ACCEPTED` |
| bad parameters | false | `INVALID_PARAMS` |
| host or engine failure | false | `HOST_ERROR` or `ENGINE_ERROR`; details only in the server log |

`Result.Metadata["lua.run"]` always carries the run record: outcome, limit, usage, the
call ledger (`tool`, `ok`, `code`, `millis`, `decision`), output and truncation flags.
The nested calls are not tool rows of their own; the `run_lua` call is one row.

## Saved scripts

A loom server keeps saved scripts in `tools.lua.scripts_dir` (default
`<LOOM_DATA_DIR>/lua_scripts`): `<name>.lua` holds the source, `<name>.json` the metadata
(description, manifest, version, published flag, owner), and `attachments.json` which agents
have which script tools. Writes are atomic (temp file and rename). A corrupt file is skipped
and logged at startup. Every agent on the server shares the store.

```yaml
tools:
  lua:
    enabled: true
    scripts_dir: ""                  # default <LOOM_DATA_DIR>/lua_scripts
    scripts:
      save_enabled: true             # agents that list manage_lua_scripts may manage scripts
      publish_as_tool_enabled: false # allow publishing a script as a lua_<name> tool
```

`run_lua` can run any saved script by `name` whenever Lua is enabled.

### `manage_lua_scripts`

Registered on agents that list it in `tools.builtin` and also have `run_lua`, when
`scripts.save_enabled` is true.

| Action | Does |
|---|---|
| `save` | Validates the name (`^[a-z][a-z0-9_]{2,40}$`), a description, the manifest, and that the source compiles (it is never run), then stores it. An existing name is `DUPLICATE` unless `overwrite` is true; a new version keeps its published state and attachments. Returns `{name, version, tool_name, published}`. |
| `get` | Returns the script with its source, manifest, version, owner and `updated_at`. |
| `list` | Returns up to 200 scripts, name-sorted, without source. |
| `delete` | Deletes the script and every attachment of it. |
| `publish` | Needs `publish_as_tool_enabled` and a manifest with `parameters`. Every `requires` entry must be a tool scripts on this agent can call (`TOOL_NOT_VISIBLE` otherwise). Marks the script published, attaches it to this agent, and registers `lua_<name>` at once. |
| `unpublish` | Clears the published flag. The tool leaves this agent at once, and every other agent's copy refuses its next call. Attachments are kept, so publishing again restores them. |
| `detach` | Removes `lua_<name>` from this agent only. |

The manifest:

| Field | Meaning |
|---|---|
| `parameters` | JSON Schema with `type: object` for the tool's arguments: at most 20 properties and 4 KiB, no `$ref`. |
| `requires` | The tools the script calls. When set, the script may call only these. |
| `returns` | One line describing the return value. |

### `lua_<name>` tools

A published script attached to an agent becomes a tool named `lua_<name>`. Its description
is `Saved Lua script. <description> Returns: <returns>`, and its input schema is
`manifest.parameters`. The tool's arguments become the script's `args`. A call re-reads the
script, so the newest version runs, and a deleted or unpublished script refuses. It runs the
same way as `run_lua`: same gate, same bridge, with the script's `requires`. Scripts can
never call `lua_*` tools.

An agent gets `lua_<name>` from its attachments, or from a custom tool in its YAML:

```yaml
tools:
  builtin: [run_lua]
  custom:
    - name: weekly_report
      implementation: lua://weekly_report   # must name a published script
```

### RPCs

`LoomService.ListLuaScripts`, `GetLuaScript`, `SaveLuaScript` and `DeleteLuaScript`
(`GET/POST /v1/lua-scripts`, `GET/DELETE /v1/lua-scripts/{name}`) work on the same store.
They return `FailedPrecondition` while `tools.lua.enabled` is false. Saves over RPC record
the owner `server`, and validation failures are `InvalidArgument`. A taken name without
`overwrite` is `AlreadyExists`. Publishing is done with `manage_lua_scripts`.

## Go API

### `Run`

```go
func Run(ctx context.Context, p Program, lim Limits, h Host) *RunResult
```

Runs `p` on the caller's goroutine. Never returns nil, never panics. Returns when the
script finishes, a limit fires, or `ctx` ends. When `ctx` ends the script stops at its
next interpreter step, even when it only computes; a single string pattern match takes
its CPU budget up front and is not interrupted part way.

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
