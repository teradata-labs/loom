// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test workflow YAML content
const testWorkflowYAML = `apiVersion: loom.teradata.com/v1
kind: Workflow
metadata:
  name: test-workflow
  version: "1.0.0"
  description: Test workflow for visualization
  labels:
    domain: analytics
spec:
  type: pipeline
  max_iterations: 3
  restart_topic: workflow_restart
  restart_policy:
    enabled: true
    max_retries: 3
  restart_triggers:
    - trigger_restart
  pipeline:
    initial_prompt: "Begin analysis"
    stages:
      - agent_id: analytics-stage-1
        prompt_template: |
          ## STAGE 1: Data Collection

          **Goal:** Collect and validate input data

          ⚠️ CRITICAL: Ensure data quality

      - agent_id: quality-stage-2
        prompt_template: |
          ## STAGE 2: Quality Analysis

          **Goal:** Analyze data quality metrics

          Use shared_memory_write to store results.

      - agent_id: insights-stage-3
        prompt_template: |
          ## STAGE 3: Generate Insights

          **Goal:** Generate actionable insights

          Read from shared_memory stage-2 results.
          ✅ MERGED results from previous stages.
`

// TestWorkflowVisualizationTool_BasicFunctionality tests successful workflow visualization.
func TestWorkflowVisualizationTool_BasicFunctionality(t *testing.T) {
	// Create temporary workflow file
	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "test-workflow.yaml")
	err := os.WriteFile(workflowPath, []byte(testWorkflowYAML), 0644)
	require.NoError(t, err)

	outputPath := filepath.Join(tmpDir, "output.html")

	// Create tool
	tool := NewWorkflowVisualizationTool()

	// Execute
	params := map[string]interface{}{
		"workflow_path": workflowPath,
		"output_path":   outputPath,
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify output file exists
	_, err = os.Stat(outputPath)
	assert.NoError(t, err, "Output HTML file should exist")

	// Verify result data
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)

	assert.Contains(t, data["output_path"], "output.html")
	assert.Equal(t, "test-workflow", data["workflow_name"])
	assert.Equal(t, "1.0.0", data["workflow_version"])
	assert.Equal(t, 3, data["stages_count"])
	assert.Equal(t, "pipeline", data["workflow_type"])
	assert.Equal(t, true, data["visualization_ready"])
	assert.Equal(t, 3, data["max_iterations"])

	// Verify HTML content contains expected elements
	htmlContent, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	htmlStr := string(htmlContent)

	assert.Contains(t, htmlStr, "test-workflow")
	assert.Contains(t, htmlStr, "Stage 1")
	assert.Contains(t, htmlStr, "Stage 2")
	assert.Contains(t, htmlStr, "Stage 3")
	assert.Contains(t, htmlStr, "echarts")
	assert.Contains(t, htmlStr, "Data Collection")
	assert.Contains(t, htmlStr, "Quality Analysis")
	assert.Contains(t, htmlStr, "Generate Insights")

	// Verify execution time is recorded
	assert.Greater(t, result.ExecutionTimeMs, int64(0))
}

// TestWorkflowVisualizationTool_MissingWorkflowPath tests error when workflow_path is missing.
func TestWorkflowVisualizationTool_MissingWorkflowPath(t *testing.T) {
	tool := NewWorkflowVisualizationTool()

	params := map[string]interface{}{
		"output_path": "/tmp/output.html",
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error.Message, "workflow_path is required")
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
}

// TestWorkflowVisualizationTool_MissingOutputPath tests error when output_path is missing.
func TestWorkflowVisualizationTool_MissingOutputPath(t *testing.T) {
	tool := NewWorkflowVisualizationTool()

	params := map[string]interface{}{
		"workflow_path": "/tmp/workflow.yaml",
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error.Message, "output_path is required")
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
}

// TestWorkflowVisualizationTool_WorkflowFileNotFound tests error when workflow file doesn't exist.
func TestWorkflowVisualizationTool_WorkflowFileNotFound(t *testing.T) {
	tool := NewWorkflowVisualizationTool()

	tmpDir := t.TempDir()
	params := map[string]interface{}{
		"workflow_path": filepath.Join(tmpDir, "nonexistent.yaml"),
		"output_path":   filepath.Join(tmpDir, "output.html"),
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error.Message, "Workflow file not found")
	assert.Equal(t, "FILE_NOT_FOUND", result.Error.Code)
}

// TestWorkflowVisualizationTool_InvalidYAML tests error when workflow YAML is invalid.
func TestWorkflowVisualizationTool_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "invalid.yaml")

	// Write invalid YAML
	err := os.WriteFile(workflowPath, []byte("invalid: yaml: content: [unclosed"), 0644)
	require.NoError(t, err)

	tool := NewWorkflowVisualizationTool()
	params := map[string]interface{}{
		"workflow_path": workflowPath,
		"output_path":   filepath.Join(tmpDir, "output.html"),
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error.Message, "Failed to parse workflow YAML")
	assert.Equal(t, "PARSE_ERROR", result.Error.Code)
}

// TestWorkflowVisualizationTool_OutputDirectoryCreation tests that output directory is created.
func TestWorkflowVisualizationTool_OutputDirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "test-workflow.yaml")
	err := os.WriteFile(workflowPath, []byte(testWorkflowYAML), 0644)
	require.NoError(t, err)

	// Use nested directory that doesn't exist yet
	outputPath := filepath.Join(tmpDir, "nested", "dir", "output.html")

	tool := NewWorkflowVisualizationTool()
	params := map[string]interface{}{
		"workflow_path": workflowPath,
		"output_path":   outputPath,
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify output directory was created
	_, err = os.Stat(filepath.Dir(outputPath))
	assert.NoError(t, err, "Output directory should be created")

	// Verify output file exists
	_, err = os.Stat(outputPath)
	assert.NoError(t, err, "Output HTML file should exist")
}

// TestWorkflowVisualizationTool_PathExpansion tests that ~/ path is expanded.
func TestWorkflowVisualizationTool_PathExpansion(t *testing.T) {
	// Test the expandPath helper function
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "home directory expansion",
			input:    "~/test/file.txt",
			expected: filepath.Join(home, "test/file.txt"),
		},
		{
			name:     "no expansion needed",
			input:    "/absolute/path/file.txt",
			expected: "/absolute/path/file.txt",
		},
		{
			name:     "relative path",
			input:    "relative/path/file.txt",
			expected: "relative/path/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestWorkflowVisualizationTool_MinimalWorkflow tests visualization of minimal workflow.
func TestWorkflowVisualizationTool_MinimalWorkflow(t *testing.T) {
	minimalWorkflowYAML := `apiVersion: loom.teradata.com/v1
kind: Workflow
metadata:
  name: minimal
  version: "1.0"
  description: Minimal workflow
spec:
  type: pipeline
  pipeline:
    initial_prompt: "Start"
    stages:
      - agent_id: test-agent
        prompt_template: "## STAGE 1: Test"
`

	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "minimal.yaml")
	err := os.WriteFile(workflowPath, []byte(minimalWorkflowYAML), 0644)
	require.NoError(t, err)

	outputPath := filepath.Join(tmpDir, "minimal-output.html")

	tool := NewWorkflowVisualizationTool()
	params := map[string]interface{}{
		"workflow_path": workflowPath,
		"output_path":   outputPath,
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, params)
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify result
	data := result.Data.(map[string]interface{})
	assert.Equal(t, "minimal", data["workflow_name"])
	assert.Equal(t, 1, data["stages_count"])

	// max_iterations should not be in result when not set in workflow
	_, hasMaxIterations := data["max_iterations"]
	assert.False(t, hasMaxIterations, "max_iterations should not be present when not set in workflow")
}

// TestWorkflowVisualizationTool_ToolMetadata tests tool metadata methods.
func TestWorkflowVisualizationTool_ToolMetadata(t *testing.T) {
	tool := NewWorkflowVisualizationTool()

	// Test Name
	assert.Equal(t, "generate_workflow_visualization", tool.Name())

	// Test Description
	description := tool.Description()
	assert.Contains(t, description, "workflow")
	assert.Contains(t, description, "visualization")
	assert.Contains(t, description, "HTML")
	assert.Contains(t, description, "ECharts")

	// Test InputSchema
	schema := tool.InputSchema()
	assert.Equal(t, "object", schema.Type)
	assert.Contains(t, schema.Properties, "workflow_path")
	assert.Contains(t, schema.Properties, "output_path")
	assert.Contains(t, schema.Required, "workflow_path")
	assert.Contains(t, schema.Required, "output_path")

	// Test Backend
	assert.Equal(t, "", tool.Backend(), "Tool should be backend-agnostic")
}

// TestVisualizationTools_IncludesWorkflowViz tests that VisualizationTools includes workflow viz tool.
func TestVisualizationTools_IncludesWorkflowViz(t *testing.T) {
	// Viz tools are now in VisualizationTools(), NOT PresentationTools()
	tools := VisualizationTools()
	assert.Equal(t, 2, len(tools), "Should have 2 viz tools")

	// Find workflow viz tool
	var foundWorkflowViz bool
	for _, tool := range tools {
		if tool.Name() == "generate_workflow_visualization" {
			foundWorkflowViz = true
			break
		}
	}
	assert.True(t, foundWorkflowViz, "Workflow visualization tool should be included in VisualizationTools")

	// Test PresentationTools with store (should have only query tools, NOT viz tools)
	store := createTestPresentationStore(t)
	presentationTools := PresentationTools(store, "test-agent")
	assert.Equal(t, 2, len(presentationTools), "Should have 2 query tools with store")

	// Verify query tool names
	toolNames := make(map[string]bool)
	for _, tool := range presentationTools {
		toolNames[tool.Name()] = true
	}

	assert.True(t, toolNames["top_n_query"])
	assert.True(t, toolNames["group_by_query"])
	// Viz tools should NOT be in PresentationTools
	assert.False(t, toolNames["generate_workflow_visualization"])
	assert.False(t, toolNames["generate_visualization"])
}

// TestVisualizationToolNames_IncludesWorkflowViz tests viz tool names registry.
func TestVisualizationToolNames_IncludesWorkflowViz(t *testing.T) {
	names := VisualizationToolNames()
	assert.Equal(t, 2, len(names))
	assert.Contains(t, names, "generate_workflow_visualization")
	assert.Contains(t, names, "generate_visualization")

	// Verify PresentationToolNames does NOT include viz tools
	presentationNames := PresentationToolNames()
	assert.Equal(t, 2, len(presentationNames))
	assert.Contains(t, presentationNames, "top_n_query")
	assert.Contains(t, presentationNames, "group_by_query")
	assert.NotContains(t, presentationNames, "generate_workflow_visualization")
}

// TestWorkflowVisualizationTool_ConcurrentExecution tests concurrent tool usage (race detection).
func TestWorkflowVisualizationTool_ConcurrentExecution(t *testing.T) {
	tmpDir := t.TempDir()
	workflowPath := filepath.Join(tmpDir, "test-workflow.yaml")
	err := os.WriteFile(workflowPath, []byte(testWorkflowYAML), 0644)
	require.NoError(t, err)

	tool := NewWorkflowVisualizationTool()
	ctx := context.Background()
	done := make(chan bool)

	// Run 5 concurrent visualizations
	for i := 0; i < 5; i++ {
		go func(idx int) {
			outputPath := filepath.Join(tmpDir, fmt.Sprintf("output-%d.html", idx))
			params := map[string]interface{}{
				"workflow_path": workflowPath,
				"output_path":   outputPath,
			}

			result, err := tool.Execute(ctx, params)
			require.NoError(t, err)
			assert.True(t, result.Success)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}
}
