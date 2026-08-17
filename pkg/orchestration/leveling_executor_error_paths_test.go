// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// nilResultRung is an ExecuteFunc that reports success while returning no
// result. It models a rung whose underlying transport succeeded but produced
// nothing usable, which both verdict paths must treat as a failure signal rather
// than a pass.
type nilResultRung struct {
	calls atomic.Int32
}

func (r *nilResultRung) execute(context.Context, string, string) (*loomv1.AgentResult, error) {
	r.calls.Add(1)
	return nil, nil
}

func (r *nilResultRung) count() int { return int(r.calls.Load()) }

// TestLevelingShortCircuitPropagatesExecutionError pins that an execution
// failure on the short-circuit path still returns a report. Without one the
// caller cannot tell whether leveling was even consulted, and the pipeline's
// warning rendering has nothing to key on.
func TestLevelingShortCircuitPropagatesExecutionError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("frontier transport exploded")
	rung0 := newFailingRung(wantErr)
	rung1 := newMockRung(0.9, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, true),
		[]LevelingRung{
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-sc-err")

	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, result)
	require.NotNil(t, report, "the report must survive an execution error")
	assert.True(t, report.ShortCircuited)
	assert.Equal(t, catalog.TierFrontier, report.Tier)
	assert.False(t, report.Passed)
	assert.Zero(t, report.TotalCostUSD, "a failed call produced no result to price")
	assert.Equal(t, 0, rung1.count(), "an execution error is not an escalation trigger")
}

// TestLevelingActivePathPropagatesExecutionError is the low-tier twin: the
// active path has already spent money on earlier attempts, so its error return
// must carry the running total rather than reporting zero.
func TestLevelingActivePathPropagatesExecutionError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("local transport exploded")
	rung0 := newFailingRung(wantErr)
	rung1 := newMockRung(0.9, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, true),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-active-err")

	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, result)
	require.NotNil(t, report)
	assert.False(t, report.ShortCircuited)
	assert.Equal(t, catalog.TierLocal, report.Tier)
	assert.False(t, report.Passed)
	assert.Equal(t, 0, report.Escalations, "the ladder is not entered after an execution error")
	assert.Equal(t, 0, rung1.count())
}

// TestLevelingContinueModeSpendsThroughFeedback pins that the primary rung's
// Feedback function is wrapped for cost accounting exactly like its Execute. A
// CONTINUE-mode retry runs through Feedback, so an unwrapped one would make
// every continued retry invisible to MaxCostUSD.
func TestLevelingContinueModeSpendsThroughFeedback(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.02, lvlInvalidJSON)
	feedback := newMockRung(0.05, lvlInvalidJSON)
	rung1 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	result, report, err := exec.Execute(
		context.Background(),
		&loomv1.OutputPolicy{
			OutputSchema: lvlSchema,
			RetryPolicy: &loomv1.OutputRetryPolicy{
				MaxRetries:  1,
				SessionMode: loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE,
			},
		},
		[]LevelingRung{
			{
				Provider: lvlLowProvider,
				Model:    lvlLowModel,
				Execute:  rung0.execute,
				Feedback: feedback.execute,
			},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
		},
		"do the thing", "wf-continue")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 1, rung0.count(), "the first attempt runs through Execute")
	assert.Equal(t, 1, feedback.count(), "the CONTINUE retry runs through Feedback")
	assert.Equal(t, 1, rung1.count(), "both primary attempts failed, so the ladder moves on")
	assert.True(t, report.Passed)
	assert.InDelta(t, 0.02+0.05+0.90, report.TotalCostUSD, 1e-9,
		"the Feedback attempt's spend counts toward the ceiling")
	assert.Contains(t, feedback.promptAt(0), "schema validation",
		"the feedback message carries the validation failure")
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.Output)
}

// TestLevelingNilResultIsAFailureSignal covers the no-result guard on both
// verdict paths, with no OutputPolicy at all — the configuration where leveling
// hands the validator a nil policy and gets whatever the rung returned back
// unexamined. A nil result must escalate rather than be reported as a pass.
func TestLevelingNilResultIsAFailureSignal(t *testing.T) {
	t.Parallel()

	primary := &nilResultRung{}
	escalation := &nilResultRung{}

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 1,
	})

	result, report, err := exec.Execute(
		context.Background(), nil,
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: primary.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: escalation.execute},
		},
		"do the thing", "wf-nil-result")

	require.NoError(t, err, "a missing result is a verdict, not a transport failure")
	assert.Nil(t, result)
	require.NotNil(t, report)
	assert.Equal(t, 1, primary.count())
	assert.Equal(t, 1, escalation.count(), "the primary's nil result triggers one escalation")
	assert.Equal(t, 1, report.Escalations)
	assert.False(t, report.Passed)
	assert.Zero(t, report.TotalCostUSD)
	joined := strings.Join(report.Warnings, "\n")
	assert.Contains(t, joined, "no result produced")
}

// TestLevelingEscalationRungCoercionEndsTheLadder covers coercion on an
// escalation rung, which bypasses the validator and so has its own extraction
// step. A fenced-but-valid payload from rung 1 must end the ladder rather than
// pay for rung 2, and the returned result must carry the extracted JSON.
func TestLevelingEscalationRungCoercionEndsTheLadder(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.02, lvlInvalidJSON)
	rung1 := newMockRung(0.30, lvlFencedJSON)
	rung2 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 2,
		TierPolicies: map[catalog.ModelTier]TierPolicy{
			// Explicit zero retry budget isolates the escalation-side coercion
			// from the primary's in-validator retries.
			catalog.TierLocal: {RetryBudget: 0, AggressiveCoercion: true},
		},
	})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, true),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlMidProvider, Model: lvlMidModel, Execute: rung1.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung2.execute},
		},
		"do the thing", "wf-esc-coerce")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.True(t, report.Passed)
	assert.True(t, report.CoercionApplied, "the rung's fenced payload was extracted, not rejected")
	assert.Equal(t, 1, report.Escalations, "coercion on rung 1 must not pay for rung 2")
	assert.Equal(t, 1, rung0.count())
	assert.Equal(t, 1, rung1.count())
	assert.Equal(t, 0, rung2.count())
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.Output, "the result carries the extracted JSON")
	assert.InDelta(t, 0.02+0.30, report.TotalCostUSD, 1e-9)
}

// TestLevelingEscalationRungCoercionOffRejects is the control for the test
// above: with coercion disabled for the tier, the same fenced payload is a
// failure and the ladder keeps going.
func TestLevelingEscalationRungCoercionOffRejects(t *testing.T) {
	t.Parallel()

	rung0 := newMockRung(0.02, lvlInvalidJSON)
	rung1 := newMockRung(0.30, lvlFencedJSON)
	rung2 := newMockRung(0.90, lvlValidJSON)

	exec := newTestLevelingExecutor(t, &LevelingPolicy{
		Enabled:        true,
		MaxEscalations: 2,
		TierPolicies: map[catalog.ModelTier]TierPolicy{
			catalog.TierLocal: {RetryBudget: 0, AggressiveCoercion: false},
		},
	})

	result, report, err := exec.Execute(
		context.Background(), schemaPolicy(0, true),
		[]LevelingRung{
			{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
			{Provider: lvlMidProvider, Model: lvlMidModel, Execute: rung1.execute},
			{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung2.execute},
		},
		"do the thing", "wf-esc-no-coerce")

	require.NoError(t, err)
	require.NotNil(t, report)
	assert.False(t, report.CoercionApplied)
	assert.Equal(t, 2, report.Escalations)
	assert.Equal(t, 1, rung2.count(), "without coercion the fenced payload costs another rung")
	assert.True(t, report.Passed)
	require.NotNil(t, result)
	assert.Equal(t, lvlValidJSON, result.Output)
}

// TestEffectiveOutputPolicyNilStaysNil pins that a caller with no contract keeps
// having no contract: the tier's retry budget must not synthesize a policy,
// because a non-nil policy makes the validator start validating a stage that
// never asked to be validated.
func TestEffectiveOutputPolicyNilStaysNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, effectiveOutputPolicy(nil, TierPolicy{RetryBudget: 3}))
	assert.Nil(t, effectiveOutputPolicy(nil, TierPolicy{}))
}
