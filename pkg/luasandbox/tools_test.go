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

package luasandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolsCallResultShape(t *testing.T) {
	h := newFakeHost().
		with("echo", echo).
		with("text", text("plain words")).
		with("json_text", text(`{"rows":[{"id":1},{"id":2}],"total_row_count":2}`)).
		with("bad", failing("E_BAD", "it broke"))
	res := runSrc(t, `
		local e = tools.call("echo", {a = 1, nested = {b = {1, 2}}})
		local p = tools.call("text")
		local j = tools.call("json_text")
		local b = tools.call("bad", {})
		return {
			echo_ok = e.ok, echo_a = e.data.a, echo_b2 = e.data.nested.b[2], echo_text = e.text,
			text_data = p.data, text_text = p.text,
			json_rows = #j.data.rows, json_id2 = j.data.rows[2].id, json_raw = j.text,
			bad_ok = b.ok, bad_code = b.error.code, bad_msg = b.error.message,
			bad_hint = b.error.suggestion, bad_retry = b.error.retryable, bad_data = b.data,
			ms_is_number = type(e.ms) == "number",
		}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	v := res.Value.(map[string]any)
	assert.Equal(t, true, v["echo_ok"])
	assert.Equal(t, int64(1), v["echo_a"])
	assert.Equal(t, int64(2), v["echo_b2"])
	assert.Nil(t, v["echo_text"], "structured data has no text form")
	assert.Equal(t, "plain words", v["text_data"])
	assert.Equal(t, "plain words", v["text_text"])
	assert.Equal(t, int64(2), v["json_rows"])
	assert.Equal(t, int64(2), v["json_id2"])
	assert.Equal(t, `{"rows":[{"id":1},{"id":2}],"total_row_count":2}`, v["json_raw"])
	assert.Equal(t, false, v["bad_ok"])
	assert.Equal(t, "E_BAD", v["bad_code"])
	assert.Equal(t, "it broke", v["bad_msg"])
	assert.Equal(t, "try again", v["bad_hint"])
	assert.Equal(t, true, v["bad_retry"])
	assert.Nil(t, v["bad_data"])
	assert.Equal(t, true, v["ms_is_number"])

	assert.Equal(t, []string{"echo", "text", "json_text", "bad"}, h.callNames())
	require.Len(t, res.Calls, 4)
	assert.Equal(t, CallRecord{Tool: "bad", OK: false, Code: "E_BAD", Millis: res.Calls[3].Millis, Decision: "allow"}, res.Calls[3])
}

func TestToolsCallArguments(t *testing.T) {
	h := newFakeHost().with("echo", echo)
	tests := []struct {
		name, src, errContains string
	}{
		{"no name", `tools.call()`, "bad argument #1"},
		{"number name", `tools.call(5)`, "bad argument #1"},
		{"empty name", `tools.call("")`, "bad argument #1"},
		{"string args", `tools.call("echo", "x")`, "bad argument #2"},
		{"list args", `tools.call("echo", {1, 2})`, "not a list"},
		{"function in args", `tools.call("echo", {f = print})`, "cannot convert a function"},
		{"huge args", `tools.call("echo", {s = string.rep("x", 100000)})`, "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runSrc(t, tt.src, h)
			assert.Equal(t, OutcomeScriptError, res.Outcome)
			assert.Contains(t, res.Error, tt.errContains)
		})
	}
	t.Run("nil and empty args", func(t *testing.T) {
		res := runSrc(t, `local a = tools.call("echo") local b = tools.call("echo", {}) return {a = a.ok, b = b.ok}`, h)
		require.Equal(t, OutcomeOK, res.Outcome, res.Error)
		assert.Equal(t, map[string]any{"a": true, "b": true}, res.Value)
	})
}

func TestToolsMust(t *testing.T) {
	h := newFakeHost().with("echo", echo).with("bad", failing("E_BAD", "it broke"))
	res := runSrc(t, `return tools.must("echo", {x = "y"}).x`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, "y", res.Value)

	res = runSrc(t, `tools.must("bad")`, h)
	assert.Equal(t, OutcomeScriptError, res.Outcome)
	assert.Contains(t, res.Error, "bad: E_BAD: it broke")

	res = runSrc(t, `local ok, e = pcall(tools.must, "bad") return {ok, e}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, false, res.Value.([]any)[0])
}

func TestToolsCallTimeout(t *testing.T) {
	h := newFakeHost().with("slow", blocking)
	lim := small()
	lim.ToolCallTimeout = 50 * time.Millisecond
	res := Run(context.Background(), Program{Source: `local r = tools.call("slow") return {r.ok, r.error.code, r.error.retryable}`}, lim, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{false, CodeToolCallTimeout, true}, res.Value)
	assert.Equal(t, CodeToolCallTimeout, res.Calls[0].Code)
}

func TestToolsCallTruncation(t *testing.T) {
	big := strings.Repeat("abcdefgh", 32<<10) // 256 KiB, above small()'s 64 KiB
	rows := make([]any, 0, 5000)
	for i := range 5000 {
		rows = append(rows, map[string]any{"id": i, "name": "row"})
	}
	h := newFakeHost().with("big_text", text(big)).with("big_rows", func(context.Context, map[string]any) (*CallResult, error) {
		return &CallResult{OK: true, Data: rows}, nil
	})
	res := runSrc(t, `
		local a = tools.call("big_text")
		local b = tools.call("big_rows")
		return {a_len = #a.data, a_cut = a.truncated, a_bytes = a.bytes,
		        b_cut = b.truncated, b_flag = b.data.truncated, b_preview = type(b.data.preview), b_bytes = b.bytes}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	v := res.Value.(map[string]any)
	assert.LessOrEqual(t, v["a_len"], int64(small().MaxCallResultBytes))
	assert.Equal(t, true, v["a_cut"])
	assert.Equal(t, int64(len(big)), v["a_bytes"])
	assert.Equal(t, true, v["b_cut"])
	assert.Equal(t, true, v["b_flag"])
	assert.Equal(t, "string", v["b_preview"])
	assert.Greater(t, v["b_bytes"], int64(small().MaxCallResultBytes))
	assert.True(t, res.Truncated.CallResults)
}

func TestToolResultsAreChargedToMemory(t *testing.T) {
	// Each call returns 60 KiB of text, under the per-call cap. Keeping
	// every result must exhaust the 2 MiB memory budget well before the
	// 100-call budget.
	payload := strings.Repeat("x", 60<<10)
	h := newFakeHost().with("blob", text(payload))
	lim := small()
	lim.MemoryBytes = 2 << 20
	lim.MaxToolCalls = 100
	res := Run(context.Background(), Program{Source: `local keep = {} for i = 1, 100 do keep[i] = tools.call("blob").data end`}, lim, h)
	require.Equal(t, OutcomeBudgetExceeded, res.Outcome, res.Error)
	assert.Equal(t, LimitMemory, res.Limit)
	assert.Less(t, len(h.callNames()), 100)
}

func TestToolsListAndSchema(t *testing.T) {
	h := newFakeHost().with("a_tool", echo).with("b_tool", echo)
	h.schemas["a_tool"] = map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}
	res := runSrc(t, `
		local l1 = tools.list()
		local l2 = tools.list()
		return {n = #l1, first = l1[1].name, desc = l1[1].description, again = #l2,
		        schema_type = tools.schema("a_tool").type, q = tools.schema("a_tool").properties.q.type,
		        missing = tools.schema("b_tool") == nil}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"n": int64(2), "first": "a_tool", "desc": "fake a_tool", "again": int64(2),
		"schema_type": "object", "q": "string", "missing": true}, res.Value)
	assert.Equal(t, 1, h.listHits, "the visible tool list is read once per run")
	assert.Empty(t, h.callNames(), "list and schema are not tool calls")
}

func TestTurnResult(t *testing.T) {
	h := newFakeHost()
	h.turn[42] = &CallResult{OK: true, Data: `[{"status":"ok"},{"status":"ok"},{"status":"bad"}]`}
	res := runSrc(t, `
		local r = turn.result(42)
		local counts = {}
		for _, row in ipairs(r.data) do counts[row.status] = (counts[row.status] or 0) + 1 end
		local missing = turn.result(7)
		return {ok = counts.ok, bad = counts.bad, missing = missing.ok}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"ok": int64(2), "bad": int64(1), "missing": false}, res.Value)
	assert.Equal(t, "turn.result", res.Calls[0].Tool)

	res = runSrc(t, `turn.result(0)`, h)
	assert.Equal(t, OutcomeScriptError, res.Outcome)
	assert.Contains(t, res.Error, "positive message id")
}

func TestProgressEvents(t *testing.T) {
	h := newFakeHost().with("echo", echo).with("bad", failing("E", "x"))
	res := runSrc(t, `tools.call("echo") tools.call("bad") time.sleep(1)`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	require.Len(t, h.events, 5)
	assert.Equal(t, ProgressEvent{Kind: ProgressToolStarted, Tool: "echo", Seq: 1, Max: 10}, h.events[0])
	assert.Equal(t, ProgressToolCompleted, h.events[1].Kind)
	assert.True(t, h.events[1].OK)
	assert.Equal(t, 2, h.events[2].Seq)
	assert.Equal(t, "E", h.events[3].Code)
	assert.Equal(t, ProgressSleep, h.events[4].Kind)
}

// panicHost panics inside CallTool.
type panicHost struct{ *fakeHost }

func (panicHost) CallTool(context.Context, string, map[string]any) (*CallResult, error) {
	panic("tool exploded")
}

// errHost returns a Go error from CallTool: the host itself is broken.
type errHost struct{ *fakeHost }

func (errHost) CallTool(context.Context, string, map[string]any) (*CallResult, error) {
	return nil, errBoom
}

// nilHost returns neither a result nor an error.
type nilHost struct{ *fakeHost }

func (nilHost) CallTool(context.Context, string, map[string]any) (*CallResult, error) {
	return nil, nil
}

func TestHostFailuresEndTheRun(t *testing.T) {
	hosts := map[string]Host{
		"panic":       panicHost{newFakeHost()},
		"error":       errHost{newFakeHost()},
		"nil result":  nilHost{newFakeHost()},
		"list panics": func() Host { h := newFakeHost(); h.panicOn = "ListTools"; return h }(),
		"list errors": func() Host { h := newFakeHost(); h.listErr = errBoom; return h }(),
		"schema":      func() Host { h := newFakeHost(); h.panicOn = "ToolSchema"; return h }(),
	}
	src := map[string]string{
		"list panics": `pcall(tools.list) return "survived"`,
		"list errors": `pcall(tools.list) return "survived"`,
		"schema":      `pcall(tools.schema, "x") return "survived"`,
	}
	for name, h := range hosts {
		t.Run(name, func(t *testing.T) {
			s := src[name]
			if s == "" {
				s = `pcall(tools.call, "x") return "survived"` // pcall must not hide a broken host
			}
			res := Run(context.Background(), Program{Source: s}, small(), h)
			assert.Equal(t, OutcomeHostError, res.Outcome, res.Error)
			assert.NotContains(t, res.Error, "exploded", "panic values stay out of model-facing text")
			assert.NotEmpty(t, res.Detail)
		})
	}
	t.Run("progress panic is ignored", func(t *testing.T) {
		h := newFakeHost().with("echo", echo)
		h.panicOn = "Progress"
		res := Run(context.Background(), Program{Source: `return tools.call("echo").ok`}, small(), h)
		assert.Equal(t, OutcomeOK, res.Outcome, res.Error)
		assert.Equal(t, true, res.Value)
		assert.Contains(t, res.Detail, "Progress panicked")
	})
}
