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

func TestAgentManagementTool_AccessControl(t *testing.T) {
	tool := NewAgentManagementTool()

	tests := []struct {
		name      string
		agentID   string
		expectErr bool
	}{
		{
			name:      "weaver agent allowed",
			agentID:   "weaver",
			expectErr: false,
		},
		{
			name:      "other agent denied",
			agentID:   "other-agent",
			expectErr: true,
		},
		{
			name:      "empty agent ID denied",
			agentID:   "",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.agentID != "" {
				ctx = session.WithAgentID(ctx, tt.agentID)
			}

			params := map[string]interface{}{
				"action": "list",
				"type":   "agent",
			}

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)

			if tt.expectErr {
				assert.False(t, result.Success)
				assert.Equal(t, "UNAUTHORIZED", result.Error.Code)
			} else {
				// Should succeed or fail for other reasons, but not unauthorized
				if !result.Success && result.Error != nil {
					assert.NotEqual(t, "UNAUTHORIZED", result.Error.Code)
				}
			}
		})
	}
}

func TestAgentManagementTool_CreateAgent(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	// Use temp directory for testing
	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	tests := []struct {
		name        string
		params      map[string]interface{}
		expectError bool
		errorCode   string
	}{
		{
			name: "valid agent creation",
			params: map[string]interface{}{
				"action": "create_agent",
				"config": map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":        "test-agent",
						"description": "Test agent",
					},
					"spec": map[string]interface{}{
						"system_prompt": "You are a test agent",
						"tools":         []string{"shell_execute"},
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing required field",
			params: map[string]interface{}{
				"action": "create_agent",
				"config": map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "incomplete-agent",
					},
					"spec": map[string]interface{}{
						// Missing system_prompt!
						"tools": []string{"shell_execute"},
					},
				},
			},
			expectError: true,
			errorCode:   "CONVERSION_ERROR",
		},
		{
			name: "file already exists",
			params: map[string]interface{}{
				"action": "create_agent",
				"config": map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":        "test-agent", // Same as first test
						"description": "Test agent",
					},
					"spec": map[string]interface{}{
						"system_prompt": "You are a test agent",
						"tools":         []string{"shell_execute"},
					},
				},
			},
			expectError: true,
			errorCode:   "FILE_EXISTS",
		},
		{
			name: "old create action returns migration error",
			params: map[string]interface{}{
				"action":  "create",
				"type":    "agent",
				"name":    "old-style-agent",
				"content": "some yaml",
			},
			expectError: true,
			errorCode:   "INVALID_ACTION",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(ctx, tt.params)
			require.NoError(t, err)

			if tt.expectError {
				assert.False(t, result.Success)
				assert.NotNil(t, result.Error)
				if tt.errorCode != "" {
					assert.Equal(t, tt.errorCode, result.Error.Code)
				}
			} else {
				assert.True(t, result.Success, "Expected success but got: %v", result.Error)

				// Verify file was created
				agentsDir := config.GetLoomSubDir("agents")
				filePath := filepath.Join(agentsDir, "test-agent.yaml")
				assert.FileExists(t, filePath)

				// Verify YAML structure
				content, err := os.ReadFile(filePath)
				require.NoError(t, err)
				yamlStr := string(content)
				assert.Contains(t, yamlStr, "apiVersion: loom/v1")
				assert.Contains(t, yamlStr, "kind: Agent")
				assert.Contains(t, yamlStr, "name: test-agent")
			}
		})
	}
}

func TestAgentManagementTool_UpdateAgent(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	// Create initial agent using new API
	createParams := map[string]interface{}{
		"action": "create_agent",
		"config": map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":        "update-test",
				"description": "Original",
			},
			"spec": map[string]interface{}{
				"system_prompt": "Original prompt",
				"tools":         []string{},
			},
		},
	}

	result, err := tool.Execute(ctx, createParams)
	require.NoError(t, err)
	require.True(t, result.Success, "Failed to create initial agent: %v", result.Error)

	// Test update using new API
	updateParams := map[string]interface{}{
		"action": "update_agent",
		"name":   "update-test",
		"config": map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":        "update-test",
				"description": "Updated",
				"version":     "2.0.0",
			},
			"spec": map[string]interface{}{
				"system_prompt": "Updated prompt",
				"tools":         []string{"shell_execute"},
			},
		},
	}

	result, err = tool.Execute(ctx, updateParams)
	require.NoError(t, err)
	assert.True(t, result.Success, "Update failed: %v", result.Error)

	// Verify file was updated
	agentsDir := config.GetLoomSubDir("agents")
	filePath := filepath.Join(agentsDir, "update-test.yaml")
	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	yamlStr := string(content)
	assert.Contains(t, yamlStr, "Updated prompt")
	assert.Contains(t, yamlStr, "version: 2.0.0")
}

func TestAgentManagementTool_ReadAgent(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	// Create test agent
	agentsDir := config.GetLoomSubDir("agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	testContent := `apiVersion: loom/v1
kind: Agent
metadata:
  name: read-test
`

	filePath := filepath.Join(agentsDir, "read-test.yaml")
	err = os.WriteFile(filePath, []byte(testContent), 0644)
	require.NoError(t, err)

	// Test read
	params := map[string]interface{}{
		"action": "read",
		"type":   "agent",
		"name":   "read-test",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, testContent, data["content"])
}

func TestAgentManagementTool_ListAgents(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	// Create multiple agents
	agentsDir := config.GetLoomSubDir("agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	agents := []string{"agent1.yaml", "agent2.yaml", "agent3.yml"}
	for _, name := range agents {
		filePath := filepath.Join(agentsDir, name)
		err := os.WriteFile(filePath, []byte("test"), 0644)
		require.NoError(t, err)
	}

	// Test list
	params := map[string]interface{}{
		"action": "list",
		"type":   "agent",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 3, data["count"])
}

func TestAgentManagementTool_ValidateOnly(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	tests := []struct {
		name        string
		content     string
		expectValid bool
	}{
		{
			name: "valid YAML",
			content: `apiVersion: loom/v1
kind: Agent
metadata:
  name: test
spec:
  system_prompt: "Test"
  tools: []
`,
			expectValid: true,
		},
		{
			name:        "invalid YAML syntax",
			content:     "invalid: [yaml",
			expectValid: false,
		},
		{
			name: "missing required fields",
			content: `apiVersion: loom/v1
kind: Agent
spec:
  system_prompt: "Test"
`,
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"action":  "validate",
				"type":    "agent",
				"content": tt.content,
			}

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)

			data, ok := result.Data.(map[string]interface{})
			require.True(t, ok)

			valid, ok := data["valid"].(bool)
			require.True(t, ok)
			assert.Equal(t, tt.expectValid, valid)
		})
	}
}

func TestAgentManagementTool_DeleteAgent(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	// Create test agent
	agentsDir := config.GetLoomSubDir("agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	filePath := filepath.Join(agentsDir, "delete-test.yaml")
	err = os.WriteFile(filePath, []byte("test"), 0644)
	require.NoError(t, err)

	// Test delete
	params := map[string]interface{}{
		"action": "delete",
		"type":   "agent",
		"name":   "delete-test",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify file was deleted
	assert.NoFileExists(t, filePath)
}

func TestAgentManagementTool_WorkflowOperations(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	// Create test agents first (workflows need to reference them)
	agentsDir := config.GetLoomSubDir("agents")
	err := os.MkdirAll(agentsDir, 0755)
	require.NoError(t, err)

	agent1YAML := `apiVersion: loom/v1
kind: Agent
metadata:
  name: agent1
spec:
  system_prompt: "Agent 1"
  tools: []
`
	err = os.WriteFile(filepath.Join(agentsDir, "agent1.yaml"), []byte(agent1YAML), 0644)
	require.NoError(t, err)

	agent2YAML := `apiVersion: loom/v1
kind: Agent
metadata:
  name: agent2
spec:
  system_prompt: "Agent 2"
  tools: []
`
	err = os.WriteFile(filepath.Join(agentsDir, "agent2.yaml"), []byte(agent2YAML), 0644)
	require.NoError(t, err)

	// Test create workflow using new API
	params := map[string]interface{}{
		"action": "create_workflow",
		"config": map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":        "test-workflow",
				"description": "Test workflow",
			},
			"spec": map[string]interface{}{
				"type":      "fork-join",
				"prompt":    "Test prompt",
				"agent_ids": []string{"agent1", "agent2"},
			},
		},
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success, "Workflow creation failed: %v", result.Error)

	// Verify file was created in workflows directory
	workflowsDir := config.GetLoomSubDir("workflows")
	filePath := filepath.Join(workflowsDir, "test-workflow.yaml")
	assert.FileExists(t, filePath)

	// Test list workflows
	listParams := map[string]interface{}{
		"action": "list",
		"type":   "workflow",
	}

	result, err = tool.Execute(ctx, listParams)
	require.NoError(t, err)
	assert.True(t, result.Success)

	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 1, data["count"])
}

func TestAgentManagementTool_ConcurrentOperations(t *testing.T) {
	// Setup
	tool := NewAgentManagementTool()
	ctx := session.WithAgentID(context.Background(), "weaver")

	tmpDir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", tmpDir)

	// Create agent first using new API
	params := map[string]interface{}{
		"action": "create_agent",
		"config": map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":        "concurrent-test",
				"description": "Test concurrent operations",
			},
			"spec": map[string]interface{}{
				"system_prompt": "Test agent",
				"tools":         []string{},
			},
		},
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.True(t, result.Success, "Failed to create agent: %v", result.Error)

	// Run concurrent reads
	done := make(chan bool)
	errors := make(chan error, 10)

	readParams := map[string]interface{}{
		"action": "read",
		"type":   "agent",
		"name":   "concurrent-test",
	}

	for i := 0; i < 10; i++ {
		go func() {
			result, err := tool.Execute(ctx, readParams)
			if err != nil {
				errors <- err
			} else if !result.Success {
				errors <- assert.AnError
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	close(errors)
	assert.Empty(t, errors, "Expected no errors in concurrent reads")
}
