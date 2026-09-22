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
	"fmt"
	"math"
	"sort"
	"strconv"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Confidence is how concentrated a distribution is: (n·p_max − 1)/(n − 1) for
// n outcomes, in [0, 1]. A 90% winner among three gives 0.85; a uniform
// spread gives 0. It is not p_max: it says how decisively the mass sits on the
// winner, which is what a routing band should key on. With fewer than two
// outcomes it is 1.
func Confidence(probabilities map[string]float64) float64 {
	n := len(probabilities)
	if n < 2 {
		return 1
	}
	pmax := 0.0
	for _, p := range probabilities {
		if p > pmax {
			pmax = p
		}
	}
	c := (float64(n)*pmax - 1) / float64(n-1)
	return clamp01(c)
}

// NoulDecisiveness maps a Noul probability to the same [0, 1] decisiveness
// scale as Confidence: |2p − 1|. 0.5 is 0; 0 or 1 is 1. It lets a Router
// treat every answer kind with one act threshold.
func NoulDecisiveness(p float64) float64 { return clamp01(math.Abs(2*p - 1)) }

// Normalize scales probabilities in place to sum to 1 and clamps each to
// [0, 1]. It returns ErrMalformedAnswer when the map is empty, any value is
// negative or non-finite, or the sum is zero.
func Normalize(probabilities map[string]float64) error {
	if len(probabilities) == 0 {
		return fmt.Errorf("%w: empty distribution", ErrMalformedAnswer)
	}
	sum := 0.0
	for k, p := range probabilities {
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 {
			return fmt.Errorf("%w: probability for %q is %v", ErrMalformedAnswer, k, p)
		}
		sum += p
	}
	if sum <= 0 {
		return fmt.Errorf("%w: distribution sums to zero", ErrMalformedAnswer)
	}
	for k, p := range probabilities {
		probabilities[k] = clamp01(p / sum)
	}
	return nil
}

// ChoiceFromProbabilities builds a ChoiceAnswer: it normalizes the
// distribution, picks the highest-probability key (ties broken by key order
// so the result is deterministic), and computes Confidence.
func ChoiceFromProbabilities(probabilities map[string]float64) (*loomv1.ChoiceAnswer, error) {
	if err := Normalize(probabilities); err != nil {
		return nil, err
	}
	return &loomv1.ChoiceAnswer{
		Choice:        argmax(probabilities),
		Probabilities: probabilities,
		Confidence:    Confidence(probabilities),
	}, nil
}

// ScoreFromProbabilities builds a ScoreAnswer from a distribution over level
// indices ("0".."n-1") and the legend for those indices. Keys that are not
// non-negative integers are rejected. Score is the probability-weighted
// index.
func ScoreFromProbabilities(probabilities map[string]float64, legend map[string]string) (*loomv1.ScoreAnswer, error) {
	if err := Normalize(probabilities); err != nil {
		return nil, err
	}
	score := 0.0
	for k, p := range probabilities {
		idx, err := strconv.Atoi(k)
		if err != nil || idx < 0 {
			return nil, fmt.Errorf("%w: score level key %q is not a level index", ErrMalformedAnswer, k)
		}
		score += float64(idx) * p
	}
	return &loomv1.ScoreAnswer{
		Score:         score,
		Probabilities: probabilities,
		Legend:        legend,
		Confidence:    Confidence(probabilities),
	}, nil
}

// NoulOf returns the Noul answer for a question id, or ErrMalformedAnswer
// when the id is missing or is a different kind.
func NoulOf(resp *loomv1.DecisionResponse, id string) (*loomv1.NoulAnswer, error) {
	a, err := answerOf(resp, id)
	if err != nil {
		return nil, err
	}
	k, ok := a.Kind.(*loomv1.DecisionAnswer_Noul)
	if !ok || k.Noul == nil {
		return nil, fmt.Errorf("%w: %q is not a noul answer", ErrMalformedAnswer, id)
	}
	return k.Noul, nil
}

// ChoiceOf returns the Choice answer for a question id, or ErrMalformedAnswer
// when the id is missing or is a different kind.
func ChoiceOf(resp *loomv1.DecisionResponse, id string) (*loomv1.ChoiceAnswer, error) {
	a, err := answerOf(resp, id)
	if err != nil {
		return nil, err
	}
	k, ok := a.Kind.(*loomv1.DecisionAnswer_Choice)
	if !ok || k.Choice == nil {
		return nil, fmt.Errorf("%w: %q is not a choice answer", ErrMalformedAnswer, id)
	}
	return k.Choice, nil
}

// ScoreOf returns the Score answer for a question id, or ErrMalformedAnswer
// when the id is missing or is a different kind.
func ScoreOf(resp *loomv1.DecisionResponse, id string) (*loomv1.ScoreAnswer, error) {
	a, err := answerOf(resp, id)
	if err != nil {
		return nil, err
	}
	k, ok := a.Kind.(*loomv1.DecisionAnswer_Score)
	if !ok || k.Score == nil {
		return nil, fmt.Errorf("%w: %q is not a score answer", ErrMalformedAnswer, id)
	}
	return k.Score, nil
}

// AnswerConfidence is one answer's decisiveness on the shared [0, 1] scale:
// Confidence for Choice and Score, NoulDecisiveness for Noul. An empty answer
// is 0.
func AnswerConfidence(a *loomv1.DecisionAnswer) float64 {
	if a == nil {
		return 0
	}
	switch k := a.Kind.(type) {
	case *loomv1.DecisionAnswer_Noul:
		if k.Noul == nil {
			return 0
		}
		return NoulDecisiveness(k.Noul.Probability)
	case *loomv1.DecisionAnswer_Choice:
		if k.Choice == nil {
			return 0
		}
		return clamp01(k.Choice.Confidence)
	case *loomv1.DecisionAnswer_Score:
		if k.Score == nil {
			return 0
		}
		return clamp01(k.Score.Confidence)
	}
	return 0
}

// MinConfidence is the least decisive answer in a response. A request is only
// as reliable as its weakest judgment, so this is what a Router's act
// threshold compares against. An empty response is 0.
func MinConfidence(resp *loomv1.DecisionResponse) float64 {
	if resp == nil || len(resp.Answers) == 0 {
		return 0
	}
	minC := 1.0
	for _, a := range resp.Answers {
		if c := AnswerConfidence(a); c < minC {
			minC = c
		}
	}
	return minC
}

// CheckAnswers verifies that a response answers exactly the request's
// questions with matching kinds, every Choice answer names one of its
// question's options and covers only those keys, every Score answer covers
// only valid level indices, and every probability is in [0, 1]. Deciders call
// it before returning; callers may call it on anything they did not build.
func CheckAnswers(req *loomv1.DecisionRequest, resp *loomv1.DecisionResponse) error {
	if req == nil || resp == nil {
		return fmt.Errorf("%w: nil request or response", ErrMalformedAnswer)
	}
	if len(resp.Answers) != len(req.Questions) {
		return fmt.Errorf("%w: %d answers for %d questions", ErrMalformedAnswer, len(resp.Answers), len(req.Questions))
	}
	for id, q := range req.Questions {
		a, ok := resp.Answers[id]
		if !ok || a == nil {
			return fmt.Errorf("%w: no answer for %q", ErrMalformedAnswer, id)
		}
		switch qk := q.Kind.(type) {
		case *loomv1.DecisionQuestion_Noul:
			n, err := NoulOf(resp, id)
			if err != nil {
				return err
			}
			if !in01(n.Probability) {
				return fmt.Errorf("%w: %q probability %v outside [0,1]", ErrMalformedAnswer, id, n.Probability)
			}
		case *loomv1.DecisionQuestion_Choice:
			c, err := ChoiceOf(resp, id)
			if err != nil {
				return err
			}
			if _, ok := qk.Choice.Options[c.Choice]; !ok {
				return fmt.Errorf("%w: %q chose %q, not an option", ErrMalformedAnswer, id, c.Choice)
			}
			for k, p := range c.Probabilities {
				if _, ok := qk.Choice.Options[k]; !ok {
					return fmt.Errorf("%w: %q has probability for unknown option %q", ErrMalformedAnswer, id, k)
				}
				if !in01(p) {
					return fmt.Errorf("%w: %q probability for %q is %v", ErrMalformedAnswer, id, k, p)
				}
			}
			if !in01(c.Confidence) {
				return fmt.Errorf("%w: %q confidence %v outside [0,1]", ErrMalformedAnswer, id, c.Confidence)
			}
		case *loomv1.DecisionQuestion_Score:
			s, err := ScoreOf(resp, id)
			if err != nil {
				return err
			}
			n := len(qk.Score.Levels)
			for k, p := range s.Probabilities {
				idx, err := strconv.Atoi(k)
				if err != nil || idx < 0 || idx >= n {
					return fmt.Errorf("%w: %q has probability for level %q; %d levels", ErrMalformedAnswer, id, k, n)
				}
				if !in01(p) {
					return fmt.Errorf("%w: %q probability for level %q is %v", ErrMalformedAnswer, id, k, p)
				}
			}
			if s.Score < 0 || s.Score > float64(n-1) {
				return fmt.Errorf("%w: %q score %v outside [0,%d]", ErrMalformedAnswer, id, s.Score, n-1)
			}
			if !in01(s.Confidence) {
				return fmt.Errorf("%w: %q confidence %v outside [0,1]", ErrMalformedAnswer, id, s.Confidence)
			}
		default:
			return fmt.Errorf("%w: question %q has no kind", ErrMalformedAnswer, id)
		}
	}
	return nil
}

// LegendOf renders a Score question's levels as an index → description map,
// the shape ScoreAnswer.legend carries.
func LegendOf(q *loomv1.ScoreQuestion) map[string]string {
	if q == nil {
		return nil
	}
	legend := make(map[string]string, len(q.Levels))
	for i, l := range q.Levels {
		legend[strconv.Itoa(i)] = valueText(l)
	}
	return legend
}

func answerOf(resp *loomv1.DecisionResponse, id string) (*loomv1.DecisionAnswer, error) {
	if resp == nil {
		return nil, fmt.Errorf("%w: nil response", ErrMalformedAnswer)
	}
	a, ok := resp.Answers[id]
	if !ok || a == nil {
		return nil, fmt.Errorf("%w: no answer for %q", ErrMalformedAnswer, id)
	}
	return a, nil
}

func argmax(probabilities map[string]float64) string {
	keys := make([]string, 0, len(probabilities))
	for k := range probabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best, bestP := "", -1.0
	for _, k := range keys {
		if p := probabilities[k]; p > bestP {
			best, bestP = k, p
		}
	}
	return best
}

func clamp01(x float64) float64 {
	if math.IsNaN(x) {
		return 0
	}
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func in01(x float64) bool { return !math.IsNaN(x) && x >= 0 && x <= 1 }
