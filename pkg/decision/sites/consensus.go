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

package sites

import (
	"strconv"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// SiteDebateConsensus is the per-round consensus check in the debate pattern
// (collaboration.DebateOrchestrator.checkConsensus). Today it is a heuristic
// with no model call: consensus when the mean position confidence is at
// least 0.8, whatever the positions say. Here it is one Noul over the
// positions themselves. Unlike the other Phase 3 sites this adds a call
// where none was made, so a live band here buys accuracy, not latency.
const SiteDebateConsensus = "debate.consensus"

// QConsensus is the single question id at SiteDebateConsensus.
const QConsensus = "consensus"

// ReferenceSourceConsensusHeuristic labels a reference taken from the
// confidence-mean heuristic.
const ReferenceSourceConsensusHeuristic = "collaboration.debate.checkConsensus"

// Position is one debater's stance as the decider sees it.
type Position struct {
	AgentID    string
	Position   string
	Arguments  []string
	Confidence float64
}

const (
	maxConsensusTopicRunes    = 2000
	maxConsensusPositionRunes = 1500
	maxConsensusArgumentRunes = 400
	maxConsensusArguments     = 6
)

// ConsensusRequest builds the request: state is the topic and every
// position with its bounded arguments and confidence. It returns a
// *decision.ValidationError with fewer than two positions.
func ConsensusRequest(topic string, positions []Position) (*loomv1.DecisionRequest, error) {
	if len(positions) < 2 {
		return nil, &decision.ValidationError{Field: "state.positions", Msg: "consensus needs at least two positions"}
	}
	q := decision.Noul(
		"Do the positions agree on the answer to the topic?",
		decision.WithCriteria(
			"every position takes the same side or proposes the same course of action, differing at most in emphasis or supporting reasons",
			"at least one position takes a different side, proposes a different course of action, or rejects what another proposes",
		),
	)
	items := make([]any, 0, len(positions))
	for _, p := range positions {
		args := p.Arguments
		if len(args) > maxConsensusArguments {
			args = args[:maxConsensusArguments]
		}
		bounded := make([]any, 0, len(args))
		for _, a := range args {
			bounded = append(bounded, truncateRunes(a, maxConsensusArgumentRunes))
		}
		items = append(items, map[string]any{
			"agent":      p.AgentID,
			"position":   truncateRunes(p.Position, maxConsensusPositionRunes),
			"arguments":  bounded,
			"confidence": p.Confidence,
		})
	}
	state := map[string]any{
		"topic":     truncateRunes(topic, maxConsensusTopicRunes),
		"positions": items,
	}
	return decision.NewRequest(SiteDebateConsensus, state, map[string]*loomv1.DecisionQuestion{QConsensus: q})
}

// ConsensusReference renders the heuristic's verdict.
func ConsensusReference(reached bool) map[string]decision.Reference {
	return map[string]decision.Reference{
		QConsensus: {Answer: strconv.FormatBool(reached), Source: ReferenceSourceConsensusHeuristic},
	}
}

// ConsensusVerdict returns the decider's verdict: consensus when the Noul
// probability is at least 0.5. ok is false when there is no usable answer.
func ConsensusVerdict(resp *loomv1.DecisionResponse) (reached bool, ok bool) {
	if resp == nil {
		return false, false
	}
	a, err := decision.NoulOf(resp, QConsensus)
	if err != nil || a == nil {
		return false, false
	}
	return a.Probability >= 0.5, true
}
