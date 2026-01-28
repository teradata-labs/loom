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

	// CRITICAL: Check for deprecated fields first (common mistakes from old docs/LLM hallucinations)

	// 1. Deprecated workflow_type field (was never valid, likely LLM hallucination)
	if _, hasDeprecated := spec["workflow_type"]; hasDeprecated {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec.workflow_type",
			Message:  "DEPRECATED: 'workflow_type' field is not valid",
			Got:      "workflow_type",
			Expected: "type (or pattern for old format)",
			Fix:      "Replace 'spec.workflow_type: fork-join' with 'spec.type: fork-join'",
		})
	}

	// 2. Deprecated aggregation field (renamed to merge_strategy)
	if _, hasAggregation := spec["aggregation"]; hasAggregation {
		errors = append(errors, ValidationError{
			Level:    LevelStructure,
			Field:    "spec.aggregation",
			Message:  "DEPRECATED: 'aggregation' field renamed to 'merge_strategy'",
			Got:      "aggregation",
			Expected: "merge_strategy",
			Fix:      "Replace 'spec.aggregation: collect_all' with 'spec.merge_strategy: concatenate'",
		})
	}

	// 3. Incorrectly nested stages field (stages should be directly under spec for pipeline, not under a parent object)
	if stages, hasStages := spec["stages"]; hasStages {
		// Stages is only valid for pipeline pattern and should be a list directly under spec
		// NOT under spec.workflow_type.stages or similar nested structure
		if _, isMap := stages.(map[string]interface{}); isMap {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.stages",
				Message:  "DEPRECATED: 'stages' should not be a nested object",
				Got:      "map/object (nested structure)",
				Expected: "array of stage definitions (for pipeline pattern only)",
				Fix:      "For pipeline: use 'spec.stages: [{agent_id: ..., prompt_template: ...}]' directly under spec",
			})
		}
	}

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
			Fix:      "Add 'type: fork-join' under spec (preferred) or 'pattern: pipeline' (old format)",
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

	// Pattern-specific validation
	errors = append(errors, validatePatternSpecificFields(spec, patternValue)...)

	// Schema validation: check for invalid/unexpected fields
	errors = append(errors, validateOrchestrationSchema(spec, patternValue)...)

	return errors
}

// validateOrchestrationSchema validates that only valid fields are present for the pattern type.
// This catches typos, deprecated fields, and fields that don't belong to the pattern.
func validateOrchestrationSchema(spec map[string]interface{}, patternType string) []ValidationError {
	var errors []ValidationError

	// Define valid fields for each pattern type
	validFields := map[string][]string{
		"debate": {
			"pattern", "type", // Pattern identifier
			"topic", "agent_ids", "rounds", "merge_strategy", "moderator_agent_id", // Debate-specific
		},
		"fork-join": {
			"pattern", "type", // Pattern identifier
			"prompt", "agent_ids", "merge_strategy", "timeout_seconds", // Fork-join specific
		},
		"pipeline": {
			"pattern", "type", // Pattern identifier
			"initial_prompt", "stages", "pass_full_history", // Pipeline specific
		},
		"parallel": {
			"pattern", "type", // Pattern identifier
			"tasks", "merge_strategy", "timeout_seconds", // Parallel specific
		},
		"conditional": {
			"pattern", "type", // Pattern identifier
			"condition_agent_id", "condition_prompt", "branches", "default_branch", // Conditional specific
		},
		"iterative": {
			"pattern", "type", // Pattern identifier
			"max_iterations", "restart_topic", "restart_policy", "restart_triggers", "pipeline", // Iterative specific
		},
		"swarm": {
			"pattern", "type", // Pattern identifier
			"question", "agent_ids", "strategy", "confidence_threshold", "share_votes", "judge_agent_id", // Swarm specific
		},
	}

	allowedFields, ok := validFields[patternType]
	if !ok {
		// Unknown pattern type, skip schema validation
		return errors
	}

	// Create a set for faster lookup
	allowedSet := make(map[string]bool)
	for _, field := range allowedFields {
		allowedSet[field] = true
	}

	// Check each field in the spec
	for field := range spec {
		if !allowedSet[field] {
			// Check if it's a commonly confused field
			suggestion := ""
			switch field {
			case "agents":
				if patternType == "fork-join" || patternType == "debate" || patternType == "swarm" {
					suggestion = fmt.Sprintf("Use 'agent_ids' instead of 'agents' for %s pattern", patternType)
				}
			case "config":
				suggestion = "Configuration options (timeout, rounds, etc.) go directly under spec, not nested under 'config'"
			case "execution":
				suggestion = "Use 'config' section in top-level workflow, not 'execution' under spec"
			case "agent_id":
				if patternType == "parallel" {
					suggestion = "Use 'tasks' array with 'agent_id' field inside each task, not top-level 'agent_id'"
				}
			case "workflow_type":
				suggestion = "Use 'type' or 'pattern' instead of 'workflow_type'"
			case "aggregation":
				suggestion = "Use 'merge_strategy' instead of 'aggregation'"
			}

			if suggestion != "" {
				errors = append(errors, ValidationError{
					Level:    LevelStructure,
					Field:    fmt.Sprintf("spec.%s", field),
					Message:  fmt.Sprintf("Invalid field for %s pattern", patternType),
					Got:      field,
					Expected: fmt.Sprintf("Valid fields: %v", allowedFields),
					Fix:      suggestion,
				})
			} else {
				errors = append(errors, ValidationError{
					Level:    LevelStructure,
					Field:    fmt.Sprintf("spec.%s", field),
					Message:  fmt.Sprintf("Unexpected field for %s pattern", patternType),
					Got:      field,
					Expected: fmt.Sprintf("Valid fields: %v", allowedFields),
					Fix:      fmt.Sprintf("Remove this field or check the documentation for %s pattern", patternType),
				})
			}
		}
	}

	return errors
}

// validatePatternSpecificFields validates fields specific to each pattern type.
func validatePatternSpecificFields(spec map[string]interface{}, patternType string) []ValidationError {
	var errors []ValidationError

	switch patternType {
	case "fork-join":
		// fork-join requires: prompt, agent_ids, optional merge_strategy
		if _, hasPrompt := spec["prompt"]; !hasPrompt {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.prompt",
				Message:  "Missing required field for fork-join pattern",
				Expected: "prompt: string (same prompt sent to all agents)",
				Fix:      "Add 'prompt: \"Analyze this code\"' under spec",
			})
		}
		if agentIds, hasAgentIds := spec["agent_ids"]; !hasAgentIds {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.agent_ids",
				Message:  "Missing required field for fork-join pattern",
				Expected: "agent_ids: [agent1, agent2]",
				Fix:      "Add 'agent_ids: [bug-detector, perf-analyzer]' under spec",
			})
		} else if agentList, ok := agentIds.([]interface{}); ok && len(agentList) == 0 {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.agent_ids",
				Message:  "agent_ids cannot be empty",
				Expected: "At least one agent ID",
				Fix:      "Add agent IDs to the list",
			})
		}

	case "pipeline":
		// pipeline requires: initial_prompt, stages
		if _, hasInitialPrompt := spec["initial_prompt"]; !hasInitialPrompt {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.initial_prompt",
				Message:  "Missing required field for pipeline pattern",
				Expected: "initial_prompt: string",
				Fix:      "Add 'initial_prompt: \"Design auth system\"' under spec",
			})
		}
		if stages, hasStages := spec["stages"]; !hasStages {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.stages",
				Message:  "Missing required field for pipeline pattern",
				Expected: "stages: [{agent_id: ..., prompt_template: ...}]",
				Fix:      "Add 'stages:' array under spec with stage definitions",
			})
		} else if stagesList, ok := stages.([]interface{}); !ok {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.stages",
				Message:  "stages must be an array",
				Expected: "Array of stage definitions",
				Fix:      "Change stages to array format: [{agent_id: spec-writer, prompt_template: ...}]",
			})
		} else if len(stagesList) == 0 {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.stages",
				Message:  "stages cannot be empty",
				Expected: "At least one stage",
				Fix:      "Add at least one stage to the stages array",
			})
		}

	case "parallel":
		// parallel requires: tasks (array with agent_id and prompt)
		if tasks, hasTasks := spec["tasks"]; !hasTasks {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.tasks",
				Message:  "Missing required field for parallel pattern",
				Expected: "tasks: [{agent_id: ..., prompt: ...}]",
				Fix:      "Add 'tasks:' array under spec",
			})
		} else if taskList, ok := tasks.([]interface{}); ok && len(taskList) == 0 {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.tasks",
				Message:  "tasks cannot be empty",
				Expected: "At least one task",
				Fix:      "Add at least one task to the tasks array",
			})
		}

	case "debate":
		// debate requires: topic, agent_ids
		if _, hasTopic := spec["topic"]; !hasTopic {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.topic",
				Message:  "Missing required field for debate pattern",
				Expected: "topic: string",
				Fix:      "Add 'topic: \"Should we use microservices?\"' under spec",
			})
		}
		if agentIds, hasAgentIds := spec["agent_ids"]; !hasAgentIds {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.agent_ids",
				Message:  "Missing required field for debate pattern",
				Expected: "agent_ids: [agent1, agent2]",
				Fix:      "Add 'agent_ids: [architect, pragmatist]' under spec",
			})
		} else if agentList, ok := agentIds.([]interface{}); ok && len(agentList) < 2 {
			errors = append(errors, ValidationError{
				Level:    LevelStructure,
				Field:    "spec.agent_ids",
				Message:  "debate requires at least 2 agents",
				Expected: "At least 2 agent IDs",
				Fix:      "Add at least 2 agents for debate",
			})
		}
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
// Returns true for files in $LOOM_DATA_DIR/agents/ or $LOOM_DATA_DIR/workflows/
func ShouldValidate(filePath string) bool {
	cleanPath := filepath.Clean(filePath)
	return strings.Contains(cleanPath, "/.loom/agents/") ||
		strings.Contains(cleanPath, "/.loom/workflows/")
}
