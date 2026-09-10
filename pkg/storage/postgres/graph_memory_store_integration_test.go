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

//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/memory"
)

// TestGraphMemory_EmptyPropertiesJSON is the regression test for the JSONB
// bug found by seeding the meetup deployment: entities/edges/memories created
// without explicit properties (e.g. an entity auto-created by a `relate`)
// carried an empty PropertiesJSON string, which Postgres rejects on the JSONB
// column with SQLSTATE 22P02 ("invalid input syntax for type json"). Every
// graph_memory store/relate failed on Supabase as a result. The store now
// defaults an empty value to a valid JSON literal.
func TestGraphMemory_EmptyPropertiesJSON(t *testing.T) {
	pool := testPool(t)
	ensureRLSWriterRole(t, pool) // not strictly needed; superuser path is fine here
	store := NewGraphMemoryStore(pool, nil, nil)

	ctx := ContextWithUserID(context.Background(), uniqueID("graph-user"))
	agentID := uniqueID("graph-agent")

	// Entity with empty PropertiesJSON — previously 22P02.
	src, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: agentID, Name: uniqueID("austin-meetup"), EntityType: "project",
		// PropertiesJSON intentionally left empty
	})
	require.NoError(t, err, "CreateEntity with empty PropertiesJSON must not error on Postgres JSONB")

	dst, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: agentID, Name: uniqueID("demo-stack"), EntityType: "concept",
	})
	require.NoError(t, err)

	// Edge with empty PropertiesJSON — previously 22P02.
	edge, err := store.Relate(ctx, &memory.Edge{
		AgentID: agentID, SourceID: src.ID, TargetID: dst.ID, Relation: "USES",
	})
	require.NoError(t, err, "Relate with empty PropertiesJSON must not error")
	assert.NotEmpty(t, edge.ID)

	// And explicit properties still round-trip.
	withProps, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: agentID, Name: uniqueID("presenter"), EntityType: "person",
		PropertiesJSON: `{"name":"Ilsun Park"}`,
	})
	require.NoError(t, err)
	got, err := store.GetEntity(ctx, agentID, withProps.Name)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"Ilsun Park"}`, got.PropertiesJSON)
}

// TestGraphMemory_RememberAutoCreatesReferencedEntities reproduces the production
// failure where an agent's remember(entity_ids=[...]) referenced entities it had
// never created, violating graph_memory_entities -> graph_entities (SQLSTATE 23503)
// and silently dropping the memory. The store must now auto-create the entities.
func TestGraphMemory_RememberAutoCreatesReferencedEntities(t *testing.T) {
	pool := testPool(t)
	store := NewGraphMemoryStore(pool, nil, nil)
	ctx := ContextWithUserID(context.Background(), uniqueID("gm-user"))
	agentID := uniqueID("gm-agent")

	ids := []string{uniqueID("ilsun_park"), uniqueID("buf"), uniqueID("protobuf")}
	saved, err := store.Remember(ctx, &memory.Memory{
		AgentID:    agentID,
		Content:    "Ilsun uses Buf for protobuf",
		Summary:    "prefers Buf",
		MemoryType: "preference",
		Salience:   0.7,
		EntityIDs:  ids,
	})
	require.NoError(t, err,
		"remember with un-created entity_ids must auto-create them, not violate the FK (was 23503)")

	got, err := store.GetMemory(ctx, agentID, saved.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, ids, got.EntityIDs, "all referenced entities should be auto-created and linked")

	// Idempotent: remembering the same entity again must not error.
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: agentID, Content: "again", MemoryType: "fact", EntityIDs: ids[:1],
	})
	require.NoError(t, err, "re-referencing an existing entity must be a no-op, not a duplicate-key error")

	// The production failure: an entity already exists by NAME under a different
	// (UUID) id; remembering by that name must resolve to it, not collide on
	// (agent_id, name).
	existing, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: agentID, Name: uniqueID("buf"), EntityType: "tool",
	})
	require.NoError(t, err)
	require.NotEqual(t, existing.Name, existing.ID, "entity id should be a UUID, distinct from its name")

	saved2, err := store.Remember(ctx, &memory.Memory{
		AgentID: agentID, Content: "Ilsun prefers " + existing.Name, MemoryType: "preference",
		EntityIDs: []string{existing.Name}, // reference by name, not id
	})
	require.NoError(t, err, "referencing an existing entity by name must resolve, not hit a duplicate-key collision")

	got2, err := store.GetMemory(ctx, agentID, saved2.ID)
	require.NoError(t, err)
	assert.Contains(t, got2.EntityIDs, existing.ID, "link should resolve to the existing entity's id")
}

// Outcome credit must work on Postgres, not just SQLite.
//
// AdjustSalience is an optional capability discovered by type assertion in
// pkg/agent, so a store that lacks it does not fail to build or run — credit
// is simply skipped. That made this silent: a Postgres-backed deployment
// would read "outcome credit demotes bad lessons" and never demote one.
//
// The clamp is the part that cannot be copied from SQLite: MIN/MAX are
// aggregates in postgres, so this asserts both bounds actually bind.
func TestGraphMemory_AdjustSalience(t *testing.T) {
	pool := testPool(t)
	store := NewGraphMemoryStore(pool, nil, nil)
	ctx := ContextWithUserID(context.Background(), uniqueID("salience-user"))
	agentID := uniqueID("salience-agent")

	// The store must satisfy the capability pkg/agent asserts for, or credit
	// is skipped at runtime with no error anywhere.
	var _ interface {
		AdjustSalience(ctx context.Context, memoryID string, delta float64) error
	} = store

	newLesson := func(salience float64) string {
		t.Helper()
		saved, err := store.Remember(ctx, &memory.Memory{
			AgentID:    agentID,
			Content:    "always run buf generate after editing the proto",
			MemoryType: "lesson",
			Salience:   salience,
		})
		require.NoError(t, err)
		return saved.ID
	}
	salienceOf := func(id string) float64 {
		t.Helper()
		got, err := store.GetMemory(ctx, agentID, id)
		require.NoError(t, err)
		return got.Salience
	}

	t.Run("loss lowers salience", func(t *testing.T) {
		id := newLesson(0.7)
		require.NoError(t, store.AdjustSalience(ctx, id, -0.2))
		assert.InDelta(t, 0.5, salienceOf(id), 1e-6)
	})

	t.Run("win raises salience", func(t *testing.T) {
		id := newLesson(0.5)
		require.NoError(t, store.AdjustSalience(ctx, id, 0.15))
		assert.InDelta(t, 0.65, salienceOf(id), 1e-6)
	})

	t.Run("floor clamps at 0.05 so demotion is reversible", func(t *testing.T) {
		id := newLesson(0.1)
		require.NoError(t, store.AdjustSalience(ctx, id, -5))
		assert.InDelta(t, 0.05, salienceOf(id), 1e-6,
			"a demoted lesson must sink to the floor, not to zero — outcome credit "+
				"has to be able to bring it back on a later win")
	})

	t.Run("ceiling clamps at 1.0", func(t *testing.T) {
		id := newLesson(0.9)
		require.NoError(t, store.AdjustSalience(ctx, id, 5))
		assert.InDelta(t, 1.0, salienceOf(id), 1e-6)
	})

	t.Run("unknown id is a no-op, not an error", func(t *testing.T) {
		assert.NoError(t, store.AdjustSalience(ctx, uniqueID("no-such-memory"), -0.1),
			"credit iterates lessons that may have been forgotten mid-conversation")
	})
}
