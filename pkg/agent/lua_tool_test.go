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
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func staticLuaPolicy(pol luasandbox.Policy) func(context.Context) (luasandbox.Policy, error) {
	return func(context.Context) (luasandbox.Policy, error) { return pol, nil }
}

// newLuaToolAgent returns a guarded agent with tools and its run_lua tool.
func newLuaToolAgent(t *testing.T, opts RunLuaToolOptions, tools ...shuttle.Tool) (*Agent, *RunLuaTool) {
	t.Helper()
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil), tools...)
	if opts.Policy == nil {
		opts.Policy = staticLuaPolicy(luasandbox.Policy{})
	}
	if opts.Gate == nil {
		opts.Gate = luasandbox.NewGate(4, 2)
	}
	tool, err := NewRunLuaTool(ag, opts)
	require.NoError(t, err)
	return ag, tool
}

func runLuaCtx(tools ...string) context.Context {
	return luaCtx(append([]string{RunLuaToolName}, tools...))
}

func luaRun(t *testing.T, res *shuttle.Result) map[string]interface{} {
	t.Helper()
	rec, ok := res.Metadata[LuaRunMetadataKey].(map[string]interface{})
	require.True(t, ok, "missing %s metadata", LuaRunMetadataKey)
	return rec
}

func TestRunLuaTool_ScriptCallsToolsAndReturnsValue(t *testing.T) {
	read := &luaTestTool{name: "read_note"}
	_, tool := newLuaToolAgent(t, RunLuaToolOptions{}, read)

	res, err := tool.Execute(runLuaCtx("read_note"), map[string]interface{}{
		"script": `local a = tools.must("read_note", {q = args.first})
local b = tools.must("read_note", {q = "two"})
return {a.echo, b.echo}`,
		"args": map[string]interface{}{"first": "one"},
	})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v", res.Error)
	assert.Equal(t, []any{"one", "two"}, res.Data)
	assert.EqualValues(t, 2, read.runs.Load())

	rec := luaRun(t, res)
	assert.Equal(t, "ok", rec["outcome"])
	assert.Equal(t, "inline", rec["script"])
	assert.Equal(t, "inline", rec["trust"])
	calls := rec["calls"].([]map[string]interface{})
	require.Len(t, calls, 2)
	assert.Equal(t, "read_note", calls[0]["tool"])
	assert.Equal(t, "allow", calls[0]["decision"])
}

func TestRunLuaTool_ResultMapping(t *testing.T) {
	slow := &luaTestTool{name: "slow", result: func(map[string]interface{}) *shuttle.Result {
		time.Sleep(200 * time.Millisecond)
		return &shuttle.Result{Success: true, Data: "late"}
	}}
	tiny := luasandbox.Policy{Limits: luasandbox.Limits{MemoryBytes: 1 << 20}}
	cases := []struct {
		name     string
		policy   luasandbox.Policy
		params   map[string]interface{}
		ctx      func() (context.Context, context.CancelFunc)
		wantOK   bool
		wantData any
		wantCode string
		wantMsg  []string
	}{
		{name: "value", params: map[string]interface{}{"script": `return {n = 3}`}, wantOK: true, wantData: map[string]any{"n": int64(3)}},
		{name: "no value", params: map[string]interface{}{"script": `print("hi")`}, wantOK: true,
			wantData: map[string]interface{}{"output": "hi\n", "calls": 0}},
		{name: "script error", params: map[string]interface{}{"script": "print('before')\nerror('boom')"},
			wantCode: LuaCodeScriptError, wantMsg: []string{"inline:2:", "boom", "before", "calls made: 0"}},
		{name: "compile error", params: map[string]interface{}{"script": "return ("},
			wantCode: LuaCodeScriptError, wantMsg: []string{"inline"}},
		{name: "memory budget", policy: tiny, params: map[string]interface{}{"script": `local t = {} for i = 1, 1e7 do t[i] = {} end`},
			wantCode: LuaCodeBudgetExceeded, wantMsg: []string{"memory budget exceeded", "bytes allocated"}},
		{name: "cancelled", params: map[string]interface{}{"script": `tools.call("slow") return 1`},
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithTimeout(runLuaCtx("slow"), 30*time.Millisecond)
				return ctx, cancel
			}, wantCode: LuaCodeCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, tool := newLuaToolAgent(t, RunLuaToolOptions{Policy: staticLuaPolicy(tc.policy)}, slow)
			ctx, cancel := runLuaCtx("slow"), context.CancelFunc(func() {})
			if tc.ctx != nil {
				ctx, cancel = tc.ctx()
			}
			defer cancel()
			res, err := tool.Execute(ctx, tc.params)
			require.NoError(t, err)
			require.Equal(t, tc.wantOK, res.Success, "%+v", res.Error)
			_ = luaRun(t, res)
			if tc.wantOK {
				assert.Equal(t, tc.wantData, res.Data)
				return
			}
			require.NotNil(t, res.Error)
			assert.Equal(t, tc.wantCode, res.Error.Code)
			for _, m := range tc.wantMsg {
				assert.Contains(t, res.Error.Message, m)
			}
		})
	}
}

func TestRunLuaTool_ScriptErrorKeepsTheOutputTail(t *testing.T) {
	_, tool := newLuaToolAgent(t, RunLuaToolOptions{})
	res, err := tool.Execute(runLuaCtx(), map[string]interface{}{
		"script": `for i = 1, 400 do print("line " .. i) end error("late failure")`,
	})
	require.NoError(t, err)
	require.Equal(t, LuaCodeScriptError, res.Error.Code)
	assert.Contains(t, res.Error.Message, "line 400")
	assert.NotContains(t, res.Error.Message, "line 1\n")
	assert.Less(t, len(res.Error.Message), 2048+luaErrorOutputTail+200)
}

func TestRunLuaTool_InvalidParams(t *testing.T) {
	_, tool := newLuaToolAgent(t, RunLuaToolOptions{})
	for name, params := range map[string]map[string]interface{}{
		"neither":         {},
		"both":            {"script": "return 1", "name": "x"},
		"unknown key":     {"script": "return 1", "sql": "select 1"},
		"script not text": {"script": 42},
		"args not object": {"script": "return 1", "args": []interface{}{1}},
		"bad timeout":     {"script": "return 1", "timeout_seconds": 1.5},
		"zero timeout":    {"script": "return 1", "timeout_seconds": 0},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := tool.Execute(runLuaCtx(), params)
			require.NoError(t, err)
			require.False(t, res.Success)
			assert.Equal(t, LuaCodeInvalidParams, res.Error.Code, res.Error.Message)
		})
	}
	res, err := tool.Execute(runLuaCtx(), map[string]interface{}{"script": "return 1", "timeout_seconds": json.Number("5")})
	require.NoError(t, err)
	assert.True(t, res.Success, "%+v", res.Error)
}

func TestRunLuaTool_TimeoutOnlyLowersTheWallBudget(t *testing.T) {
	_, tool := newLuaToolAgent(t, RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{Limits: luasandbox.Limits{Wall: 300 * time.Millisecond}})})
	start := time.Now()
	res, err := tool.Execute(runLuaCtx(), map[string]interface{}{"script": `time.sleep(5000)`, "timeout_seconds": 3600})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "a request cannot raise the wall budget")
	require.False(t, res.Success)
	assert.Equal(t, LuaCodeBudgetExceeded, res.Error.Code)
}

func TestRunLuaTool_Refusals(t *testing.T) {
	t.Run("no session", func(t *testing.T) {
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{})
		res, err := tool.Execute(context.WithValue(context.Background(), advertisedProjectionKey{}, []string{}), map[string]interface{}{"script": "return 1"})
		require.NoError(t, err)
		assert.Equal(t, LuaCodePolicyDenied, res.Error.Code)
	})
	t.Run("no projection", func(t *testing.T) {
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{})
		res, err := tool.Execute(luaCtx(nil), map[string]interface{}{"script": "return 1"})
		require.NoError(t, err)
		assert.Equal(t, LuaCodePolicyDenied, res.Error.Code)
	})
	t.Run("policy error", func(t *testing.T) {
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{Policy: func(context.Context) (luasandbox.Policy, error) {
			return luasandbox.Policy{}, errors.New("config unreadable")
		}})
		res, err := tool.Execute(runLuaCtx(), map[string]interface{}{"script": "return 1"})
		require.NoError(t, err)
		assert.Equal(t, LuaCodePolicyDenied, res.Error.Code)
		assert.NotContains(t, res.Error.Message, "unreadable", "internal errors stay in the log")
	})
	t.Run("malformed policy", func(t *testing.T) {
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{Allow: []string{"["}})})
		res, err := tool.Execute(runLuaCtx(), map[string]interface{}{"script": "return 1"})
		require.NoError(t, err)
		assert.Equal(t, LuaCodePolicyDenied, res.Error.Code)
	})
	t.Run("busy", func(t *testing.T) {
		gate := luasandbox.NewGate(1, 1)
		release, ok := gate.TryAcquire("other")
		require.True(t, ok)
		defer release()
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{Gate: gate})
		start := time.Now()
		res, err := tool.Execute(runLuaCtx(), map[string]interface{}{"script": "return 1"})
		require.NoError(t, err)
		assert.Less(t, time.Since(start), 50*time.Millisecond, "a full gate fails fast")
		assert.Equal(t, LuaCodeBusy, res.Error.Code)
		assert.True(t, res.Error.Retryable)
	})
	t.Run("slot returned on refusal", func(t *testing.T) {
		gate := luasandbox.NewGate(1, 1)
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{Gate: gate})
		for i := 0; i < 3; i++ {
			res, err := tool.Execute(luaCtx(nil), map[string]interface{}{"script": "return 1"}) // NewLuaHost fails
			require.NoError(t, err)
			assert.Equal(t, LuaCodePolicyDenied, res.Error.Code)
		}
		inUse, _, _ := gate.Stats()
		assert.Equal(t, 0, inUse, "a refused run must return its slot")
	})
}

func TestRunLuaTool_SavedScripts(t *testing.T) {
	t.Run("no resolver", func(t *testing.T) {
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{})
		res, err := tool.Execute(runLuaCtx(), map[string]interface{}{"name": "report"})
		require.NoError(t, err)
		assert.Equal(t, LuaCodeScriptNotFound, res.Error.Code)
	})
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("report: %w", ErrScriptNotFound), LuaCodeScriptNotFound},
		{fmt.Errorf("two scripts named report: %w", ErrAmbiguousScript), LuaCodeAmbiguousScript},
		{fmt.Errorf("report v3: %w", ErrScriptNotAccepted), LuaCodeScriptNotAccepted},
		{errors.New("db down"), LuaCodePolicyDenied},
	} {
		t.Run(tc.code, func(t *testing.T) {
			_, tool := newLuaToolAgent(t, RunLuaToolOptions{Resolve: func(context.Context, string) (ResolvedScript, error) {
				return ResolvedScript{}, tc.err
			}})
			res, err := tool.Execute(runLuaCtx(), map[string]interface{}{"name": "report"})
			require.NoError(t, err)
			assert.Equal(t, tc.code, res.Error.Code)
		})
	}
	t.Run("shared script runs with shared trust and its requires", func(t *testing.T) {
		web := &luaTestTool{name: "web_search"}
		query := &luaTestTool{name: "query"}
		var asked string
		_, tool := newLuaToolAgent(t, RunLuaToolOptions{
			Policy: staticLuaPolicy(luasandbox.Policy{DenyForShared: []string{"web_search"}}),
			Resolve: func(_ context.Context, name string) (ResolvedScript, error) {
				asked = name
				return ResolvedScript{Name: "report", Trust: luasandbox.TrustShared, Requires: []string{"query", "web_search"},
					Source: `local a = tools.call("web_search", {}) local b = tools.call("query", {}) return {a.error.code, b.ok, script.name, script.trust}`}, nil
			},
		}, web, query)
		res, err := tool.Execute(runLuaCtx("web_search", "query"), map[string]interface{}{"name": "report"})
		require.NoError(t, err)
		require.True(t, res.Success, "%+v", res.Error)
		assert.Equal(t, "report", asked)
		assert.Equal(t, []any{luasandbox.CodeToolNotVisible, true, "report", "shared"}, res.Data)
		assert.EqualValues(t, 0, web.runs.Load())
		assert.Equal(t, "shared", luaRun(t, res)["trust"])
	})
}

func TestMapLuaRunResult_HostAndEngineErrorsHideDetails(t *testing.T) {
	for outcome, code := range map[luasandbox.Outcome]string{
		luasandbox.OutcomeHostError:   LuaCodeHostError,
		luasandbox.OutcomeEngineError: LuaCodeEngineError,
	} {
		res := mapLuaRunResult(&luasandbox.RunResult{Outcome: outcome, Error: "secret internals", Detail: "stack trace"}, ResolvedScript{Name: "inline"}, time.Millisecond)
		require.False(t, res.Success)
		assert.Equal(t, code, res.Error.Code)
		assert.NotContains(t, res.Error.Message, "secret")
		assert.NotContains(t, res.Error.Message, "stack")
	}
}

func TestRegisterRunLuaTool(t *testing.T) {
	opts := RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{}), Gate: luasandbox.NewGate(2, 1)}

	guarded := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil))
	require.NoError(t, guarded.RegisterRunLuaTool(opts))
	assert.True(t, guarded.tools.IsRegistered(RunLuaToolName))

	unguarded := newLuaTestAgent(t, nil)
	assert.ErrorIs(t, unguarded.RegisterRunLuaTool(opts), ErrLuaNoGuards)
	assert.False(t, unguarded.tools.IsRegistered(RunLuaToolName))

	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{DisabledTools: []string{"nothing"}})
	byChecker := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(DefaultConfig()), WithPermissionChecker(checker))
	require.NoError(t, byChecker.RegisterRunLuaTool(opts), "a permission checker alone is a guard")

	suppressed := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(DefaultConfig()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil)), WithoutBuiltinTool(RunLuaToolName))
	assert.ErrorIs(t, suppressed.RegisterRunLuaTool(opts), ErrLuaToolSuppressed)
	assert.False(t, suppressed.tools.IsRegistered(RunLuaToolName))

	_, err := NewRunLuaTool(guarded, RunLuaToolOptions{Gate: opts.Gate})
	assert.Error(t, err, "a policy is required")
	_, err = NewRunLuaTool(guarded, RunLuaToolOptions{Policy: opts.Policy})
	assert.Error(t, err, "a gate is required")
}

func TestRunLuaTool_SchemaAndDescription(t *testing.T) {
	_, tool := newLuaToolAgent(t, RunLuaToolOptions{})
	desc := tool.Description()
	assert.LessOrEqual(t, utf8.RuneCountInString(desc), 1100, "the design caps the description at 1,100 characters")
	assert.GreaterOrEqual(t, len(strings.Fields(desc)), 20)
	schema, err := json.Marshal(tool.InputSchema())
	require.NoError(t, err)
	assert.LessOrEqual(t, len(schema), 600, "schema bytes: %s", schema)
	for _, p := range []string{"script", "name", "args", "timeout_seconds"} {
		assert.Contains(t, string(schema), `"`+p+`"`)
	}
}

// End to end through the conversation loop: one run_lua call whose script
// makes two tool calls is one tool row, and a tool hidden from the model by
// the permission filter stays hidden from the script.
func TestRunLuaTool_ThroughTheConversationLoop(t *testing.T) {
	read := &luaTestTool{name: "read_note"}
	hidden := &luaTestTool{name: "hidden_note"}
	script := `local a = tools.must("read_note", {q = "a"})
local b = tools.must("read_note", {q = "b"})
local h = tools.call("hidden_note", {})
return {a.echo, b.echo, h.error.code}`
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "call_lua", Name: RunLuaToolName, Input: map[string]interface{}{"script": script}}}},
		{content: "done"},
	}}
	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{DisabledTools: []string{"hidden_note"}})
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithPermissionChecker(checker))
	ag.RegisterTool(read)
	ag.RegisterTool(hidden)
	require.NoError(t, ag.RegisterRunLuaTool(RunLuaToolOptions{
		Policy: staticLuaPolicy(luasandbox.Policy{}),
		Gate:   luasandbox.NewGate(2, 1),
	}))

	resp, err := ag.Chat(context.Background(), "lua-loop-session", "read both notes")
	require.NoError(t, err)

	var luaExecs []ToolExecution
	for _, te := range resp.ToolExecutions {
		assert.NotEqual(t, "read_note", te.ToolName, "nested calls are not tool rows")
		if te.ToolName == RunLuaToolName {
			luaExecs = append(luaExecs, te)
		}
	}
	require.Len(t, luaExecs, 1)
	res := luaExecs[0].Result
	require.NotNil(t, res)
	require.True(t, res.Success, "%+v", res.Error)
	assert.Equal(t, []any{"a", "b", luasandbox.CodeToolNotVisible}, res.Data,
		"hidden_note is registered but not advertised, so the script must not see it")
	calls := luaRun(t, res)["calls"].([]map[string]interface{})
	assert.Len(t, calls, 3)
	assert.EqualValues(t, 2, read.runs.Load())
	assert.EqualValues(t, 0, hidden.runs.Load())
}

// Registry-built agents get run_lua through RegistryConfig.RunLuaTool, with
// the same refusals as the serve path.
func TestRegistry_RegisterRunLua(t *testing.T) {
	opts := &RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{}), Gate: luasandbox.NewGate(2, 1)}
	for _, tc := range []struct {
		name      string
		opts      *RunLuaToolOptions
		agent     func() *Agent
		want      bool
		wantLevel zapcore.Level
	}{
		{"disabled", nil, func() *Agent {
			return newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil))
		}, false, zapcore.InfoLevel},
		{"enabled", opts, func() *Agent {
			return newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil))
		}, true, zapcore.InfoLevel},
		{"no guard", opts, func() *Agent { return newLuaTestAgent(t, nil) }, false, zapcore.WarnLevel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.InfoLevel)
			r := &Registry{logger: zap.New(core), runLuaTool: tc.opts}
			ag := tc.agent()
			r.registerRunLua(ag, "a")
			assert.Equal(t, tc.want, ag.tools.IsRegistered(RunLuaToolName))
			entries := logs.All()
			require.Len(t, entries, 1)
			assert.Equal(t, tc.wantLevel, entries[0].Level, entries[0].Message)
		})
	}
}
