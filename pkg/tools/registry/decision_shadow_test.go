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

package registry

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// rerankLLM returns a fixed rerank JSON keeping candidates 1 and 3.
type rerankLLM struct{}

func (rerankLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: `[{"index": 1, "score": 0.9, "reason": "match"}, {"index": 3, "score": 0.5, "reason": "partial"}]`}, nil
}
func (rerankLLM) Name() string  { return "rerank-fake" }
func (rerankLLM) Model() string { return "fake-1" }

type memShadowStore struct {
	mu   sync.Mutex
	rows []*loomv1.DecisionShadowRecord
}

func (m *memShadowStore) RecordShadow(_ context.Context, r []*loomv1.DecisionShadowRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, r...)
	return nil
}
func (m *memShadowStore) QueryShadow(context.Context, decision.ShadowQuery) ([]*loomv1.DecisionShadowRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*loomv1.DecisionShadowRecord(nil), m.rows...), nil
}

func candidates() []*loomv1.ToolSearchResult {
	return []*loomv1.ToolSearchResult{
		{Tool: &loomv1.IndexedTool{Name: "slack_send", Description: "Send a Slack message"}, Confidence: 0.4},
		{Tool: &loomv1.IndexedTool{Name: "email_send", Description: "Send an email"}, Confidence: 0.3},
		{Tool: &loomv1.IndexedTool{Name: "webhook_post", Description: "POST to a webhook"}, Confidence: 0.2},
	}
}

// binding builds a per-agent decision context the way WithDecision does,
// with its own WaitGroup.
func binding(t *testing.T, m *mock.Decider, store decision.ShadowStore) (*SearchTool, *Registry) {
	t.Helper()
	reg, err := New(Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: rerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	st := NewSearchTool(reg, WithDecision(DecisionBinding{
		Router:   decision.NewRouter(m),
		Recorder: decision.NewShadowRecorder(store, nil),
	}))
	return st, reg
}

func TestRerankWithLLMShadowRecordsAgainstKept(t *testing.T) {
	// Decider says c0 relevant, c1 not, c2 relevant → agrees with the LLM on all three.
	m := mock.New().AnswerNoul("c0", 0.95).AnswerNoul("c1", 0.1).AnswerNoul("c2", 0.7)
	store := &memShadowStore{}
	st, reg := binding(t, m, store)

	ctx := decision.WithSessionID(context.Background(), "sess-9")
	out := reg.rerankWithLLM(ctx, "notify the team on slack", "", candidates(), st.decision)
	require.Len(t, out, 2, "LLM kept indexes 1 and 3")
	st.WaitDecisionShadows()

	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, r := range rows {
		assert.Equal(t, sites.SiteToolSearchRerank, r.Site)
		assert.Equal(t, "sess-9", r.SessionId)
		assert.Equal(t, sites.ReferenceSourceLLMRerank, r.ReferenceSource)
		assert.Equal(t, r.ReferenceAnswer, r.CandidateAnswer, "question %s", r.QuestionId)
		assert.Equal(t, "mock", r.Provider)
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, r.Path, "no band: shadow only")
	}
	require.Equal(t, 1, m.CallCount())
	req := m.Calls()[0]
	assert.NotContains(t, req.State.String(), "Confidence", "state carries names and descriptions, not scores")
}

func TestRerankWithLLMNoDecisionIsUnchanged(t *testing.T) {
	reg, err := New(Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: rerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	out := reg.rerankWithLLM(context.Background(), "q", "", candidates(), nil)
	assert.Len(t, out, 2)
}

func TestWithDecisionNilStoreTracesOnly(t *testing.T) {
	m := mock.New().AnswerNoul("c0", 0.9).AnswerNoul("c1", 0.9).AnswerNoul("c2", 0.9)
	st, reg := binding(t, m, nil)
	_ = reg.rerankWithLLM(context.Background(), "q", "", candidates(), st.decision)
	st.WaitDecisionShadows()
	assert.Equal(t, 1, m.CallCount(), "decider still evaluated without a store")
}

func TestWithDecisionNilRouterIsNoDecision(t *testing.T) {
	reg, err := New(Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: rerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	st := NewSearchTool(reg, WithDecision(DecisionBinding{}))
	assert.Nil(t, st.decision)
}

// sharedRegistryWithTools is one tool registry, as looms serve has, with
// three tools FTS can find for "send".
func sharedRegistryWithTools(t *testing.T) *Registry {
	t.Helper()
	reg, err := New(Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: rerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	ctx := context.Background()
	for _, c := range candidates() {
		tool := c.Tool
		tool.Id = "custom:" + tool.Name
		tool.Source = loomv1.ToolSource_TOOL_SOURCE_CUSTOM
		require.NoError(t, reg.RegisterTool(ctx, tool))
	}
	return reg
}

func search(t *testing.T, st *SearchTool) {
	t.Helper()
	res, err := st.Execute(context.Background(), map[string]interface{}{"query": "send", "mode": "balanced"})
	require.NoError(t, err)
	require.True(t, res.Success)
	st.WaitDecisionShadows()
}

// Review #409 F2 / #410 blocking 2: with one shared registry, an agent's
// decision layer must reach only that agent's tool_search. Agent A has no
// decision layer and agent B has one; A's search must not reach B's decider.
func TestToolSearchDecisionIsPerAgent(t *testing.T) {
	reg := sharedRegistryWithTools(t)
	decB := mock.New().AnswerNoul("c0", 0.9).AnswerNoul("c1", 0.9).AnswerNoul("c2", 0.9)
	storeB := &memShadowStore{}

	stB := NewSearchTool(reg, WithDecision(DecisionBinding{Router: decision.NewRouter(decB), Recorder: decision.NewShadowRecorder(storeB, nil)}))
	stA := NewSearchTool(reg) // A: no decision layer

	search(t, stA)
	assert.Equal(t, 0, decB.CallCount(), "A's search reached B's decider")
	assert.Empty(t, storeB.rows, "A's search wrote rows to B's store")

	search(t, stB)
	assert.Equal(t, 1, decB.CallCount(), "B's own search uses B's decider")

	// A later agent C with its own decision block does not rewire B.
	decC := mock.New().AnswerNoul("c0", 0.1).AnswerNoul("c1", 0.1).AnswerNoul("c2", 0.1)
	stC := NewSearchTool(reg, WithDecision(DecisionBinding{Router: decision.NewRouter(decC)}))
	search(t, stB)
	assert.Equal(t, 2, decB.CallCount(), "B still uses B's decider after C was built")
	assert.Equal(t, 0, decC.CallCount(), "B's search reached C's decider")
	search(t, stA)
	assert.Equal(t, 0, decC.CallCount(), "A's search reached C's decider")
	search(t, stC)
	assert.Equal(t, 1, decC.CallCount())
	assert.Equal(t, 2, decB.CallCount())
}

// The gRPC SearchTools path (Registry.Search) belongs to no agent and
// consults no decider.
func TestRegistrySearchConsultsNoDecider(t *testing.T) {
	reg := sharedRegistryWithTools(t)
	dec := mock.New().AnswerNoul("c0", 0.9).AnswerNoul("c1", 0.9).AnswerNoul("c2", 0.9)
	_ = NewSearchTool(reg, WithDecision(DecisionBinding{Router: decision.NewRouter(dec)}))
	_, err := reg.Search(context.Background(), &loomv1.SearchToolsRequest{Query: "send", Mode: loomv1.SearchMode_SEARCH_MODE_BALANCED})
	require.NoError(t, err)
	assert.Equal(t, 0, dec.CallCount())
}
