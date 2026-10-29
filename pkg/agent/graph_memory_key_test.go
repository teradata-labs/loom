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

// Test layout: TestGraphMemory_RecallUsesKey is the only test that uses a spy
// store (keySpyStore); it records the agent ID of every recall-side read. The
// other keyed-path tests (extraction, tool, rename, multi-hop, skill eviction)
// use the real SQLite graph store and assert on the rows actually stored or
// returned, including that nothing is written under the agent name. The
// WithGraphMemoryAgentID and span-attribute tests check the option and the
// graph_memory.recall span directly.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/types"
)

// graphKeyTestAgent builds an agent with graph memory on, a real store, and
// the extraction LLM returning one user entity and one memory about fraud.
func graphKeyTestAgent(t *testing.T, store memory.GraphMemoryStore, name, id string) *Agent {
	t.Helper()
	extracted := ExtractedGraphData{
		Entities: []ExtractedEntity{{Name: "ilsun", EntityType: "person", IsUser: true}},
		Memories: []ExtractedMemory{{
			Content:    "Ilsun is analyzing transactions for fraud patterns",
			Summary:    "Ilsun fraud analysis",
			MemoryType: "fact",
			Salience:   0.8,
			Entities:   []ExtractedEntityRole{{Name: "ilsun", Role: "about"}},
		}},
	}
	responseJSON, err := json.Marshal(extracted)
	require.NoError(t, err)

	mem := NewMemory()
	session := mem.GetOrCreateSession(context.Background(), "key-session")
	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(context.Background(), types.Message{Role: "user", Content: "I am Ilsun, I look at fraud."})
	segMem.AddMessage(context.Background(), types.Message{Role: "assistant", Content: "Noted."})
	session.SegmentedMem = segMem

	return &Agent{
		llm:                         &extractionMockLLM{response: string(responseJSON)},
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphExtractionCadence:      5,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{
			Enabled:                  true,
			EnableExtraction:         true,
			MaxEntitiesPerExtraction: 10,
		},
		memory: mem,
		tools:  shuttle.NewRegistry(),
		config: &Config{Name: name, GraphMemoryAgentID: id},
	}
}

// graphKeyReader builds an agent that only recalls: its LLM answers every
// side-call (query distillation, rerank) with "fraud", which the rerank parser
// treats as "keep all candidates".
func graphKeyReader(store memory.GraphMemoryStore, name, id string) *Agent {
	return &Agent{
		llm:               &searchQueryLLM{content: "fraud"},
		graphMemoryStore:  store,
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, MaxContextTokens: 2000},
		config:            &Config{Name: name, GraphMemoryAgentID: id},
		tracer:            observability.NewNoOpTracer(),
	}
}

// graphKeyInject runs the recall path and returns the injected system message ("" when none).
func graphKeyInject(a *Agent) string {
	session := &types.Session{}
	session.AddMessage(context.Background(), types.Message{Role: "user", Content: "what do you know about fraud?"})
	a.injectGraphMemoryContext(context.Background(), session)
	for _, m := range session.GetMessages() {
		if m.Role == "system" {
			return m.Content
		}
	}
	return ""
}

// graphKeyToolCall runs a graph_memory tool action through the agent's registered tool.
func graphKeyToolCall(t *testing.T, a *Agent, params map[string]interface{}) string {
	t.Helper()
	a.checkAndRegisterGraphMemoryTool()
	tool, ok := a.tools.Get("graph_memory")
	require.True(t, ok)
	res, err := tool.Execute(context.Background(), params)
	require.NoError(t, err)
	require.True(t, res.Success, "graph_memory call failed: %+v", res.Error)
	return fmt.Sprint(res.Data)
}

// graphKeySeed writes a "fraud" entity and a memory linked to it under agentID.
func graphKeySeed(t *testing.T, store memory.GraphMemoryStore, agentID string) (*memory.Entity, *memory.Memory) {
	t.Helper()
	ctx := context.Background()
	ent, err := store.CreateEntity(ctx, &memory.Entity{AgentID: agentID, Name: "fraud", EntityType: "concept"})
	require.NoError(t, err)
	mem, err := store.Remember(ctx, &memory.Memory{
		AgentID:    agentID,
		Content:    "Fraud patterns cluster around midnight transactions",
		Summary:    "midnight fraud",
		MemoryType: "fact",
		Salience:   0.8,
		EntityIDs:  []string{ent.ID},
	})
	require.NoError(t, err)
	return ent, mem
}

// keySpyStore records the agent ID of every identity-bearing read the agent makes.
type keySpyStore struct {
	memory.GraphMemoryStore
	mu    sync.Mutex
	calls map[string][]string // call kind -> agent IDs seen
}

func newKeySpyStore(inner memory.GraphMemoryStore) *keySpyStore {
	return &keySpyStore{GraphMemoryStore: inner, calls: map[string][]string{}}
}

func (s *keySpyStore) note(kind, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[kind] = append(s.calls[kind], id)
}

func (s *keySpyStore) SearchEntities(ctx context.Context, agentID, query string, limit int) ([]*memory.Entity, error) {
	s.note("SearchEntities", agentID)
	return s.GraphMemoryStore.SearchEntities(ctx, agentID, query, limit)
}

func (s *keySpyStore) ContextFor(ctx context.Context, opts memory.ContextForOpts) (*memory.EntityRecall, error) {
	s.note("ContextFor", opts.AgentID)
	return s.GraphMemoryStore.ContextFor(ctx, opts)
}

func (s *keySpyStore) Recall(ctx context.Context, opts memory.RecallOpts) ([]*memory.Memory, error) {
	s.note("Recall", opts.AgentID)
	return s.GraphMemoryStore.Recall(ctx, opts)
}

func TestGraphMemoryKey_PrefersID(t *testing.T) {
	a := &Agent{config: &Config{Name: "name", GraphMemoryAgentID: "stable-id"}, id: "instance-uuid"}
	assert.Equal(t, "stable-id", a.graphMemoryKey())
}

func TestGraphMemoryKey_FallsBackToName(t *testing.T) {
	// The agent's own id is a fresh UUID per instance and must never become the key.
	a := &Agent{config: &Config{Name: "name"}, id: "instance-uuid"}
	assert.Equal(t, "name", a.graphMemoryKey())
}

// Entity search, entity-scoped ContextFor and unscoped Recall must all read
// under the key, and the injected block must carry the memory.
func TestGraphMemory_RecallUsesKey(t *testing.T) {
	inner := newTestGraphMemoryStore(t)
	graphKeySeed(t, inner, "u1")
	spy := newKeySpyStore(inner)

	out := graphKeyInject(graphKeyReader(spy, "old", "u1"))
	assert.Contains(t, out, "midnight transactions", "recall must find rows keyed by the ID")

	for _, kind := range []string{"SearchEntities", "ContextFor", "Recall"} {
		ids := spy.calls[kind]
		require.NotEmpty(t, ids, "%s was never called", kind)
		for _, id := range ids {
			assert.Equal(t, "u1", id, "%s must read under the key, not the Name", kind)
		}
	}
}

// Extraction writes entities, edges and memories under the key.
func TestGraphMemory_ExtractionWritesUnderKey(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	a := graphKeyTestAgent(t, store, "old", "u1")

	a.extractGraphMemoryAsync(ctx, "key-session")

	entity, err := store.GetEntity(ctx, "u1", "ilsun")
	require.NoError(t, err)
	assert.Equal(t, "person", entity.EntityType)
	found, err := store.Recall(ctx, memory.RecallOpts{AgentID: "u1", Query: "fraud", Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, found, "extraction must write memories under the ID")
	assert.Equal(t, "u1", found[0].MemoryAgentID)

	under, _, err := store.ListEntities(ctx, "old", "", 50, 0)
	require.NoError(t, err)
	assert.Empty(t, under, "no entities under the Name")
	oldRows, err := store.Recall(ctx, memory.RecallOpts{AgentID: "old", Query: "fraud", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, oldRows, "no memories under the Name")
}

// The registered graph_memory tool remembers and recalls under the key.
func TestGraphMemory_ToolUsesKey(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	a := graphKeyTestAgent(t, store, "old", "u1")

	graphKeyToolCall(t, a, map[string]interface{}{"action": "remember", "content": "Marcus reviews tea prices"})

	tea, err := store.Recall(ctx, memory.RecallOpts{AgentID: "u1", Query: "tea", Limit: 10})
	require.NoError(t, err)
	assert.NotEmpty(t, tea, "tool remember must write under the ID")
	oldRows, err := store.Recall(ctx, memory.RecallOpts{AgentID: "old", Query: "tea", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, oldRows, "tool must not write under the Name")

	assert.Contains(t, graphKeyToolCall(t, a, map[string]interface{}{"action": "recall", "query": "tea"}), "tea prices")
}

// With an ID, a renamed agent keeps its memory; without one, the rename orphans it.
func TestGraphMemory_RenameKeepsMemoryWhenIDSet(t *testing.T) {
	ctx := context.Background()

	withID := newTestGraphMemoryStore(t)
	graphKeyTestAgent(t, withID, "A", "u1").extractGraphMemoryAsync(ctx, "key-session")
	assert.Contains(t, graphKeyInject(graphKeyReader(withID, "B", "u1")), "fraud patterns",
		"renamed agent with the same ID must still recall")

	noID := newTestGraphMemoryStore(t)
	graphKeyTestAgent(t, noID, "A", "").extractGraphMemoryAsync(ctx, "key-session")
	assert.Contains(t, graphKeyInject(graphKeyReader(noID, "A", "")), "fraud patterns", "same Name still recalls")
	assert.Empty(t, graphKeyInject(graphKeyReader(noID, "B", "")),
		"without an ID, a rename orphans the memory (unchanged behaviour)")
}

// multiHopRecall finds the user entity and its neighbours' memories under the key.
func TestGraphMemory_MultiHopUsesKey(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	user, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: "u1", Name: "ilsun", EntityType: "person", PropertiesJSON: `{"is_user":true}`,
	})
	require.NoError(t, err)
	neighbor, mem := graphKeySeed(t, store, "u1")
	_, err = store.Relate(ctx, &memory.Edge{AgentID: "u1", SourceID: user.ID, TargetID: neighbor.ID, Relation: "KNOWS_ABOUT"})
	require.NoError(t, err)

	got := graphKeyReader(store, "old", "u1").multiHopRecall(ctx, map[string]bool{}, 2000)
	require.Len(t, got, 1, "multi-hop must traverse the graph keyed by the ID")
	assert.Equal(t, mem.ID, got[0].ID)

	assert.Empty(t, graphKeyReader(store, "old", "").multiHopRecall(ctx, map[string]bool{}, 2000),
		"an agent keyed by Name must not see the ID's graph")
}

// Evicting a skill boosts the memories about it; the lookup must use the key.
func TestGraphMemory_SkillEvictionUsesKey(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	_, mem := graphKeySeed(t, store, "u1") // entity "fraud" matches the skill name below

	orch := skills.NewOrchestrator(skills.NewLibrary(), skills.WithMaxConcurrentSkills(1))
	cfg := DefaultConfig()
	cfg.Name = "old"
	cfg.GraphMemoryAgentID = "u1"
	_ = NewAgent(&mockBackend{}, &searchQueryLLM{content: "x"},
		WithConfig(cfg), WithSkillOrchestrator(orch),
		WithGraphMemoryStore(store, &loomv1.GraphMemoryConfig{Enabled: true}))

	before, err := store.GetMemory(ctx, "u1", mem.ID)
	require.NoError(t, err)

	orch.ActivateSkill("s", &skills.Skill{Name: "fraud"}, "test", "", 0.1)
	orch.ActivateSkill("s", &skills.Skill{Name: "other"}, "test", "", 0.9) // evicts "fraud"

	require.Eventually(t, func() bool {
		after, err := store.GetMemory(ctx, "u1", mem.ID)
		return err == nil && after.AccessCount > before.AccessCount
	}, 3*time.Second, 20*time.Millisecond, "eviction must touch memories keyed by the ID")
}

func TestWithGraphMemoryAgentID(t *testing.T) {
	withID := NewAgent(nil, nil, WithName("name"), WithGraphMemoryAgentID("stable-id"))
	assert.Equal(t, "stable-id", withID.graphMemoryKey())
	assert.Equal(t, "name", withID.config.Name, "the option must not touch the name")

	without := NewAgent(nil, nil, WithName("name"))
	assert.Equal(t, "name", without.graphMemoryKey())
}

// The recall span records which memory partition was queried.
func TestGraphMemory_RecallSpanCarriesKey(t *testing.T) {
	for _, tc := range []struct{ name, id, want string }{
		{"id set", "u1", "u1"},
		{"falls back to name", "", "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &spanKeepingTracer{}
			a := graphKeyReader(newTestGraphMemoryStore(t), "old", tc.id)
			a.tracer = tr
			graphKeyInject(a)

			var got interface{}
			found := false
			tr.mu.Lock()
			for _, sp := range tr.spans {
				if sp.Name == "graph_memory.recall" {
					found = true
					got = sp.Attributes["graph_memory.agent_key"]
				}
			}
			tr.mu.Unlock()
			require.True(t, found, "no graph_memory.recall span")
			assert.Equal(t, tc.want, got)
		})
	}
}
