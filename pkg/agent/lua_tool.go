// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Error codes run_lua returns to the model (02-lua-api.md §6 of the design).
const (
	LuaCodeInvalidParams     = "INVALID_PARAMS"
	LuaCodeScriptError       = "SCRIPT_ERROR"
	LuaCodeBudgetExceeded    = "BUDGET_EXCEEDED"
	LuaCodeCancelled         = "CANCELLED"
	LuaCodeBusy              = "LUA_BUSY"
	LuaCodePolicyDenied      = "POLICY_DENIED"
	LuaCodeScriptNotFound    = "SCRIPT_NOT_FOUND"
	LuaCodeAmbiguousScript   = "AMBIGUOUS_SCRIPT"
	LuaCodeScriptNotAccepted = "SCRIPT_NOT_ACCEPTED"
	LuaCodeHostError         = "HOST_ERROR"
	LuaCodeEngineError       = "ENGINE_ERROR"
)

// LuaRunMetadataKey is the Result.Metadata key carrying the structured run
// record (outcome, usage, call ledger, output). Hosts audit and surface it;
// it is not shown to the model.
const LuaRunMetadataKey = "lua.run"

// luaErrorOutputTail is how much captured output a SCRIPT_ERROR message keeps.
const luaErrorOutputTail = 512

// runLuaDescription is the tool description, verbatim from the design
// (02-lua-api.md §1). It must stay under 1,100 characters.
const runLuaDescription = "Runs a short Lua 5.4 script in a sandbox inside the server and returns its result. " +
	"The script can call the tools you already have with `tools.call(name, args)`, so one run can replace many tool calls: " +
	"loop over results, filter and join them, retry, aggregate, and return only what you need. " +
	"Use this when you would otherwise make three or more dependent tool calls, or when you must compute over tool output before answering. " +
	"Do not use it for a single tool call. " +
	"Tools that need user approval are not run; the call returns `approval_required` and you should call that tool directly. " +
	"No network, file or OS access except through tools; when you need Python, Node or another program, use shell_execute_sandbox instead. " +
	"Budgets per run: wall time, CPU, memory (total allocation: join strings with table.concat, not `..` in a loop) and tool calls; " +
	"exceeding one ends the run with `BUDGET_EXCEEDED`. " +
	"Return a table for structured output; an array of row objects renders as a table. " +
	"Pass `script` for inline code or `name` to run a saved script. " +
	"The script reads its inputs from the global `args`."

// Errors a ScriptResolver returns for the cases run_lua reports with their
// own codes. Any other error is reported as POLICY_DENIED.
var (
	ErrScriptNotFound    = errors.New("saved script not found")
	ErrAmbiguousScript   = errors.New("script name matches more than one script")
	ErrScriptNotAccepted = errors.New("shared script version not accepted")
)

// ErrLuaToolSuppressed is returned by RegisterRunLuaTool when the host
// suppressed run_lua with WithoutBuiltinTool.
var ErrLuaToolSuppressed = errors.New("run_lua is suppressed on this agent")

// ResolvedScript is a saved script as the caller may run it.
type ResolvedScript struct {
	Name   string
	Source string
	// Trust says who wrote the script, relative to the caller.
	Trust luasandbox.Trust
	// Requires is the script's manifest.requires; nil when absent.
	Requires []string
}

// ScriptResolver finds a saved script for the caller by name or id. It is a
// plain function so this package does not depend on any script store: each
// host supplies its own (Tera over its database, loom's file store later).
// Wrap ErrScriptNotFound, ErrAmbiguousScript or ErrScriptNotAccepted to get
// those codes; the error text is shown to the model.
type ScriptResolver func(ctx context.Context, nameOrID string) (ResolvedScript, error)

// RunLuaToolOptions configures run_lua.
type RunLuaToolOptions struct {
	// Policy returns the policy for one run: limits and tool lists. Hosts
	// that read their configuration per turn resolve it here. Required. An
	// error, or a policy that fails Validate, refuses the run.
	Policy func(ctx context.Context) (luasandbox.Policy, error)
	// Gate bounds concurrent runs. Required.
	Gate *luasandbox.Gate
	// GateKey returns the key the gate counts per caller. nil uses the
	// agent's name.
	GateKey func(ctx context.Context) string
	// Resolve finds saved scripts. nil means the host has none, and every
	// `name` is SCRIPT_NOT_FOUND.
	Resolve ScriptResolver
	// Progress receives each run's activity. It may be nil and must not
	// block.
	Progress func(luasandbox.ProgressEvent)
}

// RunLuaTool is the run_lua builtin: it runs one Lua script whose tool calls
// go through this agent's guarded bridge (NewLuaHost).
type RunLuaTool struct {
	agent *Agent
	opts  RunLuaToolOptions
}

var _ shuttle.Tool = (*RunLuaTool)(nil)

// NewRunLuaTool returns run_lua for a. Policy and Gate are required.
func NewRunLuaTool(a *Agent, opts RunLuaToolOptions) (*RunLuaTool, error) {
	if a == nil {
		return nil, errors.New("run_lua: nil agent")
	}
	if opts.Policy == nil {
		return nil, errors.New("run_lua: no policy")
	}
	if opts.Gate == nil {
		return nil, errors.New("run_lua: no gate")
	}
	return &RunLuaTool{agent: a, opts: opts}, nil
}

// RegisterRunLuaTool registers run_lua on the agent. It refuses when the host
// suppressed the tool, and when the agent's executor has neither an admission
// chain nor a permission checker: nested calls would then run with no guard
// at all, so registering would fail open.
func (a *Agent) RegisterRunLuaTool(opts RunLuaToolOptions) error {
	if a.isBuiltinToolSuppressed(RunLuaToolName) {
		return ErrLuaToolSuppressed
	}
	if a.executor == nil || !a.executor.HasAdmissionGuards() {
		return ErrLuaNoGuards
	}
	tool, err := NewRunLuaTool(a, opts)
	if err != nil {
		return err
	}
	a.RegisterTool(tool)
	return nil
}

func (t *RunLuaTool) Name() string        { return RunLuaToolName }
func (t *RunLuaTool) Description() string { return runLuaDescription }
func (t *RunLuaTool) Backend() string     { return "" }

func (t *RunLuaTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema("", map[string]*shuttle.JSONSchema{
		"script":          shuttle.NewStringSchema("Lua 5.4 source. Mutually exclusive with name."),
		"name":            shuttle.NewStringSchema("Name or id of a saved script to run. Mutually exclusive with script."),
		"args":            {Type: "object", Description: "Values exposed to the script as the global table args. JSON only."},
		"timeout_seconds": {Type: "integer", Description: "Wall-time cap for this run. Clamped to the site maximum."},
	}, nil)
}

// runLuaParams are run_lua's validated parameters.
type runLuaParams struct {
	script  string
	name    string
	args    map[string]any
	timeout time.Duration
}

var runLuaParamNames = map[string]bool{"script": true, "name": true, "args": true, "timeout_seconds": true}

// parseRunLuaParams validates params. The schema cannot say "exactly one of"
// or "no other keys" in loom's JSONSchema type, so both are checked here.
func parseRunLuaParams(params map[string]interface{}) (runLuaParams, error) {
	var p runLuaParams
	var unknown []string
	for k := range params {
		if !runLuaParamNames[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return p, fmt.Errorf("unknown parameter(s): %s", strings.Join(unknown, ", "))
	}
	var err error
	if p.script, err = optionalString(params, "script"); err != nil {
		return p, err
	}
	if p.name, err = optionalString(params, "name"); err != nil {
		return p, err
	}
	switch {
	case p.script == "" && p.name == "":
		return p, errors.New("pass either script (inline Lua source) or name (a saved script)")
	case p.script != "" && p.name != "":
		return p, errors.New("pass script or name, not both")
	}
	if v, ok := params["args"]; ok && v != nil {
		m, ok := v.(map[string]interface{})
		if !ok {
			return p, fmt.Errorf("args must be an object, got %T", v)
		}
		p.args = m
	}
	if v, ok := params["timeout_seconds"]; ok && v != nil {
		secs, ok := wholeNumber(v)
		if !ok || secs <= 0 {
			return p, fmt.Errorf("timeout_seconds must be a positive whole number, got %v", v)
		}
		if secs > math.MaxInt32 {
			secs = math.MaxInt32 // clamped again by the policy below
		}
		p.timeout = time.Duration(secs) * time.Second
	}
	return p, nil
}

func optionalString(params map[string]interface{}, key string) (string, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, v)
	}
	return s, nil
}

// wholeNumber accepts the integer forms a decoded JSON number can take.
func wholeNumber(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case float64:
		if x != math.Trunc(x) || x > math.MaxInt64 || x < math.MinInt64 {
			return 0, false
		}
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

// Execute runs one script. Every refusal and failure is a Result with
// Success false; the returned error is always nil.
func (t *RunLuaTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()
	ctx, span := t.agent.tracer.StartSpan(ctx, "lua.run")
	defer t.agent.tracer.EndSpan(span)

	p, err := parseRunLuaParams(params)
	if err != nil {
		return luaFailure(span, start, LuaCodeInvalidParams, err.Error(), "", false), nil
	}
	script := ResolvedScript{Name: "inline", Source: p.script, Trust: luasandbox.TrustInline}
	if p.name != "" {
		var refused *shuttle.Result
		if script, refused = t.resolve(ctx, span, start, p.name); refused != nil {
			return refused, nil
		}
	}
	return t.runResolved(ctx, span, start, script, p.args, p.timeout), nil
}

// luaFailure is a refused or failed run.
func luaFailure(span *observability.Span, start time.Time, code, msg, suggestion string, retryable bool) *shuttle.Result {
	span.SetAttribute("lua.outcome", code)
	return &shuttle.Result{
		Success:         false,
		Error:           &shuttle.Error{Code: code, Message: msg, Suggestion: suggestion, Retryable: retryable},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}
}

// resolve looks a saved script up through the host's resolver. A non-nil
// Result is the refusal to return.
func (t *RunLuaTool) resolve(ctx context.Context, span *observability.Span, start time.Time, name string) (ResolvedScript, *shuttle.Result) {
	if t.opts.Resolve == nil {
		return ResolvedScript{}, luaFailure(span, start, LuaCodeScriptNotFound, "saved scripts are not available here", "pass the source as script", false)
	}
	script, err := t.opts.Resolve(ctx, name)
	switch {
	case errors.Is(err, ErrAmbiguousScript):
		return script, luaFailure(span, start, LuaCodeAmbiguousScript, err.Error(), "run it again with the script id", false)
	case errors.Is(err, ErrScriptNotAccepted):
		return script, luaFailure(span, start, LuaCodeScriptNotAccepted, err.Error(), "the user must review and accept this version first", false)
	case errors.Is(err, ErrScriptNotFound):
		return script, luaFailure(span, start, LuaCodeScriptNotFound, err.Error(), "", false)
	case err != nil:
		zap.L().Warn("run_lua: script lookup failed", zap.String("name", name), zap.Error(err))
		return script, luaFailure(span, start, LuaCodePolicyDenied, "the saved script could not be loaded", "", false)
	}
	if script.Name == "" {
		script.Name = name
	}
	return script, nil
}

// runResolved runs a script whose source is known: the step run_lua and the
// lua_<name> tools share. It applies the policy, takes a gate slot, builds the
// bridge and maps the outcome.
func (t *RunLuaTool) runResolved(ctx context.Context, span *observability.Span, start time.Time, script ResolvedScript, args map[string]any, timeout time.Duration) *shuttle.Result {
	span.SetAttribute("lua.script", script.Name)
	span.SetAttribute("lua.trust", script.Trust.String())

	sessionID, _ := ctx.Value("session_id").(string)
	if sessionID == "" {
		return luaFailure(span, start, LuaCodePolicyDenied, "run_lua needs a session; none is attached to this call", "", false)
	}
	pol, err := t.opts.Policy(ctx)
	if err == nil {
		err = pol.Validate()
	}
	if err != nil {
		zap.L().Warn("run_lua: policy unavailable; refusing the run", zap.String("session_id", sessionID), zap.Error(err))
		return luaFailure(span, start, LuaCodePolicyDenied, "the Lua policy could not be loaded", "call the tools directly", false)
	}

	key := t.agent.config.Name
	if t.opts.GateKey != nil {
		key = t.opts.GateKey(ctx)
	}
	release, ok := t.opts.Gate.TryAcquire(key)
	if !ok {
		return luaFailure(span, start, LuaCodeBusy, "every Lua run slot is busy", "retry shortly, or call the tools directly", true)
	}
	defer release()

	host, err := t.agent.NewLuaHost(ctx, LuaHostOptions{
		SessionID: sessionID,
		Policy:    pol,
		Trust:     script.Trust,
		Requires:  script.Requires,
		Progress:  t.opts.Progress,
	})
	if err != nil {
		zap.L().Warn("run_lua: bridge refused the run", zap.String("session_id", sessionID), zap.Error(err))
		return luaFailure(span, start, LuaCodePolicyDenied, "scripts cannot run here: "+err.Error(), "call the tools directly", false)
	}

	lim := pol.Limits.Clamp(luasandbox.Limits{Wall: timeout})
	res := luasandbox.Run(ctx, luasandbox.Program{
		Name:   script.Name,
		Source: script.Source,
		Args:   args,
		Info:   map[string]any{"trust": script.Trust.String()},
	}, lim, host)

	span.SetAttribute("lua.outcome", string(res.Outcome))
	span.SetAttribute("lua.cpu_ticks", int64(res.Used.CPUTicks))       // #nosec G115 -- bounded by MaxLimits().CPUTicks (1e10)
	span.SetAttribute("lua.memory_bytes", int64(res.Used.MemoryBytes)) // #nosec G115 -- bounded by MaxLimits().MemoryBytes
	span.SetAttribute("lua.wall_ms", res.Used.WallMillis)
	span.SetAttribute("lua.tool_calls", len(res.Calls))
	if res.Detail != "" {
		zap.L().Warn("run_lua: diagnostics", zap.String("session_id", sessionID),
			zap.String("outcome", string(res.Outcome)), zap.String("detail", res.Detail))
	}
	return mapLuaRunResult(res, script, time.Since(start))
}

// mapLuaRunResult converts a run into the tool result the model sees, per the
// design's result-mapping table (02-lua-api.md §1).
func mapLuaRunResult(res *luasandbox.RunResult, script ResolvedScript, elapsed time.Duration) *shuttle.Result {
	out := &shuttle.Result{
		ExecutionTimeMs: elapsed.Milliseconds(),
		Metadata:        map[string]interface{}{LuaRunMetadataKey: luaRunRecord(res, script)},
	}
	calls := len(res.Calls)
	switch res.Outcome {
	case luasandbox.OutcomeOK:
		out.Success = true
		if res.Value != nil {
			out.Data = res.Value
		} else {
			out.Data = map[string]interface{}{"output": res.Output, "calls": calls}
		}
		return out
	case luasandbox.OutcomeScriptError:
		msg := res.Error
		if tail := outputTail(res.Output, luaErrorOutputTail); tail != "" {
			msg += "\noutput (end):\n" + tail
		}
		msg += fmt.Sprintf("\ncalls made: %d", calls)
		out.Error = &shuttle.Error{Code: LuaCodeScriptError, Message: msg}
	case luasandbox.OutcomeBudgetExceeded:
		out.Error = &shuttle.Error{
			Code: LuaCodeBudgetExceeded,
			Message: fmt.Sprintf("%s budget exceeded: %s (used: %d CPU ticks, %d bytes allocated, %d ms, %d tool calls)",
				res.Limit, res.Error, res.Used.CPUTicks, res.Used.MemoryBytes, res.Used.WallMillis, calls),
			Suggestion: "split the work into smaller runs, or ask the tools for smaller results",
		}
	case luasandbox.OutcomeCancelled:
		out.Error = &shuttle.Error{Code: LuaCodeCancelled, Message: res.Error}
	case luasandbox.OutcomeHostError:
		out.Error = &shuttle.Error{Code: LuaCodeHostError, Message: "the script host failed; the details are in the server log"}
	default: // engine_error and anything newer
		out.Error = &shuttle.Error{Code: LuaCodeEngineError, Message: "the script engine failed; the details are in the server log"}
	}
	return out
}

// luaRunRecord is the run record stored under Metadata["lua.run"].
func luaRunRecord(res *luasandbox.RunResult, script ResolvedScript) map[string]interface{} {
	calls := make([]map[string]interface{}, 0, len(res.Calls))
	for _, c := range res.Calls {
		calls = append(calls, map[string]interface{}{
			"tool": c.Tool, "ok": c.OK, "code": c.Code, "millis": c.Millis, "decision": c.Decision,
		})
	}
	return map[string]interface{}{
		"outcome":                string(res.Outcome),
		"limit":                  res.Limit,
		"error":                  res.Error,
		"script":                 script.Name,
		"trust":                  script.Trust.String(),
		"cpu_ticks":              res.Used.CPUTicks,
		"memory_bytes":           res.Used.MemoryBytes,
		"wall_ms":                res.Used.WallMillis,
		"calls":                  calls,
		"output":                 res.Output,
		"output_truncated":       res.Truncated.Output,
		"value_truncated":        res.Truncated.Value,
		"call_results_truncated": res.Truncated.CallResults,
	}
}

// outputTail returns at most n bytes from the end of s, starting on a rune.
func outputTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}
