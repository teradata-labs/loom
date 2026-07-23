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

package agent

// D-2 acceptance test for the graph-memory half of tail-note-single-user-role-local-only:
// "a beat needing ... graph-memory recall carries [it] only as a single user-role tail note
// appended to the loop's local slice for that one beat — never stored via
// session.AddMessage, never system-role." Before D-2, injectGraphMemoryContext persisted a
// Role:"system" message per user turn via session.AddMessage (agent.go, deleted by Seam 2);
// after D-2 its recall is rewired into buildBeatTailNote (Seam 3).
//
// Uses the same real SQLite-backed graph memory store as graph_memory_extractor_test.go
// (newTestGraphMemoryStore, same package) — no mock store, per this suite's real-runtime
// convention for the agent's actual memory backends.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// graphRecallCapturingLLM answers the two internal LLM sub-calls injectGraphMemoryContext's
// logic makes (search-query distillation, candidate reranking) deterministically, and
// separately captures every OTHER ("main" conversation loop) call it receives — the call
// site whose tail note this test inspects.
type graphRecallCapturingLLM struct {
	mu        sync.Mutex
	mainCalls [][]Message
}

func (m *graphRecallCapturingLLM) Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	if len(messages) == 1 && strings.Contains(messages[0].Content, "Distill this message") {
		return &LLMResponse{Content: "sneakers", Usage: Usage{InputTokens: 5, OutputTokens: 2}}, nil
	}
	if len(messages) == 1 && strings.Contains(messages[0].Content, "Candidate memories:") {
		return &LLMResponse{Content: "1", Usage: Usage{InputTokens: 5, OutputTokens: 2}}, nil
	}

	m.mu.Lock()
	cp := make([]Message, len(messages))
	copy(cp, messages)
	m.mainCalls = append(m.mainCalls, cp)
	m.mu.Unlock()

	return &LLMResponse{Content: "final response", Usage: Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

func (m *graphRecallCapturingLLM) Name() string  { return "graph-recall-capturing" }
func (m *graphRecallCapturingLLM) Model() string { return "graph-recall-v1" }

func (m *graphRecallCapturingLLM) getMainCalls() [][]Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]Message, len(m.mainCalls))
	copy(out, m.mainCalls)
	return out
}

func TestAgent_GraphMemoryRecall_BecomesTailNote_NeverPersisted(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	const agentName = "graph-tail-agent"

	entity, err := store.CreateEntity(ctx, &memory.Entity{
		AgentID: agentName, Name: "sneakers", EntityType: "concept",
	})
	require.NoError(t, err)
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID: agentName, Content: "The white Adidas sneakers were cleaned last weekend.",
		MemoryType: "observation", Salience: 0.9, EntityIDs: []string{entity.ID},
	})
	require.NoError(t, err)

	llm := &graphRecallCapturingLLM{}
	cfg := DefaultConfig()
	cfg.Name = agentName
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg),
		WithGraphMemoryStore(store, &loomv1.GraphMemoryConfig{Enabled: true}))

	_, err = ag.Chat(ctx, "graph-tail-session", "tell me about my sneakers")
	require.NoError(t, err)

	calls := llm.getMainCalls()
	require.NotEmpty(t, calls, "the main conversation call must have been captured")
	tail := calls[0][len(calls[0])-1]
	assert.Equal(t, "user", tail.Role, "the graph-memory recall must reach the LLM as a user-role tail note")
	assert.Contains(t, tail.Content, "sneakers", "the recalled memory content must be present in the tail note")

	session, ok := ag.memory.GetSession("graph-tail-session")
	require.True(t, ok)
	for _, m := range session.Messages {
		assert.NotEqual(t, "system", m.Role,
			"graph-memory recall must never be persisted as a system message (the deleted injectGraphMemoryContext AddMessage call)")
		assert.NotContains(t, m.Content, "Graph Memory Context",
			"the old graph-memory system-message marker must never appear in persisted messages")
	}
}
