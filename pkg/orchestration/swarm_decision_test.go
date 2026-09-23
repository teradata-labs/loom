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

func tieBreakBand(actMin float64) decision.RouterOption {
	return decision.WithBands([]*loomv1.DecisionBand{{Site: sites.SiteSwarmTieBreak, ActMin: actMin}})
}

// runTiedSwarm runs a 2–2 PostgreSQL/MongoDB swarm with the given judge.
func runTiedSwarm(t *testing.T, judge *agent.Agent) (*loomv1.WorkflowResult, error) {
	t.Helper()
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	votes := []string{
		"VOTE: PostgreSQL\nCONFIDENCE: 0.8\nREASONING: ACID compliance",
		"VOTE: PostgreSQL\nCONFIDENCE: 0.9\nREASONING: Mature ecosystem",
		"VOTE: MongoDB\nCONFIDENCE: 0.85\nREASONING: Flexible schema",
		"VOTE: MongoDB\nCONFIDENCE: 0.75\nREASONING: Easy to scale",
	}
	ids := make([]string, 0, len(votes))
	for i, v := range votes {
		id := "voter-" + string(rune('a'+i))
		orch.RegisterAgent(id, createMockAgent(t, id, newMockLLMProvider(v)))
		ids = append(ids, id)
	}
	orch.RegisterAgent("judge", judge)
	return NewSwarmExecutor(orch, &loomv1.SwarmPattern{
		Question:     "Which database should we use?",
		AgentIds:     ids,
		Strategy:     loomv1.VotingStrategy_MAJORITY,
		JudgeAgentId: "judge",
	}, "wf-tie").Execute(context.Background())
}

func TestSwarmTieBreakDecision_LiveBandSkipsJudge(t *testing.T) {
	t.Parallel()
	judgeLLM := newMockLLMProvider("PostgreSQL") // would pick the other one if consulted
	dec := decisionmock.New().AnswerChoice(sites.QTieWinner, map[string]float64{"MongoDB": 0.9, "PostgreSQL": 0.05, decision.NoneOption: 0.05})
	store := &memShadowStore{}
	judge := newDecisionAgent(t, "judge", judgeLLM, dec, store, tieBreakBand(0.8))

	res, err := runTiedSwarm(t, judge)
	require.NoError(t, err)
	assert.Equal(t, "MongoDB", res.MergedOutput)
	assert.Equal(t, 0, judgeLLM.calls(), "the judge's turn was skipped")
	assert.Equal(t, 1, dec.CallCount())

	judge.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sites.SiteSwarmTieBreak, rows[0].Site)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DECIDER, rows[0].Path)
	assert.Equal(t, "MongoDB", rows[0].CandidateAnswer)
	assert.Equal(t, "wf-tie-judge", rows[0].SessionId)
	req := dec.Calls()[0]
	opts := req.Questions[sites.QTieWinner].GetChoice().Options
	assert.Len(t, opts, 3, "only the tied choices plus none_of_these are options")
	assert.Contains(t, req.State.String(), "ACID compliance", "vote reasoning reaches the decider")
}

func TestSwarmTieBreakDecision_NoneOfTheseFallsBackToJudge(t *testing.T) {
	t.Parallel()
	judgeLLM := newMockLLMProvider("PostgreSQL")
	dec := decisionmock.New().AnswerChoice(sites.QTieWinner, map[string]float64{"MongoDB": 0.02, "PostgreSQL": 0.03, decision.NoneOption: 0.95})
	store := &memShadowStore{}
	judge := newDecisionAgent(t, "judge", judgeLLM, dec, store, tieBreakBand(0.8))

	res, err := runTiedSwarm(t, judge)
	require.NoError(t, err)
	assert.Equal(t, "PostgreSQL", res.MergedOutput, "the judge decided")
	assert.Equal(t, 1, judgeLLM.calls())
	assert.Equal(t, 1, dec.CallCount(), "the live answer is recorded, not re-asked")

	judge.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, decision.NoneOption, rows[0].CandidateAnswer)
	assert.Equal(t, "PostgreSQL", rows[0].ReferenceAnswer)
	assert.Equal(t, sites.ReferenceSourceJudgeAgent, rows[0].ReferenceSource)
}

func TestSwarmTieBreakDecision_ShadowBandRecordsAgainstJudge(t *testing.T) {
	t.Parallel()
	judgeLLM := newMockLLMProvider("PostgreSQL")
	dec := decisionmock.New().AnswerChoice(sites.QTieWinner, map[string]float64{"MongoDB": 0.9, "PostgreSQL": 0.05, decision.NoneOption: 0.05})
	store := &memShadowStore{}
	judge := newDecisionAgent(t, "judge", judgeLLM, dec, store) // no band: shadow

	res, err := runTiedSwarm(t, judge)
	require.NoError(t, err)
	assert.Equal(t, "PostgreSQL", res.MergedOutput)
	assert.Equal(t, 1, judgeLLM.calls())

	judge.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, "MongoDB", rows[0].CandidateAnswer)
	assert.Equal(t, "PostgreSQL", rows[0].ReferenceAnswer, "the disagreement is recorded, not acted on")
}

func TestSwarmTieBreakDecision_DeciderErrorFallsBack(t *testing.T) {
	t.Parallel()
	judgeLLM := newMockLLMProvider("MongoDB")
	dec := decisionmock.New().SetError(decision.ErrOverloaded)
	judge := newDecisionAgent(t, "judge", judgeLLM, dec, &memShadowStore{}, tieBreakBand(0.8))

	res, err := runTiedSwarm(t, judge)
	require.NoError(t, err)
	assert.Equal(t, "MongoDB", res.MergedOutput)
	assert.Equal(t, 1, judgeLLM.calls(), "a decider error never costs the workflow anything")
	judge.WaitDecisionShadows()
}

func TestTiedChoices(t *testing.T) {
	t.Parallel()
	tied := tiedChoices(map[string]int32{"a": 3, "b": 3, "c": 1})
	assert.Equal(t, map[string]int32{"a": 3, "b": 3}, tied)
	assert.Len(t, tiedChoices(map[string]int32{"a": 2}), 1)
	assert.Empty(t, tiedChoices(nil))
}
