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

package index

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/skills"
)

// Decision-layer budgets for the tree walk. The walk is on the hot path:
// every step happens before the agent's turn begins, so a live band trades
// this much latency for skipping a generative call at this node.
const (
	routeLivePerWave   = 4 * time.Second
	routeMaxLiveBudget = 12 * time.Second
	// routeShadowTimeout bounds a background comparison, which races
	// nothing.
	routeShadowTimeout = 30 * time.Second
)

// WithRouterDecision attaches a decision router and the store its shadow
// rows are written to. Without it the walk behaves exactly as before.
func WithRouterDecision(dr *decision.Router, store decision.ShadowStore) RouterOption {
	return func(r *Router) { r.SetDecision(dr, store) }
}

// SetDecision attaches (or, with a nil router, detaches) the owning agent's
// decision layer. A skill Router belongs to one agent (BuildSkillsOptions
// builds one per agent), and the agent calls this once its own decision
// router exists, which is after the options that built this Router ran.
// Safe for concurrent use with Route.
func (r *Router) SetDecision(dr *decision.Router, store decision.ShadowStore) {
	if dr == nil {
		r.skillDecision.Store(nil)
		return
	}
	r.skillDecision.Store(&skillDecision{router: dr, store: store})
}

// DecisionRouter returns the attached decision router, or nil.
func (r *Router) DecisionRouter() *decision.Router {
	if d := r.dec(); d != nil {
		return d.router
	}
	return nil
}

// dec returns the attached decision layer, or nil.
func (r *Router) dec() *skillDecision { return r.skillDecision.Load() }

// WaitDecisionShadows blocks until every background comparison has
// finished. Tests call it before asserting on the store.
func (r *Router) WaitDecisionShadows() { r.decisionWG.Wait() }

// recorderFor builds d's recorder on first use, with the Router's tracer
// (set by an option that may run after WithRouterDecision).
func (r *Router) recorderFor(d *skillDecision) *decision.ShadowRecorder {
	d.recorderOnce.Do(func() {
		d.recorder = decision.NewShadowRecorder(d.store, r.tracer)
	})
	return d.recorder
}

// routeOptions renders a node's children and directly-attached skills as the
// option list the decider is asked about, in the same order the caller will
// read the answers back.
func routeOptions(children []*skills.SkillIndexNode, directSkills []*skills.Skill) []sites.RouteOption {
	out := make([]sites.RouteOption, 0, len(children)+len(directSkills))
	for _, c := range children {
		if c == nil {
			continue
		}
		out = append(out, sites.RouteOption{
			Kind:  sites.OptionKindSubtree,
			ID:    c.ID,
			Title: c.Title,
			Text:  c.Summary,
		})
	}
	for _, s := range directSkills {
		if s == nil {
			continue
		}
		title := s.Title
		if title == "" {
			title = s.Name
		}
		out = append(out, sites.RouteOption{
			Kind:  sites.OptionKindSkill,
			ID:    s.Name,
			Title: title,
			Text:  s.Description,
		})
	}
	return out
}

// liveRoute asks the decider first and, when its band says to act and the
// answer is worth acting on, returns the walk's decision for this node
// without a generative call. acted is false when the caller must run the
// LLM; the outcome is returned either way so the caller can record it
// against the LLM's answer without asking twice.
func (r *Router) liveRoute(ctx context.Context, sessionID, message string, node *skills.SkillIndexNode,
	options []sites.RouteOption) (sel sites.RouteSelection, acted bool, req *loomv1.DecisionRequest, out decision.Outcome) {
	d := r.dec()
	if d == nil || len(options) == 0 || d.router.Band(sites.SiteSkillRoute).Shadow {
		return sel, false, nil, decision.Outcome{}
	}
	req, err := sites.SkillRouteRequest(message, node.Title, node.Summary, options)
	if err != nil {
		r.logger.Debug("skill route: request", zap.String("node", node.ID), zap.Error(err))
		return sel, false, nil, decision.Outcome{}
	}
	budget := decision.Budget(d.router.Decider(), len(req.Questions), routeLivePerWave, routeMaxLiveBudget)
	liveCtx, cancel := context.WithTimeout(decision.WithSessionID(ctx, sessionID), budget)
	defer cancel()
	out = d.router.Decide(liveCtx, req)
	if !out.Act() {
		return sel, false, req, out
	}
	sel = sites.SkillRouteSelected(out.Response, options, out.Band, r.maxCandidates)
	if !sites.SkillRouteContributed(sel) {
		// Every answer was uncertain: "descend into everything, select
		// nothing" is not a routing decision. The LLM decides and the row
		// is recorded against it.
		out.Path = loomv1.DecisionPath_DECISION_PATH_FALLBACK
		return sites.RouteSelection{}, false, req, out
	}
	r.logger.Debug("skill route: decider acted",
		zap.String("node", node.ID), zap.Int("options", len(options)),
		zap.Int("descend", len(sel.Descend)), zap.Int("skills", len(sel.Skills)),
		zap.Int("uncertain", sel.Uncertain), zap.Duration("latency", out.Latency))
	return sel, true, req, out
}

// shadowRoute compares the decider against what the LLM walk chose, in the
// background. It never blocks the walk and never surfaces an error.
func (r *Router) shadowRoute(ctx context.Context, sessionID, message string, node *skills.SkillIndexNode,
	options []sites.RouteOption, chosen []string, source string) {
	if r.dec() == nil || len(options) == 0 {
		return
	}
	req, err := sites.SkillRouteRequest(message, node.Title, node.Summary, options)
	if err != nil {
		r.logger.Debug("skill route shadow: request", zap.String("node", node.ID), zap.Error(err))
		return
	}
	refs := sites.SkillRouteReference(options, chosen, source, sites.SkillRouteSubjects(options))
	r.runShadow(ctx, sessionID, req, refs)
}

// runShadow evaluates req in the background and records it against refs.
func (r *Router) runShadow(ctx context.Context, sessionID string, req *loomv1.DecisionRequest, refs map[string]decision.Reference) {
	// Detach from the caller: the turn that produced this comparison may end
	// before the shadow finishes, and that is fine.
	d := r.dec()
	if d == nil {
		return
	}
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	r.decisionWG.Add(1)
	go func() {
		defer r.decisionWG.Done()
		shadowCtx, cancel := context.WithTimeout(bg, routeShadowTimeout)
		defer cancel()
		out := d.router.Decide(shadowCtx, req)
		if out.Path == loomv1.DecisionPath_DECISION_PATH_ERROR {
			r.logger.Warn("skill route shadow: decider error",
				zap.String("site", req.Site), zap.Int("questions", len(req.Questions)), zap.Error(out.Err))
		}
		r.record(shadowCtx, d, sessionID, req, out, refs)
	}()
}

// recordAsync records an outcome the caller already holds, off the walk.
func (r *Router) recordAsync(ctx context.Context, sessionID string, req *loomv1.DecisionRequest,
	out decision.Outcome, refs map[string]decision.Reference) {
	d := r.dec()
	if d == nil || req == nil {
		return
	}
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	r.decisionWG.Add(1)
	go func() {
		defer r.decisionWG.Done()
		recCtx, cancel := context.WithTimeout(bg, routeShadowTimeout)
		defer cancel()
		r.record(recCtx, d, sessionID, req, out, refs)
	}()
}

func (r *Router) record(ctx context.Context, d *skillDecision, sessionID string, req *loomv1.DecisionRequest,
	out decision.Outcome, refs map[string]decision.Reference) {
	provider := ""
	if dd := d.router.Decider(); dd != nil {
		provider = dd.Name()
	}
	records := decision.BuildShadowRecords(req, out, provider, sessionID, refs)
	if err := r.recorderFor(d).Record(ctx, records); err != nil {
		r.logger.Warn("skill route shadow: record failed",
			zap.String("site", req.Site), zap.Int("rows", len(records)), zap.Error(err))
	}
}

// decisionFields are the Router's decision-layer state, kept here so the
// walk's own file stays about the walk.
type decisionFields struct {
	skillDecision atomic.Pointer[skillDecision]
	decisionWG    sync.WaitGroup
}

// skillDecision is one agent's decision layer as its skill Router sees it.
type skillDecision struct {
	router       *decision.Router
	store        decision.ShadowStore
	recorder     *decision.ShadowRecorder
	recorderOnce sync.Once
}
