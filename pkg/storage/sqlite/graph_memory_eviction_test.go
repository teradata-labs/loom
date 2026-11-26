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
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
)

// newEvictionTestStore returns a migrated store plus its db handle so tests can
// assert directly on the live (non-expired) row count.
func newEvictionTestStore(t *testing.T) (*GraphMemoryStore, *sql.DB) {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	_, err := db.Exec("PRAGMA busy_timeout = 5000")
	require.NoError(t, err)
	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx))
	return NewGraphMemoryStore(db, &mockTokenCounter{}, observability.NewNoOpTracer()), db
}

// liveCount returns the number of non-expired memories for an agent.
func liveCount(t *testing.T, db *sql.DB, agentID string) int {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM graph_memories
		 WHERE agent_id = ? AND (expires_at IS NULL OR expires_at > datetime('now'))`,
		agentID,
	).Scan(&n)
	require.NoError(t, err)
	return n
}

func TestGraphMemoryStore_EvictStaleMemories_Predicate(t *testing.T) {
	store, db := newEvictionTestStore(t)
	ctx := context.Background()

	// Low-salience, never-reused row — the prime eviction candidate.
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "stale lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.05,
	})
	require.NoError(t, err)
	// High-salience row — must survive.
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "valuable lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.9,
	})
	require.NoError(t, err)

	require.Equal(t, 2, liveCount(t, db, "agent-1"))

	n, err := store.EvictStaleMemories(ctx, memory.EvictionOptions{
		AgentID:        "agent-1",
		MinSalience:    0.1,
		MaxIdle:        0,
		MinAge:         0,
		MaxAccessCount: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the low-salience row should be evicted")
	assert.Equal(t, 1, liveCount(t, db, "agent-1"), "high-salience row should survive")
}

func TestGraphMemoryStore_EvictStaleMemories_ProtectsReused(t *testing.T) {
	store, db := newEvictionTestStore(t)
	ctx := context.Background()

	m, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "reused lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.05,
	})
	require.NoError(t, err)
	// Simulate reuse: access_count becomes 1.
	require.NoError(t, store.TouchMemories(ctx, []string{m.ID}))

	n, err := store.EvictStaleMemories(ctx, memory.EvictionOptions{
		AgentID:        "agent-1",
		MinSalience:    0.1,
		MaxIdle:        0,
		MinAge:         0,
		MaxAccessCount: 0, // only never-reused rows are eligible
	})
	require.NoError(t, err)
	assert.Equal(t, 0, n, "reused row should be protected by MaxAccessCount")
	assert.Equal(t, 1, liveCount(t, db, "agent-1"))
}

func TestGraphMemoryStore_EvictStaleMemories_MinAgeProtectsFresh(t *testing.T) {
	store, db := newEvictionTestStore(t)
	ctx := context.Background()

	_, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "fresh lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.05,
	})
	require.NoError(t, err)

	n, err := store.EvictStaleMemories(ctx, memory.EvictionOptions{
		AgentID:        "agent-1",
		MinSalience:    0.1,
		MaxIdle:        0,
		MinAge:         3600 * 1e9, // 1h; row is seconds old, so protected
		MaxAccessCount: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, n, "fresh row should be protected by MinAge")
	assert.Equal(t, 1, liveCount(t, db, "agent-1"))
}

func TestGraphMemoryStore_EvictStaleMemories_RowCap(t *testing.T) {
	store, db := newEvictionTestStore(t)
	ctx := context.Background()

	// Five rows with distinct salience; predicate disabled (MinSalience 0).
	saliences := []float64{0.9, 0.8, 0.7, 0.6, 0.5}
	for i, s := range saliences {
		_, err := store.Remember(ctx, &memory.Memory{
			AgentID: "agent-1", Content: "lesson", MemoryType: memory.MemoryTypeFact, Salience: s,
		})
		require.NoError(t, err, "insert %d", i)
	}
	require.Equal(t, 5, liveCount(t, db, "agent-1"))

	n, err := store.EvictStaleMemories(ctx, memory.EvictionOptions{
		AgentID:     "agent-1",
		MinSalience: 0, // predicate matches nothing
		MaxIdle:     0,
		MinAge:      0,
		MaxRows:     2, // keep only the 2 highest-salience rows
	})
	require.NoError(t, err)
	assert.Equal(t, 3, n, "three lowest-ranked rows should be evicted")
	assert.Equal(t, 2, liveCount(t, db, "agent-1"))
}

func TestGraphMemoryStore_EvictStaleMemories_AllAgents(t *testing.T) {
	store, db := newEvictionTestStore(t)
	ctx := context.Background()

	for _, agent := range []string{"agent-1", "agent-2"} {
		_, err := store.Remember(ctx, &memory.Memory{
			AgentID: agent, Content: "stale", MemoryType: memory.MemoryTypeFact, Salience: 0.05,
		})
		require.NoError(t, err)
	}

	n, err := store.EvictStaleMemories(ctx, memory.EvictionOptions{
		AgentID:        "", // all agents
		MinSalience:    0.1,
		MaxIdle:        0,
		MinAge:         0,
		MaxAccessCount: 0,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, n, "stale rows across both agents should be evicted")
	assert.Equal(t, 0, liveCount(t, db, "agent-1"))
	assert.Equal(t, 0, liveCount(t, db, "agent-2"))
}

func TestGraphMemoryStore_DecayAll_AllAgents(t *testing.T) {
	store, _ := newEvictionTestStore(t)
	ctx := context.Background()

	ids := map[string]string{}
	for _, agent := range []string{"agent-1", "agent-2"} {
		m, err := store.Remember(ctx, &memory.Memory{
			AgentID: agent, Content: "fact", MemoryType: memory.MemoryTypeFact, Salience: 0.8,
		})
		require.NoError(t, err)
		ids[agent] = m.ID
	}

	// Empty agentID decays every agent.
	require.NoError(t, store.DecayAll(ctx, "", 0.5))

	for agent, id := range ids {
		m, err := store.GetMemory(ctx, agent, id)
		require.NoError(t, err)
		assert.InDelta(t, 0.4, m.Salience, 0.01, "agent %s salience should be halved", agent)
	}
}
