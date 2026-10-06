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

func TestInterpreterPanicsAreEngineErrors(t *testing.T) {
	// golua's string.format("%p") indexes past its arguments and panics.
	// That is the interpreter's fault, not the host's, and it ends the run
	// whether or not the script wraps it in pcall.
	for _, src := range []string{`return string.format("%p")`, `return pcall(string.format, "%p")`} {
		res := runSrc(t, src, nil)
		assert.Equal(t, OutcomeEngineError, res.Outcome, src)
		assert.NotEmpty(t, res.Detail)
		assert.NotContains(t, res.Error, "index out of range", "Go internals stay out of model-facing text")
	}
}

func TestErrorTextIsBounded(t *testing.T) {
	lim := small()
	lim.MemoryBytes = 128 << 20
	res := Run(context.Background(), Program{Source: `error(string.rep("x", 5e6))`}, lim, newFakeHost())
	require.Equal(t, OutcomeScriptError, res.Outcome)
	assert.LessOrEqual(t, len(res.Error), maxErrorBytes+64)
	assert.Contains(t, res.Error, "bytes elided")

	res = runSrc(t, `error({})`, nil)
	assert.NotContains(t, res.Error, "0x", "heap addresses are removed")
	assert.Contains(t, res.Error, "table")
	assert.Equal(t, "short", boundError("short"))
}

func TestConversionIsBoundedBeforeAllocating(t *testing.T) {
	// Before the fix one json.encode of a 2.5M-entry table built ~170 MiB of
	// uncharged Go temporaries and spent seconds of Go CPU charged as ~400
	// ticks. Now it stops at the first entry that cannot fit.
	lim := small()
	lim.MemoryBytes = 256 << 20
	lim.Wall = 30 * time.Second
	stop := sampleHeapPeak()
	start := time.Now()
	res := Run(context.Background(), Program{Source: `
		local t = {} for i = 1, 2.5e6 do t["k" .. i] = i end
		local ok, err = pcall(json.encode, t)
		local ok2, err2 = pcall(tools.call, "x", t)
		return {ok = ok, err = err, ok2 = ok2, err2 = err2}`}, lim, newFakeHost())
	base, peak := stop()
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	v := res.Value.(map[string]any)
	assert.Equal(t, false, v["ok"])
	assert.Contains(t, v["err"], "larger than")
	assert.Equal(t, false, v["ok2"])
	assert.Contains(t, v["err2"], "larger than")
	t.Logf("elapsed %v, heap grew %d MiB, cpu %d", time.Since(start), (peak-min(base, peak))>>20, res.Used.CPUTicks)

	// Return values: a huge table is truncated to what fits, quickly.
	start = time.Now()
	res = Run(context.Background(), Program{Source: `local t = {} for i = 1, 1e6 do t[i] = i end return t`}, lim, newFakeHost())
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.True(t, res.Truncated.Value)
	assert.Less(t, len(res.Value.([]any)), 1_000_000)
	assert.Less(t, time.Since(start), slack(5*time.Second))
}

func TestSourceGuardBoundsTotalNesting(t *testing.T) {
	// 150 open brackets, each holding a 100-token chain: under the old
	// per-level limits this passed while asking the parser to recurse
	// through ~15000 levels.
	var b strings.Builder
	b.WriteString("return ")
	for range 150 {
		b.WriteString(strings.Repeat("1+", 50))
		b.WriteString("(")
	}
	b.WriteString("1")
	b.WriteString(strings.Repeat(")", 150))
	err := checkSourceShape("inline", b.String())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nesting too deep")
}

func TestSourceGuardAllowsLongFlatScripts(t *testing.T) {
	// Thousands of sequential blocks and function definitions are flat,
	// whatever their count (these were false positives).
	var b strings.Builder
	for i := range 3000 {
		b.WriteString("if x then y = 1 end ")
		b.WriteString("do local z = 2 end ")
		b.WriteString("local function f")
		b.WriteString(strings.Repeat("a", i%5+1))
		b.WriteString("() return 1 end ")
		b.WriteString("handlers[1] = function() return 2 end ")
		b.WriteString("repeat local w = 1 until true ")
	}
	assert.NoError(t, checkSourceShape("inline", b.String()))
}

func TestCompileTimeIsBoundedForLargeFlatSources(t *testing.T) {
	// Compilation runs before the limited context opens, so its cost must
	// stay small at the source-size ceiling for the constructs most likely
	// to be expensive (many locals, many statements).
	lim := MaxLimits()
	for name, unit := range map[string]string{
		"locals":      "local a%d = %d\n",
		"assignments": "x%d = %d\n",
		"calls":       "print(%d, %d)\n",
	} {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			for i := 0; b.Len() < lim.MaxSourceBytes-64; i++ {
				b.WriteString(strings.ReplaceAll(strings.ReplaceAll(unit, "%d", "1"), "a1", "a"+itoa(i%180)))
			}
			start := time.Now()
			res := Run(context.Background(), Program{Source: b.String()}, lim, newFakeHost())
			elapsed := time.Since(start)
			t.Logf("%s: %d bytes, outcome %s, %v", name, b.Len(), res.Outcome, elapsed)
			assert.Less(t, elapsed, slack(3*time.Second))
		})
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}
