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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
)

func TestAskRequest(t *testing.T) {
	tests := []struct {
		name    string
		ask     Ask
		wantErr string
		check   func(t *testing.T, req *loomv1.DecisionRequest)
	}{
		{
			name: "yes_no default kind, string facts wrapped",
			ask:  Ask{Question: "Is the result an error?", Facts: "exit code 1"},
			check: func(t *testing.T, req *loomv1.DecisionRequest) {
				assert.Equal(t, SiteAsk, req.Site)
				require.Contains(t, req.Questions, QAsk)
				_, ok := req.Questions[QAsk].Kind.(*loomv1.DecisionQuestion_Noul)
				assert.True(t, ok)
				assert.Equal(t, "exit code 1", req.GetState().GetStructValue().AsMap()["facts"])
			},
		},
		{
			name: "yes_no with criteria",
			ask:  Ask{Kind: "YES_NO", Question: "q", Facts: map[string]any{"a": 1}, WhenYes: "it is", WhenNo: "it is not"},
			check: func(t *testing.T, req *loomv1.DecisionRequest) {
				n := req.Questions[QAsk].GetNoul()
				require.NotNil(t, n)
				assert.NotNil(t, n.CriteriaTrue)
				assert.NotNil(t, n.CriteriaFalse)
			},
		},
		{
			name:    "yes_no with one criterion only",
			ask:     Ask{Question: "q", Facts: "f", WhenYes: "only one"},
			wantErr: "go together",
		},
		{
			name: "choice adds none option",
			ask:  Ask{Kind: AskChoice, Question: "which?", Facts: "f", Options: []string{"a", " b "}},
			check: func(t *testing.T, req *loomv1.DecisionRequest) {
				c := req.Questions[QAsk].GetChoice()
				require.NotNil(t, c)
				assert.Len(t, c.Options, 3)
				assert.Contains(t, c.Options, "a")
				assert.Contains(t, c.Options, "b")
				assert.Contains(t, c.Options, decision.NoneOption)
			},
		},
		{
			name: "scale keeps level order",
			ask:  Ask{Kind: AskScale, Question: "how bad?", Facts: "f", Options: []string{"low", "mid", "high"}},
			check: func(t *testing.T, req *loomv1.DecisionRequest) {
				s := req.Questions[QAsk].GetScore()
				require.NotNil(t, s)
				require.Len(t, s.Levels, 3)
				assert.Equal(t, "low", s.Levels[0].GetStringValue())
				assert.Equal(t, "high", s.Levels[2].GetStringValue())
			},
		},
		{name: "missing question", ask: Ask{Facts: "f"}, wantErr: "question is required"},
		{name: "missing facts", ask: Ask{Question: "q"}, wantErr: "facts is required"},
		{name: "blank facts", ask: Ask{Question: "q", Facts: "  "}, wantErr: "facts is required"},
		{name: "facts too long", ask: Ask{Question: "q", Facts: strings.Repeat("x", MaxAskFactsRunes+1)}, wantErr: "too long"},
		{name: "unknown kind", ask: Ask{Kind: "maybe", Question: "q", Facts: "f"}, wantErr: "kind must be"},
		{name: "choice needs two options", ask: Ask{Kind: AskChoice, Question: "q", Facts: "f", Options: []string{"a"}}, wantErr: "at least 2"},
		{name: "choice rejects empty label", ask: Ask{Kind: AskChoice, Question: "q", Facts: "f", Options: []string{"a", " "}}, wantErr: "empty label"},
		{name: "choice rejects duplicate", ask: Ask{Kind: AskChoice, Question: "q", Facts: "f", Options: []string{"a", "a "}}, wantErr: "duplicate"},
		{name: "choice rejects long label", ask: Ask{Kind: AskChoice, Question: "q", Facts: "f", Options: []string{"a", strings.Repeat("b", MaxAskOptionLength+1)}}, wantErr: "longer than"},
		{name: "scale caps levels", ask: Ask{Kind: AskScale, Question: "q", Facts: "f", Options: manyLabels(decision.MaxScoreLevels + 1)}, wantErr: "levels"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := AskRequest(tt.ask)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NoError(t, decision.Validate(req))
			if tt.check != nil {
				tt.check(t, req)
			}
		})
	}
}

func manyLabels(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "level-" + strings.Repeat("i", i+1)
	}
	return out
}

// decideWith runs an ask through a router over a scripted mock and returns
// the outcome, so AskAnswerOf sees exactly what the tool and CLI see.
func decideWith(t *testing.T, m *mock.Decider, ask Ask) decision.Outcome {
	t.Helper()
	req, err := AskRequest(ask)
	require.NoError(t, err)
	r := decision.NewRouter(m)
	return r.Decide(context.Background(), req)
}

func TestAskAnswerOf(t *testing.T) {
	t.Run("yes_no", func(t *testing.T) {
		m := mock.New().SetModel("m").SetUsage(10, 0, 0.001).AnswerNoul(QAsk, 0.82)
		out := decideWith(t, m, Ask{Question: "q", Facts: "f"})
		ans, err := AskAnswerOf("", out)
		require.NoError(t, err)
		assert.Equal(t, AskYesNo, ans.Kind)
		require.NotNil(t, ans.PYes)
		assert.InDelta(t, 0.82, *ans.PYes, 1e-9)
		assert.Equal(t, true, ans.Answer)
		assert.Equal(t, "m", ans.Model)
		assert.InDelta(t, 0.001, ans.CostUSD, 1e-9)
		assert.InDelta(t, decision.NoulDecisiveness(0.82), ans.Confidence, 1e-9)
		assert.Empty(t, ans.Probabilities)
	})
	t.Run("choice ordered by probability", func(t *testing.T) {
		m := mock.New().AnswerChoice(QAsk, map[string]float64{"a": 0.2, "b": 0.7, decision.NoneOption: 0.1})
		out := decideWith(t, m, Ask{Kind: AskChoice, Question: "q", Facts: "f", Options: []string{"a", "b"}})
		ans, err := AskAnswerOf(AskChoice, out)
		require.NoError(t, err)
		assert.Equal(t, "b", ans.Answer)
		require.Len(t, ans.Probabilities, 3)
		assert.Equal(t, "b", ans.Probabilities[0].Option)
		assert.Equal(t, "a", ans.Probabilities[1].Option)
		assert.Equal(t, decision.NoneOption, ans.Probabilities[2].Option)
	})
	t.Run("scale in level order with labels", func(t *testing.T) {
		m := mock.New().AnswerScore(QAsk, map[string]float64{"0": 0.1, "1": 0.3, "2": 0.6})
		out := decideWith(t, m, Ask{Kind: AskScale, Question: "q", Facts: "f", Options: []string{"low", "mid", "high"}})
		ans, err := AskAnswerOf(AskScale, out)
		require.NoError(t, err)
		require.NotNil(t, ans.ExpectedLevel)
		assert.InDelta(t, 1.5, *ans.ExpectedLevel, 1e-9)
		assert.Equal(t, "high", ans.Answer)
		require.Len(t, ans.Probabilities, 3)
		assert.Equal(t, "low", ans.Probabilities[0].Label)
		assert.Equal(t, 0, *ans.Probabilities[0].Level)
		assert.Equal(t, "high", ans.Probabilities[2].Label)
	})
	t.Run("decider error surfaces", func(t *testing.T) {
		m := mock.New().SetError(assert.AnError)
		out := decideWith(t, m, Ask{Question: "q", Facts: "f"})
		_, err := AskAnswerOf(AskYesNo, out)
		require.ErrorIs(t, err, assert.AnError)
	})
	t.Run("no response without error", func(t *testing.T) {
		_, err := AskAnswerOf(AskYesNo, decision.Outcome{Path: loomv1.DecisionPath_DECISION_PATH_DISABLED})
		require.Error(t, err)
	})
	t.Run("unknown kind", func(t *testing.T) {
		m := mock.New().AnswerNoul(QAsk, 0.5)
		out := decideWith(t, m, Ask{Question: "q", Facts: "f"})
		_, err := AskAnswerOf("weird", out)
		require.Error(t, err)
	})
	_ = time.Second
}
