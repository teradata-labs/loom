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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// luaTestTool is a fake tool that records its executions.
type luaTestTool struct {
	name   string
	desc   string
	result func(params map[string]interface{}) *shuttle.Result
	onRun  func()
	runs   atomic.Int64
}

func (t *luaTestTool) Name() string { return t.name }
func (t *luaTestTool) Description() string {
	if t.desc != "" {
		return t.desc
	}
	return "Fake tool " + t.name + ". It records its executions for tests."
}
func (t *luaTestTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema("", map[string]*shuttle.JSONSchema{"q": shuttle.NewStringSchema("query")}, nil)
}
func (t *luaTestTool) Backend() string { return "" }
func (t *luaTestTool) Execute(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.runs.Add(1)
	if t.onRun != nil {
		t.onRun()
	}
	if t.result != nil {
		return t.result(params), nil
	}
	return &shuttle.Result{Success: true, Data: map[string]interface{}{"echo": params["q"]}}, nil
}

// luaRecordingHook matches every call, records it, and answers with the
// decision configured for the tool (Allow by default). evaluate, when set,
// overrides the decision per evaluation.
type luaRecordingHook struct {
	mu        sync.Mutex
	seen      []string
	decisions map[string]shuttle.Decision
	evaluate  func(req shuttle.AdmissionRequest, n int) (shuttle.Decision, bool)
}

func (h *luaRecordingHook) Matches(shuttle.AdmissionRequest) bool { return true }
func (h *luaRecordingHook) Evaluate(req shuttle.AdmissionRequest) shuttle.Decision {
	h.mu.Lock()
	h.seen = append(h.seen, req.ToolName)
	n := len(h.seen)
	h.mu.Unlock()
	if h.evaluate != nil {
		if d, ok := h.evaluate(req, n); ok {
			return d
		}
	}
	if d, ok := h.decisions[req.ToolName]; ok {
		return d
	}
	return shuttle.Decision{Kind: shuttle.Allow}
}
func (h *luaRecordingHook) calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.seen...)
}

// luaBlockingResolver blocks until released, like a human who never answers.
type luaBlockingResolver struct {
	called  atomic.Int64
	release chan struct{}
}

func (r *luaBlockingResolver) Resolve(shuttle.AdmissionRequest, shuttle.Decision) shuttle.Decision {
	r.called.Add(1)
	<-r.release
	return shuttle.Decision{Kind: shuttle.Allow}
}

// newLuaTestAgent builds an agent with chain (nil for none) and tools.
func newLuaTestAgent(t *testing.T, chain *shuttle.Chain, tools ...shuttle.Tool) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	opts := []Option{WithConfig(cfg)}
	if chain != nil {
		opts = append(opts, WithAdmissionHooks(chain))
	}
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{}, opts...)
	for _, tool := range tools {
		ag.RegisterTool(tool)
	}
	return ag
}

// luaCtx returns a tool-call context with a session id and the given
// advertised projection (nil means no projection at all).
func luaCtx(projection []string) context.Context {
	ctx := context.WithValue(context.Background(), "session_id", "lua-test-session") //nolint:staticcheck // the agent's own string key
	if projection != nil {
		ctx = context.WithValue(ctx, advertisedProjectionKey{}, projection)
	}
	return ctx
}

func newTestHost(t *testing.T, ag *Agent, ctx context.Context, opt LuaHostOptions) luasandbox.Host {
	t.Helper()
	if opt.SessionID == "" {
		opt.SessionID = "lua-test-session"
	}
	h, err := ag.NewLuaHost(ctx, opt)
	require.NoError(t, err)
	return h
}

// I1: every nested call passes through the admission chain.
func TestLuaHost_NestedCallsRunThroughAdmission(t *testing.T) {
	hook := &luaRecordingHook{}
	read := &luaTestTool{name: "read_note"}
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{hook}, nil, nil), read)
	h := newTestHost(t, ag, luaCtx([]string{"read_note"}), LuaHostOptions{})

	for i := 0; i < 3; i++ {
		res, err := h.CallTool(context.Background(), "read_note", map[string]any{"q": fmt.Sprint(i)})
		require.NoError(t, err)
		require.True(t, res.OK, "%+v", res.Error)
		assert.Equal(t, "allow", res.Decision)
	}
	assert.EqualValues(t, 3, read.runs.Load())
	// Preflight and Admit each evaluate the hook once per call.
	assert.Equal(t, []string{"read_note", "read_note", "read_note", "read_note", "read_note", "read_note"}, hook.calls())
}

// I2: only the tools the model was shown are visible.
func TestLuaHost_OnlyTheAdvertisedProjectionIsVisible(t *testing.T) {
	shown := &luaTestTool{name: "shown"}
	hidden := &luaTestTool{name: "hidden"} // registered, but not in the projection
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil), shown, hidden)
	h := newTestHost(t, ag, luaCtx([]string{"shown"}), LuaHostOptions{})

	res, err := h.CallTool(context.Background(), "hidden", nil)
	require.NoError(t, err)
	require.False(t, res.OK)
	assert.Equal(t, luasandbox.CodeToolNotVisible, res.Error.Code)
	assert.Equal(t, "not_visible", res.Decision)
	assert.EqualValues(t, 0, hidden.runs.Load(), "a hidden tool must never run")

	tools, err := h.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "shown", tools[0].Name)

	_, err = h.ToolSchema(context.Background(), "hidden")
	assert.ErrorIs(t, err, luasandbox.ErrToolNotVisible)
	schema, err := h.ToolSchema(context.Background(), "shown")
	require.NoError(t, err)
	assert.Equal(t, "object", schema["type"])
}

// I14: no session, no projection or no guard → no host.
func TestLuaHost_FailsClosed(t *testing.T) {
	tool := &luaTestTool{name: "t"}
	guarded := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil), tool)
	unguarded := newLuaTestAgent(t, nil, tool)

	_, err := guarded.NewLuaHost(luaCtx([]string{"t"}), LuaHostOptions{})
	assert.ErrorIs(t, err, ErrLuaNoSession)

	_, err = guarded.NewLuaHost(luaCtx(nil), LuaHostOptions{SessionID: "s"})
	assert.ErrorIs(t, err, ErrLuaNoProjection)

	_, err = unguarded.NewLuaHost(luaCtx([]string{"t"}), LuaHostOptions{SessionID: "s"})
	assert.ErrorIs(t, err, ErrLuaNoGuards)

	_, err = guarded.NewLuaHost(luaCtx([]string{"t"}), LuaHostOptions{SessionID: "s", Policy: luasandbox.Policy{Deny: []string{"["}}})
	assert.Error(t, err, "a malformed pattern must refuse the host")
}

// I3: an approval granted to run_lua itself never covers the script's calls.
func TestLuaHost_ApprovalGrantDoesNotCoverNestedCalls(t *testing.T) {
	hook := &luaRecordingHook{decisions: map[string]shuttle.Decision{"drop_table": {Kind: shuttle.Ask}}}
	drop := &luaTestTool{name: "drop_table"}
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{hook}, nil, nil), drop)
	granted := shuttle.ContextWithAskGrant(luaCtx([]string{"drop_table"}), &shuttle.AskGrant{Approved: true})
	h := newTestHost(t, ag, granted, LuaHostOptions{})

	res, err := h.CallTool(granted, "drop_table", nil)
	require.NoError(t, err)
	require.False(t, res.OK)
	assert.Equal(t, luasandbox.CodeApprovalRequired, res.Error.Code)
	assert.Equal(t, "approval_required", res.Decision)
	assert.EqualValues(t, 0, drop.runs.Load(), "the tool body must not run")
}

// I4: a script never waits for a human, not even when a hook answers Ask only
// at execution time (after Preflight allowed the call).
func TestLuaHost_NeverBlocksOnAHuman(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook *luaRecordingHook
	}{
		{"ask at preflight", &luaRecordingHook{decisions: map[string]shuttle.Decision{"risky": {Kind: shuttle.Ask}}}},
		{"ask only at execution", &luaRecordingHook{evaluate: func(_ shuttle.AdmissionRequest, n int) (shuttle.Decision, bool) {
			if n%2 == 0 { // the second evaluation of each call is Admit's
				return shuttle.Decision{Kind: shuttle.Ask}, true
			}
			return shuttle.Decision{Kind: shuttle.Allow}, true
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &luaBlockingResolver{release: make(chan struct{})}
			defer close(resolver.release)
			risky := &luaTestTool{name: "risky"}
			ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{tc.hook}, nil, resolver), risky)
			h := newTestHost(t, ag, luaCtx([]string{"risky"}), LuaHostOptions{})

			start := time.Now()
			res, err := h.CallTool(context.Background(), "risky", nil)
			elapsed := time.Since(start)
			require.NoError(t, err)
			require.False(t, res.OK)
			assert.Equal(t, luasandbox.CodeApprovalRequired, res.Error.Code)
			assert.Less(t, elapsed, 50*time.Millisecond)
			assert.EqualValues(t, 0, resolver.called.Load(), "the blocking resolver must never be called")
			assert.EqualValues(t, 0, risky.runs.Load())
		})
	}
}

// I5: unknown names never register tools.
func TestLuaHost_UnknownNamesNeverRegisterTools(t *testing.T) {
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil), &luaTestTool{name: "known"})
	h := newTestHost(t, ag, luaCtx([]string{"known"}), LuaHostOptions{})
	before := ag.tools.Count()
	for i := 0; i < 100; i++ {
		res, err := h.CallTool(context.Background(), fmt.Sprintf("mcp_server_tool_%d", i), nil)
		require.NoError(t, err)
		require.False(t, res.OK)
	}
	assert.Equal(t, before, ag.tools.Count())
}

// I6: the host holds no lock while a nested tool runs, so a tool may call
// back into the agent.
func TestLuaHost_NestedToolCanCallTheAgent(t *testing.T) {
	var ag *Agent
	reentrant := &luaTestTool{name: "reentrant"}
	reentrant.onRun = func() { _ = ag.ListTools() }
	ag = newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil), reentrant)
	h := newTestHost(t, ag, luaCtx([]string{"reentrant"}), LuaHostOptions{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := h.CallTool(context.Background(), "reentrant", nil)
			assert.NoError(t, err)
			assert.True(t, res.OK)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("nested calls deadlocked")
	}
}

// I12: the Lua tools, script tools and the engine's hard-deny list are never
// callable, whatever the projection says.
func TestLuaHost_ReservedAndHardDeniedNames(t *testing.T) {
	names := []string{RunLuaToolName, ManageLuaScriptsToolName, "lua_report", "contact_human", "delegate_to_agent", "ok_tool"}
	var tools []shuttle.Tool
	for _, n := range names {
		tools = append(tools, &luaTestTool{name: n})
	}
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil), tools...)
	h := newTestHost(t, ag, luaCtx(names), LuaHostOptions{})

	for _, n := range names[:5] {
		res, err := h.CallTool(context.Background(), n, nil)
		require.NoError(t, err)
		require.False(t, res.OK, n)
		assert.Equal(t, luasandbox.CodeHardDenied, res.Error.Code, n)
		assert.Equal(t, "hard_denied", res.Decision, n)
	}
	res, err := h.CallTool(context.Background(), "ok_tool", nil)
	require.NoError(t, err)
	assert.True(t, res.OK)
}

func TestLuaHost_PolicyTrustAndRequires(t *testing.T) {
	names := []string{"query", "web_search", "file_write"}
	var tools []shuttle.Tool
	for _, n := range names {
		tools = append(tools, &luaTestTool{name: n})
	}
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil), tools...)
	pol := luasandbox.Policy{Deny: []string{"file_*"}, DenyForShared: []string{"web_search"}}

	visible := func(opt LuaHostOptions) []string {
		opt.Policy = pol
		h := newTestHost(t, ag, luaCtx(names), opt)
		list, err := h.ListTools(context.Background())
		require.NoError(t, err)
		var out []string
		for _, ti := range list {
			out = append(out, ti.Name)
		}
		return out
	}
	assert.Equal(t, []string{"query", "web_search"}, visible(LuaHostOptions{Trust: luasandbox.TrustOwn}))
	assert.Equal(t, []string{"query"}, visible(LuaHostOptions{Trust: luasandbox.TrustShared}))
	assert.Equal(t, []string{"web_search"}, visible(LuaHostOptions{Trust: luasandbox.TrustOwn, Requires: []string{"web_search", "file_write"}}))
	assert.Empty(t, visible(LuaHostOptions{Trust: luasandbox.TrustOwn, Requires: []string{}}))
}

func TestLuaHost_MapsToolResults(t *testing.T) {
	hook := &luaRecordingHook{decisions: map[string]shuttle.Decision{"denied": {Kind: shuttle.Deny, Reason: "not on weekends"}}}
	tools := []shuttle.Tool{
		&luaTestTool{name: "denied"},
		&luaTestTool{name: "failing", result: func(map[string]interface{}) *shuttle.Result {
			return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "QUERY_FAILED", Message: "syntax error", Suggestion: "fix the SQL", Retryable: true}}
		}},
		&luaTestTool{name: "long_job", result: func(map[string]interface{}) *shuttle.Result {
			return &shuttle.Result{Success: true, Data: "started", AwaitResource: &shuttle.AwaitResource{URI: "gdp://jobs/7"}}
		}},
		&luaTestTool{name: "bare_failure", result: func(map[string]interface{}) *shuttle.Result {
			return &shuttle.Result{Success: false}
		}},
	}
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{hook}, nil, nil), tools...)
	h := newTestHost(t, ag, luaCtx([]string{"denied", "failing", "long_job", "bare_failure"}), LuaHostOptions{})

	res, err := h.CallTool(context.Background(), "denied", nil)
	require.NoError(t, err)
	assert.Equal(t, luasandbox.CodePermissionDenied, res.Error.Code)
	assert.Equal(t, "not on weekends", res.Error.Message)
	assert.Equal(t, "deny", res.Decision)

	res, err = h.CallTool(context.Background(), "failing", nil)
	require.NoError(t, err)
	assert.Equal(t, &luasandbox.CallError{Code: "QUERY_FAILED", Message: "syntax error", Suggestion: "fix the SQL", Retryable: true}, res.Error)

	res, err = h.CallTool(context.Background(), "long_job", nil)
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Equal(t, luasandbox.CodeResourcePending, res.Error.Code)
	assert.Equal(t, "gdp://jobs/7", res.Error.Message)
	assert.True(t, res.Error.Retryable)

	res, err = h.CallTool(context.Background(), "bare_failure", nil)
	require.NoError(t, err)
	assert.Equal(t, luasandbox.CodeExecutionFailed, res.Error.Code)
}

func TestLuaHost_TurnResultMissIsAToolFailure(t *testing.T) {
	ag := newLuaTestAgent(t, shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil))
	h := newTestHost(t, ag, luaCtx([]string{}), LuaHostOptions{})
	res, err := h.TurnResult(context.Background(), 42)
	require.NoError(t, err)
	require.False(t, res.OK)
	assert.Equal(t, "NOT_FOUND", res.Error.Code)
}

func TestOneLineSummary(t *testing.T) {
	assert.Equal(t, "Reads a file.", oneLineSummary("Reads a file. Supports offsets and limits."))
	assert.Equal(t, "First line", oneLineSummary("First line\nsecond line"))
	long := strings.Repeat("wörd ", 60)
	got := oneLineSummary(long)
	assert.LessOrEqual(t, len(got), luaToolSummaryMax+len("…"))
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.True(t, utf8.ValidString(got))
}
