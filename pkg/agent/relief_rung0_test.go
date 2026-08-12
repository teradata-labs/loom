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

// Relief rung 0 unit routes — the current turn's consumed region becomes
// sheddable (sweep the §4.3 query pairs, evict consumed results), while the
// pending region — the last assistant message and everything after it — is
// untouchable by every op. These routes drive the ops directly on a
// SegmentedMemory; the e2e ladder behavior lives in test/context-optimiser.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRung0Sweep_ConsumedPairRemoved — a consumed pure query pair (empty
// assistant shell + result) is removed whole; both sides always (§4.3).
func TestRung0Sweep_ConsumedPairRemoved(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "q1", Name: "query_tool_result"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "2", ToolUseID: "q1",
		Content: "the answer page", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "used it", Turn: 1})

	sm.mu.Lock()
	changed := sm.sweepRetrievalPairsLocked(context.Background(), 1)
	sm.mu.Unlock()

	require.True(t, changed, "a consumed pair was present — sweep must report a change")
	msgs := sm.GetMessages()
	require.Len(t, msgs, 2, "shell and result both gone — both sides always")
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "used it", msgs[1].Content)
}

// TestRung0Sweep_MixedBatchSplits — a batch carrying a query call AND a real
// call: the query side is stripped, the assistant row and the real pair
// survive — the persist filter's split, applied in memory.
func TestRung0Sweep_MixedBatchSplits(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{
			{ID: "q1", Name: "query_tool_result"},
			{ID: "c1", Name: "shell"},
		}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "2", ToolUseID: "q1",
		Content: "query answer", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "3", ToolUseID: "c1",
		Content: "shell output", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "done", Turn: 1})

	sm.mu.Lock()
	changed := sm.sweepRetrievalPairsLocked(context.Background(), 1)
	sm.mu.Unlock()

	require.True(t, changed)
	msgs := sm.GetMessages()
	require.Len(t, msgs, 4, "only the query result row left")
	require.Len(t, msgs[1].ToolCalls, 1, "query call stripped, real call kept")
	assert.Equal(t, "shell", msgs[1].ToolCalls[0].Name)
	assert.Equal(t, "shell output", msgs[2].Content, "the real pair survives intact")
}

// TestRung0Sweep_PendingPairSurvives — a query pair issued by the LAST
// assistant message is pending (answer not yet consumed): untouchable.
func TestRung0Sweep_PendingPairSurvives(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "looking", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "q1", Name: "query_tool_result"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "3", ToolUseID: "q1",
		Content: "fresh answer", Turn: 1})

	sm.mu.Lock()
	changed := sm.sweepRetrievalPairsLocked(context.Background(), 1)
	sm.mu.Unlock()

	assert.False(t, changed, "nothing consumed carries a query call — sweep must be a no-op")
	msgs := sm.GetMessages()
	require.Len(t, msgs, 4)
	assert.Equal(t, "fresh answer", msgs[3].Content, "the pending answer survives whole")
}

// TestRung0Sweep_Idempotent — a second pass over the swept state changes
// nothing and says so.
func TestRung0Sweep_Idempotent(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "q1", Name: "query_tool_result"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "2", ToolUseID: "q1",
		Content: "answer", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "used it", Turn: 1})

	sm.mu.Lock()
	first := sm.sweepRetrievalPairsLocked(context.Background(), 1)
	second := sm.sweepRetrievalPairsLocked(context.Background(), 1)
	sm.mu.Unlock()

	assert.True(t, first)
	assert.False(t, second, "swept state has no query pairs left — must report no change")
}

// TestRung0Evict_PendingGuard — evict at b = t marks consumed results and
// never the pending batch.
func TestRung0Evict_PendingGuard(t *testing.T) {
	sm := newCompileMemory(t)
	big := strings.Repeat("x", 5000)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c1", Name: "shell"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "2", ToolUseID: "c1",
		Content: big, Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Turn: 1,
		ToolCalls: []ToolCall{{ID: "c2", Name: "shell"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ID: "4", ToolUseID: "c2",
		Content: big, Turn: 1})

	sm.mu.Lock()
	changed := sm.evictLocked(context.Background(), 1)
	sm.mu.Unlock()

	require.True(t, changed, "the consumed result clears the floor — must evict")
	msgs := sm.GetMessages()
	assert.True(t, msgs[2].Evicted, "consumed result evicted")
	assert.False(t, msgs[4].Evicted, "pending result (after the last assistant message) untouched")
}

// TestRung0Evict_FirstIterationNoop — no assistant message yet: everything is
// pending, evict at b = t must change nothing.
func TestRung0Evict_FirstIterationNoop(t *testing.T) {
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "q", Turn: 1})

	sm.mu.Lock()
	evictChanged := sm.evictLocked(context.Background(), 1)
	sweepChanged := sm.sweepRetrievalPairsLocked(context.Background(), 1)
	sm.mu.Unlock()

	assert.False(t, evictChanged)
	assert.False(t, sweepChanged)
	require.Len(t, sm.GetMessages(), 1, "L1 unchanged")
}
