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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/third_party/golua/astcomp"
	"github.com/teradata-labs/loom/third_party/golua/code"
	"github.com/teradata-labs/loom/third_party/golua/ircomp"
	"github.com/teradata-labs/loom/third_party/golua/parsing"
	rt "github.com/teradata-labs/loom/third_party/golua/runtime"
	"github.com/teradata-labs/loom/third_party/golua/scanner"
	"github.com/teradata-labs/loom/third_party/golua/token"
)

// maximalFlatSource is valid Lua that runs to completion, as long as
// MaxLimits allows: assignments up to the source-size ceiling. Checking,
// parsing and compiling it takes about 0.3 s, and about 3 s under the race
// detector, before the first statement runs.
func maximalFlatSource() string {
	const stmt = "x=1 "
	return strings.Repeat(stmt, MaxLimits().MaxSourceBytes/len(stmt))
}

// TestSourcePreparationHonorsRunLimits checks that the wall budget and the
// caller's context bind while the source is checked, parsed and compiled, not
// only once it runs.
func TestSourcePreparationHonorsRunLimits(t *testing.T) {
	src := maximalFlatSource()
	require.LessOrEqual(t, len(src), MaxLimits().MaxSourceBytes)
	require.Greater(t, len(src), MaxLimits().MaxSourceBytes-8, "the source is at the size ceiling")

	// Each run must stop within 150 ms: the budget or cancellation point
	// (at most 100 ms) plus polling latency (the scanner and compilers poll
	// every 256 tokens or nodes, microseconds of work) plus runtime
	// teardown. slack scales that by 10 under the race detector, which slows
	// the parser about tenfold, and by 3 otherwise, for loaded CI machines:
	// 1.5 s and 450 ms. Running preparation to the end takes about twice as
	// long as either bound.
	bound := slack(150 * time.Millisecond)

	tests := []struct {
		name    string
		wall    time.Duration
		ctx     func(t *testing.T) context.Context
		outcome Outcome
		limit   string
	}{
		{
			name: "wall budget of 100ms",
			wall: 100 * time.Millisecond,
			ctx: func(*testing.T) context.Context {
				return context.Background()
			},
			outcome: OutcomeBudgetExceeded,
			limit:   LimitWall,
		},
		{
			name: "caller cancels during preparation",
			wall: MaxLimits().Wall,
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				stop := time.AfterFunc(50*time.Millisecond, cancel)
				t.Cleanup(func() { stop.Stop(); cancel() })
				return ctx
			},
			outcome: OutcomeCancelled,
		},
		{
			name: "caller deadline of 100ms",
			wall: MaxLimits().Wall,
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			outcome: OutcomeCancelled,
		},
		{
			name: "caller already cancelled",
			wall: MaxLimits().Wall,
			ctx: func(*testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			outcome: OutcomeCancelled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := MaxLimits()
			lim.Wall = tt.wall
			ctx := tt.ctx(t)
			start := time.Now()
			res := Run(ctx, Program{Source: src}, lim, newFakeHost())
			elapsed := time.Since(start)
			t.Logf("outcome %s after %v: %s", res.Outcome, elapsed, res.Error)
			assert.Less(t, elapsed, bound, "the run must stop during preparation, not after it")
			assert.Equal(t, tt.outcome, res.Outcome, res.Error)
			assert.Equal(t, tt.limit, res.Limit)
		})
	}
}

// TestShapeGuardStopsWhenContextEnds checks the source guard on its own: it
// returns the context's error instead of scanning to the end.
func TestShapeGuardStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := checkSourceShape(ctx, "inline", maximalFlatSource())
	require.ErrorIs(t, err, context.Canceled)
	assert.NoError(t, checkSourceShape(context.Background(), "inline", maximalFlatSource()))
}

// errPollStop is the panic value the poll tests use to stop work early.
var errPollStop = errors.New("poll stop")

// TestGoluaPreparationPolls checks each vendored phase on its own: the
// scanner, the AST compiler and the IR compiler call their poll function
// every 256 units of work, and a panic from the poll stops the phase.
func TestGoluaPreparationPolls(t *testing.T) {
	src := []byte(maximalFlatSource())
	stat, err := parsing.ParseChunk(scanner.New("inline", src))
	require.NoError(t, err)
	kidx, consts, err := astcomp.CompileLuaChunk("inline", stat)
	require.NoError(t, err)

	tests := []struct {
		name string
		// work runs the phase with poll installed.
		work func(poll func())
		// minPolls is the number of polls the whole phase makes: one per
		// 256 tokens, statements plus expressions, or instructions.
		minPolls int
	}{
		{
			name: "scanner",
			work: func(poll func()) {
				sc := scanner.New("inline", src, scanner.WithPoll(poll))
				for tok := sc.Scan(); tok != nil && tok.Type != token.EOF; tok = sc.Scan() {
				}
			},
			minPolls: len(src) / 4 * 3 / 256, // "x=1 " is three tokens
		},
		{
			name: "ast compiler",
			work: func(poll func()) {
				_, _, _ = astcomp.CompileLuaChunkWithPoll("inline", stat, poll)
			},
			minPolls: len(src) / 4 * 2 / 256, // one statement and one expression each
		},
		{
			name: "ir compiler",
			work: func(poll func()) {
				kc := ircomp.NewConstantCompiler(consts, code.NewBuilder("inline"))
				kc.QueueConstant(kidx)
				kc.SetPoll(poll)
				_, _ = kc.CompileQueue()
			},
			minPolls: len(src) / 4 / 256, // at least one instruction each
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			polls := 0
			tt.work(func() { polls++ })
			assert.GreaterOrEqual(t, polls, tt.minPolls, "the whole phase polls every 256 units")

			polls = 0
			assert.PanicsWithValue(t, errPollStop, func() {
				tt.work(func() {
					polls++
					if polls == 3 {
						panic(errPollStop)
					}
				})
			}, "a panic from the poll propagates out of the phase")
			assert.Equal(t, 3, polls, "the phase stops at the poll that panicked")
		})
	}
}

// TestGoluaCompileStopsWhenInterrupted checks the runtime wiring: compiling
// inside a context whose Interrupt has fired terminates that context during
// parsing instead of compiling the whole source first.
func TestGoluaCompileStopsWhenInterrupted(t *testing.T) {
	src := []byte(maximalFlatSource())
	tests := []struct {
		name    string
		compile func(r *rt.Runtime) error
	}{
		{"chunk", func(r *rt.Runtime) error {
			_, err := r.CompileAndLoadLuaChunk("inline", src, rt.TableValue(r.GlobalEnv()))
			return err
		}},
		{"chunk or expression", func(r *rt.Runtime) error {
			_, err := r.CompileAndLoadLuaChunkOrExp("inline", src, rt.TableValue(r.GlobalEnv()))
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rt.New(nil)
			defer r.Close(nil)
			intr := rt.NewInterrupt()
			intr.Trigger("stopped by the test")
			start := time.Now()
			ctx, err := r.MainThread().CallContext(rt.RuntimeContextDef{
				HardLimits: rt.RuntimeResources{Cpu: MaxLimits().CPUTicks, Memory: MaxLimits().MemoryBytes},
				Interrupt:  intr,
			}, func() error { return tt.compile(r) })
			elapsed := time.Since(start)
			require.Error(t, err)
			assert.Equal(t, "stopped by the test", err.Error())
			require.NotNil(t, ctx)
			assert.Equal(t, rt.StatusKilled, ctx.Status(), "a termination during compilation kills the context")
			// Stopping at the first poll takes microseconds; compiling the
			// whole source takes 0.3 s (3 s under the race detector).
			assert.Less(t, elapsed, slack(30*time.Millisecond))
		})
	}
}
