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

package collaboration

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/decision"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// positionLLM answers every chat with a fixed, well-formed debate position.
type positionLLM struct{ position string }

func (l positionLLM) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: fmt.Sprintf("POSITION: %s\n\nCONFIDENCE: 90", l.position), StopReason: "end_turn"}, nil
}
func (positionLLM) Name() string  { return "mock" }
func (positionLLM) Model() string { return "mock-model" }

type mapProvider struct{ agents map[string]*agent.Agent }

func (p mapProvider) GetAgent(_ context.Context, id string) (*agent.Agent, error) {
	if a, ok := p.agents[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("no agent %q", id)
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

func consensusBand(actMin float64, mode loomv1.DecisionBandMode) decision.RouterOption {
	return decision.WithBands([]*loomv1.DecisionBand{{Site: sites.SiteDebateConsensus, ActMin: actMin, Mode: mode}})
}

// runDisagreeingDebate runs one round where both debaters answer at 90%
// confidence with opposite positions, so the heuristic says "consensus"
// while the positions plainly disagree. The first debater is the internal
// moderator and carries the decision layer.
func runDisagreeingDebate(t *testing.T, dec decision.Decider, store decision.ShadowStore, opts ...decision.RouterOption) (*loomv1.WorkflowResult, *agent.Agent, error) {
	t.Helper()
	moderator := agent.NewAgent(nil, positionLLM{"Use indexes"},
		agent.WithName("a1"),
		agent.WithDecisionRouter(decision.NewRouter(dec, opts...)),
		agent.WithDecisionShadowStore(store))
	other := agent.NewAgent(nil, positionLLM{"Use partitions, never indexes"}, agent.WithName("a2"))
	d := NewDebateOrchestrator(mapProvider{agents: map[string]*agent.Agent{"a1": moderator, "a2": other}})
	res, err := d.Execute(context.Background(), &loomv1.DebatePattern{
		Topic:    "Indexes or partitions?",
		AgentIds: []string{"a1", "a2"},
		Rounds:   1,
	})
	return res, moderator, err
}

func TestDebateConsensusDecision_LiveBandOverridesHeuristic(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().AnswerNoul(sites.QConsensus, 0.05) // positions disagree
	store := &memShadowStore{}
	res, moderator, err := runDisagreeingDebate(t, dec, store, consensusBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE))
	require.NoError(t, err)
	assert.Equal(t, "false", res.Metadata["consensus_achieved"], "the heuristic would have said true on confidence alone")

	moderator.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, sites.SiteDebateConsensus, rows[0].Site)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_DECIDER, rows[0].Path)
	assert.Equal(t, "false", rows[0].CandidateAnswer)
	req := dec.Calls()[0]
	assert.Contains(t, req.State.String(), "Use partitions, never indexes", "positions reach the decider")
}

func TestDebateConsensusDecision_TightenOnlyCannotDeclareConsensus(t *testing.T) {
	t.Parallel()
	t.Run("confident agree still defers to the heuristic", func(t *testing.T) {
		t.Parallel()
		// Decider says consensus; heuristic also says consensus here, so the
		// observable check is the recorded path: a tighten-only band may not
		// act on "reached".
		dec := decisionmock.New().AnswerNoul(sites.QConsensus, 0.95)
		store := &memShadowStore{}
		_, moderator, err := runDisagreeingDebate(t, dec, store, consensusBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY))
		require.NoError(t, err)
		moderator.WaitDecisionShadows()
		rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
		assert.Equal(t, "true", rows[0].ReferenceAnswer)
	})
	t.Run("confident disagree withholds consensus", func(t *testing.T) {
		t.Parallel()
		dec := decisionmock.New().AnswerNoul(sites.QConsensus, 0.05)
		res, _, err := runDisagreeingDebate(t, dec, &memShadowStore{}, consensusBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY))
		require.NoError(t, err)
		assert.Equal(t, "false", res.Metadata["consensus_achieved"])
	})
}

func TestDebateConsensusDecision_ShadowBandRecordsAgainstHeuristic(t *testing.T) {
	t.Parallel()
	dec := decisionmock.New().AnswerNoul(sites.QConsensus, 0.05)
	store := &memShadowStore{}
	res, moderator, err := runDisagreeingDebate(t, dec, store) // no band: shadow
	require.NoError(t, err)
	assert.Equal(t, "true", res.Metadata["consensus_achieved"], "the heuristic decides")

	moderator.WaitDecisionShadows()
	rows, err := store.QueryShadow(context.Background(), decision.ShadowQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, rows[0].Path)
	assert.Equal(t, "false", rows[0].CandidateAnswer)
	assert.Equal(t, "true", rows[0].ReferenceAnswer, "the disagreement is recorded, not acted on")
	assert.Equal(t, sites.ReferenceSourceConsensusHeuristic, rows[0].ReferenceSource)
}

func TestDebateConsensusDecision_BelowBandAndErrorsFallBack(t *testing.T) {
	t.Parallel()
	for name, dec := range map[string]*decisionmock.Decider{
		"below band": decisionmock.New().AnswerNoul(sites.QConsensus, 0.5),
		"error":      decisionmock.New().SetError(decision.ErrOverloaded),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, moderator, err := runDisagreeingDebate(t, dec, &memShadowStore{}, consensusBand(0.8, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE))
			require.NoError(t, err)
			assert.Equal(t, "true", res.Metadata["consensus_achieved"], "the heuristic decides")
			moderator.WaitDecisionShadows()
		})
	}
}

func TestDebateWithoutDecisionLayerIsUnchanged(t *testing.T) {
	t.Parallel()
	a1 := agent.NewAgent(nil, positionLLM{"x"}, agent.WithName("a1"))
	a2 := agent.NewAgent(nil, positionLLM{"y"}, agent.WithName("a2"))
	d := NewDebateOrchestrator(mapProvider{agents: map[string]*agent.Agent{"a1": a1, "a2": a2}})
	res, err := d.Execute(context.Background(), &loomv1.DebatePattern{Topic: "t", AgentIds: []string{"a1", "a2"}, Rounds: 1})
	require.NoError(t, err)
	assert.Equal(t, "true", res.Metadata["consensus_achieved"])
}
