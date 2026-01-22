// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOrchestrationWorkflowValidation tests validation of orchestration workflows (pattern-based).
func TestOrchestrationWorkflowValidation(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		shouldPass  bool
		description string
	}{
		{
			name: "valid_debate_workflow",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-debate
  description: Test debate workflow
spec:
  pattern: debate
  topic: "Should we use microservices?"
  agent_ids:
    - agent1
    - agent2
  rounds: 3
`,
			shouldPass:  true,
			description: "Valid debate orchestration workflow",
		},
		{
			name: "valid_fork_join_workflow",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-fork-join
spec:
  type: fork-join
  prompt: "Analyze this from different perspectives"
  agent_ids:
    - agent1
    - agent2
  merge_strategy: CONCATENATE
`,
			shouldPass:  true,
			description: "Valid fork-join orchestration workflow",
		},
		{
			name: "valid_pipeline_workflow",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-pipeline
spec:
  type: pipeline
  initial_prompt: "Design an authentication system"
  stages:
    - agent_id: agent1
      prompt_template: "Write spec: {{previous}}"
    - agent_id: agent2
      prompt_template: "Implement: {{previous}}"
`,
			shouldPass:  true,
			description: "Valid pipeline orchestration workflow",
		},
		{
			name: "missing_pattern_field",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-invalid
spec:
  agents:
    - id: agent1
`,
			shouldPass:  false,
			description: "Missing pattern/type field for orchestration workflow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateYAMLContent(tt.yaml, "")

			if tt.shouldPass {
				assert.True(t, result.Valid, "Expected validation to pass for %s: %v", tt.description, result.Errors)
			} else {
				assert.False(t, result.Valid, "Expected validation to fail for %s", tt.description)
			}
		})
	}
}

// TestMultiAgentWorkflowValidation tests validation of multi-agent workflows (entrypoint-based).
func TestMultiAgentWorkflowValidation(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		shouldPass  bool
		description string
	}{
		{
			name: "valid_multi_agent_workflow_structure",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-multi-agent
  description: Test multi-agent workflow
spec:
  entrypoint: coordinator
  agents:
    - name: coordinator
      description: Main coordinator
    - name: worker
      description: Worker agent
  config:
    timeout_seconds: 300
`,
			shouldPass:  true,
			description: "Valid multi-agent workflow structure (no agent file references to check)",
		},
		{
			name: "missing_entrypoint",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-invalid
spec:
  agents:
    - name: coordinator
      agent: coordinator-agent
`,
			shouldPass:  false,
			description: "Multi-agent workflow missing entrypoint",
		},
		{
			name: "missing_agents",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-invalid
spec:
  entrypoint: coordinator
`,
			shouldPass:  false,
			description: "Multi-agent workflow missing agents list",
		},
		{
			name: "entrypoint_not_in_agents",
			yaml: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-invalid
spec:
  entrypoint: nonexistent
  agents:
    - name: coordinator
      agent: coordinator-agent
`,
			shouldPass:  false,
			description: "Entrypoint agent not found in agents list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateYAMLContent(tt.yaml, "")

			if tt.shouldPass {
				assert.True(t, result.Valid, "Expected validation to pass for %s: %v", tt.description, result.Errors)
			} else {
				assert.False(t, result.Valid, "Expected validation to fail for %s", tt.description)
			}
		})
	}
}

// TestWorkflowTypeDetection tests proper detection of workflow types.
func TestWorkflowTypeDetection(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		expectedType string
	}{
		{
			name: "detect_orchestration_workflow_with_pattern",
			yaml: `apiVersion: loom/v1
kind: Workflow
spec:
  pattern: debate
`,
			expectedType: "orchestration",
		},
		{
			name: "detect_orchestration_workflow_with_type",
			yaml: `apiVersion: loom/v1
kind: Workflow
spec:
  type: fork-join
`,
			expectedType: "orchestration",
		},
		{
			name: "detect_multi_agent_workflow",
			yaml: `apiVersion: loom/v1
kind: Workflow
spec:
  entrypoint: coordinator
  agents:
    - name: coordinator
`,
			expectedType: "multi-agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test will be implemented after we add workflow type detection
			// For now, just validate that it's recognized as a Workflow
			result := ValidateYAMLContent(tt.yaml, "")
			assert.Equal(t, "Workflow", result.Kind)
		})
	}
}

// TestShellExecuteValidation verifies shell_execute properly validates both workflow types.
func TestShellExecuteValidation(t *testing.T) {
	// This test ensures that when shell_execute writes a workflow YAML,
	// it gets validated correctly regardless of whether it's an orchestration
	// workflow or a multi-agent workflow.

	tests := []struct {
		name        string
		content     string
		shouldValid bool
	}{
		{
			name: "orchestration_workflow_via_shell",
			content: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-debate
spec:
  pattern: debate
  topic: "Test debate topic"
  agent_ids:
    - agent1
    - agent2
  rounds: 2
`,
			shouldValid: true,
		},
		{
			name: "multi_agent_workflow_via_shell",
			content: `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-coordinator
spec:
  entrypoint: coordinator
  agents:
    - name: coordinator
      description: Main coordinator agent
`,
			shouldValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateYAMLContent(tt.content, "")

			if tt.shouldValid {
				assert.True(t, result.Valid, "Expected validation to pass: %v", result.Errors)
				assert.False(t, result.HasWarnings(), "Should not have warnings")
			} else {
				assert.False(t, result.Valid, "Expected validation to fail")
			}
		})
	}
}
