// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/types"
)

// YAML keys accepted for the capability-leveling block on a pipeline stage or a
// parallel task. levelingBlockKey is canonical; levelingBlockAliasKey is the
// proto field name, accepted because every other block in this loader
// (retry_policy, hitl_gate, output_schema) is keyed on its proto field name and
// a silently ignored key is worse than a second spelling.
const (
	levelingBlockKey      = "leveling"
	levelingBlockAliasKey = "leveling_policy"
)

// llmRoleEnumPrefix is the generated enum's value prefix, stripped when
// accepting the short form of a role name and re-added when parsing one.
const llmRoleEnumPrefix = "LLM_ROLE_"

// retrySessionModeEnumPrefix is the RetrySessionMode equivalent of
// llmRoleEnumPrefix.
const retrySessionModeEnumPrefix = "RETRY_SESSION_MODE_"

// parseLevelingPolicy parses an optional capability-leveling block from a
// pipeline stage or parallel task map. path is the YAML location of the
// enclosing stage/task (e.g. "spec.stages[0]") and is used only for errors.
//
// # Absence is the off switch
//
// No leveling block (and an explicitly null one) returns (nil, nil), so the
// proto field stays nil rather than becoming an empty message. Both executors
// gate on leveling_policy.enabled, so nil and disabled behave identically, but
// nil is what "the feature does not exist for this stage" looks like on the
// wire and in storage.
//
// # enabled is required when the block is present
//
// A present block must say what it wants. Defaulting a missing `enabled` to
// false would parse a full, carefully written leveling block into something
// that does nothing, with no diagnostic. `enabled: false` is accepted and
// inert.
//
// # Optional proto fields stay unset when the YAML key is absent
//
// short_circuit_mid and max_escalations are proto3 `optional` because their Go
// defaults are true and 1: writing a proto3 zero value for an absent YAML key
// would invert them. They are only set when the key is present, so
// LevelingPolicyFromProto sees UNSET and applies the documented default.
//
// # Where validation fires
//
// Type errors (wrong YAML shape or scalar type) always fail the load. Semantic
// errors — negative bounds, unknown tier names, a rung naming neither a role
// nor a provider — fail the load only when enabled is true, by running the
// freshly built proto through LevelingPolicyFromProto and the ladder-shape
// check the executors use. That reuses the executors' rules and messages
// instead of duplicating them, and it mirrors LevelingPolicyFromProto's own
// rule that a policy which was never enabled can never fail conversion.
//
// YAML shape:
//
//	leveling:
//	  enabled: true                        # required when the block is present
//	  short_circuit_mid: true              # optional; absent = UNSET (true)
//	  max_escalations: 2                   # optional; absent = UNSET (1)
//	  max_cost_usd: 0.50                   # optional; 0 = no ceiling
//	  frontier_min_output_cost_usd: 10.0   # optional; 0 = built-in default
//	  mid_min_output_cost_usd: 1.5         # optional; 0 = built-in default
//	  ladder:                              # optional
//	    - provider: ollama                 # resolved from the agent's provider pool
//	      model: deepseek-r1:latest
//	    - role: orchestrator               # or LLM_ROLE_ORCHESTRATOR
//	  tier_policies:                       # optional; keys: unknown, local,
//	    local:                             # small-open, mid, frontier
//	      retry_budget: 2
//	      aggressive_coercion: true
//	      scaffolding_depth: 0             # carried, consumed by nothing yet
func parseLevelingPolicy(enclosing map[string]interface{}, path string) (*loomv1.LevelingPolicy, error) {
	raw, key, err := levelingBlockValue(enclosing, path)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	path = path + "." + key

	block, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be an object, got %T", path, raw)
	}

	enabled, present, err := yamlBoolField(block, path, "enabled")
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("%s.enabled is required: set 'enabled: true' to turn capability leveling on for this stage, or remove the '%s' block entirely to leave it off", path, key)
	}

	policy := &loomv1.LevelingPolicy{Enabled: enabled}

	shortCircuit, present, err := yamlBoolField(block, path, "short_circuit_mid")
	if err != nil {
		return nil, err
	}
	if present {
		policy.ShortCircuitMid = proto.Bool(shortCircuit)
	}

	maxEscalations, present, err := yamlInt32Field(block, path, "max_escalations")
	if err != nil {
		return nil, err
	}
	if present {
		policy.MaxEscalations = proto.Int32(maxEscalations)
	}

	if policy.MaxCostUsd, _, err = yamlFloat64Field(block, path, "max_cost_usd"); err != nil {
		return nil, err
	}
	if policy.FrontierMinOutputCostUsd, _, err = yamlFloat64Field(block, path, "frontier_min_output_cost_usd"); err != nil {
		return nil, err
	}
	if policy.MidMinOutputCostUsd, _, err = yamlFloat64Field(block, path, "mid_min_output_cost_usd"); err != nil {
		return nil, err
	}

	if policy.Ladder, err = parseLevelingLadder(block, path); err != nil {
		return nil, err
	}
	if policy.TierPolicies, err = parseLevelingTierPolicies(block, path); err != nil {
		return nil, err
	}

	if policy.GetEnabled() {
		// The executors' own rules, applied at load time. Discarding the result
		// is deliberate: this call is a validation gate, and the executors
		// convert again with the agent in hand.
		if _, err := LevelingPolicyFromProto(policy); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := validateLevelingLadderShape(policy.GetLadder()); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}

	return policy, nil
}

// levelingBlockValue returns the raw leveling block, the key it was found
// under, and an error if both accepted keys are present. A missing or null
// block returns a nil value with a nil error.
func levelingBlockValue(enclosing map[string]interface{}, path string) (interface{}, string, error) {
	canonical, hasCanonical := enclosing[levelingBlockKey]
	alias, hasAlias := enclosing[levelingBlockAliasKey]

	switch {
	case hasCanonical && hasAlias:
		return nil, "", fmt.Errorf("%s sets both '%s' and '%s': keep one (they configure the same policy)",
			path, levelingBlockKey, levelingBlockAliasKey)
	case hasCanonical:
		return canonical, levelingBlockKey, nil
	case hasAlias:
		return alias, levelingBlockAliasKey, nil
	default:
		return nil, "", nil
	}
}

// parseLevelingLadder parses the optional escalation ladder. Rung resolution
// (provider pool / role lookup) needs an executing agent and stays in
// resolveLevelingLadder; only the shape is checked here.
func parseLevelingLadder(block map[string]interface{}, path string) ([]*loomv1.LevelingRung, error) {
	raw, ok := block["ladder"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s.ladder must be a list of rungs, got %T", path, raw)
	}

	rungs := make([]*loomv1.LevelingRung, 0, len(list))
	for i, item := range list {
		rungPath := fmt.Sprintf("%s.ladder[%d]", path, i)
		rungMap, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s must be an object with 'provider' (plus optional 'model') or 'role', got %T", rungPath, item)
		}

		rung := &loomv1.LevelingRung{}
		var err error
		if rung.Provider, _, err = yamlStringField(rungMap, rungPath, "provider"); err != nil {
			return nil, err
		}
		if rung.Model, _, err = yamlStringField(rungMap, rungPath, "model"); err != nil {
			return nil, err
		}

		roleName, present, err := yamlStringField(rungMap, rungPath, "role")
		if err != nil {
			return nil, err
		}
		if present {
			role, ok := parseLLMRoleName(roleName)
			if !ok {
				return nil, fmt.Errorf("%s.role %q is not a known LLM role (valid: %s; the full enum name such as LLM_ROLE_ORCHESTRATOR is also accepted)",
					rungPath, roleName, strings.Join(llmRoleShortNames(), ", "))
			}
			rung.Role = role
		}

		rungs = append(rungs, rung)
	}

	return rungs, nil
}

// parseLevelingTierPolicies parses the optional per-tier knob overrides. Tier
// names are not checked here: LevelingPolicyFromProto rejects unknown names and
// lists the valid ones, and routing the check through it keeps one authority.
func parseLevelingTierPolicies(block map[string]interface{}, path string) (map[string]*loomv1.LevelingTierPolicy, error) {
	raw, ok := block["tier_policies"]
	if !ok || raw == nil {
		return nil, nil
	}
	tiersRaw, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s.tier_policies must be an object keyed by tier name, got %T", path, raw)
	}

	tiers := make(map[string]*loomv1.LevelingTierPolicy, len(tiersRaw))
	for name, item := range tiersRaw {
		tierPath := fmt.Sprintf("%s.tier_policies[%s]", path, name)
		if item == nil {
			// An empty tier entry means "defaults for this tier".
			tiers[name] = &loomv1.LevelingTierPolicy{}
			continue
		}
		tierMap, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s must be an object, got %T", tierPath, item)
		}

		tier := &loomv1.LevelingTierPolicy{}
		var err error
		if tier.RetryBudget, _, err = yamlInt32Field(tierMap, tierPath, "retry_budget"); err != nil {
			return nil, err
		}
		if tier.AggressiveCoercion, _, err = yamlBoolField(tierMap, tierPath, "aggressive_coercion"); err != nil {
			return nil, err
		}
		if tier.ScaffoldingDepth, _, err = yamlInt32Field(tierMap, tierPath, "scaffolding_depth"); err != nil {
			return nil, err
		}
		tiers[name] = tier
	}

	return tiers, nil
}

// parseLLMRoleName resolves a YAML role name to an LLMRole. It accepts the
// generated enum name (LLM_ROLE_ORCHESTRATOR), the short form (orchestrator),
// and either in any case with '-' for '_'. LLM_ROLE_UNSPECIFIED is rejected:
// on a ladder rung it means "no role", which the rung shape check reports more
// usefully as "needs role or provider".
func parseLLMRoleName(name string) (loomv1.LLMRole, bool) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	if normalized == "" {
		return loomv1.LLMRole_LLM_ROLE_UNSPECIFIED, false
	}
	if !strings.HasPrefix(normalized, llmRoleEnumPrefix) {
		normalized = llmRoleEnumPrefix + normalized
	}
	value, ok := loomv1.LLMRole_value[normalized]
	if !ok || loomv1.LLMRole(value) == loomv1.LLMRole_LLM_ROLE_UNSPECIFIED {
		return loomv1.LLMRole_LLM_ROLE_UNSPECIFIED, false
	}
	return loomv1.LLMRole(value), true
}

// llmRoleShortNames lists the accepted short role forms in enum order, derived
// from the generated enum so adding an LLMRole needs no change here. Used only
// to build error messages.
func llmRoleShortNames() []string {
	return enumShortNames(loomv1.LLMRole_name, llmRoleEnumPrefix)
}

// parseRetrySessionMode resolves a YAML session_mode to a RetrySessionMode,
// accepting the same forms as parseLLMRoleName. UNSPECIFIED is rejected as an
// explicit value: leaving the key out is how you ask for the default.
func parseRetrySessionMode(name string) (loomv1.RetrySessionMode, bool) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	if normalized == "" {
		return loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED, false
	}
	if !strings.HasPrefix(normalized, retrySessionModeEnumPrefix) {
		normalized = retrySessionModeEnumPrefix + normalized
	}
	value, ok := loomv1.RetrySessionMode_value[normalized]
	if !ok || loomv1.RetrySessionMode(value) == loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED {
		return loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED, false
	}
	return loomv1.RetrySessionMode(value), true
}

// retrySessionModeShortNames is parseRetrySessionMode's error-message helper.
func retrySessionModeShortNames() []string {
	return enumShortNames(loomv1.RetrySessionMode_name, retrySessionModeEnumPrefix)
}

// enumShortNames lists a generated enum's values in numeric order, minus the
// zero value, lowercased and with the enum prefix stripped.
func enumShortNames(names map[int32]string, prefix string) []string {
	numbers := make([]int, 0, len(names))
	for number := range names {
		if number == 0 {
			continue
		}
		numbers = append(numbers, int(number))
	}
	sort.Ints(numbers)

	short := make([]string, 0, len(numbers))
	for _, number := range numbers {
		short = append(short, strings.ToLower(strings.TrimPrefix(names[int32(number)], prefix)))
	}
	return short
}

// yamlBoolField reads an optional bool. The second return reports whether the
// key was present with a non-null value, which is how a proto3 optional field
// stays UNSET for an absent key.
func yamlBoolField(m map[string]interface{}, path, key string) (bool, bool, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return false, false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, false, fmt.Errorf("%s.%s must be a boolean (true or false), got %T", path, key, raw)
	}
	return value, true, nil
}

// yamlStringField reads an optional string.
func yamlStringField(m map[string]interface{}, path, key string) (string, bool, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return "", false, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", false, fmt.Errorf("%s.%s must be a string, got %T", path, key, raw)
	}
	return value, true, nil
}

// yamlInt32Field reads an optional integer. A float that carries a fraction is
// rejected rather than truncated.
func yamlInt32Field(m map[string]interface{}, path, key string) (int32, bool, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch value := raw.(type) {
	case int:
		return types.SafeInt32(value), true, nil
	case int32:
		return value, true, nil
	case int64:
		return types.SafeInt32FromInt64(value), true, nil
	case float64:
		if value != math.Trunc(value) {
			return 0, false, fmt.Errorf("%s.%s must be a whole number, got %v", path, key, value)
		}
		return types.SafeInt32FromInt64(int64(value)), true, nil
	default:
		return 0, false, fmt.Errorf("%s.%s must be an integer, got %T", path, key, raw)
	}
}

// yamlFloat64Field reads an optional number, accepting integers written without
// a decimal point.
func yamlFloat64Field(m map[string]interface{}, path, key string) (float64, bool, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch value := raw.(type) {
	case float64:
		return value, true, nil
	case float32:
		return float64(value), true, nil
	case int:
		return float64(value), true, nil
	case int32:
		return float64(value), true, nil
	case int64:
		return float64(value), true, nil
	default:
		return 0, false, fmt.Errorf("%s.%s must be a number, got %T", path, key, raw)
	}
}
