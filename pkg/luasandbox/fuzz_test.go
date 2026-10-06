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
	"hash/fnv"
	"testing"
	"time"
)

// chaosHost fails, errors or panics deterministically, driven by the call
// arguments, so the fuzzer explores host failure paths too.
type chaosHost struct{ *fakeHost }

func (h chaosHost) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	f := fnv.New32a()
	_, _ = f.Write([]byte(name))
	switch f.Sum32() % 5 {
	case 0:
		panic("chaos")
	case 1:
		return nil, errBoom
	case 2:
		return &CallResult{OK: false, Error: &CallError{Code: "E", Message: "chaos"}}, nil
	case 3:
		return &CallResult{OK: true, Data: `{"a":[1,2,{"b":null}]}`}, nil
	}
	return &CallResult{OK: true, Data: args}, nil
}

var validOutcomes = map[Outcome]bool{
	OutcomeOK: true, OutcomeScriptError: true, OutcomeBudgetExceeded: true,
	OutcomeCancelled: true, OutcomeHostError: true, OutcomeEngineError: true,
}

// FuzzRun feeds arbitrary source to the engine. Whatever the input, Run must
// not panic, must return a known outcome, and must finish within its wall
// budget plus a small margin.
//
//	go test -run='^$' -fuzz='^FuzzRun$' -fuzztime=60s ./pkg/luasandbox/
func FuzzRun(f *testing.F) {
	seeds := []string{
		`return 1`,
		`while true do end`,
		`local t = {} for i = 1, 1e7 do t[i] = i end`,
		`return string.rep("x", 1e9)`,
		`local s = "x" for i = 1, 40 do s = s .. s end`,
		`pcall(function() while true do end end)`,
		`return tools.call("a", {x = {1, 2, json.null}})`,
		`return tools.must("bb")`,
		`return json.decode(json.encode({a = {b = {c = {}}}}))`,
		`return turn.result(1)`,
		`local function f(n) return pcall(f, n + 1) end return f(1)`,
		`setmetatable({}, {__gc = function() error("x") end})`,
		`return ` + "((((((((((((((((((((1))))))))))))))))))))",
		`return tools.list(), tools.schema("a")`,
		`time.sleep(1) return time.now()`,
		`return xpcall(error, function(e) return e end, "z")`,
		// Metamethod recursion overflowed the Go stack before setmetatable
		// was restricted; keep the class in the corpus.
		`local t = setmetatable({}, {}) getmetatable(t).__index = function(t, k) return t[k] end return t.x`,
		`local mt = {} local t = setmetatable({}, mt) mt.__index = function(t, k) return t[k] end return t.x`,
		`local mt = {__lt = function(a, b) return a < b end} local a = setmetatable({}, mt) return a < a`,
		`local mt = {} mt.__close = function() local x <close> = setmetatable({}, mt) end do local y <close> = setmetatable({}, mt) end`,
		`getmetatable("").__index = function(s, k) return s[k] end return ("x").y`,
		`return string.format("%p")`,
		`local t = {} for i = 1, 1e6 do t["k" .. i] = i end return json.encode(t)`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	lim := Limits{
		Wall:               300 * time.Millisecond,
		CPUTicks:           2_000_000,
		MemoryBytes:        8 << 20,
		MaxToolCalls:       5,
		ToolCallTimeout:    50 * time.Millisecond,
		MaxCallResultBytes: 16 << 10,
		MaxOutputBytes:     1 << 10,
		MaxResultBytes:     16 << 10,
		MaxSourceBytes:     32 << 10,
		MaxSleep:           10 * time.Millisecond,
	}
	f.Fuzz(func(t *testing.T, src string) {
		start := time.Now()
		res := Run(context.Background(), Program{Source: src}, lim, chaosHost{newFakeHost()})
		if res == nil {
			t.Fatal("Run returned nil")
			return
		}
		if !validOutcomes[res.Outcome] {
			t.Fatalf("unknown outcome %q", res.Outcome)
		}
		if elapsed := time.Since(start); elapsed > lim.Wall+2*time.Second {
			t.Fatalf("run took %v against a %v wall budget", elapsed, lim.Wall)
		}
	})
}
