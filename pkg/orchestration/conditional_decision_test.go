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

package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/observability"
)

// calls reports how many chats the mock LLM served.
func (m *mockLLMProvider) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

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

// newDecisionAgent is createMockAgent with a decision router injected.
func newDecisionAgent(t *testing.T, name string, llm agent.LLMProvider, dec decision.Decider, store decision.ShadowStore, opts ...decision.RouterOption) *agent.Agent {
	t.Helper()
	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	return agent.NewAgent(&mockBackend{}, llm,
		agent.WithName(name),
		agent.WithConfig(cfg),
		agent.WithDecisionRouter(decision.NewRouter(dec, opts...)),
		agent.WithDecisionShadowStore(store))
}

func decisionClassifier(t *testing.T, llm agent.LLMProvider, dec decision.Decider, store decision.ShadowStore, opts ...decision.RouterOption) *agent.Agent {
	t.Helper()
	return newDecisionAgent(t, "classifier", llm, dec, store, opts...)
}

func liveBranchBand(actMin float64) decision.RouterOption {
	return decision.WithBands([]*loomv1.DecisionBand{{Site: sites.SiteWorkflowBranch, ActMin: actMin}})
}

func forkJoin(prompt string) *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
		Prompt: prompt, AgentIds: []string{"worker"}, MergeStrategy: loomv1.MergeStrategy_FIRST,
	}}}
}

func conditionalPattern(withDefault bool) *loomv1.WorkflowPattern {
	c := &loomv1.ConditionalPattern{
		ConditionAgentId: "classifier",
		ConditionPrompt:  "Classify this issue: the app crashes on start",
		Branches: map[string]*loomv1.WorkflowPattern{
			"bug":     forkJoin("Fix the bug"),
			"feature": forkJoin("Build the feature"),
		},
	}
	if withDefault {
		c.DefaultBranch = forkJoin("Triage manually")
	}
	return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Conditional{Conditional: c}}
}

func runConditional(t *testing.T, classifier *agent.Agent, pattern *loomv1.WorkflowPattern) (*loomv1.WorkflowResult, *mockLLMProvider, error) {
	t.Helper()
	workerLLM := newMockLLMProvider("done")
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("classifier", classifier)
	orch.RegisterAgent("worker", createMockAgent(t, "worker", workerLLM))
	res, err := orch.ExecutePattern(context.Background(), pattern)
	return res, workerLLM, err
}

func TestConditionalDecision_LiveBandSkipsConditionAgent(t *testing.T) {
	t.Parallel()
	classifierLLM := newMockLLMProvider("feature") // would pick the wrong branch if consulted
	dec := decisionmock.New().AnswerChoice(sites.QBranch, map[string]float64{"bug": 0.9, "feature": 0.05, sites.BranchNoneOfThese: 0.05})
	store := &memShadowStore{}
	classifier := decisionClassifier(t, classifierLLM, dec, store, liveBranchBand(0.8))

	res, worker, err := runConditional(t, classifier, conditionalPattern(false))
	require.NoError(t, err)
	assert.Equal(t, "bug", res.Metadata["selected_branch"])
	assert.Equal(t, "bug", res.Metadata["condition_result"])
	assert.Equal(t, 0, classifierLLM.calls(), "the condition agent's turn was skipped")
	assert.Equal(t, 1, worker.calls(), "the branch still ran")
	assert.Equal(t, 1, dec.CallCount())

	classifier.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sites.SiteWorkflowBranch, rows[0].Site)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DECIDER, rows[0].Path)
	assert.Equal(t, "bug", rows[0].CandidateAnswer)
	assert.Equal(t, "", rows[0].ReferenceAnswer, "acted: nothing to compare against")
}

func TestConditionalDecision_NoneOfTheseTakesDefaultOnlyWhenPresent(t *testing.T) {
	t.Parallel()
	none := map[string]float64{"bug": 0.02, "feature": 0.03, sites.BranchNoneOfThese: 0.95}

	t.Run("default present: acted", func(t *testing.T) {
		t.Parallel()
		classifierLLM := newMockLLMProvider("bug")
		dec := decisionmock.New().AnswerChoice(sites.QBranch, none)
		classifier := decisionClassifier(t, classifierLLM, dec, &memShadowStore{}, liveBranchBand(0.8))
		res, _, err := runConditional(t, classifier, conditionalPattern(true))
		require.NoError(t, err)
		assert.Equal(t, "default", res.Metadata["selected_branch"])
		assert.Equal(t, 0, classifierLLM.calls())
	})

	t.Run("no default: agent decides", func(t *testing.T) {
		t.Parallel()
		classifierLLM := newMockLLMProvider("bug")
		dec := decisionmock.New().AnswerChoice(sites.QBranch, none)
		store := &memShadowStore{}
		classifier := decisionClassifier(t, classifierLLM, dec, store, liveBranchBand(0.8))
		res, _, err := runConditional(t, classifier, conditionalPattern(false))
		require.NoError(t, err)
		assert.Equal(t, "bug", res.Metadata["selected_branch"])
		assert.Equal(t, 1, classifierLLM.calls(), "none_of_these without a default is not actionable")
		assert.Equal(t, 1, dec.CallCount(), "the live answer is recorded, not re-asked")

		classifier.WaitDecisionShadows()
		rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, sites.BranchNoneOfThese, rows[0].CandidateAnswer)
		assert.Equal(t, "bug", rows[0].ReferenceAnswer)
		assert.Equal(t, sites.ReferenceSourceSelectBranch, rows[0].ReferenceSource)
	})
}

func TestConditionalDecision_BelowBandFallsBackToAgent(t *testing.T) {
	t.Parallel()
	classifierLLM := newMockLLMProvider("feature")
	// (3·0.5−1)/2 = 0.25, well under the band.
	dec := decisionmock.New().AnswerChoice(sites.QBranch, map[string]float64{"bug": 0.5, "feature": 0.4, sites.BranchNoneOfThese: 0.1})
	store := &memShadowStore{}
	classifier := decisionClassifier(t, classifierLLM, dec, store, liveBranchBand(0.8))

	res, _, err := runConditional(t, classifier, conditionalPattern(false))
	require.NoError(t, err)
	assert.Equal(t, "feature", res.Metadata["selected_branch"], "the agent decided")
	assert.Equal(t, 1, classifierLLM.calls())

	classifier.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, "bug", rows[0].CandidateAnswer)
	assert.Equal(t, "feature", rows[0].ReferenceAnswer, "the disagreement is recorded, not acted on")
}

func TestConditionalDecision_ShadowBandRecordsAgainstAgent(t *testing.T) {
	t.Parallel()
	classifierLLM := newMockLLMProvider("garbage answer")
	dec := decisionmock.New().AnswerChoice(sites.QBranch, map[string]float64{"bug": 0.9, "feature": 0.05, sites.BranchNoneOfThese: 0.05})
	store := &memShadowStore{}
	classifier := decisionClassifier(t, classifierLLM, dec, store) // no band: shadow

	res, _, err := runConditional(t, classifier, conditionalPattern(true))
	require.NoError(t, err)
	assert.Equal(t, "default", res.Metadata["selected_branch"])
	assert.Equal(t, 1, classifierLLM.calls())

	classifier.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, "bug", rows[0].CandidateAnswer)
	assert.Equal(t, sites.BranchNoneOfThese, rows[0].ReferenceAnswer, "default branch renders as none_of_these")
}

func TestConditionalDecision_DeciderErrorFallsBack(t *testing.T) {
	t.Parallel()
	classifierLLM := newMockLLMProvider("bug")
	dec := decisionmock.New().SetError(decision.ErrOverloaded)
	classifier := decisionClassifier(t, classifierLLM, dec, &memShadowStore{}, liveBranchBand(0.8))

	res, _, err := runConditional(t, classifier, conditionalPattern(false))
	require.NoError(t, err)
	assert.Equal(t, "bug", res.Metadata["selected_branch"])
	assert.Equal(t, 1, classifierLLM.calls(), "a decider error never costs the workflow anything")
	classifier.WaitDecisionShadows()
}
