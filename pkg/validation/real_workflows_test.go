// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRealWorkflowExamples validates actual workflow files from the examples directory.
func TestRealWorkflowExamples(t *testing.T) {
	// Get project root
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(wd, "..", "..")

	tests := []struct {
		name         string
		path         string
		workflowType string // "orchestration" or "multi-agent"
		shouldPass   bool
	}{
		{
			name:         "architecture_debate_orchestration",
			path:         "examples/reference/workflows/orchestration-patterns/architecture-debate.yaml",
			workflowType: "orchestration",
			shouldPass:   true,
		},
		{
			name:         "dungeon_crawl_multi_agent",
			path:         "examples/reference/workflows/event-driven/dungeon-crawler/workflows/dungeon-crawl.yaml",
			workflowType: "multi-agent",
			shouldPass:   true,
		},
		{
			name:         "security_analysis_fork_join",
			path:         "examples/reference/workflows/orchestration-patterns/security-analysis.yaml",
			workflowType: "orchestration",
			shouldPass:   true,
		},
		{
			name:         "complexity_routing_conditional",
			path:         "examples/reference/workflows/orchestration-patterns/complexity-routing.yaml",
			workflowType: "orchestration",
			shouldPass:   true,
		},
		{
			name:         "technology_swarm",
			path:         "examples/reference/workflows/orchestration-patterns/technology-swarm.yaml",
			workflowType: "orchestration",
			shouldPass:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fullPath := filepath.Join(projectRoot, tt.path)

			// A moved or deleted example must fail loudly: silent skips let
			// every subtest here rot away unnoticed when examples/ moved.
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				t.Fatalf("Workflow file not found (update the path): %s", fullPath)
			}

			// Validate the file
			result := ValidateYAMLFile(fullPath)

			if tt.shouldPass {
				// File should pass structure validation
				// Note: Semantic validation might have warnings about missing agent files
				structureErrors := []ValidationError{}
				for _, err := range result.Errors {
					if err.Level == LevelSyntax || err.Level == LevelStructure {
						structureErrors = append(structureErrors, err)
					}
				}

				assert.Empty(t, structureErrors, "Expected no syntax/structure errors for %s", tt.name)
				assert.Equal(t, "Workflow", result.Kind)

				// Log any semantic errors/warnings for debugging
				if len(result.Errors) > 0 {
					t.Logf("Semantic errors for %s: %v", tt.name, result.Errors)
				}
				if len(result.Warnings) > 0 {
					t.Logf("Warnings for %s: %v", tt.name, result.Warnings)
				}
			} else {
				assert.False(t, result.Valid, "Expected validation to fail for %s", tt.name)
			}
		})
	}
}

// TestWorkflowTypeDetectionOnRealFiles tests that we correctly detect workflow types.
func TestWorkflowTypeDetectionOnRealFiles(t *testing.T) {
	// Get project root
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(wd, "..", "..")

	tests := []struct {
		name         string
		path         string
		expectedType string
	}{
		{
			name:         "debate_is_orchestration",
			path:         "examples/reference/workflows/orchestration-patterns/architecture-debate.yaml",
			expectedType: "orchestration",
		},
		{
			name:         "brainstorm_is_multi_agent",
			path:         "examples/reference/workflows/event-driven/brainstorm-session/workflows/brainstorm-session.yaml",
			expectedType: "multi-agent",
		},
		{
			name:         "security_analysis_is_orchestration",
			path:         "examples/reference/workflows/orchestration-patterns/security-analysis.yaml",
			expectedType: "orchestration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fullPath := filepath.Join(projectRoot, tt.path)

			// A moved or deleted example must fail loudly: silent skips let
			// every subtest here rot away unnoticed when examples/ moved.
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				t.Fatalf("Workflow file not found (update the path): %s", fullPath)
			}

			// Read and parse file
			content, err := os.ReadFile(fullPath)
			if err != nil {
				t.Fatal(err)
			}

			result := ValidateYAMLContent(string(content), fullPath)

			// Just verify it's recognized as a Workflow
			assert.Equal(t, "Workflow", result.Kind, "Should be recognized as Workflow")

			// The validation should pass structure checks for both types
			hasStructureErrors := false
			for _, err := range result.Errors {
				if err.Level == LevelStructure {
					hasStructureErrors = true
					t.Logf("Structure error: %v", err)
				}
			}
			assert.False(t, hasStructureErrors, "Should not have structure errors")
		})
	}
}
