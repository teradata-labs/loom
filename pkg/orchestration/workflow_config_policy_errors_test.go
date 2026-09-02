// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkflowYAMLOutputPolicyFieldTypeErrors covers every scalar gate inside
// the unified output_policy block, including the nested retry_policy: a wrong
// type anywhere in the block must fail the load with the full YAML path, so an
// operator is told which key is wrong rather than getting a policy with a
// silently missing field.
func TestWorkflowYAMLOutputPolicyFieldTypeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains []string
	}{
		{
			name:     "output_schema not a string",
			body:     "      output_policy:\n        output_schema: 7\n",
			contains: []string{"spec.stages[0].output_policy.output_schema must be a string", "int"},
		},
		{
			name:     "acceptance_criteria not a string",
			body:     "      output_policy:\n        acceptance_criteria: 7\n",
			contains: []string{"spec.stages[0].output_policy.acceptance_criteria must be a string"},
		},
		{
			name:     "validator_agent_id not a string",
			body:     "      output_policy:\n        validator_agent_id: true\n",
			contains: []string{"spec.stages[0].output_policy.validator_agent_id must be a string", "bool"},
		},
		{
			name:     "judge_config_id not a string",
			body:     "      output_policy:\n        judge_config_id: 1.5\n",
			contains: []string{"spec.stages[0].output_policy.judge_config_id must be a string"},
		},
		{
			name: "nested retry_policy field is wrong",
			body: "      output_policy:\n        retry_policy:\n          max_retries: true\n",
			contains: []string{
				"spec.stages[0].output_policy.retry_policy.max_retries must be an integer",
			},
		},
		{
			name: "nested retry_policy session_mode is unknown",
			body: "      output_policy:\n        retry_policy:\n          max_retries: 1\n          session_mode: teleport\n",
			contains: []string{
				"spec.stages[0].output_policy.retry_policy.session_mode",
				"is not a known retry session mode",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadLevelingStage(t, tt.body)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidWorkflow)
			for _, want := range tt.contains {
				assert.Contains(t, err.Error(), want)
			}

			// A parallel task carries the same block through its own call site,
			// which must report the task's YAML path.
			_, taskErr := loadLevelingTask(t, tt.body)
			require.Error(t, taskErr)
			require.ErrorIs(t, taskErr, ErrInvalidWorkflow)
			assert.Contains(t, taskErr.Error(), "spec.tasks[0].output_policy")
		})
	}
}

// TestWorkflowYAMLParallelTaskOutputPolicyNotAnObject is the parallel-side call
// site of parseOutputPolicy: a task-level block of the wrong shape must fail the
// load with the task's path rather than being dropped.
func TestWorkflowYAMLParallelTaskOutputPolicyNotAnObject(t *testing.T) {
	t.Parallel()

	_, err := loadLevelingTask(t, "      output_policy: strict\n")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "spec.tasks[0].output_policy must be an object")
}

// TestWorkflowYAMLRetryPolicyFieldTypeErrors covers the stage-level retry_policy
// scalar gates that the extended-fields test does not reach.
func TestWorkflowYAMLRetryPolicyFieldTypeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		contains []string
	}{
		{
			name:     "include_valid_values not a boolean",
			body:     "      retry_policy:\n        max_retries: 1\n        include_valid_values: maybe\n",
			contains: []string{"spec.stages[0].retry_policy.include_valid_values must be a boolean"},
		},
		{
			name:     "session_mode not a string",
			body:     "      retry_policy:\n        max_retries: 1\n        session_mode: 7\n",
			contains: []string{"spec.stages[0].retry_policy.session_mode must be a string", "int"},
		},
		{
			name:     "feedback_template not a string",
			body:     "      retry_policy:\n        max_retries: 1\n        feedback_template: 7\n",
			contains: []string{"spec.stages[0].retry_policy.feedback_template must be a string"},
		},
		{
			name:     "cooldown_ms not an integer",
			body:     "      retry_policy:\n        max_retries: 1\n        cooldown_ms: soon\n",
			contains: []string{"spec.stages[0].retry_policy.cooldown_ms must be an integer"},
		},
		{
			name:     "cooldown_ms fractional",
			body:     "      retry_policy:\n        max_retries: 1\n        cooldown_ms: 12.5\n",
			contains: []string{"spec.stages[0].retry_policy.cooldown_ms must be a whole number"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadLevelingStage(t, tt.body)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidWorkflow)
			for _, want := range tt.contains {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestWorkflowYAMLConditionalRetryPolicyError covers convertConditionalPattern's
// spec-level retry_policy call site: the conditional pattern parses the block at
// spec scope, so a malformed one must fail there too and say so.
func TestWorkflowYAMLConditionalRetryPolicyError(t *testing.T) {
	t.Parallel()

	_, err := LoadWorkflowFromYAMLBytes([]byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: conditional-bad-retry
spec:
  type: conditional
  condition_agent_id: classifier
  condition_prompt: "simple or complex?"
  retry_policy:
    max_retries: true
  branches:
    simple:
      type: fork-join
      prompt: "quick"
      agent_ids: [junior]
      merge_strategy: first
`))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "spec.retry_policy.max_retries must be an integer")
}

// TestWorkflowYAMLSwarmRetryPolicyError is the swarm-pattern twin.
func TestWorkflowYAMLSwarmRetryPolicyError(t *testing.T) {
	t.Parallel()

	_, err := LoadWorkflowFromYAMLBytes([]byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: swarm-bad-retry
spec:
  type: swarm
  question: "which database?"
  agent_ids: [voter1, voter2, voter3]
  strategy: majority
  retry_policy:
    max_retries: 1
    session_mode: teleport
`))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "spec.retry_policy.session_mode")
	assert.Contains(t, err.Error(), "is not a known retry session mode")
}
