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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A requested compaction must never report success after dropping turns behind
// a placeholder line: without a summary the fold is skipped.
func TestCompact_NeverFoldsWithoutASummary(t *testing.T) {
	t.Run("failing compressor and nothing to stub: reports unavailable, history intact", func(t *testing.T) {
		sm := proseMemory(t, 200000, 20000, 6)
		comp := &failingCompressor{}
		sm.SetCompressor(comp)

		res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.ErrorIs(t, err, ErrSummaryUnavailable)
		assert.Positive(t, comp.calls)
		assert.False(t, res.Compacted)
		assert.Len(t, unfoldedTurns(sm), 6, "no turn left the context")
		assert.Equal(t, 0, sm.summary.n)
		assert.False(t, sm.foldRequireSummary, "the flag does not leak into relief")
	})

	t.Run("no compressor at all behaves the same", func(t *testing.T) {
		sm := proseMemory(t, 200000, 20000, 6)

		_, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.ErrorIs(t, err, ErrSummaryUnavailable)
		assert.Len(t, unfoldedTurns(sm), 6)
	})

	t.Run("stubbing still counts when only the summary failed", func(t *testing.T) {
		sm := toolTurnMemory(t, 200000, 20000, 6)
		sm.SetCompressor(&failingCompressor{})

		res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.NoError(t, err)
		assert.True(t, res.Compacted)
		assert.True(t, res.ToolResultsStubbed)
		assert.False(t, res.TurnsFolded, "partial success is reported as such")
		assert.Len(t, unfoldedTurns(sm), 6)
	})
}

// Embedders may build fresh memory for every turn, so the cooldown must not
// live only on the memory instance.
func TestAutoCompact_CooldownSurvivesFreshMemoryForTheSameSession(t *testing.T) {
	const sessionID = "sess-cooldown-survives-reload"
	t.Cleanup(func() {
		autoCompactAttempts.mu.Lock()
		delete(autoCompactAttempts.turns, sessionID)
		autoCompactAttempts.mu.Unlock()
	})

	first := proseMemory(t, 12000, 1000, 6)
	first.sessionID = sessionID
	failing := &failingCompressor{}
	first.SetCompressor(failing)
	_, err := first.AutoCompact(context.Background(), 65)
	require.NoError(t, err)
	require.Positive(t, failing.calls, "the first turn tried and failed")
	tried := failing.calls

	// The next turn loads the same session into a brand-new memory.
	second := proseMemory(t, 12000, 1000, 6)
	second.sessionID = sessionID
	second.SetCompressor(failing)
	_, err = second.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.Equal(t, tried, failing.calls, "no repeat attempt within the turn gap")
}

// A reset deletes the history and restarts turn numbering at 1, so a record
// from the old numbering (turn 100) must not suppress the new conversation.
func TestAutoCompact_ResetRestartsTheCooldown(t *testing.T) {
	const sessionID = "sess-cooldown-reset"
	t.Cleanup(func() {
		autoCompactAttempts.mu.Lock()
		delete(autoCompactAttempts.turns, sessionID)
		autoCompactAttempts.mu.Unlock()
	})
	autoCompactAttempts.mu.Lock()
	autoCompactAttempts.turns[sessionID] = 100
	autoCompactAttempts.mu.Unlock()

	// After the reset and a reload the same session is at turn 6 again.
	sm := proseMemory(t, 12000, 1000, 6)
	sm.sessionID = sessionID
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.True(t, res.TurnsFolded, "the new conversation is not held back by the old numbering")
	assert.NotEmpty(t, comp.input)
}

// Folds are batched: one summary call covers at least autoCompactMinTurnGap
// turns, a limit derived from the stored rows, so it survives a restart too.
func TestAutoCompact_FoldsOnlyOnceEnoughTurnsAreEligible(t *testing.T) {
	// 4 turns, newest 2 kept: only turns 1-2 are eligible, below the batch size.
	sm := proseMemory(t, 12000, 1000, 4)
	comp := &recordingCompressor{}
	sm.SetCompressor(comp)

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.False(t, res.TurnsFolded)
	assert.Empty(t, comp.input, "too few turns to be worth a summary call yet")
}
