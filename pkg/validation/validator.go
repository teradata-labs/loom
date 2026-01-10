// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ValidateYAMLFile validates a YAML file at the given path.
// Automatically detects if it's an Agent or Workflow based on content.
func ValidateYAMLFile(filePath string) ValidationResult {
	result := ValidationResult{
		Valid:    true,
		FilePath: filePath,
	}

	// Read file
	content, err := os.ReadFile(filePath)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Level:   LevelSyntax,
			Message: fmt.Sprintf("Failed to read file: %v", err),
		})
		return result
	}

	return ValidateYAMLContent(string(content), filePath)
}

// ValidateYAMLContent validates YAML content and returns detailed results.
// filePath is optional (for better error messages).
func ValidateYAMLContent(content, filePath string) ValidationResult {
	result := ValidationResult{
		Valid:    true,
		FilePath: filePath,
	}

	// Level 1: Syntax Validation
	syntaxErrors := validateSyntax(content)
	result.Errors = append(result.Errors, syntaxErrors...)

	// If syntax fails, can't continue to structure validation
	if len(syntaxErrors) > 0 {
		result.Valid = false
		return result
	}

	// Detect kind (Agent or Workflow)
	kind := detectKind(content)
	result.Kind = kind

	// Level 2: Structure Validation
	structErrors := validateStructure(content, kind)
	result.Errors = append(result.Errors, structErrors...)

	// Level 3: Semantic Validation
	semanticErrors, semanticWarnings := validateSemantics(content, kind, filePath)
	result.Errors = append(result.Errors, semanticErrors...)
	result.Warnings = append(result.Warnings, semanticWarnings...)

	// Mark as invalid if any errors found
	if len(result.Errors) > 0 {
		result.Valid = false
	}

	return result
}

// Level 1: Syntax Validation
func validateSyntax(content string) []ValidationError {
	var errors []ValidationError

	// Try to parse YAML
	var data map[string]interface{}
	err := yaml.Unmarshal([]byte(content), &data)
	if err != nil {
		// Parse error message to extract line number
		line := extractLineNumber(err.Error())
		errors = append(errors, ValidationError{
			Level:   LevelSyntax,
			Line:    line,
			Message: fmt.Sprintf("YAML syntax error: %v", err),
			Fix:     "Check for missing colons, incorrect indentation, or invalid characters",
		})
	}

	return errors
}

// Level 2: Structure Validation (Proto Schema Compliance)
func validateStructure(content, kind string) []ValidationError {
	var errors []ValidationError

	if kind == "Agent" {
		// Validate agent structure using YAML parsing
		errors = append(errors, validateAgentStructure(content)...)
	} else if kind == "Workflow" {
		// Validate workflow structure
		errors = append(errors, validateWorkflowStructure(content)...)
	} else if kind == "" {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Message:  "Unable to determine file kind (Agent or Workflow)",
			Expected: "File must have 'kind: Agent' or 'kind: Workflow'",
			Fix:      "Add 'kind: Agent' or 'kind: Workflow' under metadata",
		})
	}

	return errors
}

// validateAgentStructure validates agent YAML structure.
func validateAgentStructure(content string) []ValidationError {
	var errors []ValidationError
	var data map[string]interface{}

	if err := yaml.Unmarshal([]byte(content), &data); err != nil {
		return errors // Syntax error already caught
	}

	// Check required fields
	if apiVersion, ok := data["apiVersion"].(string); !ok || apiVersion == "" {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "apiVersion",
			Message:  "Missing required field",
			Expected: "apiVersion: loom/v1",
			Fix:      "Add 'apiVersion: loom/v1' at the top of the file",
		})
	} else if apiVersion != "loom/v1" {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "apiVersion",
			Message:  "Invalid apiVersion",
			Got:      apiVersion,
			Expected: "loom/v1",
			Fix:      "Change apiVersion to 'loom/v1' (not 'loom.dev/v1' or other variants)",
		})
	}

	if kind, ok := data["kind"].(string); !ok || kind == "" {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "kind",
			Message:  "Missing required field",
			Expected: "kind: Agent",
			Fix:      "Add 'kind: Agent' near the top of the file",
		})
	}

	// Check metadata
	metadata, hasMetadata := data["metadata"].(map[string]interface{})
	if !hasMetadata {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "metadata",
			Message:  "Missing required metadata section",
			Expected: "metadata with name, version, description",
			Fix:      "Add metadata section with at least 'name' field",
		})
	} else {
		if name, ok := metadata["name"].(string); !ok || name == "" {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "metadata.name",
				Message:  "Missing required field",
				Expected: "Non-empty string",
				Fix:      "Add 'name: your-agent-name' under metadata",
			})
		}
	}

	// Check spec
	spec, hasSpec := data["spec"].(map[string]interface{})
	if !hasSpec {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec",
			Message:  "Missing required spec section",
			Expected: "spec with system_prompt and tools",
			Fix:      "Add spec section with agent configuration",
		})
	} else {
		// Check tools format (common mistake: tools: {builtin: []})
		if tools, hasTools := spec["tools"]; hasTools {
			if _, isMap := tools.(map[string]interface{}); isMap {
				errors = append(errors, ValidationError{
					Level:    LevelStructure,
					Field:    "spec.tools",
					Message:  "Invalid tools format",
					Got:      "object/map (nested structure)",
					Expected: "flat array of strings",
					Fix:      "Change from 'tools: {builtin: [...]}' to flat array 'tools: [shell_execute, ...]'",
				})
			}
		}
	}

	return errors
}

// validateWorkflowStructure validates workflow YAML structure.
// Handles both orchestration workflows (pattern/type-based) and multi-agent workflows (entrypoint-based).
func validateWorkflowStructure(content string) []ValidationError {
	var errors []ValidationError
	var data map[string]interface{}

	if err := yaml.Unmarshal([]byte(content), &data); err != nil {
		return errors // Syntax error already caught
	}

	// Check required fields
	if apiVersion, ok := data["apiVersion"].(string); !ok || apiVersion == "" {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "apiVersion",
			Message:  "Missing required field",
			Expected: "apiVersion: loom/v1",
			Fix:      "Add 'apiVersion: loom/v1' at the top of the file",
		})
	} else if !strings.HasPrefix(apiVersion, "loom") {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "apiVersion",
			Message:  "Invalid apiVersion",
			Got:      apiVersion,
			Expected: "loom/v1",
			Fix:      "Change apiVersion to 'loom/v1'",
		})
	}

	if kind, ok := data["kind"].(string); !ok || kind == "" {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "kind",
			Message:  "Missing required field",
			Expected: "kind: Workflow",
			Fix:      "Add 'kind: Workflow' near the top of the file",
		})
	}

	// Check metadata
	metadata, hasMetadata := data["metadata"].(map[string]interface{})
	if !hasMetadata {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "metadata",
			Message:  "Missing required metadata section",
			Expected: "metadata with name, version, description",
			Fix:      "Add metadata section with at least 'name' field",
		})
	} else {
		if name, ok := metadata["name"].(string); !ok || name == "" {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "metadata.name",
				Message:  "Missing required field",
				Expected: "Non-empty string",
				Fix:      "Add 'name: your-workflow-name' under metadata",
			})
		}
	}

	// Check spec
	spec, hasSpec := data["spec"].(map[string]interface{})
	if !hasSpec {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec",
			Message:  "Missing required spec section",
			Expected: "spec with workflow configuration",
			Fix:      "Add spec section with workflow configuration",
		})
		return errors
	}

	// Detect workflow type: orchestration (pattern/type) vs multi-agent (entrypoint)
	workflowType := detectWorkflowType(spec)

	switch workflowType {
	case "orchestration":
		// Orchestration workflows use pattern or type field
		errors = append(errors, validateOrchestrationWorkflowStructure(spec)...)
	case "multi-agent":
		// Multi-agent workflows use entrypoint and agents
		errors = append(errors, validateMultiAgentWorkflowStructure(spec)...)
	default:
		// Ambiguous - missing both pattern/type AND entrypoint
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec",
			Message:  "Unable to determine workflow type",
			Expected: "Either 'pattern/type' (orchestration) or 'entrypoint' (multi-agent)",
			Fix:      "Add 'pattern: debate' for orchestration or 'entrypoint: coordinator' for multi-agent",
		})
	}

	return errors
}

// validateOrchestrationWorkflowStructure validates structure for orchestration workflows.
func validateOrchestrationWorkflowStructure(spec map[string]interface{}) []ValidationError {
	var errors []ValidationError

	// Check for pattern or type field
	hasPattern := false
	patternValue := ""

	if pattern, ok := spec["pattern"].(string); ok && pattern != "" {
		hasPattern = true
		patternValue = pattern
	}
	if workflowType, ok := spec["type"].(string); ok && workflowType != "" {
		hasPattern = true
		patternValue = workflowType
	}

	if !hasPattern {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec.pattern or spec.type",
			Message:  "Missing required field for orchestration workflow",
			Expected: "One of: pattern or type",
			Fix:      "Add 'pattern: debate' or 'type: fork-join' under spec",
		})
		return errors
	}

	// Validate pattern/type value
	validPatterns := []string{"debate", "fork-join", "pipeline", "parallel", "conditional", "iterative", "swarm"}
	isValidPattern := false
	for _, valid := range validPatterns {
		if patternValue == valid {
			isValidPattern = true
			break
		}
	}

	if !isValidPattern {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec.pattern or spec.type",
			Message:  fmt.Sprintf("Invalid workflow pattern: %s", patternValue),
			Expected: fmt.Sprintf("One of: %v", validPatterns),
			Fix:      "Use a supported orchestration pattern",
		})
	}

	return errors
}

// validateMultiAgentWorkflowStructure validates structure for multi-agent workflows.
func validateMultiAgentWorkflowStructure(spec map[string]interface{}) []ValidationError {
	var errors []ValidationError

	// Check for entrypoint
	if entrypoint, ok := spec["entrypoint"].(string); !ok || entrypoint == "" {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec.entrypoint",
			Message:  "Missing required field",
			Expected: "Name of the entrypoint agent",
			Fix:      "Add 'entrypoint: coordinator' (or your main agent name) under spec",
		})
	}

	// Check for agents list
	if agents, ok := spec["agents"].([]interface{}); !ok || len(agents) == 0 {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec.agents",
			Message:  "Missing or empty agents list",
			Expected: "Array of agent definitions",
			Fix:      "Add 'agents:' array with at least one agent under spec",
		})
	}

	return errors
}

// detectKind detects if the YAML is an Agent or Workflow.
func detectKind(content string) string {
	var data map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &data); err != nil {
		return ""
	}

	if kind, ok := data["kind"].(string); ok {
		return kind
	}

	return ""
}

// extractLineNumber extracts line number from YAML error messages.
func extractLineNumber(errMsg string) int {
	// YAML errors typically include "line X"
	re := regexp.MustCompile(`line (\d+)`)
	matches := re.FindStringSubmatch(errMsg)
	if len(matches) > 1 {
		var line int
		if _, err := fmt.Sscanf(matches[1], "%d", &line); err == nil {
			return line
		}
	}
	return 0
}

// ShouldValidate checks if a file path should be validated.
// Returns true for files in ~/.loom/agents/ or ~/.loom/workflows/
func ShouldValidate(filePath string) bool {
	cleanPath := filepath.Clean(filePath)
	return strings.Contains(cleanPath, "/.loom/agents/") ||
		strings.Contains(cleanPath, "/.loom/workflows/")
}
