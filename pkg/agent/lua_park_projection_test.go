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
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/luasandbox"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// probeScript lists the tools the script sees and calls target, reporting both
// in one string the test can find in the tool row.
func probeScript(target string) string {
	return fmt.Sprintf(`local names = {}
for _, t in ipairs(tools.list()) do names[#names + 1] = t.name end
local r = tools.call(%q, {})
local code = "called"
if not r.ok then code = r.error.code end
return "names=" .. table.concat(names, ",") .. ";code=" .. code .. ";"`, target)
}

var probeResult = regexp.MustCompile(`names=([^;]*);code=([^;]*);`)

// toolRowContent returns the content of the tool row answering callID.
func toolRowContent(t *testing.T, ag *Agent, sessionID, callID string) string {
	t.Helper()
	sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, ag.config.Name, "")
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "tool" && m.ToolUseID == callID {
			return m.Content
		}
	}
	t.Fatalf("no tool row for %s", callID)
	return ""
}

// parkedLuaFixture is a park-enabled agent with run_lua behind an Ask hook,
// so a run_lua batch parks before it runs.
func parkedLuaFixture(t *testing.T, responses []mockLLMResponse, toolNames ...string) *parkFixture {
	t.Helper()
	f := newParkFixture(t, []shuttle.Hook{scopedAskHook{tool: RunLuaToolName}}, responses, toolNames...)
	require.NoError(t, f.ag.RegisterRunLuaTool(RunLuaToolOptions{
		Policy: staticLuaPolicy(luasandbox.Policy{}),
		Gate:   luasandbox.NewGate(2, 1),
	}))
	return f
}

func runLuaCall(id, script string) llmtypes.ToolCall {
	return llmtypes.ToolCall{ID: id, Name: RunLuaToolName, Input: map[string]interface{}{"script": script}}
}

// approveParked approves every item of the session's pending park.
func approveParked(t *testing.T, f *parkFixture, sessionID string) {
	t.Helper()
	hr := f.pendingParked(t, sessionID)
	_, err := f.ag.ResumeChat(context.Background(), sessionID, ParkDecision{
		RequestID: hr.ID,
		ItemIDs:   paramKeys(hr),
		Approved:  true,
	}, nil)
	require.NoError(t, err)
}

// A run_lua call resumed from a park sees the tools the model was shown for
// the provider call that requested it, never the resume-time set (review:
// Ed-Jaramillo, #426).
func TestParkedRunLua_UsesTheRequestingCallsProjection(t *testing.T) {
	tests := []struct {
		name string
		// target is the tool the script lists and calls.
		target string
		// responses script the model; the run_lua call is "call_lua".
		responses []mockLLMResponse
		toolNames []string
		// beforePark runs before the first Chat; whilePark between the park
		// and the approval.
		beforePark func(t *testing.T, f *parkFixture)
		whilePark  func(t *testing.T, f *parkFixture, sessionID string)
	}{
		{
			name:   "tool registered for the session while parked",
			target: "late_note",
			responses: []mockLLMResponse{
				{toolCalls: []llmtypes.ToolCall{runLuaCall("call_lua", probeScript("late_note"))}},
				{content: "continued"},
			},
			toolNames: []string{"read_note"},
			whilePark: func(t *testing.T, f *parkFixture, sessionID string) {
				late := &countingTool{name: "late_note"}
				f.ag.RegisterTool(late)
				f.ag.registerSessionTool(sessionID, "late_note")
				f.tools["late_note"] = late
				sess := f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, f.ag.config.Name, "")
				require.Contains(t, advertisedNames(f.ag, sess), "late_note", "the resume-time set includes the late tool")
			},
		},
		{
			name:   "tool removed by recovery on the requesting call",
			target: "flaky_note",
			responses: []mockLLMResponse{
				// The first call finds the breaker open; recovery disables
				// flaky_note, so the next provider call does not offer it.
				{toolCalls: []llmtypes.ToolCall{{ID: "call_flaky", Name: "flaky_note", Input: map[string]interface{}{}}}},
				{toolCalls: []llmtypes.ToolCall{runLuaCall("call_lua", probeScript("flaky_note"))}},
				{content: "continued"},
			},
			toolNames: []string{"read_note", "flaky_note"},
			beforePark: func(t *testing.T, f *parkFixture) {
				require.True(t, f.ag.config.EnableSelfHealing)
				require.NotNil(t, f.ag.circuitBreakers)
				br := f.ag.circuitBreakers.GetBreaker(f.ag.executor.CanonicalToolName("flaky_note"))
				for range 10 {
					_ = br.Execute(func() error { return errors.New("backend down") })
				}
			},
			whilePark: func(t *testing.T, f *parkFixture, sessionID string) {
				names, ok := parkedProjection(f.pendingParked(t, sessionID))
				require.True(t, ok)
				require.NotContains(t, names, "flaky_note", "recovery removed the tool from the requesting call")
				sess := f.ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, f.ag.config.Name, "")
				require.Contains(t, advertisedNames(f.ag, sess), "flaky_note", "the resume-time set brings it back")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const sessionID = "s-lua-park"
			f := parkedLuaFixture(t, tt.responses, tt.toolNames...)
			if tt.beforePark != nil {
				tt.beforePark(t, f)
			}
			_, err := f.ag.Chat(context.Background(), sessionID, "go")
			var parked *TurnParkedError
			require.ErrorAs(t, err, &parked)
			if tt.whilePark != nil {
				tt.whilePark(t, f, sessionID)
			}

			approveParked(t, f, sessionID)

			content := toolRowContent(t, f.ag, sessionID, "call_lua")
			m := probeResult.FindStringSubmatch(content)
			require.NotNil(t, m, "run_lua did not return the probe: %s", content)
			assert.Contains(t, m[1], "read_note", "a tool the model was shown stays visible")
			assert.NotContains(t, m[1], tt.target, "tools.list() must omit a tool the model was not shown")
			assert.Equal(t, luasandbox.CodeToolNotVisible, m[2], "tools.call() must refuse a tool the model was not shown")
			assert.EqualValues(t, 0, f.tools[tt.target].runs.Load())
		})
	}
}

// A parked batch without a recorded projection (parked by an older build) runs
// no script: run_lua fails closed with a tool error.
func TestParkedRunLua_FailsClosedWithoutAProjection(t *testing.T) {
	const sessionID = "s-lua-legacy"
	f := parkedLuaFixture(t, []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{runLuaCall("call_lua", probeScript("read_note"))}},
		{content: "continued"},
	}, "read_note")
	_, err := f.ag.Chat(context.Background(), sessionID, "go")
	var parked *TurnParkedError
	require.ErrorAs(t, err, &parked)

	hr := f.pendingParked(t, sessionID)
	delete(hr.Context, parkProjectionContextKey)
	require.NoError(t, f.store.Update(context.Background(), hr))

	approveParked(t, f, sessionID)

	content := toolRowContent(t, f.ag, sessionID, "call_lua")
	assert.NotRegexp(t, probeResult, content, "the script must not run")
	assert.Contains(t, content, LuaCodePolicyDenied)
	assert.Contains(t, content, ErrLuaNoProjection.Error())
	assert.EqualValues(t, 0, f.tools["read_note"].runs.Load())
}

// The park records the projection, and parkedProjection reads it back from
// every shape a store returns.
func TestParkedProjection(t *testing.T) {
	tests := []struct {
		name  string
		ctx   map[string]interface{}
		want  []string
		found bool
	}{
		{"in-memory store keeps the slice", map[string]interface{}{parkProjectionContextKey: []string{"a", "b"}}, []string{"a", "b"}, true},
		{"JSON stores decode to interfaces", map[string]interface{}{parkProjectionContextKey: []interface{}{"a", "b"}}, []string{"a", "b"}, true},
		{"empty projection is a projection", map[string]interface{}{parkProjectionContextKey: []interface{}{}}, []string{}, true},
		{"missing key", map[string]interface{}{"kind": "parked"}, nil, false},
		{"nil context", nil, nil, false},
		{"wrong type", map[string]interface{}{parkProjectionContextKey: "a,b"}, nil, false},
		{"non-string element", map[string]interface{}{parkProjectionContextKey: []interface{}{"a", 1}}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parkedProjection(&shuttle.HumanRequest{Context: tt.ctx})
			assert.Equal(t, tt.found, ok)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("recorded at park", func(t *testing.T) {
		f := parkedLuaFixture(t, []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{runLuaCall("call_lua", "return 1")}},
			{content: "continued"},
		}, "read_note")
		_, err := f.ag.Chat(context.Background(), "s-lua-rec", "go")
		var parked *TurnParkedError
		require.ErrorAs(t, err, &parked)
		names, ok := parkedProjection(f.pendingParked(t, "s-lua-rec"))
		require.True(t, ok)
		assert.Contains(t, names, "read_note")
		assert.Contains(t, names, RunLuaToolName)
	})
}

func TestRestrictToProjection(t *testing.T) {
	tools := []shuttle.Tool{namedStubTool{name: "a"}, namedStubTool{name: "b"}, namedStubTool{name: "c"}}
	tests := []struct {
		name  string
		names []string
		want  []string
	}{
		{"intersection keeps current order", []string{"c", "a", "z"}, []string{"a", "c"}},
		{"empty projection keeps nothing", []string{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toolNames(restrictToProjection(tools, tt.names)))
		})
	}
}
