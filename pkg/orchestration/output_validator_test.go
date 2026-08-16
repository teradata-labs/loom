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
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

func newTestOutputValidator(t *testing.T) *OutputValidator {
	t.Helper()
	return NewOutputValidator(observability.NewNoOpTracer(), zaptest.NewLogger(t))
}

// jsonCoerce is the same free rewrite the leveling executor hands the validator.
func jsonCoerce(output string) (string, bool) {
	extracted := extractJSONFromText(output)
	return extracted, extracted != ""
}

// TestValidateAndRetryOutcome pins the verdict the validator hands back for each
// terminal shape of the retry loop.
func TestValidateAndRetryOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		policy     *loomv1.OutputPolicy
		outputs    []string
		coerce     CoerceFunc
		wantCalls  int
		wantPassed bool
		wantErr    bool // outcome.Err set
		wantWarns  int
		wantCoerce bool
		wantOutput string
	}{
		{
			name:       "first attempt passes",
			policy:     schemaPolicy(2, true),
			outputs:    []string{lvlValidJSON},
			wantCalls:  1,
			wantPassed: true,
			wantOutput: lvlValidJSON,
		},
		{
			name:       "retries exhausted reports the final error",
			policy:     schemaPolicy(2, true),
			outputs:    []string{lvlInvalidJSON},
			wantCalls:  3,
			wantPassed: false,
			wantErr:    true,
			wantWarns:  3,
			wantOutput: lvlInvalidJSON,
		},
		{
			name:       "a later attempt passes and clears the verdict",
			policy:     schemaPolicy(2, true),
			outputs:    []string{lvlInvalidJSON, lvlValidJSON},
			wantCalls:  2,
			wantPassed: true,
			wantWarns:  1,
			wantOutput: lvlValidJSON,
		},
		{
			name:       "nil policy passes vacuously",
			policy:     nil,
			outputs:    []string{lvlInvalidJSON},
			wantCalls:  1,
			wantPassed: true,
			wantOutput: lvlInvalidJSON,
		},
		{
			name:       "policy with no criteria passes vacuously",
			policy:     &loomv1.OutputPolicy{RetryPolicy: &loomv1.OutputRetryPolicy{MaxRetries: 2}},
			outputs:    []string{lvlInvalidJSON},
			wantCalls:  1,
			wantPassed: true,
			wantOutput: lvlInvalidJSON,
		},
		{
			name:       "coercion rescues the first attempt",
			policy:     schemaPolicy(2, true),
			outputs:    []string{lvlFencedJSON},
			coerce:     jsonCoerce,
			wantCalls:  1,
			wantPassed: true,
			wantCoerce: true,
			wantOutput: lvlValidJSON,
		},
		{
			name:       "coercion that cannot rescue leaves the retries alone",
			policy:     schemaPolicy(2, true),
			outputs:    []string{lvlInvalidJSON},
			coerce:     jsonCoerce,
			wantCalls:  3,
			wantPassed: false,
			wantErr:    true,
			wantWarns:  3,
			wantOutput: lvlInvalidJSON,
		},
		{
			name:       "coercion rewrite that still fails the schema does not pass",
			policy:     schemaPolicy(1, true),
			outputs:    []string{"prelude {\"wrong\":\"shape\"} epilogue"},
			coerce:     jsonCoerce,
			wantCalls:  2,
			wantPassed: false,
			wantErr:    true,
			wantWarns:  2,
			wantOutput: "prelude {\"wrong\":\"shape\"} epilogue",
		},
		{
			name:       "coercion is inert without a schema",
			policy:     &loomv1.OutputPolicy{AcceptanceCriteria: "answers the question", RetryPolicy: &loomv1.OutputRetryPolicy{MaxRetries: 1}},
			outputs:    []string{lvlFencedJSON},
			coerce:     jsonCoerce,
			wantCalls:  1,
			wantPassed: true,
			wantOutput: lvlFencedJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			v := newTestOutputValidator(t)
			rung := newMockRung(0, tt.outputs...)

			result, outcome, err := v.ValidateAndRetry(
				context.Background(), tt.policy, rung.execute, nil,
				"do the thing", "wf-outcome", tt.coerce)

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantCalls, rung.count(), "execute call count")
			assert.Equal(t, tt.wantPassed, outcome.Passed)
			if tt.wantErr {
				require.Error(t, outcome.Err, "a failed outcome must carry its error")
			} else {
				assert.NoError(t, outcome.Err)
			}
			assert.Len(t, outcome.Warnings, tt.wantWarns)
			assert.Equal(t, tt.wantCoerce, outcome.CoercionApplied)
			assert.Equal(t, tt.wantOutput, result.Output)
		})
	}
}

// TestValidateAndRetryWarningsFormat pins the per-attempt warning strings, which
// feed retry prompts and workflow metadata.
func TestValidateAndRetryWarningsFormat(t *testing.T) {
	t.Parallel()

	v := newTestOutputValidator(t)
	rung := newMockRung(0, lvlInvalidJSON)

	_, outcome, err := v.ValidateAndRetry(
		context.Background(), schemaPolicy(1, true), rung.execute, nil,
		"do the thing", "wf-warn", nil)

	require.NoError(t, err)
	require.Len(t, outcome.Warnings, 2)
	assert.Contains(t, outcome.Warnings[0], "attempt 1: ")
	assert.Contains(t, outcome.Warnings[1], "attempt 2: ")
	assert.Contains(t, outcome.Warnings[0], "not valid JSON")
	require.Error(t, outcome.Err)
	assert.Contains(t, outcome.Err.Error(), "not valid JSON")

	// The retry prompt carries the previous attempt's warning.
	assert.Contains(t, rung.promptAt(1), "attempt 1: ")
}

// TestValidateAndRetryCoercionSkipsTheRetryPrompt proves a coerced pass never
// reaches the retry machinery: one session, no retry prompt, no warnings.
func TestValidateAndRetryCoercionSkipsTheRetryPrompt(t *testing.T) {
	t.Parallel()

	v := newTestOutputValidator(t)
	rung := newMockRung(0, lvlFencedJSON, lvlValidJSON)

	result, outcome, err := v.ValidateAndRetry(
		context.Background(), schemaPolicy(3, true), rung.execute, nil,
		"do the thing", "wf-coerce-once", jsonCoerce)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, rung.count(), "a free rewrite must not cost a retry")
	assert.True(t, outcome.Passed)
	assert.True(t, outcome.CoercionApplied)
	assert.NoError(t, outcome.Err)
	assert.Empty(t, outcome.Warnings, "a rescued attempt produces no warning")
	assert.Equal(t, lvlValidJSON, result.Output, "the result carries the rewrite")
	assert.Equal(t, "do the thing", rung.promptAt(0), "original prompt, unmodified")
	assert.Equal(t, "wf-coerce-once", rung.sessionAt(0))
}

// TestValidateAndRetryExecutionErrorOutcome proves an execution failure reports a
// failed verdict alongside the warnings accumulated so far.
func TestValidateAndRetryExecutionErrorOutcome(t *testing.T) {
	t.Parallel()

	v := newTestOutputValidator(t)
	rung := newFailingRung(errors.New("upstream 503"))

	result, outcome, err := v.ValidateAndRetry(
		context.Background(), schemaPolicy(1, true), rung.execute, nil,
		"do the thing", "wf-exec-err", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream 503")
	assert.Nil(t, result)
	assert.False(t, outcome.Passed)
	assert.Empty(t, outcome.Warnings)
}

// TestValidateAndRetryCanceledContextOutcome proves cancellation reports a failed
// verdict rather than a vacuous pass.
func TestValidateAndRetryCanceledContextOutcome(t *testing.T) {
	t.Parallel()

	v := newTestOutputValidator(t)
	rung := newMockRung(0, lvlValidJSON)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, outcome, err := v.ValidateAndRetry(
		ctx, schemaPolicy(1, true), rung.execute, nil,
		"do the thing", "wf-canceled", nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, result)
	assert.Equal(t, 0, rung.count(), "a canceled context executes nothing")
	assert.False(t, outcome.Passed)
}

// TestValidateAndRetryCoerceNotConsultedOnPass proves the hook is only reached by
// a failing attempt — a valid output is never rewritten.
func TestValidateAndRetryCoerceNotConsultedOnPass(t *testing.T) {
	t.Parallel()

	v := newTestOutputValidator(t)
	rung := newMockRung(0, lvlValidJSON)

	calls := 0
	coerce := func(output string) (string, bool) {
		calls++
		return `{"answer":"rewritten"}`, true
	}

	result, outcome, err := v.ValidateAndRetry(
		context.Background(), schemaPolicy(1, true), rung.execute, nil,
		"do the thing", "wf-coerce-unused", coerce)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, calls, "a passing output is never handed to the hook")
	assert.True(t, outcome.Passed)
	assert.False(t, outcome.CoercionApplied)
	assert.Equal(t, lvlValidJSON, result.Output)
}

// TestLevelingPrimaryVerdictComesFromTheValidator proves the active path takes its
// verdict from the validator's outcome instead of re-validating: a schema-passing
// primary reports Passed with no escalation and no judge call, and a coerced pass
// is reported as a pass.
func TestLevelingPrimaryVerdictComesFromTheValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		output     string
		wantPassed bool
		wantCoerce bool
		wantOutput string
		wantCalls  int
	}{
		{
			name:       "schema pass",
			output:     lvlValidJSON,
			wantPassed: true,
			wantOutput: lvlValidJSON,
			wantCalls:  1,
		},
		{
			name:       "coerced pass",
			output:     lvlFencedJSON,
			wantPassed: true,
			wantCoerce: true,
			wantOutput: lvlValidJSON,
			wantCalls:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A judge that would fail the output if it were ever consulted.
			judge := newMockJudge(0.01, false)
			rung0 := newMockRung(0.02, tt.output)
			rung1 := newMockRung(0.90, lvlValidJSON)

			exec := newTestLevelingExecutor(t, &LevelingPolicy{
				Enabled:        true,
				MaxEscalations: 1,
				Judge:          judge.judge,
			})

			result, report, err := exec.Execute(
				context.Background(), schemaPolicy(1, true),
				[]LevelingRung{
					{Provider: lvlLowProvider, Model: lvlLowModel, Execute: rung0.execute},
					{Provider: lvlFrontierProvider, Model: lvlFrontierModel, Execute: rung1.execute},
				},
				"do the thing", "wf-primary-verdict")

			require.NoError(t, err)
			require.NotNil(t, report)
			assert.Equal(t, tt.wantPassed, report.Passed)
			assert.Equal(t, tt.wantCoerce, report.CoercionApplied)
			assert.Equal(t, tt.wantCalls, rung0.count(), "no attempt beyond the first")
			assert.Equal(t, 0, rung1.count(), "a passing primary must not escalate")
			assert.Equal(t, 0, report.Escalations)
			assert.Equal(t, 0, judge.count(), "a schema owns the verdict, the judge stays idle")
			assert.Empty(t, report.Warnings)
			assert.InDelta(t, 0.02, report.TotalCostUSD, 1e-9)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantOutput, result.Output)
		})
	}
}
