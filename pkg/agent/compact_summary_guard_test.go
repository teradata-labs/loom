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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingCompressor is an enabled compressor that always errors.
type failingCompressor struct{ calls int }

func (c *failingCompressor) CompressMessages(context.Context, []Message) (string, error) {
	c.calls++
	return "", errors.New("provider down")
}
func (c *failingCompressor) IsEnabled() bool { return true }

// proseMemory builds turns 1..turns, each a large user message and a large
// assistant reply, with no tool calls.
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

// unfoldedTurns is the set of turns that still have a row in the context.
func unfoldedTurns(sm *SegmentedMemory) map[int64]bool {
	turns := map[int64]bool{}
	for _, m := range sm.contextMessages {
		if !m.Folded {
			turns[m.Turn] = true
		}
	}
	return turns
}

// A requested compaction must never report success after dropping turns behind
// a placeholder line: without a summary the fold is skipped.
func TestCompact_NeverFoldsWithoutASummary(t *testing.T) {
	t.Run("failing compressor: reports unavailable, history intact", func(t *testing.T) {
		sm := proseMemory(t, 200000, 20000, 6)
		comp := &failingCompressor{}
		sm.SetCompressor(comp)

		res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.ErrorIs(t, err, ErrSummaryUnavailable)
		assert.Positive(t, comp.calls, "the compressor was asked")
		assert.False(t, res.Compacted, "nothing was compacted")
		assert.Len(t, unfoldedTurns(sm), 6, "no turn left the context")
		assert.Equal(t, 0, sm.summary.n, "no summary version was written")
		assert.False(t, sm.foldRequireSummary, "the flag does not leak into relief")
	})

	t.Run("no compressor at all behaves the same", func(t *testing.T) {
		sm := proseMemory(t, 200000, 20000, 6)

		_, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.ErrorIs(t, err, ErrSummaryUnavailable)
		assert.Len(t, unfoldedTurns(sm), 6, "no turn left the context")
	})

	// A compaction folds only, so a failed summary with tool turns is the same
	// refusal as with prose turns: no partial success, nothing stubbed.
	t.Run("tool turns and a failed summary: nothing is stubbed instead", func(t *testing.T) {
		sm := toolTurnMemory(t, 200000, 20000, 6)
		sm.SetCompressor(&failingCompressor{})

		res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.ErrorIs(t, err, ErrSummaryUnavailable)
		assert.False(t, res.Compacted, "no partial success: nothing is stubbed")
		assert.False(t, res.TurnsFolded, "the fold did not run without a summary")
		assert.Len(t, unfoldedTurns(sm), 6, "no turn left the context")
	})
}
