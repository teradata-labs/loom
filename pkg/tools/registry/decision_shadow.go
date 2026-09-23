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

// SetDecisionRouter wires the decision layer into tool_search's rerank. The
// agent calls it when it hands the registry to its executor; a nil router
// disables the shadow. The store may be nil (shadows traced, not persisted).
func (r *Registry) SetDecisionRouter(router *decision.Router, store decision.ShadowStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisionRouter = router
	r.decisionRecorder = decision.NewShadowRecorder(store, r.tracer)
}

// WaitDecisionShadows blocks until in-flight shadow evaluations finish. Tests
// call it before asserting on the store.
func (r *Registry) WaitDecisionShadows() { r.decisionWG.Wait() }

// liveDecideTimeout bounds a decider call made on the search path ahead of
// the LLM rerank; slower than this and the search falls back.
const liveDecideTimeout = 3 * time.Second

// decisionSignal is the RelevanceSignal type written on results the decider
// ranked.
const decisionSignal = "decision"

func (r *Registry) decisionParts() (*decision.Router, *decision.ShadowRecorder) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.decisionRouter, r.decisionRecorder
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
// toolSubjects renders each candidate's tool name as a shadow-row subject.
func toolSubjects(candidates []*loomv1.ToolSearchResult) []string {
	out := make([]string, len(candidates))
	for i, c := range candidates {
		if c != nil && c.Tool != nil {
			out[i] = "tool:" + c.Tool.Name
		}
	}
	return out
}

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

// recordAsync writes shadow rows off the search path.
func (r *Registry) recordAsync(ctx context.Context, router *decision.Router, recorder *decision.ShadowRecorder, req *loomv1.DecisionRequest, out decision.Outcome, refs map[string]decision.Reference) {
	provider := ""
	if router.Decider() != nil {
		provider = router.Decider().Name()
	}
	sessionID := decision.SessionIDFromContext(ctx)
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	r.decisionWG.Add(1)
	go func() {
		defer r.decisionWG.Done()
		recCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		records := decision.BuildShadowRecords(req, out, provider, sessionID, refs)
		if err := recorder.Record(recCtx, records); err != nil {
			r.logger.Warn("decision shadow: record failed", zap.String("site", req.Site), zap.Int("rows", len(records)), zap.Error(err))
		}
	}()
}

// shadowRerank evaluates the decider's relevance judgment for every candidate
// in the background and records it against the candidates the LLM kept.
func (r *Registry) shadowRerank(ctx context.Context, query string, candidates, kept []*loomv1.ToolSearchResult) {
	router, recorder := r.decisionParts()
	if router == nil || len(candidates) == 0 {
		return
	}
	req, err := rerankRequest(query, candidates)
	if err != nil {
		r.logger.Debug("decision shadow: tool_search rerank request", zap.Error(err))
		return
	}
	refs := sites.RerankReferenceSubjects(len(candidates), keptIndexes(candidates, kept), sites.ReferenceSourceLLMRerank, toolSubjects(candidates))
	sessionID := decision.SessionIDFromContext(ctx)
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	r.decisionWG.Add(1)
	go func() {
		defer r.decisionWG.Done()
		shadowCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		out := router.Decide(shadowCtx, req)
		provider := ""
		if router.Decider() != nil {
			provider = router.Decider().Name()
		}
		records := decision.BuildShadowRecords(req, out, provider, sessionID, refs)
		if err := recorder.Record(shadowCtx, records); err != nil {
			r.logger.Warn("decision shadow: record failed", zap.String("site", req.Site), zap.Int("rows", len(records)), zap.Error(err))
		}
	}()
}

// liveRerank is the live path for tool_search's rerank. With a live band it
// asks the decider first; when the band says to act it returns the kept
// candidates ordered by relevance probability, each carrying a "decision"
// signal, and the LLM rerank is skipped. When the decider does not clear the
// band, acted is false and the request and outcome are returned so the
// caller can record them against the LLM's answer.
func (r *Registry) liveRerank(ctx context.Context, query string, candidates []*loomv1.ToolSearchResult) (results []*loomv1.ToolSearchResult, acted bool, req *loomv1.DecisionRequest, out decision.Outcome) {
	router, _ := r.decisionParts()
	if router == nil || len(candidates) == 0 || router.Band(sites.SiteToolSearchRerank).Shadow {
		return nil, false, nil, decision.Outcome{}
	}
	req, err := rerankRequest(query, candidates)
	if err != nil {
		r.logger.Debug("decision: tool_search rerank request", zap.Error(err))
		return nil, false, nil, decision.Outcome{}
	}
	liveCtx, cancel := context.WithTimeout(ctx, liveDecideTimeout)
	defer cancel()
	out = router.Decide(liveCtx, req)
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
	r.logger.Debug("decision: tool_search rerank acted",
		zap.Int("candidates", len(candidates)), zap.Int("kept", len(results)),
		zap.Int("uncertain_kept", uncertain), zap.Duration("latency", out.Latency))
	return results, true, req, out
}
