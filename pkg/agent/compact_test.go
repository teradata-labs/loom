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

// recordingCompressor returns a fixed summary and keeps the input it was given.
type recordingCompressor struct {
	mu    sync.Mutex
	input []Message
}

func (c *recordingCompressor) CompressMessages(_ context.Context, msgs []Message) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = append([]Message(nil), msgs...)
	return "SUMMARY: kept the settled SQL and the open approvals", nil
}
func (c *recordingCompressor) IsEnabled() bool { return true }

// toolTurnMemory builds turns 1..turns, each with one tool call and a large result.
func toolTurnMemory(t *testing.T, maxTokens, reserved, turns int) *SegmentedMemory {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", maxTokens, reserved)
	sm.SetThreshold(1 << 20)
	for turn := 1; turn <= turns; turn++ {
		id := fmt.Sprintf("c%d", turn)
		sm.AddMessage(ctx, Message{Role: "user", Content: fmt.Sprintf("question %d", turn), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Turn: int64(turn),
			ToolCalls: []ToolCall{{ID: id, Name: "run_query", Input: map[string]interface{}{"n": turn}}}})
		sm.AddMessage(ctx, Message{Role: "tool", ID: fmt.Sprintf("%d", turn*10), ToolUseID: id,
			Content: denseResult(turn) + denseResult(turn+50), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: fmt.Sprintf("answer %d", turn), Turn: int64(turn)})
	}
	return sm
}

func TestCompact_StubsOldResultsFoldsOldTurnsAndKeepsRecentVerbatim(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 6)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err)

	assert.True(t, res.Compacted)
	assert.True(t, res.TurnsFolded)
	assert.Greater(t, res.TokensSaved(), 0)
	assert.Less(t, res.TokensAfter, res.TokensBefore)
	assert.Equal(t, 1, res.SummaryVersion)

	var summary string
	sent := map[int64]bool{}
	for _, m := range sm.GetMessagesForLLM() {
		if m.Role == "system" && strings.Contains(m.Content, "SUMMARY") {
			summary = m.Content
		}
		if m.Role != "system" {
			sent[m.Turn] = true
		}
	}
	assert.Contains(t, summary, "open approvals")
	assert.True(t, sent[5] && sent[6], "the newest two turns stay verbatim")
	assert.False(t, sent[1] || sent[2] || sent[3] || sent[4], "older turns live in the summary")
	for _, m := range sm.GetMessagesForLLM() {
		if m.Role == "tool" && m.Turn >= 5 {
			assert.NotContains(t, m.Content, "evicted from context", "kept turns keep their results whole")
		}
	}
}

func TestCompact_FocusReachesTheCompressor(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 5)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	_, err := sm.Compact(context.Background(), CompactOptions{Focus: "keep the SQL we settled on", KeepTurns: 1})
	require.NoError(t, err)

	var joined []string
	for _, m := range comp.input {
		joined = append(joined, m.Content)
	}
	assert.Contains(t, strings.Join(joined, "\n"), "keep the SQL we settled on")
	assert.Empty(t, sm.compactFocus, "the focus is cleared once the compaction ends")
}

func TestCompact_NothingOlderThanKeepTurnsIsANoOp(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 2)
	sm.SetCompressor(&recordingCompressor{})

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

	require.NoError(t, err)
	assert.False(t, res.Compacted)
	assert.Equal(t, res.TokensBefore, res.TokensAfter)
}

func TestCompact_RescuesASessionAlreadyOverItsWindow(t *testing.T) {
	// usable 11000; five turns of ~1.5K-token results plus the rest is over it.
	sm := toolTurnMemory(t, 12000, 1000, 12)
	sm.SetCompressor(&recordingCompressor{})
	sm.reliefInFlight = false
	sm.mu.Lock()
	start := sm.estimateLocked()
	sm.mu.Unlock()
	require.Greater(t, start, 11000, "fixture must start over the window")

	res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})
	require.NoError(t, err)

	require.True(t, res.Compacted)
	assert.LessOrEqual(t, res.TokensAfter, 11000, "the compacted session fits again")
	assert.True(t, sm.EnsureFits(context.Background()).Fits)
}

func TestCompact_RefusesWhenAPassIsAlreadyRunning(t *testing.T) {
	sm := toolTurnMemory(t, 200000, 20000, 4)
	sm.reliefInFlight = true

	_, err := sm.Compact(context.Background(), CompactOptions{})

	assert.ErrorIs(t, err, ErrCompactionBusy)
}
