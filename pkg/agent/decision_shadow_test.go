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

package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// rerankReplyLLM answers every chat with a fixed rerank reply.
type rerankReplyLLM struct{ reply string }

func (l rerankReplyLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: l.reply, StopReason: "end_turn"}, nil
}
func (rerankReplyLLM) Name() string  { return "mock" }
func (rerankReplyLLM) Model() string { return "mock-model" }

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
func (m *memShadowStore) QueryShadow(_ context.Context, q decision.ShadowQuery) ([]*loomv1.DecisionShadowRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*loomv1.DecisionShadowRecord
	for _, r := range m.rows {
		if q.Site == "" || r.Site == q.Site {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestInitDecisionRouterJev(t *testing.T) {
	t.Setenv(jev.EnvTypeSafeAPIKey, "")
	t.Setenv(jev.EnvJevAPIKey, "")
	t.Setenv(jev.EnvAIGatewayAPIKey, "jv_test_not_a_real_key")
	ag := NewAgent(nil, rerankReplyLLM{reply: "none"}, WithName("dec"),
		WithDecisionConfig(&loomv1.DecisionConfig{Provider: "jev", Model: "typesafe-ai/jev", AllowAlias: true, TimeoutMs: 1200}, nil))
	require.NotNil(t, ag.DecisionRouter(), "a gateway key enables the jev provider")
	inst, ok := ag.DecisionRouter().Decider().(*decision.Instrumented)
	require.True(t, ok)
	client, ok := inst.Unwrap().(*jev.Client)
	require.True(t, ok)
	assert.Equal(t, "jev", client.Name())
	assert.Equal(t, jev.VercelGatewayModel, client.Model())
	assert.Equal(t, jev.VercelGatewayBaseURL+jev.DefaultPath, client.URL(), "gateway key alone routes to the gateway")

	// A pinned TypeSafe id against the gateway is a misconfiguration: the
	// layer stays off rather than sending a model the gateway rejects.
	pinned := NewAgent(nil, rerankReplyLLM{reply: "none"}, WithName("dec"),
		WithDecisionConfig(&loomv1.DecisionConfig{Provider: "jev", Model: "jev-1.13.0"}, nil))
	assert.Nil(t, pinned.DecisionRouter())
}

func TestInitDecisionRouterFromConfig(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *loomv1.DecisionConfig
		wantEnabled  bool
		wantProvider string
	}{
		{name: "nil config is off", cfg: nil},
		{name: "provider off", cfg: &loomv1.DecisionConfig{Provider: "off"}},
		{name: "empty provider is off", cfg: &loomv1.DecisionConfig{Provider: ""}},
		{name: "mock", cfg: &loomv1.DecisionConfig{Provider: "mock"}, wantEnabled: true, wantProvider: "mock"},
		{name: "llm adapts the main LLM by default", cfg: &loomv1.DecisionConfig{Provider: "llm"}, wantEnabled: true, wantProvider: "llm:mock"},
		{name: "llm with unconfigured role falls back to main", cfg: &loomv1.DecisionConfig{Provider: "llm", LlmRole: "classifier"}, wantEnabled: true, wantProvider: "llm:mock"},
		{name: "jev without credentials stays off", cfg: &loomv1.DecisionConfig{Provider: "jev"}},
		{name: "unknown provider stays off", cfg: &loomv1.DecisionConfig{Provider: "oracle"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: the jev case depends on the credential env vars
			// being absent, and TestInitDecisionRouterJev sets them.
			ag := NewAgent(nil, rerankReplyLLM{reply: "none"}, WithName("dec"), WithDecisionConfig(tt.cfg, nil))
			if !tt.wantEnabled {
				assert.Nil(t, ag.DecisionRouter())
				assert.Equal(t, "off", ag.decisionConfigSummary())
				return
			}
			require.NotNil(t, ag.DecisionRouter())
			// Instrumented wraps the decider; unwrap to read the provider name.
			inst, ok := ag.DecisionRouter().Decider().(*decision.Instrumented)
			require.True(t, ok, "decider is wrapped in Instrumented")
			assert.Equal(t, tt.wantProvider, inst.Unwrap().Name())
			assert.Equal(t, tt.wantProvider, ag.deciderName())
		})
	}
}

func TestInitDecisionRouterBandsAndRoleLLM(t *testing.T) {
	t.Parallel()
	classifier := rerankReplyLLM{reply: "classifier"}
	ag := NewAgent(nil, rerankReplyLLM{reply: "main"},
		WithName("dec"),
		WithClassifierLLM(classifier),
		WithDecisionConfig(&loomv1.DecisionConfig{
			Provider: "llm", LlmRole: "classifier", MaxPerSession: 3,
			Bands: []*loomv1.DecisionBand{{Site: "recall.rerank", ActMin: 0.7}},
		}, nil))
	require.NotNil(t, ag.DecisionRouter())
	assert.InDelta(t, 0.7, ag.DecisionRouter().Band("recall.rerank").ActMin, 1e-9)
	assert.Equal(t, decision.ShadowBand, ag.DecisionRouter().Band("other"))
	assert.Equal(t, classifier, ag.roleLLMForDecision("classifier"))
	assert.Equal(t, ag.llm, ag.roleLLMForDecision("judge"), "unconfigured role falls back to main")
	assert.Equal(t, ag.llm, ag.roleLLMForDecision(""), "empty role is main")
}

func TestRerankMemoriesShadowsAgainstLLMChoice(t *testing.T) {
	t.Parallel()
	// The generative rerank keeps candidates 1 and 3 (1-based).
	llm := rerankReplyLLM{reply: "1,3"}
	// The decider agrees on c0 and c2, disagrees on c1 (says relevant).
	dec := decisionmock.New().AnswerNoul("c0", 0.9).AnswerNoul("c1", 0.8).AnswerNoul("c2", 0.95)
	store := &memShadowStore{}
	ag := NewAgent(nil, llm, WithName("dec"),
		WithDecisionRouter(decision.NewRouter(dec)),
		WithDecisionShadowStore(store))
	require.NotNil(t, ag.decisionRecorder)

	candidates := []*memory.Memory{
		{ID: "m1", Content: "the user drives a blue car"},
		{ID: "m2", Content: "quarterly revenue table"},
		{ID: "m3", Content: "GPS malfunction after March service"},
	}
	ctx := decision.WithSessionID(context.Background(), "sess-7")
	kept := ag.rerankMemories(ctx, "what happened to my car's GPS?", candidates)
	require.Len(t, kept, 2, "existing mechanism decides")
	assert.Equal(t, "m1", kept[0].ID)
	assert.Equal(t, "m3", kept[1].ID)

	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: sites.SiteRecallRerank})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	byQ := map[string]*loomv1.DecisionShadowRecord{}
	for _, r := range rows {
		byQ[r.QuestionId] = r
		assert.Equal(t, "sess-7", r.SessionId)
		assert.Equal(t, sites.ReferenceSourceLLMRerank, r.ReferenceSource)
		assert.Equal(t, "mock", r.Provider)
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, r.Path)
	}
	assert.Equal(t, "true", byQ["c0"].ReferenceAnswer)
	assert.Equal(t, "false", byQ["c1"].ReferenceAnswer)
	assert.Equal(t, "true", byQ["c2"].ReferenceAnswer)
	assert.Equal(t, "true", byQ["c1"].CandidateAnswer, "the one disagreement is recorded, not acted on")
	assert.Equal(t, 1, dec.CallCount())
}

func TestRerankMemoriesWithoutRouterIsUnchanged(t *testing.T) {
	t.Parallel()
	ag := NewAgent(nil, rerankReplyLLM{reply: "2"}, WithName("dec"))
	kept := ag.rerankMemories(context.Background(), "q", []*memory.Memory{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}})
	require.Len(t, kept, 1)
	assert.Equal(t, "b", kept[0].ID)
	ag.WaitDecisionShadows()
}

func TestShadowFailureKindRecordsAgainstInferErrorType(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().
		AnswerChoice(sites.QFailureKind, map[string]float64{sites.KindServerSaturated: 0.9, sites.KindOther: 0.1}).
		AnswerNoul(sites.QRetryHelps, 0.1)
	store := &memShadowStore{}
	ag := NewAgent(nil, rerankReplyLLM{reply: "none"}, WithName("dec"),
		WithDecisionRouter(decision.NewRouter(dec)), WithDecisionShadowStore(store))
	ctx := context.Background()

	// A Result-level failure with a nil Go error: the shape the breaker misses.
	ag.shadowFailureKind(ctx, "sess-1", "teradata:connect", map[string]interface{}{"host": "h", "password": "secret"},
		&shuttle.Result{Success: false, Error: &shuttle.Error{Code: "MCP_CALL_FAILED", Message: `{"code":"session_handle_budget_full"}`}}, nil)
	// A success.
	ag.shadowFailureKind(ctx, "sess-1", "teradata:execute_query", map[string]interface{}{"sql": "SELECT 1"},
		&shuttle.Result{Success: true}, nil)
	// A Go error.
	ag.shadowFailureKind(ctx, "sess-1", "flaky", nil, nil, errors.New("connection reset"))
	ag.WaitDecisionShadows()

	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: sites.SiteFailureKind})
	require.NoError(t, err)
	require.Len(t, rows, 6, "2 questions × 3 executions")

	var saturatedRef, notFailureRef int
	for _, r := range rows {
		if r.QuestionId != sites.QFailureKind {
			continue
		}
		switch r.ReferenceAnswer {
		case sites.KindServerSaturated:
			saturatedRef++
			assert.Equal(t, sites.KindServerSaturated, r.CandidateAnswer, "mock agrees on the saturated connect")
		case sites.KindNotAFailure:
			notFailureRef++
		}
	}
	assert.Equal(t, 1, saturatedRef)
	assert.Equal(t, 1, notFailureRef)

	// State never carried the input: only its digest.
	require.Equal(t, 3, dec.CallCount())
	for _, call := range dec.Calls() {
		raw := call.State.String()
		assert.NotContains(t, raw, "secret")
		assert.NotContains(t, raw, "SELECT 1")
	}
}

func TestShadowNeverBlocksOnDeciderFailure(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().SetError(decision.ErrOverloaded)
	store := &memShadowStore{}
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("dec"),
		WithDecisionRouter(decision.NewRouter(dec)), WithDecisionShadowStore(store))
	kept := ag.rerankMemories(context.Background(), "q", []*memory.Memory{{ID: "a", Content: "a"}})
	require.Len(t, kept, 1)
	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_ERROR, rows[0].Path)
	assert.Equal(t, "", rows[0].CandidateAnswer)
	assert.Equal(t, "true", rows[0].ReferenceAnswer)
}

func TestSessionIDFromContextHelper(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", sessionIDFromContext(context.Background()))
	assert.Equal(t, "", sessionIDFromContext(nil)) //nolint:staticcheck // nil ctx is the case under test
	assert.Equal(t, "d", sessionIDFromContext(decision.WithSessionID(context.Background(), "d")))
	legacy := context.WithValue(context.Background(), "session_id", "legacy") //nolint:staticcheck // the legacy string key is the case under test
	assert.Equal(t, "legacy", sessionIDFromContext(legacy))
	both := decision.WithSessionID(legacy, "typed")
	assert.Equal(t, "typed", sessionIDFromContext(both), "the typed key wins")
}
