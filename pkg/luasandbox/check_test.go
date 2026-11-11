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
)

func TestCheck(t *testing.T) {
	small := Limits{MaxSourceBytes: 1 << 10}
	cases := []struct {
		name, src string
		wantErr   string // substring; empty means no error
	}{
		{"valid", `local x = args.n or 1 return x * 2`, ""},
		{"valid with tools", `local r = tools.must("q", {}) return r`, ""},
		{"syntax error has a position", "local x =\nreturn (", "report:"},
		{"too large", strings.Repeat("-- comment\n", 200), "the limit is 1024"},
		{"too deep", strings.Repeat("(", 300) + "1" + strings.Repeat(")", 300), "nest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(context.Background(), "report", tc.src, small)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// Check never runs the script: an infinite loop compiles instantly.
func TestCheckDoesNotExecute(t *testing.T) {
	require.NoError(t, Check(context.Background(), "", `while true do end`, Limits{}))
	require.NoError(t, Check(context.Background(), "", `error("never raised")`, Limits{}))
}

// Check honors the caller's context and the wall budget while it scans and
// compiles, as Run does (review: Ed-Jaramillo, #425, ported to #427's save
// path). The source is valid and at the size ceiling, so without the bound
// the check runs to the end: about 0.3 s, 3 s under the race detector.
func TestCheckHonorsContextAndWall(t *testing.T) {
	src := maximalFlatSource()
	// 100 ms of budget or cancellation point, plus microseconds of polling
	// latency, plus runtime teardown; slack scales it for -race (x10) and
	// loaded CI (x3), the same bound as TestSourcePreparationHonorsRunLimits.
	bound := slack(150 * time.Millisecond)
	tests := []struct {
		name  string
		wall  time.Duration
		ctx   func(t *testing.T) context.Context
		cause error
	}{
		{"wall budget of 100ms", 100 * time.Millisecond, func(*testing.T) context.Context { return context.Background() }, context.DeadlineExceeded},
		{"caller cancels during the check", MaxLimits().Wall, func(t *testing.T) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			stop := time.AfterFunc(50*time.Millisecond, cancel)
			t.Cleanup(func() { stop.Stop(); cancel() })
			return ctx
		}, context.Canceled},
		{"caller already cancelled", MaxLimits().Wall, func(*testing.T) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lim := MaxLimits()
			lim.Wall = tt.wall
			start := time.Now()
			err := Check(tt.ctx(t), "report", src, lim)
			elapsed := time.Since(start)
			t.Logf("err=%v after %v", err, elapsed)
			assert.Less(t, elapsed, bound, "the check must stop early, not after compiling")
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.cause), "got %v, want %v", err, tt.cause)
		})
	}
	require.NoError(t, Check(context.Background(), "report", src, MaxLimits()), "the source itself is valid")
}
