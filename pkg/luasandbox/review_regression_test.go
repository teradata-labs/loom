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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/third_party/golua/lib/stringlib/pattern"
	rt "github.com/teradata-labs/loom/third_party/golua/runtime"
)

const cyclicChildEnv = "LUASANDBOX_CYCLIC_CHILD"

// TestCyclicHostDataIsBounded runs in a child process: before the fix a
// cyclic host result exhausted the Go stack, which no recover can catch.
func TestCyclicHostDataIsBounded(t *testing.T) {
	if os.Getenv(cyclicChildEnv) == "1" {
		cyclicHostDataChild(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCyclicHostDataIsBounded$", "-test.v") // #nosec G204 -- re-runs this test binary
	cmd.Env = append(os.Environ(), cyclicChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "the child process must survive a cyclic host result:\n%s", tail(string(out), 4096))
	assert.Contains(t, string(out), "--- PASS: TestCyclicHostDataIsBounded")
}

func cyclicHostDataChild(t *testing.T) {
	m := map[string]any{"ok": 1}
	for _, k := range []string{"a", "b", "c", "d"} {
		m[k] = m // fan-out 4: a depth bound alone would visit 4^32 nodes
	}
	s := []any{"x", nil}
	s[1] = s
	m["list"] = s
	h := newFakeHost().with("cyclic", func(context.Context, map[string]any) (*CallResult, error) {
		return &CallResult{OK: true, Data: m}, nil
	})
	start := time.Now()
	res := runSrc(t, `local d = tools.call("cyclic").data return {ok = d.ok, a = d.a, list2 = d.list[2]}`, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"ok": int64(1), "a": cyclicMarker, "list2": cyclicMarker}, res.Value)
	assert.Less(t, time.Since(start), slack(2*time.Second))
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestNamedVarargAndCreatedTablesAreCharged(t *testing.T) {
	const n = 50000
	used := func(t *testing.T, mem uint64, value string) *RunResult {
		lim := small()
		lim.MemoryBytes = mem
		src := "local function make(... xs) return xs end\nlocal keep = {}\nfor i = 1, 50000 do keep[i] = " + value + " end\nreturn #keep"
		return Run(context.Background(), Program{Source: src}, lim, newFakeHost())
	}
	base := used(t, 64<<20, "1")
	require.Equal(t, OutcomeOK, base.Outcome, base.Error)
	perTable := uint64(rt.TableHeaderSize)
	for name, value := range map[string]string{"named varargs": "make()", "table.create(0)": "table.create(0)"} {
		t.Run(name, func(t *testing.T) {
			res := used(t, 64<<20, value)
			require.Equal(t, OutcomeOK, res.Outcome, res.Error)
			assert.GreaterOrEqual(t, res.Used.MemoryBytes, base.Used.MemoryBytes+n*perTable,
				"each retained table is charged at least its header")
			// A budget between the integer baseline and the tables' real cost
			// must stop the run.
			res = used(t, base.Used.MemoryBytes+n*perTable/2, value)
			assert.Equal(t, OutcomeBudgetExceeded, res.Outcome, res.Error)
			assert.Equal(t, LimitMemory, res.Limit)
		})
	}
}

func TestStrictArrayConversionFitsExactly(t *testing.T) {
	encode := func(budget int, src string) *RunResult {
		lim := small()
		lim.MaxCallResultBytes = budget
		return Run(context.Background(), Program{Source: src}, lim, newFakeHost())
	}
	res := encode(10, `return json.encode({"123456"})`)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, `["123456"]`, res.Value, "10 bytes fit a 10-byte budget")
	res = encode(9, `return json.encode({"123456"})`)
	assert.Equal(t, OutcomeScriptError, res.Outcome, "one byte over is an error, not an empty array")
	assert.Contains(t, res.Error, "larger than 9 bytes")

	// {"xs":["12","34"]} is 18 bytes.
	var got map[string]any
	h := newFakeHost().with("echo", func(_ context.Context, args map[string]any) (*CallResult, error) {
		got = args
		return &CallResult{OK: true}, nil
	})
	lim := small()
	lim.MaxCallResultBytes = 18
	res = Run(context.Background(), Program{Source: `return tools.call("echo", {xs = {"12", "34"}}).ok`}, lim, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"xs": []any{"12", "34"}}, got, "the last element is not dropped")
	lim.MaxCallResultBytes = 17
	res = Run(context.Background(), Program{Source: `return tools.call("echo", {xs = {"12", "34"}}).ok`}, lim, h)
	assert.Equal(t, OutcomeScriptError, res.Outcome)
	assert.Contains(t, res.Error, "larger than 17 bytes")
}

func TestJSONDecodeChargesAsItBuilds(t *testing.T) {
	lim := small()
	lim.MemoryBytes = 16 << 20
	if raceEnabled {
		lim.Wall *= 10
		lim.CPUTicks *= 10
	}
	// 4 MB of JSON admitted as a Lua string; decoded whole into a Go tree it
	// would be about 100 MB before any charge.
	src := `local s = "[" .. string.rep("0,", 2000000) .. "0]" return #json.decode(s)`
	stop := sampleHeapPeak()
	res := Run(context.Background(), Program{Source: src}, lim, newFakeHost())
	base, peak := stop()
	require.Equal(t, OutcomeBudgetExceeded, res.Outcome, res.Error)
	assert.Equal(t, LimitMemory, res.Limit)
	const collectorLag = 16 << 20
	grew := peak - min(base, peak)
	assert.Less(t, grew, 2*lim.MemoryBytes+collectorLag, "heap grew %d MiB against a %d MiB budget", grew>>20, lim.MemoryBytes>>20)
}

func TestOversizedHostResultIsMeasuredNotEncoded(t *testing.T) {
	s := &run{lim: small()} // 64 KiB per call
	big := make([]any, 2_000_000)
	for i := range big {
		big[i] = "xy"
	}
	stop := sampleHeapPeak()
	data, text, size, cut := s.prepareData(map[string]any{"rows": big})
	base, peak := stop()
	assert.True(t, cut)
	assert.Nil(t, text)
	assert.Greater(t, size, s.lim.MaxCallResultBytes)
	summary := data.(map[string]any)
	assert.Equal(t, true, summary["truncated"])
	assert.LessOrEqual(t, len(summary["preview"].(string)), previewBytes)
	assert.True(t, strings.HasPrefix(summary["preview"].(string), `{"rows":["xy","xy"`))
	// Encoding the whole value would allocate a 10 MB buffer.
	assert.Less(t, peak-min(base, peak), uint64(4<<20), "measuring stops at the limit")
}

func TestMeasureJSONMatchesEncodingJSON(t *testing.T) {
	for _, v := range []any{
		nil, true, "plain", "esc \"\\\n\r\t\x01<>&   日本 \xff", 1.5, int64(-3), json.Number("12"),
		map[string]any{"b": []any{1, "x", nil}, "a": map[string]any{}}, []any{}, customRow{ID: 1, Name: "n"},
	} {
		want, err := json.Marshal(v)
		require.NoError(t, err)
		m := measureJSON(v, 1<<20, 1<<20)
		assert.False(t, m.flawed)
		assert.Equal(t, len(want), m.n, "%#v", v)
		assert.Equal(t, string(want), string(m.out), "%#v", v)
	}
}

func TestLateSuccessAfterCallTimeoutIsATimeout(t *testing.T) {
	h := newFakeHost().with("late", func(context.Context, map[string]any) (*CallResult, error) {
		time.Sleep(150 * time.Millisecond) // ignores cancellation
		return &CallResult{OK: true, Data: "too late"}, nil
	})
	lim := small()
	lim.ToolCallTimeout = 50 * time.Millisecond
	res := Run(context.Background(), Program{Source: `local r = tools.call("late") return {ok = r.ok, code = r.error and r.error.code}`}, lim, h)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, map[string]any{"ok": false, "code": CodeToolCallTimeout}, res.Value)
	require.Len(t, res.Calls, 1)
	assert.Equal(t, CodeToolCallTimeout, res.Calls[0].Code)
}

func TestOutOfRangeAnchoredMatchIsNoMatch(t *testing.T) {
	tests := map[string]any{
		`return string.match("", "^", math.maxinteger) == nil`: true,
		`return string.match("", "^", 5) == nil`:               true,
		`return string.match("abc", "^c", 4) == nil`:           true,
		`return string.match("abc", "^", 4)`:                   "",
		`return string.match("abc", "^b", 2)`:                  "b",
	}
	for src, want := range tests {
		res := runSrc(t, src, nil)
		require.Equal(t, OutcomeOK, res.Outcome, "%s: %s", src, res.Error)
		assert.Equal(t, want, res.Value, src)
	}
}

func TestStrictConversionRejectsNonFinite(t *testing.T) {
	for _, src := range []string{
		`return json.encode(0/0)`,
		`return json.encode({1, math.huge})`,
		`return tools.call("echo", {x = -math.huge})`,
	} {
		res := runSrc(t, src, newFakeHost().with("echo", echo))
		assert.Equal(t, OutcomeScriptError, res.Outcome, src)
		assert.Contains(t, res.Error, "NaN and infinity cannot be encoded as JSON", src)
	}
	res := runSrc(t, `return {1, 0/0}`, nil)
	require.Equal(t, OutcomeOK, res.Outcome, res.Error)
	assert.Equal(t, []any{int64(1), nil}, res.Value, "a returned value keeps null plus a warning")
	assert.Contains(t, res.Output, "became null")
}

func TestTruncatedOutputFitsItsCap(t *testing.T) {
	for _, limit := range []int{1, 30, 60, 100, 301, 4 << 10} {
		w := newCappedWriter(limit)
		for range 1000 {
			_, _ = w.Write([]byte("0123456789"))
		}
		s, cut := w.String()
		assert.True(t, cut)
		assert.LessOrEqual(t, len(s), max(limit, 3), "limit %d", limit)
		if limit >= 100 {
			assert.Contains(t, s, "bytes of output elided")
			assert.True(t, strings.HasSuffix(s, "0123456789"), "the final output survives")
		}
	}
	lim := small()
	res := Run(context.Background(), Program{Source: `for i = 1, 5000 do print(i) end error(string.rep("e", 100000))`}, lim, newFakeHost())
	assert.LessOrEqual(t, len(res.Output), lim.MaxOutputBytes)
	assert.True(t, res.Truncated.Output)
	assert.LessOrEqual(t, len(res.Error), maxErrorBytes)
	assert.Contains(t, res.Error, "bytes elided")
}

func TestFailedHostCallsAreInTheLedger(t *testing.T) {
	tests := map[string]toolFunc{
		"host error": func(context.Context, map[string]any) (*CallResult, error) { return nil, errBoom },
		"nil result": func(context.Context, map[string]any) (*CallResult, error) { return nil, nil },
	}
	for name, f := range tests {
		t.Run(name, func(t *testing.T) {
			h := newFakeHost().with("bad", f)
			res := runSrc(t, `tools.call("bad")`, h)
			require.Equal(t, OutcomeHostError, res.Outcome)
			require.Len(t, res.Calls, 1)
			assert.Equal(t, CallRecord{Tool: "bad", OK: false, Code: CodeHostError, Millis: res.Calls[0].Millis}, res.Calls[0])
			completed := 0
			h.mu.Lock()
			for _, ev := range h.events {
				if ev.Kind == ProgressToolCompleted {
					completed++
					assert.Equal(t, CodeHostError, ev.Code)
				}
			}
			h.mu.Unlock()
			assert.Equal(t, 1, completed)
		})
	}
}

// TestPatternMatchPollPanicPropagates checks the vendored matcher's poll hook
// directly: it is called while a long match runs, and the panic it raises (a
// context termination, in the engine) reaches the caller instead of being
// swallowed as an empty match.
func TestPatternMatchPollPanicPropagates(t *testing.T) {
	pat, err := pattern.New("a*a*a*a*a*a*b")
	require.NoError(t, err)
	subject := strings.Repeat("a", 60)
	type stop struct{}
	for name, match := range map[string]func(func()){
		"Match":          func(poll func()) { pat.Match(subject, 0, 0, poll) },
		"MatchFromStart": func(poll func()) { pat.MatchFromStart(subject, 0, 0, poll) },
	} {
		t.Run(name, func(t *testing.T) {
			polls := 0
			assert.PanicsWithValue(t, stop{}, func() {
				match(func() {
					polls++
					if polls == 3 {
						panic(stop{})
					}
				})
			})
			assert.Equal(t, 3, polls)
		})
	}

	// An exhausted budget is still reported as a failed match, not a panic.
	captures, used := pat.Match(subject, 0, 1000, func() {})
	assert.Nil(t, captures)
	assert.Equal(t, uint64(1001), used)
}
