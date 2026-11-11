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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proseMemory builds turns 1..n of user/assistant prose, each message ~3 dense
// rows. Prose has no tool result to evict, so only a fold or the fallback view
// can bring it down.
func proseMemory(t *testing.T, maxTokens, reserved, turns int) *SegmentedMemory {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", maxTokens, reserved)
	sm.SetThreshold(16384)
	for turn := 1; turn <= turns; turn++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: denseResult(turn) + denseResult(turn+100), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: denseResult(turn+200) + denseResult(turn+300), Turn: int64(turn)})
	}
	return sm
}

func sentTurns(sm *SegmentedMemory) map[int64]bool {
	turns := map[int64]bool{}
	for _, m := range sm.GetMessagesForLLM() {
		if m.Role != "system" {
			turns[m.Turn] = true
		}
	}
	return turns
}

func TestEnsureFits_UnderUsableIsANoOp(t *testing.T) {
	sm := proseMemory(t, 100000, 1000, 2)

	fit := sm.EnsureFits(context.Background())

	require.True(t, fit.Fits)
	assert.False(t, fit.Fallback)
	assert.False(t, fit.Shed)
	assert.Len(t, sentTurns(sm), 2, "nothing is dropped when the request already fits")
}

func TestEnsureFits_NoWindowConfiguredDisablesTheGuard(t *testing.T) {
	sm := proseMemory(t, 0, 0, 3)

	assert.True(t, sm.EnsureFits(context.Background()).Fits)
}

func TestEnsureFits_FallbackKeepsNewestTurnsAndFits(t *testing.T) {
	// usable 11000; 12 turns of ~3K tokens each is far over it.
	sm := proseMemory(t, 12000, 1000, 12)
	// A pass already in flight makes ReleasePressure shed nothing, as when the
	// compressor fails and every region is already flagged.
	sm.reliefInFlight = true

	fit := sm.EnsureFits(context.Background())

	require.True(t, fit.Fits, "the fallback view always fits when the newest turns do")
	require.True(t, fit.Fallback)
	require.LessOrEqual(t, fit.Estimate, fit.Usable)
	turns := sentTurns(sm)
	assert.True(t, turns[12], "the current turn is always sent")
	assert.False(t, turns[1], "old turns are dropped from the send view")
	assert.Len(t, turns, fit.TurnsKept)

	// Send-side only: the store still holds every turn for the next fold.
	stored := map[int64]bool{}
	for _, m := range sm.contextMessages {
		stored[m.Turn] = true
	}
	assert.Len(t, stored, 12)

	// The next call re-evaluates from scratch: with room again, nothing is dropped.
	sm.tokenBudget.MaxTokens = 1000000
	next := sm.EnsureFits(context.Background())
	assert.True(t, next.Fits)
	assert.False(t, next.Fallback)
	assert.Len(t, sentTurns(sm), 12)
}

func TestEnsureFits_CurrentTurnAloneTooBigReportsNotFitting(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 3000, 500)
	sm.SetThreshold(1 << 20)
	huge := ""
	for i := 0; i < 20; i++ {
		huge += denseResult(i)
	}
	sm.AddMessage(ctx, Message{Role: "user", Content: huge, Turn: 1})

	fit := sm.EnsureFits(ctx)

	assert.False(t, fit.Fits)
	assert.Greater(t, fit.Estimate, fit.Usable)
	assert.False(t, fit.Fallback)
}

func TestKeepNewestTurns_KeepsSystemAndSyntheticResultsWithTheirTurn(t *testing.T) {
	compiled := []Message{
		{Role: "system", Content: "ROM"},
		{Role: "system", Content: "summary"},
		{Role: "user", Content: "old", Turn: 1},
		{Role: "assistant", Turn: 2, ToolCalls: []ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: "tool", ToolUseID: "a", Content: "r", Turn: 2},
		{Role: "tool", ToolUseID: "b", Content: "synthetic"}, // no Turn: belongs to turn 2
		{Role: "user", Content: "now", Turn: 3},
	}

	kept := keepNewestTurns(compiled, 3, 2)

	var got []string
	for _, m := range kept {
		got = append(got, fmt.Sprintf("%s:%s", m.Role, m.Content))
	}
	assert.Equal(t, []string{"system:ROM", "system:summary", "assistant:", "tool:r", "tool:synthetic", "user:now"}, got)
}

func TestChat_PreflightRefusesOversizedRequestWithoutCallingTheProvider(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	ag := reliefSeamAgent(t, llm, "preflight_too_long", sm)

	// Over the 11000 usable window by itself: nothing relief or the fallback can drop.
	_, err := ag.Chat(context.Background(), "preflight_too_long", strings.Repeat("lorem ipsum ", 6000))

	require.Error(t, err)
	var re *RecoverableError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "context_exhausted", re.ErrorType)
	assert.Contains(t, err.Error(), "compact the conversation or start a new session")
	assert.Equal(t, 0, llm.calls, "an oversized request is never sent")
}
