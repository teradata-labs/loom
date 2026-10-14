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
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDangerousGlobalsAreAbsent(t *testing.T) {
	for _, name := range []string{
		"require", "package", "load", "loadstring", "dofile", "loadfile",
		"collectgarbage", "warn", "coroutine", "io", "os", "debug", "golib", "runtime",
	} {
		t.Run(name, func(t *testing.T) {
			res := runSrc(t, "return "+name+" == nil", nil)
			require.Equal(t, OutcomeOK, res.Outcome, res.Error)
			assert.Equal(t, true, res.Value)
		})
	}
	res := runSrc(t, `return string.dump == nil`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, true, res.Value)
}

func TestPureLibrariesWorkUnderLimits(t *testing.T) {
	// golua refuses Go functions that do not declare compliance with the
	// active limits; every library function a script may use must work
	// with all three limits on.
	res := runSrc(t, `
		local out = {}
		out[#out+1] = string.format("%05.1f|%s|%d", 3.14159, "x", 42)
		out[#out+1] = ("Hello"):upper() .. ("WORLD"):lower() .. ("abc"):rep(2, "-")
		out[#out+1] = select(2, ("a,b,c"):gsub(",", ";"))
		out[#out+1] = ("key=value"):match("(%w+)=(%w+)")
		for w in ("one two"):gmatch("%a+") do out[#out+1] = w end
		out[#out+1] = string.byte("A") .. string.char(66) .. #string.pack("i4", 7)
		local t = {5, 3, 9, 1}
		table.sort(t) table.insert(t, 10) table.remove(t, 1)
		out[#out+1] = table.concat(t, ",") .. "|" .. select("#", table.unpack(t))
		out[#out+1] = math.floor(math.max(1.5, 2.5)) + math.abs(-3) + math.fmod(7, 4)
		out[#out+1] = math.type(1) .. math.type(1.0) .. tostring(math.tointeger(4.0))
		math.randomseed(1) out[#out+1] = math.random(1, 1)
		out[#out+1] = utf8.len("héllo") .. utf8.char(72, 105)
		out[#out+1] = tostring(rawequal(t, t)) .. rawlen(t) .. type(next({}))
		local mt = setmetatable({}, {__index = function(_, k) return k .. "!" end})
		out[#out+1] = mt.hey .. tostring(getmetatable(mt) ~= nil)
		out[#out+1] = tonumber("0x10") + tonumber("7")
		return table.concat(out, " ")`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, "003.1|x|42 HELLOworldabc-abc 2 key one two 65B4 3,5,9,10|4 8 integerfloat4 1 5Hi true4nil hey!true 23", res.Value)
}

func TestJSON(t *testing.T) {
	tests := []struct {
		name, src string
		want      any
	}{
		{"encode object", `return json.encode({b = 1, a = {true, json.null, "x"}})`, `{"a":[true,null,"x"],"b":1}`},
		{"encode no html escaping", `return json.encode({s = "<a&b>"})`, `{"s":"<a&b>"}`},
		{"encode empty table", `return json.encode({})`, `{}`},
		{"encode float", `return json.encode({f = 1.5, i = 2})`, `{"f":1.5,"i":2}`},
		{"decode object drops nulls", `local v = json.decode('{"a":1,"b":null}') return {a = v.a, has_b = v.b ~= nil}`, map[string]any{"a": int64(1), "has_b": false}},
		{"decode array keeps null positions", `local v = json.decode('[1,null,3]') return {#v, v[2] == json.null, v[3]}`, []any{int64(3), true, int64(3)}},
		{"decode integers stay integers", `return math.type(json.decode('{"n":9007199254740993}').n)`, "integer"},
		{"decode float", `return json.decode('2.5')`, 2.5},
		{"decode top-level null", `return json.decode('null') == nil`, true},
		{"round trip", `local s = '{"k":[1,2,{"z":"w"}]}' return json.encode(json.decode(s)) == s`, true},
		{"null prints", `return tostring(json.null)`, "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runSrc(t, tt.src, nil)
			require.Equal(t, OutcomeOK, res.Outcome, res.Error)
			assert.Equal(t, tt.want, res.Value)
		})
	}
	errs := []struct{ name, src, contains string }{
		{"invalid", `json.decode("{nope")`, "invalid JSON"},
		{"trailing data", `json.decode('{} {}')`, "unexpected data"},
		{"not a string", `json.decode({})`, "expected a string"},
		{"function", `json.encode({f = print})`, "cannot convert a function"},
		{"cycle", `local t = {} t.t = t json.encode(t)`, "nested more than"},
		{"too large", `json.encode({s = string.rep("x", 100000)})`, "larger than"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			res := runSrc(t, tt.src, nil)
			assert.Equal(t, OutcomeScriptError, res.Outcome)
			assert.Contains(t, res.Error, tt.contains)
		})
	}
	t.Run("decode is charged to memory", func(t *testing.T) {
		lim := small()
		lim.MemoryBytes = 4 << 20
		res := Run(context.Background(), Program{Source: `
			local parts = {} for i = 1, 20000 do parts[i] = '"' .. string.rep("v", 50) .. '"' end
			local s = "[" .. table.concat(parts, ",") .. "]"
			local keep = {} for i = 1, 10 do keep[i] = json.decode(s) end`}, lim, newFakeHost())
		assert.Equal(t, OutcomeBudgetExceeded, res.Outcome, res.Error)
		assert.Equal(t, LimitMemory, res.Limit)
	})
}

func TestTime(t *testing.T) {
	res := runSrc(t, `return {now = time.now(), unix = time.unix()}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	v := res.Value.(map[string]any)
	assert.Regexp(t, regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`), v["now"])
	assert.InDelta(t, float64(time.Now().Unix()), v["unix"], 5)

	start := time.Now()
	res = runSrc(t, `time.sleep(30) return 1`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)

	lim := small()
	lim.MaxSleep = 20 * time.Millisecond
	start = time.Now()
	res = Run(context.Background(), Program{Source: `time.sleep(60000) return 1`}, lim, newFakeHost())
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Less(t, time.Since(start), slack(200*time.Millisecond), "sleep is capped at MaxSleep")

	for _, bad := range []string{`time.sleep(-1)`, `time.sleep("x")`, `time.sleep(0/0)`} {
		res = runSrc(t, bad, nil)
		assert.Equal(t, OutcomeScriptError, res.Outcome, bad)
	}
}
