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

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/luasandbox/store"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// parkedScriptToolFixture is a park-enabled agent with a published saved
// script "probe" (running probeScript(target)) registered as lua_probe behind
// an Ask hook, so a lua_probe batch parks before it runs.
func parkedScriptToolFixture(t *testing.T, target string, responses []mockLLMResponse, toolNames ...string) *parkFixture {
	t.Helper()
	st := newTestScriptStore(t)
	ctx := context.Background()
	_, _, err := st.Save(ctx, store.Script{Name: "probe", Description: "Lists and calls tools.", Source: probeScript(target),
		Manifest: &store.Manifest{Parameters: map[string]interface{}{"type": "object"}}}, false)
	require.NoError(t, err)
	require.NoError(t, st.SetPublished(ctx, "probe", true))

	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: "lua_probe"}}, responses, toolNames...)
	require.NoError(t, f.ag.RegisterRunLuaTool(RunLuaToolOptions{
		Policy:  staticLuaPolicy(luasandbox.Policy{}),
		Gate:    luasandbox.NewGate(2, 1),
		Resolve: StoreScriptResolver(st),
	}))
	rep := f.ag.RegisterLuaScriptTools(ctx, LuaScriptsOptions{Store: st, SaveEnabled: true, PublishEnabled: true}, false, []string{"probe"})
	require.Contains(t, rep.Registered, "lua_probe", "%v", rep.Skipped)
	return f
}

// The lua_<name> tools of saved scripts run through runResolved like run_lua,
// so a parked call keeps its requesting call's projection too (review:
// Ed-Jaramillo, #426, ported to #427's script tools).
func TestParkedScriptTool_UsesTheRequestingCallsProjection(t *testing.T) {
	const sessionID = "s-lua-tool-park"
	probe := llmtypes.ToolCall{ID: "call_probe", Name: "lua_probe", Input: map[string]interface{}{}}
	f := parkedScriptToolFixture(t, "late_note", []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{probe}},
		{content: "continued"},
	}, "read_note")

	_, err := f.ag.Chat(context.Background(), sessionID, "go")
	var parked *TurnParkedError
	require.ErrorAs(t, err, &parked)

	late := &countingTool{name: "late_note"}
	f.ag.RegisterTool(late)
	f.ag.registerSessionTool(sessionID, "late_note")
	sess := f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, f.ag.config.Name, "")
	require.Contains(t, advertisedNames(f.ag, sess), "late_note", "the resume-time set includes the late tool")

	approveParked(t, f, sessionID)

	content := toolRowContent(t, f.ag, sessionID, "call_probe")
	m := probeResult.FindStringSubmatch(content)
	require.NotNil(t, m, "lua_probe did not return the probe: %s", content)
	assert.Contains(t, m[1], "read_note")
	assert.NotContains(t, m[1], "late_note", "tools.list() must omit a tool the model was not shown")
	assert.Equal(t, luasandbox.CodeToolNotVisible, m[2], "tools.call() must refuse a tool the model was not shown")
	assert.EqualValues(t, 0, late.runs.Load())
}

// A parked lua_<name> call without a recorded projection fails closed.
func TestParkedScriptTool_FailsClosedWithoutAProjection(t *testing.T) {
	const sessionID = "s-lua-tool-legacy"
	probe := llmtypes.ToolCall{ID: "call_probe", Name: "lua_probe", Input: map[string]interface{}{}}
	f := parkedScriptToolFixture(t, "read_note", []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{probe}},
		{content: "continued"},
	}, "read_note")

	_, err := f.ag.Chat(context.Background(), sessionID, "go")
	var parked *TurnParkedError
	require.ErrorAs(t, err, &parked)
	hr := f.pendingParked(t, sessionID)
	delete(hr.Context, parkProjectionContextKey)
	require.NoError(t, f.store.Update(context.Background(), hr))

	approveParked(t, f, sessionID)

	content := toolRowContent(t, f.ag, sessionID, "call_probe")
	assert.NotRegexp(t, probeResult, content, "the script must not run")
	assert.Contains(t, content, LuaCodePolicyDenied)
	assert.Contains(t, content, ErrLuaNoProjection.Error())
	assert.EqualValues(t, 0, f.tools["read_note"].runs.Load())
}
