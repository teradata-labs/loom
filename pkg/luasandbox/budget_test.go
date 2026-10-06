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
	"runtime/metrics"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cases are the probes behind the design's safety claims
// (docs/architecture/lua-script-engine.md). Each must end the run with the
// named budget, inside the time bound, without the process allocating
// unbounded memory.
func TestBudgetsStopHostileScripts(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		limit string
		edit  func(*Limits)
	}{
		{"infinite loop hits wall", `while true do end`, LimitWall, func(l *Limits) { l.CPUTicks = 1 << 40; l.Wall = 200 * time.Millisecond }},
		{"infinite loop hits cpu", `while true do end`, LimitCPU, func(l *Limits) { l.CPUTicks = 5_000_000 }},
		{"table growth", `local t = {} for i = 1, 1e8 do t[i] = i end`, LimitMemory, nil},
		{"string.rep", `local s = string.rep("x", 2e8) return #s`, LimitMemory, nil},
		{"string doubling", `local s = "x" for i = 1, 40 do s = s .. s end return #s`, LimitMemory, nil},
		{"table.concat", `local s = string.rep("x", 1e6) local t = {} for i = 1, 100 do t[i] = s end return #table.concat(t)`, LimitMemory, nil},
		{"gsub expansion", `local s = string.rep("x", 1e6) return #s:gsub(".", "%0%0%0%0%0%0%0%0")`, LimitMemory, func(l *Limits) { l.MemoryBytes = 4 << 20 }},
		{"string.format", `local s = string.rep("x", 1e7) return #string.format("%s%s%s%s", s, s, s, s)`, LimitMemory, nil},
		{"string.pack", `local s = string.rep("x", 1e7) return #string.pack("s4s4s4s4", s, s, s, s)`, LimitMemory, nil},
		{"tostring metamethod", `return #tostring(setmetatable({}, {__tostring = function() return string.rep("x", 1e8) end}))`, LimitMemory, nil},
		{"huge error value", `error(string.rep("x", 2e8))`, LimitMemory, nil},
		{"deep lua recursion", `local function f(n) return f(n + 1) + 1 end return f(0)`, LimitMemory, nil},
		{"memory bomb inside pcall", `pcall(function() local t = {} for i = 1, 1e8 do t[i] = i end end) return "survived"`, LimitMemory, nil},
		{"loop inside pcall", `pcall(function() while true do end end) return "survived"`, LimitCPU, func(l *Limits) { l.CPUTicks = 5_000_000 }},
		{"finalizer loop", `G = setmetatable({}, {__gc = function() while true do end end}) return 1`, LimitWall, func(l *Limits) { l.Wall = 300 * time.Millisecond; l.CPUTicks = 1 << 40 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := small()
			if tt.edit != nil {
				tt.edit(&lim)
			}
			if raceEnabled && tt.limit != LimitWall {
				// The race detector slows the interpreter about tenfold; keep
				// the wall budget out of the way of the limit under test.
				lim.Wall *= 10
			}
			stop := sampleHeapPeak()
			start := time.Now()
			res := Run(context.Background(), Program{Source: tt.src}, lim, newFakeHost())
			elapsed := time.Since(start)
			base, peak := stop()

			require.Equal(t, OutcomeBudgetExceeded, res.Outcome, "error: %s", res.Error)
			assert.Equal(t, tt.limit, res.Limit, "error: %s", res.Error)
			assert.Less(t, elapsed, slack(lim.Wall), "the run must end within its wall budget")
			// An allocation path the budget does not see would show up as a
			// heap peak far above the budget: every bomb here asks for 100 MiB
			// to 1 GiB. The heap metric also counts garbage the collector has
			// not reached yet (gsub churns through small buffers), so the
			// bound is 4x the budget, for growth by doubling, plus a fixed
			// allowance for collector lag.
			const collectorLag = 64 << 20
			grew := peak - min(base, peak)
			assert.Less(t, grew, 4*lim.MemoryBytes+collectorLag, "heap grew %d MiB against a %d MiB budget", grew>>20, lim.MemoryBytes>>20)
		})
	}
}

// sampleHeapPeak samples the live heap every millisecond until the returned
// stop function is called, and reports the starting and peak sizes.
func sampleHeapPeak() (stop func() (base, peak uint64)) {
	read := func() uint64 {
		s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		metrics.Read(s)
		return s[0].Value.Uint64()
	}
	base := read()
	var (
		mu   sync.Mutex
		peak = base
		done = make(chan struct{})
		wg   sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				v := read()
				mu.Lock()
				peak = max(peak, v)
				mu.Unlock()
			}
		}
	}()
	return func() (uint64, uint64) {
		close(done)
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		return base, max(peak, read())
	}
}

func TestBudgetErrorsThatAreNotKills(t *testing.T) {
	// golua reports these as ordinary errors; they must stay catchable and
	// must not allocate the requested size.
	tests := []struct{ name, src, contains string }{
		{"unpack too many", `return select('#', table.unpack({}, 1, 1e8))`, "too many values"},
		{"format width", `return string.format("%" .. string.rep("9", 20) .. "d", 1)`, "too long"},
		{"gsub recursion", `local function f(s) return (s:gsub(".", f)) end return f("ab")`, "stack overflow"},
		{"sort recursion", `local function cmp(a, b) table.sort({2, 1}, cmp) return a < b end table.sort({3, 2, 1}, cmp)`, "stack overflow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runSrc(t, tt.src, nil)
			require.NotEqual(t, OutcomeOK, res.Outcome)
			assert.Contains(t, res.Error+res.Output, tt.contains)
		})
	}
	t.Run("pcall recursion is caught at the depth guard", func(t *testing.T) {
		// golua caps Go-function nesting at 1000, so pcall recursion cannot
		// overflow the Go stack; each level catches the error instead.
		res := runSrc(t, `local function f() return select(2, pcall(f)) end return f()`, nil)
		require.Equal(t, OutcomeOK, res.Outcome, res.Error)
		assert.Contains(t, res.Value, "stack overflow")
	})
	t.Run("caught by pcall", func(t *testing.T) {
		res := runSrc(t, `local ok, e = pcall(table.unpack, {}, 1, 1e8) return {ok = ok, e = tostring(e)}`, nil)
		require.Equal(t, OutcomeOK, res.Outcome, res.Error)
		assert.Equal(t, false, res.Value.(map[string]any)["ok"])
	})
}

func TestToolCallBudget(t *testing.T) {
	h := newFakeHost().with("noop", text("ok"))
	lim := small()
	lim.MaxToolCalls = 3
	res := Run(context.Background(), Program{Source: `
		for i = 1, 10 do
			pcall(tools.call, "noop", {})  -- swallowing errors must not help
		end`}, lim, h)
	require.Equal(t, OutcomeBudgetExceeded, res.Outcome, res.Error)
	assert.Equal(t, LimitToolCalls, res.Limit)
	assert.Len(t, h.callNames(), 3)
	assert.Len(t, res.Calls, 3)
}

func TestGoluaLimitMessagesArePinned(t *testing.T) {
	// limitFromMessage depends on these phrasings. An interpreter upgrade
	// that changes them must fail here, not misclassify in production.
	lim := small()
	lim.CPUTicks = 1_000_000
	res := Run(context.Background(), Program{Source: `while true do end`}, lim, newFakeHost())
	assert.Contains(t, res.Error, "CPU limit of")
	res = Run(context.Background(), Program{Source: `return string.rep("x", 1e9)`}, small(), newFakeHost())
	assert.Contains(t, res.Error, "memory limit of")
	lim = small()
	lim.Wall, lim.CPUTicks = 100*time.Millisecond, 1<<40
	res = Run(context.Background(), Program{Source: `while true do end`}, lim, newFakeHost())
	assert.Contains(t, res.Error, "time limit of")
}
