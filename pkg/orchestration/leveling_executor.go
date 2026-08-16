// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/observability"
)

// LevelingJudge is an optional LLM-backed quality signal. It is invoked only
// when no free (non-LLM) failure signal is available. costUSD is the judge's
// own spend and counts against LevelingPolicy.MaxCostUSD.
type LevelingJudge func(ctx context.Context, prompt, output string) (pass bool, reason string, costUSD float64, err error)

// TierPolicy holds per-tier leveling knobs.
type TierPolicy struct {
	// RetryBudget is the same-model retry count applied when the caller's
	// OutputPolicy carries no RetryPolicy of its own.
	RetryBudget int
	// AggressiveCoercion attempts free JSON extraction (extractJSONFromText)
	// before declaring a schema failure.
	AggressiveCoercion bool
	// ScaffoldingDepth is reserved for the capability-leveling pattern domain
	// (C2, not yet implemented). It is not consumed by this executor.
	ScaffoldingDepth int
}

// DefaultTierPolicies returns the built-in per-tier knobs. Callers get a fresh
// map each time and may mutate it.
//
// Weaker tiers get retries plus free coercion because their failures are
// usually formatting noise. Stronger tiers get neither: a frontier model that
// fails a schema is unlikely to be rescued by asking again, so paying for a
// retry is waste.
func DefaultTierPolicies() map[catalog.ModelTier]TierPolicy {
	return map[catalog.ModelTier]TierPolicy{
		catalog.TierLocal:     {RetryBudget: 2, AggressiveCoercion: true},
		catalog.TierSmallOpen: {RetryBudget: 2, AggressiveCoercion: true},
		catalog.TierMid:       {RetryBudget: 1, AggressiveCoercion: false},
		catalog.TierFrontier:  {RetryBudget: 0, AggressiveCoercion: false},
		catalog.TierUnknown:   {RetryBudget: 0, AggressiveCoercion: false},
	}
}

// LevelingPolicy configures capability leveling. The zero value is disabled;
// a nil *LevelingPolicy is also disabled. When disabled, Execute delegates
// directly to OutputValidator.ValidateAndRetry — no catalog lookup, no judge,
// no extra LLM calls.
type LevelingPolicy struct {
	Enabled bool
	// ShortCircuitMid: when true (the DefaultLevelingPolicy default), mid-tier
	// primaries short-circuit like frontier ones.
	ShortCircuitMid bool
	// MaxEscalations caps how many ladder rungs beyond the primary may run.
	MaxEscalations int
	// MaxCostUSD is a hard ceiling summed over every leveling-visible LLM
	// call (all attempts on all rungs, plus judge calls). 0 means no ceiling.
	MaxCostUSD float64
	// Judge is an optional semantic signal. It is consulted only when the
	// effective OutputPolicy carries no schema, so a configured judge costs
	// nothing on schema-bearing workflows.
	Judge LevelingJudge
	// TierPolicies overrides DefaultTierPolicies() per tier; a tier absent from
	// this map falls back to its built-in entry. nil uses the defaults.
	TierPolicies map[catalog.ModelTier]TierPolicy
	// Thresholds shifts the pricing cutoffs used to classify the primary rung.
	// The zero value means the catalog's built-in cutoffs, so leaving this
	// field alone gives the same tiers as catalog.TierOf.
	Thresholds catalog.TierThresholds
}

// DefaultLevelingPolicy returns the default policy: disabled, mid-tier
// short-circuiting on, one escalation rung allowed once enabled.
func DefaultLevelingPolicy() *LevelingPolicy {
	return &LevelingPolicy{
		Enabled:         false,
		ShortCircuitMid: true,
		MaxEscalations:  1,
	}
}

// LevelingRung is one step of the escalation ladder. Provider/Model identify
// the rung's model in the catalog for tier resolution.
type LevelingRung struct {
	Provider string
	Model    string
	Execute  ExecuteFunc
	Feedback FeedbackFunc // optional, forwarded to the validator
}

// LevelingReport records what the executor actually did and cost.
type LevelingReport struct {
	Tier            catalog.ModelTier
	ShortCircuited  bool
	Escalations     int
	JudgeCalls      int
	CoercionApplied bool
	Passed          bool
	BudgetExhausted bool
	TotalCostUSD    float64
	Warnings        []string
}

// LevelingExecutor runs an agent through a capability ladder: a primary model,
// then optional stronger rungs, escalating only when a failure signal says the
// primary's output is unusable.
type LevelingExecutor struct {
	validator *OutputValidator
	policy    *LevelingPolicy
	tracer    observability.Tracer
	logger    *zap.Logger
}

// NewLevelingExecutor creates a leveling executor. A nil validator is
// constructed internally; a nil tracer becomes a no-op tracer; a nil logger
// becomes a no-op logger. A nil policy leaves leveling disabled.
func NewLevelingExecutor(
	validator *OutputValidator,
	policy *LevelingPolicy,
	tracer observability.Tracer,
	logger *zap.Logger,
) *LevelingExecutor {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if validator == nil {
		validator = NewOutputValidator(tracer, logger)
	}
	return &LevelingExecutor{
		validator: validator,
		policy:    policy,
		tracer:    tracer,
		logger:    logger,
	}
}

// levelingVerdict is the outcome of checking one attempt's output.
type levelingVerdict struct {
	pass   bool
	reason string
	// signal is false when neither a schema nor a judge could produce a
	// verdict — there is nothing to escalate on.
	signal bool
	// budgetBlocked is true when a judge call was skipped by the cost ceiling.
	budgetBlocked bool
}

// Execute runs the ladder under the leveling policy and returns the chosen
// result plus a report of what it took. A nil report means leveling was
// disabled and this call behaved exactly like OutputValidator.ValidateAndRetry.
//
// On exhaustion (rungs or budget) the best result obtained is returned with
// Passed=false and Warnings populated — exhaustion is not an error.
func (e *LevelingExecutor) Execute(
	ctx context.Context,
	outputPolicy *loomv1.OutputPolicy,
	ladder []LevelingRung,
	prompt string,
	workflowID string,
) (*loomv1.AgentResult, *LevelingReport, error) {
	if len(ladder) == 0 {
		return nil, nil, fmt.Errorf("leveling: ladder is empty, need at least a primary rung")
	}

	// Disabled path. No span, no catalog lookup, no wrapping closures, no
	// judge — one call into the validator and nothing else. With leveling off
	// this is byte-for-byte the behavior of not having this executor at all.
	if e.policy == nil || !e.policy.Enabled {
		result, _, err := e.validator.ValidateAndRetry(
			ctx, outputPolicy, ladder[0].Execute, ladder[0].Feedback, prompt, workflowID)
		return result, nil, err
	}

	ctx, span := e.tracer.StartSpan(ctx, "leveling.execute")
	report := &LevelingReport{}
	defer func() {
		if span != nil {
			span.SetAttribute("leveling.tier", report.Tier.String())
			span.SetAttribute("leveling.short_circuited", fmt.Sprintf("%t", report.ShortCircuited))
			span.SetAttribute("leveling.escalations", fmt.Sprintf("%d", report.Escalations))
			span.SetAttribute("leveling.judge_calls", fmt.Sprintf("%d", report.JudgeCalls))
			span.SetAttribute("leveling.passed", fmt.Sprintf("%t", report.Passed))
			span.SetAttribute("leveling.budget_exhausted", fmt.Sprintf("%t", report.BudgetExhausted))
			span.SetAttribute("leveling.total_cost_usd", fmt.Sprintf("%.6f", report.TotalCostUSD))
		}
		e.tracer.EndSpan(span)
	}()

	// One memoized catalog lookup, using the policy's thresholds (a zero-value
	// TierThresholds resolves to the catalog's built-in cutoffs). This is the
	// entire cost of the enabled-but-short-circuiting path.
	report.Tier = catalog.TierOfWith(ladder[0].Provider, ladder[0].Model, e.policy.Thresholds)

	// Short-circuit: the primary is already strong enough that leveling has
	// nothing to add, or it is unclassified and we refuse to guess. Either way
	// no judge runs and no rung beyond the primary is touched.
	if e.shouldShortCircuit(report.Tier) {
		result, warnings, err := e.validator.ValidateAndRetry(
			ctx, outputPolicy, ladder[0].Execute, ladder[0].Feedback, prompt, workflowID)
		report.ShortCircuited = true
		report.Warnings = append(report.Warnings, warnings...)
		report.TotalCostUSD = resultCostUSD(result)
		if err != nil {
			return result, report, err
		}
		// Free structural re-check only; never an LLM call on this path.
		report.Passed = true
		if schema := outputPolicy.GetOutputSchema(); schema != "" && result != nil {
			report.Passed = validateJSONSchema(schema, result.Output) == nil
		}
		e.logger.Debug("leveling short-circuited",
			zap.String("tier", report.Tier.String()),
			zap.Bool("passed", report.Passed))
		return result, report, nil
	}

	// Active path: local, small-open, or mid with ShortCircuitMid disabled.
	tierPolicy := e.tierPolicyFor(report.Tier)
	effPolicy := effectiveOutputPolicy(outputPolicy, tierPolicy)
	schema := effPolicy.GetOutputSchema()

	// Running cost total. The whole flow below is sequential — the validator
	// calls the wrapped funcs inline — so a plain float64 needs no locking.
	var total float64
	addCost := func(r *loomv1.AgentResult) { total += resultCostUSD(r) }

	wrappedExecute := func(ctx context.Context, sessionID, p string) (*loomv1.AgentResult, error) {
		r, err := ladder[0].Execute(ctx, sessionID, p)
		addCost(r)
		return r, err
	}
	var wrappedFeedback FeedbackFunc
	if ladder[0].Feedback != nil {
		wrappedFeedback = func(ctx context.Context, sessionID, fb string) (*loomv1.AgentResult, error) {
			r, err := ladder[0].Feedback(ctx, sessionID, fb)
			addCost(r)
			return r, err
		}
	}

	result, warnings, err := e.validator.ValidateAndRetry(
		ctx, effPolicy, wrappedExecute, wrappedFeedback, prompt, workflowID)
	report.Warnings = append(report.Warnings, warnings...)
	if err != nil {
		report.TotalCostUSD = total
		return result, report, err
	}

	verdict := e.evaluate(ctx, schema, tierPolicy, result, prompt, report, &total)
	if verdict.pass {
		// Includes the no-signal case: nothing told us the output is bad, so we
		// spend nothing proving otherwise.
		report.Passed = true
		report.TotalCostUSD = total
		return result, report, nil
	}

	// Escalation ladder. Bounded by MaxEscalations and by MaxCostUSD, whichever
	// bites first.
	lastResult := result
	reason := verdict.reason
	for i := 1; i < len(ladder); i++ {
		if report.Escalations >= e.policy.MaxEscalations {
			break
		}
		if e.budgetExceeded(total) {
			report.BudgetExhausted = true
			e.logger.Debug("leveling escalation blocked by cost ceiling",
				zap.Float64("total_cost_usd", total),
				zap.Float64("max_cost_usd", e.policy.MaxCostUSD),
				zap.Int("rung", i))
			break
		}

		lastOutput := ""
		if lastResult != nil {
			lastOutput = lastResult.Output
		}
		escPrompt := buildRetryPromptWithOutput(
			prompt, reason, lastOutput, effPolicy.GetRetryPolicy(), i, e.policy.MaxEscalations)
		sessionID := fmt.Sprintf("%s-lvl%d", workflowID, i)

		// An attempted rung counts against MaxEscalations even if it errors —
		// the call was made and may already have cost money.
		report.Escalations++
		e.logger.Debug("leveling escalating",
			zap.Int("rung", i),
			zap.String("rung_provider", ladder[i].Provider),
			zap.String("rung_model", ladder[i].Model),
			zap.String("reason", reason))

		escalated, execErr := ladder[i].Execute(ctx, sessionID, escPrompt)
		total += resultCostUSD(escalated)
		if execErr != nil {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("escalation rung %d (%s/%s) failed: %v", i, ladder[i].Provider, ladder[i].Model, execErr))
			continue
		}
		lastResult = escalated

		v := e.evaluate(ctx, schema, tierPolicy, escalated, prompt, report, &total)
		if v.pass {
			report.Passed = true
			report.TotalCostUSD = total
			return escalated, report, nil
		}
		reason = v.reason
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("escalation rung %d output rejected: %s", i, v.reason))
	}

	// Return best + flag. The caller decides whether an unvalidated output is
	// usable; exhaustion is not an error here.
	report.Passed = false
	report.TotalCostUSD = total
	if reason != "" {
		report.Warnings = append(report.Warnings, fmt.Sprintf("leveling exhausted: %s", reason))
	}
	return lastResult, report, nil
}

// shouldShortCircuit reports whether the primary tier makes leveling pointless.
func (e *LevelingExecutor) shouldShortCircuit(tier catalog.ModelTier) bool {
	switch tier {
	case catalog.TierFrontier:
		return true
	case catalog.TierMid:
		return e.policy.ShortCircuitMid
	case catalog.TierUnknown:
		// Conservative: never add cost for models we cannot classify.
		return true
	default:
		return false
	}
}

// tierPolicyFor resolves the knobs for a tier, preferring the caller's
// overrides and falling back to the built-in entry for that tier.
func (e *LevelingExecutor) tierPolicyFor(tier catalog.ModelTier) TierPolicy {
	if tp, ok := e.policy.TierPolicies[tier]; ok {
		return tp
	}
	return DefaultTierPolicies()[tier]
}

// budgetExceeded reports whether the cost ceiling has been reached.
func (e *LevelingExecutor) budgetExceeded(total float64) bool {
	return e.policy.MaxCostUSD > 0 && total >= e.policy.MaxCostUSD
}

// evaluate decides whether one attempt's output is acceptable, preferring free
// signals. Order matters for latency and cost:
//
//  1. A schema is structural and free — if one is present it owns the verdict
//     and the judge is never consulted.
//  2. Free JSON extraction is tried before declaring a schema failure, so a
//     fenced-but-valid payload never triggers a paid escalation.
//  3. Only with no schema at all does the judge run, and only inside budget.
//  4. With neither schema nor judge there is no signal, so the output stands.
//
// On successful coercion the result's Output is rewritten in place: callers get
// the extracted JSON rather than the original mixed text.
func (e *LevelingExecutor) evaluate(
	ctx context.Context,
	schema string,
	tierPolicy TierPolicy,
	result *loomv1.AgentResult,
	prompt string,
	report *LevelingReport,
	total *float64,
) levelingVerdict {
	if result == nil {
		return levelingVerdict{pass: false, reason: "no result produced", signal: true}
	}

	if schema != "" {
		schemaErr := validateJSONSchema(schema, result.Output)
		if schemaErr == nil {
			return levelingVerdict{pass: true, signal: true}
		}
		if tierPolicy.AggressiveCoercion {
			if extracted := extractJSONFromText(result.Output); extracted != "" {
				if validateJSONSchema(schema, extracted) == nil {
					result.Output = extracted
					report.CoercionApplied = true
					e.logger.Debug("leveling coerced output to schema-valid JSON without escalating")
					return levelingVerdict{pass: true, signal: true}
				}
			}
		}
		return levelingVerdict{pass: false, reason: schemaErr.Error(), signal: true}
	}

	if e.policy.Judge != nil {
		if e.budgetExceeded(*total) {
			// Skipping the judge leaves us without a verdict. Fail open rather
			// than reject an output we never actually examined.
			report.BudgetExhausted = true
			e.logger.Debug("leveling judge skipped by cost ceiling",
				zap.Float64("total_cost_usd", *total),
				zap.Float64("max_cost_usd", e.policy.MaxCostUSD))
			return levelingVerdict{pass: true, signal: true, budgetBlocked: true}
		}
		pass, reason, costUSD, judgeErr := e.policy.Judge(ctx, prompt, result.Output)
		report.JudgeCalls++
		*total += costUSD
		if judgeErr != nil {
			// Fail open: a broken judge must not degrade a result.
			report.Warnings = append(report.Warnings, fmt.Sprintf("judge error: %v", judgeErr))
			e.logger.Debug("leveling judge errored, treating output as acceptable",
				zap.Error(judgeErr))
			return levelingVerdict{pass: true, signal: true}
		}
		e.logger.Debug("leveling judge verdict",
			zap.Bool("pass", pass),
			zap.Float64("judge_cost_usd", costUSD))
		return levelingVerdict{pass: pass, reason: reason, signal: true}
	}

	// No schema, no judge: no signal exists, so no work is done.
	return levelingVerdict{pass: true, signal: false}
}

// effectiveOutputPolicy applies the tier's retry budget to the caller's policy
// without mutating it. A caller-supplied RetryPolicy always wins.
func effectiveOutputPolicy(outputPolicy *loomv1.OutputPolicy, tierPolicy TierPolicy) *loomv1.OutputPolicy {
	if outputPolicy == nil {
		// nil policy means the validator executes once with no validation.
		return nil
	}
	if outputPolicy.RetryPolicy != nil || tierPolicy.RetryBudget <= 0 {
		return outputPolicy
	}
	cloned, ok := proto.Clone(outputPolicy).(*loomv1.OutputPolicy)
	if !ok {
		return outputPolicy
	}
	cloned.RetryPolicy = &loomv1.OutputRetryPolicy{MaxRetries: int32(tierPolicy.RetryBudget)}
	return cloned
}

// resultCostUSD reads an agent result's spend, tolerating nil results and nil
// cost blocks.
func resultCostUSD(r *loomv1.AgentResult) float64 {
	return r.GetCost().GetCostUsd()
}
