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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each of these scripts, with golua's own setmetatable, overflowed the Go
// stack (1 GB, fatal to the whole process) within a second. With the
// restriction they must end as an ordinary script error. If the restriction
// regresses, this test crashes the test binary, which is the right alarm.
func TestMetamethodRecursionCannotCrashTheProcess(t *testing.T) {
	scripts := map[string]string{
		"__index function":        `local t = setmetatable({}, {}) getmetatable(t).__index = function(t, k) return t[k] end return t.x`,
		"__index set late":        `local mt = {} local t = setmetatable({}, mt) mt.__index = function(t, k) return t[k] end return t.x`,
		"__newindex function":     `setmetatable({}, {__newindex = function(t, k, v) t[k] = v end}).x = 1`,
		"__eq":                    `local mt = {__eq = function(a, b) return a == b end} return setmetatable({}, mt) == setmetatable({}, mt)`,
		"__lt":                    `local mt = {__lt = function(a, b) return a < b end} local a = setmetatable({}, mt) return a < a`,
		"__le":                    `local mt = {__le = function(a, b) return a <= b end} local a = setmetatable({}, mt) return a <= a`,
		"__add":                   `local mt = {__add = function(a, b) return a + b end} return setmetatable({}, mt) + 1`,
		"__unm":                   `return -setmetatable({}, {__unm = function(a) return -a end})`,
		"__len":                   `return #setmetatable({}, {__len = function(a) return #a end})`,
		"__concat":                `return setmetatable({}, {__concat = function(a, b) return a .. b end}) .. "x"`,
		"__close":                 `local mt = {} mt.__close = function() local x <close> = setmetatable({}, mt) end do local y <close> = setmetatable({}, mt) end`,
		"callable table as __add": `return setmetatable({}, {__add = setmetatable({}, {__call = function() return 1 end})}) + 1`,
		"string metatable":        `getmetatable("").__index = function(s, k) return s[k] end return ("x").y`,
	}
	lim := DefaultLimits() // the full 256 MiB budget: memory alone would not stop these in time
	for name, src := range scripts {
		t.Run(name, func(t *testing.T) {
			res := Run(context.Background(), Program{Source: src}, lim, newFakeHost())
			assert.NotEqual(t, OutcomeEngineError, res.Outcome, res.Detail)
			assert.Contains(t, []Outcome{OutcomeScriptError, OutcomeOK}, res.Outcome, res.Error)
		})
	}
}

func TestMetamethodRules(t *testing.T) {
	forbidden := []string{"__add", "__sub", "__mul", "__div", "__mod", "__pow", "__unm", "__idiv",
		"__band", "__bor", "__bxor", "__shl", "__shr", "__bnot", "__concat", "__len", "__eq", "__lt", "__le", "__close"}
	for _, ev := range forbidden {
		res := runSrc(t, `setmetatable({}, {`+ev+` = function() end})`, nil)
		assert.Equal(t, OutcomeScriptError, res.Outcome, ev)
		assert.Contains(t, res.Error, ev+" is not available in scripts", ev)
	}
	for _, ev := range []string{"__index", "__newindex"} {
		res := runSrc(t, `setmetatable({}, {`+ev+` = function() end})`, nil)
		assert.Equal(t, OutcomeScriptError, res.Outcome, ev)
		assert.Contains(t, res.Error, ev+" must be a table", ev)
	}
}

func TestAllowedMetatablePatterns(t *testing.T) {
	res := runSrc(t, `
		-- Prototype objects: __index as a table.
		local Account = {} Account.__index = Account
		function Account.new(b) return setmetatable({balance = b}, Account) end
		function Account:deposit(v) self.balance = self.balance + v end
		local a = Account.new(10) a:deposit(5)

		-- Defaults through a table chain.
		local defaults = setmetatable({}, {__index = {color = "blue"}})

		-- __newindex as a table redirects writes.
		local store = {}
		local proxy = setmetatable({}, {__newindex = store})
		proxy.k = "v"

		-- Metamethods reached through library functions stay available.
		local named = setmetatable({}, {__tostring = function() return "named" end})
		local callable = setmetatable({}, {__call = function(self, x) return x * 2 end})
		local counted = setmetatable({}, {__pairs = function(t) return next, {1, 2}, nil end})
		local n = 0 for _ in pairs(counted) do n = n + 1 end

		return {balance = a.balance, color = defaults.color, stored = store.k, raw = rawget(proxy, "k"),
		        named = tostring(named), doubled = callable(21), pairs = n}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"balance": int64(15), "color": "blue", "stored": "v",
		"named": "named", "doubled": int64(42), "pairs": int64(2)}, res.Value)
}

func TestMetatablesAreCopied(t *testing.T) {
	res := runSrc(t, `
		local mt = {__index = {a = 1}}
		local t = setmetatable({}, mt)
		mt.__index = {a = 2}           -- changing the original has no effect
		local seen = getmetatable(t)
		seen.__index = {a = 3}         -- nor does changing the copy getmetatable returns
		local same = getmetatable(t) == mt
		local str = getmetatable("")
		str.__index = nil              -- strings keep their methods
		return {a = t.a, same = same, upper = ("x"):upper()}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"a": int64(1), "same": false, "upper": "X"}, res.Value)

	res = runSrc(t, `local t = setmetatable({}, {__metatable = "locked"}) return {getmetatable(t), pcall(setmetatable, t, {})}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	v := res.Value.([]any)
	assert.Equal(t, "locked", v[0])
	assert.Equal(t, false, v[1])
	assert.Contains(t, v[2], "protected metatable")

	for _, bad := range []string{`setmetatable(1, {})`, `setmetatable({}, 1)`, `setmetatable({})`} {
		assert.Equal(t, OutcomeScriptError, runSrc(t, bad, nil).Outcome, bad)
	}
	res = runSrc(t, `local t = setmetatable({}, {__index = {a = 1}}) setmetatable(t, nil) return {t.a == nil, getmetatable(t) == nil, getmetatable(1) == nil}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{true, true, true}, res.Value)
}

func TestGcMetamethodStillRuns(t *testing.T) {
	// __gc stays allowed; a finalizer that errors is reported as a warning.
	res := runSrc(t, `setmetatable({}, {__gc = function() error("in finalizer") end}) return 1`, nil)
	assert.Contains(t, []Outcome{OutcomeOK, OutcomeScriptError}, res.Outcome, res.Error)
}
