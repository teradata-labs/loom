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
package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
)

// evictTokenCounter is a trivial token counter for the eviction integration test.
type evictTokenCounter struct{}

func (evictTokenCounter) CountTokens(text string) int { return len(text) / 4 }

// TestGraphMemoryStore_EvictStaleMemories_Postgres verifies, against a real
// PostgreSQL instance, that the cross-tenant eviction sweep works THROUGH the
// forced per-user RLS on graph_memories: it decays and soft-evicts stale rows
// (bypassing RLS as admin maintenance) while a high-salience row survives and
// stays recallable under the owning user's RLS context.
//
// Runs in the ordinary gate: CI sets TEST_POSTGRES_URL; a local run without one
// skips. This is the proof the feature works on the deployed Postgres backend,
// not only on SQLite.
func TestGraphMemoryStore_EvictStaleMemories_Postgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL store test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "failed to connect to PostgreSQL")
	t.Cleanup(pool.Close)

	migrator, err := NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx), "failed to run migrations")

	store := NewGraphMemoryStore(pool, evictTokenCounter{}, observability.NewNoOpTracer())

	userID := fmt.Sprintf("evict-user-%d", time.Now().UnixNano())
	agentID := fmt.Sprintf("evict-agent-%d", time.Now().UnixNano())
	userCtx := ContextWithUserID(ctx, userID)
	t.Cleanup(func() {
		// Admin cleanup (bypasses RLS via raw pool connection is not guaranteed,
		// so scope by our unique agent_id to avoid touching other rows).
		_, _ = pool.Exec(context.Background(), "DELETE FROM graph_memories WHERE agent_id = $1", agentID)
	})

	// Insert under the user's RLS context so user_id is populated from context.
	stale, err := store.Remember(userCtx, &memory.Memory{
		AgentID: agentID, Content: "stale failure lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.3,
	})
	require.NoError(t, err)
	_, err = store.Remember(userCtx, &memory.Memory{
		AgentID: agentID, Content: "valuable failure lesson", MemoryType: memory.MemoryTypeFact, Salience: 0.9,
	})
	require.NoError(t, err)

	// Both recallable before eviction (RLS-scoped read under the owning user).
	before, err := store.Recall(userCtx, memory.RecallOpts{AgentID: agentID, MinSalience: 0.01, Limit: 100})
	require.NoError(t, err)
	require.Len(t, before, 2)

	// Cross-tenant admin eviction (no user context): floor 0.6 makes the 0.3 row
	// eligible, high MaxAccessCount keys purely on salience, MinAge/MaxIdle off.
	n, err := store.EvictStaleMemories(ctx, memory.EvictionOptions{
		AgentID:        agentID,
		MinSalience:    0.6,
		MaxIdle:        0,
		MinAge:         0,
		MaxAccessCount: 1_000_000,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the low-salience row should be evicted")

	// The stale row is gone from recall; the valuable one remains.
	after, err := store.Recall(userCtx, memory.RecallOpts{AgentID: agentID, MinSalience: 0.01, Limit: 100})
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, "valuable failure lesson", after[0].Content)
	assert.NotEqual(t, stale.ID, after[0].ID)
}
