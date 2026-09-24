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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/session"
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
		{ID: "m1", Content: "the user drives a blue car", Source: "conversation", SourceID: "sess-1"},
		{ID: "m2", Content: "quarterly revenue table"},
		{ID: "m3", Content: "GPS malfunction after March service", Source: memory.SourceAutoExtracted, SourceID: "sess-3"},
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
	assert.Equal(t, "session:sess-1", byQ["c0"].Subject, "a conversation memory's subject is its source session")
	assert.Equal(t, "memory:m2", byQ["c1"].Subject, "a memory without a source session is identified by id")
	assert.Equal(t, "session:sess-3", byQ["c2"].Subject, "an auto-extracted memory points at the session it came from")
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

// countingRerankLLM is rerankReplyLLM that counts chats, so a test can prove the
// generative rerank was skipped.
type countingRerankLLM struct {
	reply string
	calls atomic.Int32
}

func (l *countingRerankLLM) Chat(context.Context, []types.Message, []shuttle.Tool) (*types.LLMResponse, error) {
	l.calls.Add(1)
	return &types.LLMResponse{Content: l.reply, StopReason: "end_turn"}, nil
}
func (*countingRerankLLM) Name() string  { return "mock" }
func (*countingRerankLLM) Model() string { return "mock-model" }

func liveRerankBand(actMin float64) decision.RouterOption {
	return decision.WithBands([]*loomv1.DecisionBand{{
		Site:      sites.SiteRecallRerank,
		ActMin:    actMin,
		Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION,
	}})
}

func TestRerankMemoriesLiveBandSkipsLLM(t *testing.T) {
	t.Parallel()
	llm := &countingRerankLLM{reply: "1,2,3"}
	// c0 confident relevant, c1 confident irrelevant, c2 uncertain (kept).
	dec := decisionmock.New().AnswerNoul("c0", 0.95).AnswerNoul("c1", 0.05).AnswerNoul("c2", 0.55)
	store := &memShadowStore{}
	ag := NewAgent(nil, llm, WithName("dec"),
		WithDecisionRouter(decision.NewRouter(dec, liveRerankBand(0.8))),
		WithDecisionShadowStore(store))

	candidates := []*memory.Memory{
		{ID: "m1", Content: "the user drives a blue car"},
		{ID: "m2", Content: "quarterly revenue table"},
		{ID: "m3", Content: "GPS malfunction after March service"},
	}
	ctx := decision.WithSessionID(context.Background(), "sess-live")
	kept := ag.rerankMemories(ctx, "what happened to my car's GPS?", candidates)
	require.Len(t, kept, 2)
	assert.Equal(t, "m1", kept[0].ID)
	assert.Equal(t, "m3", kept[1].ID, "uncertain candidate is kept, never dropped")
	assert.Equal(t, int32(0), llm.calls.Load(), "the generative rerank was skipped")

	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: sites.SiteRecallRerank})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, r := range rows {
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DECIDER, r.Path)
		assert.Equal(t, "", r.ReferenceAnswer, "acted: nothing to compare against")
	}
}

func TestRerankMemoriesLiveBandNotClearedFallsBackAndRecords(t *testing.T) {
	t.Parallel()
	llm := &countingRerankLLM{reply: "2"}
	// Every answer is a coin flip: nothing clears the band.
	dec := decisionmock.New().AnswerNoul("c0", 0.5).AnswerNoul("c1", 0.5)
	store := &memShadowStore{}
	ag := NewAgent(nil, llm, WithName("dec"),
		WithDecisionRouter(decision.NewRouter(dec, liveRerankBand(0.8))),
		WithDecisionShadowStore(store))

	candidates := []*memory.Memory{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}}
	ctx := decision.WithSessionID(context.Background(), "sess-fb")
	kept := ag.rerankMemories(ctx, "q", candidates)
	require.Len(t, kept, 1)
	assert.Equal(t, "b", kept[0].ID, "the LLM decided")
	assert.Equal(t, int32(1), llm.calls.Load())
	assert.Equal(t, 1, dec.CallCount(), "the live answer is reused, not re-asked")

	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: sites.SiteRecallRerank})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byQ := map[string]*loomv1.DecisionShadowRecord{}
	for _, r := range rows {
		byQ[r.QuestionId] = r
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, r.Path)
		assert.Equal(t, sites.ReferenceSourceLLMRerank, r.ReferenceSource)
	}
	assert.Equal(t, "false", byQ["c0"].ReferenceAnswer)
	assert.Equal(t, "true", byQ["c1"].ReferenceAnswer)
}

func TestExportedDecisionHelpersNoRouter(t *testing.T) {
	t.Parallel()
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("off"))
	req, err := sites.BranchRequest("p", []string{"a", "b"})
	require.NoError(t, err)
	out := ag.LiveDecide(context.Background(), "s", req)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DISABLED, out.Path)
	assert.False(t, out.Act())
	ag.RecordDecisionAsync(context.Background(), "s", req, out, nil)
	ag.RunDecisionShadow(context.Background(), "s", req, nil)
	ag.WaitDecisionShadows()
}

// A shadow row's subject is the memory's source session whenever the memory
// carries one, for both the extractor and the graph_memory tool, so a grader
// that knows the evidence sessions can score each keep-or-drop decision.
func TestMemorySubjectsUseSessionProvenance(t *testing.T) {
	candidates := []*memory.Memory{
		{ID: "m1", Source: memory.SourceAutoExtracted, SourceID: "sess-a"},
		{ID: "m2", Source: memory.SourceAgent, SourceID: "sess-b"},
		{ID: "m3", Source: "conversation", SourceID: "sess-c"},
		{ID: "m4", Source: memory.SourceAutoExtracted}, // legacy row, no provenance
		{ID: "m5", Source: "task", SourceID: "task-9"}, // SourceID is not a session
		nil,
	}
	got := memorySubjects(candidates)
	assert.Equal(t, []string{"session:sess-a", "session:sess-b", "session:sess-c", "memory:m4", "memory:m5", ""}, got)
}

// A live turn carries the agent session through pkg/session only. The recall
// shadow rows must be keyed to it; the LongMemEval A/B recorded 66,000 rows
// with an empty session id before this was checked.
func TestRerankMemoriesShadowRowsCarryAgentSession(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().AnswerNoul("c0", 0.9)
	store := &memShadowStore{}
	ag := NewAgent(nil, rerankReplyLLM{reply: "1"}, WithName("dec"),
		WithDecisionRouter(decision.NewRouter(dec)),
		WithDecisionShadowStore(store))
	ctx := session.WithSessionID(context.Background(), "sess-agent-9")
	_ = ag.rerankMemories(ctx, "q", []*memory.Memory{{ID: "m1", Content: "x"}})
	ag.WaitDecisionShadows()
	rows, err := store.QueryShadow(ctx, decision.ShadowQuery{Site: sites.SiteRecallRerank})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "sess-agent-9", rows[0].SessionId)
}
