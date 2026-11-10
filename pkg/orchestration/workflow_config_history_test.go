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
)

const historyPipelineYAML = `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: history-pipeline
spec:
  type: pipeline
  initial_prompt: "go"
  stages:
    - agent_id: researcher
      prompt_template: "Research"
    - agent_id: presenter
      prompt_template: "Build the deck from: {{history}}"
`

func TestLoadWorkflowFromYAML_PipelineHistoryBlock(t *testing.T) {
	pattern, err := LoadWorkflowFromYAML(createTempYAMLFile(t, historyPipelineYAML+`
  history:
    max_entry_bytes: 4096
    max_input_tokens: 60000
    overflow_strategy: truncate
`))
	require.NoError(t, err)
	hp := pattern.GetPipeline().GetHistoryPolicy()
	require.NotNil(t, hp)
	assert.Equal(t, int32(4096), hp.MaxEntryBytes)
	assert.Equal(t, int32(60000), hp.MaxInputTokens)
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE, hp.OverflowStrategy)

	defaults, err := LoadWorkflowFromYAML(createTempYAMLFile(t, historyPipelineYAML+"  history: {}\n"))
	require.NoError(t, err)
	require.NotNil(t, defaults.GetPipeline().GetHistoryPolicy(), "an empty block enables the budget with defaults")
	assert.Zero(t, defaults.GetPipeline().GetHistoryPolicy().MaxInputTokens)

	none, err := LoadWorkflowFromYAML(createTempYAMLFile(t, historyPipelineYAML))
	require.NoError(t, err)
	assert.Nil(t, none.GetPipeline().GetHistoryPolicy())

	passFull, err := LoadWorkflowFromYAML(createTempYAMLFile(t, `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: pass-full
spec:
  type: pipeline
  initial_prompt: "go"
  pass_full_history: true
  history:
    overflow_strategy: fail
  stages:
    - agent_id: a
      prompt_template: "A"
    - agent_id: b
      prompt_template: "B"
`))
	require.NoError(t, err, "pass_full_history makes history reach the stages without a placeholder")
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL, passFull.GetPipeline().GetHistoryPolicy().OverflowStrategy)
}

func TestLoadWorkflowFromYAML_IterativePipelineHistoryBlock(t *testing.T) {
	pattern, err := LoadWorkflowFromYAML(createTempYAMLFile(t, `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: iterative-history
spec:
  type: iterative
  max_iterations: 2
  pipeline:
    initial_prompt: "go"
    history:
      max_input_tokens: 5000
    stages:
      - agent_id: a
        prompt_template: "A"
      - agent_id: b
        prompt_template: "B: {{history}}"
`))
	require.NoError(t, err)
	hp := pattern.GetIterative().GetPipeline().GetHistoryPolicy()
	require.NotNil(t, hp)
	assert.Equal(t, int32(5000), hp.MaxInputTokens)
}

func TestParseHistoryPolicy_Errors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"not an object", historyPipelineYAML + "  history: 5\n", "spec.history must be an object"},
		{"nil value", historyPipelineYAML + "  history:\n", "use `history: {}`"},
		{"unknown key", historyPipelineYAML + "  history:\n    max_branch_bytes: 1\n", "spec.history.max_branch_bytes is not a history field"},
		{"negative entry bytes", historyPipelineYAML + "  history:\n    max_entry_bytes: -1\n", "spec.history.max_entry_bytes must be >= 0"},
		{"negative tokens", historyPipelineYAML + "  history:\n    max_input_tokens: -5\n", "spec.history.max_input_tokens must be >= 0"},
		{"fractional tokens", historyPipelineYAML + "  history:\n    max_input_tokens: 10.5\n", "must be a whole number"},
		{"bad overflow", historyPipelineYAML + "  history:\n    overflow_strategy: explode\n", `unknown value "explode"`},
		{"no history in any stage", `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: dead-history
spec:
  type: pipeline
  initial_prompt: "go"
  history: {}
  stages:
    - agent_id: a
      prompt_template: "A"
    - agent_id: b
      prompt_template: "B: {{previous}}"
`, "spec.history has no effect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadWorkflowFromYAML(createTempYAMLFile(t, tt.yaml))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidWorkflow)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

const debateYAML = `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: bounded-debate
spec:
  type: debate
  topic: "Monolith or microservices?"
  agent_ids: [advocate, skeptic]
  rounds: 2
  moderator_agent_id: moderator
`

func TestLoadWorkflowFromYAML_DebateJoinBlock(t *testing.T) {
	pattern, err := LoadWorkflowFromYAML(createTempYAMLFile(t, debateYAML+`
  join:
    prompt_template: "Decide on {{prompt}}: {{branches}}"
    max_branch_bytes: 2048
    max_input_tokens: 40000
    overflow_strategy: pre_reduce
`))
	require.NoError(t, err)
	jp := pattern.GetDebate().GetJoinPolicy()
	require.NotNil(t, jp)
	assert.Empty(t, jp.AgentId)
	assert.Equal(t, "Decide on {{prompt}}: {{branches}}", jp.PromptTemplate)
	assert.Equal(t, int32(2048), jp.MaxBranchBytes)
	assert.Equal(t, int32(40000), jp.MaxInputTokens)
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE, jp.OverflowStrategy)

	overflowOnly, err := LoadWorkflowFromYAML(createTempYAMLFile(t, debateYAML+"  join:\n    overflow_strategy: truncate\n"))
	require.NoError(t, err, "the budget always derives from the receiving agent, so overflow_strategy alone can fire")
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE, overflowOnly.GetDebate().GetJoinPolicy().OverflowStrategy)

	_, err = LoadWorkflowFromYAML(createTempYAMLFile(t, debateYAML+"  join:\n    agent_id: someone\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.join.agent_id is not used by debate")
	assert.Contains(t, err.Error(), "moderator_agent_id is the join agent")

	_, err = LoadWorkflowFromYAML(createTempYAMLFile(t, debateYAML+"  join: {}\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.join sets nothing")

	_, err = LoadWorkflowFromYAML(createTempYAMLFile(t, debateYAML+"  join:\n    max_branch_bytes: -3\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.join.max_branch_bytes must be >= 0")
}
