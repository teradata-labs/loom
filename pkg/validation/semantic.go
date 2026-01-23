// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/teradata-labs/loom/pkg/config"
	"gopkg.in/yaml.v3"
)

// Level 3: Semantic Validation (Logical Consistency)
func validateSemantics(content, kind, filePath string) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	if kind == "Agent" {
		agentErrors, agentWarnings := validateAgentSemantics(content)
		errors = append(errors, agentErrors...)
		warnings = append(warnings, agentWarnings...)
	} else if kind == "Workflow" {
		workflowErrors, workflowWarnings := validateWorkflowSemantics(content, filePath)
		errors = append(errors, workflowErrors...)
		warnings = append(warnings, workflowWarnings...)
	}

	return errors, warnings
}

// validateAgentSemantics validates logical consistency for Agent configs.
func validateAgentSemantics(content string) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	var data map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &data); err != nil {
		return errors, warnings // Syntax error already caught
	}

	spec, hasSpec := data["spec"].(map[string]interface{})
	if !hasSpec {
		return errors, warnings // Structure error already caught
	}

	// Validate tools
	if tools, ok := spec["tools"].([]interface{}); ok {
		toolErrors, toolWarnings := validateTools(tools)
		errors = append(errors, toolErrors...)
		warnings = append(warnings, toolWarnings...)
	}

	// Validate memory configuration
	if memory, ok := spec["memory"].(map[string]interface{}); ok {
		memErrors, memWarnings := validateMemoryConfig(memory)
		errors = append(errors, memErrors...)
		warnings = append(warnings, memWarnings...)
	}

	// Validate config section
	if config, ok := spec["config"].(map[string]interface{}); ok {
		configErrors, configWarnings := validateAgentConfig(config)
		errors = append(errors, configErrors...)
		warnings = append(warnings, configWarnings...)
	}

	return errors, warnings
}

// validateWorkflowSemantics validates logical consistency for Workflow configs.
// Handles both orchestration and multi-agent workflows.
func validateWorkflowSemantics(content, filePath string) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	var data map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &data); err != nil {
		return errors, warnings // Syntax error already caught
	}

	spec, hasSpec := data["spec"].(map[string]interface{})
	if !hasSpec {
		return errors, warnings // Structure error already caught
	}

	// Detect workflow type
	workflowType := detectWorkflowType(spec)

	switch workflowType {
	case "orchestration":
		// Validate orchestration workflow semantics
		orchErrors, orchWarnings := validateOrchestrationSemantics(spec)
		errors = append(errors, orchErrors...)
		warnings = append(warnings, orchWarnings...)
	case "multi-agent":
		// Validate multi-agent workflow semantics
		maErrors, maWarnings := validateMultiAgentSemantics(spec, filePath)
		errors = append(errors, maErrors...)
		warnings = append(warnings, maWarnings...)
	}

	// Validate common workflow config
	if workflowConfig, ok := spec["config"].(map[string]interface{}); ok {
		configWarnings := validateWorkflowConfig(workflowConfig)
		warnings = append(warnings, configWarnings...)
	}

	return errors, warnings
}

// validateOrchestrationSemantics validates orchestration workflow semantics.
func validateOrchestrationSemantics(spec map[string]interface{}) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	// Get pattern/type
	pattern := ""
	if p, ok := spec["pattern"].(string); ok {
		pattern = p
	} else if t, ok := spec["type"].(string); ok {
		pattern = t
	}

	// Pattern-specific validation
	switch pattern {
	case "debate":
		debateErrors, debateWarnings := validateDebatePattern(spec)
		errors = append(errors, debateErrors...)
		warnings = append(warnings, debateWarnings...)
	case "fork-join":
		fjErrors, fjWarnings := validateForkJoinPattern(spec)
		errors = append(errors, fjErrors...)
		warnings = append(warnings, fjWarnings...)
	case "pipeline":
		pipeErrors, pipeWarnings := validatePipelinePattern(spec)
		errors = append(errors, pipeErrors...)
		warnings = append(warnings, pipeWarnings...)
		// Add other patterns as needed
	}

	return errors, warnings
}

// validateMultiAgentSemantics validates multi-agent workflow semantics.
func validateMultiAgentSemantics(spec map[string]interface{}, filePath string) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	entrypoint, _ := spec["entrypoint"].(string)
	agents, hasAgents := spec["agents"].([]interface{})

	if !hasAgents {
		return errors, warnings // Structure error already caught
	}

	// Parse agent list
	agentNames := make(map[string]string) // name -> agent config reference
	for _, agentItem := range agents {
		agentMap, ok := agentItem.(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := agentMap["name"].(string)
		agentRef, _ := agentMap["agent"].(string)

		if name != "" {
			agentNames[name] = agentRef
		}
	}

	// Check entrypoint exists in agents list
	if entrypoint != "" {
		if _, exists := agentNames[entrypoint]; !exists {
			errors = append(errors, ValidationError{
				Level:    LevelSemantic,
				Field:    "spec.entrypoint",
				Message:  fmt.Sprintf("Entrypoint '%s' not found in agents list", entrypoint),
				Expected: fmt.Sprintf("One of: %v", getMapKeys(agentNames)),
				Fix:      fmt.Sprintf("Change entrypoint to match an agent name, or add agent with name '%s'", entrypoint),
			})
		}
	}

	// Check agent file references
	loomDir := resolveLoomDir(filePath)
	for name, agentRef := range agentNames {
		if agentRef == "" {
			continue
		}

		// Check if agent file exists
		agentPath := filepath.Join(loomDir, "agents", agentRef+".yaml")
		if _, err := os.Stat(agentPath); os.IsNotExist(err) {
			errors = append(errors, ValidationError{
				Level:    LevelSemantic,
				Field:    fmt.Sprintf("spec.agents[%s].agent", name),
				Message:  fmt.Sprintf("Referenced agent file not found: %s", agentRef),
				Expected: fmt.Sprintf("File: %s", agentPath),
				Fix:      fmt.Sprintf("Create the agent file first: %s.yaml in %s/agents/", agentRef, config.GetLoomDataDir()),
			})
		}
	}

	// Validate communication section
	if comm, ok := spec["communication"].(map[string]interface{}); ok {
		commErrors, commWarnings := validateCommunication(comm, agentNames)
		errors = append(errors, commErrors...)
		warnings = append(warnings, commWarnings...)
	}

	return errors, warnings
}

// validateDebatePattern validates debate orchestration pattern.
func validateDebatePattern(spec map[string]interface{}) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	// Check for agents (either inline definitions or agent_ids references)
	// New format uses agent_ids (flat array of strings), old format uses agents (array of objects with roles)
	agentIds, hasAgentIds := spec["agent_ids"].([]interface{})
	agents, hasAgents := spec["agents"].([]interface{})

	if hasAgentIds {
		// New format: agent_ids should have at least 2 entries (already checked in structure validation)
		// Semantic validation: just ensure we have enough debaters (we can't validate roles without loading agent files)
		if len(agentIds) < 2 {
			errors = append(errors, ValidationError{
				Level:    LevelSemantic,
				Field:    "spec.agent_ids",
				Message:  "Debate pattern requires at least 2 agents",
				Expected: "Array with at least 2 agent IDs",
				Fix:      "Add at least 2 agent IDs to agent_ids array",
			})
		}
	} else if hasAgents {
		// Old format: inline agent definitions with roles
		if len(agents) < 2 {
			errors = append(errors, ValidationError{
				Level:    LevelSemantic,
				Field:    "spec.agents",
				Message:  "Debate pattern requires at least 2 agents with debater role",
				Expected: "Array with at least 2 agent definitions",
				Fix:      "Add at least 2 agents with role: debater",
			})
		}
	}
	// If neither format is present, it was already caught in structure validation

	// Check for rounds config
	if config, ok := spec["config"].(map[string]interface{}); ok {
		if rounds, ok := config["rounds"].(int); ok {
			if rounds < 1 {
				errors = append(errors, ValidationError{
					Level:   LevelSemantic,
					Field:   "spec.config.rounds",
					Message: "Debate rounds must be at least 1",
					Got:     fmt.Sprintf("%d", rounds),
				})
			}
		}
	}

	return errors, warnings
}

// validateForkJoinPattern validates fork-join orchestration pattern.
func validateForkJoinPattern(spec map[string]interface{}) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	// Check for prompt or agent_ids
	if prompt, ok := spec["prompt"].(string); !ok || prompt == "" {
		warnings = append(warnings, ValidationWarning{
			Field:   "spec.prompt",
			Message: "Fork-join pattern typically requires a prompt field",
			Fix:     "Add 'prompt' field with the query to distribute to agents",
		})
	}

	if agentIDs, ok := spec["agent_ids"].([]interface{}); !ok || len(agentIDs) == 0 {
		warnings = append(warnings, ValidationWarning{
			Field:   "spec.agent_ids",
			Message: "Fork-join pattern typically requires agent_ids",
			Fix:     "Add 'agent_ids' array with agent identifiers",
		})
	}

	return errors, warnings
}

// validatePipelinePattern validates pipeline orchestration pattern.
func validatePipelinePattern(spec map[string]interface{}) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	// Check for stages
	stages, hasStages := spec["stages"].([]interface{})
	if !hasStages || len(stages) == 0 {
		errors = append(errors, ValidationError{
			Level:    LevelSemantic,
			Field:    "spec.stages",
			Message:  "Pipeline pattern requires stages array",
			Expected: "Array of stage definitions",
			Fix:      "Add 'stages' array with at least one stage",
		})
	}

	return errors, warnings
}

// validateTools checks if tool names are valid.
func validateTools(tools []interface{}) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	// Known builtin tools
	builtinTools := map[string]bool{
		"shell_execute":          true,
		"tool_search":            true,
		"get_error_detail":       true,
		"get_tool_result":        true,
		"search_conversation":    true,
		"recall_conversation":    true,
		"clear_recalled_context": true,
		"send_message":           true,
		"receive_message":        true,
		"publish_message":        true,
		"subscribe_topic":        true,
		"shared_memory_read":     true,
		"shared_memory_write":    true,
		"contact_human":          true,
	}

	for _, tool := range tools {
		toolName, ok := tool.(string)
		if !ok {
			continue
		}

		// Check for common typos
		if !builtinTools[toolName] {
			// Check for close matches
			suggestion := findClosestTool(toolName, builtinTools)
			if suggestion != "" {
				errors = append(errors, ValidationError{
					Level:   LevelSemantic,
					Field:   "spec.tools",
					Message: fmt.Sprintf("Unknown tool: %s", toolName),
					Fix:     fmt.Sprintf("Did you mean '%s'?", suggestion),
					Got:     toolName,
				})
			} else {
				// Tool might be from MCP - just warn
				warnings = append(warnings, ValidationWarning{
					Field:   "spec.tools",
					Message: fmt.Sprintf("Tool '%s' not in builtin list (may be from MCP server)", toolName),
					Fix:     "Verify tool is available from configured MCP servers",
				})
			}
		}
	}

	return errors, warnings
}

// validateMemoryConfig validates memory configuration settings.
func validateMemoryConfig(memory map[string]interface{}) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	// Check memory type
	if memType, ok := memory["type"].(string); ok {
		validTypes := []string{"sqlite", "memory"}
		if !contains(validTypes, memType) {
			errors = append(errors, ValidationError{
				Level:    LevelSemantic,
				Field:    "spec.memory.type",
				Message:  fmt.Sprintf("Invalid memory type: %s", memType),
				Expected: "One of: sqlite, memory",
				Fix:      "Use 'sqlite' for persistent memory or 'memory' for in-memory only",
			})
		}
	}

	// Check memory compression
	if compression, ok := memory["memory_compression"].(map[string]interface{}); ok {
		if profile, ok := compression["workload_profile"].(string); ok {
			validProfiles := []string{"conversational", "data_intensive", "balanced"}
			if !contains(validProfiles, profile) {
				errors = append(errors, ValidationError{
					Level:    LevelSemantic,
					Field:    "spec.memory.memory_compression.workload_profile",
					Message:  fmt.Sprintf("Invalid workload profile: %s", profile),
					Expected: "One of: conversational, data_intensive, balanced",
					Fix:      suggestWorkloadProfile(profile),
				})
			}
		}

		// Validate threshold percentages
		if warning, ok := compression["warning_threshold_percent"].(int); ok {
			if warning < 0 || warning > 100 {
				errors = append(errors, ValidationError{
					Level:    LevelSemantic,
					Field:    "spec.memory.memory_compression.warning_threshold_percent",
					Message:  "Threshold must be between 0 and 100",
					Got:      fmt.Sprintf("%d", warning),
					Expected: "0-100",
				})
			}
		}

		if critical, ok := compression["critical_threshold_percent"].(int); ok {
			if critical < 0 || critical > 100 {
				errors = append(errors, ValidationError{
					Level:    LevelSemantic,
					Field:    "spec.memory.memory_compression.critical_threshold_percent",
					Message:  "Threshold must be between 0 and 100",
					Got:      fmt.Sprintf("%d", critical),
					Expected: "0-100",
				})
			}
		}
	}

	return errors, warnings
}

// validateAgentConfig validates agent configuration settings.
func validateAgentConfig(config map[string]interface{}) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	// Check for reasonable limits
	if maxTurns, ok := config["max_turns"].(int); ok {
		if maxTurns < 1 {
			errors = append(errors, ValidationError{
				Level:   LevelSemantic,
				Field:   "spec.config.max_turns",
				Message: "max_turns must be at least 1",
				Got:     fmt.Sprintf("%d", maxTurns),
			})
		}
		if maxTurns < 10 {
			warnings = append(warnings, ValidationWarning{
				Field:   "spec.config.max_turns",
				Message: fmt.Sprintf("max_turns is very low (%d) - agent may not have enough iterations", maxTurns),
				Fix:     "Consider increasing to at least 10-20 for typical workflows",
			})
		}
	}

	if maxToolExec, ok := config["max_tool_executions"].(int); ok {
		if maxToolExec < 1 {
			errors = append(errors, ValidationError{
				Level:   LevelSemantic,
				Field:   "spec.config.max_tool_executions",
				Message: "max_tool_executions must be at least 1",
				Got:     fmt.Sprintf("%d", maxToolExec),
			})
		}
	}

	return errors, warnings
}

// validateCommunication validates workflow communication configuration.
func validateCommunication(comm map[string]interface{}, agentNames map[string]string) ([]ValidationError, []ValidationWarning) {
	var errors []ValidationError
	var warnings []ValidationWarning

	pattern, _ := comm["pattern"].(string)
	hub, _ := comm["hub"].(string)

	// Valid patterns
	validPatterns := []string{"hub-and-spoke", "pipeline", "parallel", "debate", "fork-join", "peer-to-peer-pub-sub"}
	if pattern != "" && !contains(validPatterns, pattern) {
		errors = append(errors, ValidationError{
			Level:    LevelSemantic,
			Field:    "spec.communication.pattern",
			Message:  fmt.Sprintf("Invalid communication pattern: %s", pattern),
			Expected: fmt.Sprintf("One of: %v", validPatterns),
			Fix:      "Use a supported pattern or omit this field",
		})
	}

	// Hub-and-spoke requires hub to be specified
	if pattern == "hub-and-spoke" {
		if hub == "" {
			errors = append(errors, ValidationError{
				Level:    LevelSemantic,
				Field:    "spec.communication.hub",
				Message:  "Hub-and-spoke pattern requires 'hub' to be specified",
				Expected: "Name of the hub agent",
				Fix:      "Add 'hub: coordinator' (or your hub agent name) under communication",
			})
		} else if _, exists := agentNames[hub]; !exists {
			errors = append(errors, ValidationError{
				Level:    LevelSemantic,
				Field:    "spec.communication.hub",
				Message:  fmt.Sprintf("Hub agent '%s' not found in agents list", hub),
				Expected: fmt.Sprintf("One of: %v", getMapKeys(agentNames)),
				Fix:      fmt.Sprintf("Change hub to match an agent name, or add agent with name '%s'", hub),
			})
		}
	}

	return errors, warnings
}

// validateWorkflowConfig validates workflow configuration settings.
func validateWorkflowConfig(config map[string]interface{}) []ValidationWarning {
	var warnings []ValidationWarning

	if timeout, ok := config["timeout_seconds"].(int); ok {
		if timeout < 60 {
			warnings = append(warnings, ValidationWarning{
				Field:   "spec.config.timeout_seconds",
				Message: fmt.Sprintf("Workflow timeout is very short (%ds)", timeout),
				Fix:     "Consider at least 300s (5 minutes) for complex workflows",
			})
		}
	}

	return warnings
}

// Helper functions

func findClosestTool(tool string, validTools map[string]bool) string {
	tool = strings.ToLower(tool)

	// Check for exact match first
	if validTools[tool] {
		return ""
	}

	// Common typos
	typos := map[string]string{
		"shell_exec":    "shell_execute",
		"execute_shell": "shell_execute",
		"search_tools":  "tool_search",
		"find_tools":    "tool_search",
		"get_error":     "get_error_detail",
		"error_detail":  "get_error_detail",
		"search_conv":   "search_conversation",
		"recall_conv":   "recall_conversation",
		"clear_context": "clear_recalled_context",
		"send_msg":      "send_message",
		"receive_msg":   "receive_message",
		"publish":       "publish_message",
		"subscribe":     "subscribe_topic",
		"read_memory":   "shared_memory_read",
		"write_memory":  "shared_memory_write",
		"ask_human":     "contact_human",
		"human_contact": "contact_human",
	}

	if suggestion, ok := typos[tool]; ok {
		return suggestion
	}

	return ""
}

func suggestWorkloadProfile(profile string) string {
	profile = strings.ToLower(profile)

	if strings.Contains(profile, "conversation") {
		return "Did you mean 'conversational'?"
	}
	if strings.Contains(profile, "data") {
		return "Did you mean 'data_intensive'?"
	}
	if strings.Contains(profile, "balance") {
		return "Did you mean 'balanced'?"
	}

	return "Use 'conversational', 'data_intensive', or 'balanced'"
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func getMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func resolveLoomDir(filePath string) string {
	if filePath == "" {
		return config.GetLoomDataDir()
	}

	// Extract .loom directory from file path
	parts := strings.Split(filepath.Clean(filePath), string(filepath.Separator))
	for i, part := range parts {
		if part == ".loom" && i > 0 {
			return filepath.Join(parts[:i+1]...)
		}
	}

	// Default to configured loom data directory
	return config.GetLoomDataDir()
}
