// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

// A one-stage pipeline and a one-task parallel workflow, each with the leveling
// block appended by the loaders below. Bodies are indented to sit inside the
// stage/task list item.
const (
	levelingStageYAMLHeader = `apiVersion: loom/v1
kind: Workflow
metadata:
  name: leveling-stage-test
spec:
  type: pipeline
  initial_prompt: go
  stages:
    - agent_id: worker
      prompt_template: "{{previous}}"
`

	levelingTaskYAMLHeader = `apiVersion: loom/v1
kind: Workflow
metadata:
  name: leveling-task-test
spec:
  type: parallel
  tasks:
    - agent_id: worker
      prompt: go
`
)

// loadLevelingStage appends body to a minimal pipeline document and returns the
// single stage it produces.
func loadLevelingStage(t *testing.T, body string) (*loomv1.PipelineStage, error) {
	t.Helper()
	pattern, err := LoadWorkflowFromYAMLBytes([]byte(levelingStageYAMLHeader + body))
	if err != nil {
		return nil, err
	}
	stages := pattern.GetPipeline().GetStages()
	require.Len(t, stages, 1)
	return stages[0], nil
}

// loadLevelingTask is loadLevelingStage's parallel-pattern twin.
func loadLevelingTask(t *testing.T, body string) (*loomv1.AgentTask, error) {
	t.Helper()
	pattern, err := LoadWorkflowFromYAMLBytes([]byte(levelingTaskYAMLHeader + body))
	if err != nil {
		return nil, err
	}
	tasks := pattern.GetParallel().GetTasks()
	require.Len(t, tasks, 1)
	return tasks[0], nil
}

// TestWorkflowYAMLLevelingAbsentBlockYieldsNilPolicy pins the off switch: with
// no leveling block the proto field must be nil, not an empty message. Both
// executors gate on GetLevelingPolicy().GetEnabled(), and nil is what "the
// feature does not exist for this stage/task" looks like on the wire.
func TestWorkflowYAMLLevelingAbsentBlockYieldsNilPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "no leveling key at all", body: ""},
		{name: "explicitly null leveling block", body: "      leveling:\n"},
		{name: "explicitly null leveling_policy alias", body: "      leveling_policy:\n"},
		{name: "unrelated keys only", body: "      output_schema: '{}'\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stage, err := loadLevelingStage(t, tt.body)
			require.NoError(t, err)
			assert.Nil(t, stage.LevelingPolicy, "stage leveling policy must stay nil")
			assert.False(t, stage.GetLevelingPolicy().GetEnabled(), "the executor gate stays closed")

			task, err := loadLevelingTask(t, tt.body)
			require.NoError(t, err)
			assert.Nil(t, task.LevelingPolicy, "task leveling policy must stay nil")
			assert.False(t, task.GetLevelingPolicy().GetEnabled(), "the executor gate stays closed")
		})
	}
}

// TestWorkflowYAMLLevelingFullRoundTrip parses every field the block supports,
// on both carriers.
func TestWorkflowYAMLLevelingFullRoundTrip(t *testing.T) {
	t.Parallel()

	const body = `      leveling:
        enabled: true
        short_circuit_mid: false
        max_escalations: 2
        max_cost_usd: 0.50
        frontier_min_output_cost_usd: 10.0
        mid_min_output_cost_usd: 1.5
        ladder:
          - provider: ollama
            model: deepseek-r1:latest
          - role: LLM_ROLE_ORCHESTRATOR
        tier_policies:
          local:
            retry_budget: 2
            aggressive_coercion: true
          small-open:
            retry_budget: 1
`

	assertFull := func(t *testing.T, p *loomv1.LevelingPolicy) {
		t.Helper()
		require.NotNil(t, p)
		assert.True(t, p.GetEnabled())

		require.NotNil(t, p.ShortCircuitMid, "explicit short_circuit_mid must be SET")
		assert.False(t, p.GetShortCircuitMid())
		require.NotNil(t, p.MaxEscalations, "explicit max_escalations must be SET")
		assert.Equal(t, int32(2), p.GetMaxEscalations())

		assert.InDelta(t, 0.50, p.GetMaxCostUsd(), 1e-9)
		assert.InDelta(t, 10.0, p.GetFrontierMinOutputCostUsd(), 1e-9)
		assert.InDelta(t, 1.5, p.GetMidMinOutputCostUsd(), 1e-9)

		require.Len(t, p.GetLadder(), 2)
		assert.Equal(t, "ollama", p.GetLadder()[0].GetProvider())
		assert.Equal(t, "deepseek-r1:latest", p.GetLadder()[0].GetModel())
		assert.Equal(t, loomv1.LLMRole_LLM_ROLE_UNSPECIFIED, p.GetLadder()[0].GetRole())
		assert.Equal(t, loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR, p.GetLadder()[1].GetRole())
		assert.Empty(t, p.GetLadder()[1].GetProvider())

		require.Len(t, p.GetTierPolicies(), 2)
		local := p.GetTierPolicies()["local"]
		require.NotNil(t, local)
		assert.Equal(t, int32(2), local.GetRetryBudget())
		assert.True(t, local.GetAggressiveCoercion())
		smallOpen := p.GetTierPolicies()["small-open"]
		require.NotNil(t, smallOpen)
		assert.Equal(t, int32(1), smallOpen.GetRetryBudget())
		assert.False(t, smallOpen.GetAggressiveCoercion())

		// The whole point of the surface: what it produces must convert.
		goPolicy, err := LevelingPolicyFromProto(p)
		require.NoError(t, err)
		require.NotNil(t, goPolicy)
		assert.True(t, goPolicy.Enabled)
		assert.False(t, goPolicy.ShortCircuitMid)
		assert.Equal(t, 2, goPolicy.MaxEscalations)
	}

	t.Run("pipeline stage", func(t *testing.T) {
		t.Parallel()
		stage, err := loadLevelingStage(t, body)
		require.NoError(t, err)
		assertFull(t, stage.GetLevelingPolicy())
	})

	t.Run("parallel task", func(t *testing.T) {
		t.Parallel()
		task, err := loadLevelingTask(t, body)
		require.NoError(t, err)
		assertFull(t, task.GetLevelingPolicy())
	})

	t.Run("leveling_policy alias parses identically", func(t *testing.T) {
		t.Parallel()
		stage, err := loadLevelingStage(t, strings.Replace(body, "leveling:", "leveling_policy:", 1))
		require.NoError(t, err)
		assertFull(t, stage.GetLevelingPolicy())
	})
}

// TestWorkflowYAMLLevelingOptionalFieldsUnsetVsExplicitZero is the reason
// short_circuit_mid and max_escalations are proto3 `optional`: their Go defaults
// are true and 1, so an absent YAML key must leave the proto field UNSET (nil
// pointer) rather than write a proto3 zero value that would invert them.
func TestWorkflowYAMLLevelingOptionalFieldsUnsetVsExplicitZero(t *testing.T) {
	t.Parallel()

	t.Run("absent keys leave both fields UNSET and defaults apply", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t, "      leveling:\n        enabled: true\n")
		require.NoError(t, err)

		p := stage.GetLevelingPolicy()
		require.NotNil(t, p)
		assert.Nil(t, p.ShortCircuitMid, "absent short_circuit_mid must be a nil pointer, not false")
		assert.Nil(t, p.MaxEscalations, "absent max_escalations must be a nil pointer, not 0")

		goPolicy, err := LevelingPolicyFromProto(p)
		require.NoError(t, err)
		assert.True(t, goPolicy.ShortCircuitMid, "UNSET short_circuit_mid means the true default")
		assert.Equal(t, 1, goPolicy.MaxEscalations, "UNSET max_escalations means the 1 default")
	})

	t.Run("explicit false and zero are SET and override the defaults", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t,
			"      leveling:\n        enabled: true\n        short_circuit_mid: false\n        max_escalations: 0\n")
		require.NoError(t, err)

		p := stage.GetLevelingPolicy()
		require.NotNil(t, p)
		require.NotNil(t, p.ShortCircuitMid, "explicit false must be SET, not UNSET")
		assert.False(t, p.GetShortCircuitMid())
		require.NotNil(t, p.MaxEscalations, "explicit 0 must be SET, not UNSET")
		assert.Equal(t, int32(0), p.GetMaxEscalations())

		goPolicy, err := LevelingPolicyFromProto(p)
		require.NoError(t, err)
		assert.False(t, goPolicy.ShortCircuitMid)
		assert.Equal(t, 0, goPolicy.MaxEscalations,
			"explicit 0 disables escalation while keeping per-tier retry/coercion")
	})

	t.Run("explicit true and one are SET and match the defaults", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t,
			"      leveling:\n        enabled: true\n        short_circuit_mid: true\n        max_escalations: 1\n")
		require.NoError(t, err)

		p := stage.GetLevelingPolicy()
		require.NotNil(t, p.ShortCircuitMid)
		assert.True(t, p.GetShortCircuitMid())
		require.NotNil(t, p.MaxEscalations)
		assert.Equal(t, int32(1), p.GetMaxEscalations())
	})
}

// TestWorkflowYAMLLevelingDisabledBlockLoadsAndStaysInert covers two things at
// once: an `enabled: false` block loads without any other field being validated
// (matching LevelingPolicyFromProto's rule that a never-enabled policy can never
// fail conversion), and the stage it produces is inert in the executor — the
// schema its output violates is not enforced.
func TestWorkflowYAMLLevelingDisabledBlockLoadsAndStaysInert(t *testing.T) {
	t.Parallel()

	// Deliberately hostile: a bad tier key, a negative bound and an
	// unresolvable rung. None of it may be read while enabled is false.
	const body = `      output_policy:
        output_schema: '{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}'
      leveling:
        enabled: false
        max_escalations: -1
        max_cost_usd: -1.0
        ladder:
          - provider: not-in-any-pool
        tier_policies:
          NOT-A-TIER:
            retry_budget: -5
`

	stage, err := loadLevelingStage(t, body)
	require.NoError(t, err, "a disabled leveling block must load even when the rest is nonsense")

	p := stage.GetLevelingPolicy()
	require.NotNil(t, p, "the block was present, so the field is set")
	assert.False(t, p.GetEnabled(), "the executor gate stays closed")
	require.NotNil(t, stage.GetOutputPolicy(), "output_policy parsed but must stay unenforced")

	orch := newLevelingTestOrchestrator(t)
	llm := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
	escalation := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.9, lvlValidJSON)
	registerLevelingAgent(t, orch, "worker", llm,
		map[string]agent.LLMProvider{"not-in-any-pool": escalation})

	result, err := runLevelingPipeline(t, orch, stage)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, lvlInvalidJSON, result.MergedOutput, "output passes through unvalidated")
	assert.Equal(t, 1, llm.count(), "exactly one agent call: no validation, no retry")
	assert.Equal(t, 0, escalation.count(), "no ladder rung is reachable")
	assert.NotContains(t, result.Metadata, "validation_warnings", "nothing was validated")
}

// TestWorkflowYAMLLevelingErrors covers the load-time diagnostics. Type errors
// always fire; semantic errors fire only for an enabled policy, which is where
// they are routed through LevelingPolicyFromProto and the ladder shape check
// rather than re-implemented.
func TestWorkflowYAMLLevelingErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains []string
	}{
		{
			name:     "enabled missing",
			body:     "      leveling:\n        max_escalations: 2\n",
			contains: []string{"spec.stages[0].leveling.enabled is required", "enabled: true"},
		},
		{
			name:     "enabled not a boolean",
			body:     "      leveling:\n        enabled: yes-please\n",
			contains: []string{"spec.stages[0].leveling.enabled must be a boolean", "string"},
		},
		{
			name:     "leveling block is not an object",
			body:     "      leveling: true\n",
			contains: []string{"spec.stages[0].leveling must be an object"},
		},
		{
			name:     "both keys set",
			body:     "      leveling:\n        enabled: true\n      leveling_policy:\n        enabled: true\n",
			contains: []string{"sets both 'leveling' and 'leveling_policy'"},
		},
		{
			name:     "max_escalations not an integer",
			body:     "      leveling:\n        enabled: true\n        max_escalations: lots\n",
			contains: []string{"spec.stages[0].leveling.max_escalations must be an integer"},
		},
		{
			name:     "max_escalations fractional",
			body:     "      leveling:\n        enabled: true\n        max_escalations: 1.5\n",
			contains: []string{"must be a whole number"},
		},
		{
			name:     "max_cost_usd not a number",
			body:     "      leveling:\n        enabled: true\n        max_cost_usd: cheap\n",
			contains: []string{"spec.stages[0].leveling.max_cost_usd must be a number"},
		},
		{
			name:     "negative max_escalations",
			body:     "      leveling:\n        enabled: true\n        max_escalations: -1\n",
			contains: []string{"max_escalations must be >= 0"},
		},
		{
			name:     "negative max_cost_usd",
			body:     "      leveling:\n        enabled: true\n        max_cost_usd: -0.5\n",
			contains: []string{"max_cost_usd must be >= 0"},
		},
		{
			name: "unknown tier key",
			body: "      leveling:\n        enabled: true\n        tier_policies:\n          teeny:\n            retry_budget: 1\n",
			contains: []string{
				`unknown tier name "teeny"`,
				"unknown, local, small-open, mid, frontier",
			},
		},
		{
			name:     "tier policies not an object",
			body:     "      leveling:\n        enabled: true\n        tier_policies: local\n",
			contains: []string{"tier_policies must be an object keyed by tier name"},
		},
		{
			name:     "negative retry budget",
			body:     "      leveling:\n        enabled: true\n        tier_policies:\n          local:\n            retry_budget: -2\n",
			contains: []string{`tier "local" retry_budget must be >= 0`},
		},
		{
			name: "removed scaffolding_depth key is rejected, not ignored",
			body: "      leveling:\n        enabled: true\n        tier_policies:\n          local:\n            scaffolding_depth: 3\n",
			contains: []string{
				"spec.stages[0].leveling.tier_policies[local].scaffolding_depth was removed",
				"C2 capability-adaptive scaffolding was rejected on measurement",
				"docs/plan-capability-leveling.md",
			},
		},
		{
			name:     "ladder is not a list",
			body:     "      leveling:\n        enabled: true\n        ladder: ollama\n",
			contains: []string{"spec.stages[0].leveling.ladder must be a list of rungs"},
		},
		{
			name:     "rung is not an object",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - ollama\n",
			contains: []string{"spec.stages[0].leveling.ladder[0] must be an object"},
		},
		{
			name:     "rung with neither role nor provider",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - model: deepseek-r1:latest\n",
			contains: []string{"rung 1 needs role or provider"},
		},
		{
			name:     "unknown role",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - role: wizard\n",
			contains: []string{`role "wizard" is not a known LLM role`, "orchestrator", "LLM_ROLE_ORCHESTRATOR"},
		},
		{
			name:     "explicit unspecified role is not a rung",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - role: unspecified\n",
			contains: []string{"is not a known LLM role"},
		},
		{
			name:     "provider not a string",
			body:     "      leveling:\n        enabled: true\n        ladder:\n          - provider: 7\n",
			contains: []string{"spec.stages[0].leveling.ladder[0].provider must be a string"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadLevelingStage(t, tt.body)
			require.Error(t, err, "the load must fail, not silently ignore the block")
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

// TestWorkflowYAMLLevelingSemanticErrorsSkippedWhenDisabled pins the asymmetry:
// the same bodies that fail above load cleanly with enabled: false, because
// LevelingPolicyFromProto never validates a policy that was never enabled.
func TestWorkflowYAMLLevelingSemanticErrorsSkippedWhenDisabled(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"negative max_escalations": "      leveling:\n        enabled: false\n        max_escalations: -1\n",
		"unknown tier key":         "      leveling:\n        enabled: false\n        tier_policies:\n          teeny: {}\n",
		"rung with neither":        "      leveling:\n        enabled: false\n        ladder:\n          - model: m\n",
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stage, err := loadLevelingStage(t, body)
			require.NoError(t, err)
			assert.False(t, stage.GetLevelingPolicy().GetEnabled())
		})
	}
}

// TestWorkflowYAMLLevelingTypeErrorsFireEvenWhenDisabled is the other half of
// the asymmetry: a body that is not shaped like the schema at all is a load
// error regardless of enabled, because there is nothing to store.
func TestWorkflowYAMLLevelingTypeErrorsFireEvenWhenDisabled(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"ladder not a list":      "      leveling:\n        enabled: false\n        ladder: ollama\n",
		"max_escalations a word": "      leveling:\n        enabled: false\n        max_escalations: lots\n",
		"unknown role":           "      leveling:\n        enabled: false\n        ladder:\n          - role: wizard\n",
		// The removed knob belongs on this side of the asymmetry: the key is not
		// in the schema at any enabled state, so a disabled block cannot store
		// it either. Ignoring it quietly is what removing the knob prevents.
		"removed scaffolding_depth": "      leveling:\n        enabled: false\n        tier_policies:\n          local:\n            scaffolding_depth: 3\n",
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := loadLevelingStage(t, body)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidWorkflow)
		})
	}
}

// TestWorkflowYAMLLevelingRoleForms proves the short form and the full enum name
// both resolve, in any case and with '-' for '_'.
func TestWorkflowYAMLLevelingRoleForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		yamlValue string
		want      loomv1.LLMRole
	}{
		{yamlValue: "orchestrator", want: loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR},
		{yamlValue: "LLM_ROLE_ORCHESTRATOR", want: loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR},
		{yamlValue: "llm_role_orchestrator", want: loomv1.LLMRole_LLM_ROLE_ORCHESTRATOR},
		{yamlValue: "Judge", want: loomv1.LLMRole_LLM_ROLE_JUDGE},
		{yamlValue: "agent", want: loomv1.LLMRole_LLM_ROLE_AGENT},
		{yamlValue: "classifier", want: loomv1.LLMRole_LLM_ROLE_CLASSIFIER},
		{yamlValue: "compressor", want: loomv1.LLMRole_LLM_ROLE_COMPRESSOR},
		{yamlValue: "llm-role-compressor", want: loomv1.LLMRole_LLM_ROLE_COMPRESSOR},
	}

	for _, tt := range tests {
		t.Run(tt.yamlValue, func(t *testing.T) {
			t.Parallel()

			stage, err := loadLevelingStage(t,
				"      leveling:\n        enabled: true\n        ladder:\n          - role: "+tt.yamlValue+"\n")
			require.NoError(t, err)

			ladder := stage.GetLevelingPolicy().GetLadder()
			require.Len(t, ladder, 1)
			assert.Equal(t, tt.want, ladder[0].GetRole())
		})
	}
}

// TestLoadWorkflowFromYAML_LevelingReachesExecutorGate is the end-to-end check:
// a leveling block written in YAML must reach
// GetLevelingPolicy().GetEnabled() in the executors and drive a real
// escalation, not merely deserialize.
func TestLoadWorkflowFromYAML_LevelingReachesExecutorGate(t *testing.T) {
	t.Parallel()

	t.Run("pipeline stage from testdata file", func(t *testing.T) {
		t.Parallel()

		pattern, err := LoadWorkflowFromYAML(filepath.Join("testdata", "leveling-pipeline.yaml"))
		require.NoError(t, err)

		stages := pattern.GetPipeline().GetStages()
		require.Len(t, stages, 1)
		stage := stages[0]
		require.True(t, stage.GetLevelingPolicy().GetEnabled(), "the executor gate must be open")
		require.NotNil(t, stage.GetOutputPolicy(), "leveling needs the schema contract to escalate on")

		orch := newLevelingTestOrchestrator(t)
		// A local-tier primary that never satisfies the schema, and a frontier
		// rung resolved from the provider pool by the YAML-declared provider.
		primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
		rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.01, lvlValidJSON)
		registerLevelingAgent(t, orch, "worker", primary,
			map[string]agent.LLMProvider{lvlFrontierProvider: rung})

		result, err := runLevelingPipeline(t, orch, stage)
		require.NoError(t, err)
		require.NotNil(t, result)

		assert.Equal(t, lvlValidJSON, result.MergedOutput, "the escalation rung's output wins")
		assert.Equal(t, 1, primary.count(), "retry_budget: 0 from YAML means one primary attempt")
		assert.Equal(t, 1, rung.count(), "the YAML-declared ladder rung ran")
		require.Len(t, result.AgentResults, 1)
		assert.Equal(t, lvlFrontierModel, result.AgentResults[0].Metadata[levelingRungModelKey],
			"the result is labeled with the rung that produced it")
	})

	t.Run("parallel task", func(t *testing.T) {
		t.Parallel()

		task, err := loadLevelingTask(t, `      output_policy:
        output_schema: '{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}'
      leveling:
        enabled: true
        max_escalations: 1
        ladder:
          - provider: anthropic
            model: claude-opus-4-7
        tier_policies:
          local:
            retry_budget: 0
`)
		require.NoError(t, err)
		require.True(t, task.GetLevelingPolicy().GetEnabled())

		orch := newLevelingTestOrchestrator(t)
		primary := newLvlMockLLM(lvlLowProvider, lvlLowModel, 0, lvlInvalidJSON)
		rung := newLvlMockLLM(lvlFrontierProvider, lvlFrontierModel, 0.01, lvlValidJSON)
		registerLevelingAgent(t, orch, "worker", primary,
			map[string]agent.LLMProvider{lvlFrontierProvider: rung})

		result, err := runLevelingParallel(t, orch, task)
		require.NoError(t, err)
		require.Len(t, result.AgentResults, 1)
		assert.Equal(t, lvlValidJSON, result.AgentResults[0].Output)
		assert.Equal(t, 1, primary.count())
		assert.Equal(t, 1, rung.count(), "the YAML-declared ladder rung ran for the task")
	})
}

// TestWorkflowYAMLOutputPolicyBlock covers the unified output_policy block the
// leveling path consumes as its validation contract.
func TestWorkflowYAMLOutputPolicyBlock(t *testing.T) {
	t.Parallel()

	const body = `      output_policy:
        output_schema: '{"type":"object"}'
        acceptance_criteria: "must answer the question"
        validator_agent_id: reviewer
        judge_config_id: strict
        retry_policy:
          max_retries: 2
          session_mode: continue
          feedback_template: "attempt {{attempt}} failed: {{error}}"
          cooldown_ms: 250
`

	assertPolicy := func(t *testing.T, p *loomv1.OutputPolicy) {
		t.Helper()
		require.NotNil(t, p)
		assert.Equal(t, `{"type":"object"}`, p.GetOutputSchema())
		assert.Equal(t, "must answer the question", p.GetAcceptanceCriteria())
		assert.Equal(t, "reviewer", p.GetValidatorAgentId())
		assert.Equal(t, "strict", p.GetJudgeConfigId())
		require.NotNil(t, p.GetRetryPolicy())
		assert.Equal(t, int32(2), p.GetRetryPolicy().GetMaxRetries())
		assert.Equal(t, loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE, p.GetRetryPolicy().GetSessionMode())
		assert.Equal(t, "attempt {{attempt}} failed: {{error}}", p.GetRetryPolicy().GetFeedbackTemplate())
		assert.Equal(t, int32(250), p.GetRetryPolicy().GetCooldownMs())
		assert.True(t, p.GetRetryPolicy().GetIncludeValidValues(), "absent include_valid_values means true")
	}

	t.Run("pipeline stage", func(t *testing.T) {
		t.Parallel()
		stage, err := loadLevelingStage(t, body)
		require.NoError(t, err)
		assertPolicy(t, stage.GetOutputPolicy())
	})

	t.Run("parallel task", func(t *testing.T) {
		t.Parallel()
		task, err := loadLevelingTask(t, body)
		require.NoError(t, err)
		assertPolicy(t, task.GetOutputPolicy())
	})

	t.Run("absent block yields nil", func(t *testing.T) {
		t.Parallel()
		stage, err := loadLevelingStage(t, "")
		require.NoError(t, err)
		assert.Nil(t, stage.OutputPolicy)
	})

	t.Run("not an object", func(t *testing.T) {
		t.Parallel()
		_, err := loadLevelingStage(t, "      output_policy: strict\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "spec.stages[0].output_policy must be an object")
	})
}

// TestWorkflowYAMLRetryPolicyExtendedFields covers the fields
// parseOutputRetryPolicy gained: session_mode, feedback_template and
// cooldown_ms, which the pre-existing surface could not express.
func TestWorkflowYAMLRetryPolicyExtendedFields(t *testing.T) {
	t.Parallel()

	t.Run("stage-level retry_policy carries the new fields", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t, `      output_schema: '{"type":"object"}'
      retry_policy:
        max_retries: 3
        include_valid_values: false
        session_mode: RETRY_SESSION_MODE_ESCALATE
        feedback_template: "fix it: {{error}}"
        cooldown_ms: 100
`)
		require.NoError(t, err)

		rp := stage.GetRetryPolicy()
		require.NotNil(t, rp)
		assert.Equal(t, int32(3), rp.GetMaxRetries())
		assert.False(t, rp.GetIncludeValidValues())
		assert.Equal(t, loomv1.RetrySessionMode_RETRY_SESSION_MODE_ESCALATE, rp.GetSessionMode())
		assert.Equal(t, "fix it: {{error}}", rp.GetFeedbackTemplate())
		assert.Equal(t, int32(100), rp.GetCooldownMs())
	})

	t.Run("session_mode short forms", func(t *testing.T) {
		t.Parallel()

		for value, want := range map[string]loomv1.RetrySessionMode{
			"fresh":    loomv1.RetrySessionMode_RETRY_SESSION_MODE_FRESH,
			"continue": loomv1.RetrySessionMode_RETRY_SESSION_MODE_CONTINUE,
			"escalate": loomv1.RetrySessionMode_RETRY_SESSION_MODE_ESCALATE,
		} {
			stage, err := loadLevelingStage(t,
				"      retry_policy:\n        max_retries: 1\n        session_mode: "+value+"\n")
			require.NoError(t, err, value)
			assert.Equal(t, want, stage.GetRetryPolicy().GetSessionMode(), value)
		}
	})

	t.Run("absent session_mode stays UNSPECIFIED", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t, "      retry_policy:\n        max_retries: 1\n")
		require.NoError(t, err)
		assert.Equal(t, loomv1.RetrySessionMode_RETRY_SESSION_MODE_UNSPECIFIED,
			stage.GetRetryPolicy().GetSessionMode(), "absent means the FRESH-compatible default")
	})

	t.Run("errors", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name     string
			body     string
			contains string
		}{
			{
				name:     "unknown session mode",
				body:     "      retry_policy:\n        max_retries: 1\n        session_mode: teleport\n",
				contains: `session_mode "teleport" is not a known retry session mode`,
			},
			{
				name:     "negative cooldown",
				body:     "      retry_policy:\n        max_retries: 1\n        cooldown_ms: -5\n",
				contains: "cooldown_ms must be >= 0",
			},
			{
				name:     "retry-only field without retries",
				body:     "      retry_policy:\n        feedback_template: \"fix it\"\n",
				contains: "feedback_template needs max_retries >= 1",
			},
			{
				name:     "max_retries not an integer",
				body:     "      retry_policy:\n        max_retries: many\n",
				contains: "max_retries must be an integer",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				_, err := loadLevelingStage(t, tt.body)
				require.Error(t, err)
				require.ErrorIs(t, err, ErrInvalidWorkflow)
				assert.Contains(t, err.Error(), tt.contains)
			})
		}
	})

	t.Run("zero max_retries still means no policy", func(t *testing.T) {
		t.Parallel()

		stage, err := loadLevelingStage(t, "      retry_policy:\n        max_retries: 0\n")
		require.NoError(t, err)
		assert.Nil(t, stage.RetryPolicy, "0 retries is the same as no retry policy, as before")
	})
}
