// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// TestLevelingEscalationStopsOnCancelledContext pins the cancellation check at
// the top of the escalation loop. ValidateAndRetry checks ctx.Err() per
// attempt; the ladder did not, so a context cancelled during the primary's only
// attempt let every remaining rung fire, fail, and be written off — handing the
// caller a nil error and an "exhausted" report for a run that had been called
// off, having paid for each rung on the way.
func TestLevelingEscalationStopsOnCancelledContext(t *testing.T) {
	t.Parallel()

	primary := newMockRung(0.02, lvlInvalidJSON)
	rung := newMockRung(0.90, lvlValidJSON)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The primary cancels on its way out: the attempt itself completes and its
	// output fails the schema, so the ladder is entered with a dead context.
	cancelOnReturn := func(c context.Context, sessionID, prompt string) (*loomv1.AgentResult, error) {
		result, err := primary.execute(c, sessionID, prompt)
		cancel()
		return result, err
	}

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 2,
		// Zero retry budget keeps the primary to a single attempt, so the
		// cancellation lands between the validator and the first rung.
		TierPolicies: map[catalog.ModelTier]TierPolicy{catalog.TierLocal: {RetryBudget: 0}},
	})

	result, report, err := exec.Execute(
		ctx, schemaPolicy(0, false),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: cancelOnReturn},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung.execute},
		},
		"do the thing", "wf-lvl-cancel")

	require.Error(t, err, "cancellation is an error, matching ValidateAndRetry")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, primary.count())
	assert.Equal(t, 0, rung.count(), "no rung fires against a cancelled context")
	require.NotNil(t, report)
	assert.Equal(t, 0, report.Escalations)
	assert.False(t, report.Passed)
	assert.InDelta(t, 0.02, report.TotalCostUSD, 1e-9, "the primary's spend is still reported")
	assert.True(t, lvlAnyWarningContains(report.Warnings, "escalation stopped"),
		"the stop is recorded: %v", report.Warnings)
	require.NotNil(t, result, "the primary's output is still handed back")
	assert.Equal(t, lvlInvalidJSON, result.Output)
}

// TestLevelingNilRungExecuteRejected pins the nil-Execute guard. Execute is an
// exported field on LevelingRung, so a Go caller can build a ladder with one
// unset; dispatching it panics on a nil func call. The whole ladder is checked
// up front, before either the disabled or the enabled path runs, so the rung is
// named in an error instead — including rungs the path in question would never
// have dispatched.
func TestLevelingNilRungExecuteRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   *LevelingPolicy
		nilIndex int
		wantErr  string
	}{
		{
			name:     "nil primary with leveling enabled",
			policy:   &LevelingPolicy{Enabled: true, MaxEscalations: 1},
			nilIndex: 0,
			wantErr:  "leveling: rung 0 (ollama/llama3.2) has no Execute function",
		},
		{
			name:     "nil escalation rung with leveling enabled",
			policy:   &LevelingPolicy{Enabled: true, MaxEscalations: 1},
			nilIndex: 1,
			wantErr:  "leveling: rung 1 (anthropic/claude-opus-4-7) has no Execute function",
		},
		{
			name:     "nil primary with leveling disabled",
			policy:   nil,
			nilIndex: 0,
			wantErr:  "leveling: rung 0 (ollama/llama3.2) has no Execute function",
		},
		{
			// The disabled path only ever touches ladder[0], but a ladder it
			// cannot climb is still a wiring mistake worth naming.
			name:     "nil escalation rung with leveling disabled",
			policy:   nil,
			nilIndex: 1,
			wantErr:  "leveling: rung 1 (anthropic/claude-opus-4-7) has no Execute function",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			healthy := newMockRung(0.02, lvlValidJSON)
			ladder := []LevelingRung{
				{Provider: lvlLowProvider, Model: lvlLowModel},
				{Provider: lvlFrontierProvider, Model: lvlFrontierModel},
			}
			for i := range ladder {
				if i != tt.nilIndex {
					ladder[i].Execute = healthy.execute
				}
			}

			exec := newTestLevelingExecutor(t, tt.policy)

			result, report, err := exec.Execute(
				context.Background(), schemaPolicy(0, false), ladder,
				"do the thing", "wf-lvl-nilrung")

			require.Error(t, err)
			assert.EqualError(t, err, tt.wantErr)
			assert.Nil(t, result)
			assert.Nil(t, report, "a rejected ladder never started, so there is nothing to report")
			assert.Equal(t, 0, healthy.count(), "no rung runs once the ladder is rejected")
		})
	}
}

// TestLevelingEmptyLadderNotReportedAsNilRung guards the ordering of the two
// ladder-shape guards: an empty ladder is still reported as empty rather than
// being described as a rung with no Execute function.
func TestLevelingEmptyLadderNotReportedAsNilRung(t *testing.T) {
	t.Parallel()

	exec := newTestLevelingExecutor(t, &LevelingPolicy{Enabled: true})

	_, _, err := exec.Execute(
		context.Background(), schemaPolicy(0, false), []LevelingRung{},
		"do the thing", "wf-lvl-empty")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ladder is empty")
	assert.NotContains(t, err.Error(), "has no Execute function")
}

// TestLevelingStageWarningsNameTheCause pins the sentence an operator reads
// when a leveled stage continues with unvalidated output. A short circuit has
// two unrelated causes — a primary the catalog places at mid or frontier, and a
// primary the catalog has never heard of — and the warning used to call both
// "a strong primary", telling the operator the opposite of the truth for a
// model nothing had been measured about.
func TestLevelingStageWarningsNameTheCause(t *testing.T) {
	t.Parallel()

	stage := &loomv1.PipelineStage{AgentId: "worker"}

	tests := []struct {
		name        string
		report      *LevelingReport
		wantCause   string
		wantAbsent  []string
		wantPrefix  int
		wantWarning bool
	}{
		{
			name:        "unclassified primary is not called strong",
			report:      &LevelingReport{Tier: catalog.TierUnknown, ShortCircuited: true},
			wantCause:   "leveling short-circuited on an unclassified primary (model not in the catalog) and did not escalate",
			wantAbsent:  []string{"strong primary"},
			wantWarning: true,
		},
		{
			name:        "frontier primary is called strong",
			report:      &LevelingReport{Tier: catalog.TierFrontier, ShortCircuited: true},
			wantCause:   "leveling short-circuited on a strong primary and did not escalate",
			wantAbsent:  []string{"unclassified"},
			wantWarning: true,
		},
		{
			name:        "mid primary that short-circuited is called strong",
			report:      &LevelingReport{Tier: catalog.TierMid, ShortCircuited: true},
			wantCause:   "leveling short-circuited on a strong primary and did not escalate",
			wantWarning: true,
		},
		{
			name:        "exhausted ladder names exhaustion",
			report:      &LevelingReport{Tier: catalog.TierLocal, Escalations: 1, BudgetExhausted: true},
			wantCause:   "leveling exhausted its rungs",
			wantAbsent:  []string{"short-circuited"},
			wantWarning: true,
		},
		{
			name:   "a passing report adds nothing beyond the executor's own warnings",
			report: &LevelingReport{Tier: catalog.TierLocal, Passed: true, Warnings: []string{"judge skipped"}},
			// The one executor warning is re-prefixed with the stage; no
			// "continuing with unvalidated output" line follows it.
			wantPrefix: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := levelingStageWarnings(stage, 3, tt.report)

			if !tt.wantWarning {
				require.Len(t, got, tt.wantPrefix)
				for _, w := range got {
					assert.True(t, strings.HasPrefix(w, "stage 3 (worker): "), w)
					assert.NotContains(t, w, "continuing with unvalidated output")
				}
				return
			}

			require.Len(t, got, len(tt.report.Warnings)+1)
			last := got[len(got)-1]
			assert.Contains(t, last, "stage 3 (worker): continuing with unvalidated output — "+tt.wantCause)
			assert.Contains(t, last, "tier="+tt.report.Tier.String())
			assert.Contains(t, last, "escalations="+strconv.Itoa(tt.report.Escalations))
			assert.Contains(t, last, "budget_exhausted="+strconv.FormatBool(tt.report.BudgetExhausted))
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, last, absent)
			}
		})
	}
}
