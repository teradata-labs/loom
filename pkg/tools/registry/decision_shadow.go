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
// from the SearchTool that made the call. Nil means no decision layer.
type searchDecision struct {
	router   *decision.Router
	recorder *decision.ShadowRecorder
	track    func(fn func())
}

// shadowRerank evaluates the decider's relevance judgment for every candidate
// in the background and records it against the candidates the LLM kept.
func (d *searchDecision) shadowRerank(ctx context.Context, logger *zap.Logger, query string, candidates, kept []*loomv1.ToolSearchResult) {
	if d == nil || d.router == nil || len(candidates) == 0 {
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
		logger.Debug("decision shadow: tool_search rerank request", zap.Error(err))
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
	if d.router.Decider() != nil {
		provider = d.router.Decider().Name()
	}
	sessionID := decision.SessionIDFromContext(ctx)
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	router, recorder := d.router, d.recorder
	d.track(func() {
		shadowCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		out := router.Decide(shadowCtx, req)
		records := decision.BuildShadowRecords(req, out, provider, sessionID, refs)
		if err := recorder.Record(shadowCtx, records); err != nil {
			logger.Debug("decision shadow: record failed", zap.String("site", req.Site), zap.Error(err))
		}
	})
}
