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

package e2e

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
)

// mockTokenCounter counts 1 token per 4 chars.
type mockTokenCounter struct{}

func (m *mockTokenCounter) CountTokens(text string) int { return len(text) / 4 }

func newE2EStore(t *testing.T) memory.GraphMemoryStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "e2e_graph_memory.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	migrator, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(context.Background()))

	return sqlite.NewGraphMemoryStore(db, &mockTokenCounter{}, observability.NewNoOpTracer())
}

// TestGraphMemory_FullLifecycle: remember -> recall -> supersede -> recall (superseded excluded)
func TestGraphMemory_FullLifecycle(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	// Step 1: Remember
	original, err := store.Remember(ctx, &memory.Memory{
		AgentID:    "e2e-agent",
		Content:    "User lives in New Orleans and works at Tulane University",
		Summary:    "Lives in NOLA, works at Tulane",
		MemoryType: memory.MemoryTypeFact,
		Salience:   0.8,
		Tags:       []string{"location", "work"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, original.ID)
	assert.Greater(t, original.TokenCount, 0, "token count should be precomputed")

	// Step 2: Recall
	recalled, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "e2e-agent",
		Query:   "New Orleans",
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, recalled, 1)
	assert.Equal(t, original.ID, recalled[0].ID)

	// Step 3: Supersede
	replacement, err := store.Supersede(ctx, original.ID, &memory.Memory{
		AgentID:    "e2e-agent",
		Content:    "User moved to Austin and now works at UT Austin",
		Summary:    "Lives in Austin, works at UT",
		MemoryType: memory.MemoryTypeFact,
		Tags:       []string{"location", "work"},
	})
	require.NoError(t, err)
	assert.NotEqual(t, original.ID, replacement.ID)
	assert.InDelta(t, 0.8, replacement.Salience, 0.01, "should inherit salience")

	// Step 4: Recall should exclude superseded
	recalled, err = store.Recall(ctx, memory.RecallOpts{
		AgentID: "e2e-agent",
		Query:   "lives OR works",
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, recalled, 1)
	assert.Equal(t, replacement.ID, recalled[0].ID, "should only return replacement")

	// Lineage
	lineage, err := store.GetLineage(ctx, replacement.ID)
	require.NoError(t, err)
	require.Len(t, lineage, 1)
	assert.Equal(t, memory.LineageSupersedes, lineage[0].RelationType)
}

// TestGraphMemory_Consolidation: remember 4 -> consolidate -> verify salience
func TestGraphMemory_Consolidation(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	ids := make([]string, 4)
	contents := []string{
		"Family movie night happens every Friday",
		"Family prefers musicals and animated films",
		"No horror movies for family viewing",
		"Popcorn is mandatory for movie night",
	}
	for i, content := range contents {
		m, err := store.Remember(ctx, &memory.Memory{
			AgentID:    "e2e-agent",
			Content:    content,
			MemoryType: memory.MemoryTypeFact,
			Salience:   0.6,
		})
		require.NoError(t, err)
		ids[i] = m.ID
	}

	// Consolidate
	consolidated, err := store.Consolidate(ctx, ids, &memory.Memory{
		AgentID: "e2e-agent",
		Content: "Family movie night rules: Fridays, musicals/animated preferred, no horror, popcorn mandatory",
		Summary: "Movie night rules",
		Tags:    []string{"family", "movies"},
	})
	require.NoError(t, err)
	assert.Equal(t, memory.MemoryTypeConsolidation, consolidated.MemoryType)
	assert.InDelta(t, 0.6, consolidated.Salience, 0.01, "max of originals")

	// Check originals have halved salience
	for _, id := range ids {
		m, err := store.GetMemory(ctx, "e2e-agent", id)
		require.NoError(t, err)
		assert.InDelta(t, 0.3, m.Salience, 0.01, "should be halved")
	}

	// Lineage
	lineage, err := store.GetLineage(ctx, consolidated.ID)
	require.NoError(t, err)
	assert.Len(t, lineage, 4)
}

// TestGraphMemory_BudgetEnforcement: content -> summary -> skip degradation
func TestGraphMemory_BudgetEnforcement(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	// Create entity
	entity, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "e2e-agent", Name: "alice", EntityType: "person",
	})
	require.NoError(t, err)

	// Create memories with known token counts
	for i := 0; i < 5; i++ {
		_, err := store.Remember(ctx, &memory.Memory{
			AgentID:    "e2e-agent",
			Content:    "This is a longer memory content that should take up some tokens in the budget allocation test scenario",
			Summary:    "Short summary",
			MemoryType: memory.MemoryTypeFact,
			Salience:   0.8,
			EntityIDs:  []string{entity.ID},
		})
		require.NoError(t, err)
	}

	// ContextFor with very tight budget
	recall, err := store.ContextFor(ctx, memory.ContextForOpts{
		AgentID:    "e2e-agent",
		EntityName: "alice",
		MaxTokens:  600, // Just enough for profile + graph + maybe 1 memory
	})
	require.NoError(t, err)
	assert.NotNil(t, recall.Entity)
	assert.LessOrEqual(t, recall.TotalTokensUsed, 600, "should respect budget")
}

// TestGraphMemory_GraphTraversal: relate entities -> neighbors correct depth
func TestGraphMemory_GraphTraversal(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	// Create a chain: A -> B -> C -> D
	entities := make([]*memory.Entity, 4)
	names := []string{"a", "b", "c", "d"}
	for i, name := range names {
		e, err := store.CreateEntity(ctx, &memory.Entity{
			AgentID: "e2e-agent", Name: name, EntityType: "concept",
		})
		require.NoError(t, err)
		entities[i] = e
	}

	for i := 0; i < 3; i++ {
		_, err := store.Relate(ctx, &memory.Edge{
			AgentID:  "e2e-agent",
			SourceID: entities[i].ID,
			TargetID: entities[i+1].ID,
			Relation: "LEADS_TO",
		})
		require.NoError(t, err)
	}

	// Depth 1: A -> B
	edges, err := store.Neighbors(ctx, entities[0].ID, "", "outbound", 1)
	require.NoError(t, err)
	assert.Len(t, edges, 1)

	// Depth 2: A -> B -> C
	edges, err = store.Neighbors(ctx, entities[0].ID, "", "outbound", 2)
	require.NoError(t, err)
	assert.Len(t, edges, 2)

	// Depth 3: A -> B -> C -> D
	edges, err = store.Neighbors(ctx, entities[0].ID, "", "outbound", 3)
	require.NoError(t, err)
	assert.Len(t, edges, 3)
}

// TestGraphMemory_SalienceDecayAndBoost: fresh > stale, accessed stays salient
func TestGraphMemory_SalienceDecayAndBoost(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	// Create a memory
	m, err := store.Remember(ctx, &memory.Memory{
		AgentID:    "e2e-agent",
		Content:    "Decayable test memory for salience",
		MemoryType: memory.MemoryTypeFact,
		Salience:   0.8,
	})
	require.NoError(t, err)

	// Decay all
	err = store.DecayAll(ctx, "e2e-agent", 0.5) // Aggressive decay for test
	require.NoError(t, err)

	// Check decayed
	decayed, err := store.GetMemory(ctx, "e2e-agent", m.ID)
	require.NoError(t, err)
	assert.InDelta(t, 0.4, decayed.Salience, 0.01, "should be halved by decay")

	// Touch to boost access tracking
	err = store.TouchMemories(ctx, []string{m.ID})
	require.NoError(t, err)

	touched, err := store.GetMemory(ctx, "e2e-agent", m.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, touched.AccessCount)
	assert.NotNil(t, touched.AccessedAt)
}

// TestGraphMemory_Forget: soft-deleted excluded
func TestGraphMemory_Forget(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	m, err := store.Remember(ctx, &memory.Memory{
		AgentID:    "e2e-agent",
		Content:    "This fact will be forgotten",
		MemoryType: memory.MemoryTypeFact,
		Salience:   0.5,
	})
	require.NoError(t, err)

	err = store.Forget(ctx, m.ID)
	require.NoError(t, err)

	// Should not appear in recall
	recalled, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "e2e-agent",
		Limit:   10,
	})
	require.NoError(t, err)
	assert.Empty(t, recalled, "forgotten memory should not appear in recall")

	// But should still be retrievable directly (soft delete)
	got, err := store.GetMemory(ctx, "e2e-agent", m.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.ExpiresAt, "should have expiration set")
}

// TestGraphMemory_MemoryImmutability: content never changes after creation
func TestGraphMemory_MemoryImmutability(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	m, err := store.Remember(ctx, &memory.Memory{
		AgentID:    "e2e-agent",
		Content:    "This content is immutable and must never change",
		MemoryType: memory.MemoryTypeFact,
		Salience:   0.5,
	})
	require.NoError(t, err)
	originalContent := m.Content

	// Touch (access tracking is the only allowed mutation)
	err = store.TouchMemories(ctx, []string{m.ID})
	require.NoError(t, err)

	// Verify content unchanged
	got, err := store.GetMemory(ctx, "e2e-agent", m.ID)
	require.NoError(t, err)
	assert.Equal(t, originalContent, got.Content, "memory content must not change")
	assert.Equal(t, 1, got.AccessCount, "access count should increment")
}

// TestGraphMemory_EntityJunctionSearch: memory linked to multiple entities findable from any
func TestGraphMemory_EntityJunctionSearch(t *testing.T) {
	store := newE2EStore(t)
	ctx := context.Background()

	// Create 3 entities
	e1, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "e2e-agent", Name: "alice", EntityType: "person"})
	e2, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "e2e-agent", Name: "bob", EntityType: "person"})
	e3, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "e2e-agent", Name: "project-x", EntityType: "project"})

	// Create memory linked to all three
	m, err := store.Remember(ctx, &memory.Memory{
		AgentID:    "e2e-agent",
		Content:    "Alice and Bob are collaborating on Project X",
		MemoryType: memory.MemoryTypeExperience,
		Salience:   0.7,
		EntityIDs:  []string{e1.ID, e2.ID, e3.ID},
	})
	require.NoError(t, err)

	// Should be findable from any entity
	for _, eid := range []string{e1.ID, e2.ID, e3.ID} {
		recalled, err := store.Recall(ctx, memory.RecallOpts{
			AgentID:   "e2e-agent",
			EntityIDs: []string{eid},
			Limit:     10,
		})
		require.NoError(t, err)
		require.Len(t, recalled, 1, "should find memory from entity %s", eid)
		assert.Equal(t, m.ID, recalled[0].ID)
	}
}
