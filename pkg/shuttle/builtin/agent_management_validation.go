// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// validateAgentReferences validates that all agent references in a workflow exist as .yaml files.
// This prevents errors where workflows reference non-existent agents.
func validateAgentReferences(workflowSpec map[string]interface{}) *shuttle.Error {
	agentsDir := config.GetLoomSubDir("agents")

	// Extract agent_ids field (used by debate, fork-join, parallel patterns)
	if agentIDsRaw, ok := workflowSpec["agent_ids"]; ok {
		if agentIDs, ok := agentIDsRaw.([]interface{}); ok {
			for _, idRaw := range agentIDs {
				if agentID, ok := idRaw.(string); ok {
					if err := checkAgentExists(agentsDir, agentID); err != nil {
						return err
					}
				}
			}
		}
	}

	// Extract stages field (used by pipeline, iterative patterns)
	if stagesRaw, ok := workflowSpec["stages"]; ok {
		if stages, ok := stagesRaw.([]interface{}); ok {
			for i, stageRaw := range stages {
				if stage, ok := stageRaw.(map[string]interface{}); ok {
					if agentID, ok := stage["agent_id"].(string); ok {
						if err := checkAgentExists(agentsDir, agentID); err != nil {
							return &shuttle.Error{
								Code: "INVALID_AGENT_REFERENCE",
								Message: fmt.Sprintf("stage %d references non-existent agent '%s': %s",
									i, agentID, err.Message),
								Suggestion: fmt.Sprintf("Create agent '%s' first using create_agent action, or check the agent name", agentID),
							}
						}
					}
				}
			}
		}
	}

	// Extract tasks field (used by parallel pattern)
	if tasksRaw, ok := workflowSpec["tasks"]; ok {
		if tasks, ok := tasksRaw.([]interface{}); ok {
			for i, taskRaw := range tasks {
				if task, ok := taskRaw.(map[string]interface{}); ok {
					if agentID, ok := task["agent_id"].(string); ok {
						if err := checkAgentExists(agentsDir, agentID); err != nil {
							return &shuttle.Error{
								Code: "INVALID_AGENT_REFERENCE",
								Message: fmt.Sprintf("task %d references non-existent agent '%s': %s",
									i, agentID, err.Message),
								Suggestion: fmt.Sprintf("Create agent '%s' first using create_agent action, or check the agent name", agentID),
							}
						}
					}
				}
			}
		}
	}

	// Extract moderator_agent_id field (used by debate pattern)
	if moderatorID, ok := workflowSpec["moderator_agent_id"].(string); ok && moderatorID != "" {
		if err := checkAgentExists(agentsDir, moderatorID); err != nil {
			return &shuttle.Error{
				Code:       "INVALID_AGENT_REFERENCE",
				Message:    fmt.Sprintf("moderator references non-existent agent '%s': %s", moderatorID, err.Message),
				Suggestion: fmt.Sprintf("Create agent '%s' first using create_agent action, or check the agent name", moderatorID),
			}
		}
	}

	// Extract condition_agent_id field (used by conditional pattern)
	if conditionAgentID, ok := workflowSpec["condition_agent_id"].(string); ok && conditionAgentID != "" {
		if err := checkAgentExists(agentsDir, conditionAgentID); err != nil {
			return &shuttle.Error{
				Code:       "INVALID_AGENT_REFERENCE",
				Message:    fmt.Sprintf("condition agent references non-existent agent '%s': %s", conditionAgentID, err.Message),
				Suggestion: fmt.Sprintf("Create agent '%s' first using create_agent action, or check the agent name", conditionAgentID),
			}
		}
	}

	return nil
}

// checkAgentExists verifies that an agent configuration file exists.
func checkAgentExists(agentsDir, agentID string) *shuttle.Error {
	// Agent ID should match the filename (without .yaml extension)
	filename := agentID
	if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
		filename += ".yaml"
	}

	filePath := filepath.Join(agentsDir, filename)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return &shuttle.Error{
			Code:    "AGENT_NOT_FOUND",
			Message: fmt.Sprintf("agent file not found: %s", filePath),
		}
	}

	return nil
}

// validateToolNames validates that all tool names in an agent config are valid.
// This queries the tool registry to ensure tools exist.
func validateToolNames(toolsSpec map[string]interface{}, registry *shuttle.Registry) *shuttle.Error {
	// Check builtin tools
	if builtinRaw, ok := toolsSpec["builtin"]; ok {
		if builtin, ok := builtinRaw.([]interface{}); ok {
			for _, toolRaw := range builtin {
				if toolName, ok := toolRaw.(string); ok {
					if !isValidBuiltinTool(toolName, registry) {
						return &shuttle.Error{
							Code:       "INVALID_TOOL_NAME",
							Message:    fmt.Sprintf("builtin tool '%s' not found in registry", toolName),
							Suggestion: fmt.Sprintf("Check tool name spelling or available tools using: loom tools list"),
						}
					}
				}
			}
		}
	}

	// MCP tools don't need validation here - they're validated at runtime
	// when the MCP server connects

	// Custom tools validation would require checking if the implementation file exists
	// but that's better done at agent load time rather than config creation time

	return nil
}

// isValidBuiltinTool checks if a builtin tool exists in the registry.
func isValidBuiltinTool(toolName string, registry *shuttle.Registry) bool {
	if registry == nil {
		// No registry available, skip validation
		return true
	}

	// Check if tool exists in registry
	tool, _ := registry.Get(toolName)
	return tool != nil
}

// mapProtoErrorToFieldPath converts proto validation errors to actionable error messages.
// This helps LLMs understand which field caused the error and how to fix it.
func mapProtoErrorToFieldPath(err error, context string) *shuttle.Error {
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	// Map common proto errors to field paths
	mappings := map[string]struct {
		field      string
		suggestion string
	}{
		"temperature": {
			field:      "spec.llm.temperature",
			suggestion: "Temperature must be between 0.0 and 1.0",
		},
		"max_tokens": {
			field:      "spec.llm.max_tokens",
			suggestion: "max_tokens must be a positive integer",
		},
		"top_p": {
			field:      "spec.llm.top_p",
			suggestion: "top_p must be between 0.0 and 1.0",
		},
		"top_k": {
			field:      "spec.llm.top_k",
			suggestion: "top_k must be a positive integer",
		},
		"max_iterations": {
			field:      "spec.behavior.max_iterations",
			suggestion: "max_iterations must be a positive integer",
		},
		"timeout_seconds": {
			field:      "spec.behavior.timeout_seconds",
			suggestion: "timeout_seconds must be a positive integer",
		},
		"max_turns": {
			field:      "spec.behavior.max_turns",
			suggestion: "max_turns must be a positive integer",
		},
		"max_tool_executions": {
			field:      "spec.behavior.max_tool_executions",
			suggestion: "max_tool_executions must be a positive integer",
		},
		"min_confidence": {
			field:      "spec.behavior.patterns.min_confidence",
			suggestion: "min_confidence must be between 0.0 and 1.0",
		},
	}

	// Check if error message contains any of our mapped fields
	for keyword, mapping := range mappings {
		if strings.Contains(strings.ToLower(errMsg), keyword) {
			return &shuttle.Error{
				Code:       "PROTO_VALIDATION_ERROR",
				Message:    fmt.Sprintf("%s: %s", mapping.field, errMsg),
				Suggestion: mapping.suggestion,
			}
		}
	}

	// Default error if no specific mapping found
	return &shuttle.Error{
		Code:       "PROTO_VALIDATION_ERROR",
		Message:    fmt.Sprintf("%s: %s", context, errMsg),
		Suggestion: "Check the field value and ensure it meets the proto specification requirements",
	}
}

// validateWorkflowAgentField checks for field validation errors - using "role" instead of "agent_id" field.
// This is the main issue we're trying to prevent with structured validation.
func validateWorkflowAgentField(workflowSpec map[string]interface{}) *shuttle.Error {
	// Check stages for invalid "role" field
	if stagesRaw, ok := workflowSpec["stages"]; ok {
		if stages, ok := stagesRaw.([]interface{}); ok {
			for i, stageRaw := range stages {
				if stage, ok := stageRaw.(map[string]interface{}); ok {
					// Check if "role" field exists (this is wrong!)
					if _, hasRole := stage["role"]; hasRole {
						return &shuttle.Error{
							Code: "INVALID_FIELD",
							Message: fmt.Sprintf("stage %d has invalid field 'role' - workflows use 'agent_id' to reference agent configs",
								i),
							Suggestion: "Replace 'role' with 'agent_id' and use the agent config filename (without .yaml)",
						}
					}

					// Also check if agent_id is missing
					if _, hasAgentID := stage["agent_id"]; !hasAgentID {
						return &shuttle.Error{
							Code: "MISSING_FIELD",
							Message: fmt.Sprintf("stage %d is missing required field 'agent_id'",
								i),
							Suggestion: "Add 'agent_id' field with the agent config filename (without .yaml)",
						}
					}
				}
			}
		}
	}

	// Check tasks for invalid "role" field
	if tasksRaw, ok := workflowSpec["tasks"]; ok {
		if tasks, ok := tasksRaw.([]interface{}); ok {
			for i, taskRaw := range tasks {
				if task, ok := taskRaw.(map[string]interface{}); ok {
					if _, hasRole := task["role"]; hasRole {
						return &shuttle.Error{
							Code: "INVALID_FIELD",
							Message: fmt.Sprintf("task %d has invalid field 'role' - workflows use 'agent_id' to reference agent configs",
								i),
							Suggestion: "Replace 'role' with 'agent_id' and use the agent config filename (without .yaml)",
						}
					}

					if _, hasAgentID := task["agent_id"]; !hasAgentID {
						return &shuttle.Error{
							Code: "MISSING_FIELD",
							Message: fmt.Sprintf("task %d is missing required field 'agent_id'",
								i),
							Suggestion: "Add 'agent_id' field with the agent config filename (without .yaml)",
						}
					}
				}
			}
		}
	}

	return nil
}
