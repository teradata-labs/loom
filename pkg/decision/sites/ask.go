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
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// SiteAsk is the direct-use site: an agent (through the "decide" tool) or a
// person (through `loom decision ask`) puts one question to the decider
// about facts they supply. Nothing branches on the answer automatically; it
// is returned to whoever asked. Rows recorded here have no reference.
const SiteAsk = "tool.decide"

// QAsk is the single question id in an ask request.
const QAsk = "q"

// Ask kinds.
const (
	AskYesNo  = "yes_no"
	AskChoice = "choice"
	AskScale  = "scale"
)

// Bounds on one ask. Facts are the whole context the decider sees, so an
// oversized string fails loudly instead of being truncated into a different
// question.
const (
	MaxAskFactsRunes   = 12000
	MaxAskOptionLength = 200
)

// Ask is one direct question to the decider.
type Ask struct {
	// Kind is AskYesNo (default when empty), AskChoice or AskScale.
	Kind string
	// Question is what to decide, phrased so Facts can answer it.
	Question string
	// Facts is what the answer depends on: a string, or a JSON-like object
	// (map[string]any). The decider sees nothing else.
	Facts any
	// Options are the labels to choose among (AskChoice) or the levels from
	// lowest to highest (AskScale). Ignored for AskYesNo.
	Options []string
	// WhenYes and WhenNo pin down what each side of a yes/no means. Both or
	// neither.
	WhenYes, WhenNo string
}

// AskRequest validates an Ask and renders it as one request at SiteAsk with
// a single question QAsk. Choice questions get decision.NoneOption added so
// the decider can say none fits. Errors are *decision.ValidationError-style
// user errors: the caller shows them to whoever asked.
func AskRequest(a Ask) (*loomv1.DecisionRequest, error) {
	question := strings.TrimSpace(a.Question)
	if question == "" {
		return nil, fmt.Errorf("question is required")
	}
	kind := strings.ToLower(strings.TrimSpace(a.Kind))
	if kind == "" {
		kind = AskYesNo
	}

	facts := a.Facts
	switch f := facts.(type) {
	case nil:
		return nil, fmt.Errorf("facts is required")
	case string:
		if strings.TrimSpace(f) == "" {
			return nil, fmt.Errorf("facts is required")
		}
		if n := utf8.RuneCountInString(f); n > MaxAskFactsRunes {
			return nil, fmt.Errorf("facts is too long (%d runes; limit %d): give only what the answer depends on", n, MaxAskFactsRunes)
		}
		facts = map[string]any{"facts": f}
	}

	var (
		q   *loomv1.DecisionQuestion
		err error
	)
	switch kind {
	case AskYesNo:
		whenYes, whenNo := strings.TrimSpace(a.WhenYes), strings.TrimSpace(a.WhenNo)
		var opts []decision.NoulOption
		if whenYes != "" || whenNo != "" {
			if whenYes == "" || whenNo == "" {
				return nil, fmt.Errorf("when_yes and when_no go together")
			}
			opts = append(opts, decision.WithCriteria(whenYes, whenNo))
		}
		q = decision.Noul(question, opts...)
	case AskChoice:
		if err := checkAskOptions(a.Options); err != nil {
			return nil, err
		}
		labels := make(map[string]any, len(a.Options))
		for _, o := range a.Options {
			o = strings.TrimSpace(o)
			labels[o] = o
		}
		if q, err = decision.Choice(question, labels, decision.WithNoneOption("none of the options fits")); err != nil {
			return nil, err
		}
	case AskScale:
		if err := checkAskOptions(a.Options); err != nil {
			return nil, err
		}
		levels := make([]any, len(a.Options))
		for i, o := range a.Options {
			levels[i] = strings.TrimSpace(o)
		}
		if q, err = decision.Score(question, levels...); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("kind must be %s, %s or %s, got %q", AskYesNo, AskChoice, AskScale, a.Kind)
	}

	return decision.NewRequest(SiteAsk, facts, map[string]*loomv1.DecisionQuestion{QAsk: q})
}

func checkAskOptions(options []string) error {
	if len(options) < 2 {
		return fmt.Errorf("options needs at least 2 entries")
	}
	seen := make(map[string]bool, len(options))
	for _, o := range options {
		o = strings.TrimSpace(o)
		if o == "" {
			return fmt.Errorf("options must not contain an empty label")
		}
		if utf8.RuneCountInString(o) > MaxAskOptionLength {
			return fmt.Errorf("option %q… is longer than %d runes", string([]rune(o)[:20]), MaxAskOptionLength)
		}
		if seen[o] {
			return fmt.Errorf("duplicate option %q", o)
		}
		seen[o] = true
	}
	return nil
}

// AskAnswer is the decider's answer to an Ask, shaped for whoever asked: the
// number to branch on first, the whole distribution after.
type AskAnswer struct {
	Kind string `json:"kind"`
	// PYes is p(yes) for AskYesNo.
	PYes *float64 `json:"p_yes,omitempty"`
	// Answer is true/false for AskYesNo, the chosen label (or
	// decision.NoneOption) for AskChoice, the most likely level's label for
	// AskScale.
	Answer any `json:"answer"`
	// ExpectedLevel is the probability-weighted level index for AskScale.
	ExpectedLevel *float64 `json:"expected_level,omitempty"`
	// Probabilities: by probability, highest first, for AskChoice; by level,
	// lowest first, for AskScale. Empty for AskYesNo.
	Probabilities []AskProbability `json:"probabilities,omitempty"`
	// Confidence is the decider's decisiveness on [0, 1] (decision.MinConfidence).
	Confidence float64 `json:"confidence"`
	Model      string  `json:"model,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	LatencyMs  int64   `json:"latency_ms"`
}

// AskProbability is one entry of a distribution.
type AskProbability struct {
	Option string  `json:"option,omitempty"`
	Level  *int    `json:"level,omitempty"`
	Label  string  `json:"label,omitempty"`
	P      float64 `json:"p"`
}

// AskAnswerOf reads the answer for kind out of an outcome's response.
func AskAnswerOf(kind string, out decision.Outcome) (*AskAnswer, error) {
	if out.Response == nil {
		if out.Err != nil {
			return nil, out.Err
		}
		return nil, fmt.Errorf("no answer (path %s)", out.Path)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = AskYesNo
	}
	ans := &AskAnswer{
		Kind:       kind,
		Confidence: out.Confidence,
		Model:      out.Response.GetModel(),
		CostUSD:    out.Response.GetUsage().GetCostUsd(),
		LatencyMs:  out.Latency.Milliseconds(),
	}
	switch kind {
	case AskYesNo:
		a, err := decision.NoulOf(out.Response, QAsk)
		if err != nil {
			return nil, err
		}
		p := a.Probability
		ans.PYes = &p
		ans.Answer = p >= 0.5
	case AskChoice:
		a, err := decision.ChoiceOf(out.Response, QAsk)
		if err != nil {
			return nil, err
		}
		ans.Answer = a.Choice
		ans.Probabilities = sortedAskProbabilities(a.Probabilities)
	case AskScale:
		a, err := decision.ScoreOf(out.Response, QAsk)
		if err != nil {
			return nil, err
		}
		s := a.Score
		ans.ExpectedLevel = &s
		ans.Probabilities = scaleAskProbabilities(a)
		best, bestP := "", -1.0
		for _, e := range ans.Probabilities {
			if e.P > bestP {
				best, bestP = e.Label, e.P
			}
		}
		ans.Answer = best
	default:
		return nil, fmt.Errorf("unknown ask kind %q", kind)
	}
	return ans, nil
}

func sortedAskProbabilities(p map[string]float64) []AskProbability {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if p[keys[i]] != p[keys[j]] {
			return p[keys[i]] > p[keys[j]]
		}
		return keys[i] < keys[j]
	})
	out := make([]AskProbability, 0, len(keys))
	for _, k := range keys {
		out = append(out, AskProbability{Option: k, P: p[k]})
	}
	return out
}

func scaleAskProbabilities(a *loomv1.ScoreAnswer) []AskProbability {
	out := make([]AskProbability, 0, len(a.Probabilities))
	for i := 0; i < len(a.Probabilities); i++ {
		key := fmt.Sprintf("%d", i)
		p, ok := a.Probabilities[key]
		if !ok {
			continue
		}
		level := i
		e := AskProbability{Level: &level, P: p}
		if a.Legend != nil {
			e.Label = a.Legend[key]
		}
		out = append(out, e)
	}
	return out
}
