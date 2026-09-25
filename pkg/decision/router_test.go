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

package decision_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/observability"
)

func noulRequest(t *testing.T, site string) *loomv1.DecisionRequest {
	t.Helper()
	req, err := decision.NewRequest(site, "state", map[string]*loomv1.DecisionQuestion{
		"q": decision.Noul("true?"),
	})
	require.NoError(t, err)
	return req
}

func TestRouterPaths(t *testing.T) {
	t.Parallel()
	const site = "test.site"
	replace := decision.Band{ActMin: 0.8, Mode: loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE}

	tests := []struct {
		name       string
		decider    func() decision.Decider
		opts       []decision.RouterOption
		req        func(t *testing.T) *loomv1.DecisionRequest
		wantPath   loomv1.DecisionPath
		wantErrIs  error
		wantAct    bool
		wantResp   bool
		wantConfGE float64
	}{
		{
			name:     "nil decider is disabled",
			decider:  func() decision.Decider { return nil },
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_DISABLED, wantErrIs: decision.ErrDisabled,
		},
		{
			name:     "off decider is disabled",
			decider:  func() decision.Decider { return decision.Off{} },
			opts:     []decision.RouterOption{decision.WithBand(site, replace)},
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_DISABLED, wantErrIs: decision.ErrDisabled,
		},
		{
			name:     "invalid request is error before any call",
			decider:  func() decision.Decider { return mock.New().AnswerNoul("q", 0.99) },
			opts:     []decision.RouterOption{decision.WithBand(site, replace)},
			req:      func(*testing.T) *loomv1.DecisionRequest { return &loomv1.DecisionRequest{Site: site} },
			wantPath: loomv1.DecisionPath_DECISION_PATH_ERROR, wantErrIs: decision.ErrValidation,
		},
		{
			name:     "decider error",
			decider:  func() decision.Decider { return mock.New().SetError(decision.ErrRateLimited) },
			opts:     []decision.RouterOption{decision.WithBand(site, replace)},
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_ERROR, wantErrIs: decision.ErrRateLimited,
		},
		{
			name:     "malformed answer is error",
			decider:  func() decision.Decider { return mock.New() }, // nothing scripted
			opts:     []decision.RouterOption{decision.WithBand(site, replace)},
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_ERROR, wantErrIs: decision.ErrMalformedAnswer,
		},
		{
			name:     "no band configured means shadow: response attached, never act",
			decider:  func() decision.Decider { return mock.New().AnswerNoul("q", 1.0) },
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_FALLBACK, wantResp: true, wantConfGE: 1,
		},
		{
			name:     "explicit shadow band never acts",
			decider:  func() decision.Decider { return mock.New().AnswerNoul("q", 1.0) },
			opts:     []decision.RouterOption{decision.WithBand(site, decision.Band{ActMin: 0.1, Shadow: true})},
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_FALLBACK, wantResp: true, wantConfGE: 1,
		},
		{
			name:     "confident answer acts",
			decider:  func() decision.Decider { return mock.New().AnswerNoul("q", 0.95) }, // decisiveness 0.9
			opts:     []decision.RouterOption{decision.WithBand(site, replace)},
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_DECIDER, wantAct: true, wantResp: true, wantConfGE: 0.8,
		},
		{
			name:     "uncertain answer falls back with response attached",
			decider:  func() decision.Decider { return mock.New().AnswerNoul("q", 0.6) }, // decisiveness 0.2
			opts:     []decision.RouterOption{decision.WithBand(site, replace)},
			req:      func(t *testing.T) *loomv1.DecisionRequest { return noulRequest(t, site) },
			wantPath: loomv1.DecisionPath_DECISION_PATH_FALLBACK, wantResp: true,
		},
		{
			name:    "band applies per site",
			decider: func() decision.Decider { return mock.New().AnswerNoul("q", 0.95) },
			opts:    []decision.RouterOption{decision.WithBand("other.site", replace)},
			req: func(t *testing.T) *loomv1.DecisionRequest {
				return noulRequest(t, site)
			},
			wantPath: loomv1.DecisionPath_DECISION_PATH_FALLBACK, wantResp: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := decision.NewRouter(tt.decider(), tt.opts...)
			out := r.Decide(context.Background(), tt.req(t))
			assert.Equal(t, tt.wantPath.String(), out.Path.String())
			assert.Equal(t, tt.wantAct, out.Act())
			if tt.wantErrIs != nil {
				assert.True(t, errors.Is(out.Err, tt.wantErrIs), "want %v, got %v", tt.wantErrIs, out.Err)
			} else {
				assert.NoError(t, out.Err)
			}
			if tt.wantResp {
				require.NotNil(t, out.Response)
				assert.GreaterOrEqual(t, out.Confidence, tt.wantConfGE)
			} else {
				assert.Nil(t, out.Response)
			}
		})
	}
}

func TestRouterPerQuestionAggregateActsDespiteOneUncertainAnswer(t *testing.T) {
	t.Parallel()
	const site = "fanout"
	m := mock.New().AnswerNoul("c0", 0.99).AnswerNoul("c1", 0.5) // c1 has decisiveness 0
	req, err := decision.NewRequest(site, "s", map[string]*loomv1.DecisionQuestion{
		"c0": decision.Noul("?"), "c1": decision.Noul("?"),
	})
	require.NoError(t, err)

	minBand := decision.Band{ActMin: 0.5}
	assert.False(t, decision.NewRouter(m, decision.WithBand(site, minBand)).Decide(context.Background(), req).Act(),
		"MIN aggregate: the uncertain candidate vetoes")

	pq := decision.Band{ActMin: 0.5, Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION}
	out := decision.NewRouter(m, decision.WithBand(site, pq)).Decide(context.Background(), req)
	assert.True(t, out.Act(), "PER_QUESTION aggregate: the request acts")
	assert.True(t, out.Band.Confident(out.Response.Answers["c0"]))
	assert.False(t, out.Band.Confident(out.Response.Answers["c1"]), "the site sees which answers cleared the band")

	pqShadow := pq
	pqShadow.Shadow = true
	assert.False(t, decision.NewRouter(m, decision.WithBand(site, pqShadow)).Decide(context.Background(), req).Act(), "shadow still never acts")

	fromProto := decision.BandFromProto(&loomv1.DecisionBand{Site: site, ActMin: 0.5, Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION})
	assert.True(t, fromProto.PerQuestion())
	assert.False(t, decision.BandFromProto(&loomv1.DecisionBand{Site: site}).PerQuestion(), "unspecified aggregate is MIN")
}

func TestRouterMinConfidenceGatesOnWeakestAnswer(t *testing.T) {
	t.Parallel()
	const site = "multi"
	m := mock.New().
		AnswerNoul("sure", 1.0).
		AnswerChoice("unsure", map[string]float64{"a": 0.5, "b": 0.5})
	choice, err := decision.Choice("pick", map[string]any{"a": "A", "b": "B"})
	require.NoError(t, err)
	req, err := decision.NewRequest(site, "s", map[string]*loomv1.DecisionQuestion{
		"sure":   decision.Noul("?"),
		"unsure": choice,
	})
	require.NoError(t, err)

	r := decision.NewRouter(m, decision.WithBand(site, decision.Band{ActMin: 0.5}))
	out := r.Decide(context.Background(), req)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, out.Path, "a uniform choice drags MinConfidence to 0")
	assert.InDelta(t, 0, out.Confidence, 1e-9)
}

func TestRouterBudget(t *testing.T) {
	t.Parallel()
	const site = "budget"
	m := mock.New().AnswerNoul("q", 0.99).SetUsage(10, 0, 0.001)
	r := decision.NewRouter(m,
		decision.WithBand(site, decision.Band{ActMin: 0.5}),
		decision.WithBudget(2, 0),
	)
	ctxA := decision.WithSessionID(context.Background(), "A")
	ctxB := decision.WithSessionID(context.Background(), "B")
	req := noulRequest(t, site)

	assert.True(t, r.Decide(ctxA, req).Act())
	assert.True(t, r.Decide(ctxA, req).Act())
	third := r.Decide(ctxA, req)
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_BUDGET, third.Path)
	assert.True(t, errors.Is(third.Err, decision.ErrBudgetExhausted))
	assert.Equal(t, 2, m.CallCount(), "budget refusal makes no decider call")

	assert.True(t, r.Decide(ctxB, req).Act(), "budgets are per session")

	r.ForgetSession("A")
	assert.True(t, r.Decide(ctxA, req).Act(), "forgetting resets the session")

	// Cost budget.
	rc := decision.NewRouter(mock.New().AnswerNoul("q", 0.99).SetUsage(1, 0, 0.6),
		decision.WithBand(site, decision.Band{ActMin: 0.5}),
		decision.WithBudget(0, 1.0),
	)
	assert.True(t, rc.Decide(ctxA, req).Act())
	assert.True(t, rc.Decide(ctxA, req).Act(), "0.6 spent, still under 1.0")
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_BUDGET, rc.Decide(ctxA, req).Path, "1.2 spent")
}

func TestRouterBandFromProtoAndSetBand(t *testing.T) {
	t.Parallel()
	r := decision.NewRouter(mock.New(), decision.WithBands([]*loomv1.DecisionBand{
		{Site: "a", ActMin: 0.7},
		{Site: "b", ActMin: 2.0, Mode: loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY, Shadow: true},
		{Site: "", ActMin: 0.1}, // ignored: no site
		nil,
	}))
	a := r.Band("a")
	assert.InDelta(t, 0.7, a.ActMin, 1e-9)
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE, a.Mode, "unspecified defaults to replace")
	b := r.Band("b")
	assert.InDelta(t, 1.0, b.ActMin, 1e-9, "clamped")
	assert.Equal(t, loomv1.DecisionBandMode_DECISION_BAND_MODE_TIGHTEN_ONLY, b.Mode)
	assert.True(t, b.Shadow)
	assert.Equal(t, decision.ShadowBand, r.Band("unknown"))
	assert.Equal(t, decision.ShadowBand, decision.BandFromProto(nil))

	r.SetBand("unknown", decision.Band{ActMin: 0.3})
	assert.InDelta(t, 0.3, r.Band("unknown").ActMin, 1e-9)
}

func TestRouterRecordsFallbackMetric(t *testing.T) {
	t.Parallel()
	tracer := newRecordingTracer()
	r := decision.NewRouter(mock.New().AnswerNoul("q", 0.5), decision.WithTracer(tracer))
	out := r.Decide(context.Background(), noulRequest(t, "m"))
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK, out.Path)
	metrics := tracer.Metrics(observability.MetricDecisionFallbacks)
	require.Len(t, metrics, 1)
	assert.Equal(t, "m", metrics[0].Labels["decision.site"])
	assert.Equal(t, loomv1.DecisionPath_DECISION_PATH_FALLBACK.String(), metrics[0].Labels["decision.path"])

	// An acting outcome records no fallback.
	acted := decision.NewRouter(mock.New().AnswerNoul("q", 0.99),
		decision.WithTracer(tracer), decision.WithBand("m", decision.Band{ActMin: 0.5}))
	require.True(t, acted.Decide(context.Background(), noulRequest(t, "m")).Act())
	assert.Len(t, tracer.Metrics(observability.MetricDecisionFallbacks), 1)
}

// TestRouterConcurrent exercises bands, budgets and the mock under the race
// detector: many sessions deciding at once, one goroutine rewriting bands.
func TestRouterConcurrent(t *testing.T) {
	t.Parallel()
	const site = "conc"
	m := mock.New().AnswerNoul("q", 0.99).SetUsage(1, 0, 0.0001)
	r := decision.NewRouter(m,
		decision.WithBand(site, decision.Band{ActMin: 0.5}),
		decision.WithBudget(50, 0),
	)
	req := noulRequest(t, site)

	var wg sync.WaitGroup
	const sessions, perSession = 16, 20
	acts := make([]int, sessions)
	for s := 0; s < sessions; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			ctx := decision.WithSessionID(context.Background(), fmt.Sprintf("s%d", s))
			for i := 0; i < perSession; i++ {
				if r.Decide(ctx, req).Act() {
					acts[s]++
				}
			}
		}(s)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r.SetBand(site, decision.Band{ActMin: float64(i%2) * 0.5})
			_ = r.Band(site)
		}
	}()
	wg.Wait()
	total := 0
	for _, a := range acts {
		total += a
	}
	assert.Equal(t, sessions*perSession, total, "every call under budget and above band acts")
	assert.Equal(t, sessions*perSession, m.CallCount())
}

func TestBandIsTrue(t *testing.T) {
	t.Parallel()
	var none decision.Band
	assert.True(t, none.IsTrue(0.5), "the default threshold is 0.5")
	assert.False(t, none.IsTrue(0.49))
	assert.True(t, none.IsTrue(decision.DefaultTrueMin))

	lower := decision.Band{TrueMin: 0.3}
	assert.True(t, lower.IsTrue(0.3))
	assert.False(t, lower.IsTrue(0.29))

	// A band from the proto carries it, clamped.
	b := decision.BandFromProto(&loomv1.DecisionBand{Site: "s", ActMin: 0.5, TrueMin: 0.3})
	assert.InDelta(t, 0.3, b.TrueMin, 1e-9)
	assert.True(t, b.IsTrue(0.31))
	assert.InDelta(t, 0, decision.BandFromProto(&loomv1.DecisionBand{Site: "s", TrueMin: -1}).TrueMin, 1e-9)
	assert.InDelta(t, 1, decision.BandFromProto(&loomv1.DecisionBand{Site: "s", TrueMin: 5}).TrueMin, 1e-9)
}
