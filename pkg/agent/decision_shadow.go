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
		client, err := jev.New(jcfg)
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
		records := decision.BuildShadowRecords(req, out, a.deciderName(), sessionID, refs)
		if err := a.decisionRecorder.Record(shadowCtx, records); err != nil {
			zap.L().Debug("decision shadow: record failed", zap.String("site", req.Site), zap.Error(err))
		}
	}()
}

// shadowRerank compares the decider's per-candidate relevance against the
// memories the existing rerank kept.
func (a *Agent) shadowRerank(ctx context.Context, sessionID, site, userMessage string, candidates, kept []*memory.Memory, source string) {
	if a.decisionRouter == nil || len(candidates) == 0 {
		return
	}
	texts := make([]string, 0, len(candidates))
	for _, m := range candidates {
		texts = append(texts, m.Content)
	}
	req, err := sites.RerankRequest(site, userMessage, texts)
	if err != nil {
		zap.L().Debug("decision shadow: rerank request", zap.String("site", site), zap.Error(err))
		return
	}
	keptSet := make(map[*memory.Memory]struct{}, len(kept))
	for _, m := range kept {
		keptSet[m] = struct{}{}
	}
	keptIdx := make([]int, 0, len(kept))
	for i, m := range candidates {
		if _, ok := keptSet[m]; ok {
			keptIdx = append(keptIdx, i)
		}
	}
	a.runShadow(ctx, sessionID, req, sites.RerankReference(len(candidates), keptIdx, source))
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
