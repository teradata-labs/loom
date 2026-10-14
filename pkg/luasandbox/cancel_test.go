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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelOnCall returns a tool that cancels the run's caller context on its
// nth invocation, standing in for a user pressing stop mid-script.
func cancelOnCall(n int, cancel context.CancelFunc) toolFunc {
	count := 0
	return func(context.Context, map[string]any) (*CallResult, error) {
		count++
		if count == n {
			cancel()
		}
		return &CallResult{OK: true, Data: "ok"}, nil
	}
}

func TestCancelPassesThroughEveryPcall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newFakeHost().with("tool", cancelOnCall(3, cancel))
	lim := small()
	lim.Wall, lim.CPUTicks = 10*time.Second, 1<<40 // only cancellation can end this
	src := `
		local function spin() local x = 0 while true do x = x + 1 end end
		local ok1 = pcall(function()
			local ok2 = xpcall(function()
				local ok3 = pcall(function() tools.call("tool") tools.call("tool") tools.call("tool") spin() end)
				print("level 3 returned", ok3) spin()
			end, function(e) return e end)
			print("level 2 returned", ok2) spin()
		end)
		print("level 1 returned", ok1) spin()`
	start := time.Now()
	res := Run(ctx, Program{Source: src}, lim, h)
	require.Equal(t, OutcomeCancelled, res.Outcome, "error: %s output: %s", res.Error, res.Output)
	assert.Empty(t, res.Output, "no pcall level may observe the cancellation")
	assert.Less(t, time.Since(start), slack(200*time.Millisecond))
}

func TestCancelDuringToolCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := newFakeHost().with("slow", blocking)
	lim := small()
	lim.ToolCallTimeout = 10 * time.Second
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	res := Run(ctx, Program{Source: `local r = tools.call("slow") return r.ok`}, lim, h)
	assert.Equal(t, OutcomeCancelled, res.Outcome, res.Error)
	assert.Less(t, time.Since(start), slack(300*time.Millisecond))
}

func TestCancelDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lim := small()
	lim.MaxSleep = 10 * time.Second
	lim.Wall = 20 * time.Second
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	res := Run(ctx, Program{Source: `pcall(time.sleep, 9000) return "woke"`}, lim, newFakeHost())
	assert.Equal(t, OutcomeCancelled, res.Outcome, res.Error)
	assert.Less(t, time.Since(start), slack(300*time.Millisecond))
}

func TestWallBudgetDuringToolCall(t *testing.T) {
	h := newFakeHost().with("slow", blocking)
	lim := small()
	lim.Wall, lim.ToolCallTimeout = 200*time.Millisecond, 10*time.Second
	res := Run(context.Background(), Program{Source: `pcall(tools.call, "slow") return 1`}, lim, h)
	assert.Equal(t, OutcomeBudgetExceeded, res.Outcome, res.Error)
	assert.Equal(t, LimitWall, res.Limit)
}

func TestCallerDeadlineIsCancellation(t *testing.T) {
	// A caller deadline sooner than the wall budget is the caller's
	// decision, so it reports as cancelled rather than as a budget.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	lim := small()
	lim.Wall, lim.CPUTicks = 10*time.Second, 1<<40
	res := Run(ctx, Program{Source: `while true do end`}, lim, newFakeHost())
	assert.Equal(t, OutcomeCancelled, res.Outcome, res.Error)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel2()
	h := newFakeHost().with("slow", blocking)
	res = Run(ctx2, Program{Source: `tools.call("slow")`}, lim, h)
	assert.Equal(t, OutcomeCancelled, res.Outcome, res.Error)
}

func TestPureComputeCancellationIsImmediate(t *testing.T) {
	// A script that never calls the host is stopped by the interpreter's
	// interrupt as soon as the caller cancels, not by its CPU budget.
	ctx, cancel := context.WithCancel(context.Background())
	lim := small()
	lim.CPUTicks, lim.Wall = 1<<40, 20*time.Second
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	res := Run(ctx, Program{Source: `
		local function spin() while true do end end
		pcall(function() pcall(spin) print("level 2 returned") spin() end)
		print("level 1 returned")
		spin()`}, lim, newFakeHost())
	assert.Equal(t, OutcomeCancelled, res.Outcome, res.Error)
	assert.Empty(t, res.Output, "no enclosing level runs after the interrupt")
	assert.Less(t, time.Since(start), slack(200*time.Millisecond))
}

func TestPureComputeWallBudgetIsExact(t *testing.T) {
	lim := small()
	lim.CPUTicks, lim.Wall = 1<<40, 150*time.Millisecond
	start := time.Now()
	res := Run(context.Background(), Program{Source: `while true do end`}, lim, newFakeHost())
	assert.Equal(t, OutcomeBudgetExceeded, res.Outcome, res.Error)
	assert.Equal(t, LimitWall, res.Limit)
	assert.Less(t, time.Since(start), slack(400*time.Millisecond))
}

func TestPcallStillCatchesOrdinaryErrors(t *testing.T) {
	h := newFakeHost().with("bad", failing("E_BAD", "nope"))
	res := runSrc(t, `
		local ok1, e1 = pcall(error, "first")
		local ok2, e2 = pcall(tools.must, "bad", {})
		local ok3, e3 = xpcall(function() error({code = 7}) end, function(e) return "handled " .. e.code end)
		local ok4, a, b = pcall(function(x, y) return x + y, x * y end, 3, 4)
		return {ok1 = ok1, e1 = e1, ok2 = ok2, e2 = e2, ok3 = ok3, e3 = e3, ok4 = ok4, a = a, b = b}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	v := res.Value.(map[string]any)
	assert.Equal(t, false, v["ok1"])
	assert.Contains(t, v["e1"], "first") // golua prefixes a position; reference Lua does not
	assert.Equal(t, false, v["ok2"])
	assert.Contains(t, v["e2"], "bad: E_BAD: nope")
	assert.Equal(t, false, v["ok3"])
	assert.Equal(t, "handled 7", v["e3"])
	assert.Equal(t, true, v["ok4"])
	assert.Equal(t, int64(7), v["a"])
	assert.Equal(t, int64(12), v["b"])
}
