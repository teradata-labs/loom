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

// Package mock is a scripted Decider for tests. Answers are set per question
// id; every request is recorded so a test can assert what a call site asked.
package mock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
)

// Decider is a scripted decision.Decider. It is safe for concurrent use.
type Decider struct {
	mu      sync.Mutex
	answers map[string]*loomv1.DecisionAnswer
	err     error
	latency time.Duration
	model   string
	usage   *loomv1.DecisionUsage
	calls   []*loomv1.DecisionRequest
	hook    func(*loomv1.DecisionRequest) error
}

// New returns an empty mock answering as model "mock-1".
func New() *Decider {
	return &Decider{answers: make(map[string]*loomv1.DecisionAnswer), model: "mock-1"}
}

// Name implements decision.Decider.
func (m *Decider) Name() string { return "mock" }

// Model implements decision.Decider.
func (m *Decider) Model() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.model
}

// SetModel changes the model the mock reports.
func (m *Decider) SetModel(model string) *Decider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.model = model
	return m
}

// SetUsage sets the usage every response carries.
func (m *Decider) SetUsage(inputTokens, outputTokens int64, costUSD float64) *Decider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = &loomv1.DecisionUsage{InputTokens: inputTokens, OutputTokens: outputTokens, CostUsd: costUSD}
	return m
}

// SetError makes every Decide return err (after latency). nil clears it.
func (m *Decider) SetError(err error) *Decider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
	return m
}

// SetHook installs a function run on every Decide after validation and
// recording; a non-nil error is returned instead of the scripted answers.
// Tests use it to fail requests by shape (for example, too many questions).
func (m *Decider) SetHook(h func(*loomv1.DecisionRequest) error) *Decider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hook = h
	return m
}

// SetLatency makes every Decide wait before answering, honouring ctx.
func (m *Decider) SetLatency(d time.Duration) *Decider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latency = d
	return m
}

// Answer scripts an arbitrary answer for a question id.
func (m *Decider) Answer(id string, a *loomv1.DecisionAnswer) *Decider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers[id] = a
	return m
}

// AnswerNoul scripts a Noul probability.
func (m *Decider) AnswerNoul(id string, p float64) *Decider {
	return m.Answer(id, &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Noul{Noul: &loomv1.NoulAnswer{Probability: p}}})
}

// AnswerChoice scripts a Choice from a distribution; choice and confidence
// are derived the way a real decider would derive them. It panics on a
// malformed distribution, which is a test authoring error.
func (m *Decider) AnswerChoice(id string, probabilities map[string]float64) *Decider {
	c, err := decision.ChoiceFromProbabilities(cloneProbs(probabilities))
	if err != nil {
		panic(fmt.Sprintf("mock: AnswerChoice(%q): %v", id, err))
	}
	return m.Answer(id, &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Choice{Choice: c}})
}

// AnswerScore scripts a Score from a distribution over level indices
// ("0".."n-1"). Legend is filled from the request at Decide time.
func (m *Decider) AnswerScore(id string, probabilities map[string]float64) *Decider {
	s, err := decision.ScoreFromProbabilities(cloneProbs(probabilities), nil)
	if err != nil {
		panic(fmt.Sprintf("mock: AnswerScore(%q): %v", id, err))
	}
	return m.Answer(id, &loomv1.DecisionAnswer{Kind: &loomv1.DecisionAnswer_Score{Score: s}})
}

// Decide validates the request, records it, waits any scripted latency, and
// returns the scripted answers for the request's question ids. A question
// without a scripted answer is an ErrMalformedAnswer, so a test that forgets
// to script one fails loudly.
func (m *Decider) Decide(ctx context.Context, req *loomv1.DecisionRequest) (*loomv1.DecisionResponse, error) {
	if err := decision.Validate(req); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.calls = append(m.calls, proto.Clone(req).(*loomv1.DecisionRequest))
	err, latency, model, usage, hook := m.err, m.latency, m.model, m.usage, m.hook
	answers := make(map[string]*loomv1.DecisionAnswer, len(req.Questions))
	for id := range req.Questions {
		if a, ok := m.answers[id]; ok {
			answers[id] = proto.Clone(a).(*loomv1.DecisionAnswer)
		}
	}
	m.mu.Unlock()

	if latency > 0 {
		select {
		case <-time.After(latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if hook != nil {
		if err := hook(req); err != nil {
			return nil, err
		}
	}

	for id, q := range req.Questions {
		a, ok := answers[id]
		if !ok {
			return nil, fmt.Errorf("%w: mock has no scripted answer for %q", decision.ErrMalformedAnswer, id)
		}
		// Fill the legend from the question so CheckAnswers sees what a
		// real decider would echo.
		if sq, ok := q.Kind.(*loomv1.DecisionQuestion_Score); ok {
			if sa, ok := a.Kind.(*loomv1.DecisionAnswer_Score); ok && sa.Score != nil && sa.Score.Legend == nil {
				sa.Score.Legend = decision.LegendOf(sq.Score)
			}
		}
	}

	resp := &loomv1.DecisionResponse{Model: model, Answers: answers}
	if usage != nil {
		resp.Usage = proto.Clone(usage).(*loomv1.DecisionUsage)
	} else {
		resp.Usage = &loomv1.DecisionUsage{}
	}
	if err := decision.CheckAnswers(req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Calls returns copies of every request received, in order.
func (m *Decider) Calls() []*loomv1.DecisionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*loomv1.DecisionRequest, len(m.calls))
	for i, c := range m.calls {
		out[i] = proto.Clone(c).(*loomv1.DecisionRequest)
	}
	return out
}

// CallCount is len(Calls()) without the copies.
func (m *Decider) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Reset clears recorded calls, scripted answers, error and latency.
func (m *Decider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
	m.answers = make(map[string]*loomv1.DecisionAnswer)
	m.err = nil
	m.latency = 0
}

func cloneProbs(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
