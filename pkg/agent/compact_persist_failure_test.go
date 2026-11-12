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

// brokenWriteStore is a session store whose compaction writes fail; any other
// call would panic on the nil embedded interface, which these tests never make.
type brokenWriteStore struct {
	SessionStorage
	err error
}

func (s *brokenWriteStore) MarkEvicted(context.Context, string, []int64) error { return s.err }
func (s *brokenWriteStore) FoldMessages(context.Context, string, []int64, int, string) error {
	return s.err
}

// A manual compaction that could not write to the store must say so. Relief
// logs the failure and carries on, which for a requested compaction would read
// as an ordinary no-op after the summary call was already paid for.
func TestCompact_ReportsAFailedWriteInsteadOfANoOp(t *testing.T) {
	dbDown := errors.New("connection refused")

	t.Run("fold write fails", func(t *testing.T) {
		sm := proseMemory(t, 200000, 20000, 6)
		sm.SetSessionStore(&brokenWriteStore{err: dbDown}, "sess-broken-fold")
		sm.SetCompressor(&recordingCompressor{})

		res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.ErrorIs(t, err, ErrCompactionNotSaved)
		require.ErrorIs(t, err, dbDown, "the cause is kept for the log")
		assert.False(t, res.Compacted)
		assert.Len(t, unfoldedTurns(sm), 6, "nothing was dropped from context")
		assert.Equal(t, 0, sm.summary.n)
		assert.NoError(t, sm.compactPersistErr, "the marker does not leak into relief")
	})

	t.Run("evict and fold writes both fail", func(t *testing.T) {
		sm := toolTurnMemory(t, 200000, 20000, 6)
		sm.SetSessionStore(&brokenWriteStore{err: dbDown}, "sess-broken-both")
		sm.SetCompressor(&recordingCompressor{})

		res, err := sm.Compact(context.Background(), CompactOptions{KeepTurns: 2})

		require.ErrorIs(t, err, ErrCompactionNotSaved)
		assert.False(t, res.Compacted)
		assert.Len(t, unfoldedTurns(sm), 6)
	})
}

// Automatic compaction stays best-effort: a failed write is logged and the turn
// that just succeeded is untouched.
func TestAutoCompact_StaysBestEffortWhenTheWriteFails(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 6)
	sm.SetSessionStore(&brokenWriteStore{err: errors.New("connection refused")}, "sess-auto-broken")
	t.Cleanup(func() {
		autoCompactAttempts.mu.Lock()
		delete(autoCompactAttempts.turns, "sess-auto-broken")
		autoCompactAttempts.mu.Unlock()
	})
	sm.SetCompressor(&recordingCompressor{})

	res, err := sm.AutoCompact(context.Background(), 65)

	require.NoError(t, err)
	assert.False(t, res.Compacted)
	assert.Len(t, unfoldedTurns(sm), 6)
}
