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

package decision

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestConfidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		probs map[string]float64
		want  float64
	}{
		{name: "empty", probs: nil, want: 1},
		{name: "single", probs: map[string]float64{"a": 1}, want: 1},
		{name: "certain of three", probs: map[string]float64{"a": 1, "b": 0, "c": 0}, want: 1},
		{name: "uniform of three", probs: map[string]float64{"a": 1.0 / 3, "b": 1.0 / 3, "c": 1.0 / 3}, want: 0},
		{name: "ninety percent of three", probs: map[string]float64{"a": 0.9, "b": 0.05, "c": 0.05}, want: 0.85},
		{name: "ninety percent of two", probs: map[string]float64{"a": 0.9, "b": 0.1}, want: 0.8},
		{name: "uniform of two", probs: map[string]float64{"a": 0.5, "b": 0.5}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tt.want, Confidence(tt.probs), 1e-9)
		})
	}
}

func TestNoulDecisiveness(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, 0, NoulDecisiveness(0.5), 1e-9)
	assert.InDelta(t, 1, NoulDecisiveness(0), 1e-9)
	assert.InDelta(t, 1, NoulDecisiveness(1), 1e-9)
	assert.InDelta(t, 0.8, NoulDecisiveness(0.9), 1e-9)
	assert.InDelta(t, 0.8, NoulDecisiveness(0.1), 1e-9)
	assert.Equal(t, 0.0, NoulDecisiveness(math.NaN()))
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      map[string]float64
		want    map[string]float64
		wantErr bool
	}{
		{name: "already normal", in: map[string]float64{"a": 0.25, "b": 0.75}, want: map[string]float64{"a": 0.25, "b": 0.75}},
		{name: "scales", in: map[string]float64{"a": 1, "b": 3}, want: map[string]float64{"a": 0.25, "b": 0.75}},
		{name: "empty", in: map[string]float64{}, wantErr: true},
		{name: "negative", in: map[string]float64{"a": -1, "b": 2}, wantErr: true},
		{name: "nan", in: map[string]float64{"a": math.NaN(), "b": 1}, wantErr: true},
		{name: "inf", in: map[string]float64{"a": math.Inf(1), "b": 1}, wantErr: true},
		{name: "all zero", in: map[string]float64{"a": 0, "b": 0}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Normalize(tt.in)
			if tt.wantErr {
				assert.True(t, errors.Is(err, ErrMalformedAnswer))
				return
			}
			require.NoError(t, err)
			for k, v := range tt.want {
				assert.InDelta(t, v, tt.in[k], 1e-9, k)
			}
		})
	}
}

func TestChoiceFromProbabilities(t *testing.T) {
	t.Parallel()
	c, err := ChoiceFromProbabilities(map[string]float64{"x": 2, "y": 6, "z": 2})
	require.NoError(t, err)
	assert.Equal(t, "y", c.Choice)
	assert.InDelta(t, 0.6, c.Probabilities["y"], 1e-9)
	assert.InDelta(t, Confidence(c.Probabilities), c.Confidence, 1e-9)

	// Ties break by key order so the answer is deterministic.
	tie, err := ChoiceFromProbabilities(map[string]float64{"b": 1, "a": 1})
	require.NoError(t, err)
	assert.Equal(t, "a", tie.Choice)

	_, err = ChoiceFromProbabilities(map[string]float64{})
	assert.True(t, errors.Is(err, ErrMalformedAnswer))
}

func TestScoreFromProbabilities(t *testing.T) {
	t.Parallel()
	legend := map[string]string{"0": "low", "1": "mid", "2": "high"}
	s, err := ScoreFromProbabilities(map[string]float64{"0": 0.1, "1": 0.2, "2": 0.7}, legend)
	require.NoError(t, err)
	assert.InDelta(t, 1.6, s.Score, 1e-9)
	assert.Equal(t, legend, s.Legend)
	assert.InDelta(t, 0.55, s.Confidence, 1e-9)

	_, err = ScoreFromProbabilities(map[string]float64{"low": 1, "high": 0}, legend)
	assert.True(t, errors.Is(err, ErrMalformedAnswer), "non-integer level keys rejected")
	_, err = ScoreFromProbabilities(map[string]float64{"-1": 1, "0": 0}, legend)
	assert.True(t, errors.Is(err, ErrMalformedAnswer), "negative level keys rejected")
}

func noulAns(p float64) *loomv1.DecisionAnswer {
	return &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Noul{Noul: &loomv1.NoulAnswer{Probability: p}}}
}

func choiceAns(t *testing.T, probs map[string]float64) *loomv1.DecisionAnswer {
	t.Helper()
	c, err := ChoiceFromProbabilities(probs)
	require.NoError(t, err)
	return &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Choice{Choice: c}}
}

func scoreAns(t *testing.T, probs map[string]float64, q *loomv1.ScoreQuestion) *loomv1.DecisionAnswer {
	t.Helper()
	s, err := ScoreFromProbabilities(probs, LegendOf(q))
	require.NoError(t, err)
	return &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Score{Score: s}}
}

func TestAccessorsAndMinConfidence(t *testing.T) {
	t.Parallel()
	resp := &loomv1.DecisionResponse{Answers: map[string]*loomv1.DecisionAnswer{
		"n": noulAns(0.95),                                        // decisiveness 0.9
		"c": choiceAns(t, map[string]float64{"a": 0.6, "b": 0.4}), // confidence 0.2
	}}
	n, err := NoulOf(resp, "n")
	require.NoError(t, err)
	assert.InDelta(t, 0.95, n.Probability, 1e-9)
	c, err := ChoiceOf(resp, "c")
	require.NoError(t, err)
	assert.Equal(t, "a", c.Choice)

	_, err = ScoreOf(resp, "c")
	assert.True(t, errors.Is(err, ErrMalformedAnswer), "wrong kind")
	_, err = NoulOf(resp, "missing")
	assert.True(t, errors.Is(err, ErrMalformedAnswer), "missing id")
	_, err = NoulOf(nil, "n")
	assert.True(t, errors.Is(err, ErrMalformedAnswer), "nil response")

	assert.InDelta(t, 0.2, MinConfidence(resp), 1e-9, "weakest judgment wins")
	assert.Equal(t, 0.0, MinConfidence(nil))
	assert.Equal(t, 0.0, MinConfidence(&loomv1.DecisionResponse{}))
	assert.Equal(t, 0.0, AnswerConfidence(&loomv1.DecisionAnswer{}))
}

func TestCheckAnswers(t *testing.T) {
	t.Parallel()
	scoreQ := mustScore(t)
	req := &loomv1.DecisionRequest{
		State: structpb.NewStringValue("s"),
		Questions: map[string]*loomv1.DecisionQuestion{
			"n": Noul("q"),
			"c": mustChoice(t),
			"s": scoreQ,
		},
	}
	good := func() *loomv1.DecisionResponse {
		return &loomv1.DecisionResponse{Answers: map[string]*loomv1.DecisionAnswer{
			"n": noulAns(0.3),
			"c": choiceAns(t, map[string]float64{"a": 0.1, "b": 0.8, "c": 0.1}),
			"s": scoreAns(t, map[string]float64{"0": 0.2, "1": 0.3, "2": 0.5}, scoreQ.GetScore()),
		}}
	}
	tests := []struct {
		name   string
		mutate func(*loomv1.DecisionResponse)
		ok     bool
	}{
		{name: "valid", mutate: func(*loomv1.DecisionResponse) {}, ok: true},
		{name: "missing answer", mutate: func(r *loomv1.DecisionResponse) { delete(r.Answers, "n") }},
		{name: "extra answer", mutate: func(r *loomv1.DecisionResponse) { r.Answers["x"] = noulAns(0.1) }},
		{name: "wrong kind", mutate: func(r *loomv1.DecisionResponse) { r.Answers["n"] = choiceAns(t, map[string]float64{"a": 1, "b": 0}) }},
		{name: "noul out of range", mutate: func(r *loomv1.DecisionResponse) { r.Answers["n"] = noulAns(1.5) }},
		{name: "choice not an option", mutate: func(r *loomv1.DecisionResponse) { r.Answers["c"].GetChoice().Choice = "zzz" }},
		{name: "choice unknown key", mutate: func(r *loomv1.DecisionResponse) { r.Answers["c"].GetChoice().Probabilities["zzz"] = 0 }},
		{name: "choice probability out of range", mutate: func(r *loomv1.DecisionResponse) { r.Answers["c"].GetChoice().Probabilities["a"] = 2 }},
		{name: "choice confidence out of range", mutate: func(r *loomv1.DecisionResponse) { r.Answers["c"].GetChoice().Confidence = -0.1 }},
		{name: "score bad level", mutate: func(r *loomv1.DecisionResponse) { r.Answers["s"].GetScore().Probabilities["9"] = 0 }},
		{name: "score out of range", mutate: func(r *loomv1.DecisionResponse) { r.Answers["s"].GetScore().Score = 7 }},
		{name: "score probability out of range", mutate: func(r *loomv1.DecisionResponse) { r.Answers["s"].GetScore().Probabilities["0"] = 3 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := good()
			tt.mutate(r)
			err := CheckAnswers(req, r)
			if tt.ok {
				require.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, ErrMalformedAnswer), "got %v", err)
		})
	}
	assert.True(t, errors.Is(CheckAnswers(nil, good()), ErrMalformedAnswer))
	assert.True(t, errors.Is(CheckAnswers(req, nil), ErrMalformedAnswer))
}

func TestLegendOf(t *testing.T) {
	t.Parallel()
	assert.Nil(t, LegendOf(nil))
	q, err := Score("r", "low", map[string]any{"summary": "high"})
	require.NoError(t, err)
	legend := LegendOf(q.GetScore())
	assert.Equal(t, "low", legend["0"])
	assert.Equal(t, `{"summary":"high"}`, legend["1"])
}
