// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package workflowviz

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractStageTitle(t *testing.T) {
	tests := []struct {
		name     string
		prompt   string
		expected string
	}{
		{
			name: "standard stage header",
			prompt: `{{previous}}

## STAGE 1: DISCOVER ACCESSIBLE DATABASES

**Goal:** Find all databases`,
			expected: "DISCOVER ACCESSIBLE DATABASES",
		},
		{
			name: "stage with extra whitespace",
			prompt: `
## STAGE 2:  Find nPath Tables

Some content`,
			expected: "Find nPath Tables",
		},
		{
			name: "no stage header",
			prompt: `Just some random content
without a proper stage header`,
			expected: "Unknown Stage",
		},
		{
			name: "multiple stage headers (takes first)",
			prompt: `## STAGE 1: First Header
## STAGE 2: Second Header`,
			expected: "First Header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractStageTitle(tt.prompt)
			if result != tt.expected {
				t.Errorf("ExtractStageTitle() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractKeyMarkers(t *testing.T) {
	tests := []struct {
		name     string
		prompt   string
		expected []string
	}{
		{
			name: "critical marker",
			prompt: `**⚠️ CRITICAL: TOKEN BUDGET LIMIT**
Some content here`,
			expected: []string{"CRITICAL", "TOKEN_BUDGET"},
		},
		{
			name: "shared memory marker",
			prompt: `Store in shared_memory for later use
And also use {{history}} for context`,
			expected: []string{"SHARED_MEMORY", "FULL_HISTORY"},
		},
		{
			name: "volatile table marker",
			prompt: `CREATE VOLATILE TABLE results AS (
  SELECT * FROM source
) WITH DATA;`,
			expected: []string{"VOLATILE_TABLE"},
		},
		{
			name:     "merged marker",
			prompt:   `✅ MERGED STAGE - combines two operations`,
			expected: []string{"MERGED"},
		},
		{
			name:     "no markers",
			prompt:   `Just normal content without any special markers`,
			expected: []string{},
		},
		{
			name: "multiple markers",
			prompt: `⚠️ CRITICAL operation with shared_memory
and VOLATILE TABLE usage`,
			expected: []string{"CRITICAL", "SHARED_MEMORY", "VOLATILE_TABLE"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractKeyMarkers(tt.prompt)
			if len(result) != len(tt.expected) {
				t.Errorf("ExtractKeyMarkers() returned %d markers, want %d\nGot: %v\nWant: %v",
					len(result), len(tt.expected), result, tt.expected)
				return
			}
			for i, marker := range result {
				if marker != tt.expected[i] {
					t.Errorf("ExtractKeyMarkers()[%d] = %q, want %q", i, marker, tt.expected[i])
				}
			}
		})
	}
}

func TestCategorizeAgent(t *testing.T) {
	tests := []struct {
		name         string
		agentID      string
		wantCategory int
		wantColor    string
		wantName     string
	}{
		{"analytics agent", "td-expert-analytics-stage-1", 0, "#4CAF50", "Analytics"},
		{"quality agent", "td-expert-quality-stage-3", 1, "#2196F3", "Quality"},
		{"performance agent", "td-expert-performance-stage-7", 2, "#FF9800", "Performance"},
		{"insights agent", "td-expert-insights-stage-10", 3, "#9C27B0", "Insights"},
		{"architecture agent", "td-expert-architecture-review", 4, "#E91E63", "Architecture"},
		{"transcend agent", "td-expert-transcend-optimizer", 5, "#00BCD4", "Transcend"},
		{"unknown agent", "custom-agent-xyz", 6, "#757575", "Other"},
		{"uppercase analytics", "TD-EXPERT-ANALYTICS-STAGE-1", 0, "#4CAF50", "Analytics"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category, color, name := CategorizeAgent(tt.agentID)
			if category != tt.wantCategory {
				t.Errorf("CategorizeAgent() category = %d, want %d", category, tt.wantCategory)
			}
			if color != tt.wantColor {
				t.Errorf("CategorizeAgent() color = %s, want %s", color, tt.wantColor)
			}
			if name != tt.wantName {
				t.Errorf("CategorizeAgent() name = %s, want %s", name, tt.wantName)
			}
		})
	}
}

func TestFindSharedMemoryConnections(t *testing.T) {
	stages := []WorkflowStage{
		{
			AgentID:        "stage-1",
			PromptTemplate: "Normal stage without shared memory",
		},
		{
			AgentID: "stage-2",
			PromptTemplate: `shared_memory_write(
				key="stage-2-results",
				value=data
			)`,
		},
		{
			AgentID:        "stage-3",
			PromptTemplate: "Another normal stage",
		},
		{
			AgentID: "stage-4",
			PromptTemplate: `shared_memory_read(
				key="stage-2-results"
			)`,
		},
	}

	connections := FindSharedMemoryConnections(stages)

	// Should find connection from stage 2 (index 1) to stage 4 (index 3)
	if len(connections) != 1 {
		t.Errorf("FindSharedMemoryConnections() found %d connections, want 1", len(connections))
		return
	}

	if connections[0].From != 1 || connections[0].To != 3 {
		t.Errorf("FindSharedMemoryConnections() = {From: %d, To: %d}, want {From: 1, To: 3}",
			connections[0].From, connections[0].To)
	}
}

func TestParseWorkflow(t *testing.T) {
	// Create a temporary YAML file
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "test-workflow.yaml")

	yamlContent := `apiVersion: loom/v1
kind: Workflow
metadata:
  name: test-workflow
  version: "1.0.0"
  description: Test workflow for unit tests
  labels:
    category: testing
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Test prompt"
    stages:
      - agent_id: test-agent-1
        prompt_template: |
          ## STAGE 1: TEST STAGE
          Test content
      - agent_id: test-agent-2
        prompt_template: |
          ## STAGE 2: ANOTHER TEST
          More test content
`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to create test YAML file: %v", err)
	}

	// Test parsing
	workflow, err := ParseWorkflow(yamlPath)
	if err != nil {
		t.Fatalf("ParseWorkflow() error = %v", err)
	}

	// Verify basic structure
	if workflow.APIVersion != "loom/v1" {
		t.Errorf("APIVersion = %q, want %q", workflow.APIVersion, "loom/v1")
	}
	if workflow.Kind != "Workflow" {
		t.Errorf("Kind = %q, want %q", workflow.Kind, "Workflow")
	}
	if workflow.Metadata.Name != "test-workflow" {
		t.Errorf("Metadata.Name = %q, want %q", workflow.Metadata.Name, "test-workflow")
	}
	if workflow.Metadata.Version != "1.0.0" {
		t.Errorf("Metadata.Version = %q, want %q", workflow.Metadata.Version, "1.0.0")
	}
	if len(workflow.Spec.Pipeline.Stages) != 2 {
		t.Errorf("len(Spec.Pipeline.Stages) = %d, want 2", len(workflow.Spec.Pipeline.Stages))
	}
}

func TestParseWorkflowInvalidPath(t *testing.T) {
	_, err := ParseWorkflow("/nonexistent/path/to/workflow.yaml")
	if err == nil {
		t.Error("ParseWorkflow() with invalid path should return error")
	}
}

func TestParseWorkflowInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "invalid.yaml")

	invalidYAML := `this is not valid: yaml: content:
  - broken
    structure`

	if err := os.WriteFile(yamlPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	_, err := ParseWorkflow(yamlPath)
	if err == nil {
		t.Error("ParseWorkflow() with invalid YAML should return error")
	}
}

func TestExtractKeyInstructions(t *testing.T) {
	prompt := `
**Goal:** Discover all accessible databases

**Tasks:**
1. Query system tables
2. Filter results

⚠️ CRITICAL: Must handle permissions correctly

✅ MERGED stage for efficiency
`

	instructions := ExtractKeyInstructions(prompt)

	if len(instructions) < 2 {
		t.Errorf("ExtractKeyInstructions() returned %d instructions, want at least 2", len(instructions))
	}

	// Should extract the goal
	foundGoal := false
	for _, instr := range instructions {
		if instr == " Discover all accessible databases" {
			foundGoal = true
			break
		}
	}
	if !foundGoal {
		t.Errorf("ExtractKeyInstructions() did not extract Goal")
	}
}
