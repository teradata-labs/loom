// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// TestLevelingBudgetGatesPrimaryRetries pins the cost gate on the primary rung's
// same-model retries. Before it existed, MaxCostUSD bounded escalations and
// judge calls only, so a retry budget could spend past the ceiling before the
// first escalation was ever considered.
func TestLevelingBudgetGatesPrimaryRetries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		maxCostUSD      float64
		wantPrimary     int
		wantEscalations int
		wantRungCalls   int
		wantTotal       float64
		wantExhausted   bool
	}{
		{
			// 0.40 per attempt against a 0.50 ceiling: attempt 2 is allowed
			// (0.40 < 0.50), attempt 3 is not (0.80 >= 0.50), and neither is
			// the escalation rung.
			name:          "ceiling stops the third attempt and the rung",
			maxCostUSD:    0.50,
			wantPrimary:   2,
			wantRungCalls: 0,
			wantTotal:     0.80,
			wantExhausted: true,
		},
		{
			// The control: same ladder, no ceiling. Every retry runs and the
			// escalation follows, so the gate is what stopped the run above.
			name:            "no ceiling spends the whole retry budget",
			maxCostUSD:      0,
			wantPrimary:     3,
			wantEscalations: 1,
			wantRungCalls:   1,
			wantTotal:       3*0.40 + 0.90,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			primary := newMockRung(0.40, lvlInvalidJSON)
			rung := newMockRung(0.90, lvlValidJSON)

			exec := newTestLevelingExecutor(t, &LevelingPolicy{
				Enabled:        true,
				MaxEscalations: 1,
				MaxCostUSD:     tt.maxCostUSD,
			})

			result, report, err := exec.Execute(
				context.Background(), schemaPolicy(2, true),
				[]LevelingRung{
					{Provider: lvlLowProvider, Model: lvlLowModel, Execute: primary.execute},
					{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung.execute},
				},
				"do the thing", "wf-budget-retries")

			require.NoError(t, err)
			require.NotNil(t, report)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantPrimary, primary.count())
			assert.Equal(t, tt.wantRungCalls, rung.count())
			assert.Equal(t, tt.wantEscalations, report.Escalations)
			assert.InDelta(t, tt.wantTotal, report.TotalCostUSD, 1e-9)
			assert.Equal(t, tt.wantExhausted, report.BudgetExhausted)

			if tt.wantExhausted {
				assert.Contains(t, report.Warnings, "attempt 3 not made: cost ceiling reached before the retry")
			}
		})
	}
}

// TestLevelingFencedJSONPassesFreeOnEveryPath is the core of the coercion
// contract: a payload that is valid JSON inside a code fence satisfies a schema
// for free, on whichever path the executor takes. It used to depend on a
// per-tier knob, which meant a tier could reject a payload the pre-leveling
// pipeline accepted.
func TestLevelingFencedJSONPassesFreeOnEveryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		model    string
		policy   *LevelingPolicy
		wantTier catalog.ModelTier
		wantSC   bool
	}{
		{
			name:     "short-circuit path, frontier primary",
			provider: lvlFrontierProvider,
			model:    lvlFrontierModel,
			policy:   &LevelingPolicy{Enabled: true, ShortCircuitMid: true, MaxEscalations: 1},
			wantTier: catalog.TierFrontier,
			wantSC:   true,
		},
		{
			name:     "short-circuit path, unclassified primary",
			provider: lvlUnknownProvider,
			model:    lvlUnknownModel,
			policy:   &LevelingPolicy{Enabled: true, ShortCircuitMid: true, MaxEscalations: 1},
			wantTier: catalog.TierUnknown,
			wantSC:   true,
		},
		{
			name:     "active path, local primary",
			provider: lvlLowProvider,
			model:    lvlLowModel,
			policy:   &LevelingPolicy{Enabled: true, ShortCircuitMid: true, MaxEscalations: 1},
			wantTier: catalog.TierLocal,
		},
		{
			name:     "active path, mid primary with short-circuiting off",
			provider: lvlMidProvider,
			model:    lvlMidModel,
			policy:   &LevelingPolicy{Enabled: true, ShortCircuitMid: false, MaxEscalations: 1},
			wantTier: catalog.TierMid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			primary := newMockRung(0.05, lvlFencedJSON)
			rung := newMockRung(0.90, lvlValidJSON)

			exec := newTestLevelingExecutor(t, tt.policy)

			result, report, err := exec.Execute(
				context.Background(), schemaPolicy(2, true),
				[]LevelingRung{
					{Provider: tt.provider, Model: tt.model, Execute: primary.execute},
					{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung.execute},
				},
				"do the thing", "wf-fenced")

			require.NoError(t, err)
			require.NotNil(t, report)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantTier, report.Tier)
			assert.Equal(t, tt.wantSC, report.ShortCircuited)
			assert.True(t, report.Passed, "a fenced-but-valid payload satisfies the schema")
			assert.True(t, report.CoercionApplied, "the pass came from the free rewrite")
			assert.Equal(t, lvlValidJSON, result.Output, "the result carries the extracted JSON")
			assert.Equal(t, 1, primary.count(), "no retry: the rewrite ends the loop on the first attempt")
			assert.Equal(t, 0, rung.count(), "no escalation is paid for")
			assert.Equal(t, 0, report.Escalations)
			assert.Empty(t, report.Warnings)
			assert.InDelta(t, 0.05, report.TotalCostUSD, 1e-9)
		})
	}
}

// TestLevelingShortCircuitTotalsEveryAttempt pins the short-circuit path's cost
// accounting. The path runs the validator, which may retry, so the report has to
// sum every attempt rather than reading the cost off the one result returned.
func TestLevelingShortCircuitTotalsEveryAttempt(t *testing.T) {
	t.Parallel()

	primary := newMockRung(0.30, lvlInvalidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:         true,
		ShortCircuitMid: true,
		MaxEscalations:  1,
	})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(2, true),
		[]LevelingRung{{
			Provider: lvlFrontierProvider,
			Model:    lvlFrontierModel,
			Execute:  primary.execute,
		}},
		"do the thing", "wf-sc-cost")

	require.NoError(t, err)
	require.NotNil(t, report)
	require.NotNil(t, result)
	assert.True(t, report.ShortCircuited)
	assert.False(t, report.Passed)
	assert.Equal(t, 3, primary.count(), "the caller's retry policy still applies")
	assert.InDelta(t, 3*0.30, report.TotalCostUSD, 1e-9,
		"every attempt counts, not just the one whose result came back")
}

// TestLevelingExhaustionReturnsAUsableResult pins which output comes back when
// the ladder is spent: the last rung's when the last rung attempted produced
// one, and the primary's when that rung errored and produced nothing.
func TestLevelingExhaustionReturnsAUsableResult(t *testing.T) {
	t.Parallel()

	const primaryOut = `{"wrong":"primary"}`
	const rungOut = `{"wrong":"rung"}`

	tests := []struct {
		name            string
		rungs           []*mockRung
		maxEscalations  int
		wantOutput      string
		wantEscalations int
		wantWarning     string
	}{
		{
			name:            "final rung errored: the primary's result comes back",
			rungs:           []*mockRung{newFailingRung(errors.New("rung 1 is down"))},
			maxEscalations:  1,
			wantOutput:      primaryOut,
			wantEscalations: 1,
			wantWarning:     "escalation rung 1 (anthropic/claude-opus-4-7) failed: rung 1 is down",
		},
		{
			name:            "final rung produced a rejected output: that output comes back",
			rungs:           []*mockRung{newMockRung(0.90, rungOut)},
			maxEscalations:  1,
			wantOutput:      rungOut,
			wantEscalations: 1,
			wantWarning:     "escalation rung 1 output rejected",
		},
		{
			// A usable rung followed by a broken one: the broken rung is the
			// last attempted, so it has no output and the primary's stands.
			name: "good rung then errored rung: the primary's result comes back",
			rungs: []*mockRung{
				newMockRung(0.50, rungOut),
				newFailingRung(errors.New("rung 2 is down")),
			},
			maxEscalations:  2,
			wantOutput:      primaryOut,
			wantEscalations: 2,
			wantWarning:     "escalation rung 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			primary := newMockRung(0.01, primaryOut)
			ladder := []LevelingRung{{
				Provider: lvlLowProvider,
				Model:    lvlLowModel,
				Execute:  primary.execute,
			}}
			for _, r := range tt.rungs {
				ladder = append(ladder, LevelingRung{
					Provider: lvlFrontierProvider,
					Model:    lvlFrontierModel,
					Execute:  r.execute,
				})
			}

			exec := newTestLevelingExecutor(t, &LevelingPolicy{
				Enabled:        true,
				MaxEscalations: tt.maxEscalations,
				// Zero retry budget keeps the primary at one call so the
				// returned output is unambiguous.
				TierPolicies: map[catalog.ModelTier]TierPolicy{
					catalog.TierLocal: {RetryBudget: 0},
				},
			})

			result, report, err := exec.Execute(
				context.Background(), schemaPolicy(0, true), ladder, "do the thing", "wf-exhausted")

			require.NoError(t, err, "exhaustion is not an error")
			require.NotNil(t, report)
			require.NotNil(t, result, "exhaustion still returns an output")
			assert.False(t, report.Passed)
			assert.Equal(t, tt.wantEscalations, report.Escalations)
			assert.Equal(t, tt.wantOutput, result.Output)
			assert.Equal(t, 1, primary.count())

			joined := ""
			for _, w := range report.Warnings {
				joined += w + "\n"
			}
			assert.Contains(t, joined, tt.wantWarning)
			assert.Contains(t, joined, "leveling exhausted:")
		})
	}
}
