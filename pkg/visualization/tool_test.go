// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package visualization

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestVisualizationTool_Execute tests the visualization tool execution
func TestVisualizationTool_Execute(t *testing.T) {
	tool := NewVisualizationTool()

	// Prepare test data
	topNResult := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"pattern": "A→B→C", "frequency": float64(100)},
			map[string]interface{}{"pattern": "A→C", "frequency": float64(80)},
		},
		"source_key": "test-data",
		"total":      1000,
	}

	topNJSON, _ := json.Marshal(topNResult)

	params := map[string]interface{}{
		"datasets": []interface{}{
			map[string]interface{}{
				"name": "top_patterns",
				"data": string(topNJSON),
			},
		},
		"title":       "Test Report",
		"summary":     "This is a test report",
		"output_path": "/tmp/loom-test-report.html",
		"theme":       "dark",
	}

	// Execute tool
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error.Message)
	}

	// Verify result data
	data := result.Data.(map[string]interface{})
	if data["output_path"] != "/tmp/loom-test-report.html" {
		t.Errorf("output_path = %v, want /tmp/loom-test-report.html", data["output_path"])
	}
	if data["visualizations"] != 1 {
		t.Errorf("visualizations = %v, want 1", data["visualizations"])
	}

	// Verify file was created
	if _, err := os.Stat("/tmp/loom-test-report.html"); os.IsNotExist(err) {
		t.Error("Output file was not created")
	}

	// Read and verify HTML content
	htmlBytes, err := os.ReadFile("/tmp/loom-test-report.html")
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	html := string(htmlBytes)
	requiredElements := []string{
		"<!DOCTYPE html>",
		"Test Report",
		"This is a test report",
		"echarts",
		"chart-0",
	}

	for _, elem := range requiredElements {
		if !strings.Contains(html, elem) {
			t.Errorf("HTML missing required element: %s", elem)
		}
	}

	// Cleanup
	os.Remove("/tmp/loom-test-report.html")
}

// TestVisualizationTool_InvalidParams tests parameter validation
func TestVisualizationTool_InvalidParams(t *testing.T) {
	tool := NewVisualizationTool()

	tests := []struct {
		name        string
		params      map[string]interface{}
		expectedErr string
	}{
		{
			name:        "missing datasets",
			params:      map[string]interface{}{},
			expectedErr: "datasets is required",
		},
		{
			name: "missing title",
			params: map[string]interface{}{
				"datasets": []interface{}{
					map[string]interface{}{
						"name": "test",
						"data": "{}",
					},
				},
			},
			expectedErr: "title is required",
		},
		{
			name: "missing summary",
			params: map[string]interface{}{
				"datasets": []interface{}{
					map[string]interface{}{
						"name": "test",
						"data": "{}",
					},
				},
				"title": "Test",
			},
			expectedErr: "summary is required",
		},
		{
			name: "missing output_path",
			params: map[string]interface{}{
				"datasets": []interface{}{
					map[string]interface{}{
						"name": "test",
						"data": "{}",
					},
				},
				"title":   "Test",
				"summary": "Test",
			},
			expectedErr: "output_path is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}

			if result.Success {
				t.Error("Expected tool to fail but it succeeded")
			}

			if !strings.Contains(result.Error.Message, tt.expectedErr) {
				t.Errorf("Error message = %s, want to contain %s", result.Error.Message, tt.expectedErr)
			}
		})
	}
}

// TestVisualizationTool_Schema tests tool schema
func TestVisualizationTool_Schema(t *testing.T) {
	tool := NewVisualizationTool()

	if tool.Name() != "generate_visualization" {
		t.Errorf("Name = %s, want generate_visualization", tool.Name())
	}

	desc := tool.Description()
	if !strings.Contains(desc, "visualization") {
		t.Error("Description should mention visualization")
	}

	schema := tool.InputSchema()
	if schema == nil {
		t.Fatal("InputSchema returned nil")
	}

	// Verify required fields
	requiredFields := []string{"datasets", "title", "summary", "output_path"}
	for _, field := range requiredFields {
		if !contains(schema.Required, field) {
			t.Errorf("Schema missing required field: %s", field)
		}
	}
}

// contains checks if a string slice contains a value
func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}
