// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// lvlAnyWarningContains reports whether any report warning carries substr.
func lvlAnyWarningContains(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestLevelingShortCircuitHonorsTierPolicies pins that tier_policies reaches
// the short-circuit path. Before this, the branch handed the caller's
// OutputPolicy straight to the validator and never consulted tierPolicyFor, so
// a retry_budget configured for frontier, unknown or mid was config that did
// nothing: frontier with retry_budget=3 still made exactly one call.
//
// The tiers that short-circuit skip the ladder and the judge, not their own
// retry budget. The frontier and unknown defaults are zero, so a caller who
// leaves them alone still sees exactly one call; a caller who sets one gets the
// retries, bounded by MaxCostUSD like every other paid attempt, and always
// yielding to an explicit caller RetryPolicy.
func TestLevelingShortCircuitHonorsTierPolicies(t *testing.T) {
	t.Parallel()

	const primaryCost = 0.10
	const rungCost = 0.90

	tests := []struct {
		name            string
		provider        string
		model           string
		shortCircuitMid bool
		tierPolicies    map[catalog.ModelTier]TierPolicy
		maxCostUSD      float64
		callerRetries   int32
		explicitRetry   bool

		wantTier           catalog.ModelTier
		wantShortCircuited bool
		wantPrimaryCalls   int
		wantRungCalls      int
		wantEscalations    int
		wantExhausted      bool
		wantCeilingWarning bool
	}{
		// (a) Defaults preserve the historical behavior for the tiers whose
		// built-in budget is zero, and apply mid's built-in 1 where it is not.
		{
			name:               "frontier default budget is one call",
			provider:           lvlFrontierProvider,
			model:              lvlFrontierModel,
			wantTier:           catalog.TierFrontier,
			wantShortCircuited: true,
			wantPrimaryCalls:   1,
		},
		{
			name:               "unknown default budget is one call",
			provider:           lvlUnknownProvider,
			model:              lvlUnknownModel,
			wantTier:           catalog.TierUnknown,
			wantShortCircuited: true,
			wantPrimaryCalls:   1,
		},
		{
			name:               "mid default budget is one retry",
			provider:           lvlMidProvider,
			model:              lvlMidModel,
			shortCircuitMid:    true,
			wantTier:           catalog.TierMid,
			wantShortCircuited: true,
			wantPrimaryCalls:   2,
		},

		// (b) A configured budget is spent on the short-circuit path.
		{
			name:               "frontier retry budget is spent",
			provider:           lvlFrontierProvider,
			model:              lvlFrontierModel,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierFrontier: {RetryBudget: 3}},
			wantTier:           catalog.TierFrontier,
			wantShortCircuited: true,
			wantPrimaryCalls:   4,
		},
		{
			name:               "unknown retry budget is spent",
			provider:           lvlUnknownProvider,
			model:              lvlUnknownModel,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierUnknown: {RetryBudget: 3}},
			wantTier:           catalog.TierUnknown,
			wantShortCircuited: true,
			wantPrimaryCalls:   4,
		},
		{
			name:               "mid retry budget is spent",
			provider:           lvlMidProvider,
			model:              lvlMidModel,
			shortCircuitMid:    true,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierMid: {RetryBudget: 3}},
			wantTier:           catalog.TierMid,
			wantShortCircuited: true,
			wantPrimaryCalls:   4,
		},

		// (c) An explicit caller RetryPolicy still wins, exactly as it does on
		// the active path.
		{
			name:               "frontier caller retry policy beats the tier budget",
			provider:           lvlFrontierProvider,
			model:              lvlFrontierModel,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierFrontier: {RetryBudget: 3}},
			callerRetries:      1,
			explicitRetry:      true,
			wantTier:           catalog.TierFrontier,
			wantShortCircuited: true,
			wantPrimaryCalls:   2,
		},
		{
			name:               "unknown caller retry policy beats the tier budget",
			provider:           lvlUnknownProvider,
			model:              lvlUnknownModel,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierUnknown: {RetryBudget: 3}},
			callerRetries:      1,
			explicitRetry:      true,
			wantTier:           catalog.TierUnknown,
			wantShortCircuited: true,
			wantPrimaryCalls:   2,
		},
		{
			name:               "mid caller retry policy beats the tier budget",
			provider:           lvlMidProvider,
			model:              lvlMidModel,
			shortCircuitMid:    true,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierMid: {RetryBudget: 3}},
			callerRetries:      1,
			explicitRetry:      true,
			wantTier:           catalog.TierMid,
			wantShortCircuited: true,
			wantPrimaryCalls:   2,
		},

		// (d) The ceiling bounds a short-circuit retry budget: one attempt of
		// 0.10 against a 0.05 ceiling leaves nothing for the retry.
		{
			name:               "frontier retry budget stopped by the cost ceiling",
			provider:           lvlFrontierProvider,
			model:              lvlFrontierModel,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierFrontier: {RetryBudget: 3}},
			maxCostUSD:         0.05,
			wantTier:           catalog.TierFrontier,
			wantShortCircuited: true,
			wantPrimaryCalls:   1,
			wantExhausted:      true,
			wantCeilingWarning: true,
		},
		{
			name:               "unknown retry budget stopped by the cost ceiling",
			provider:           lvlUnknownProvider,
			model:              lvlUnknownModel,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierUnknown: {RetryBudget: 3}},
			maxCostUSD:         0.05,
			wantTier:           catalog.TierUnknown,
			wantShortCircuited: true,
			wantPrimaryCalls:   1,
			wantExhausted:      true,
			wantCeilingWarning: true,
		},
		{
			name:               "mid retry budget stopped by the cost ceiling",
			provider:           lvlMidProvider,
			model:              lvlMidModel,
			shortCircuitMid:    true,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierMid: {RetryBudget: 3}},
			maxCostUSD:         0.05,
			wantTier:           catalog.TierMid,
			wantShortCircuited: true,
			wantPrimaryCalls:   1,
			wantExhausted:      true,
			wantCeilingWarning: true,
		},

		// The control: the same override on a tier that does not short-circuit
		// keeps taking the active path, ladder and all.
		{
			name:               "local retry budget still takes the active path",
			provider:           lvlLowProvider,
			model:              lvlLowModel,
			tierPolicies:       map[catalog.ModelTier]TierPolicy{catalog.TierLocal: {RetryBudget: 3}},
			wantTier:           catalog.TierLocal,
			wantShortCircuited: false,
			wantPrimaryCalls:   4,
			wantRungCalls:      1,
			wantEscalations:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			primary := newMockRung(primaryCost, lvlInvalidJSON)
			rung := newMockRung(rungCost, lvlInvalidJSON)

			exec := newTestLevelingExecutor(t, &LevelingPolicy{
				Enabled:         true,
				ShortCircuitMid: tt.shortCircuitMid,
				MaxEscalations:  1,
				MaxCostUSD:      tt.maxCostUSD,
				TierPolicies:    tt.tierPolicies,
			})

			result, report, err := exec.Execute(
				context.Background(),
				schemaPolicy(tt.callerRetries, tt.explicitRetry),
				[]LevelingRung{
					{Provider: tt.provider, Model: tt.model, Execute: primary.execute},
					{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung.execute},
				},
				"do the thing", "wf-sc-tierpolicy")

			require.NoError(t, err)
			require.NotNil(t, report)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantTier, report.Tier)
			assert.Equal(t, tt.wantShortCircuited, report.ShortCircuited)
			assert.Equal(t, tt.wantPrimaryCalls, primary.count(), "primary attempts")
			assert.Equal(t, tt.wantRungCalls, rung.count(), "escalation rung calls")
			assert.Equal(t, tt.wantEscalations, report.Escalations)
			assert.Equal(t, tt.wantExhausted, report.BudgetExhausted)
			assert.False(t, report.Passed, "the primary never satisfies the schema")
			assert.InDelta(t,
				float64(tt.wantPrimaryCalls)*primaryCost+float64(tt.wantRungCalls)*rungCost,
				report.TotalCostUSD, 1e-9, "every attempt is priced")
			assert.Equal(t, tt.wantCeilingWarning,
				lvlAnyWarningContains(report.Warnings, "cost ceiling reached"),
				"ceiling warning: %v", report.Warnings)
		})
	}
}
