// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/validation"
)

// TestLoadWorkflowAgents_WeaverFormat tests loading a weaver-generated workflow
func TestLoadWorkflowAgents_WeaverFormat(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)

	workflowPath := filepath.Join(homeDir, ".loom", "workflows", "dnd-game-master-workflow.yaml")

	// Skip if workflow file doesn't exist
	if _, err := os.Stat(workflowPath); os.IsNotExist(err) {
		t.Skip("Workflow file not found, skipping test")
	}

	// Create a mock LLM provider
	provider := &mockLLMProvider{}

	// Load workflow
	configs, err := LoadWorkflowAgents(workflowPath, provider)
	require.NoError(t, err, "Failed to load workflow")
	require.NotEmpty(t, configs, "No agent configs returned")

	t.Logf("Loaded %d agent configs from workflow", len(configs))

	// Check coordinator agent
	var coordinator *loomv1.AgentConfig
	var subAgents []*loomv1.AgentConfig

	for _, config := range configs {
		t.Logf("Agent: %s (role=%s)", config.Name, config.Metadata["role"])

		if config.Metadata["role"] == "coordinator" {
			coordinator = config
		} else {
			subAgents = append(subAgents, config)
		}
	}

	require.NotNil(t, coordinator, "No coordinator agent found")
	// Display name should match filename (without .yaml), not the name field
	assert.Equal(t, "dnd-game-master-workflow", coordinator.Name)
	assert.Equal(t, "coordinator", coordinator.Metadata["role"])
	assert.Equal(t, "workflow_coordinator", coordinator.Metadata["type"])

	// Check sub-agents are properly namespaced
	require.NotEmpty(t, subAgents, "No sub-agents found")

	for _, subAgent := range subAgents {
		// Sub-agents should be namespaced as {workflow}:{agent-name} using filename
		assert.Contains(t, subAgent.Name, "dnd-game-master-workflow:", "Sub-agent not properly namespaced")
		assert.Equal(t, "executor", subAgent.Metadata["role"])
		assert.Equal(t, "workflow_agent", subAgent.Metadata["type"])
		assert.Equal(t, "dnd-game-master-workflow", subAgent.Metadata["workflow"])
	}

	t.Logf("✓ Coordinator: %s", coordinator.Name)
	t.Logf("✓ Sub-agents (%d):", len(subAgents))
	for _, sa := range subAgents {
		t.Logf("  - %s", sa.Name)
	}
}

// TestLoadWorkflowAgents_OrchestrationFormat tests loading an orchestration-format workflow
func TestLoadWorkflowAgents_OrchestrationFormat(t *testing.T) {
	// Create a temporary orchestration workflow
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-debate-workflow
  description: Test debate workflow
spec:
  type: debate
  topic: Should we use microservices?
  agent_ids:
    - architect
    - engineer
  moderator_agent_id: senior-architect
  rounds: 3
  merge_strategy: consensus
`

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "test-workflow.yaml")
	err := os.WriteFile(workflowPath, []byte(workflowYAML), 0600)
	require.NoError(t, err)

	// Create mock provider
	provider := &mockLLMProvider{}

	// Load workflow
	configs, err := LoadWorkflowAgents(workflowPath, provider)
	require.NoError(t, err, "Failed to load orchestration workflow")
	require.NotEmpty(t, configs, "No agent configs returned")

	t.Logf("Loaded %d agent configs from orchestration workflow", len(configs))

	// Check coordinator
	var coordinator *loomv1.AgentConfig
	var subAgents []*loomv1.AgentConfig

	for _, config := range configs {
		t.Logf("Agent: %s (role=%s)", config.Name, config.Metadata["role"])

		if config.Metadata["role"] == "coordinator" {
			coordinator = config
		} else {
			subAgents = append(subAgents, config)
		}
	}

	require.NotNil(t, coordinator, "No coordinator found")
	assert.Equal(t, "test-debate-workflow", coordinator.Name)
	assert.Equal(t, "coordinator", coordinator.Metadata["role"])
	assert.Equal(t, "debate", coordinator.Metadata["pattern"])

	// Should have 3 sub-agents: architect, engineer, senior-architect
	require.Len(t, subAgents, 3, "Expected 3 sub-agents")

	// Check sub-agent namespacing
	expectedSubAgents := []string{
		"test-debate-workflow:architect",
		"test-debate-workflow:engineer",
		"test-debate-workflow:senior-architect",
	}

	for _, expected := range expectedSubAgents {
		found := false
		for _, config := range subAgents {
			if config.Name == expected {
				found = true
				assert.Equal(t, "executor", config.Metadata["role"])
				assert.Equal(t, "workflow_agent", config.Metadata["type"])
				break
			}
		}
		assert.True(t, found, "Expected sub-agent %s not found", expected)
	}

	t.Logf("✓ Coordinator: %s", coordinator.Name)
	t.Logf("✓ Sub-agents: %v", expectedSubAgents)
}

// TestLoadWorkflowAgents_ForkJoinPatternType verifies the registry loader accepts the
// canonical hyphenated spelling used by the workflow validator, the canonical converter
// and Weaver's template emitter. The underscored fork_join form is an internal Go and
// proto identifier, not part of the YAML vocabulary, and is rejected.
func TestLoadWorkflowAgents_ForkJoinPatternType(t *testing.T) {
	const workflowTemplate = `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-forkjoin
  description: Test fork-join workflow
spec:
  type: %s
  agent_ids:
    - agent-a
    - agent-b
  prompt: 'Research the target: {{input}}'
  merge_strategy: summary
`

	tests := []struct {
		name        string
		patternType string
		wantErr     string
	}{
		{
			name:        "canonical hyphenated form loads",
			patternType: "fork-join",
		},
		{
			name:        "internal underscored form is rejected",
			patternType: "fork_join",
			wantErr:     "unsupported workflow pattern type: fork_join",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowPath := filepath.Join(t.TempDir(), "test-forkjoin.yaml")
			workflowYAML := fmt.Sprintf(workflowTemplate, tt.patternType)
			require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0600))

			configs, err := LoadWorkflowAgents(workflowPath, &mockLLMProvider{})

			if tt.wantErr != "" {
				require.Error(t, err, "expected %q to be rejected", tt.patternType)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)

			var coordinator *loomv1.AgentConfig
			subAgents := make(map[string]*loomv1.AgentConfig)
			for _, config := range configs {
				if config.Metadata["role"] == "coordinator" {
					coordinator = config
					continue
				}
				subAgents[config.Name] = config
			}

			require.NotNil(t, coordinator, "no coordinator generated")
			assert.Equal(t, "test-forkjoin", coordinator.Name)
			assert.Equal(t, "fork-join", coordinator.Metadata["pattern"])

			require.Len(t, subAgents, 2)
			assert.Contains(t, subAgents, "test-forkjoin:agent-a")
			assert.Contains(t, subAgents, "test-forkjoin:agent-b")
		})
	}
}

func TestLoadWorkflowAgents_ParallelTasks(t *testing.T) {
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-parallel
  description: Test parallel workflow
spec:
  type: parallel
  tasks:
    - agent_id: agent-a
      prompt: First task
    - agent_id: agent-b
      prompt: Second task
    - agent_id: agent-a
      prompt: Third task using the first agent
`

	workflowPath := filepath.Join(t.TempDir(), "test-parallel.yaml")
	require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0600))

	configs, err := LoadWorkflowAgents(workflowPath, &mockLLMProvider{})
	require.NoError(t, err)
	require.Len(t, configs, 3)
	assert.Equal(t, "test-parallel", configs[0].Name)
	assert.Equal(t, "test-parallel:agent-a", configs[1].Name)
	assert.Equal(t, "test-parallel:agent-b", configs[2].Name)

	configsByName := make(map[string]*loomv1.AgentConfig, len(configs))
	for _, config := range configs {
		configsByName[config.Name] = config
	}

	coordinator := configsByName["test-parallel"]
	require.NotNil(t, coordinator)
	assert.Equal(t, "coordinator", coordinator.Metadata["role"])
	assert.Equal(t, "test-parallel", coordinator.Metadata["workflow"])
	assert.Equal(t, "parallel", coordinator.Metadata["pattern"])

	for _, agentID := range []string{"agent-a", "agent-b"} {
		subAgent := configsByName["test-parallel:"+agentID]
		require.NotNil(t, subAgent)
		assert.Equal(t, "executor", subAgent.Metadata["role"])
		assert.Equal(t, "test-parallel", subAgent.Metadata["workflow"])
		assert.Equal(t, agentID, subAgent.Metadata["agent_id"])
	}
}

func TestLoadWorkflowAgents_ParallelRejectsMalformedTasks(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{
			name:    "missing tasks",
			spec:    "  type: parallel\n",
			wantErr: "parallel workflow requires 'spec.tasks' array",
		},
		{
			name: "tasks is not an array",
			spec: `  type: parallel
  tasks:
    agent_id: agent-a
    prompt: First task
`,
			wantErr: "parallel workflow requires 'spec.tasks' array",
		},
		{
			name: "task is not an object",
			spec: `  type: parallel
  tasks:
    - agent-a
`,
			wantErr: "parallel workflow task 0 must be an object",
		},
		{
			name: "task is missing agent ID after valid task",
			spec: `  type: parallel
  tasks:
    - agent_id: agent-a
      prompt: First task
    - prompt: Missing agent ID
`,
			wantErr: "parallel workflow task 1 missing non-empty 'agent_id'",
		},
		{
			name: "task has empty agent ID",
			spec: `  type: parallel
  tasks:
    - agent_id: ""
      prompt: Empty agent ID
`,
			wantErr: "parallel workflow task 0 missing non-empty 'agent_id'",
		},
		{
			name: "task has non-string agent ID",
			spec: `  type: parallel
  tasks:
    - agent_id: 42
      prompt: Non-string agent ID
`,
			wantErr: "parallel workflow task 0 missing non-empty 'agent_id'",
		},
		{
			name: "task has whitespace-only agent ID",
			spec: `  type: parallel
  tasks:
    - agent_id: "   "
      prompt: Whitespace-only agent ID
`,
			wantErr: "parallel workflow task 0 missing non-empty 'agent_id'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: invalid-parallel
spec:
` + tt.spec

			workflowPath := filepath.Join(t.TempDir(), "invalid-parallel.yaml")
			require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0600))

			configs, err := LoadWorkflowAgents(workflowPath, &mockLLMProvider{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Nil(t, configs)
		})
	}
}

// TestLoadWorkflowAgents_CanonicalPatternContract pins the registry loader's
// accepted spec.type vocabulary to validation.CanonicalWorkflowPatternTypes.
// Every canonical pattern type must both pass pkg/validation and load through
// LoadWorkflowAgents with all referenced agents registered, so a vocabulary
// or schema split between the validator and the loader (the fork-join bug,
// #307) fails here instead of surfacing as silently skipped workflows.
func TestLoadWorkflowAgents_CanonicalPatternContract(t *testing.T) {
	type contractCase struct {
		yaml       string
		wantAgents []string
	}

	cases := map[string]contractCase{
		"debate": {
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: contract-debate
  description: Canonical debate spec
spec:
  type: debate
  topic: 'Monolith or microservices?'
  agent_ids:
    - architect
    - pragmatist
  rounds: 1
  moderator_agent_id: moderator
`,
			wantAgents: []string{"architect", "pragmatist", "moderator"},
		},
		"fork-join": {
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: contract-fork-join
  description: Canonical fork-join spec
spec:
  type: fork-join
  prompt: 'Research the target: {{input}}'
  agent_ids:
    - researcher-a
    - researcher-b
  merge_strategy: concatenate
`,
			wantAgents: []string{"researcher-a", "researcher-b"},
		},
		"pipeline": {
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: contract-pipeline
  description: Canonical pipeline spec
spec:
  type: pipeline
  initial_prompt: '{{input}}'
  stages:
    - agent_id: spec-writer
      prompt_template: 'Write the spec: {{input}}'
    - agent_id: implementer
      prompt_template: 'Implement: {{previous_output}}'
`,
			wantAgents: []string{"spec-writer", "implementer"},
		},
		"parallel": {
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: contract-parallel
  description: Canonical parallel spec
spec:
  type: parallel
  tasks:
    - agent_id: task-a
      prompt: 'Do task A'
    - agent_id: task-b
      prompt: 'Do task B'
  merge_strategy: concatenate
`,
			wantAgents: []string{"task-a", "task-b"},
		},
		"conditional": {
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: contract-conditional
  description: Canonical conditional spec with nested branch patterns
spec:
  type: conditional
  condition_agent_id: classifier
  condition_prompt: 'Is this a bug report? {{input}}'
  branches:
    bug:
      type: pipeline
      initial_prompt: '{{input}}'
      stages:
        - agent_id: bug-triager
          prompt_template: 'Triage: {{input}}'
    feature:
      type: fork-join
      prompt: 'Assess: {{input}}'
      agent_ids:
        - feature-assessor
  default_branch:
    type: fork-join
    prompt: 'Fallback: {{input}}'
    agent_ids:
      - generalist
  retry_policy:
    max_retries: 2
`,
			wantAgents: []string{"classifier", "bug-triager", "feature-assessor", "generalist"},
		},
		"iterative": {
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: contract-iterative
  description: Canonical iterative spec wrapping a base pipeline
spec:
  type: iterative
  max_iterations: 2
  pipeline:
    initial_prompt: '{{input}}'
    stages:
      - agent_id: drafter
        prompt_template: 'Draft: {{input}}'
      - agent_id: reviewer
        prompt_template: 'Review: {{previous_output}}'
`,
			wantAgents: []string{"drafter", "reviewer"},
		},
		"swarm": {
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: contract-swarm
  description: Canonical swarm spec
spec:
  type: swarm
  question: 'Best approach for {{input}}?'
  agent_ids:
    - voter-a
    - voter-b
    - voter-c
  strategy: majority
  judge_agent_id: judge
  retry_policy:
    max_retries: 1
`,
			wantAgents: []string{"voter-a", "voter-b", "voter-c", "judge"},
		},
	}

	// The table must cover the canonical vocabulary exactly: adding a type to
	// validation.CanonicalWorkflowPatternTypes without teaching the loader
	// (and this table) about it fails here.
	canonical := validation.CanonicalWorkflowPatternTypes()
	tableTypes := make([]string, 0, len(cases))
	for typ := range cases {
		tableTypes = append(tableTypes, typ)
	}
	require.ElementsMatch(t, canonical, tableTypes,
		"contract table out of sync with validation.CanonicalWorkflowPatternTypes — add a spec for the new pattern type")

	for _, patternType := range canonical {
		t.Run(patternType, func(t *testing.T) {
			tc := cases[patternType]
			workflowName := "contract-" + patternType

			// The canonical validator accepts the spec.
			result := validation.ValidateYAMLContent(tc.yaml, workflowName+".yaml")
			require.True(t, result.Valid, "validator rejected canonical %s spec: %+v", patternType, result.Errors)

			// The registry loader loads the same spec and registers every
			// referenced agent.
			workflowPath := filepath.Join(t.TempDir(), workflowName+".yaml")
			require.NoError(t, os.WriteFile(workflowPath, []byte(tc.yaml), 0600))

			configs, err := LoadWorkflowAgents(workflowPath, &mockLLMProvider{})
			require.NoError(t, err, "registry loader rejected canonical %s workflow", patternType)

			var coordinator *loomv1.AgentConfig
			var subAgents []string
			for _, config := range configs {
				if config.Metadata["role"] == "coordinator" {
					coordinator = config
					continue
				}
				subAgents = append(subAgents, config.Name)
			}

			require.NotNil(t, coordinator, "no coordinator generated for %s", patternType)
			assert.Equal(t, workflowName, coordinator.Name)
			assert.Equal(t, patternType, coordinator.Metadata["pattern"])

			wantSubAgents := make([]string, 0, len(tc.wantAgents))
			for _, agentID := range tc.wantAgents {
				wantSubAgents = append(wantSubAgents, workflowName+":"+agentID)
			}
			assert.ElementsMatch(t, wantSubAgents, subAgents)
		})
	}
}

// TestLoadWorkflowAgents_ConditionalNestedPatterns exercises recursive agent
// collection: a conditional branch holding another conditional, and an agent
// shared across branches that must register exactly once.
func TestLoadWorkflowAgents_ConditionalNestedPatterns(t *testing.T) {
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: nested-conditional
spec:
  type: conditional
  condition_agent_id: router
  condition_prompt: 'Route: {{input}}'
  branches:
    simple:
      type: fork-join
      prompt: 'Handle: {{input}}'
      agent_ids:
        - handler
        - escalation
    complex:
      type: conditional
      condition_agent_id: sub-router
      condition_prompt: 'Sub-route: {{input}}'
      branches:
        deep:
          type: pipeline
          initial_prompt: '{{input}}'
          stages:
            - agent_id: specialist
              prompt_template: 'Deep dive: {{input}}'
            - agent_id: escalation
              prompt_template: 'Escalate: {{previous_output}}'
`

	workflowPath := filepath.Join(t.TempDir(), "nested-conditional.yaml")
	require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0600))

	configs, err := LoadWorkflowAgents(workflowPath, &mockLLMProvider{})
	require.NoError(t, err)

	var subAgents []string
	for _, config := range configs {
		if config.Metadata["role"] != "coordinator" {
			subAgents = append(subAgents, config.Name)
		}
	}

	// escalation appears in two branches but registers once.
	assert.ElementsMatch(t, []string{
		"nested-conditional:router",
		"nested-conditional:sub-router",
		"nested-conditional:specialist",
		"nested-conditional:escalation",
		"nested-conditional:handler",
	}, subAgents)
}

// TestLoadWorkflowAgents_ConditionalRejectsMalformedSpecs covers the strict
// errors for canonical conditional workflows.
func TestLoadWorkflowAgents_ConditionalRejectsMalformedSpecs(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantErr string
	}{
		{
			name: "missing branches",
			spec: `  type: conditional
  condition_agent_id: classifier
  condition_prompt: 'Classify: {{input}}'
`,
			wantErr: "conditional workflow requires 'spec.branches' map",
		},
		{
			name: "branch is not an object",
			spec: `  type: conditional
  condition_agent_id: classifier
  condition_prompt: 'Classify: {{input}}'
  branches:
    broken: just-a-string
`,
			wantErr: `conditional branch "broken" must be an object with a 'type' field`,
		},
		{
			name: "branch missing type",
			spec: `  type: conditional
  condition_agent_id: classifier
  condition_prompt: 'Classify: {{input}}'
  branches:
    untyped:
      agent_id: orphan
`,
			wantErr: `conditional branch "untyped": workflow pattern spec missing 'type' field`,
		},
		{
			name: "default_branch is not an object",
			spec: `  type: conditional
  condition_agent_id: classifier
  condition_prompt: 'Classify: {{input}}'
  branches:
    ok:
      type: fork-join
      prompt: 'Handle: {{input}}'
      agent_ids:
        - handler
  default_branch: nope
`,
			wantErr: "conditional default_branch must be an object with a 'type' field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: invalid-conditional
spec:
` + tt.spec

			workflowPath := filepath.Join(t.TempDir(), "invalid-conditional.yaml")
			require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0600))

			configs, err := LoadWorkflowAgents(workflowPath, &mockLLMProvider{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Nil(t, configs)
		})
	}
}

// TestLoadWorkflowAgents_ShippedOrchestrationExamples loads every shipped
// orchestration example through the registry loader. Before the canonical
// pattern-type work, several of these (swarm, conditional) validated but were
// silently skipped by the registry at startup.
func TestLoadWorkflowAgents_ShippedOrchestrationExamples(t *testing.T) {
	examplesDir := filepath.Join("..", "..", "examples", "reference", "workflows", "orchestration-patterns")

	var files []string
	err := filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".yaml" {
			files = append(files, path)
		}
		return nil
	})
	require.NoError(t, err, "examples directory missing (update the path)")
	require.NotEmpty(t, files)

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			configs, err := LoadWorkflowAgents(file, &mockLLMProvider{})
			require.NoError(t, err, "shipped example failed to load in the registry: %s", file)

			var coordinator *loomv1.AgentConfig
			subAgents := 0
			for _, config := range configs {
				if config.Metadata["role"] == "coordinator" {
					coordinator = config
					continue
				}
				subAgents++
			}
			require.NotNil(t, coordinator, "no coordinator generated for %s", file)
			assert.Greater(t, subAgents, 0, "no sub-agents registered for %s", file)
		})
	}
}

// TestLoadWorkflowAgents_LoaderOnlySpellingsRejected locks out spec.type
// spellings that only the registry loader ever accepted. They were never
// valid for the validator or the canonical converter, so accepting them here
// reintroduces the vocabulary split fixed for fork_join in #307.
func TestLoadWorkflowAgents_LoaderOnlySpellingsRejected(t *testing.T) {
	workflowYAML := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: loader-only-spelling
spec:
  type: iterative_pipeline
  stages:
    - agent_id: stage-agent
      prompt_template: 'Run: {{input}}'
`

	workflowPath := filepath.Join(t.TempDir(), "loader-only-spelling.yaml")
	require.NoError(t, os.WriteFile(workflowPath, []byte(workflowYAML), 0600))

	configs, err := LoadWorkflowAgents(workflowPath, &mockLLMProvider{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported workflow pattern type: iterative_pipeline")
	assert.Nil(t, configs)
}
