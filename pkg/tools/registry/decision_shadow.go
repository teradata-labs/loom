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

package registry

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/sites"
)

// shadowTimeout bounds one background shadow evaluation.
const shadowTimeout = 30 * time.Second

// liveDecideTimeout bounds a decider call made on the search path ahead of
// the LLM rerank; slower than this and the search falls back.
const liveDecideTimeout = 3 * time.Second

// decisionSignal is the RelevanceSignal type written on results the decider
// ranked.
const decisionSignal = "decision"

// DecisionBinding is one agent's decision layer as its tool_search sees it.
// It travels with that agent's SearchTool, never with the shared Registry:
// the registry is one per server and serves every agent, so a router stored
// on it would let one agent's decider and bands govern every agent's search.
type DecisionBinding struct {
	// Router is the agent's decision router. Nil means no decision layer.
	Router *decision.Router
	// Recorder persists shadow rows; nil traces nothing and stores nothing.
	Recorder *decision.ShadowRecorder
	// Track runs a background shadow evaluation on the owner's books so the
	// owner (the agent) can wait for it at shutdown. Nil runs it on the
	// SearchTool's own WaitGroup (SearchTool.WaitDecisionShadows).
	Track func(fn func())
}

// SearchToolOption configures a SearchTool at construction.
type SearchToolOption func(*SearchTool)

// WithDecision gives this SearchTool (one agent's tool_search) the agent's
// decision layer. A binding with a nil Router is ignored: an agent with no
// decision layer gets no router.
func WithDecision(b DecisionBinding) SearchToolOption {
	return func(t *SearchTool) {
		if b.Router == nil {
			return
		}
		sd := &searchDecision{router: b.Router, recorder: b.Recorder, track: b.Track}
		if sd.track == nil {
			sd.track = func(fn func()) {
				t.decisionWG.Add(1)
				go func() {
					defer t.decisionWG.Done()
					fn()
				}()
			}
		}
		t.decision = sd
	}
}

// searchDecision is the per-call decision context Registry.search receives
// from the SearchTool that made the call. Nil means no decision layer; every
// method is safe on a nil receiver.
type searchDecision struct {
	router   *decision.Router
	recorder *decision.ShadowRecorder
	track    func(fn func())
}

// rerankRequest builds the tool_search rerank request over candidate names
// and descriptions (never their scores or schemas).
func rerankRequest(query string, candidates []*loomv1.ToolSearchResult) (*loomv1.DecisionRequest, error) {
	texts := make([]string, 0, len(candidates))
	for _, c := range candidates {
		text := ""
		if c != nil && c.Tool != nil {
			text = c.Tool.Name + ": " + c.Tool.Description
		}
		texts = append(texts, text)
	}
	return sites.RerankRequest(sites.SiteToolSearchRerank, query, texts)
}

// keptIndexes maps the LLM rerank's output back to candidate indexes and
// keeps only the ones the LLM scored at or above sites.RerankKeepProbability.
// The LLM rerank re-orders and returns everything it scored above 0.3, so
// "returned" is not a relevance verdict; its score is. The first campaign
// compared the decider against "returned" and read 13% agreement on a site
// where the decider was the more discriminating party.
func keptIndexes(candidates, reranked []*loomv1.ToolSearchResult) []int {
	keptSet := make(map[*loomv1.ToolSearchResult]struct{}, len(reranked))
	for _, k := range reranked {
		if k != nil && k.Confidence >= sites.RerankKeepProbability {
			keptSet[k] = struct{}{}
		}
	}
	idx := make([]int, 0, len(keptSet))
	for i, c := range candidates {
		if _, ok := keptSet[c]; ok {
			idx = append(idx, i)
		}
	}
	return idx
}

// providerName is the decider label written on shadow rows.
func (d *searchDecision) providerName() string {
	if d == nil || d.router == nil || d.router.Decider() == nil {
		return ""
	}
	return d.router.Decider().Name()
}

// recordAsync writes shadow rows off the search path, on the owning agent's
// books.
func (d *searchDecision) recordAsync(ctx context.Context, logger *zap.Logger, req *loomv1.DecisionRequest, out decision.Outcome, refs map[string]decision.Reference) {
	if d == nil || d.router == nil || req == nil {
		return
	}
	provider := d.providerName()
	sessionID := decision.SessionIDFromContext(ctx)
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	recorder := d.recorder
	d.track(func() {
		recCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		records := decision.BuildShadowRecords(req, out, provider, sessionID, refs)
		if err := recorder.Record(recCtx, records); err != nil {
			logger.Warn("decision shadow: record failed", zap.String("site", req.Site), zap.Int("rows", len(records)), zap.Error(err))
		}
	})
}

// shadowRerank evaluates the decider's relevance judgment for every candidate
// in the background and records it against the candidates the LLM kept.
func (d *searchDecision) shadowRerank(ctx context.Context, logger *zap.Logger, query string, candidates, kept []*loomv1.ToolSearchResult) {
	if d == nil || d.router == nil || len(candidates) == 0 {
		return
	}
	req, err := rerankRequest(query, candidates)
	if err != nil {
		logger.Debug("decision shadow: tool_search rerank request", zap.Error(err))
		return
	}
	refs := sites.RerankReference(len(candidates), keptIndexes(candidates, kept), sites.ReferenceSourceLLMRerank)
	provider := d.providerName()
	sessionID := decision.SessionIDFromContext(ctx)
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	router, recorder := d.router, d.recorder
	d.track(func() {
		shadowCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		out := router.Decide(shadowCtx, req)
		records := decision.BuildShadowRecords(req, out, provider, sessionID, refs)
		if err := recorder.Record(shadowCtx, records); err != nil {
			logger.Warn("decision shadow: record failed", zap.String("site", req.Site), zap.Int("rows", len(records)), zap.Error(err))
		}
	})
}

// liveRerank is the live path for tool_search's rerank. With a live band on
// the calling agent's router it asks that agent's decider first; when the
// band says to act it returns the kept candidates ordered by relevance
// probability, each carrying a "decision" signal, and the LLM rerank is
// skipped. When the decider does not clear the band, acted is false and the
// request and outcome are returned so the caller can record them against the
// LLM's answer.
func (d *searchDecision) liveRerank(ctx context.Context, logger *zap.Logger, query string, candidates []*loomv1.ToolSearchResult) (results []*loomv1.ToolSearchResult, acted bool, req *loomv1.DecisionRequest, out decision.Outcome) {
	if d == nil || d.router == nil || len(candidates) == 0 || d.router.Band(sites.SiteToolSearchRerank).Shadow {
		return nil, false, nil, decision.Outcome{}
	}
	req, err := rerankRequest(query, candidates)
	if err != nil {
		logger.Debug("decision: tool_search rerank request", zap.Error(err))
		return nil, false, nil, decision.Outcome{}
	}
	liveCtx, cancel := context.WithTimeout(ctx, liveDecideTimeout)
	defer cancel()
	out = d.router.Decide(liveCtx, req)
	if !out.Act() {
		return nil, false, req, out
	}
	idx, uncertain := sites.RerankKeptWithBand(out.Response, len(candidates), out.Band)
	if !sites.RerankContributed(len(candidates), uncertain) {
		// Every answer was uncertain: the LLM rerank decides.
		out.Path = loomv1.DecisionPath_DECISION_PATH_FALLBACK
		return nil, false, req, out
	}
	results = make([]*loomv1.ToolSearchResult, 0, len(idx))
	for _, i := range idx {
		if i >= len(candidates) {
			continue
		}
		c := candidates[i]
		if a, err := decision.NoulOf(out.Response, sites.CandidateQuestionID(i)); err == nil {
			c.Confidence = a.Probability
			c.MatchReason = "decision: relevance " + fmt.Sprintf("%.2f", a.Probability)
			c.Signals = append(c.Signals, &loomv1.RelevanceSignal{
				SignalType:  decisionSignal,
				Description: c.MatchReason,
				Weight:      a.Probability,
			})
		}
		results = append(results, c)
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Confidence > results[j].Confidence })
	logger.Debug("decision: tool_search rerank acted",
		zap.Int("candidates", len(candidates)), zap.Int("kept", len(results)),
		zap.Int("uncertain_kept", uncertain), zap.Duration("latency", out.Latency))
	return results, true, req, out
}
