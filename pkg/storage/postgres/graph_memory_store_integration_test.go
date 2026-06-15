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
