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
	"context"
	"errors"
	"sync"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// Band is a call site's confidence routing rule.
type Band struct {
	// ActMin is the MinConfidence at or above which the decider's answer is
	// used. Below it the site's existing mechanism decides.
	ActMin float64
	// Mode says how the site may use a confident answer. TightenOnly sites
	// may only make an outcome more restrictive.
	Mode loomv1.DecisionBandMode
	// Shadow records the decider's answer but never acts on it: the path is
	// always FALLBACK and the response is attached for comparison.
	Shadow bool
	// Aggregate says how a multi-question request is judged against ActMin:
	// MIN (the weakest answer must clear it) or PER_QUESTION (act when the
	// decider answered; the site applies ActMin per answer). Fan-out sites
	// (one question per candidate) use PER_QUESTION so one uncertain
	// candidate cannot veto the rest.
	Aggregate loomv1.DecisionBandAggregate
}

// PerQuestion reports whether the band judges answers individually.
func (b Band) PerQuestion() bool {
	return b.Aggregate == loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_PER_QUESTION
}

// Confident reports whether one answer clears the band's threshold.
func (b Band) Confident(a *loomv1.DecisionAnswer) bool {
	return AnswerConfidence(a) >= b.ActMin
}

// ShadowBand is the default for a site with no configured band: measure,
// never branch. A site must be given a band explicitly before its decider
// answer can change behaviour.
var ShadowBand = Band{ActMin: 1, Mode: loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE, Shadow: true,
	Aggregate: loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_MIN}

// BandFromProto converts a configured band.
func BandFromProto(b *loomv1.DecisionBand) Band {
	if b == nil {
		return ShadowBand
	}
	mode := b.Mode
	if mode == loomv1.DecisionBandMode_DECISION_BAND_MODE_UNSPECIFIED {
		mode = loomv1.DecisionBandMode_DECISION_BAND_MODE_REPLACE
	}
	agg := b.Aggregate
	if agg == loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_UNSPECIFIED {
		agg = loomv1.DecisionBandAggregate_DECISION_BAND_AGGREGATE_MIN
	}
	return Band{ActMin: clamp01(b.ActMin), Mode: mode, Shadow: b.Shadow, Aggregate: agg}
}

// Outcome is what a Router returns: the decider's response when it produced
// one, which path the call site should take, and the confidence that path
// was chosen on. A call site reads Path first.
type Outcome struct {
	// Response is the decider's answer, or nil on ERROR, DISABLED and BUDGET.
	// On FALLBACK it is present when the decider answered below the band
	// (so shadow comparisons can record it) and nil otherwise.
	Response *loomv1.DecisionResponse
	// Path says who decides: DECIDER means act on Response; anything else
	// means run the site's existing mechanism.
	Path loomv1.DecisionPath
	// Confidence is MinConfidence(Response), or 0 without a response.
	Confidence float64
	// Band is the rule that produced this outcome.
	Band Band
	// Err is the decider error on ERROR, ErrDisabled on DISABLED,
	// ErrBudgetExhausted on BUDGET, nil otherwise.
	Err error
	// Latency is the decider round trip, zero when none was made.
	Latency time.Duration
}

// Act reports whether the call site should branch on Response.
func (o Outcome) Act() bool { return o.Path == loomv1.DecisionPath_DECISION_PATH_DECIDER }

// Router applies per-site bands and per-session budgets in front of a
// Decider and reports which path answered. It is safe for concurrent use.
type Router struct {
	decider Decider
	tracer  observability.Tracer

	mu    sync.RWMutex
	bands map[string]Band

	budget *budget
}

// RouterOption configures a Router.
type RouterOption func(*Router)

// WithBand sets the band for one site.
func WithBand(site string, b Band) RouterOption {
	return func(r *Router) { r.bands[site] = b }
}

// WithBands sets bands from configuration.
func WithBands(bands []*loomv1.DecisionBand) RouterOption {
	return func(r *Router) {
		for _, b := range bands {
			if b != nil && b.Site != "" {
				r.bands[b.Site] = BandFromProto(b)
			}
		}
	}
}

// WithBudget caps decisions per session by count and by cost. Zero means
// unlimited for that dimension.
func WithBudget(maxPerSession int64, maxCostUSD float64) RouterOption {
	return func(r *Router) { r.budget = newBudget(maxPerSession, maxCostUSD) }
}

// WithTracer records fallback metrics. Spans for the decider call itself come
// from wrapping the Decider in Instrumented.
func WithTracer(t observability.Tracer) RouterOption {
	return func(r *Router) { r.tracer = t }
}

// NewRouter builds a Router. A nil decider routes every request to DISABLED.
func NewRouter(d Decider, opts ...RouterOption) *Router {
	r := &Router{decider: d, bands: make(map[string]Band)}
	for _, o := range opts {
		o(r)
	}
	if r.tracer == nil {
		r.tracer = observability.NewNoOpTracer()
	}
	return r
}

// Band returns the band for a site, or ShadowBand when none is configured.
func (r *Router) Band(site string) Band {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if b, ok := r.bands[site]; ok {
		return b
	}
	return ShadowBand
}

// SetBand replaces a site's band at runtime.
func (r *Router) SetBand(site string, b Band) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bands[site] = b
}

// Decider returns the wrapped decider, or nil.
func (r *Router) Decider() Decider { return r.decider }

// Decide routes one request. It never returns an error: failures are
// reported on Outcome.Path and Outcome.Err so a call site's fallback runs the
// same way whatever went wrong. The request is validated before any decider
// is called.
func (r *Router) Decide(ctx context.Context, req *loomv1.DecisionRequest) Outcome {
	site := ""
	if req != nil {
		site = req.Site
	}
	band := r.Band(site)
	out := Outcome{Path: loomv1.DecisionPath_DECISION_PATH_FALLBACK, Band: band}

	if r.decider == nil {
		out.Path = loomv1.DecisionPath_DECISION_PATH_DISABLED
		out.Err = ErrDisabled
		r.recordFallback(site, out.Path)
		return out
	}
	if err := Validate(req); err != nil {
		out.Path = loomv1.DecisionPath_DECISION_PATH_ERROR
		out.Err = err
		r.recordFallback(site, out.Path)
		return out
	}

	sessionID := SessionIDFromContext(ctx)
	if r.budget != nil && !r.budget.allow(sessionID) {
		out.Path = loomv1.DecisionPath_DECISION_PATH_BUDGET
		out.Err = ErrBudgetExhausted
		r.recordFallback(site, out.Path)
		return out
	}

	start := time.Now()
	resp, err := r.decider.Decide(ctx, req)
	out.Latency = time.Since(start)
	if err != nil {
		out.Err = err
		if errors.Is(err, ErrDisabled) {
			out.Path = loomv1.DecisionPath_DECISION_PATH_DISABLED
		} else {
			out.Path = loomv1.DecisionPath_DECISION_PATH_ERROR
		}
		r.recordFallback(site, out.Path)
		return out
	}
	if err := CheckAnswers(req, resp); err != nil {
		out.Err = err
		out.Path = loomv1.DecisionPath_DECISION_PATH_ERROR
		r.recordFallback(site, out.Path)
		return out
	}

	if r.budget != nil {
		cost := 0.0
		if resp.Usage != nil {
			cost = resp.Usage.CostUsd
		}
		r.budget.record(sessionID, cost)
	}

	out.Response = resp
	out.Confidence = MinConfidence(resp)
	if !band.Shadow && (band.PerQuestion() || out.Confidence >= band.ActMin) {
		out.Path = loomv1.DecisionPath_DECISION_PATH_DECIDER
		return out
	}
	r.recordFallback(site, out.Path)
	return out
}

// ForgetSession releases a session's budget accounting. Hosts call it when
// they retire a session so the table is bounded by live sessions.
func (r *Router) ForgetSession(sessionID string) {
	if r.budget != nil {
		r.budget.forget(sessionID)
	}
}

func (r *Router) recordFallback(site string, path loomv1.DecisionPath) {
	r.tracer.RecordMetric(observability.MetricDecisionFallbacks, 1, map[string]string{
		"decision.site": site,
		"decision.path": path.String(),
	})
}

// budget is per-session call and cost accounting.
type budget struct {
	maxCalls int64
	maxCost  float64

	mu       sync.Mutex
	sessions map[string]*spend
}

type spend struct {
	calls int64
	cost  float64
}

func newBudget(maxCalls int64, maxCost float64) *budget {
	return &budget{maxCalls: maxCalls, maxCost: maxCost, sessions: make(map[string]*spend)}
}

func (b *budget) allow(sessionID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sessionID]
	if s == nil {
		return true
	}
	if b.maxCalls > 0 && s.calls >= b.maxCalls {
		return false
	}
	if b.maxCost > 0 && s.cost >= b.maxCost {
		return false
	}
	return true
}

func (b *budget) record(sessionID string, cost float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sessionID]
	if s == nil {
		s = &spend{}
		b.sessions[sessionID] = s
	}
	s.calls++
	s.cost += cost
}

func (b *budget) forget(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sessions, sessionID)
}
