// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
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
