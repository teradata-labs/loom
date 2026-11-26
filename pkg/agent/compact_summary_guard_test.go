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
	t.Run("failing compressor: reports unavailable, history intact", func(t *testing.T) {
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

}
