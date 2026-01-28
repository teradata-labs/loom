// Copyright 2025 Loom Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workflows

import (
	"path/filepath"
	"testing"

	"github.com/teradata-labs/loom/pkg/orchestration"
)

// TestAgentPathReferences validates that agents can be loaded from path references
func TestAgentPathReferences(t *testing.T) {
	path := filepath.Join("test-data", "path-reference-workflow.yaml")

	pattern, metadata, err := orchestration.ParseWorkflowFromYAML(path)
	if err != nil {
		t.Fatalf("Failed to parse workflow with path references: %v", err)
	}

	if pattern == nil {
		t.Fatal("Expected non-nil pattern")
	}

	if metadata == nil {
		t.Fatal("Expected non-nil metadata")
	}

	// Verify it's a pipeline pattern
	patternType := orchestration.GetPatternType(pattern)
	if patternType != "pipeline" {
		t.Errorf("Expected pattern type 'pipeline', got '%s'", patternType)
	}

	// Verify 2 agents were loaded
	agentIDs := orchestration.ExtractAgentIDs(pattern)
	if len(agentIDs) != 2 {
		t.Errorf("Expected 2 agents, got %d: %v", len(agentIDs), agentIDs)
	}

	// Verify agent IDs
	expectedIDs := []string{"test-agent-1", "test-agent-2"}
	for i, expectedID := range expectedIDs {
		if i >= len(agentIDs) {
			t.Errorf("Missing agent ID: %s", expectedID)
			continue
		}
		if agentIDs[i] != expectedID {
			t.Errorf("Expected agent ID '%s', got '%s'", expectedID, agentIDs[i])
		}
	}
}

// TestAgentPathOverrides validates that workflow can override fields from path-loaded agents
func TestAgentPathOverrides(t *testing.T) {
	path := filepath.Join("test-data", "path-override-workflow.yaml")

	pattern, metadata, err := orchestration.ParseWorkflowFromYAML(path)
	if err != nil {
		t.Fatalf("Failed to parse workflow with path overrides: %v", err)
	}

	if pattern == nil {
		t.Fatal("Expected non-nil pattern")
	}

	if metadata == nil {
		t.Fatal("Expected non-nil metadata")
	}

	// Verify it's a fork_join pattern
	patternType := orchestration.GetPatternType(pattern)
	if patternType != "fork_join" {
		t.Errorf("Expected pattern type 'fork_join', got '%s'", patternType)
	}

	// Verify 1 agent was loaded
	agentIDs := orchestration.ExtractAgentIDs(pattern)
	if len(agentIDs) != 1 {
		t.Errorf("Expected 1 agent, got %d: %v", len(agentIDs), agentIDs)
	}

	// Verify the agent ID
	if agentIDs[0] != "test-agent-override" {
		t.Errorf("Expected agent ID 'test-agent-override', got '%s'", agentIDs[0])
	}
}

// TestMixedInlineAndPathAgents validates workflows with both inline and path-referenced agents
func TestMixedInlineAndPathAgents(t *testing.T) {
	path := filepath.Join("test-data", "mixed-agents-workflow.yaml")

	pattern, metadata, err := orchestration.ParseWorkflowFromYAML(path)
	if err != nil {
		t.Fatalf("Failed to parse workflow with mixed agents: %v", err)
	}

	if pattern == nil {
		t.Fatal("Expected non-nil pattern")
	}

	if metadata == nil {
		t.Fatal("Expected non-nil metadata")
	}

	// Verify it's a parallel pattern
	patternType := orchestration.GetPatternType(pattern)
	if patternType != "parallel" {
		t.Errorf("Expected pattern type 'parallel', got '%s'", patternType)
	}

	// Verify 3 agents (2 path-referenced + 1 inline)
	agentIDs := orchestration.ExtractAgentIDs(pattern)
	if len(agentIDs) != 3 {
		t.Errorf("Expected 3 agents, got %d: %v", len(agentIDs), agentIDs)
	}

	// Verify agent IDs
	expectedIDs := []string{"path-agent", "inline-agent", "another-path-agent"}
	for i, expectedID := range expectedIDs {
		if i >= len(agentIDs) {
			t.Errorf("Missing agent ID: %s", expectedID)
			continue
		}
		if agentIDs[i] != expectedID {
			t.Errorf("Expected agent ID '%s' at position %d, got '%s'", expectedID, i, agentIDs[i])
		}
	}
}

// TestInvalidAgentPath validates error handling for non-existent agent path
func TestInvalidAgentPath(t *testing.T) {
	path := filepath.Join("test-data", "invalid-path-workflow.yaml")

	_, _, err := orchestration.ParseWorkflowFromYAML(path)
	if err == nil {
		t.Fatal("Expected error for non-existent agent path")
	}

	// Verify error mentions the failed path resolution
	errMsg := err.Error()
	if !contains(errMsg, "failed to resolve agent definitions") && !contains(errMsg, "failed to load agent") {
		t.Errorf("Expected error about failed path resolution, got: %s", errMsg)
	}
}

// TestInvalidAgentConfig validates error handling for invalid agent config file
func TestInvalidAgentConfig(t *testing.T) {
	path := filepath.Join("test-data", "invalid-config-workflow.yaml")

	_, _, err := orchestration.ParseWorkflowFromYAML(path)
	if err == nil {
		t.Fatal("Expected error for invalid agent config")
	}

	// Verify error mentions system_prompt is required
	errMsg := err.Error()
	if !contains(errMsg, "system_prompt is required") {
		t.Errorf("Expected error about missing system_prompt, got: %s", errMsg)
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsRec(s, substr))
}

func containsRec(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
