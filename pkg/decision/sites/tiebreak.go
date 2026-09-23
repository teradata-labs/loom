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
	"sort"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// SiteSwarmTieBreak is the swarm vote tie-break
// (orchestration.SwarmExecutor.invokeJudge). Today a judge agent reads the
// question, the tied options and every vote's reasoning and answers with one
// option; here it is one Choice over the tied options.
const SiteSwarmTieBreak = "swarm.tie_break"

// QTieWinner is the single question id at SiteSwarmTieBreak.
const QTieWinner = "winner"

// ReferenceSourceJudgeAgent labels a reference taken from the judge agent's
// parsed decision.
const ReferenceSourceJudgeAgent = "orchestration.swarm.invokeJudge"

// TieVote is one swarm vote as the decider sees it.
type TieVote struct {
	Choice     string
	Confidence float64
	Reasoning  string
}

// maxTieQuestionRunes bounds the swarm question in state.
const maxTieQuestionRunes = 2000

// maxTieReasoningRunes bounds each vote's reasoning in state; the judge
// prompt today shows 150 bytes, which loses most of a sentence.
const maxTieReasoningRunes = 400

// TieBreakRequest builds the request: options are the tied choices (plus
// none_of_these, so the decider has an honest place for "neither"), state is
// the question, the tied options with their vote counts, and every vote with
// its confidence and bounded reasoning. It returns a *decision.ValidationError
// when fewer than two choices are tied.
func TieBreakRequest(question string, tied map[string]int32, votes []TieVote) (*loomv1.DecisionRequest, error) {
	if len(tied) < 2 {
		return nil, &decision.ValidationError{Field: "questions.winner.options", Msg: "a tie needs at least two choices"}
	}
	keys := make([]string, 0, len(tied))
	for k := range tied {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	options := make(map[string]any, len(keys))
	tiedState := make([]any, 0, len(keys))
	for _, k := range keys {
		options[k] = "The votes and their reasoning, taken together, support " + k + " best."
		tiedState = append(tiedState, map[string]any{"choice": k, "votes": tied[k]})
	}
	q, err := decision.Choice(
		"Which of the tied choices do the votes' reasons support best as the answer to the question?",
		options,
		decision.WithNoneOption("The reasons given do not favour any tied choice over the others."),
	)
	if err != nil {
		return nil, err
	}
	voteState := make([]any, 0, len(votes))
	for _, v := range votes {
		voteState = append(voteState, map[string]any{
			"choice":     v.Choice,
			"confidence": v.Confidence,
			"reasoning":  truncateRunes(v.Reasoning, maxTieReasoningRunes),
		})
	}
	state := map[string]any{
		"question": truncateRunes(question, maxTieQuestionRunes),
		"tied":     tiedState,
		"votes":    voteState,
	}
	return decision.NewRequest(SiteSwarmTieBreak, state, map[string]*loomv1.DecisionQuestion{QTieWinner: q})
}

// TieBreakReference renders the judge agent's decision. Pass "" when the
// judge failed to produce a valid choice; that renders as none_of_these.
func TieBreakReference(judgeDecision string) map[string]decision.Reference {
	if judgeDecision == "" {
		judgeDecision = decision.NoneOption
	}
	return map[string]decision.Reference{
		QTieWinner: {Answer: judgeDecision, Source: ReferenceSourceJudgeAgent},
	}
}

// TieBreakWinner returns the decider's pick. ok is false when the response
// carries no usable Choice answer. The key may be decision.NoneOption, which
// no caller should act on: a tie-break has to name a choice.
func TieBreakWinner(resp *loomv1.DecisionResponse) (key string, ok bool) {
	if resp == nil {
		return "", false
	}
	a, err := decision.ChoiceOf(resp, QTieWinner)
	if err != nil || a == nil || a.Choice == "" {
		return "", false
	}
	return a.Choice, true
}
