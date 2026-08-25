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
	"github.com/teradata-labs/loom/pkg/llm/catalog"
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

// removedTierPolicyScaffoldingDepthKey is a tier knob that existed only while
// this feature was on its branch. It is rejected rather than ignored: the key
// had one intended consumer (C2 capability-adaptive scaffolding), that consumer
// was rejected on measurement, and a config the loader silently drops is the
// exact failure mode removing the knob was meant to prevent.
const removedTierPolicyScaffoldingDepthKey = "scaffolding_depth"

// removedTierPolicyAggressiveCoercionKey is the second tier knob removed on
// this branch, for the same reason and with the same treatment: free JSON
// extraction now runs on every schema-bearing path, so the key selected
// between "coerce" and "coerce" — and switching it off would have rejected
// payloads the pre-leveling pipeline accepted.
const removedTierPolicyAggressiveCoercionKey = "aggressive_coercion"

// levelingValidationPromptKey is the legacy per-stage semantic check that
// leveling cannot carry. It is a sibling of the leveling block rather than a key
// inside it, so it is read off the enclosing stage map.
const levelingValidationPromptKey = "validation_prompt"

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
// nor a provider, a validation_prompt on the same stage — fail the load only
// when enabled is true, by running the freshly built proto through
// LevelingPolicyFromProto and the ladder-shape check the executors use. That
// reuses the executors' rules and messages instead of duplicating them, and it
// mirrors LevelingPolicyFromProto's own rule that a policy which was never
// enabled can never fail conversion.
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
		if err := validateLevelingValidationPromptConflict(enclosing, path); err != nil {
			return nil, err
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

// validateLevelingValidationPromptConflict rejects a stage that asks for both
// enabled leveling and the legacy validation_prompt.
//
// PipelineExecutor.executeStageWithLeveling rejects the same pair and keeps
// doing so: a workflow submitted as raw proto over gRPC never passes through
// this loader, so the executor check remains the one that cannot be bypassed.
// This is the load-time half — a config surface that can see the conflict should
// not need a workflow run to report it — and its wording is kept in step with
// the executor's.
//
// The key is read with the same tolerant cast convertPipelinePattern uses for
// validation_prompt itself, so a non-string value is ignored here exactly as it
// is there rather than being reported as a leveling problem. Parallel tasks
// carry no validation_prompt, so the check is a no-op for them.
func validateLevelingValidationPromptConflict(enclosing map[string]interface{}, path string) error {
	prompt, _ := enclosing[levelingValidationPromptKey].(string)
	if prompt == "" {
		return nil
	}
	return fmt.Errorf("%s cannot be combined with the legacy %s — leveling has no semantic-prompt signal, so the criteria would be silently dropped; move the criteria into output_policy.output_schema or disable leveling on this stage",
		path, levelingValidationPromptKey)
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
//
// The removed aggressive_coercion and scaffolding_depth keys are rejected here,
// alongside the shape and scalar-type checks, rather than in the enabled-gated
// semantic pass: neither key exists in the schema at any enabled state, so —
// like a wrong YAML type — there is nothing to store either way.
//
// # A tier entry that overrides nothing means the built-in defaults
//
// `local:` (null) and `local: {}` both name a tier without asking for a
// different retry_budget, and both are filled with that tier's built-in values
// by defaultTierPolicyProto. Storing an all-zero policy instead would silently
// override the defaults the entry asks for — proto3 cannot tell an unset int32
// from 0, so local's built-in budget of 2 would arrive at the executor as 0.
// An explicit `retry_budget: 0` still means zero retries.
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
			// A null tier entry means "the built-in defaults for this tier".
			tiers[name] = defaultTierPolicyProto(name)
			continue
		}
		tierMap, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s must be an object, got %T", tierPath, item)
		}

		retryBudget, hasRetryBudget, err := yamlInt32Field(tierMap, tierPath, "retry_budget")
		if err != nil {
			return nil, err
		}
		if _, removed := tierMap[removedTierPolicyAggressiveCoercionKey]; removed {
			return nil, fmt.Errorf("%s.%s was removed: free JSON extraction now runs on every schema-bearing leveling path, so the knob gated nothing — remove the key (see docs/plan-capability-leveling.md)",
				tierPath, removedTierPolicyAggressiveCoercionKey)
		}
		if _, removed := tierMap[removedTierPolicyScaffoldingDepthKey]; removed {
			return nil, fmt.Errorf("%s.%s was removed: C2 capability-adaptive scaffolding was rejected on measurement (it made a weak model worse), so the knob is gone rather than dead — remove the key (see docs/plan-capability-leveling.md)",
				tierPath, removedTierPolicyScaffoldingDepthKey)
		}

		// An entry that names the tier without overriding retry_budget — `{}` —
		// is the same request as the null entry above.
		tier := defaultTierPolicyProto(name)
		if hasRetryBudget {
			tier.RetryBudget = retryBudget
		}
		tiers[name] = tier
	}

	return tiers, nil
}

// defaultTierPolicyProto is the proto form of a tier's built-in knobs, used for
// a tier entry that overrides nothing.
//
// An unresolvable tier name yields an empty policy rather than an error: name
// validation belongs to LevelingPolicyFromProto, which owns the message that
// lists the valid names and is reached from parseLevelingPolicy's enabled gate.
// catalog.ParseModelTier is the same resolver it uses, so the two cannot
// disagree about which names exist.
func defaultTierPolicyProto(name string) *loomv1.LevelingTierPolicy {
	tier, ok := catalog.ParseModelTier(name)
	if !ok {
		return &loomv1.LevelingTierPolicy{}
	}
	return &loomv1.LevelingTierPolicy{
		RetryBudget: types.SafeInt32(DefaultTierPolicies()[tier].RetryBudget),
	}
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
