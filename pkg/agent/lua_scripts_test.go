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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store"
	"github.com/teradata-labs/loom/pkg/luasandbox/store/filestore"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func newTestScriptStore(t *testing.T) *filestore.Store {
	t.Helper()
	st, err := filestore.Open(t.TempDir(), luasandbox.Limits{})
	require.NoError(t, err)
	return st
}

var sumParams = map[string]interface{}{
	"type":       "object",
	"properties": map[string]interface{}{"q": map[string]interface{}{"type": "string"}},
}

// newScriptsAgent returns a guarded agent named name with run_lua (resolving
// against st) and the given tools registered, plus the saved-script options.
func newScriptsAgent(t *testing.T, name string, st store.ScriptStore, publish bool, tools ...shuttle.Tool) (*Agent, LuaScriptsOptions) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Name = name
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(cfg),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil)))
	for _, tool := range tools {
		ag.RegisterTool(tool)
	}
	require.NoError(t, ag.RegisterRunLuaTool(RunLuaToolOptions{
		Policy:  staticLuaPolicy(luasandbox.Policy{}),
		Gate:    luasandbox.NewGate(4, 4),
		Resolve: StoreScriptResolver(st),
	}))
	opts := LuaScriptsOptions{Store: st, SaveEnabled: true, PublishEnabled: publish}
	rep := ag.RegisterLuaScriptTools(context.Background(), opts, true, nil)
	require.Contains(t, rep.Registered, ManageLuaScriptsToolName, "%v", rep.Skipped)
	return ag, opts
}

func manage(t *testing.T, ag *Agent, ctx context.Context, params map[string]interface{}) *shuttle.Result {
	t.Helper()
	tool, ok := ag.tools.Get(ManageLuaScriptsToolName)
	require.True(t, ok)
	res, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	return res
}

func TestStoreScriptResolver(t *testing.T) {
	st := newTestScriptStore(t)
	_, _, err := st.Save(context.Background(), store.Script{Name: "lookup", Description: "d", Source: "return 1",
		Manifest: &store.Manifest{Requires: []string{"query"}}}, false)
	require.NoError(t, err)
	resolve := StoreScriptResolver(st)

	got, err := resolve(context.Background(), "lookup")
	require.NoError(t, err)
	assert.Equal(t, ResolvedScript{Name: "lookup", Source: "return 1", Trust: luasandbox.TrustOwn, Requires: []string{"query"}}, got)

	_, err = resolve(context.Background(), "missing")
	assert.ErrorIs(t, err, ErrScriptNotFound)
}

func TestManageLuaScripts_SaveGetListDelete(t *testing.T) {
	st := newTestScriptStore(t)
	ag, _ := newScriptsAgent(t, "agent-a", st, false)
	ctx := runLuaCtx()

	res := manage(t, ag, ctx, map[string]interface{}{"action": "save", "name": "bad_one", "description": "d", "script": "return ("})
	require.False(t, res.Success)
	assert.Equal(t, LuaCodeInvalidParams, res.Error.Code, "a source that does not compile is refused")

	res = manage(t, ag, ctx, map[string]interface{}{"action": "save", "name": "double_it", "description": "Doubles args.n.",
		"script": "return args.n * 2", "manifest": map[string]interface{}{"parameters": sumParams, "returns": "a number"}})
	require.True(t, res.Success, "%+v", res.Error)
	assert.Equal(t, map[string]interface{}{"name": "double_it", "version": 1, "tool_name": "lua_double_it", "published": false}, res.Data)

	res = manage(t, ag, ctx, map[string]interface{}{"action": "save", "name": "double_it", "description": "d", "script": "return 1"})
	assert.Equal(t, LuaCodeDuplicate, res.Error.Code)

	res = manage(t, ag, ctx, map[string]interface{}{"action": "get", "name": "double_it"})
	require.True(t, res.Success)
	data := res.Data.(map[string]interface{})
	assert.Equal(t, "return args.n * 2", data["script"])
	assert.Equal(t, "agent-a", data["owner"])
	assert.Equal(t, "own", data["trust"])

	res = manage(t, ag, ctx, map[string]interface{}{"action": "list"})
	require.True(t, res.Success)
	rows := res.Data.([]map[string]interface{})
	require.Len(t, rows, 1)
	assert.Equal(t, "double_it", rows[0]["name"])
	assert.NotContains(t, rows[0], "script", "list omits the source")

	// run_lua runs the saved script by name.
	run, _ := ag.tools.Get(RunLuaToolName)
	out, err := run.Execute(ctx, map[string]interface{}{"name": "double_it", "args": map[string]interface{}{"n": 21}})
	require.NoError(t, err)
	require.True(t, out.Success, "%+v", out.Error)
	assert.EqualValues(t, 42, out.Data)

	res = manage(t, ag, ctx, map[string]interface{}{"action": "delete", "name": "double_it"})
	require.True(t, res.Success)
	res = manage(t, ag, ctx, map[string]interface{}{"action": "get", "name": "double_it"})
	assert.Equal(t, LuaCodeScriptNotFound, res.Error.Code)

	for _, bad := range []map[string]interface{}{
		{"action": "frobnicate", "name": "abc"},
		{"action": "get", "name": "Bad Name"},
		{"action": "save", "name": "abc", "description": "d", "script": "return 1", "extra": true},
		{"action": "save", "name": "abc", "description": "d", "script": "return 1", "manifest": map[string]interface{}{"oops": 1}},
	} {
		res = manage(t, ag, ctx, bad)
		assert.Equal(t, LuaCodeInvalidParams, res.Error.Code, "%v", bad)
	}
}

func TestManageLuaScripts_PublishAttachUnpublishDetach(t *testing.T) {
	st := newTestScriptStore(t)
	query := &luaTestTool{name: "query"}
	secret := &luaTestTool{name: "secret"}
	ag, opts := newScriptsAgent(t, "agent-a", st, true, query, secret)
	ctx := runLuaCtx("query", "secret", ManageLuaScriptsToolName)

	save := func(name, src string, manifest map[string]interface{}) {
		params := map[string]interface{}{"action": "save", "name": name, "description": "Runs a query.", "script": src}
		if manifest != nil {
			params["manifest"] = manifest
		}
		res := manage(t, ag, ctx, params)
		require.True(t, res.Success, "%+v", res.Error)
	}

	save("no_manifest", "return 1", nil)
	res := manage(t, ag, ctx, map[string]interface{}{"action": "publish", "name": "no_manifest"})
	assert.Equal(t, LuaCodeInvalidParams, res.Error.Code, "publish needs manifest parameters")

	save("needs_hidden", "return 1", map[string]interface{}{"parameters": sumParams, "requires": []interface{}{"not_advertised"}})
	res = manage(t, ag, ctx, map[string]interface{}{"action": "publish", "name": "needs_hidden"})
	assert.Equal(t, luasandbox.CodeToolNotVisible, res.Error.Code, "requires must be callable from this agent")

	save("run_query", `local r = tools.must("query", {q = args.q}) local s = tools.call("secret", {}) return {r.echo, s.error.code}`,
		map[string]interface{}{"parameters": sumParams, "requires": []interface{}{"query"}, "returns": "the echo"})
	res = manage(t, ag, ctx, map[string]interface{}{"action": "publish", "name": "run_query"})
	require.True(t, res.Success, "%+v", res.Error)
	tool, ok := ag.tools.Get("lua_run_query")
	require.True(t, ok, "publish registers the tool at once")
	assert.Equal(t, "Saved Lua script. Runs a query. Returns: the echo", tool.Description())
	attached, _ := st.Attachments(context.Background(), "agent-a")
	assert.Equal(t, []string{"run_query"}, attached)

	out, err := tool.Execute(runLuaCtx("query", "secret"), map[string]interface{}{"q": "hello"})
	require.NoError(t, err)
	require.True(t, out.Success, "%+v", out.Error)
	assert.Equal(t, []any{"hello", luasandbox.CodeToolNotVisible}, out.Data, "requires limits the script to query")
	assert.EqualValues(t, 0, secret.runs.Load())

	// Another agent with the same store sees the attachment only if attached.
	other, _ := newScriptsAgent(t, "agent-b", st, true, query)
	_, ok = other.tools.Get("lua_run_query")
	assert.False(t, ok, "not attached to agent-b")
	require.NoError(t, st.Attach(context.Background(), "agent-b", "run_query"))
	rep := other.RegisterLuaScriptTools(context.Background(), opts, false, nil)
	assert.Contains(t, rep.Registered, "lua_run_query")
	otherTool, _ := other.tools.Get("lua_run_query")

	// Unpublish takes the tool off this agent at once and refuses calls on
	// the other agent's copy.
	res = manage(t, ag, ctx, map[string]interface{}{"action": "unpublish", "name": "run_query"})
	require.True(t, res.Success)
	_, ok = ag.tools.Get("lua_run_query")
	assert.False(t, ok)
	out, err = otherTool.Execute(runLuaCtx("query"), map[string]interface{}{"q": "x"})
	require.NoError(t, err)
	assert.Equal(t, LuaCodePolicyDenied, out.Error.Code)
	agents, _ := st.AttachedAgents(context.Background(), "run_query")
	assert.Equal(t, []string{"agent-a", "agent-b"}, agents, "unpublish keeps attachments")

	// Publish again, then detach from agent-a only.
	res = manage(t, ag, ctx, map[string]interface{}{"action": "publish", "name": "run_query"})
	require.True(t, res.Success)
	res = manage(t, ag, ctx, map[string]interface{}{"action": "detach", "name": "run_query"})
	require.True(t, res.Success)
	agents, _ = st.AttachedAgents(context.Background(), "run_query")
	assert.Equal(t, []string{"agent-b"}, agents)
	out, err = otherTool.Execute(runLuaCtx("query"), map[string]interface{}{"q": "x"})
	require.NoError(t, err)
	assert.True(t, out.Success, "agent-b keeps its attachment: %+v", out.Error)

	// Deleting the script stops every copy.
	res = manage(t, ag, ctx, map[string]interface{}{"action": "delete", "name": "run_query"})
	require.True(t, res.Success)
	out, err = otherTool.Execute(runLuaCtx("query"), map[string]interface{}{"q": "x"})
	require.NoError(t, err)
	assert.Equal(t, LuaCodeScriptNotFound, out.Error.Code)
}

func TestManageLuaScripts_PublishDisabled(t *testing.T) {
	st := newTestScriptStore(t)
	ag, _ := newScriptsAgent(t, "agent-a", st, false)
	ctx := runLuaCtx()
	res := manage(t, ag, ctx, map[string]interface{}{"action": "save", "name": "abc", "description": "d", "script": "return 1",
		"manifest": map[string]interface{}{"parameters": sumParams}})
	require.True(t, res.Success)
	res = manage(t, ag, ctx, map[string]interface{}{"action": "publish", "name": "abc"})
	assert.Equal(t, LuaCodePolicyDenied, res.Error.Code)
}

func TestRegisterLuaScriptTools(t *testing.T) {
	st := newTestScriptStore(t)
	ctx := context.Background()
	for _, name := range []string{"published_one", "custom_one", "draft_one"} {
		_, _, err := st.Save(ctx, store.Script{Name: name, Description: "d", Source: "return 1",
			Manifest: &store.Manifest{Parameters: sumParams}}, false)
		require.NoError(t, err)
	}
	require.NoError(t, st.SetPublished(ctx, "published_one", true))
	require.NoError(t, st.SetPublished(ctx, "custom_one", true))
	require.NoError(t, st.Attach(ctx, "agent-a", "published_one"))

	t.Run("without run_lua nothing registers", func(t *testing.T) {
		ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil))
		rep := ag.RegisterLuaScriptTools(ctx, LuaScriptsOptions{Store: st, SaveEnabled: true}, true, []string{"custom_one"})
		assert.Empty(t, rep.Registered)
		assert.Contains(t, rep.Skipped, ManageLuaScriptsToolName)
		assert.Contains(t, rep.Skipped, "lua_custom_one")
	})
	t.Run("attachments, custom tools and refusals", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Name = "agent-a"
		ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, WithConfig(cfg),
			WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil)))
		require.NoError(t, ag.RegisterRunLuaTool(RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{}), Gate: luasandbox.NewGate(2, 2)}))
		rep := ag.RegisterLuaScriptTools(ctx, LuaScriptsOptions{Store: st, SaveEnabled: false}, true,
			[]string{"custom_one", "draft_one", "missing_one"})
		assert.ElementsMatch(t, []string{"lua_published_one", "lua_custom_one"}, rep.Registered)
		assert.Contains(t, rep.Skipped[ManageLuaScriptsToolName], "save_enabled")
		assert.Contains(t, rep.Skipped["lua_draft_one"], "not published")
		assert.Contains(t, rep.Skipped["lua_missing_one"], "not found")
		assert.True(t, ag.tools.IsRegistered("lua_published_one"))
	})
}

func TestLuaScriptFromImplementation(t *testing.T) {
	name, ok := LuaScriptFromImplementation("lua://weekly_report")
	assert.True(t, ok)
	assert.Equal(t, "weekly_report", name)
	for _, impl := range []string{"lua://", "/path/plugin.so", "LUA://x", ""} {
		_, ok := LuaScriptFromImplementation(impl)
		assert.False(t, ok, impl)
	}
}

// The registry registers saved-script tools after its builtin filter, and a
// lua:// custom tool no longer logs "not yet implemented".
func TestRegistry_RegisterLuaScripts(t *testing.T) {
	st := newTestScriptStore(t)
	ctx := context.Background()
	_, _, err := st.Save(ctx, store.Script{Name: "custom_one", Description: "d", Source: "return 1",
		Manifest: &store.Manifest{Parameters: sumParams}}, false)
	require.NoError(t, err)
	require.NoError(t, st.SetPublished(ctx, "custom_one", true))

	core, logs := observer.New(zapcore.InfoLevel)
	runOpts := &RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{}), Gate: luasandbox.NewGate(2, 2)}
	r := &Registry{logger: zap.New(core), runLuaTool: runOpts, luaScripts: &LuaScriptsOptions{Store: st, SaveEnabled: true}}
	cfg := &loomv1.AgentConfig{Name: "agent-a", Tools: &loomv1.ToolsConfig{
		Builtin: []string{RunLuaToolName, ManageLuaScriptsToolName},
		Custom:  []*loomv1.CustomToolConfig{{Name: "weekly", Implementation: "lua://custom_one"}},
	}}
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil))
	r.registerRunLua(ag, cfg.Name)
	require.NoError(t, r.registerCustomTools(ctx, ag, cfg.Tools.Custom))
	r.registerLuaScripts(ctx, ag, cfg)
	assert.True(t, ag.tools.IsRegistered(ManageLuaScriptsToolName))
	assert.True(t, ag.tools.IsRegistered("lua_custom_one"))
	assert.Empty(t, logs.FilterMessageSnippet("not yet implemented").All())
}

// luaToolsSeenLLM records the tool names offered on each call.
type luaToolsSeenLLM struct {
	*mockToolCallingLLM
	mu    sync.Mutex
	calls [][]string
}

func (c *luaToolsSeenLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	c.mu.Lock()
	c.calls = append(c.calls, names)
	c.mu.Unlock()
	return c.mockToolCallingLLM.Chat(ctx, messages, tools)
}

// End to end: publishing in one model call advertises lua_<name> on the next,
// and calling it runs the script.
func TestPublishedScriptToolThroughTheConversationLoop(t *testing.T) {
	st := newTestScriptStore(t)
	_, _, err := st.Save(context.Background(), store.Script{Name: "double_it", Description: "Doubles n.", Source: "return args.n * 2",
		Manifest: &store.Manifest{Parameters: map[string]interface{}{"type": "object",
			"properties": map[string]interface{}{"n": map[string]interface{}{"type": "integer"}}}}}, false)
	require.NoError(t, err)

	llm := &luaToolsSeenLLM{mockToolCallingLLM: &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: ManageLuaScriptsToolName, Input: map[string]interface{}{"action": "publish", "name": "double_it"}}}},
		{toolCalls: []llmtypes.ToolCall{{ID: "c2", Name: "lua_double_it", Input: map[string]interface{}{"n": 21}}}},
		{content: "done"},
	}}}
	cfg := DefaultConfig()
	cfg.Name = "agent-a"
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil)))
	require.NoError(t, ag.RegisterRunLuaTool(RunLuaToolOptions{Policy: staticLuaPolicy(luasandbox.Policy{}), Gate: luasandbox.NewGate(2, 2), Resolve: StoreScriptResolver(st)}))
	ag.RegisterLuaScriptTools(context.Background(), LuaScriptsOptions{Store: st, SaveEnabled: true, PublishEnabled: true}, true, nil)

	resp, err := ag.Chat(context.Background(), "lua-publish-session", "publish double_it and use it")
	require.NoError(t, err)

	llm.mu.Lock()
	calls := llm.calls
	llm.mu.Unlock()
	require.GreaterOrEqual(t, len(calls), 2)
	assert.NotContains(t, calls[0], "lua_double_it")
	assert.Contains(t, calls[1], "lua_double_it", "published on call 1, advertised on call 2")

	var got *shuttle.Result
	for _, te := range resp.ToolExecutions {
		if te.ToolName == "lua_double_it" {
			got = te.Result
		}
	}
	require.NotNil(t, got)
	require.True(t, got.Success, "%+v", got.Error)
	assert.EqualValues(t, 42, got.Data)
}
