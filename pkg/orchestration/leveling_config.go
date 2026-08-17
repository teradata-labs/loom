// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/types"
)

// validLevelingTierNames lists the tier keys accepted in
// LevelingPolicy.tier_policies, in ascending capability order. It mirrors
// catalog.ModelTier.String() and is used only to build error messages.
var validLevelingTierNames = []string{"unknown", "local", "small-open", "mid", "frontier"}

// Metadata keys set on a result produced by an escalation rung, so callers can
// see which model actually produced the output they received.
const (
	levelingRungProviderKey = "leveling_rung_provider"
	levelingRungModelKey    = "leveling_rung_model"
)

// LevelingPolicyFromProto converts a proto LevelingPolicy into the Go policy
// consumed by LevelingExecutor.
//
// A nil policy returns (nil, nil): leveling is off. A policy with enabled=false
// returns a disabled Go policy without validating any other field, so stored
// configs that were never enabled can never fail conversion.
//
// Proto3 optional fields carry the executor's defaults when absent:
// short_circuit_mid defaults to true and max_escalations to 1, matching
// DefaultLevelingPolicy. Negative max_escalations, max_cost_usd or retry_budget
// are rejected rather than clamped — a negative bound is a config mistake, not
// an intent.
//
// The returned policy's Judge is always nil. The proto surface deliberately
// carries no judge reference: the free structural signal (a JSON Schema on the
// stage/task OutputPolicy) owns the verdict, so a schema-passing low-tier
// output never triggers a paid judge call. A judge can only be supplied in Go
// by a caller that constructs LevelingPolicy directly.
//
// The proto ladder is not converted here — rungs need an agent to resolve their
// LLMs. See resolveLevelingLadder.
func LevelingPolicyFromProto(p *loomv1.LevelingPolicy) (*LevelingPolicy, error) {
	if p == nil {
		return nil, nil
	}
	if !p.GetEnabled() {
		return &LevelingPolicy{Enabled: false}, nil
	}

	policy := &LevelingPolicy{
		Enabled:         true,
		ShortCircuitMid: true,
		MaxEscalations:  1,
		Thresholds: catalog.TierThresholds{
			FrontierMinOutputCostUSD: p.GetFrontierMinOutputCostUsd(),
			MidMinOutputCostUSD:      p.GetMidMinOutputCostUsd(),
		},
	}

	if p.ShortCircuitMid != nil {
		policy.ShortCircuitMid = p.GetShortCircuitMid()
	}
	if p.MaxEscalations != nil {
		if p.GetMaxEscalations() < 0 {
			return nil, fmt.Errorf("leveling policy: max_escalations must be >= 0, got %d", p.GetMaxEscalations())
		}
		policy.MaxEscalations = int(p.GetMaxEscalations())
	}
	if p.GetMaxCostUsd() < 0 {
		return nil, fmt.Errorf("leveling policy: max_cost_usd must be >= 0, got %f", p.GetMaxCostUsd())
	}
	policy.MaxCostUSD = p.GetMaxCostUsd()

	if len(p.GetTierPolicies()) > 0 {
		policy.TierPolicies = make(map[catalog.ModelTier]TierPolicy, len(p.GetTierPolicies()))
		for name, tp := range p.GetTierPolicies() {
			tier, ok := catalog.ParseModelTier(name)
			if !ok {
				return nil, fmt.Errorf("leveling policy: unknown tier name %q in tier_policies (valid names: %s)",
					name, strings.Join(validLevelingTierNames, ", "))
			}
			if tp.GetRetryBudget() < 0 {
				return nil, fmt.Errorf("leveling policy: tier %q retry_budget must be >= 0, got %d", name, tp.GetRetryBudget())
			}
			policy.TierPolicies[tier] = TierPolicy{
				RetryBudget:        int(tp.GetRetryBudget()),
				AggressiveCoercion: tp.GetAggressiveCoercion(),
			}
		}
	}

	return policy, nil
}

// validateLevelingLadderShape checks the part of a proto ladder that can be
// judged without an agent: every rung must exist and must name either a role or
// a provider. It exists so a config surface (the workflow YAML loader) can
// reject a malformed ladder at load time instead of at execution time.
//
// resolveLevelingLadder re-checks both conditions because it must — it resolves
// rungs supplied by callers that never went through a config loader — and its
// wording is kept identical to these messages.
func validateLevelingLadderShape(protoRungs []*loomv1.LevelingRung) error {
	for i, pr := range protoRungs {
		switch {
		case pr == nil:
			return fmt.Errorf("leveling ladder: rung %d is nil", i+1)
		case pr.GetRole() != loomv1.LLMRole_LLM_ROLE_UNSPECIFIED, pr.GetProvider() != "":
		default:
			return fmt.Errorf("leveling ladder: rung %d needs role or provider", i+1)
		}
	}
	return nil
}

// resolveLevelingLadder builds the executor's ladder: the caller's primary rung
// followed by one rung per proto rung, each bound to an LLM already configured
// on the executing agent.
//
// primary is rung 0 as the caller built it (its Execute/Feedback run the agent's
// own conversation, and its Provider/Model come from the agent's main LLM).
// agentID labels the AgentResult that an escalation rung produces so workflow
// cost aggregation attributes the spend to the same agent as the primary.
//
// Each proto rung resolves through exactly one of two lookups that already
// exist on the agent — no provider is constructed and no routing is added:
//
//   - role set: agent.GetLLMForRole(role)
//   - otherwise provider set: agent.GetProviderPool()[provider]
//
// A rung with neither is a config error, as is a provider name absent from the
// pool. Provider/Model on the returned rung prefer the explicit proto fields
// (they are what the catalog is keyed on) and fall back to what the resolved
// LLM reports.
func resolveLevelingLadder(
	ag *agent.Agent,
	agentID string,
	primary LevelingRung,
	protoRungs []*loomv1.LevelingRung,
) ([]LevelingRung, error) {
	if ag == nil {
		return nil, fmt.Errorf("leveling ladder: agent is required to resolve rungs")
	}

	ladder := make([]LevelingRung, 0, len(protoRungs)+1)
	ladder = append(ladder, primary)

	for i, pr := range protoRungs {
		if pr == nil {
			return nil, fmt.Errorf("leveling ladder: rung %d is nil", i+1)
		}

		var llm agent.LLMProvider
		switch {
		case pr.GetRole() != loomv1.LLMRole_LLM_ROLE_UNSPECIFIED:
			llm = ag.GetLLMForRole(pr.GetRole())
			if llm == nil {
				return nil, fmt.Errorf("leveling ladder: rung %d role %s has no LLM configured on agent %q",
					i+1, pr.GetRole(), agentID)
			}
		case pr.GetProvider() != "":
			pool := ag.GetProviderPool()
			if pool == nil {
				return nil, fmt.Errorf("leveling ladder: rung %d names provider %q but agent %q has no provider pool; a provider-based rung requires the provider to be present in the agent's provider pool",
					i+1, pr.GetProvider(), agentID)
			}
			var ok bool
			llm, ok = pool[pr.GetProvider()]
			if !ok || llm == nil {
				return nil, fmt.Errorf("leveling ladder: rung %d provider %q is not in agent %q's provider pool; a provider-based rung requires the provider to be present in the agent's provider pool",
					i+1, pr.GetProvider(), agentID)
			}
		default:
			return nil, fmt.Errorf("leveling ladder: rung %d needs role or provider", i+1)
		}

		provider := pr.GetProvider()
		if provider == "" {
			provider = llm.Name()
		}
		model := pr.GetModel()
		if model == "" {
			model = llm.Model()
		}

		ladder = append(ladder, LevelingRung{
			Provider: provider,
			Model:    model,
			Execute:  levelingRungExecute(llm, agentID, provider, model),
			// Feedback is intentionally nil: an escalation rung is a one-shot
			// call with no session to continue, and the validator falls back to
			// a fresh execute when no feedback function is supplied.
			Feedback: nil,
		})
	}

	return ladder, nil
}

// levelingRungExecute returns an ExecuteFunc that sends one prompt straight to
// the rung's LLM — no tools, no session, and no mutation of the agent, matching
// how PipelineExecutor.validateStageOutput uses the merge LLM directly. The
// sessionID the validator supplies is unused because a bare LLM call has no
// conversation to join.
//
// Cost comes from the provider's own Usage.CostUSD on the raw LLMResponse (every
// client fills it from catalog pricing), so escalation spend counts against
// LevelingPolicy.MaxCostUSD without this package pricing anything itself.
func levelingRungExecute(llm agent.LLMProvider, agentID, provider, model string) ExecuteFunc {
	return func(ctx context.Context, _ string, prompt string) (*loomv1.AgentResult, error) {
		start := time.Now()
		resp, err := llm.Chat(ctx, []types.Message{{
			Role:      "user",
			Content:   prompt,
			Timestamp: start,
		}}, nil)
		if err != nil {
			return nil, fmt.Errorf("leveling rung %s/%s failed: %w", provider, model, err)
		}
		return &loomv1.AgentResult{
			AgentId: agentID,
			Output:  resp.Content,
			Metadata: map[string]string{
				levelingRungProviderKey: provider,
				levelingRungModelKey:    model,
			},
			ConfidenceScore: 1.0,
			DurationMs:      time.Since(start).Milliseconds(),
			Cost: &loomv1.AgentExecutionCost{
				TotalTokens:  types.SafeInt32(resp.Usage.TotalTokens),
				InputTokens:  types.SafeInt32(resp.Usage.InputTokens),
				OutputTokens: types.SafeInt32(resp.Usage.OutputTokens),
				CostUsd:      resp.Usage.CostUSD,
			},
		}, nil
	}
}

// backfillLevelingResultMetadata copies an executor's standard result metadata
// onto a leveling result, without overwriting keys the result already carries.
// A result produced by an escalation rung only has the rung's own keys, so this
// keeps downstream consumers of the executor's keys working regardless of which
// rung won. It is a no-op for a primary-rung result, which already has them.
func backfillLevelingResultMetadata(result *loomv1.AgentResult, base map[string]string) {
	if result == nil || len(base) == 0 {
		return
	}
	if result.Metadata == nil {
		result.Metadata = make(map[string]string, len(base))
	}
	for k, v := range base {
		if _, ok := result.Metadata[k]; !ok {
			result.Metadata[k] = v
		}
	}
}

// effectiveLevelingOutputPolicy returns the validation contract leveling should
// enforce for a pipeline stage. The unified output_policy wins; otherwise the
// legacy output_schema/retry_policy pair is synthesized into one so enabling
// leveling on a stage written against the legacy fields still validates against
// them. nil means the stage has no contract, and leveling then has no free
// signal to escalate on.
func effectiveLevelingOutputPolicy(stage *loomv1.PipelineStage) *loomv1.OutputPolicy {
	if stage.GetOutputPolicy() != nil {
		return stage.GetOutputPolicy()
	}
	if stage.GetOutputSchema() != "" || stage.GetRetryPolicy() != nil {
		return &loomv1.OutputPolicy{
			OutputSchema: stage.GetOutputSchema(),
			RetryPolicy:  stage.GetRetryPolicy(),
		}
	}
	return nil
}
