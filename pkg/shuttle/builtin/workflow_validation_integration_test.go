// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShellExecuteValidatesOrchestrationWorkflows verifies that shell_execute
// automatically validates orchestration workflow YAML files.
//
// SKIPPED: Auto-validation removed from shell_execute (commit 0336205).
// Validation now handled by agent_management tool for better UX.
func TestShellExecuteValidatesOrchestrationWorkflows(t *testing.T) {
	t.Skip("Auto-validation removed from shell_execute - see agent_management tool instead")
	// Create temp directory for test
	tempDir := t.TempDir()
	workflowsDir := filepath.Join(tempDir, ".loom", "workflows")
	require.NoError(t, os.MkdirAll(workflowsDir, 0755))

	tool := NewShellExecuteTool(tempDir)

	tests := []struct {
		name           string
		command        string
		expectValid    bool
		expectValidMsg string
	}{
		{
			name: "valid_debate_workflow",
			command: `cat > .loom/workflows/test-debate.yaml <<'EOF'
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-debate
spec:
  pattern: debate
  agents:
    - id: agent1
      role: debater
      system_prompt: Test
    - id: agent2
      role: debater
      system_prompt: Test
  config:
    rounds: 2
EOF`,
			expectValid:    true,
			expectValidMsg: "✅ test-debate.yaml validated successfully",
		},
		{
			name: "valid_fork_join_workflow",
			command: `cat > .loom/workflows/test-fork-join.yaml <<'EOF'
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-fork-join
spec:
  type: fork-join
  prompt: "Test prompt"
  agent_ids:
    - agent1
    - agent2
EOF`,
			expectValid:    true,
			expectValidMsg: "✅ test-fork-join.yaml validated successfully",
		},
		{
			name: "invalid_orchestration_missing_pattern",
			command: `cat > .loom/workflows/test-invalid.yaml <<'EOF'
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-invalid
spec:
  agents:
    - id: agent1
EOF`,
			expectValid:    false,
			expectValidMsg: "Unable to determine workflow type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			params := map[string]interface{}{
				"command": tt.command,
			}

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Check if command succeeded
			assert.True(t, result.Success, "Command should succeed")

			// Check stdout for validation message
			stdout, ok := result.Data.(map[string]interface{})["stdout"].(string)
			require.True(t, ok, "Should have stdout")

			if tt.expectValid {
				assert.Contains(t, stdout, tt.expectValidMsg, "Should contain validation success message")
			} else {
				assert.Contains(t, stdout, tt.expectValidMsg, "Should contain validation error message")
			}
		})
	}
}

// TestShellExecuteValidatesMultiAgentWorkflows verifies that shell_execute
// automatically validates multi-agent workflow YAML files.
//
// SKIPPED: Auto-validation removed from shell_execute (commit 0336205).
// Validation now handled by agent_management tool for better UX.
func TestShellExecuteValidatesMultiAgentWorkflows(t *testing.T) {
	t.Skip("Auto-validation removed from shell_execute - see agent_management tool instead")
	// Create temp directory for test
	tempDir := t.TempDir()
	workflowsDir := filepath.Join(tempDir, ".loom", "workflows")
	require.NoError(t, os.MkdirAll(workflowsDir, 0755))

	tool := NewShellExecuteTool(tempDir)

	tests := []struct {
		name           string
		command        string
		expectValid    bool
		expectValidMsg string
	}{
		{
			name: "valid_multi_agent_workflow",
			command: `cat > .loom/workflows/test-coordinator.yaml <<'EOF'
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-coordinator
spec:
  entrypoint: coordinator
  agents:
    - name: coordinator
      description: Main coordinator
EOF`,
			expectValid:    true,
			expectValidMsg: "✅ test-coordinator.yaml validated successfully",
		},
		{
			name: "invalid_multi_agent_missing_entrypoint",
			command: `cat > .loom/workflows/test-invalid.yaml <<'EOF'
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-invalid
spec:
  agents:
    - name: coordinator
EOF`,
			expectValid:    false,
			expectValidMsg: "Unable to determine workflow type",
		},
		{
			name: "invalid_multi_agent_entrypoint_mismatch",
			command: `cat > .loom/workflows/test-mismatch.yaml <<'EOF'
apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-mismatch
spec:
  entrypoint: nonexistent
  agents:
    - name: coordinator
EOF`,
			expectValid:    false,
			expectValidMsg: "Entrypoint 'nonexistent' not found in agents list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			params := map[string]interface{}{
				"command": tt.command,
			}

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Check if command succeeded
			assert.True(t, result.Success, "Command should succeed")

			// Check stdout for validation message
			stdout, ok := result.Data.(map[string]interface{})["stdout"].(string)
			require.True(t, ok, "Should have stdout")

			if tt.expectValid {
				assert.Contains(t, stdout, tt.expectValidMsg, "Should contain validation success message")
			} else {
				// For invalid workflows, check that validation output is present
				assert.True(t, strings.Contains(stdout, "STRUCTURE") || strings.Contains(stdout, "SEMANTIC"),
					"Should contain validation error messages")
			}
		})
	}
}

// TestShellExecuteValidationDoesNotInterfereWithNonWorkflows verifies that
// validation only runs for workflow files, not other shell commands.
func TestShellExecuteValidationDoesNotInterfereWithNonWorkflows(t *testing.T) {
	tempDir := t.TempDir()
	tool := NewShellExecuteTool(tempDir)

	ctx := context.Background()
	params := map[string]interface{}{
		"command": "echo 'Hello, World!'",
	}

	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Check that stdout only contains the echo output, no validation messages
	stdout, ok := result.Data.(map[string]interface{})["stdout"].(string)
	require.True(t, ok)
	assert.Equal(t, "Hello, World!", strings.TrimSpace(stdout))
	assert.NotContains(t, stdout, "validated")
	assert.NotContains(t, stdout, "✅")
}
