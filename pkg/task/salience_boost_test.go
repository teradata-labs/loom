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

package task

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/memory"
)

// mockGraphMemoryStore is a minimal mock that tracks SearchEntities, Recall,
// and TouchMemories calls for salience boost testing.
type mockGraphMemoryStore struct {
	mu sync.Mutex

	// Stubbed data
	entities []*memory.Entity
	memories []*memory.Memory

	// Call tracking
	searchCalls   []searchCall
	recallCalls   []recallCall
	touchedIDs    [][]string
	rememberCalls int
}

type searchCall struct {
	AgentID string
	Query   string
	Limit   int
}

type recallCall struct {
	Opts memory.RecallOpts
}

func (m *mockGraphMemoryStore) SearchEntities(_ context.Context, agentID, query string, limit int) ([]*memory.Entity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searchCalls = append(m.searchCalls, searchCall{AgentID: agentID, Query: query, Limit: limit})
	return m.entities, nil
}

func (m *mockGraphMemoryStore) Recall(_ context.Context, opts memory.RecallOpts) ([]*memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallCalls = append(m.recallCalls, recallCall{Opts: opts})
	return m.memories, nil
}

func (m *mockGraphMemoryStore) TouchMemories(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.touchedIDs = append(m.touchedIDs, ids)
	return nil
}

func (m *mockGraphMemoryStore) Remember(_ context.Context, _ *memory.Memory) (*memory.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rememberCalls++
	return &memory.Memory{ID: "mem-new"}, nil
}

// Unused interface methods — stubs to satisfy GraphMemoryStore.
func (m *mockGraphMemoryStore) CreateEntity(context.Context, *memory.Entity) (*memory.Entity, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) GetEntity(context.Context, string, string) (*memory.Entity, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) UpdateEntity(context.Context, *memory.Entity) (*memory.Entity, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) ListEntities(context.Context, string, string, int, int) ([]*memory.Entity, int, error) {
	return nil, 0, nil
}
func (m *mockGraphMemoryStore) DeleteEntity(context.Context, string, string) error { return nil }
func (m *mockGraphMemoryStore) Relate(context.Context, *memory.Edge) (*memory.Edge, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) Unrelate(context.Context, string, string, string) error { return nil }
func (m *mockGraphMemoryStore) Neighbors(context.Context, string, string, string, int) ([]*memory.Edge, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) ListEdgesFrom(context.Context, string) ([]*memory.Edge, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) ListEdgesTo(context.Context, string) ([]*memory.Edge, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) GetMemory(context.Context, string, string) (*memory.Memory, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) Forget(context.Context, string) error { return nil }
func (m *mockGraphMemoryStore) Supersede(context.Context, string, *memory.Memory) (*memory.Memory, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) Consolidate(context.Context, []string, *memory.Memory) (*memory.Memory, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) GetLineage(context.Context, string) ([]*memory.MemoryLineage, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) DecayAll(context.Context, string, float64) error { return nil }
func (m *mockGraphMemoryStore) ContextFor(context.Context, memory.ContextForOpts) (*memory.EntityRecall, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) VectorRecall(context.Context, memory.VectorRecallOpts) ([]*memory.Memory, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) GetStats(context.Context, string) (*memory.GraphStats, error) {
	return nil, nil
}
func (m *mockGraphMemoryStore) Close() error { return nil }

func TestBoostRelatedEntitySalience_TouchesMemories(t *testing.T) {
	mock := &mockGraphMemoryStore{
		entities: []*memory.Entity{
			{ID: "ent-1", Name: "SQL Optimization", AgentID: "test-agent"},
			{ID: "ent-2", Name: "Query Tuning", AgentID: "test-agent"},
		},
		memories: []*memory.Memory{
			{ID: "mem-1"},
			{ID: "mem-2"},
		},
	}

	mgr := NewManager(nil, nil, nil, zap.NewNop())
	mgr.SetGraphMemory(mock)

	task := &Task{
		ID:           "task-1",
		Title:        "Optimize SQL query",
		Objective:    "Reduce query latency by 50%",
		OwnerAgentID: "test-agent",
		CloseReason:  "done",
	}

	mgr.boostRelatedEntitySalience(task)

	// Allow async goroutine from rememberTaskCompletion — but
	// boostRelatedEntitySalience is called synchronously here.
	mock.mu.Lock()
	defer mock.mu.Unlock()

	// Should have searched entities.
	require.Len(t, mock.searchCalls, 1)
	assert.Equal(t, "test-agent", mock.searchCalls[0].AgentID)
	assert.Contains(t, mock.searchCalls[0].Query, "Optimize SQL query")
	assert.Contains(t, mock.searchCalls[0].Query, "Reduce query latency by 50%")
	assert.Equal(t, 5, mock.searchCalls[0].Limit)

	// Should have recalled memories for each entity.
	require.Len(t, mock.recallCalls, 2) // one per entity
	assert.Equal(t, []string{"ent-1"}, mock.recallCalls[0].Opts.EntityIDs)
	assert.Equal(t, []string{"ent-2"}, mock.recallCalls[1].Opts.EntityIDs)

	// Should have touched the deduplicated memory IDs.
	require.Len(t, mock.touchedIDs, 1)
	// Both entities return same memories, so dedup gives 2 unique IDs.
	assert.ElementsMatch(t, []string{"mem-1", "mem-2"}, mock.touchedIDs[0])
}

func TestBoostRelatedEntitySalience_WithSkillMetadata(t *testing.T) {
	mock := &mockGraphMemoryStore{
		entities: []*memory.Entity{
			{ID: "ent-skill", Name: "code-review", AgentID: "test-agent"},
		},
		memories: []*memory.Memory{
			{ID: "mem-skill-1"},
		},
	}

	mgr := NewManager(nil, nil, nil, zap.NewNop())
	mgr.SetGraphMemory(mock)

	task := &Task{
		ID:           "task-2",
		Title:        "Review pull request",
		Objective:    "Check for security issues",
		OwnerAgentID: "test-agent",
		CloseReason:  "approved",
		Metadata:     map[string]string{"skill_name": "code-review"},
	}

	mgr.boostRelatedEntitySalience(task)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	// Should have two search calls: one for task subject, one for skill name.
	require.Len(t, mock.searchCalls, 2)
	assert.Contains(t, mock.searchCalls[0].Query, "Review pull request")
	assert.Equal(t, "code-review", mock.searchCalls[1].Query)
	assert.Equal(t, 1, mock.searchCalls[1].Limit)

	// TouchMemories should have been called.
	require.Len(t, mock.touchedIDs, 1)
	assert.Contains(t, mock.touchedIDs[0], "mem-skill-1")
}

func TestBoostRelatedEntitySalience_NilGraphMemory(t *testing.T) {
	mgr := NewManager(nil, nil, nil, zap.NewNop())
	// graphMemory is nil — should be a no-op, no panic.
	mgr.boostRelatedEntitySalience(&Task{
		ID:    "task-x",
		Title: "something",
	})
}

func TestBoostRelatedEntitySalience_NoEntitiesFound(t *testing.T) {
	mock := &mockGraphMemoryStore{
		entities: []*memory.Entity{}, // empty
		memories: []*memory.Memory{},
	}

	mgr := NewManager(nil, nil, nil, zap.NewNop())
	mgr.SetGraphMemory(mock)

	task := &Task{
		ID:           "task-3",
		Title:        "Unknown topic",
		OwnerAgentID: "test-agent",
	}

	mgr.boostRelatedEntitySalience(task)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	// Should search but not touch anything.
	require.Len(t, mock.searchCalls, 1)
	assert.Len(t, mock.touchedIDs, 0, "no memories to touch when no entities found")
}

func TestRememberTaskCompletion_InvokesBoost(t *testing.T) {
	mock := &mockGraphMemoryStore{
		entities: []*memory.Entity{
			{ID: "ent-1", Name: "testing", AgentID: "agent-1"},
		},
		memories: []*memory.Memory{
			{ID: "mem-1"},
		},
	}

	mgr := NewManager(nil, nil, nil, zap.NewNop())
	mgr.SetGraphMemory(mock)

	task := &Task{
		ID:           "task-5",
		Title:        "Write tests",
		Objective:    "Verify salience boost",
		OwnerAgentID: "agent-1",
		CloseReason:  "completed",
	}

	mgr.rememberTaskCompletion(context.Background(), task)

	// The boost runs asynchronously — give it time to complete.
	time.Sleep(50 * time.Millisecond)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	// Remember should have been called (the main memory creation).
	assert.Equal(t, 1, mock.rememberCalls)

	// Boost should have searched and touched.
	require.Len(t, mock.searchCalls, 1)
	require.Len(t, mock.touchedIDs, 1)
	assert.Contains(t, mock.touchedIDs[0], "mem-1")
}
