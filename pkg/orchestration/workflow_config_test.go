// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Test helper to create temporary YAML files
func createTempYAMLFile(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "workflow.yaml")
	err := os.WriteFile(tmpFile, []byte(content), 0644)
	require.NoError(t, err)
	return tmpFile
}

func TestLoadWorkflowFromYAML_DebatePattern(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: architecture-debate
  version: "1.0"
  description: Debate architecture decisions
spec:
  type: debate
  topic: "Should we use microservices or monolith?"
  agent_ids:
    - architect
    - pragmatist
  rounds: 3
  merge_strategy: consensus
  moderator_agent_id: senior-architect
`

	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	require.NotNil(t, pattern)

	debate := pattern.GetDebate()
	require.NotNil(t, debate)
	assert.Equal(t, "Should we use microservices or monolith?", debate.Topic)
	assert.Equal(t, []string{"architect", "pragmatist"}, debate.AgentIds)
	assert.Equal(t, int32(3), debate.Rounds)
	assert.Equal(t, loomv1.MergeStrategy_CONSENSUS, debate.MergeStrategy)
	assert.Equal(t, "senior-architect", debate.ModeratorAgentId)
}

func TestLoadWorkflowFromYAML_ForkJoinPattern(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: parallel-analysis
  version: "1.0"
spec:
  type: fork-join
  prompt: "Analyze this code for bugs, performance, and security issues"
  agent_ids:
    - bug-detector
    - perf-analyzer
    - security-checker
  merge_strategy: concatenate
  timeout_seconds: 300
`

	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	require.NotNil(t, pattern)

	forkJoin := pattern.GetForkJoin()
	require.NotNil(t, forkJoin)
	assert.Equal(t, "Analyze this code for bugs, performance, and security issues", forkJoin.Prompt)
	assert.Equal(t, []string{"bug-detector", "perf-analyzer", "security-checker"}, forkJoin.AgentIds)
	assert.Equal(t, loomv1.MergeStrategy_CONCATENATE, forkJoin.MergeStrategy)
	assert.Equal(t, int32(300), forkJoin.TimeoutSeconds)
}

func TestLoadWorkflowFromYAML_PipelinePattern(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: sequential-design
  version: "1.0"
spec:
  type: pipeline
  initial_prompt: "Design a user authentication system"
  stages:
    - agent_id: spec-writer
      prompt_template: "Write API specification: {{previous}}"
      validation_prompt: "Check if spec is complete"
    - agent_id: implementer
      prompt_template: "Implement based on: {{previous}}"
    - agent_id: test-engineer
      prompt_template: "Create tests for: {{previous}}"
  pass_full_history: true
`

	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	require.NotNil(t, pattern)

	pipeline := pattern.GetPipeline()
	require.NotNil(t, pipeline)
	assert.Equal(t, "Design a user authentication system", pipeline.InitialPrompt)
	assert.Len(t, pipeline.Stages, 3)
	assert.Equal(t, "spec-writer", pipeline.Stages[0].AgentId)
	assert.Equal(t, "Write API specification: {{previous}}", pipeline.Stages[0].PromptTemplate)
	assert.Equal(t, "Check if spec is complete", pipeline.Stages[0].ValidationPrompt)
	assert.True(t, pipeline.PassFullHistory)
}

func TestLoadWorkflowFromYAML_ParallelPattern(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: independent-tasks
  version: "1.0"
spec:
  type: parallel
  tasks:
    - agent_id: error-handler
      prompt: "Design error handling strategy"
      metadata:
        priority: "high"
        category: "implementation"
    - agent_id: doc-writer
      prompt: "Write API documentation"
      metadata:
        priority: "medium"
    - agent_id: example-creator
      prompt: "Create usage examples"
  merge_strategy: concatenate
  timeout_seconds: 600
`

	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	require.NotNil(t, pattern)

	parallel := pattern.GetParallel()
	require.NotNil(t, parallel)
	assert.Len(t, parallel.Tasks, 3)
	assert.Equal(t, "error-handler", parallel.Tasks[0].AgentId)
	assert.Equal(t, "Design error handling strategy", parallel.Tasks[0].Prompt)
	assert.Equal(t, "high", parallel.Tasks[0].Metadata["priority"])
	assert.Equal(t, "implementation", parallel.Tasks[0].Metadata["category"])
	assert.Equal(t, loomv1.MergeStrategy_CONCATENATE, parallel.MergeStrategy)
	assert.Equal(t, int32(600), parallel.TimeoutSeconds)
}

func TestLoadWorkflowFromYAML_ConditionalPattern(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: complexity-routing
  version: "1.0"
spec:
  type: conditional
  condition_agent_id: complexity-classifier
  condition_prompt: "Is this a simple or complex feature? Answer: simple or complex"
  branches:
    simple:
      type: fork-join
      prompt: "Quick implementation approach"
      agent_ids:
        - junior-dev
      merge_strategy: first
    complex:
      type: pipeline
      initial_prompt: "Detailed design needed"
      stages:
        - agent_id: architect
          prompt_template: "Design architecture"
        - agent_id: senior-dev
          prompt_template: "Implement: {{previous}}"
  default_branch:
    type: fork-join
    prompt: "Standard implementation"
    agent_ids:
      - standard-dev
    merge_strategy: first
`

	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	require.NotNil(t, pattern)

	conditional := pattern.GetConditional()
	require.NotNil(t, conditional)
	assert.Equal(t, "complexity-classifier", conditional.ConditionAgentId)
	assert.Equal(t, "Is this a simple or complex feature? Answer: simple or complex", conditional.ConditionPrompt)
	assert.Len(t, conditional.Branches, 2)

	// Check simple branch
	simpleBranch := conditional.Branches["simple"]
	require.NotNil(t, simpleBranch)
	simpleForkJoin := simpleBranch.GetForkJoin()
	require.NotNil(t, simpleForkJoin)
	assert.Equal(t, "Quick implementation approach", simpleForkJoin.Prompt)
	assert.Equal(t, []string{"junior-dev"}, simpleForkJoin.AgentIds)

	// Check complex branch
	complexBranch := conditional.Branches["complex"]
	require.NotNil(t, complexBranch)
	complexPipeline := complexBranch.GetPipeline()
	require.NotNil(t, complexPipeline)
	assert.Equal(t, "Detailed design needed", complexPipeline.InitialPrompt)
	assert.Len(t, complexPipeline.Stages, 2)

	// Check default branch
	require.NotNil(t, conditional.DefaultBranch)
	defaultForkJoin := conditional.DefaultBranch.GetForkJoin()
	require.NotNil(t, defaultForkJoin)
	assert.Equal(t, "Standard implementation", defaultForkJoin.Prompt)
}

func TestLoadWorkflowFromYAML_FileNotFound(t *testing.T) {
	_, err := LoadWorkflowFromYAML("/nonexistent/path/workflow.yaml")
	assert.ErrorIs(t, err, ErrFileNotFound)
}

func TestLoadWorkflowFromYAML_InvalidYAML(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: broken
  invalid yaml structure [[[
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidYAML)
}

func TestLoadWorkflowFromYAML_MissingAPIVersion(t *testing.T) {
	yaml := `
kind: Workflow
metadata:
  name: test
spec:
  type: debate
  topic: "test"
  agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "missing apiVersion")
}

func TestLoadWorkflowFromYAML_WrongAPIVersion(t *testing.T) {
	yaml := `
apiVersion: loom/v2
kind: Workflow
metadata:
  name: test
spec:
  type: debate
  topic: "test"
  agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "unsupported apiVersion")
}

func TestLoadWorkflowFromYAML_MissingKind(t *testing.T) {
	yaml := `
apiVersion: loom/v1
metadata:
  name: test
spec:
  type: debate
  topic: "test"
  agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "missing kind")
}

func TestLoadWorkflowFromYAML_WrongKind(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Pipeline
metadata:
  name: test
spec:
  type: debate
  topic: "test"
  agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "unsupported kind")
}

func TestLoadWorkflowFromYAML_MissingMetadataName(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  version: "1.0"
spec:
  type: debate
  topic: "test"
  agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "missing metadata.name")
}

func TestLoadWorkflowFromYAML_MissingSpec(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "missing spec")
}

func TestLoadWorkflowFromYAML_MissingSpecType(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  topic: "test"
  agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "missing spec.type")
}

func TestLoadWorkflowFromYAML_UnsupportedPatternType(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: invalid-pattern
  topic: "test"
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrUnsupportedPattern)
}

func TestLoadWorkflowFromYAML_DebateMissingTopic(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: debate
  agent_ids: [agent1, agent2]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'topic'")
}

func TestLoadWorkflowFromYAML_DebateMissingAgentIds(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: debate
  topic: "test"
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'agent_ids'")
}

func TestLoadWorkflowFromYAML_ForkJoinMissingPrompt(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: fork-join
  agent_ids: [agent1, agent2]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'prompt'")
}

func TestLoadWorkflowFromYAML_PipelineMissingInitialPrompt(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: pipeline
  stages:
    - agent_id: agent1
      prompt_template: "test"
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'initial_prompt'")
}

func TestLoadWorkflowFromYAML_PipelineMissingStages(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: pipeline
  initial_prompt: "test"
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'stages'")
}

func TestLoadWorkflowFromYAML_ParallelMissingTasks(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: parallel
  merge_strategy: concatenate
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'tasks'")
}

func TestLoadWorkflowFromYAML_ConditionalMissingConditionAgentId(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: conditional
  condition_prompt: "test"
  branches:
    yes:
      type: fork-join
      prompt: "test"
      agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'condition_agent_id'")
}

func TestLoadWorkflowFromYAML_ConditionalMissingBranches(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: conditional
  condition_agent_id: classifier
  condition_prompt: "test"
`
	tmpFile := createTempYAMLFile(t, yaml)
	_, err := LoadWorkflowFromYAML(tmpFile)
	assert.ErrorIs(t, err, ErrInvalidWorkflow)
	assert.Contains(t, err.Error(), "requires 'branches'")
}

func TestParseMergeStrategy(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected loomv1.MergeStrategy
	}{
		{"consensus", "consensus", loomv1.MergeStrategy_CONSENSUS},
		{"voting", "voting", loomv1.MergeStrategy_VOTING},
		{"concatenate", "concatenate", loomv1.MergeStrategy_CONCATENATE},
		{"first", "first", loomv1.MergeStrategy_FIRST},
		{"best", "best", loomv1.MergeStrategy_BEST},
		{"summary", "summary", loomv1.MergeStrategy_SUMMARY},
		{"unknown", "unknown", loomv1.MergeStrategy_MERGE_STRATEGY_UNSPECIFIED},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMergeStrategy(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLoadWorkflowFromYAML_DefaultValues(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: minimal-debate
spec:
  type: debate
  topic: "test topic"
  agent_ids: [agent1, agent2]
`
	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	debate := pattern.GetDebate()
	require.NotNil(t, debate)

	// Default rounds should be 1
	assert.Equal(t, int32(1), debate.Rounds)
	// Default merge strategy should be CONSENSUS
	assert.Equal(t, loomv1.MergeStrategy_CONSENSUS, debate.MergeStrategy)
	// Moderator should be empty
	assert.Empty(t, debate.ModeratorAgentId)
}

func TestLoadWorkflowFromYAML_FilePermissions(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  type: debate
  topic: "test"
  agent_ids: [agent1]
`
	tmpFile := createTempYAMLFile(t, yaml)

	// Make file unreadable
	err := os.Chmod(tmpFile, 0000)
	require.NoError(t, err)

	// Try to load
	_, err = LoadWorkflowFromYAML(tmpFile)

	// Restore permissions for cleanup
	_ = os.Chmod(tmpFile, 0644)

	assert.ErrorIs(t, err, ErrInvalidPermissions)
}

// TestLoadWorkflowFromYAML_IterativePattern tests YAML loading for Weaver-generated iterative workflows.
func TestLoadWorkflowFromYAML_IterativePattern(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: npath-discovery-v3.5
  version: "3.5"
  description: Autonomous nPath discovery with restart coordination
spec:
  type: iterative
  max_iterations: 5
  restart_topic: workflow.restart
  restart_policy:
    enabled: true
    restartable_stages:
      - table-discovery
      - data-sampling
    cooldown_seconds: 10
    reset_shared_memory: false
    preserve_outputs: true
  restart_triggers:
    - npath-execution
    - presentation
  pipeline:
    initial_prompt: "Discover nPath-suitable tables in the database"
    stages:
      - agent_id: table-discovery
        prompt_template: "Find tables for nPath analysis"
      - agent_id: data-sampling
        prompt_template: "Sample data from {{previous}}"
      - agent_id: npath-execution
        prompt_template: "Execute nPath on {{previous}}"
      - agent_id: presentation
        prompt_template: "Present results: {{previous}}"
`

	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	require.NotNil(t, pattern)

	// Verify iterative pattern
	iterative := pattern.GetIterative()
	require.NotNil(t, iterative, "Expected iterative pattern")
	assert.Equal(t, int32(5), iterative.MaxIterations)
	assert.Equal(t, "workflow.restart", iterative.RestartTopic)

	// Verify restart policy
	require.NotNil(t, iterative.RestartPolicy)
	assert.True(t, iterative.RestartPolicy.Enabled)
	assert.Len(t, iterative.RestartPolicy.RestartableStages, 2)
	assert.Contains(t, iterative.RestartPolicy.RestartableStages, "table-discovery")
	assert.Contains(t, iterative.RestartPolicy.RestartableStages, "data-sampling")
	assert.Equal(t, int32(10), iterative.RestartPolicy.CooldownSeconds)
	assert.False(t, iterative.RestartPolicy.ResetSharedMemory)
	assert.True(t, iterative.RestartPolicy.PreserveOutputs)

	// Verify restart triggers
	assert.Len(t, iterative.RestartTriggers, 2)
	assert.Contains(t, iterative.RestartTriggers, "npath-execution")
	assert.Contains(t, iterative.RestartTriggers, "presentation")

	// Verify pipeline
	require.NotNil(t, iterative.Pipeline)
	assert.Equal(t, "Discover nPath-suitable tables in the database", iterative.Pipeline.InitialPrompt)
	assert.Len(t, iterative.Pipeline.Stages, 4)

	// Verify stages
	stages := iterative.Pipeline.Stages
	assert.Equal(t, "table-discovery", stages[0].AgentId)
	assert.Equal(t, "data-sampling", stages[1].AgentId)
	assert.Equal(t, "npath-execution", stages[2].AgentId)
	assert.Equal(t, "presentation", stages[3].AgentId)
}

// TestLoadWorkflowFromYAML_IterativePattern_MinimalConfig tests minimal iterative config.
func TestLoadWorkflowFromYAML_IterativePattern_MinimalConfig(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: simple-iterative
spec:
  type: iterative
  pipeline:
    initial_prompt: "Start"
    stages:
      - agent_id: stage1
        prompt_template: "Execute"
`

	tmpFile := createTempYAMLFile(t, yaml)
	pattern, err := LoadWorkflowFromYAML(tmpFile)

	require.NoError(t, err)
	require.NotNil(t, pattern)

	iterative := pattern.GetIterative()
	require.NotNil(t, iterative)

	// Verify defaults
	assert.Equal(t, int32(3), iterative.MaxIterations)          // Default max_iterations
	assert.Equal(t, "workflow.restart", iterative.RestartTopic) // Default topic
	assert.Nil(t, iterative.RestartPolicy)                      // No policy specified
}

// TestLoadWorkflowFromYAML_IterativePattern_InvalidConfig tests error handling.
func TestLoadWorkflowFromYAML_IterativePattern_InvalidConfig(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		expectedErr string
	}{
		{
			name: "missing_pipeline",
			yaml: `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: invalid
spec:
  type: iterative
  max_iterations: 3
`,
			expectedErr: "requires 'pipeline' field",
		},
		{
			name: "invalid_max_iterations_type",
			yaml: `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: invalid
spec:
  type: iterative
  max_iterations: "not a number"
  pipeline:
    initial_prompt: "Start"
    stages:
      - agent_id: stage1
        prompt_template: "Execute"
`,
			expectedErr: "max_iterations must be an integer",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := createTempYAMLFile(t, tt.yaml)
			_, err := LoadWorkflowFromYAML(tmpFile)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}
