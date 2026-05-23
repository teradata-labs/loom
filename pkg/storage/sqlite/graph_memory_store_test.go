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
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
)

// mockTokenCounter is a simple token counter that estimates 1 token per 4 chars.
type mockTokenCounter struct{}

func (m *mockTokenCounter) CountTokens(text string) int {
	return len(text) / 4
}

// newTestGraphMemoryStore creates a migrated test store.
func newTestGraphMemoryStore(t *testing.T) *GraphMemoryStore {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()

	// Set busy_timeout for concurrent access tests (matches NewMigrator pattern).
	_, err := db.Exec("PRAGMA busy_timeout = 5000")
	require.NoError(t, err)

	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx))

	return NewGraphMemoryStore(db, &mockTokenCounter{}, observability.NewNoOpTracer())
}

// =============================================================================
// Entity CRUD Tests
// =============================================================================

func TestGraphMemoryStore_CreateEntity(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	entity := &memory.Entity{
		AgentID:        "agent-1",
		Name:           "test-person",
		EntityType:     "person",
		PropertiesJSON: `{"role":"engineer"}`,
		Owner:          "owner-1",
	}

	created, err := store.CreateEntity(ctx, entity)
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "test-person", created.Name)
	assert.False(t, created.CreatedAt.IsZero())
}

func TestGraphMemoryStore_CreateEntity_DuplicateName(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	entity := &memory.Entity{AgentID: "agent-1", Name: "dup", EntityType: "concept"}
	_, err := store.CreateEntity(ctx, entity)
	require.NoError(t, err)

	// Same agent_id + name should fail.
	entity2 := &memory.Entity{AgentID: "agent-1", Name: "dup", EntityType: "concept"}
	_, err = store.CreateEntity(ctx, entity2)
	assert.Error(t, err)
}

func TestGraphMemoryStore_GetEntity(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "findme", EntityType: "tool",
		PropertiesJSON: `{"desc":"test"}`,
	})
	require.NoError(t, err)

	found, err := store.GetEntity(ctx, "agent-1", "findme")
	require.NoError(t, err)
	assert.Equal(t, "findme", found.Name)
	assert.Equal(t, "tool", found.EntityType)
	assert.Equal(t, `{"desc":"test"}`, found.PropertiesJSON)
}

func TestGraphMemoryStore_UpdateEntity(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "mutable", EntityType: "concept",
		PropertiesJSON: `{"v":1}`,
	})
	require.NoError(t, err)

	updated, err := store.UpdateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "mutable", EntityType: "project",
		PropertiesJSON: `{"v":2}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "project", updated.EntityType)
	assert.Equal(t, `{"v":2}`, updated.PropertiesJSON)
}

func TestGraphMemoryStore_ListEntities(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	for _, name := range []string{"a", "b", "c"} {
		_, err := store.CreateEntity(ctx, &memory.Entity{
			AgentID: "agent-1", Name: name, EntityType: "concept",
		})
		require.NoError(t, err)
	}
	_, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "d", EntityType: "person",
	})
	require.NoError(t, err)

	// List all.
	all, total, err := store.ListEntities(ctx, "agent-1", "", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 4, total)
	assert.Len(t, all, 4)

	// List by type.
	concepts, total, err := store.ListEntities(ctx, "agent-1", "concept", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, concepts, 3)
}

func TestGraphMemoryStore_DeleteEntity_CascadesEdges(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	e1, err := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "src", EntityType: "concept"})
	require.NoError(t, err)
	e2, err := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "tgt", EntityType: "concept"})
	require.NoError(t, err)

	_, err = store.Relate(ctx, &memory.Edge{
		AgentID: "agent-1", SourceID: e1.ID, TargetID: e2.ID, Relation: "USES",
	})
	require.NoError(t, err)

	// Delete source entity — edge should cascade.
	err = store.DeleteEntity(ctx, "agent-1", "src")
	require.NoError(t, err)

	edges, err := store.ListEdgesTo(ctx, e2.ID)
	require.NoError(t, err)
	assert.Empty(t, edges, "edges should be cascade-deleted")
}

// =============================================================================
// Edge CRUD Tests
// =============================================================================

func TestGraphMemoryStore_Relate_Upsert(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	e1, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "a", EntityType: "concept"})
	e2, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "b", EntityType: "concept"})

	// First relate.
	edge1, err := store.Relate(ctx, &memory.Edge{
		AgentID: "agent-1", SourceID: e1.ID, TargetID: e2.ID,
		Relation: "USES", PropertiesJSON: `{"v":1}`,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, edge1.ID)

	// Upsert same triple with different properties.
	edge2, err := store.Relate(ctx, &memory.Edge{
		AgentID: "agent-1", SourceID: e1.ID, TargetID: e2.ID,
		Relation: "USES", PropertiesJSON: `{"v":2}`,
	})
	require.NoError(t, err)

	// Should be same edge (upserted).
	edges, err := store.ListEdgesFrom(ctx, e1.ID)
	require.NoError(t, err)
	assert.Len(t, edges, 1)
	assert.Equal(t, `{"v":2}`, edges[0].PropertiesJSON)
	_ = edge2
}

func TestGraphMemoryStore_Neighbors_Outbound(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// A -> B -> C
	a, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "a", EntityType: "concept"})
	b, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "b", EntityType: "concept"})
	c, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "c", EntityType: "concept"})

	_, err := store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: a.ID, TargetID: b.ID, Relation: "KNOWS"})
	require.NoError(t, err)
	_, err = store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: b.ID, TargetID: c.ID, Relation: "KNOWS"})
	require.NoError(t, err)

	// Depth 1: only A->B
	edges, err := store.Neighbors(ctx, a.ID, "", "outbound", 1)
	require.NoError(t, err)
	assert.Len(t, edges, 1)

	// Depth 2: A->B and B->C
	edges, err = store.Neighbors(ctx, a.ID, "", "outbound", 2)
	require.NoError(t, err)
	assert.Len(t, edges, 2)
}

func TestGraphMemoryStore_Unrelate(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	e1, err := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "src", EntityType: "concept"})
	require.NoError(t, err)
	e2, err := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "tgt", EntityType: "concept"})
	require.NoError(t, err)

	_, err = store.Relate(ctx, &memory.Edge{
		AgentID: "agent-1", SourceID: e1.ID, TargetID: e2.ID, Relation: "USES",
	})
	require.NoError(t, err)

	// Verify edge exists.
	edges, err := store.ListEdgesFrom(ctx, e1.ID)
	require.NoError(t, err)
	assert.Len(t, edges, 1)

	// Unrelate.
	err = store.Unrelate(ctx, e1.ID, e2.ID, "USES")
	require.NoError(t, err)

	// Verify edge removed.
	edges, err = store.ListEdgesFrom(ctx, e1.ID)
	require.NoError(t, err)
	assert.Empty(t, edges, "edge should be removed after Unrelate")
}

func TestGraphMemoryStore_Neighbors_Inbound(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// A -> B -> C (query inbound from C should find B->C, then A->B at depth 2)
	a, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "a", EntityType: "concept"})
	b, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "b", EntityType: "concept"})
	c, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "c", EntityType: "concept"})

	_, err := store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: a.ID, TargetID: b.ID, Relation: "KNOWS"})
	require.NoError(t, err)
	_, err = store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: b.ID, TargetID: c.ID, Relation: "KNOWS"})
	require.NoError(t, err)

	// Inbound depth 1 from C: only B->C
	edges, err := store.Neighbors(ctx, c.ID, "", "inbound", 1)
	require.NoError(t, err)
	assert.Len(t, edges, 1)
	assert.Equal(t, b.ID, edges[0].SourceID)

	// Inbound depth 2 from C: B->C and A->B
	edges, err = store.Neighbors(ctx, c.ID, "", "inbound", 2)
	require.NoError(t, err)
	assert.Len(t, edges, 2)
}

func TestGraphMemoryStore_Neighbors_Both(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// A -> B -> C (query "both" from B should find both A->B and B->C)
	a, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "a", EntityType: "concept"})
	b, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "b", EntityType: "concept"})
	c, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "c", EntityType: "concept"})

	_, err := store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: a.ID, TargetID: b.ID, Relation: "KNOWS"})
	require.NoError(t, err)
	_, err = store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: b.ID, TargetID: c.ID, Relation: "KNOWS"})
	require.NoError(t, err)

	// Both from B: should find A->B and B->C
	edges, err := store.Neighbors(ctx, b.ID, "", "both", 1)
	require.NoError(t, err)
	assert.Len(t, edges, 2)
}

func TestGraphMemoryStore_Neighbors_WithRelationFilter(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	a, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "a", EntityType: "concept"})
	b, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "b", EntityType: "concept"})
	c, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "c", EntityType: "concept"})

	_, err := store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: a.ID, TargetID: b.ID, Relation: "KNOWS"})
	require.NoError(t, err)
	_, err = store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: a.ID, TargetID: c.ID, Relation: "USES"})
	require.NoError(t, err)

	// Filter to KNOWS only.
	edges, err := store.Neighbors(ctx, a.ID, "KNOWS", "outbound", 1)
	require.NoError(t, err)
	assert.Len(t, edges, 1)
	assert.Equal(t, "KNOWS", edges[0].Relation)

	// Filter to USES only.
	edges, err = store.Neighbors(ctx, a.ID, "USES", "outbound", 1)
	require.NoError(t, err)
	assert.Len(t, edges, 1)
	assert.Equal(t, "USES", edges[0].Relation)

	// No filter returns both.
	edges, err = store.Neighbors(ctx, a.ID, "", "outbound", 1)
	require.NoError(t, err)
	assert.Len(t, edges, 2)
}

// =============================================================================
// Memory Tests
// =============================================================================

func TestGraphMemoryStore_Remember_Recall(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	mem := &memory.Memory{
		AgentID:    "agent-1",
		Content:    "The user prefers dark mode for all applications",
		Summary:    "User prefers dark mode",
		MemoryType: memory.MemoryTypePreference,
		Source:     "conversation",
		Tags:       []string{"ui", "preference"},
		Salience:   0.8,
	}

	created, err := store.Remember(ctx, mem)
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Greater(t, created.TokenCount, 0)
	assert.Greater(t, created.SummaryTokenCount, 0)

	// Recall by FTS query.
	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "agent-1",
		Query:   "dark mode",
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	assert.Equal(t, created.ID, memories[0].ID)
}

func TestGraphMemoryStore_Remember_WithEntityLinks(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Create entities.
	e1, err := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "user1", EntityType: "person"})
	require.NoError(t, err)
	e2, err := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "project1", EntityType: "project"})
	require.NoError(t, err)

	// Remember with entity links.
	mem := &memory.Memory{
		AgentID:    "agent-1",
		Content:    "User1 started working on project1 today",
		MemoryType: memory.MemoryTypeExperience,
		Salience:   0.7,
		EntityIDs:  []string{e1.ID, e2.ID},
	}

	created, err := store.Remember(ctx, mem)
	require.NoError(t, err)

	// Recall scoped to e1 entity.
	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID:   "agent-1",
		EntityIDs: []string{e1.ID},
		Limit:     10,
	})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	assert.Equal(t, created.ID, memories[0].ID)

	// Recall scoped to e2 entity also finds it.
	memories, err = store.Recall(ctx, memory.RecallOpts{
		AgentID:   "agent-1",
		EntityIDs: []string{e2.ID},
		Limit:     10,
	})
	require.NoError(t, err)
	require.Len(t, memories, 1)
}

func TestGraphMemoryStore_Forget(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	created, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Forgettable fact", MemoryType: memory.MemoryTypeFact, Salience: 0.5,
	})
	require.NoError(t, err)

	err = store.Forget(ctx, created.ID)
	require.NoError(t, err)

	// Should be excluded from recall.
	memories, err := store.Recall(ctx, memory.RecallOpts{AgentID: "agent-1", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, memories)
}

func TestGraphMemoryStore_Supersede(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Create original memory.
	original, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "User lives in New Orleans", MemoryType: memory.MemoryTypeFact, Salience: 0.8,
	})
	require.NoError(t, err)

	// Supersede it.
	replacement, err := store.Supersede(ctx, original.ID, &memory.Memory{
		AgentID: "agent-1", Content: "User moved to Austin", MemoryType: memory.MemoryTypeFact,
	})
	require.NoError(t, err)
	assert.NotEqual(t, original.ID, replacement.ID)

	// Replacement should inherit salience.
	assert.InDelta(t, 0.8, replacement.Salience, 0.01)

	// Recall should return only the replacement.
	memories, err := store.Recall(ctx, memory.RecallOpts{AgentID: "agent-1", Query: "lives OR moved", Limit: 10})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	assert.Equal(t, replacement.ID, memories[0].ID)

	// Lineage should show SUPERSEDES.
	lineage, err := store.GetLineage(ctx, replacement.ID)
	require.NoError(t, err)
	require.Len(t, lineage, 1)
	assert.Equal(t, memory.LineageSupersedes, lineage[0].RelationType)
	assert.Equal(t, original.ID, lineage[0].OldMemoryID)
}

func TestGraphMemoryStore_Consolidate(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Create 3 related memories.
	ids := make([]string, 3)
	for i, content := range []string{
		"Family movie night is Fridays",
		"Family prefers musicals",
		"No horror movies for family",
	} {
		m, err := store.Remember(ctx, &memory.Memory{
			AgentID: "agent-1", Content: content, MemoryType: memory.MemoryTypeFact, Salience: 0.6,
		})
		require.NoError(t, err)
		ids[i] = m.ID
	}

	// Consolidate.
	consolidated, err := store.Consolidate(ctx, ids, &memory.Memory{
		AgentID: "agent-1",
		Content: "Family movie night: Fridays, prefer musicals, no horror",
		Summary: "Family movie rules",
		Tags:    []string{"family", "movies"},
	})
	require.NoError(t, err)
	assert.Equal(t, memory.MemoryTypeConsolidation, consolidated.MemoryType)
	assert.InDelta(t, 0.6, consolidated.Salience, 0.01) // max of originals

	// Original memories should have halved salience.
	for _, id := range ids {
		m, err := store.GetMemory(ctx, "agent-1", id)
		require.NoError(t, err)
		assert.InDelta(t, 0.3, m.Salience, 0.01, "original should have halved salience")
	}

	// Lineage should show CONSOLIDATES for each.
	lineage, err := store.GetLineage(ctx, consolidated.ID)
	require.NoError(t, err)
	assert.Len(t, lineage, 3)
	for _, l := range lineage {
		assert.Equal(t, memory.LineageConsolidates, l.RelationType)
	}
}

func TestGraphMemoryStore_Consolidate_CustomDecay(t *testing.T) {
	// Use a custom consolidation decay (0.25 instead of default 0.5).
	db := newTestDB(t)
	ctx := context.Background()
	_, err := db.Exec("PRAGMA busy_timeout = 5000")
	require.NoError(t, err)
	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx))

	cfg := memory.DefaultSalienceConfig()
	cfg.ConsolidationDecay = 0.25
	store := NewGraphMemoryStore(db, &mockTokenCounter{}, observability.NewNoOpTracer(), WithSalienceConfig(cfg))

	// Create 2 memories with salience 0.8.
	ids := make([]string, 2)
	for i, content := range []string{"Fact A", "Fact B"} {
		m, err := store.Remember(ctx, &memory.Memory{
			AgentID: "agent-1", Content: content, MemoryType: memory.MemoryTypeFact, Salience: 0.8,
		})
		require.NoError(t, err)
		ids[i] = m.ID
	}

	// Consolidate.
	consolidated, err := store.Consolidate(ctx, ids, &memory.Memory{
		AgentID: "agent-1", Content: "Combined A+B",
	})
	require.NoError(t, err)
	assert.InDelta(t, 0.8, consolidated.Salience, 0.01) // max of originals

	// Source salience should be 0.8 * 0.25 = 0.2 (not the default 0.4).
	for _, id := range ids {
		m, err := store.GetMemory(ctx, "agent-1", id)
		require.NoError(t, err)
		assert.InDelta(t, 0.2, m.Salience, 0.01, "should use custom consolidation decay 0.25")
	}
}

func TestGraphMemoryStore_TouchMemories(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	created, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Touch test memory", MemoryType: memory.MemoryTypeFact, Salience: 0.5,
	})
	require.NoError(t, err)

	err = store.TouchMemories(ctx, []string{created.ID})
	require.NoError(t, err)

	// Verify access_count incremented.
	mem, err := store.GetMemory(ctx, "agent-1", created.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, mem.AccessCount)
	assert.NotNil(t, mem.AccessedAt)
}

func TestGraphMemoryStore_DecayAll(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Decayable memory", MemoryType: memory.MemoryTypeFact, Salience: 0.8,
	})
	require.NoError(t, err)

	err = store.DecayAll(ctx, "agent-1", 0.5)
	require.NoError(t, err)

	// Verify salience decayed.
	memories, err := store.Recall(ctx, memory.RecallOpts{AgentID: "agent-1", MinSalience: 0.01, Limit: 10})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	assert.InDelta(t, 0.4, memories[0].Salience, 0.01)
}

// =============================================================================
// ContextFor Tests
// =============================================================================

func TestGraphMemoryStore_ContextFor(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Setup: entity + edges + memories.
	entity, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "alice", EntityType: "person",
		PropertiesJSON: `{"role":"engineer"}`,
	})
	require.NoError(t, err)

	project, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "loom-project", EntityType: "project",
	})
	require.NoError(t, err)

	_, err = store.Relate(ctx, &memory.Edge{
		AgentID: "agent-1", SourceID: entity.ID, TargetID: project.ID, Relation: "WORKS_ON",
	})
	require.NoError(t, err)

	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Alice prefers Go for backend development",
		MemoryType: memory.MemoryTypePreference, Salience: 0.8,
		EntityIDs: []string{entity.ID}, TokenCount: 10, SummaryTokenCount: 5,
	})
	require.NoError(t, err)

	// ContextFor alice.
	recall, err := store.ContextFor(ctx, memory.ContextForOpts{
		AgentID:    "agent-1",
		EntityName: "alice",
		Topic:      "Go backend",
		MaxTokens:  5000,
	})
	require.NoError(t, err)
	assert.NotNil(t, recall.Entity)
	assert.Equal(t, "alice", recall.Entity.Name)
	assert.NotEmpty(t, recall.EdgesOut)
	assert.NotEmpty(t, recall.Memories)
}

func TestGraphMemoryStore_ContextFor_EntityNotFound(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// ContextFor a nonexistent entity should return empty recall, not error.
	recall, err := store.ContextFor(ctx, memory.ContextForOpts{
		AgentID:    "agent-1",
		EntityName: "nonexistent",
		MaxTokens:  5000,
	})
	require.NoError(t, err)
	assert.NotNil(t, recall)
	assert.Nil(t, recall.Entity)
	assert.Empty(t, recall.Memories)
}

// =============================================================================
// Stats Tests
// =============================================================================

func TestGraphMemoryStore_GetStats(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	e1, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "s1", EntityType: "concept"})
	e2, _ := store.CreateEntity(ctx, &memory.Entity{AgentID: "agent-1", Name: "s2", EntityType: "concept"})
	_, _ = store.Relate(ctx, &memory.Edge{AgentID: "agent-1", SourceID: e1.ID, TargetID: e2.ID, Relation: "REL"})

	_, _ = store.Remember(ctx, &memory.Memory{AgentID: "agent-1", Content: "fact1", MemoryType: "fact", Salience: 0.5})
	_, _ = store.Remember(ctx, &memory.Memory{AgentID: "agent-1", Content: "pref1", MemoryType: "preference", Salience: 0.5})

	stats, err := store.GetStats(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 2, stats.EntityCount)
	assert.Equal(t, 1, stats.EdgeCount)
	assert.Equal(t, 2, stats.MemoryCount)
	assert.Equal(t, 2, stats.ActiveMemoryCount)
	assert.Equal(t, 1, stats.MemoriesByType["fact"])
	assert.Equal(t, 1, stats.MemoriesByType["preference"])
}

// =============================================================================
// Concurrent Access
// =============================================================================

func TestGraphMemoryStore_ConcurrentAccess(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Create entities sequentially first (SQLite single-writer).
	for i := 0; i < 10; i++ {
		_, err := store.CreateEntity(ctx, &memory.Entity{
			AgentID:    "agent-1",
			Name:       fmt.Sprintf("concurrent-entity-%d", i),
			EntityType: "concept",
		})
		require.NoError(t, err)
	}
	for i := 0; i < 10; i++ {
		_, err := store.Remember(ctx, &memory.Memory{
			AgentID:    "agent-1",
			Content:    fmt.Sprintf("Concurrent memory #%d with some content", i),
			MemoryType: memory.MemoryTypeFact,
			Salience:   0.5,
		})
		require.NoError(t, err)
	}

	// Now do concurrent READS — SQLite WAL supports concurrent readers.
	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.ListEntities(ctx, "agent-1", "", 50, 0)
			if err != nil {
				errCh <- err
			}
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Recall(ctx, memory.RecallOpts{AgentID: "agent-1", Limit: 10})
			if err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent read error: %v", err)
	}

	entities, total, err := store.ListEntities(ctx, "agent-1", "", 50, 0)
	require.NoError(t, err)
	assert.Equal(t, 10, total)
	assert.Len(t, entities, 10)

	stats, err := store.GetStats(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, 10, stats.MemoryCount)
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestGraphMemoryStore_Recall_NoResults(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "nonexistent-agent",
		Query:   "anything",
		Limit:   10,
	})
	require.NoError(t, err)
	assert.Empty(t, memories)
}

func TestGraphMemoryStore_GetEntity_NotFound(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.GetEntity(ctx, "agent-1", "nonexistent")
	assert.Error(t, err)
}

func TestGraphMemoryStore_SearchEntities(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "kubernetes-cluster", EntityType: "tool",
		PropertiesJSON: `{"provider":"aws"}`,
	})
	require.NoError(t, err)

	entities, err := store.SearchEntities(ctx, "agent-1", "kubernetes", 10)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "kubernetes-cluster", entities[0].Name)
}

func TestGraphMemoryStore_Recall_ByType(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "A fact memory", MemoryType: memory.MemoryTypeFact, Salience: 0.5,
	})
	require.NoError(t, err)
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "A preference memory", MemoryType: memory.MemoryTypePreference, Salience: 0.5,
	})
	require.NoError(t, err)

	// Recall only facts.
	facts, err := store.Recall(ctx, memory.RecallOpts{
		AgentID:    "agent-1",
		MemoryType: memory.MemoryTypeFact,
		Limit:      10,
	})
	require.NoError(t, err)
	assert.Len(t, facts, 1)
	assert.Equal(t, memory.MemoryTypeFact, facts[0].MemoryType)
}

func TestGraphMemoryStore_Recall_ByTags(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	_, err := store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Tagged memory", MemoryType: memory.MemoryTypeFact,
		Tags: []string{"important", "ui"}, Salience: 0.5,
	})
	require.NoError(t, err)
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Other memory", MemoryType: memory.MemoryTypeFact,
		Tags: []string{"backend"}, Salience: 0.5,
	})
	require.NoError(t, err)

	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "agent-1",
		Tags:    []string{"important"},
		Limit:   10,
	})
	require.NoError(t, err)
	assert.Len(t, memories, 1)
	assert.Contains(t, memories[0].Tags, "important")
}

func TestGraphMemoryStore_Recall_CombinedFilters(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Create entity for scoping.
	entity, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "agent-1", Name: "project-x", EntityType: "project",
	})
	require.NoError(t, err)

	// Memory 1: fact, tagged "backend", linked to entity, salience 0.9
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Project X uses Go for backend", MemoryType: memory.MemoryTypeFact,
		Tags: []string{"backend", "go"}, Salience: 0.9, EntityIDs: []string{entity.ID},
	})
	require.NoError(t, err)

	// Memory 2: preference, tagged "backend", linked to entity, salience 0.3
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Team prefers gRPC over REST", MemoryType: memory.MemoryTypePreference,
		Tags: []string{"backend", "api"}, Salience: 0.3, EntityIDs: []string{entity.ID},
	})
	require.NoError(t, err)

	// Memory 3: fact, tagged "frontend", NOT linked to entity, salience 0.8
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: "agent-1", Content: "Frontend uses React", MemoryType: memory.MemoryTypeFact,
		Tags: []string{"frontend"}, Salience: 0.8,
	})
	require.NoError(t, err)

	// Combined filter: type=fact + tag=backend + entity scoped + min_salience=0.5
	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID:     "agent-1",
		MemoryType:  memory.MemoryTypeFact,
		Tags:        []string{"backend"},
		EntityIDs:   []string{entity.ID},
		MinSalience: 0.5,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, memories, 1, "should match only fact+backend+entity+high-salience")
	assert.Contains(t, memories[0].Content, "Project X uses Go")
}

func TestGraphMemoryStore_Recall_MinSalience(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// Create memories with varying salience.
	for _, s := range []float64{0.2, 0.5, 0.8} {
		_, err := store.Remember(ctx, &memory.Memory{
			AgentID: "agent-1", Content: fmt.Sprintf("Memory with salience %.1f", s),
			MemoryType: memory.MemoryTypeFact, Salience: s,
		})
		require.NoError(t, err)
	}

	// Recall with min_salience=0.6 should only return the 0.8 memory.
	memories, err := store.Recall(ctx, memory.RecallOpts{
		AgentID:     "agent-1",
		MinSalience: 0.6,
		Limit:       10,
	})
	require.NoError(t, err)
	require.Len(t, memories, 1)
	assert.InDelta(t, 0.8, memories[0].Salience, 0.01)
}

func TestGraphMemoryStore_EntityRecallFormat(t *testing.T) {
	recall := &memory.EntityRecall{
		Entity: &memory.Entity{Name: "alice", EntityType: "person"},
		Memories: []memory.ScoredMemory{
			{Memory: &memory.Memory{Content: "Likes Go", MemoryType: "preference"}, ComputedSalience: 0.8},
		},
		EdgesOut: []*memory.Edge{{Relation: "WORKS_ON", TargetID: "project-1"}},
	}

	formatted := recall.Format()
	assert.Contains(t, formatted, "alice")
	assert.Contains(t, formatted, "WORKS_ON")
	assert.Contains(t, formatted, "Likes Go")
}
