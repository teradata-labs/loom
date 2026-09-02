// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// loadRetryStageWithLogger loads a one-stage pipeline whose stage body is body,
// with logger wired into the loader, and returns the stage plus everything the
// loader logged at warn level or above.
func loadRetryStageWithLogger(t *testing.T, body string) (*loomv1.PipelineStage, []observer.LoggedEntry, error) {
	t.Helper()

	core, logs := observer.New(zapcore.WarnLevel)
	pattern, err := LoadWorkflowFromYAMLBytesWithLogger(
		[]byte(levelingStageYAMLHeader+body), zap.New(core))
	if err != nil {
		return nil, logs.All(), err
	}
	stages := pattern.GetPipeline().GetStages()
	require.Len(t, stages, 1)
	return stages[0], logs.All(), nil
}

// warnContext renders a logged entry's fields as strings. The concrete Go type a
// zap field decodes to is the logging library's business; what this test pins is
// the content an operator reads — the offending value and what it resolved to.
func warnContext(entry observer.LoggedEntry) map[string]string {
	rendered := make(map[string]string, len(entry.Context))
	for key, value := range entry.ContextMap() {
		rendered[key] = fmt.Sprint(value)
	}
	return rendered
}

// TestWorkflowYAMLRetryPolicyLegacyShapesStayLoadable pins the three retry_policy
// shapes that loaded before the strict scalar helpers and must keep loading, each
// with a warning that names the offending value and what it resolved to.
//
// The shapes are tolerated because rejecting them would break configs that
// already run: the strict version of this loader turned three silently-accepted
// documents into load failures. The warning is the difference from the original
// silence — the value is still accepted the way it always was, but no longer
// without a trace.
func TestWorkflowYAMLRetryPolicyLegacyShapesStayLoadable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		// wantMaxRetries is 0 when the tolerated shape means "no retry policy":
		// wantRetryPolicy then says whether the field is nil.
		wantRetryPolicy bool
		wantMaxRetries  int32
		wantWarnMessage string
		wantWarnFields  map[string]string
	}{
		{
			name:            "fractional max_retries truncates toward zero",
			body:            "      retry_policy:\n        max_retries: 2.5\n",
			wantRetryPolicy: true,
			wantMaxRetries:  2,
			wantWarnMessage: "workflow retry_policy.max_retries is not a whole number: truncating toward zero",
			wantWarnFields: map[string]string{
				"field":       "spec.stages[0].retry_policy.max_retries",
				"yaml_value":  "2.5",
				"resolved_to": "2",
			},
		},
		{
			name:            "string max_retries is ignored, as if absent",
			body:            "      retry_policy:\n        max_retries: \"3\"\n",
			wantRetryPolicy: false,
			wantWarnMessage: "workflow retry_policy.max_retries is a string: ignoring it as if the key were absent",
			wantWarnFields: map[string]string{
				"field":       "spec.stages[0].retry_policy.max_retries",
				"yaml_value":  "3",
				"resolved_to": "no retry policy",
			},
		},
		{
			name:            "session_mode without max_retries drops the policy",
			body:            "      retry_policy:\n        session_mode: fresh\n",
			wantRetryPolicy: false,
			wantWarnMessage: "workflow retry_policy dropped: its retry-only keys need max_retries >= 1",
			wantWarnFields: map[string]string{
				"field":        "spec.stages[0].retry_policy",
				"max_retries":  "0",
				"ignored_keys": "[session_mode]",
				"resolved_to":  "no retry policy",
			},
		},
		{
			name:            "feedback_template and cooldown_ms with max_retries: 0 drop the policy",
			body:            "      retry_policy:\n        max_retries: 0\n        feedback_template: \"fix it\"\n        cooldown_ms: 250\n",
			wantRetryPolicy: false,
			wantWarnMessage: "workflow retry_policy dropped: its retry-only keys need max_retries >= 1",
			wantWarnFields: map[string]string{
				"field":        "spec.stages[0].retry_policy",
				"max_retries":  "0",
				"ignored_keys": "[feedback_template cooldown_ms]",
			},
		},
		{
			name:            "negative max_retries with a retry-only key drops the policy",
			body:            "      retry_policy:\n        max_retries: -1\n        cooldown_ms: 10\n",
			wantRetryPolicy: false,
			wantWarnMessage: "workflow retry_policy dropped: its retry-only keys need max_retries >= 1",
			wantWarnFields: map[string]string{
				"field":        "spec.stages[0].retry_policy",
				"max_retries":  "-1",
				"ignored_keys": "[cooldown_ms]",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stage, entries, err := loadRetryStageWithLogger(t, tt.body)
			require.NoError(t, err, "the shape loaded before the strict helpers and must keep loading")

			if tt.wantRetryPolicy {
				require.NotNil(t, stage.RetryPolicy)
				assert.Equal(t, tt.wantMaxRetries, stage.GetRetryPolicy().GetMaxRetries())
			} else {
				assert.Nil(t, stage.RetryPolicy, "the tolerated shape means no retry policy")
			}

			require.Len(t, entries, 1, "exactly one warning, naming the one tolerated value")
			assert.Equal(t, zapcore.WarnLevel, entries[0].Level)
			assert.Equal(t, tt.wantWarnMessage, entries[0].Message)
			context := warnContext(entries[0])
			for key, want := range tt.wantWarnFields {
				assert.Equal(t, want, context[key], "warning field %q", key)
			}
		})
	}
}

// TestWorkflowYAMLRetryPolicyToleranceWarnsPerOffendingValue pins that the
// tolerances compose: a string max_retries alongside a retry-only key warns twice
// — once for the ignored value and once for the dropped policy — because an
// operator fixing this config needs to be told both things.
func TestWorkflowYAMLRetryPolicyToleranceWarnsPerOffendingValue(t *testing.T) {
	t.Parallel()

	stage, entries, err := loadRetryStageWithLogger(t,
		"      retry_policy:\n        max_retries: \"3\"\n        session_mode: continue\n")
	require.NoError(t, err)
	assert.Nil(t, stage.RetryPolicy)

	require.Len(t, entries, 2)
	assert.Contains(t, entries[0].Message, "max_retries is a string")
	assert.Contains(t, entries[1].Message, "retry_policy dropped")
	assert.Equal(t, "[session_mode]", warnContext(entries[1])["ignored_keys"])
}

// TestWorkflowYAMLRetryPolicyToleranceReportsNestedPath pins that the warning
// names the block it came from, not a generic "retry_policy": the same tolerance
// runs for the retry_policy nested in output_policy, and for a spec-level one on
// a pattern that carries it there.
func TestWorkflowYAMLRetryPolicyToleranceReportsNestedPath(t *testing.T) {
	t.Parallel()

	t.Run("output_policy retry_policy", func(t *testing.T) {
		t.Parallel()

		stage, entries, err := loadRetryStageWithLogger(t,
			"      output_policy:\n        retry_policy:\n          max_retries: 1.5\n")
		require.NoError(t, err)
		require.NotNil(t, stage.GetOutputPolicy().GetRetryPolicy())
		assert.Equal(t, int32(1), stage.GetOutputPolicy().GetRetryPolicy().GetMaxRetries())

		require.Len(t, entries, 1)
		assert.Equal(t, "spec.stages[0].output_policy.retry_policy.max_retries",
			warnContext(entries[0])["field"])
	})

	t.Run("swarm spec-level retry_policy", func(t *testing.T) {
		t.Parallel()

		core, logs := observer.New(zapcore.WarnLevel)
		pattern, err := LoadWorkflowFromYAMLBytesWithLogger([]byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: swarm-tolerated-retry
spec:
  type: swarm
  question: "which database?"
  agent_ids: [voter1, voter2, voter3]
  retry_policy:
    max_retries: 2.5
`), zap.New(core))
		require.NoError(t, err)
		assert.Equal(t, int32(2), pattern.GetSwarm().GetRetryPolicy().GetMaxRetries())

		entries := logs.All()
		require.Len(t, entries, 1)
		assert.Equal(t, "spec.retry_policy.max_retries", warnContext(entries[0])["field"])
	})
}

// TestWorkflowYAMLRetryPolicyStrictnessSurvivesTolerance is the boundary: the
// tolerance is a list of three shapes with legacy configs behind them, not a
// general leniency. Every other malformation in the block still fails the load,
// and so does every malformation in a leveling block — that surface has no
// pre-existing configs to keep loading.
func TestWorkflowYAMLRetryPolicyStrictnessSurvivesTolerance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name:     "boolean max_retries",
			body:     "      retry_policy:\n        max_retries: true\n",
			contains: "spec.stages[0].retry_policy.max_retries must be an integer",
		},
		{
			name:     "unknown session_mode with a positive max_retries",
			body:     "      retry_policy:\n        max_retries: 1\n        session_mode: teleport\n",
			contains: "is not a known retry session mode",
		},
		{
			name:     "fractional cooldown_ms",
			body:     "      retry_policy:\n        max_retries: 1\n        cooldown_ms: 12.5\n",
			contains: "spec.stages[0].retry_policy.cooldown_ms must be a whole number",
		},
		{
			name:     "string cooldown_ms",
			body:     "      retry_policy:\n        max_retries: 1\n        cooldown_ms: \"250\"\n",
			contains: "spec.stages[0].retry_policy.cooldown_ms must be an integer",
		},
		{
			name:     "leveling max_escalations is not tolerated the way max_retries is",
			body:     "      leveling:\n        enabled: true\n        max_escalations: \"2\"\n",
			contains: "spec.stages[0].leveling.max_escalations must be an integer",
		},
		{
			name:     "leveling retry_budget fraction is not truncated",
			body:     "      leveling:\n        enabled: true\n        tier_policies:\n          local:\n            retry_budget: 1.5\n",
			contains: "tier_policies[local].retry_budget must be a whole number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, entries, err := loadRetryStageWithLogger(t, tt.body)
			require.Error(t, err, "only the three legacy shapes are tolerated")
			require.ErrorIs(t, err, ErrInvalidWorkflow)
			assert.Contains(t, err.Error(), tt.contains)
			assert.Empty(t, entries, "a load error is reported as an error, not as a warning")
		})
	}
}

// TestWorkflowYAMLRetryPolicyToleranceWithoutLogger pins that the warnings are
// optional and the tolerance is not: the logger-free entry points every current
// caller uses must accept the same three shapes, with the same results, and
// without a nil-logger panic.
func TestWorkflowYAMLRetryPolicyToleranceWithoutLogger(t *testing.T) {
	t.Parallel()

	t.Run("fractional max_retries", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t, "      retry_policy:\n        max_retries: 2.5\n")
		require.NoError(t, err)
		assert.Equal(t, int32(2), stage.GetRetryPolicy().GetMaxRetries())
	})

	t.Run("string max_retries", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t, "      retry_policy:\n        max_retries: \"3\"\n")
		require.NoError(t, err)
		assert.Nil(t, stage.RetryPolicy)
	})

	t.Run("retry-only key without max_retries", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t, "      retry_policy:\n        feedback_template: \"fix it\"\n")
		require.NoError(t, err)
		assert.Nil(t, stage.RetryPolicy)
	})
}
