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
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type customRow struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// TestGoValuesReachLua covers every Go type a host may hand back as tool
// data, including typed slices, structs and numbers of every width.
func TestGoValuesReachLua(t *testing.T) {
	data := map[string]any{
		"i": 1, "i8": int8(-8), "i16": int16(16), "i32": int32(-32), "i64": int64(1 << 40),
		"u": uint(7), "u8": uint8(8), "u16": uint16(16), "u32": uint32(32), "u64": uint64(64),
		"u64big": uint64(math.MaxUint64), "f32": float32(0.5), "f64": 2.25, "whole": 4.0,
		"jint": json.Number("12"), "jfloat": json.Number("1.5"), "jbad": json.Number("x"),
		"strs": []string{"a", "b"}, "rows": []map[string]any{{"k": 1}},
		"smap": map[string]string{"x": "y"}, "struct": customRow{ID: 3, Name: "c"},
		"structs": []customRow{{ID: 1}}, "skip": nil, "list": []any{nil, true},
		"chan": make(chan int),
	}
	h := newFakeHost().with("typed", func(context.Context, map[string]any) (*CallResult, error) {
		return &CallResult{OK: true, Data: data}, nil
	})
	res := runSrc(t, `
		local d = tools.call("typed").data
		return {
			i = d.i, i8 = d.i8, i16 = d.i16, i32 = d.i32, i64 = d.i64,
			u = d.u, u8 = d.u8, u16 = d.u16, u32 = d.u32, u64 = d.u64,
			u64big = math.type(d.u64big), f32 = d.f32, f64 = d.f64, whole = math.type(d.whole),
			jint = math.type(d.jint), jfloat = d.jfloat, jbad = d.jbad,
			strs = d.strs[2], rows = d.rows[1].k, smap = d.smap.x,
			struct = d.struct.name, structs = d.structs[1].id, skip = d.skip == nil,
			list1 = d.list[1] == json.null, list2 = d.list[2], chan = type(d.chan),
		}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{
		"i": int64(1), "i8": int64(-8), "i16": int64(16), "i32": int64(-32), "i64": int64(1 << 40),
		"u": int64(7), "u8": int64(8), "u16": int64(16), "u32": int64(32), "u64": int64(64),
		"u64big": "float", "f32": 0.5, "f64": 2.25, "whole": "integer",
		"jint": "integer", "jfloat": 1.5, "jbad": "x",
		"strs": "b", "rows": int64(1), "smap": "y", "struct": "c", "structs": int64(1),
		"skip": true, "list1": true, "list2": true, "chan": "string",
	}, res.Value)
}

// TestLuaValuesReachGo covers table key and value kinds on the way out.
func TestLuaValuesReachGo(t *testing.T) {
	res := runSrc(t, `return {[1.5] = "f", [true] = "b", [-1] = "neg", [0] = "zero", s = "x"}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"1.5": "f", "true": "b", "-1": "neg", "0": "zero", "s": "x"}, res.Value)

	res = runSrc(t, `return {[{}] = 1}`, nil)
	assert.Equal(t, OutcomeScriptError, res.Outcome)
	assert.Contains(t, res.Error, "as a JSON object key")

	h := newFakeHost().with("echo", echo)
	res = runSrc(t, `return tools.call("echo", {k = {[2.5] = 1}}).data.k["2.5"]`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, int64(1), res.Value)
}

func TestStrictConversionBudget(t *testing.T) {
	c := newGoConv(10, false, nil)
	_, _, err := c.str("this string is too long")
	assert.ErrorContains(t, err, "larger than 10 bytes")

	c = newGoConv(3, false, nil)
	_, _, err = c.scalar(true, 5)
	assert.ErrorContains(t, err, "larger than 3 bytes")

	c = newGoConv(3, true, nil)
	_, keep, err := c.scalar(true, 5)
	assert.NoError(t, err)
	assert.False(t, keep)

	c = newGoConv(100, true, nil)
	x, keep, err := c.str(string(make([]byte, 50)))
	assert.NoError(t, err)
	assert.True(t, keep)
	assert.Len(t, x, 50)
}

func TestTruncatedObjectsKeepSortedKeys(t *testing.T) {
	lim := small()
	lim.MaxResultBytes = 120
	res := Run(context.Background(), Program{Source: `
		local t = {}
		for i = 1, 50 do t[string.format("k%02d", i)] = i end
		return t`}, lim, newFakeHost())
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.True(t, res.Truncated.Value)
	obj := res.Value.(map[string]any)
	require.NotEmpty(t, obj)
	assert.Contains(t, obj, "k01", "truncation keeps keys in sorted order")
	assert.NotContains(t, obj, "k50")
}

func TestNormalizeJSONFallbacks(t *testing.T) {
	assert.Equal(t, map[string]any{"id": json.Number("1"), "name": "a"}, normalizeJSON(customRow{ID: 1, Name: "a"}))
	assert.IsType(t, "", normalizeJSON(make(chan int)))
	size, _ := jsonSize(make(chan int))
	assert.Equal(t, -1, size)
	size, raw := jsonSize(map[string]int{"a": 1})
	assert.Equal(t, 7, size)
	assert.Equal(t, `{"a":1}`, string(raw))
}

func TestPrepareDataEdgeCases(t *testing.T) {
	s := &run{lim: small()}
	data, text, size, cut := s.prepareData(nil)
	assert.Nil(t, data)
	assert.Nil(t, text)
	assert.Zero(t, size)
	assert.False(t, cut)

	data, text, _, cut = s.prepareData(make(chan int))
	assert.IsType(t, "", data, "unencodable data falls back to its text")
	assert.NotNil(t, text)
	assert.False(t, cut)

	data, _, _, _ = s.prepareData(map[string]any{"ok": 1, "bad": []any{make(chan int)}})
	m := data.(map[string]any)
	assert.Equal(t, 1, m["ok"], "good fields survive a bad sibling")
	assert.IsType(t, "", m["bad"].([]any)[0])

	for _, notJSON := range []string{"", "  ", "plain", `{"a":1} trailing`, `{bad`, `"just a string"`} {
		data, _, _, _ = s.prepareData(notJSON)
		assert.Equal(t, notJSON, data, "%q stays text", notJSON)
	}
}

func TestErrorPartsWithoutError(t *testing.T) {
	code, msg := errorParts(&CallResult{OK: false})
	assert.Equal(t, CodeExecutionFailed, code)
	assert.NotEmpty(t, msg)
	h := newFakeHost().with("silent", func(context.Context, map[string]any) (*CallResult, error) {
		return &CallResult{OK: false}, nil
	})
	res := runSrc(t, `local r = tools.call("silent") return {r.error.code, r.error.retryable == nil}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{CodeExecutionFailed, true}, res.Value)
}
