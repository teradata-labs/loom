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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

func TestRunLuaTool_SavedScriptNeedsAValidTrustTier(t *testing.T) {
	const src = `local r = tools.call("file_write", {}) return {r.ok, r.error and r.error.code}`
	for _, tc := range []struct {
		name    string
		trust   luasandbox.Trust
		refused bool
		ran     bool
	}{
		{"omitted (zero value)", 0, true, false},
		{"explicit inline", luasandbox.TrustInline, true, false},
		{"out of range", luasandbox.Trust(7), true, false},
		{"own keeps DenyForShared tools", luasandbox.TrustOwn, false, true},
		{"shared loses DenyForShared tools", luasandbox.TrustShared, false, false},
		{"published loses DenyForShared tools", luasandbox.TrustPublished, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write := &luaTestTool{name: "file_write"}
			_, tool := newLuaToolAgent(t, RunLuaToolOptions{
				Policy: staticLuaPolicy(luasandbox.Policy{DenyForShared: []string{"file_write"}}),
				Resolve: func(context.Context, string) (ResolvedScript, error) {
					return ResolvedScript{Name: "report", Source: src, Trust: tc.trust}, nil
				},
			}, write)
			res, err := tool.Execute(runLuaCtx("file_write"), map[string]interface{}{"name": "report"})
			require.NoError(t, err)
			if tc.refused {
				require.False(t, res.Success)
				assert.Equal(t, LuaCodePolicyDenied, res.Error.Code)
				assert.Contains(t, res.Error.Message, "no valid trust tier")
			} else {
				require.True(t, res.Success, "%+v", res.Error)
			}
			ran := write.runs.Load() > 0
			assert.Equal(t, tc.ran, ran, "the DenyForShared tool body ran")
		})
	}
}

func TestRunLuaTool_RefusalsCarryARunRecord(t *testing.T) {
	gate := luasandbox.NewGate(1, 1)
	release, ok := gate.TryAcquire("other")
	require.True(t, ok)
	defer release()
	_, busy := newLuaToolAgent(t, RunLuaToolOptions{Gate: gate})
	_, plain := newLuaToolAgent(t, RunLuaToolOptions{})

	for _, tc := range []struct {
		name   string
		tool   *RunLuaTool
		params map[string]interface{}
		code   string
		script string
		trust  string
	}{
		{"invalid params", plain, map[string]interface{}{}, LuaCodeInvalidParams, "", ""},
		{"busy", busy, map[string]interface{}{"script": "return 1"}, LuaCodeBusy, "inline", "inline"},
		{"saved script lookup", plain, map[string]interface{}{"name": "report"}, LuaCodeScriptNotFound, "report", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.tool.Execute(runLuaCtx(), tc.params)
			require.NoError(t, err)
			require.False(t, res.Success)
			assert.Equal(t, tc.code, res.Error.Code)
			rec := luaRun(t, res)
			assert.Equal(t, LuaOutcomeRefused, rec["outcome"])
			assert.Equal(t, tc.code, rec["refusal"])
			assert.Equal(t, tc.script, rec["script"])
			assert.Equal(t, tc.trust, rec["trust"])
			assert.Equal(t, uint64(0), rec["cpu_ticks"])
			assert.Equal(t, uint64(0), rec["memory_bytes"])
			assert.Empty(t, rec["calls"])
		})
	}
}

func TestLuaHost_RefusedNestedCallsAreTraced(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	tracer := observability.NewMockTracer()
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{},
		WithConfig(cfg), WithTracer(tracer),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{&luaRecordingHook{}}, nil, nil)))
	write := &luaTestTool{name: "file_write"}
	ag.RegisterTool(write)
	tool, err := NewRunLuaTool(ag, RunLuaToolOptions{
		Policy: staticLuaPolicy(luasandbox.Policy{Reserved: []string{"file_write"}}),
		Gate:   luasandbox.NewGate(4, 2),
	})
	require.NoError(t, err)

	res, err := tool.Execute(runLuaCtx("file_write"), map[string]interface{}{
		"script": `return {tools.call("file_write", {}).error.code, tools.call("not_there", {}).error.code}`,
	})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v", res.Error)
	assert.EqualValues(t, 0, write.runs.Load(), "a refused tool body never runs")

	spans := tracer.GetSpansByName("lua.tool_call")
	require.Len(t, spans, 2, "every nested call attempt has a span, refused ones included")
	decisions := map[string]any{}
	for _, sp := range spans {
		decisions[sp.Attributes["tool_name"].(string)] = sp.Attributes["lua.decision"]
	}
	assert.Equal(t, map[string]any{"file_write": "hard_denied", "not_there": "not_visible"}, decisions)
}
