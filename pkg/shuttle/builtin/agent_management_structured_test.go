// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/session"
)

// TestDNDBugPrevention tests that the structured API prevents field validation errors
// where "role" field was incorrectly used instead of "agent_id" in workflow specs.
func TestDNDBugPrevention(t *testing.T) {
	// Setup temp directory
	tmpDir := t.TempDir()
	oldLoomData := os.Getenv("LOOM_DATA_DIR")
	_ = os.Setenv("LOOM_DATA_DIR", tmpDir)
	defer func() { _ = os.Setenv("LOOM_DATA_DIR", oldLoomData) }()

	// Create weaver context
	ctx := session.WithAgentID(context.Background(), "weaver")

	tool := NewAgentManagementTool()

	// Test case: Workflow with "role" field (should fail validation)
	t.Run("workflow_with_role_field_fails", func(t *testing.T) {
		// First create the agent so it exists
		agentParams := map[string]interface{}{
			"action": "create_agent",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":        "dnd-dm",
					"description": "Dungeon Master",
				},
				"spec": map[string]interface{}{
					"system_prompt": "You are a Dungeon Master",
					"tools":         []string{"publish"}, // Use flat array format
				},
			},
		}

		result, err := tool.Execute(ctx, agentParams)
		require.NoError(t, err)
		if !result.Success {
			t.Logf("Agent creation failed: %+v", result.Error)
			t.Logf("Validation data: %+v", result.Data)
		}
		require.True(t, result.Success, "Agent creation should succeed")

		// Now try to create workflow with WRONG "role" field
		workflowParams := map[string]interface{}{
			"action": "create_workflow",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":        "dnd-game",
					"description": "D&D Campaign",
				},
				"spec": map[string]interface{}{
					"type":           "pipeline",
					"initial_prompt": "Start the adventure",
					"stages": []interface{}{
						map[string]interface{}{
							"role":            "coordinator", // WRONG! Should be agent_id
							"prompt_template": "Guide the adventure",
						},
					},
				},
			},
		}

		result, err = tool.Execute(ctx, workflowParams)
		require.NoError(t, err)
		assert.False(t, result.Success, "Workflow creation should fail with role field")
		assert.Equal(t, "INVALID_FIELD", result.Error.Code)
		assert.Contains(t, result.Error.Message, "invalid field 'role'")
	})

	// Test case: Workflow with correct "agent_id" field (should succeed)
	t.Run("workflow_with_agent_id_succeeds", func(t *testing.T) {
		workflowParams := map[string]interface{}{
			"action": "create_workflow",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":        "dnd-game-correct",
					"description": "D&D Campaign (correct version)",
				},
				"spec": map[string]interface{}{
					"type":           "pipeline",
					"initial_prompt": "Start the adventure",
					"stages": []interface{}{
						map[string]interface{}{
							"agent_id":        "dnd-dm", // CORRECT!
							"prompt_template": "Guide the adventure",
						},
					},
				},
			},
		}

		result, err := tool.Execute(ctx, workflowParams)
		require.NoError(t, err)
		assert.True(t, result.Success, "Workflow creation should succeed with agent_id field")

		// Verify file was created
		workflowPath := filepath.Join(tmpDir, "workflows", "dnd-game-correct.yaml")
		assert.FileExists(t, workflowPath)
	})
}

// TestCreateAgentStructured tests the structured create_agent action.
func TestCreateAgentStructured(t *testing.T) {
	// Setup temp directory
	tmpDir := t.TempDir()
	oldLoomData := os.Getenv("LOOM_DATA_DIR")
	_ = os.Setenv("LOOM_DATA_DIR", tmpDir)
	defer func() { _ = os.Setenv("LOOM_DATA_DIR", oldLoomData) }()

	// Create weaver context
	ctx := session.WithAgentID(context.Background(), "weaver")

	tool := NewAgentManagementTool()

	t.Run("create_agent_with_structured_config", func(t *testing.T) {
		params := map[string]interface{}{
			"action": "create_agent",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":        "test-agent",
					"description": "Test agent for structured API",
					"version":     "1.0.0",
				},
				"spec": map[string]interface{}{
					"system_prompt": "You are a helpful assistant",
					"tools":         []string{"web_search"}, // Use flat array format
					"llm": map[string]interface{}{
						"provider":    "anthropic",
						"model":       "claude-3-5-sonnet-20241022-v2:0",
						"temperature": 0.7,
					},
					"behavior": map[string]interface{}{
						"max_iterations": 10,
						"max_turns":      25,
					},
				},
			},
		}

		result, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.True(t, result.Success, "Agent creation should succeed")

		// Verify file was created
		agentPath := filepath.Join(tmpDir, "agents", "test-agent.yaml")
		assert.FileExists(t, agentPath)

		// Read and verify YAML content
		content, err := os.ReadFile(agentPath)
		require.NoError(t, err)
		yamlStr := string(content)

		// Check for K8s-style structure
		assert.Contains(t, yamlStr, "apiVersion: loom/v1")
		assert.Contains(t, yamlStr, "kind: Agent")
		assert.Contains(t, yamlStr, "name: test-agent")
		assert.Contains(t, yamlStr, "system_prompt: You are a helpful assistant")
	})

	t.Run("create_agent_missing_required_field", func(t *testing.T) {
		params := map[string]interface{}{
			"action": "create_agent",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test-agent-2",
				},
				"spec": map[string]interface{}{
					// Missing system_prompt!
					"tools": []string{"web_search"}, // Use flat array format
				},
			},
		}

		result, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, result.Success, "Agent creation should fail without system_prompt")
		assert.Equal(t, "CONVERSION_ERROR", result.Error.Code)
		assert.Contains(t, result.Error.Message, "system_prompt")
	})
}

// TestAgentReferenceValidation tests that workflow creation validates agent references.
func TestAgentReferenceValidation(t *testing.T) {
	// Setup temp directory
	tmpDir := t.TempDir()
	oldLoomData := os.Getenv("LOOM_DATA_DIR")
	_ = os.Setenv("LOOM_DATA_DIR", tmpDir)
	defer func() { _ = os.Setenv("LOOM_DATA_DIR", oldLoomData) }()

	// Create agents directory
	agentsDir := filepath.Join(tmpDir, "agents")
	err := os.MkdirAll(agentsDir, 0750)
	require.NoError(t, err)

	// Create weaver context
	ctx := session.WithAgentID(context.Background(), "weaver")

	tool := NewAgentManagementTool()

	t.Run("workflow_with_nonexistent_agent_fails", func(t *testing.T) {
		params := map[string]interface{}{
			"action": "create_workflow",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test-workflow",
				},
				"spec": map[string]interface{}{
					"type":           "pipeline",
					"initial_prompt": "Do something",
					"stages": []interface{}{
						map[string]interface{}{
							"agent_id":        "nonexistent-agent", // This agent doesn't exist!
							"prompt_template": "Do the thing",
						},
					},
				},
			},
		}

		result, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, result.Success, "Workflow creation should fail with nonexistent agent reference")
		assert.Equal(t, "INVALID_AGENT_REFERENCE", result.Error.Code)
		assert.Contains(t, result.Error.Message, "nonexistent-agent")
	})

	t.Run("workflow_with_existing_agent_succeeds", func(t *testing.T) {
		// First create the agent file
		agentYAML := `apiVersion: loom/v1
kind: Agent
metadata:
  name: existing-agent
spec:
  system_prompt: "I exist"
  tools:
    builtin: []
`
		agentPath := filepath.Join(agentsDir, "existing-agent.yaml")
		err := os.WriteFile(agentPath, []byte(agentYAML), 0600)
		require.NoError(t, err)

		// Now create workflow referencing this agent
		params := map[string]interface{}{
			"action": "create_workflow",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test-workflow-valid",
				},
				"spec": map[string]interface{}{
					"type":           "pipeline",
					"initial_prompt": "Do something",
					"stages": []interface{}{
						map[string]interface{}{
							"agent_id":        "existing-agent", // This agent exists!
							"prompt_template": "Do the thing",
						},
					},
				},
			},
		}

		result, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.True(t, result.Success, "Workflow creation should succeed with existing agent reference")

		// Verify workflow file was created
		workflowPath := filepath.Join(tmpDir, "workflows", "test-workflow-valid.yaml")
		assert.FileExists(t, workflowPath)
	})
}

// TestUpdateWorkflowValidation tests that workflow updates also validate agent references.
func TestUpdateWorkflowValidation(t *testing.T) {
	tmpDir := t.TempDir()
	oldLoomData := os.Getenv("LOOM_DATA_DIR")
	_ = os.Setenv("LOOM_DATA_DIR", tmpDir)
	defer func() { _ = os.Setenv("LOOM_DATA_DIR", oldLoomData) }()

	// Create directories
	agentsDir := config.GetLoomSubDir("agents")
	workflowsDir := config.GetLoomSubDir("workflows")
	err := os.MkdirAll(agentsDir, 0750)
	require.NoError(t, err)
	err = os.MkdirAll(workflowsDir, 0750)
	require.NoError(t, err)

	// Create an agent
	agentYAML := `apiVersion: loom/v1
kind: Agent
metadata:
  name: agent-one
spec:
  system_prompt: "Agent one"
  tools:
    builtin: []
`
	agentPath := filepath.Join(agentsDir, "agent-one.yaml")
	err = os.WriteFile(agentPath, []byte(agentYAML), 0600)
	require.NoError(t, err)

	// Create initial workflow
	initialWorkflow := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: update-test
spec:
  type: pipeline
  initial_prompt: "Initial"
  stages:
    - agent_id: agent-one
      prompt_template: "Do something"
`
	workflowPath := filepath.Join(workflowsDir, "update-test.yaml")
	err = os.WriteFile(workflowPath, []byte(initialWorkflow), 0600)
	require.NoError(t, err)

	ctx := session.WithAgentID(context.Background(), "weaver")
	tool := NewAgentManagementTool()

	t.Run("update_with_invalid_agent_fails", func(t *testing.T) {
		params := map[string]interface{}{
			"action": "update_workflow",
			"name":   "update-test",
			"config": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "update-test",
				},
				"spec": map[string]interface{}{
					"type":           "pipeline",
					"initial_prompt": "Updated",
					"stages": []interface{}{
						map[string]interface{}{
							"agent_id":        "nonexistent-agent", // Invalid!
							"prompt_template": "Do something else",
						},
					},
				},
			},
		}

		result, err := tool.Execute(ctx, params)
		require.NoError(t, err)
		assert.False(t, result.Success, "Update should fail with invalid agent reference")
		assert.Equal(t, "INVALID_AGENT_REFERENCE", result.Error.Code)
	})
}
