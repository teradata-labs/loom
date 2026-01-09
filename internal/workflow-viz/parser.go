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
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowMetadata represents the workflow metadata
type WorkflowMetadata struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	Description string            `yaml:"description"`
	Labels      map[string]string `yaml:"labels"`
}

// WorkflowStage represents a single stage in the pipeline
type WorkflowStage struct {
	AgentID        string `yaml:"agent_id"`
	PromptTemplate string `yaml:"prompt_template"`
}

// WorkflowPipeline represents the pipeline configuration
type WorkflowPipeline struct {
	InitialPrompt string          `yaml:"initial_prompt"`
	Stages        []WorkflowStage `yaml:"stages"`
}

// WorkflowSpec represents the workflow specification
type WorkflowSpec struct {
	Type            string           `yaml:"type"`
	MaxIterations   int              `yaml:"max_iterations"`
	RestartTopic    string           `yaml:"restart_topic"`
	RestartPolicy   map[string]any   `yaml:"restart_policy"`
	RestartTriggers []string         `yaml:"restart_triggers"`
	Pipeline        WorkflowPipeline `yaml:"pipeline"`
}

// Workflow represents the complete workflow structure
type Workflow struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   WorkflowMetadata `yaml:"metadata"`
	Spec       WorkflowSpec     `yaml:"spec"`
}

// ParseWorkflow reads and parses a workflow YAML file
func ParseWorkflow(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading workflow file: %w", err)
	}

	var workflow Workflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	return &workflow, nil
}

// ExtractStageTitle extracts the stage title from prompt template
// Looks for pattern: ## STAGE N: TITLE
func ExtractStageTitle(promptTemplate string) string {
	lines := strings.Split(promptTemplate, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## STAGE") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "Unknown Stage"
}

// ExtractKeyMarkers identifies important markers in the prompt
func ExtractKeyMarkers(promptTemplate string) []string {
	markers := []string{}
	lines := strings.Split(promptTemplate, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Critical markers
		if strings.Contains(line, "⚠️ CRITICAL") {
			markers = append(markers, "CRITICAL")
		}
		if strings.Contains(line, "TOKEN BUDGET") || strings.Contains(line, "🔴 TOKEN BUDGET") {
			markers = append(markers, "TOKEN_BUDGET")
		}
		if strings.Contains(line, "✅ MERGED") {
			markers = append(markers, "MERGED")
		}
		if strings.Contains(line, "{{history}}") {
			markers = append(markers, "FULL_HISTORY")
		}
		if strings.Contains(line, "shared_memory") {
			markers = append(markers, "SHARED_MEMORY")
		}
		if strings.Contains(line, "VOLATILE TABLE") {
			markers = append(markers, "VOLATILE_TABLE")
		}
	}

	return markers
}

// ExtractKeyInstructions extracts important instructions from prompt
func ExtractKeyInstructions(promptTemplate string) []string {
	instructions := []string{}
	lines := strings.Split(promptTemplate, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Goal and task markers
		if strings.HasPrefix(line, "**Goal:**") {
			instructions = append(instructions, strings.TrimPrefix(line, "**Goal:**"))
		}
		// Critical information
		if strings.Contains(line, "⚠️ CRITICAL") || strings.Contains(line, "🔴 TOKEN BUDGET") {
			instructions = append(instructions, line)
		}
		if strings.Contains(line, "✅ MERGED") || strings.Contains(line, "VOLATILE TABLE") {
			instructions = append(instructions, line)
		}
	}

	return instructions
}

// CategorizeAgent returns category info based on agent ID
func CategorizeAgent(agentID string) (category int, color string, categoryName string) {
	agentLower := strings.ToLower(agentID)
	switch {
	case strings.Contains(agentLower, "analytics"):
		return 0, "#4CAF50", "Analytics"
	case strings.Contains(agentLower, "quality"):
		return 1, "#2196F3", "Quality"
	case strings.Contains(agentLower, "performance"):
		return 2, "#FF9800", "Performance"
	case strings.Contains(agentLower, "insights"):
		return 3, "#9C27B0", "Insights"
	case strings.Contains(agentLower, "architecture"):
		return 4, "#E91E63", "Architecture"
	case strings.Contains(agentLower, "transcend"):
		return 5, "#00BCD4", "Transcend"
	default:
		return 6, "#757575", "Other"
	}
}

// FindSharedMemoryConnections detects shared memory connections between stages
func FindSharedMemoryConnections(stages []WorkflowStage) []struct{ From, To int } {
	connections := []struct{ From, To int }{}

	for i, stage := range stages {
		if !strings.Contains(stage.PromptTemplate, "shared_memory") {
			continue
		}

		// Check if later stages reference this stage's data
		for j := i + 2; j < len(stages); j++ {
			laterStage := stages[j]
			// Look for explicit stage references or shared_memory reads
			if strings.Contains(laterStage.PromptTemplate, fmt.Sprintf("stage-%d", i+1)) ||
				(strings.Contains(laterStage.PromptTemplate, "shared_memory_read") &&
					strings.Contains(stage.PromptTemplate, "shared_memory_write")) {
				connections = append(connections, struct{ From, To int }{From: i, To: j})
				break // Only add one connection per source stage
			}
		}
	}

	return connections
}
