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

func TestRunReturnValues(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want any
	}{
		{"nothing", `local x = 1`, nil},
		{"nil", `return nil`, nil},
		{"integer", `return 42`, int64(42)},
		{"float", `return 1.5`, 1.5},
		{"integral float stays float", `return 3.0`, 3.0},
		{"string", `return "héllo"`, "héllo"},
		{"bool", `return true`, true},
		{"empty table is an object", `return {}`, map[string]any{}},
		{"sequence", `return {1, "two", true}`, []any{int64(1), "two", true}},
		{"object", `return {a = 1, b = {c = "d"}}`, map[string]any{"a": int64(1), "b": map[string]any{"c": "d"}}},
		{"sparse array becomes object", `return {[1] = "a", [3] = "c"}`, map[string]any{"1": "a", "3": "c"}},
		{"rows", `return {{region = "EU", total = 10}, {region = "US", total = 12}}`,
			[]any{map[string]any{"region": "EU", "total": int64(10)}, map[string]any{"region": "US", "total": int64(12)}}},
		{"json.null keeps array positions", `return {1, json.null, 3}`, []any{int64(1), nil, int64(3)}},
		{"first of many returns", `return 1, 2, 3`, int64(1)},
		{"args are visible", `return args.n`, nil},
		{"script name", `return script.name`, "inline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runSrc(t, tt.src, nil)
			require.Equal(t, OutcomeOK, res.Outcome, "error: %s detail: %s", res.Error, res.Detail)
			assert.Equal(t, tt.want, res.Value)
		})
	}
}

func TestRunArgsAndInfo(t *testing.T) {
	p := Program{
		Name:   "top_stores",
		Source: `return {region = args.region, n = args.n * 2, tags = args.tags, who = script.name, v = script.version}`,
		Args:   map[string]any{"region": "EU", "n": 21, "tags": []any{"a", nil, "c"}},
		Info:   map[string]any{"version": 3, "name": "ignored"},
	}
	res := Run(context.Background(), p, small(), newFakeHost())
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{
		"region": "EU", "n": int64(42), "tags": []any{"a", nil, "c"}, "who": "top_stores", "v": int64(3),
	}, res.Value)
}

func TestRunScriptErrors(t *testing.T) {
	tests := []struct {
		name, src, contains string
	}{
		{"syntax", `return (`, "inline:1"},
		{"runtime", `local t = nil; return t.x`, "inline:1"},
		{"error call", `error("custom failure")`, "custom failure"},
		{"error with level 0", `error("plain", 0)`, "plain"},
		{"function return", `return function() end`, "cannot return this value"},
		{"nested function return", `return {f = print}`, "cannot return this value"},
		{"self reference", `local t = {} t.self = t return t`, "nested more than 32 levels"},
		{"removed load", `return load("return 1")()`, "inline:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runSrc(t, tt.src, nil)
			assert.Equal(t, OutcomeScriptError, res.Outcome)
			assert.Contains(t, res.Error, tt.contains)
			assert.False(t, strings.HasPrefix(res.Error, "error: "), "golua prefix must be stripped: %q", res.Error)
		})
	}
}

func TestRunRefusesBadInputs(t *testing.T) {
	t.Run("nil host", func(t *testing.T) {
		res := Run(context.Background(), Program{Source: "return 1"}, small(), nil)
		assert.Equal(t, OutcomeHostError, res.Outcome)
		assert.NotEmpty(t, res.Detail)
	})
	t.Run("cancelled before start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := Run(ctx, Program{Source: "return 1"}, small(), newFakeHost())
		assert.Equal(t, OutcomeCancelled, res.Outcome)
	})
	t.Run("source too large", func(t *testing.T) {
		lim := small()
		lim.MaxSourceBytes = 100
		res := Run(context.Background(), Program{Source: strings.Repeat(" ", 101)}, lim, newFakeHost())
		assert.Equal(t, OutcomeScriptError, res.Outcome)
		assert.Contains(t, res.Error, "the limit is 100")
	})
}

func TestRunOutputCapture(t *testing.T) {
	res := runSrc(t, `print("a", 1, true) log("b") return 0`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, "a\t1\ttrue\nb\n", res.Output)
	assert.False(t, res.Truncated.Output)

	res = runSrc(t, `for i = 1, 5000 do print("line", i) end`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.True(t, res.Truncated.Output)
	assert.LessOrEqual(t, len(res.Output), small().MaxOutputBytes+128)
	assert.True(t, strings.HasPrefix(res.Output, "line\t1\n"))
	assert.True(t, strings.HasSuffix(res.Output, "line\t5000\n"))
}

func TestRunReturnTruncation(t *testing.T) {
	res := runSrc(t, `local t = {} for i = 1, 20000 do t[i] = {id = i, name = "row"} end return t`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.True(t, res.Truncated.Value)
	rows, ok := res.Value.([]any)
	require.True(t, ok)
	assert.Greater(t, len(rows), 100)
	assert.Less(t, len(rows), 20000)

	res = runSrc(t, `return string.rep("x", 200000)`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.True(t, res.Truncated.Value)
	s, ok := res.Value.(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(s), small().MaxResultBytes)
	assert.Contains(t, s, "bytes elided")
}

func TestRunNonFiniteNumbers(t *testing.T) {
	res := runSrc(t, `return {1/0, 0/0, 2}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{nil, nil, int64(2)}, res.Value)
	assert.Contains(t, res.Output, "2 NaN or infinite numbers")
}

func TestRunUsageAndWall(t *testing.T) {
	res := runSrc(t, `local x = 0 for i = 1, 100000 do x = x + i end return x`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, int64(5000050000), res.Value)
	assert.Greater(t, res.Used.CPUTicks, uint64(100000))
	assert.Less(t, res.Used.WallMillis, uint64(slack(time.Second).Milliseconds()))
}

func TestRunIsolatesRuns(t *testing.T) {
	h := newFakeHost()
	res := runSrc(t, `leaked = "yes" string.upper = nil return 1`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	res = runSrc(t, `return {leaked = leaked, upper = string.upper("a")}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"upper": "A"}, res.Value)
}
