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
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Relief's last-resort rung: a turn whose OWN tool results exceed the window.
// Shape reproduced from production — one assistant response fanning out
// hundreds of parallel calls, every result under the offload threshold, so each
// renders whole and the prior-turn ladder (which stops at T−1) sheds nothing.

// denseResult is token-dense (~4 B/token), varied per call so a stub genuinely
// sheds tokens and no two rows share a token-cache entry.
func denseResult(i int) string {
	return strings.Repeat(fmt.Sprintf("row %d acct=%05d region=%d spend=%d.%02d; ", i, i*37, i%7, i*113, i%100), 60)
}

// parallelBatchMemory builds a settled turn 1 and a current turn 2 holding one
// assistant row with n tool calls followed by their n results (IDs 1000+i).
func parallelBatchMemory(t *testing.T, maxTokens, reserved, n int, toolName string) *SegmentedMemory {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", maxTokens, reserved)
	sm.SetThreshold(16384) // production default: every result below renders whole
	sm.AddMessage(ctx, Message{Role: "user", Content: "hi", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", Content: "hello", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: "run them all in parallel", Turn: 2})
	calls := make([]ToolCall, n)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("c%d", i), Name: toolName, Input: map[string]interface{}{"i": i}}
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 2, ToolCalls: calls})
	for i := 0; i < n; i++ {
		sm.AddMessage(ctx, Message{Role: "tool", ID: fmt.Sprintf("%d", 1000+i),
			ToolUseID: fmt.Sprintf("c%d", i), Content: denseResult(i), Turn: 2})
	}
	return sm
}

// toolRenders maps ToolUseID → rendered content from one compile.
func toolRenders(sm *SegmentedMemory) map[string]string {
	out := map[string]string{}
	for _, m := range sm.GetMessagesForLLM() {
		if m.Role == "tool" {
			out[m.ToolUseID] = m.Content
		}
	}
	return out
}

func TestReleasePressure_OffloadsCurrentTurnWhenPriorTurnsCannotShed(t *testing.T) {
	const n = 40
	// usable 11000 → start 9900, target 6600; the batch is ~40 × 750 tokens.
	sm := parallelBatchMemory(t, 12000, 1000, n, "run_query")

	shed, estimate, target := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "the current turn is the only pressure; the last-resort rung must shed it")
	assert.LessOrEqual(t, estimate, target, "offloading the turn's results reaches the release mark")

	renders := toolRenders(sm)
	stubbed := 0
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		if renders[id] == denseResult(i) {
			continue
		}
		stubbed++
		assert.Contains(t, renders[id], "held in memory this turn",
			"a pressure-offloaded row renders the §5.5 OFFLOAD stub, not the evicted one")
		assert.Contains(t, renders[id], fmt.Sprintf("message_id=%d", 1000+i),
			"the stub advertises the row's query_tool_result door")
	}
	require.Greater(t, stubbed, 0)

	// Oldest first: every stubbed row precedes every whole row.
	lastStub, firstWhole := -1, n
	for i := 0; i < n; i++ {
		if renders[fmt.Sprintf("c%d", i)] == denseResult(i) {
			if i < firstWhole {
				firstWhole = i
			}
		} else {
			lastStub = i
		}
	}
	assert.Less(t, lastStub, firstWhole, "the newest results stay whole; the oldest are offloaded first")

	// Lossless within the turn: the payload is still readable, and nothing was
	// flagged evicted (render state only, never persisted).
	payload, err := sm.inTurnPayload(1000)
	require.NoError(t, err)
	assert.Equal(t, denseResult(0), payload)
	for _, m := range sm.GetMessages() {
		assert.False(t, m.Evicted, "the rung never sets the durable evicted flag")
	}
}

func TestReleasePressure_CurrentTurnRungKeepsHalfWhenHalfSuffices(t *testing.T) {
	const n = 16
	// Window sized so offloading the oldest half is enough: ~16 × 750 = 12K
	// tokens of results, target 60% of usable.
	sm := parallelBatchMemory(t, 16000, 1000, n, "run_query")

	shed, estimate, target := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed)
	require.LessOrEqual(t, estimate, target)

	renders := toolRenders(sm)
	for i := n / 2; i < n; i++ {
		assert.Equal(t, denseResult(i), renders[fmt.Sprintf("c%d", i)],
			"first rung keeps the newest n/2 whole — shed the minimum needed")
	}
}

func TestReleasePressure_PriorTurnRungsRunBeforeCurrentTurn(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	sm.SetThreshold(16384)
	// Turn 1 carries the pressure; turn 2 has a single small result.
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})
	calls := make([]ToolCall, 20)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("p%d", i), Name: "run_query", Input: map[string]interface{}{"i": i}}
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 1, ToolCalls: calls})
	for i := range calls {
		sm.AddMessage(ctx, Message{Role: "tool", ID: fmt.Sprintf("%d", i+1), ToolUseID: fmt.Sprintf("p%d", i),
			Content: denseResult(i), Turn: 1})
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Content: "done", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: "q2", Turn: 2})
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 2,
		ToolCalls: []ToolCall{{ID: "cur", Name: "run_query", Input: map[string]interface{}{}}}})
	sm.AddMessage(ctx, Message{Role: "tool", ID: "500", ToolUseID: "cur", Content: denseResult(999), Turn: 2})

	shed, estimate, target := sm.ReleasePressure(ctx, 0)
	require.True(t, shed)
	require.LessOrEqual(t, estimate, target)
	assert.Equal(t, denseResult(999), toolRenders(sm)["cur"],
		"prior-turn eviction reached target, so the current turn was never touched")
}

func TestPressureOffload_InertOnceTurnAdvances(t *testing.T) {
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query")
	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed)
	require.Contains(t, toolRenders(sm)["c0"], "held in memory this turn")

	// A new turn starts: the offload set was scoped to turn 2 and must not
	// render turn-2 rows as offload stubs (their query door is gone). They are
	// ordinary prior-turn rows now, left to the evict ladder.
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "next", Turn: 3})
	assert.Equal(t, denseResult(0), toolRenders(sm)["c0"],
		"a stale pressure set is inert once the turn advances")
}

// The model's own query_tool_result read-back is never a last-resort
// candidate. Shape from the PR #420 review (F1): once the stubs alone keep the
// estimate over the start mark, the next pass runs the ladder to keep=0 — and
// before the fix it stubbed the read itself, rendering a stub whose door
// pointed at the read (message_id=5000). The model could never see the data.
func TestReleasePressure_CurrentTurnRungNeverStubsQueryToolResultReads(t *testing.T) {
	readBack := denseResult(0) + denseResult(1) // ~6 KB, one page, under threshold
	for _, n := range []int{40, 120, 200, 300} {
		t.Run(fmt.Sprintf("fanout=%d", n), func(t *testing.T) {
			ctx := context.Background()
			// usable 19000 → start 17100, target 11400.
			sm := parallelBatchMemory(t, 20000, 1000, n, "run_query")
			_, _, _ = sm.ReleasePressure(ctx, 0)
			require.Contains(t, toolRenders(sm)["c0"], "held in memory this turn",
				"pass 1 offloads the oldest results")

			// The model reads one offloaded result back through its door.
			sm.AddMessage(ctx, Message{Role: "assistant", Turn: 2, ToolCalls: []ToolCall{{
				ID: "read0", Name: "query_tool_result", Input: map[string]interface{}{"message_id": 1000},
			}}})
			sm.AddMessage(ctx, Message{Role: "tool", ID: "5000", ToolUseID: "read0", Content: readBack, Turn: 2})

			_, _, _ = sm.ReleasePressure(ctx, 0)
			r := toolRenders(sm)["read0"]
			assert.Equal(t, readBack, r, "the read-back renders whole on every later pass")
			assert.NotContains(t, r, "message_id=5000", "a stub must never point the model at its own read")
		})
	}
}

func TestCurrentTurnOverflow(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		build  func(t *testing.T) *SegmentedMemory
		relief bool
		want   bool
	}{
		{
			// 300 stubs alone (~136 tokens each) exceed start 17,100.
			name:   "stubs alone over start after keep=0",
			build:  func(t *testing.T) *SegmentedMemory { return parallelBatchMemory(t, 20000, 1000, 300, "run_query") },
			relief: true,
			want:   true,
		},
		{
			name:   "relief reached target",
			build:  func(t *testing.T) *SegmentedMemory { return parallelBatchMemory(t, 20000, 1000, 40, "run_query") },
			relief: true,
			want:   false,
		},
		{
			// Candidates still render whole: the ladder has room, so this is
			// relief's job, not an overflow.
			name:   "before relief runs",
			build:  func(t *testing.T) *SegmentedMemory { return parallelBatchMemory(t, 20000, 1000, 300, "run_query") },
			relief: false,
			want:   false,
		},
		{
			// Every result is over the threshold, so the size rule stubs them
			// all and the last-resort rung has no candidate: still irreducible.
			name: "size-rule stubs only",
			build: func(t *testing.T) *SegmentedMemory {
				sm := parallelBatchMemory(t, 20000, 1000, 300, "run_query")
				sm.SetThreshold(1024)
				return sm
			},
			relief: true,
			want:   true,
		},
		{
			// A ROM that alone fills the window is a config problem, not the
			// turn's results.
			name: "no stubbed tool result",
			build: func(t *testing.T) *SegmentedMemory {
				sm := NewSegmentedMemory(denseText("rom", 400), 20000, 1000)
				sm.AddMessage(ctx, Message{Role: "user", Content: "hi", Turn: 1})
				return sm
			},
			relief: true,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := tt.build(t)
			if tt.relief {
				_, _, _ = sm.ReleasePressure(ctx, 0)
			}
			o, over := sm.CurrentTurnOverflow()
			require.Equal(t, tt.want, over)
			if !tt.want {
				assert.Equal(t, CurrentTurnOverflow{}, o)
				return
			}
			assert.GreaterOrEqual(t, o.FloorTokens, o.StartTokens)
			assert.Equal(t, 300, o.ToolResults)
			assert.Equal(t, 300, o.StubbedResults, "every result is a stub at keep=0")
		})
	}
}

func TestReleasePressure_CurrentTurnRungOverridesOffloadExemption(t *testing.T) {
	sm := parallelBatchMemory(t, 12000, 1000, 40, "get_syntax_help")
	sm.SetOffloadExemptTools([]string{"get_syntax_help"})

	shed, estimate, target := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed)
	assert.LessOrEqual(t, estimate, target,
		"exemption yields to the last resort — the alternative is a turn that cannot be sent")
	assert.Contains(t, toolRenders(sm)["c0"], "held in memory this turn")
}

func TestReleasePressure_CurrentTurnRungStorelessRowsFallBackToEvictedStub(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	sm.SetThreshold(16384)
	sm.AddMessage(ctx, Message{Role: "user", Content: "q", Turn: 1})
	calls := make([]ToolCall, 40)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("c%d", i), Name: "run_query", Input: map[string]interface{}{"i": i}}
	}
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: 1, ToolCalls: calls})
	for i := range calls {
		// No durable ID: there is no query_tool_result door to advertise.
		sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: fmt.Sprintf("c%d", i), Content: denseResult(i), Turn: 1})
	}

	shed, _, _ := sm.ReleasePressure(ctx, 0)
	require.True(t, shed)
	r := toolRenders(sm)["c0"]
	assert.Contains(t, r, "evicted from context", "storeless rows get the evicted stub, never message_id=0")
	assert.NotContains(t, r, "message_id=0")
}

func TestCompile_PressureOffloadedRowsSitBehindCacheBreakpoint(t *testing.T) {
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query")
	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed)

	out := sm.GetMessagesForLLM()
	firstStub, bp := -1, -1
	for i, m := range out {
		if firstStub == -1 && m.Role == "tool" && strings.Contains(m.Content, "held in memory this turn") {
			firstStub = i
		}
		// The stable marker is the first non-system breakpoint; the later
		// till-NOW marker closes the current turn and sits after the stubs
		// by design (it is what re-renders each turn).
		if bp == -1 && i >= 1 && m.CacheBreakpoint && m.Role != "system" {
			bp = i
		}
	}
	require.NotEqual(t, -1, firstStub)
	require.NotEqual(t, -1, bp)
	assert.Less(t, bp, firstStub,
		"a pressure stub re-renders next turn, so it must never be inside the cached prefix")
}

func TestReleasePressure_CurrentTurnRungConcurrentReaders(t *testing.T) {
	sm := parallelBatchMemory(t, 12000, 1000, 40, "run_query")
	var wg sync.WaitGroup
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = sm.GetMessagesForLLM()
				_, _ = sm.inTurnPayload(1000)
			}
		}()
	}
	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	wg.Wait()
	assert.True(t, shed)
}
