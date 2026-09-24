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

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/decision/jev"
	decisionllm "github.com/teradata-labs/loom/pkg/decision/llm"
	decisionmock "github.com/teradata-labs/loom/pkg/decision/mock"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Decision-layer wiring for the agent.
//
// Phase 1 of docs/plans/jev-decision-layer-plan.md: the agent builds a
// decision.Router from its DecisionConfig and, at each wired call site, runs
// the decider in the background alongside the existing mechanism and records
// both answers as shadow rows. Nothing here changes what the agent does; a
// site only branches on a decider answer once its band says so, and no site
// has a live band yet.

// shadowTimeout bounds one background shadow evaluation. It is generous
// because a shadow is off the hot path; it exists so a hung decider cannot
// leak goroutines.
const shadowTimeout = 30 * time.Second

// Decision-layer provider names accepted in DecisionConfig.provider.
const (
	DecisionProviderOff  = "off"
	DecisionProviderLLM  = "llm"
	DecisionProviderMock = "mock"
	DecisionProviderJev  = "jev"
)

// WithDecisionConfig wires the decision layer from configuration. A nil
// config or provider "off" leaves the layer disabled. The shadow store may be
// nil, in which case shadow comparisons are evaluated (and traced) but not
// persisted.
func WithDecisionConfig(cfg *loomv1.DecisionConfig, store decision.ShadowStore) Option {
	return func(a *Agent) {
		a.decisionCfg = cfg
		a.decisionShadowStore = store
	}
}

// WithDecisionRouter installs a prebuilt router, bypassing WithDecisionConfig.
// Tests use it to inject a mock decider; production wiring goes through
// configuration.
func WithDecisionRouter(r *decision.Router) Option {
	return func(a *Agent) { a.decisionRouter = r }
}

// WithDecisionShadowStore sets the shadow store when the router is provided
// directly with WithDecisionRouter.
func WithDecisionShadowStore(store decision.ShadowStore) Option {
	return func(a *Agent) { a.decisionShadowStore = store }
}

// DecisionRouter returns the agent's router, or nil when the layer is off.
func (a *Agent) DecisionRouter() *decision.Router { return a.decisionRouter }

// WaitDecisionShadows blocks until every in-flight background shadow
// evaluation has finished. Tests call it before asserting on the store.
func (a *Agent) WaitDecisionShadows() { a.decisionWG.Wait() }

// initDecisionRouter runs once after options are applied. It resolves the
// configured provider against the agent's own LLMs, wraps it in
// Instrumented, and builds the Router with the configured bands and budget.
// Misconfiguration disables the layer with a logged warning rather than
// failing agent construction: the decision layer is auxiliary.
func (a *Agent) initDecisionRouter() {
	if a.decisionRecorder == nil {
		a.decisionRecorder = decision.NewShadowRecorder(a.decisionShadowStore, a.tracer)
	}
	if a.decisionRouter != nil {
		return // injected
	}
	cfg := a.decisionCfg
	if cfg == nil || cfg.Provider == "" || cfg.Provider == DecisionProviderOff {
		return
	}

	var decider decision.Decider
	switch cfg.Provider {
	case DecisionProviderLLM:
		provider := a.roleLLMForDecision(cfg.LlmRole)
		if provider == nil {
			zap.L().Warn("decision layer: no LLM available for llm provider; layer disabled",
				zap.String("llm_role", cfg.LlmRole))
			return
		}
		opts := []decisionllm.Option{}
		if a.prompts != nil {
			opts = append(opts, decisionllm.WithPromptRegistry(a.prompts))
		}
		if cfg.TimeoutMs > 0 {
			opts = append(opts, decisionllm.WithTimeout(time.Duration(cfg.TimeoutMs)*time.Millisecond))
		}
		decider = decisionllm.New(provider, opts...)
	case DecisionProviderMock:
		decider = decisionmock.New()
	case DecisionProviderJev:
		// Credentials come from the environment only (TYPESAFE_API_KEY or a
		// Vercel AI Gateway key), never from agent YAML.
		jcfg, err := jev.FromDecisionConfig(cfg)
		if err != nil {
			zap.L().Warn("decision layer: provider \"jev\" configured but unusable; layer disabled", zap.Error(err))
			return
		}
		// Shared per process: agents with the same decider settings draw on
		// one client and one rate budget (jev.Shared).
		client, err := jev.Shared(jcfg)
		if err != nil {
			zap.L().Warn("decision layer: jev client; layer disabled", zap.Error(err))
			return
		}
		zap.L().Info("decision layer: jev client", zap.String("url", client.URL()), zap.String("model", client.Model()))
		decider = client
	default:
		zap.L().Warn("decision layer: unknown provider; layer disabled", zap.String("provider", cfg.Provider))
		return
	}

	// Large fan-out requests (a 64-candidate rerank) are split into
	// concurrent chunks sized by the config or the decider's own hint, and a
	// chunk that still overloads the provider is bisected; see
	// decision.Chunked. Answers do not change, only round trips.
	decider = decision.Chunk(decider, decision.WithChunkSize(int(cfg.MaxQuestionsPerRequest)))

	routerOpts := []decision.RouterOption{
		decision.WithTracer(a.tracer),
		decision.WithBands(cfg.Bands),
	}
	if cfg.MaxPerSession > 0 || cfg.MaxCostUsdPerSession > 0 {
		routerOpts = append(routerOpts, decision.WithBudget(cfg.MaxPerSession, cfg.MaxCostUsdPerSession))
	}
	a.decisionRouter = decision.NewRouter(decision.NewInstrumented(decider, a.tracer), routerOpts...)
	zap.L().Info("decision layer enabled",
		zap.String("provider", decider.Name()),
		zap.String("model", decider.Model()),
		zap.Int("bands", len(cfg.Bands)),
		zap.Bool("shadow_store", a.decisionShadowStore != nil))
}

// roleLLMForDecision maps DecisionConfig.llm_role onto the agent's LLMs. An
// unset role or an unconfigured role falls back to the main LLM, mirroring
// how the role LLMs themselves fall back.
func (a *Agent) roleLLMForDecision(role string) LLMProvider {
	switch strings.ToLower(role) {
	case "classifier":
		if a.classifierLLM != nil {
			return a.classifierLLM
		}
	case "compressor":
		if a.compressorLLM != nil {
			return a.compressorLLM
		}
	case "judge":
		if a.judgeLLM != nil {
			return a.judgeLLM
		}
	case "orchestrator":
		if a.orchestratorLLM != nil {
			return a.orchestratorLLM
		}
	}
	return a.llm
}

// sessionIDFromContext recovers the session id for a shadow row. The agent
// stamps "session_id" onto tool-execution contexts (contextWithValue in
// executeToolWithSelfCorrection); decision.WithSessionID is the layer's own
// key. Either is accepted; "" when neither is present.
func sessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if s := decision.SessionIDFromContext(ctx); s != "" {
		return s
	}
	if v, ok := ctx.Value("session_id").(string); ok { //nolint:staticcheck // legacy string key set by contextWithValue
		return v
	}
	return ""
}

// deciderName is the provider label written on shadow rows.
func (a *Agent) deciderName() string {
	if a.decisionRouter == nil || a.decisionRouter.Decider() == nil {
		return ""
	}
	return a.decisionRouter.Decider().Name()
}

// liveDecidePerWave bounds one round of provider requests made on the hot
// path, ahead of the existing mechanism. A live band trades this much
// latency for skipping a generative call; when the decider is slower the
// site falls back and the turn is no worse than before. A chunked request
// costs several sequential rounds (decision.Chunked), so the budget scales
// with them rather than staying flat: decision.Budget does the arithmetic.
const liveDecidePerWave = 4 * time.Second

// maxLiveDecideBudget caps the whole live call however many rounds it needs.
// Measured on the LongMemEval A/B with chunking on (476 uncapped requests of
// 41-64 candidates): 91% finished inside 4 s and the p90 was 1.9 s, while
// the tail that missed it ran 17-30 s in the client's retry ladder. Waiting
// for that tail costs more than the generative rerank it was meant to skip.
const maxLiveDecideBudget = 12 * time.Second

// runShadow evaluates req in the background and records the outcome against
// refs. It never blocks the caller and never surfaces an error; failures
// become ERROR-path rows and metrics.
func (a *Agent) runShadow(ctx context.Context, sessionID string, req *loomv1.DecisionRequest, refs map[string]decision.Reference) {
	if a.decisionRouter == nil || req == nil {
		return
	}
	// Detach from the caller's cancellation: the turn that produced this
	// comparison may end before the shadow finishes, and that is fine.
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	a.decisionWG.Add(1)
	go func() {
		defer a.decisionWG.Done()
		shadowCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		out := a.decisionRouter.Decide(shadowCtx, req)
		if out.Path == loomv1.DecisionPath_DECISION_PATH_ERROR {
			// Warn, not Debug: an ERROR row without a log line was the only
			// trace the first campaign left, and it was not enough to triage.
			zap.L().Warn("decision shadow: decider error",
				zap.String("site", req.Site), zap.Int("questions", len(req.Questions)),
				zap.Duration("latency", out.Latency), zap.Error(out.Err))
		}
		a.recordDecision(shadowCtx, sessionID, req, out, refs)
	}()
}

// recordDecision writes shadow rows for an outcome already obtained.
func (a *Agent) recordDecision(ctx context.Context, sessionID string, req *loomv1.DecisionRequest, out decision.Outcome, refs map[string]decision.Reference) {
	records := decision.BuildShadowRecords(req, out, a.deciderName(), sessionID, refs)
	if err := a.decisionRecorder.Record(ctx, records); err != nil {
		// Warn: a store that rejects rows (a schema behind the binary, a
		// locked file) otherwise drops a whole campaign's evidence silently.
		zap.L().Warn("decision shadow: record failed", zap.String("site", req.Site), zap.Int("rows", len(records)), zap.Error(err))
	}
}

// RunDecisionShadow evaluates req in the background against refs. Exported
// for call sites outside this package that borrow the agent's decision layer
// (the conditional workflow executor uses the condition agent's). A nil
// router is a no-op.
func (a *Agent) RunDecisionShadow(ctx context.Context, sessionID string, req *loomv1.DecisionRequest, refs map[string]decision.Reference) {
	a.runShadow(ctx, sessionID, req, refs)
}

// LiveDecide asks the agent's router synchronously, bounded by a short
// timeout. Callers check the site's band first; a shadow band never pays
// this latency. It returns a DISABLED outcome when the layer is off.
func (a *Agent) LiveDecide(ctx context.Context, sessionID string, req *loomv1.DecisionRequest) decision.Outcome {
	if a.decisionRouter == nil || req == nil {
		return decision.Outcome{Path: loomv1.DecisionPath_DECISION_PATH_DISABLED}
	}
	return a.liveDecide(ctx, sessionID, req)
}

// RecordDecisionAsync records an outcome the caller already holds, off the
// hot path. refs may be nil when the decider's answer was acted on and there
// is nothing to compare against.
func (a *Agent) RecordDecisionAsync(ctx context.Context, sessionID string, req *loomv1.DecisionRequest, out decision.Outcome, refs map[string]decision.Reference) {
	if a.decisionRouter == nil || req == nil {
		return
	}
	a.recordDecisionAsync(ctx, sessionID, req, out, refs)
}

// recordDecisionAsync is recordDecision off the hot path.
func (a *Agent) recordDecisionAsync(ctx context.Context, sessionID string, req *loomv1.DecisionRequest, out decision.Outcome, refs map[string]decision.Reference) {
	bg := decision.WithSessionID(context.WithoutCancel(ctx), sessionID)
	a.decisionWG.Add(1)
	go func() {
		defer a.decisionWG.Done()
		recCtx, cancel := context.WithTimeout(bg, shadowTimeout)
		defer cancel()
		a.recordDecision(recCtx, sessionID, req, out, refs)
	}()
}

// liveDecide asks the router synchronously, bounded by liveDecideTimeout.
// Only called when the site's band is live; a shadow band never pays this.
func (a *Agent) liveDecide(ctx context.Context, sessionID string, req *loomv1.DecisionRequest) decision.Outcome {
	budget := decision.Budget(a.decisionRouter.Decider(), len(req.Questions), liveDecidePerWave, maxLiveDecideBudget)
	liveCtx, cancel := context.WithTimeout(decision.WithSessionID(ctx, sessionID), budget)
	defer cancel()
	return a.decisionRouter.Decide(liveCtx, req)
}

// rerankRequest builds the recall rerank request for a candidate list.
func rerankRequest(site, userMessage string, candidates []*memory.Memory) (*loomv1.DecisionRequest, error) {
	texts := make([]string, 0, len(candidates))
	for _, m := range candidates {
		texts = append(texts, m.Content)
	}
	return sites.RerankRequest(site, userMessage, texts)
}

// memorySubjects renders each candidate's provenance as a shadow-row subject:
// the session the memory was extracted from when known, else the memory id.
// Never the content. A benchmark that knows which sessions hold the evidence
// can grade every keep-or-drop decision from this alone.
func memorySubjects(candidates []*memory.Memory) []string {
	out := make([]string, len(candidates))
	for i, m := range candidates {
		switch {
		case m == nil:
		case memoryFromSession(m):
			out[i] = "session:" + m.SourceID
		default:
			out[i] = "memory:" + m.ID
		}
	}
	return out
}

// memoryFromSession reports whether a memory's SourceID names the agent
// session it was extracted from (the extractor records it for
// auto_extracted memories; a conversation source carries one too).
func memoryFromSession(m *memory.Memory) bool {
	if m.SourceID == "" {
		return false
	}
	switch m.Source {
	case memory.SourceAutoExtracted, memory.SourceAgent, "conversation":
		return true
	}
	return false
}

// keptIndexes maps the kept memories back to candidate indexes.
func keptIndexes(candidates, kept []*memory.Memory) []int {
	keptSet := make(map[*memory.Memory]struct{}, len(kept))
	for _, m := range kept {
		keptSet[m] = struct{}{}
	}
	idx := make([]int, 0, len(kept))
	for i, m := range candidates {
		if _, ok := keptSet[m]; ok {
			idx = append(idx, i)
		}
	}
	return idx
}

// shadowRerank compares the decider's per-candidate relevance against the
// memories the existing rerank kept.
func (a *Agent) shadowRerank(ctx context.Context, sessionID, site, userMessage string, candidates, kept []*memory.Memory, source string) {
	if a.decisionRouter == nil || len(candidates) == 0 {
		return
	}
	req, err := rerankRequest(site, userMessage, candidates)
	if err != nil {
		zap.L().Debug("decision shadow: rerank request", zap.String("site", site), zap.Error(err))
		return
	}
	a.runShadow(ctx, sessionID, req, sites.RerankReferenceSubjects(len(candidates), keptIndexes(candidates, kept), source, memorySubjects(candidates)))
}

// liveRerank is the live path for a rerank site: ask the decider first and,
// when its band says to act, return the kept candidates without a generative
// call. The second return is false when the caller must run the existing
// mechanism; the outcome is returned either way so the caller can record it
// against the mechanism's answer without a second decider call.
func (a *Agent) liveRerank(ctx context.Context, sessionID, site, userMessage string, candidates []*memory.Memory) (kept []*memory.Memory, acted bool, req *loomv1.DecisionRequest, out decision.Outcome) {
	if a.decisionRouter == nil || len(candidates) == 0 || a.decisionRouter.Band(site).Shadow {
		return nil, false, nil, decision.Outcome{}
	}
	req, err := rerankRequest(site, userMessage, candidates)
	if err != nil {
		zap.L().Debug("decision: rerank request", zap.String("site", site), zap.Error(err))
		return nil, false, nil, decision.Outcome{}
	}
	out = a.liveDecide(ctx, sessionID, req)
	if !out.Act() {
		return nil, false, req, out
	}
	idx, uncertain := sites.RerankKeptWithBand(out.Response, len(candidates), out.Band)
	if !sites.RerankContributed(len(candidates), uncertain) {
		// Every answer was uncertain: the generative rerank decides, and the
		// row is recorded as a fallback against it.
		out.Path = loomv1.DecisionPath_DECISION_PATH_FALLBACK
		return nil, false, req, out
	}
	kept = make([]*memory.Memory, 0, len(idx))
	for _, i := range idx {
		if i < len(candidates) {
			kept = append(kept, candidates[i])
		}
	}
	zap.L().Debug("decision: rerank acted",
		zap.String("site", site), zap.Int("candidates", len(candidates)),
		zap.Int("kept", len(kept)), zap.Int("uncertain_kept", uncertain),
		zap.Duration("latency", out.Latency))
	return kept, true, req, out
}

// shadowFailureKind compares the decider's failure classification against
// what the Success flag plus fabric.InferErrorType say about one tool result.
// State carries the error text and an input digest, never the input or the
// result payload.
func (a *Agent) shadowFailureKind(ctx context.Context, sessionID, toolName string, input map[string]interface{}, result *shuttle.Result, execErr error) {
	if a.decisionRouter == nil {
		return
	}
	success := execErr == nil && (result == nil || result.Success)
	errorCode, errorText := "", ""
	switch {
	case execErr != nil:
		errorText = execErr.Error()
	case result != nil && result.Error != nil:
		errorCode = result.Error.Code
		errorText = result.Error.Message
	case result != nil && !result.Success:
		errorText = "tool reported failure without error details"
	}
	req, err := sites.FailureKindRequest(toolName, errorCode, errorText, input)
	if err != nil {
		zap.L().Debug("decision shadow: failure request", zap.String("tool", toolName), zap.Error(err))
		return
	}
	a.runShadow(ctx, sessionID, req, sites.FailureKindReference(success, errorCode, errorText))
}

// propagateDecisionRouter hands the router to a tool registry that can use
// it (tool_search's rerank). The registry is reached through the shuttle
// interface, so this is an optional capability check, not a hard type.
func (a *Agent) propagateDecisionRouter(reg shuttle.ToolRegistry) {
	if a.decisionRouter == nil || reg == nil {
		return
	}
	if sink, ok := reg.(interface {
		SetDecisionRouter(*decision.Router, decision.ShadowStore)
	}); ok {
		sink.SetDecisionRouter(a.decisionRouter, a.decisionShadowStore)
	}
}

// decisionConfigSummary is a one-line description for logs and status.
func (a *Agent) decisionConfigSummary() string {
	if a.decisionRouter == nil {
		return "off"
	}
	return fmt.Sprintf("%s (%s)", a.deciderName(), a.decisionRouter.Decider().Model())
}
