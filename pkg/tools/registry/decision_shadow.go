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

// shadowRerank evaluates the decider's relevance judgment for every candidate
// in the background and records it against the candidates the LLM kept.
func (r *Registry) shadowRerank(ctx context.Context, query string, candidates, kept []*loomv1.ToolSearchResult) {
	r.mu.RLock()
	router, recorder := r.decisionRouter, r.decisionRecorder
	r.mu.RUnlock()
	if router == nil || len(candidates) == 0 {
		return
	}

	texts := make([]string, 0, len(candidates))
	for _, c := range candidates {
		text := ""
		if c != nil && c.Tool != nil {
			text = c.Tool.Name + ": " + c.Tool.Description
		}
		texts = append(texts, text)
	}
	req, err := sites.RerankRequest(sites.SiteToolSearchRerank, query, texts)
	if err != nil {
		r.logger.Debug("decision shadow: tool_search rerank request", zap.Error(err))
		return
	}
	keptSet := make(map[*loomv1.ToolSearchResult]struct{}, len(kept))
	for _, k := range kept {
		keptSet[k] = struct{}{}
	}
	keptIdx := make([]int, 0, len(kept))
	for i, c := range candidates {
		if _, ok := keptSet[c]; ok {
			keptIdx = append(keptIdx, i)
		}
	}
	refs := sites.RerankReference(len(candidates), keptIdx, sites.ReferenceSourceLLMRerank)

	provider := ""
	if router.Decider() != nil {
		provider = router.Decider().Name()
	}
	sessionID := decision.SessionIDFromContext(ctx)
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	r.decisionWG.Add(1)
	go func() {
		defer r.decisionWG.Done()
		shadowCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		out := router.Decide(shadowCtx, req)
		records := decision.BuildShadowRecords(req, out, provider, sessionID, refs)
		if err := recorder.Record(shadowCtx, records); err != nil {
			r.logger.Debug("decision shadow: record failed", zap.String("site", req.Site), zap.Error(err))
		}
	}()
}
