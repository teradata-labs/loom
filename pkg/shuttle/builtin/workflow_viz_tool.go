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
	"strings"
	"time"

	workflowviz "github.com/teradata-labs/loom/internal/workflow-viz"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// WorkflowVisualizationTool provides workflow visualization capability for agents.
// Converts workflow YAML files to interactive ECharts HTML visualizations.
// Pattern: Transform workflow definition into visual diagram for analysis and documentation.
type WorkflowVisualizationTool struct{}

// NewWorkflowVisualizationTool creates a new workflow visualization tool.
func NewWorkflowVisualizationTool() *WorkflowVisualizationTool {
	return &WorkflowVisualizationTool{}
}

func (t *WorkflowVisualizationTool) Name() string {
	return "generate_workflow_visualization"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/presentation.yaml).
// This fallback is used only when prompts are not configured.
func (t *WorkflowVisualizationTool) Description() string {
	return `Generates an interactive HTML visualization of a Loom workflow YAML file.

This tool converts workflow definitions (pipeline stages, agents, connections) into
visual diagrams using ECharts. The output is an interactive HTML file that shows:
- All stages in the workflow with their titles and agents
- Sequential flow connections between stages
- Shared memory connections (dashed orange lines)
- Agent categories with color coding
- Critical markers (TOKEN_BUDGET, MERGED, FULL_HISTORY)
- Hover tooltips with stage details and instructions

Use this when you need:
- Visual documentation of workflow structure
- Analysis of workflow complexity and dependencies
- Presentation of workflow architecture to stakeholders
- Debugging workflow connections and data flow

The generated HTML file is standalone and can be opened in any browser.
It includes interactive features: zoom, pan, drag nodes, and click for details.

Example workflow paths:
- $LOOM_DATA_DIR/agents/workflow-npath-autonomous-v3.6-streamlined.yaml
- ./workflows/my-workflow.yaml
- /absolute/path/to/workflow.yaml`
}

func (t *WorkflowVisualizationTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for workflow visualization",
		map[string]*shuttle.JSONSchema{
			"workflow_path": shuttle.NewStringSchema("Path to workflow YAML file (required, supports ~/ expansion)"),
			"output_path":   shuttle.NewStringSchema("Path for output HTML file (required, supports ~/ expansion)"),
		},
		[]string{"workflow_path", "output_path"},
	)
}

func (t *WorkflowVisualizationTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters
	workflowPath, ok := params["workflow_path"].(string)
	if !ok || workflowPath == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "workflow_path is required",
				Suggestion: "Provide the path to a workflow YAML file (e.g., '$LOOM_DATA_DIR/agents/workflow.yaml')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	outputPath, ok := params["output_path"].(string)
	if !ok || outputPath == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "output_path is required",
				Suggestion: "Provide the output path for HTML file (e.g., '/tmp/workflow-diagram.html')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Expand paths (handle ~/ for home directory)
	workflowPath = expandPath(workflowPath)
	outputPath = expandPath(outputPath)

	// Validate workflow file exists
	if _, err := os.Stat(workflowPath); err != nil {
		if os.IsNotExist(err) {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:       "FILE_NOT_FOUND",
					Message:    fmt.Sprintf("Workflow file not found: %s", workflowPath),
					Suggestion: "Check if the workflow path is correct and the file exists",
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_ACCESS_ERROR",
				Message:    fmt.Sprintf("Cannot access workflow file: %v", err),
				Suggestion: "Check file permissions",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Parse workflow YAML
	workflow, err := workflowviz.ParseWorkflow(workflowPath)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "PARSE_ERROR",
				Message:    fmt.Sprintf("Failed to parse workflow YAML: %v", err),
				Suggestion: "Ensure the workflow file is valid YAML and follows Loom workflow schema",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Generate visualization data
	data, err := workflowviz.GenerateVisualization(workflow)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "VISUALIZATION_ERROR",
				Message:    fmt.Sprintf("Failed to generate visualization: %v", err),
				Retryable:  true,
				Suggestion: "Check workflow structure and stage definitions",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Create output directory if it doesn't exist
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "OUTPUT_DIR_ERROR",
				Message:    fmt.Sprintf("Failed to create output directory: %v", err),
				Suggestion: "Check write permissions for the output directory",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Generate HTML file
	if err := workflowviz.GenerateHTML(data, outputPath); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_WRITE_ERROR",
				Message:    fmt.Sprintf("Failed to write HTML file: %v", err),
				Retryable:  true,
				Suggestion: "Check write permissions for the output path",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Get absolute path for result
	absOutputPath, _ := filepath.Abs(outputPath)

	// Build success result
	result := map[string]interface{}{
		"output_path":         absOutputPath,
		"workflow_name":       workflow.Metadata.Name,
		"workflow_version":    workflow.Metadata.Version,
		"stages_count":        len(workflow.Spec.Pipeline.Stages),
		"workflow_type":       workflow.Spec.Type,
		"visualization_ready": true,
	}

	if workflow.Spec.MaxIterations > 0 {
		result["max_iterations"] = workflow.Spec.MaxIterations
	}

	return &shuttle.Result{
		Success: true,
		Data:    result,
		Metadata: map[string]interface{}{
			"tool":          "workflow_visualization",
			"workflow_path": workflowPath,
			"output_path":   absOutputPath,
			"stages_count":  len(workflow.Spec.Pipeline.Stages),
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *WorkflowVisualizationTool) Backend() string {
	return "" // Backend-agnostic
}

// expandPath expands ~/ to the user's home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
