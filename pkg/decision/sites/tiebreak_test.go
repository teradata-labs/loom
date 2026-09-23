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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

func TestTieBreakRequest(t *testing.T) {
	t.Parallel()
	votes := []TieVote{
		{Choice: "PostgreSQL", Confidence: 0.8, Reasoning: "ACID compliance"},
		{Choice: "MongoDB", Confidence: 0.85, Reasoning: strings.Repeat("s", maxTieReasoningRunes+5)},
	}
	req, err := TieBreakRequest("Which database?", map[string]int32{"MongoDB": 2, "PostgreSQL": 2}, votes)
	require.NoError(t, err)
	assert.Equal(t, SiteSwarmTieBreak, req.Site)
	opts := req.Questions[QTieWinner].GetChoice().Options
	assert.Len(t, opts, 3, "two tied choices plus none_of_these")
	assert.Contains(t, opts, "PostgreSQL")
	assert.Contains(t, opts, decision.NoneOption)

	state := req.State.GetStructValue().AsMap()
	assert.Equal(t, "Which database?", state["question"])
	tied := state["tied"].([]any)
	require.Len(t, tied, 2)
	assert.Equal(t, "MongoDB", tied[0].(map[string]any)["choice"], "tied choices sorted")
	vs := state["votes"].([]any)
	require.Len(t, vs, 2)
	assert.True(t, strings.HasSuffix(vs[1].(map[string]any)["reasoning"].(string), "…"), "reasoning bounded")

	_, err = TieBreakRequest("q", map[string]int32{"only": 3}, nil)
	assert.ErrorIs(t, err, decision.ErrValidation)
}

func TestTieBreakReferenceAndWinner(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "PostgreSQL", TieBreakReference("PostgreSQL")[QTieWinner].Answer)
	assert.Equal(t, decision.NoneOption, TieBreakReference("")[QTieWinner].Answer, "a judge that failed renders as none")
	assert.Equal(t, ReferenceSourceJudgeAgent, TieBreakReference("x")[QTieWinner].Source)

	req, err := TieBreakRequest("q", map[string]int32{"a": 1, "b": 1}, nil)
	require.NoError(t, err)
	m := mock.New().AnswerChoice(QTieWinner, map[string]float64{"a": 0.9, "b": 0.05, decision.NoneOption: 0.05})
	out := decision.NewRouter(m).Decide(context.Background(), req)
	require.NoError(t, out.Err)
	key, ok := TieBreakWinner(out.Response)
	assert.True(t, ok)
	assert.Equal(t, "a", key)

	_, ok = TieBreakWinner(nil)
	assert.False(t, ok)
}
