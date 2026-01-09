// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestParseWorkflowFromYAML_Pipeline(t *testing.T) {
	// Create temp directory for test files
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "pipeline.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: code-review-pipeline
  description: Sequential code review stages
spec:
  pattern: pipeline
  agents:
    - id: syntax-checker
      name: Syntax Checker
      system_prompt: "Check code syntax"
      prompt_template: "Review syntax: {{previous}}"
    - id: logic-reviewer
      name: Logic Reviewer
      system_prompt: "Review code logic"
      prompt_template: "Review logic: {{previous}}"
orchestration:
  pass_full_history: true
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	pattern, metadata, err := ParseWorkflowFromYAML(yamlPath)
	if err != nil {
		t.Fatalf("ParseWorkflowFromYAML failed: %v", err)
	}

	// Verify metadata
	if metadata.Name != "code-review-pipeline" {
		t.Errorf("Expected name 'code-review-pipeline', got '%s'", metadata.Name)
	}

	// Verify pattern type
	pipeline, ok := pattern.Pattern.(*loomv1.WorkflowPattern_Pipeline)
	if !ok {
		t.Fatalf("Expected pipeline pattern, got %T", pattern.Pattern)
	}

	// Verify stages
	if len(pipeline.Pipeline.Stages) != 2 {
		t.Errorf("Expected 2 stages, got %d", len(pipeline.Pipeline.Stages))
	}

	// Verify first stage
	if pipeline.Pipeline.Stages[0].AgentId != "syntax-checker" {
		t.Errorf("Expected agent_id 'syntax-checker', got '%s'", pipeline.Pipeline.Stages[0].AgentId)
	}

	// Verify pass_full_history
	if !pipeline.Pipeline.PassFullHistory {
		t.Error("Expected pass_full_history to be true")
	}
}

func TestParseWorkflowFromYAML_ForkJoin(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "forkjoin.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: parallel-review
spec:
  pattern: fork_join
  agents:
    - id: security-analyst
      name: Security Analyst
      system_prompt: "Analyze security"
    - id: performance-analyst
      name: Performance Analyst
      system_prompt: "Analyze performance"
  config:
    merge_strategy: summary
orchestration:
  timeout_seconds: 300
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	pattern, _, err := ParseWorkflowFromYAML(yamlPath)
	if err != nil {
		t.Fatalf("ParseWorkflowFromYAML failed: %v", err)
	}

	forkJoin, ok := pattern.Pattern.(*loomv1.WorkflowPattern_ForkJoin)
	if !ok {
		t.Fatalf("Expected fork_join pattern, got %T", pattern.Pattern)
	}

	// Verify agent IDs
	if len(forkJoin.ForkJoin.AgentIds) != 2 {
		t.Errorf("Expected 2 agents, got %d", len(forkJoin.ForkJoin.AgentIds))
	}

	// Verify merge strategy
	if forkJoin.ForkJoin.MergeStrategy != loomv1.MergeStrategy_SUMMARY {
		t.Errorf("Expected SUMMARY merge strategy, got %v", forkJoin.ForkJoin.MergeStrategy)
	}

	// Verify timeout
	if forkJoin.ForkJoin.TimeoutSeconds != 300 {
		t.Errorf("Expected timeout 300, got %d", forkJoin.ForkJoin.TimeoutSeconds)
	}
}

func TestParseWorkflowFromYAML_Parallel(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "parallel.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: parallel-tasks
spec:
  pattern: parallel
  agents:
    - id: task1
      name: Task 1
      system_prompt: "Execute task 1"
      prompt_template: "Task 1: {{user_query}}"
    - id: task2
      name: Task 2
      system_prompt: "Execute task 2"
      prompt_template: "Task 2: {{user_query}}"
  config:
    merge_strategy: concatenate
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	pattern, _, err := ParseWorkflowFromYAML(yamlPath)
	if err != nil {
		t.Fatalf("ParseWorkflowFromYAML failed: %v", err)
	}

	parallel, ok := pattern.Pattern.(*loomv1.WorkflowPattern_Parallel)
	if !ok {
		t.Fatalf("Expected parallel pattern, got %T", pattern.Pattern)
	}

	// Verify tasks
	if len(parallel.Parallel.Tasks) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(parallel.Parallel.Tasks))
	}

	// Verify merge strategy
	if parallel.Parallel.MergeStrategy != loomv1.MergeStrategy_CONCATENATE {
		t.Errorf("Expected CONCATENATE merge strategy, got %v", parallel.Parallel.MergeStrategy)
	}
}

func TestParseWorkflowFromYAML_Debate(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "debate.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: architecture-debate
spec:
  pattern: debate
  agents:
    - id: pragmatist
      name: Pragmatist
      role: debater
      system_prompt: "Argue for practical solutions"
    - id: idealist
      name: Idealist
      role: debater
      system_prompt: "Argue for ideal solutions"
    - id: judge
      name: Judge
      role: moderator
      system_prompt: "Moderate the debate"
  config:
    rounds: 5
orchestration:
  max_rounds: 5
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	pattern, _, err := ParseWorkflowFromYAML(yamlPath)
	if err != nil {
		t.Fatalf("ParseWorkflowFromYAML failed: %v", err)
	}

	debate, ok := pattern.Pattern.(*loomv1.WorkflowPattern_Debate)
	if !ok {
		t.Fatalf("Expected debate pattern, got %T", pattern.Pattern)
	}

	// Verify debaters (should exclude moderator)
	if len(debate.Debate.AgentIds) != 2 {
		t.Errorf("Expected 2 debaters, got %d", len(debate.Debate.AgentIds))
	}

	// Verify moderator
	if debate.Debate.ModeratorAgentId != "judge" {
		t.Errorf("Expected moderator 'judge', got '%s'", debate.Debate.ModeratorAgentId)
	}

	// Verify rounds
	if debate.Debate.Rounds != 5 {
		t.Errorf("Expected 5 rounds, got %d", debate.Debate.Rounds)
	}
}

func TestParseWorkflowFromYAML_Conditional(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "conditional.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: conditional-routing
spec:
  pattern: conditional
  agents:
    - id: classifier
      name: Classifier
      role: classifier
      system_prompt: "Classify the request"
      prompt_template: "Classify: {{user_query}}"
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	pattern, _, err := ParseWorkflowFromYAML(yamlPath)
	if err != nil {
		t.Fatalf("ParseWorkflowFromYAML failed: %v", err)
	}

	conditional, ok := pattern.Pattern.(*loomv1.WorkflowPattern_Conditional)
	if !ok {
		t.Fatalf("Expected conditional pattern, got %T", pattern.Pattern)
	}

	// Verify condition agent
	if conditional.Conditional.ConditionAgentId != "classifier" {
		t.Errorf("Expected condition_agent_id 'classifier', got '%s'", conditional.Conditional.ConditionAgentId)
	}
}

func TestParseWorkflowFromYAML_InvalidAPIVersion(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "invalid.yaml")

	yamlContent := `apiVersion: loom/v2
kind: Workflow
metadata:
  name: test
spec:
  pattern: pipeline
  agents: []
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	_, _, err := ParseWorkflowFromYAML(yamlPath)
	if err == nil {
		t.Fatal("Expected error for invalid apiVersion, got nil")
	}
}

func TestParseWorkflowFromYAML_MissingPattern(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "missing.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  agents:
    - id: agent1
      name: Agent 1
      system_prompt: "test"
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	_, _, err := ParseWorkflowFromYAML(yamlPath)
	if err == nil {
		t.Fatal("Expected error for missing pattern, got nil")
	}
}

func TestParseWorkflowFromYAML_EmptyAgents(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "empty.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  pattern: pipeline
  agents: []
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	_, _, err := ParseWorkflowFromYAML(yamlPath)
	if err == nil {
		t.Fatal("Expected error for empty agents, got nil")
	}
}

func TestParseWorkflowFromYAML_UnsupportedPattern(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "unsupported.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test
spec:
  pattern: unsupported_pattern
  agents:
    - id: agent1
      name: Agent 1
      system_prompt: "test"
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write test YAML: %v", err)
	}

	_, _, err := ParseWorkflowFromYAML(yamlPath)
	if err == nil {
		t.Fatal("Expected error for unsupported pattern, got nil")
	}
}
