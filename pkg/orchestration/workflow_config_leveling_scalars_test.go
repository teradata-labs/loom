// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// TestWorkflowYAMLLevelingScalarTypeErrors covers the per-field type gates that
// TestWorkflowYAMLLevelingErrors does not reach. Every case is a wrong YAML
// scalar type, so it must fail the load regardless of `enabled` — the block
// cannot be stored at all, so there is nothing for the enabled gate to defer.
func TestWorkflowYAMLLevelingScalarTypeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains []string
	}{
		{
			name:     "short_circuit_mid not a boolean",
			body:     "      leveling:\n        enabled: true\n        short_circuit_mid: maybe\n",
			contains: []string{"spec.stages[0].leveling.short_circuit_mid must be a boolean", "string"},
		},
		{
			name:     "frontier_min_output_cost_usd not a number",
			body:     "      leveling:\n        enabled: true\n        frontier_min_output_cost_usd: expensive\n",
			contains: []string{"spec.stages[0].leveling.frontier_min_output_cost_usd must be a number"},
		},
		{
			name:     "mid_min_output_cost_usd not a number",
			body:     "      leveling:\n        enabled: true\n        mid_min_output_cost_usd: cheapish\n",
			contains: []string{"spec.stages[0].leveling.mid_min_output_cost_usd must be a number"},
		},
		{
			name:     "rung model not a string",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - provider: ollama\n            model: 7\n",
			contains: []string{"spec.stages[0].leveling.ladder[0].model must be a string", "int"},
		},
		{
			name:     "rung role not a string",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - role: 7\n",
			contains: []string{"spec.stages[0].leveling.ladder[0].role must be a string", "int"},
		},
		{
			name:     "tier entry not an object",
			body:     "      leveling:\n        enabled: true\n        tier_policies:\n          local: fast\n",
			contains: []string{"spec.stages[0].leveling.tier_policies[local] must be an object", "string"},
		},
		{
			name:     "tier retry_budget not an integer",
			body:     "      leveling:\n        enabled: true\n        tier_policies:\n          local:\n            retry_budget: many\n",
			contains: []string{"tier_policies[local].retry_budget must be an integer"},
		},
		{
			name:     "tier aggressive_coercion not a boolean",
			body:     "      leveling:\n        enabled: true\n        tier_policies:\n          local:\n            aggressive_coercion: sure\n",
			contains: []string{"tier_policies[local].aggressive_coercion must be a boolean"},
		},
		{
			// The removed key has no type left to get wrong: whatever it carries,
			// the load fails because the key itself is gone.
			name:     "removed scaffolding_depth outranks its old type check",
			body:     "      leveling:\n        enabled: true\n        tier_policies:\n          local:\n            scaffolding_depth: deep\n",
			contains: []string{"tier_policies[local].scaffolding_depth was removed"},
		},
		{
			name:     "blank role is not a role",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - role: \"   \"\n",
			contains: []string{"is not a known LLM role"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadLevelingStage(t, tt.body)
			require.Error(t, err, "a wrong scalar type must fail the load, not be ignored")
			require.ErrorIs(t, err, ErrInvalidWorkflow)
			for _, want := range tt.contains {
				assert.Contains(t, err.Error(), want)
			}

			// The same body on a parallel task fails the same way, with the
			// task's own YAML path.
			_, taskErr := loadLevelingTask(t, tt.body)
			require.Error(t, taskErr)
			assert.Contains(t, taskErr.Error(), "spec.tasks[0]")
		})
	}
}

// TestWorkflowYAMLLevelingNullTierEntryMeansDefaults pins the documented
// shorthand: a tier key with no body is "use the defaults for this tier", which
// must produce a present-but-empty tier policy rather than being dropped. A
// dropped key would silently disagree with the YAML the operator wrote.
func TestWorkflowYAMLLevelingNullTierEntryMeansDefaults(t *testing.T) {
	t.Parallel()

	stage, err := loadLevelingStage(t, `      leveling:
        enabled: true
        tier_policies:
          local:
          mid:
            retry_budget: 1
`)
	require.NoError(t, err)

	tiers := stage.GetLevelingPolicy().GetTierPolicies()
	require.Len(t, tiers, 2)
	local, ok := tiers["local"]
	require.True(t, ok, "an empty tier entry must still be present")
	require.NotNil(t, local)
	assert.Equal(t, int32(0), local.GetRetryBudget())
	assert.False(t, local.GetAggressiveCoercion())
	assert.Equal(t, int32(1), tiers["mid"].GetRetryBudget())

	// The empty entry is a valid tier name, so conversion succeeds and the tier
	// carries the explicit zeros rather than the built-in defaults.
	goPolicy, err := LevelingPolicyFromProto(stage.GetLevelingPolicy())
	require.NoError(t, err)
	require.Contains(t, goPolicy.TierPolicies, catalog.TierLocal)
	assert.Equal(t, 0, goPolicy.TierPolicies[catalog.TierLocal].RetryBudget,
		"an explicitly empty tier entry overrides the built-in retry budget with 0")
}

// TestWorkflowYAMLLevelingWholeNumberFloatsAccepted covers the YAML forms that
// are numerically fine but not the obvious Go type: an integer written with a
// decimal point (max_escalations: 2.0) and a float field written without one
// (max_cost_usd: 1).
func TestWorkflowYAMLLevelingWholeNumberFloatsAccepted(t *testing.T) {
	t.Parallel()

	stage, err := loadLevelingStage(t, `      leveling:
        enabled: true
        max_escalations: 2.0
        max_cost_usd: 1
        frontier_min_output_cost_usd: 12
`)
	require.NoError(t, err)

	p := stage.GetLevelingPolicy()
	require.NotNil(t, p.MaxEscalations, "2.0 is a whole number, so the field is SET")
	assert.Equal(t, int32(2), p.GetMaxEscalations())
	assert.InDelta(t, 1.0, p.GetMaxCostUsd(), 1e-9)
	assert.InDelta(t, 12.0, p.GetFrontierMinOutputCostUsd(), 1e-9)
}

// TestWorkflowYAMLRetrySessionModeBlankRejected is parseRetrySessionMode's
// blank-name gate: whitespace is not "leave it out", it is a value that names no
// mode, and silently defaulting it would hide a typo.
func TestWorkflowYAMLRetrySessionModeBlankRejected(t *testing.T) {
	t.Parallel()

	_, err := loadLevelingStage(t, "      retry_policy:\n        max_retries: 1\n        session_mode: \"   \"\n")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "is not a known retry session mode")
	assert.Contains(t, err.Error(), "continue, fresh, escalate",
		"the valid-values list is derived from the generated enum, in enum order")
}

// TestYAMLInt32FieldAcceptedTypes pins yamlInt32Field's type tolerance. The
// tolerance is the contract — the helper is shared by every integer key on the
// leveling and retry surfaces, and different decoder paths hand it int, int32,
// int64 or float64 for the same YAML document — so each accepted type is
// asserted directly rather than through whichever one today's decoder happens
// to produce.
func TestYAMLInt32FieldAcceptedTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         interface{}
		wantValue   int32
		wantPresent bool
		wantErr     string
	}{
		{name: "int", raw: 3, wantValue: 3, wantPresent: true},
		{name: "int32", raw: int32(4), wantValue: 4, wantPresent: true},
		{name: "int64", raw: int64(5), wantValue: 5, wantPresent: true},
		{name: "whole float64", raw: float64(6), wantValue: 6, wantPresent: true},
		{name: "negative int", raw: -2, wantValue: -2, wantPresent: true},
		{name: "explicit nil is absent", raw: nil, wantValue: 0, wantPresent: false},
		{name: "fractional float64", raw: 1.5, wantErr: "must be a whole number"},
		{name: "string", raw: "many", wantErr: "must be an integer"},
		{name: "bool", raw: true, wantErr: "must be an integer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			value, present, err := yamlInt32Field(
				map[string]interface{}{"k": tt.raw}, "spec", "k")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Contains(t, err.Error(), "spec.k")
				assert.False(t, present)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantValue, value)
			assert.Equal(t, tt.wantPresent, present)
		})
	}

	t.Run("absent key", func(t *testing.T) {
		t.Parallel()
		value, present, err := yamlInt32Field(map[string]interface{}{}, "spec", "k")
		require.NoError(t, err)
		assert.Equal(t, int32(0), value)
		assert.False(t, present)
	})
}

// TestYAMLFloat64FieldAcceptedTypes is yamlInt32Field's twin: a cost written as
// `1`, `1.0` or via any integer width must mean the same number, because a
// float key silently rejecting `max_cost_usd: 1` would be a usability trap.
func TestYAMLFloat64FieldAcceptedTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         interface{}
		wantValue   float64
		wantPresent bool
		wantErr     string
	}{
		{name: "float64", raw: 0.5, wantValue: 0.5, wantPresent: true},
		{name: "float32", raw: float32(0.25), wantValue: 0.25, wantPresent: true},
		{name: "int", raw: 2, wantValue: 2, wantPresent: true},
		{name: "int32", raw: int32(3), wantValue: 3, wantPresent: true},
		{name: "int64", raw: int64(4), wantValue: 4, wantPresent: true},
		{name: "explicit nil is absent", raw: nil, wantValue: 0, wantPresent: false},
		{name: "string", raw: "cheap", wantErr: "must be a number"},
		{name: "bool", raw: false, wantErr: "must be a number"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			value, present, err := yamlFloat64Field(
				map[string]interface{}{"k": tt.raw}, "spec", "k")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Contains(t, err.Error(), "spec.k")
				assert.False(t, present)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tt.wantValue, value, 1e-9)
			assert.Equal(t, tt.wantPresent, present)
		})
	}
}

// TestParseLLMRoleNameBlankAndUnknown covers the two rejections that make the
// ladder error message trustworthy: a name that normalizes to nothing, and a
// name that is not an enum value.
func TestParseLLMRoleNameBlankAndUnknown(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "   ", "\t\n", "-", "_"} {
		role, ok := parseLLMRoleName(name)
		assert.False(t, ok, "%q must not resolve to a role", name)
		assert.Equal(t, loomv1.LLMRole_LLM_ROLE_UNSPECIFIED, role)
	}

	role, ok := parseLLMRoleName("wizard")
	assert.False(t, ok)
	assert.Equal(t, loomv1.LLMRole_LLM_ROLE_UNSPECIFIED, role)

	role, ok = parseLLMRoleName("judge")
	assert.True(t, ok)
	assert.Equal(t, loomv1.LLMRole_LLM_ROLE_JUDGE, role)
}

// TestParseRetrySessionModeBlankAndUnknown is parseLLMRoleName's twin.
func TestParseRetrySessionModeBlankAndUnknown(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "  ", "_", "unspecified", "teleport"} {
		mode, ok := parseRetrySessionMode(name)
		assert.False(t, ok, "%q must not resolve to a session mode", name)
		assert.Equal(t, loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED, mode)
	}

	mode, ok := parseRetrySessionMode("Continue")
	assert.True(t, ok)
	assert.Equal(t, loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE, mode)
}
