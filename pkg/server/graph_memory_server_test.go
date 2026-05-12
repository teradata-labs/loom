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

package server

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
)

// =============================================================================
// Mock GraphMemoryStore
// =============================================================================

// mockGraphMemoryStore is an in-memory mock of memory.GraphMemoryStore for tests.
type mockGraphMemoryStore struct {
	mu       sync.RWMutex
	entities map[string]*memory.Entity // keyed by "agentID/name"
	edges    []*memory.Edge
	memories map[string]*memory.Memory // keyed by ID
	nextID   int
}

func newMockGraphMemoryStore() *mockGraphMemoryStore {
	return &mockGraphMemoryStore{
		entities: make(map[string]*memory.Entity),
		memories: make(map[string]*memory.Memory),
	}
}

func (m *mockGraphMemoryStore) entityKey(agentID, name string) string {
	return agentID + "/" + name
}

func (m *mockGraphMemoryStore) nextIDStr() string {
	m.nextID++
	return fmt.Sprintf("id-%d", m.nextID)
}

func (m *mockGraphMemoryStore) CreateEntity(_ context.Context, entity *memory.Entity) (*memory.Entity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entity.ID = m.nextIDStr()
	entity.CreatedAt = time.Now()
	entity.UpdatedAt = time.Now()
	m.entities[m.entityKey(entity.AgentID, entity.Name)] = entity
	return entity, nil
}

func (m *mockGraphMemoryStore) GetEntity(_ context.Context, agentID, name string) (*memory.Entity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entities[m.entityKey(agentID, name)]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return e, nil
}

func (m *mockGraphMemoryStore) UpdateEntity(_ context.Context, entity *memory.Entity) (*memory.Entity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.entityKey(entity.AgentID, entity.Name)
	existing, ok := m.entities[key]
	if !ok {
		return nil, fmt.Errorf("entity not found: agent_id=%s name=%s", entity.AgentID, entity.Name)
	}
	existing.EntityType = entity.EntityType
	existing.PropertiesJSON = entity.PropertiesJSON
	existing.UpdatedAt = time.Now()
	return existing, nil
}

func (m *mockGraphMemoryStore) ListEntities(_ context.Context, agentID, entityType string, limit, offset int) ([]*memory.Entity, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*memory.Entity
	for _, e := range m.entities {
		if e.AgentID != agentID {
			continue
		}
		if entityType != "" && e.EntityType != entityType {
			continue
		}
		result = append(result, e)
	}
	total := len(result)
	if offset >= len(result) {
		return nil, total, nil
	}
	result = result[offset:]
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, total, nil
}

func (m *mockGraphMemoryStore) SearchEntities(_ context.Context, _ string, _ string, _ int) ([]*memory.Entity, error) {
	return nil, nil
}

func (m *mockGraphMemoryStore) DeleteEntity(_ context.Context, agentID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.entityKey(agentID, name)
	if _, ok := m.entities[key]; !ok {
		return fmt.Errorf("entity not found: agent_id=%s name=%s", agentID, name)
	}
	delete(m.entities, key)
	return nil
}

func (m *mockGraphMemoryStore) Relate(_ context.Context, edge *memory.Edge) (*memory.Edge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	edge.ID = m.nextIDStr()
	edge.CreatedAt = time.Now()
	edge.UpdatedAt = time.Now()
	m.edges = append(m.edges, edge)
	return edge, nil
}

func (m *mockGraphMemoryStore) Unrelate(_ context.Context, sourceID, targetID, relation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.edges {
		if e.SourceID == sourceID && e.TargetID == targetID && e.Relation == relation {
			m.edges = append(m.edges[:i], m.edges[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockGraphMemoryStore) Neighbors(_ context.Context, entityID string, relation string, direction string, _ int) ([]*memory.Edge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*memory.Edge
	for _, e := range m.edges {
		match := false
		switch direction {
		case "outbound":
			match = e.SourceID == entityID
		case "inbound":
			match = e.TargetID == entityID
		case "both":
			match = e.SourceID == entityID || e.TargetID == entityID
		}
		if match && (relation == "" || e.Relation == relation) {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *mockGraphMemoryStore) ListEdgesFrom(_ context.Context, entityID string) ([]*memory.Edge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*memory.Edge
	for _, e := range m.edges {
		if e.SourceID == entityID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *mockGraphMemoryStore) ListEdgesTo(_ context.Context, entityID string) ([]*memory.Edge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*memory.Edge
	for _, e := range m.edges {
		if e.TargetID == entityID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *mockGraphMemoryStore) Remember(_ context.Context, mem *memory.Memory) (*memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem.ID = m.nextIDStr()
	mem.CreatedAt = time.Now()
	if mem.Salience == 0 {
		mem.Salience = 0.5
	}
	m.memories[mem.ID] = mem
	return mem, nil
}

func (m *mockGraphMemoryStore) GetMemory(_ context.Context, agentID, memoryID string) (*memory.Memory, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mem, ok := m.memories[memoryID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	if mem.AgentID != agentID {
		return nil, sql.ErrNoRows
	}
	return mem, nil
}

func (m *mockGraphMemoryStore) Recall(_ context.Context, opts memory.RecallOpts) ([]*memory.Memory, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*memory.Memory
	for _, mem := range m.memories {
		if mem.AgentID != opts.AgentID {
			continue
		}
		if opts.MemoryType != "" && mem.MemoryType != opts.MemoryType {
			continue
		}
		result = append(result, mem)
	}
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

func (m *mockGraphMemoryStore) Forget(_ context.Context, memoryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.memories[memoryID]; !ok {
		return fmt.Errorf("memory not found: %s", memoryID)
	}
	now := time.Now()
	m.memories[memoryID].ExpiresAt = &now
	return nil
}

func (m *mockGraphMemoryStore) Supersede(_ context.Context, oldMemoryID string, newMem *memory.Memory) (*memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.memories[oldMemoryID]
	if !ok {
		return nil, fmt.Errorf("memory not found: %s", oldMemoryID)
	}
	old.IsSuperseded = true
	newMem.ID = m.nextIDStr()
	newMem.CreatedAt = time.Now()
	if newMem.Salience == 0 {
		newMem.Salience = old.Salience
	}
	m.memories[newMem.ID] = newMem
	return newMem, nil
}

func (m *mockGraphMemoryStore) Consolidate(_ context.Context, memoryIDs []string, consolidated *memory.Memory) (*memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	consolidated.ID = m.nextIDStr()
	consolidated.CreatedAt = time.Now()
	m.memories[consolidated.ID] = consolidated
	return consolidated, nil
}

func (m *mockGraphMemoryStore) GetLineage(_ context.Context, _ string) ([]*memory.MemoryLineage, error) {
	return nil, nil
}

func (m *mockGraphMemoryStore) TouchMemories(_ context.Context, _ []string) error {
	return nil
}

func (m *mockGraphMemoryStore) DecayAll(_ context.Context, _ string, _ float64) error {
	return nil
}

func (m *mockGraphMemoryStore) ContextFor(_ context.Context, opts memory.ContextForOpts) (*memory.EntityRecall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entity, ok := m.entities[m.entityKey(opts.AgentID, opts.EntityName)]
	if !ok {
		return &memory.EntityRecall{}, nil
	}

	result := &memory.EntityRecall{
		Entity:      entity,
		EntityNames: make(map[string]string),
	}

	// Collect edges.
	for _, e := range m.edges {
		if e.SourceID == entity.ID {
			result.EdgesOut = append(result.EdgesOut, e)
		}
		if e.TargetID == entity.ID {
			result.EdgesIn = append(result.EdgesIn, e)
		}
	}

	// Collect memories linked to entity.
	for _, mem := range m.memories {
		if mem.AgentID == opts.AgentID {
			result.Memories = append(result.Memories, memory.ScoredMemory{
				Memory:           mem,
				ComputedSalience: mem.Salience,
				CombinedScore:    mem.Salience,
			})
		}
	}
	result.TotalCandidates = len(result.Memories)
	return result, nil
}

func (m *mockGraphMemoryStore) VectorRecall(_ context.Context, _ memory.VectorRecallOpts) ([]*memory.Memory, error) {
	return nil, nil
}

func (m *mockGraphMemoryStore) GetStats(_ context.Context, agentID string) (*memory.GraphStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	stats := &memory.GraphStats{MemoriesByType: make(map[string]int)}
	for _, e := range m.entities {
		if e.AgentID == agentID {
			stats.EntityCount++
		}
	}
	for _, e := range m.edges {
		if e.AgentID == agentID {
			stats.EdgeCount++
		}
	}
	for _, mem := range m.memories {
		if mem.AgentID == agentID {
			stats.MemoryCount++
			if mem.ExpiresAt == nil || mem.ExpiresAt.After(time.Now()) {
				stats.ActiveMemoryCount++
			}
			stats.MemoriesByType[mem.MemoryType]++
		}
	}
	return stats, nil
}

func (m *mockGraphMemoryStore) Close() error {
	return nil
}

// =============================================================================
// Tests
// =============================================================================

func TestGraphMemoryServer_CreateEntity_GetEntity_Roundtrip(t *testing.T) {
	store := newMockGraphMemoryStore()
	srv := NewGraphMemoryServer(store, zap.NewNop())
	ctx := context.Background()

	// Create entity.
	createResp, err := srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{
		AgentId:        "agent-1",
		Name:           "Alice",
		EntityType:     "person",
		PropertiesJson: `{"role":"engineer"}`,
		Owner:          "test",
	})
	require.NoError(t, err)
	require.NotNil(t, createResp.Entity)
	assert.Equal(t, "Alice", createResp.Entity.Name)
	assert.Equal(t, "person", createResp.Entity.EntityType)
	assert.Equal(t, `{"role":"engineer"}`, createResp.Entity.PropertiesJson)
	assert.NotEmpty(t, createResp.Entity.Id)

	// Get entity back.
	getResp, err := srv.GetEntity(ctx, &loomv1.GetEntityRequest{
		AgentId: "agent-1",
		Name:    "Alice",
	})
	require.NoError(t, err)
	require.NotNil(t, getResp.Entity)
	assert.Equal(t, createResp.Entity.Id, getResp.Entity.Id)
	assert.Equal(t, "Alice", getResp.Entity.Name)
	assert.Equal(t, "person", getResp.Entity.EntityType)
}

func TestGraphMemoryServer_Remember_Recall_Roundtrip(t *testing.T) {
	store := newMockGraphMemoryStore()
	srv := NewGraphMemoryServer(store, zap.NewNop())
	ctx := context.Background()

	// Remember a fact.
	rememberResp, err := srv.Remember(ctx, &loomv1.RememberRequest{
		AgentId:    "agent-1",
		Content:    "Alice prefers dark mode",
		MemoryType: "preference",
		Source:     "conversation",
		Salience:   0.8,
		Tags:       []string{"ui", "preference"},
	})
	require.NoError(t, err)
	require.NotNil(t, rememberResp.Memory)
	assert.Equal(t, "Alice prefers dark mode", rememberResp.Memory.Content)
	assert.Equal(t, "preference", rememberResp.Memory.MemoryType)
	assert.NotEmpty(t, rememberResp.Memory.Id)

	// Recall it.
	recallResp, err := srv.Recall(ctx, &loomv1.RecallRequest{
		AgentId: "agent-1",
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(recallResp.Memories), 1)
	found := false
	for _, sm := range recallResp.Memories {
		if sm.Memory.Id == rememberResp.Memory.Id {
			found = true
			assert.Equal(t, "Alice prefers dark mode", sm.Memory.Content)
		}
	}
	assert.True(t, found, "recalled memory should contain the remembered item")
}

func TestGraphMemoryServer_Relate_Neighbors(t *testing.T) {
	store := newMockGraphMemoryStore()
	srv := NewGraphMemoryServer(store, zap.NewNop())
	ctx := context.Background()

	// Create two entities.
	_, err := srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{
		AgentId:    "agent-1",
		Name:       "Alice",
		EntityType: "person",
	})
	require.NoError(t, err)

	_, err = srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{
		AgentId:    "agent-1",
		Name:       "ProjectX",
		EntityType: "project",
	})
	require.NoError(t, err)

	// Relate them.
	relateResp, err := srv.Relate(ctx, &loomv1.RelateRequest{
		AgentId:    "agent-1",
		SourceName: "Alice",
		TargetName: "ProjectX",
		Relation:   "WORKS_ON",
	})
	require.NoError(t, err)
	require.NotNil(t, relateResp.Edge)
	assert.Equal(t, "WORKS_ON", relateResp.Edge.Relation)
	assert.NotEmpty(t, relateResp.Edge.Id)

	// Query neighbors.
	neighborsResp, err := srv.Neighbors(ctx, &loomv1.NeighborsRequest{
		AgentId:    "agent-1",
		EntityName: "Alice",
		Direction:  loomv1.NeighborDirection_NEIGHBOR_DIRECTION_OUTBOUND,
	})
	require.NoError(t, err)
	assert.Len(t, neighborsResp.Edges, 1)
	assert.Equal(t, "WORKS_ON", neighborsResp.Edges[0].Relation)
}

func TestGraphMemoryServer_ContextFor(t *testing.T) {
	store := newMockGraphMemoryStore()
	srv := NewGraphMemoryServer(store, zap.NewNop())
	ctx := context.Background()

	// Create entity and memory.
	_, err := srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{
		AgentId:    "agent-1",
		Name:       "Alice",
		EntityType: "person",
	})
	require.NoError(t, err)

	_, err = srv.Remember(ctx, &loomv1.RememberRequest{
		AgentId:    "agent-1",
		Content:    "Alice works on ProjectX",
		MemoryType: "fact",
		Salience:   0.7,
	})
	require.NoError(t, err)

	// Query ContextFor.
	resp, err := srv.ContextFor(ctx, &loomv1.ContextForRequest{
		AgentId:    "agent-1",
		EntityName: "Alice",
		MaxTokens:  2000,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Recall)
	require.NotNil(t, resp.Recall.Entity)
	assert.Equal(t, "Alice", resp.Recall.Entity.Name)
	assert.GreaterOrEqual(t, len(resp.Recall.Memories), 1)
}

func TestGraphMemoryServer_ValidationErrors(t *testing.T) {
	store := newMockGraphMemoryStore()
	srv := NewGraphMemoryServer(store, zap.NewNop())
	ctx := context.Background()

	tests := []struct {
		name     string
		call     func() error
		wantCode codes.Code
	}{
		{
			name: "CreateEntity missing agent_id",
			call: func() error {
				_, err := srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{Name: "X"})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "CreateEntity missing name",
			call: func() error {
				_, err := srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{AgentId: "a"})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "GetEntity not found",
			call: func() error {
				_, err := srv.GetEntity(ctx, &loomv1.GetEntityRequest{AgentId: "a", Name: "nope"})
				return err
			},
			wantCode: codes.NotFound,
		},
		{
			name: "Remember missing content",
			call: func() error {
				_, err := srv.Remember(ctx, &loomv1.RememberRequest{AgentId: "a"})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Recall missing agent_id",
			call: func() error {
				_, err := srv.Recall(ctx, &loomv1.RecallRequest{})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Forget missing memory_id",
			call: func() error {
				_, err := srv.Forget(ctx, &loomv1.ForgetRequest{AgentId: "a"})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Relate missing relation",
			call: func() error {
				_, err := srv.Relate(ctx, &loomv1.RelateRequest{AgentId: "a", SourceName: "s", TargetName: "t"})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "Relate source entity not found",
			call: func() error {
				_, err := srv.Relate(ctx, &loomv1.RelateRequest{
					AgentId: "a", SourceName: "ghost", TargetName: "t", Relation: "R",
				})
				return err
			},
			wantCode: codes.NotFound,
		},
		{
			name: "Consolidate needs at least 2 memory_ids",
			call: func() error {
				_, err := srv.Consolidate(ctx, &loomv1.ConsolidateRequest{
					AgentId:   "a",
					MemoryIds: []string{"one"},
					Content:   "merged",
				})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "ContextFor missing entity_name",
			call: func() error {
				_, err := srv.ContextFor(ctx, &loomv1.ContextForRequest{AgentId: "a"})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "GetGraphStats missing agent_id",
			call: func() error {
				_, err := srv.GetGraphStats(ctx, &loomv1.GetGraphStatsRequest{})
				return err
			},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Error(t, err)
			st, ok := status.FromError(err)
			require.True(t, ok, "expected gRPC status error")
			assert.Equal(t, tt.wantCode, st.Code(), "expected code %s, got %s: %s", tt.wantCode, st.Code(), st.Message())
		})
	}
}

func TestGraphMemoryServer_Supersede(t *testing.T) {
	store := newMockGraphMemoryStore()
	srv := NewGraphMemoryServer(store, zap.NewNop())
	ctx := context.Background()

	// Remember an initial fact.
	rememberResp, err := srv.Remember(ctx, &loomv1.RememberRequest{
		AgentId:    "agent-1",
		Content:    "Alice works at Acme",
		MemoryType: "fact",
		Salience:   0.7,
	})
	require.NoError(t, err)
	oldID := rememberResp.Memory.Id

	// Supersede it.
	supersedeResp, err := srv.Supersede(ctx, &loomv1.SupersedeRequest{
		AgentId:     "agent-1",
		OldMemoryId: oldID,
		NewContent:  "Alice works at Globex",
		NewSummary:  "employer change",
		NewTags:     []string{"employer"},
	})
	require.NoError(t, err)
	require.NotNil(t, supersedeResp.NewMemory)
	assert.Equal(t, "Alice works at Globex", supersedeResp.NewMemory.Content)
	assert.NotEqual(t, oldID, supersedeResp.NewMemory.Id)
	require.NotNil(t, supersedeResp.Lineage)
	assert.Equal(t, oldID, supersedeResp.Lineage.OldMemoryId)
	assert.Equal(t, supersedeResp.NewMemory.Id, supersedeResp.Lineage.NewMemoryId)
	assert.Equal(t, loomv1.LineageRelationType_LINEAGE_RELATION_TYPE_SUPERSEDES, supersedeResp.Lineage.RelationType)
}

func TestGraphMemoryServer_GetGraphStats(t *testing.T) {
	store := newMockGraphMemoryStore()
	srv := NewGraphMemoryServer(store, zap.NewNop())
	ctx := context.Background()

	// Seed some data.
	_, err := srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{
		AgentId: "agent-1", Name: "Alice", EntityType: "person",
	})
	require.NoError(t, err)
	_, err = srv.CreateEntity(ctx, &loomv1.CreateEntityRequest{
		AgentId: "agent-1", Name: "Bob", EntityType: "person",
	})
	require.NoError(t, err)
	_, err = srv.Remember(ctx, &loomv1.RememberRequest{
		AgentId: "agent-1", Content: "fact 1", MemoryType: "fact", Salience: 0.5,
	})
	require.NoError(t, err)

	// Get stats.
	resp, err := srv.GetGraphStats(ctx, &loomv1.GetGraphStatsRequest{AgentId: "agent-1"})
	require.NoError(t, err)
	require.NotNil(t, resp.Stats)
	assert.Equal(t, int32(2), resp.Stats.EntityCount)
	assert.Equal(t, int32(1), resp.Stats.MemoryCount)
	assert.Equal(t, int32(1), resp.Stats.ActiveMemoryCount)
}
