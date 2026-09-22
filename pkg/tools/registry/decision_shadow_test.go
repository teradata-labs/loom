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

func TestRerankWithLLMShadowRecordsAgainstKept(t *testing.T) {
	reg, err := New(Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: rerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })

	// Decider says c0 relevant, c1 not, c2 relevant → agrees with the LLM on all three.
	m := mock.New().AnswerNoul("c0", 0.95).AnswerNoul("c1", 0.1).AnswerNoul("c2", 0.7)
	store := &memShadowStore{}
	reg.SetDecisionRouter(decision.NewRouter(m), store)

	ctx := decision.WithSessionID(context.Background(), "sess-9")
	out := reg.rerankWithLLM(ctx, "notify the team on slack", "", candidates())
	require.Len(t, out, 2, "LLM kept indexes 1 and 3")
	reg.WaitDecisionShadows()

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

func TestRerankWithLLMNoRouterIsUnchanged(t *testing.T) {
	reg, err := New(Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: rerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	out := reg.rerankWithLLM(context.Background(), "q", "", candidates())
	assert.Len(t, out, 2)
	reg.WaitDecisionShadows() // no goroutines; returns immediately
}

func TestSetDecisionRouterNilStoreTracesOnly(t *testing.T) {
	reg, err := New(Config{DBPath: filepath.Join(t.TempDir(), "tools.db"), LLM: rerankLLM{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reg.Close() })
	m := mock.New().AnswerNoul("c0", 0.9).AnswerNoul("c1", 0.9).AnswerNoul("c2", 0.9)
	reg.SetDecisionRouter(decision.NewRouter(m), nil)
	_ = reg.rerankWithLLM(context.Background(), "q", "", candidates())
	reg.WaitDecisionShadows()
	assert.Equal(t, 1, m.CallCount(), "decider still evaluated without a store")
}
