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

// Each of these scripts aborted the whole process with a fatal Go stack
// overflow on upstream golua v0.3.0: the VM ran the metamethod in a nested
// loop that no depth limit counted. The vendored golua limits nested loops
// (third_party/golua/README.md, upstream PR #132), so each is now an ordinary
// script error. A regression crashes the test binary, which is the right
// alarm.
func TestMetamethodRecursionIsACatchableError(t *testing.T) {
	scripts := map[string]string{
		"__index":          `local mt = {} mt.__index = function(t, k) return t[k] end return setmetatable({}, mt).x`,
		"__newindex":       `setmetatable({}, {__newindex = function(t, k, v) t[k] = v end}).x = 1`,
		"__eq":             `local mt = {__eq = function(a, b) return a == b end} return setmetatable({}, mt) == setmetatable({}, mt)`,
		"__lt":             `local mt = {__lt = function(a, b) return a < b end} local a = setmetatable({}, mt) return a < a`,
		"__le":             `local mt = {__le = function(a, b) return a <= b end} local a = setmetatable({}, mt) return a <= a`,
		"__add":            `local mt = {__add = function(a, b) return a + b end} return setmetatable({}, mt) + 1`,
		"__unm":            `return -setmetatable({}, {__unm = function(a) return -a end})`,
		"__len":            `return #setmetatable({}, {__len = function(a) return #a end})`,
		"__concat":         `return setmetatable({}, {__concat = function(a, b) return a .. b end}) .. "x"`,
		"__close":          `local mt = {} mt.__close = function() local x <close> = setmetatable({}, mt) end do local y <close> = setmetatable({}, mt) end`,
		"string metatable": `getmetatable("").__index = function(s, k) return s[k] end return ("x").y`,
	}
	lim := DefaultLimits() // the full budget: memory alone would not stop these in time
	for name, src := range scripts {
		t.Run(name, func(t *testing.T) {
			res := Run(context.Background(), Program{Source: src}, lim, newFakeHost())
			require.Equal(t, OutcomeScriptError, res.Outcome, "error %q detail %q", res.Error, res.Detail)
			assert.Contains(t, res.Error, "stack overflow")
		})
	}
	t.Run("caught by pcall", func(t *testing.T) {
		res := runSrc(t, `local mt = {} mt.__index = function(t, k) return t[k] end
			local ok, err = pcall(function() return setmetatable({}, mt).x end)
			return {ok = ok, overflow = tostring(err):find("stack overflow") ~= nil}`, nil)
		require.Equal(t, OutcomeOK, res.Outcome, res.Error)
		assert.Equal(t, map[string]any{"ok": false, "overflow": true}, res.Value)
	})
}

func TestMetamethodsWork(t *testing.T) {
	res := runSrc(t, `
		local Account = {} Account.__index = Account
		function Account.new(b) return setmetatable({balance = b}, Account) end
		function Account:deposit(v) self.balance = self.balance + v end
		local a = Account.new(10) a:deposit(5)

		local defaults = setmetatable({}, {__index = function(_, k) return "default " .. k end})
		local Vec = {}
		Vec.__index = Vec
		Vec.__add = function(p, q) return setmetatable({x = p.x + q.x}, Vec) end
		Vec.__eq = function(p, q) return p.x == q.x end
		Vec.__lt = function(p, q) return p.x < q.x end
		Vec.__len = function(p) return p.x end
		Vec.__concat = function(p, q) return tostring(p.x) .. "|" .. tostring(q.x) end
		Vec.__tostring = function(p) return "vec(" .. p.x .. ")" end
		local function vec(x) return setmetatable({x = x}, Vec) end
		local sum = vec(1) + vec(2)
		local log = {}
		local logged = setmetatable({}, {__newindex = function(t, k, v) log[#log + 1] = k rawset(t, k, v) end})
		logged.a = 1
		local closed = false
		do local c <close> = setmetatable({}, {__close = function() closed = true end}) end
		return {balance = a.balance, color = defaults.color, sum = sum.x, eq = vec(3) == sum,
		        lt = vec(1) < sum, len = #sum, cat = vec(1) .. vec(2), str = tostring(sum),
		        log = log[1], closed = closed, same = getmetatable(a) == Account}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"balance": int64(15), "color": "default color", "sum": int64(3), "eq": true,
		"lt": true, "len": int64(3), "cat": "1|2", "str": "vec(3)", "log": "a", "closed": true, "same": true}, res.Value)
}

func TestGcMetamethodStillRuns(t *testing.T) {
	// A finalizer that errors is reported as a warning, not a crash.
	res := runSrc(t, `setmetatable({}, {__gc = function() error("in finalizer") end}) return 1`, nil)
	assert.Contains(t, []Outcome{OutcomeOK, OutcomeScriptError}, res.Outcome, res.Error)
}
