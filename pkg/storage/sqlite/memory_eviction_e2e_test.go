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

//go:build fts5

package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/storage"
	"go.uber.org/zap/zaptest"
)

// recallContents returns the content strings currently recallable for an agent.
func recallContents(t *testing.T, store *GraphMemoryStore, agentID string) []string {
	t.Helper()
	mems, err := store.Recall(context.Background(), memory.RecallOpts{
		AgentID:     agentID,
		MinSalience: 0.01, // below both rows so recall reflects only eviction
		Limit:       100,
	})
	require.NoError(t, err)
	out := make([]string, len(mems))
	for i, m := range mems {
		out[i] = m.Content
	}
	return out
}

// TestMemoryEvictionSweep_EndToEnd wires the REAL SQLite graph-memory store
// through the REAL background eviction goroutine and proves that a stale,
// never-reused, low-salience lesson disappears from recall on a sweep while a
// high-salience lesson survives. This is the demoable end-to-end path that
// cmd/looms wires at server boot.
func TestMemoryEvictionSweep_EndToEnd(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// A stale lesson: low salience, never reused.
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID: "fr-agent", Content: "stale failure lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.3,
	})
	require.NoError(t, err)
	// A valuable lesson: high salience.
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "fr-agent", Content: "valuable failure lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.9,
	})
	require.NoError(t, err)

	// Both are recallable before the sweep runs.
	before := recallContents(t, store, "fr-agent")
	require.ElementsMatch(t, []string{"stale failure lesson", "valuable failure lesson"}, before)

	// Start the real background sweeper. Eviction floor 0.6 makes the 0.3 row
	// eligible and keeps the 0.9 row. MinAge/MaxIdle disabled so freshly created
	// rows qualify. MaxAccessCount is high because Recall touches rows (bumping
	// access_count), so this demo keys purely on the salience floor.
	sweeper := storage.StartMemoryEvictionSweep(store, storage.MemoryEvictionConfig{
		Interval:       10 * time.Millisecond,
		DecayFactor:    0, // skip decay so the floor test is deterministic
		MinSalience:    0.6,
		MaxIdle:        0,
		MinAge:         0,
		MaxAccessCount: 1_000_000,
		MaxRows:        0,
	}, zaptest.NewLogger(t))
	defer sweeper.Stop()

	// The stale lesson should vanish from recall; the valuable one remains.
	require.Eventually(t, func() bool {
		got := recallContents(t, store, "fr-agent")
		return len(got) == 1 && got[0] == "valuable failure lesson"
	}, 3*time.Second, 20*time.Millisecond, "stale lesson should be evicted from recall")

	after := recallContents(t, store, "fr-agent")
	assert.Equal(t, []string{"valuable failure lesson"}, after)
}
