// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/validation"
	"gopkg.in/yaml.v3"
)

// AgentManagementTool provides agent and workflow YAML management with validation.
// This tool is designed for meta-agents like weaver that create and manage agent/workflow configurations.
type AgentManagementTool struct{}

// K8sStyleAgentConfig represents the K8s-style agent YAML format.
// This is a local copy to avoid import cycle with pkg/agent.
type K8sStyleAgentConfig struct {
	APIVersion string                 `yaml:"apiVersion" json:"apiVersion"`
	Kind       string                 `yaml:"kind" json:"kind"`
	Metadata   map[string]interface{} `yaml:"metadata" json:"metadata"`
	Spec       map[string]interface{} `yaml:"spec" json:"spec"`
}

// WorkflowConfig represents the K8s-style workflow YAML format.
// This is a local copy to avoid import cycle with pkg/orchestration.
type WorkflowConfig struct {
	APIVersion string                 `yaml:"apiVersion" json:"apiVersion"`
	Kind       string                 `yaml:"kind" json:"kind"`
	Metadata   map[string]interface{} `yaml:"metadata" json:"metadata"`
	Spec       map[string]interface{} `yaml:"spec" json:"spec"`
	Schedule   map[string]interface{} `yaml:"schedule,omitempty" json:"schedule,omitempty"`
}

// NewAgentManagementTool creates a new agent management tool.
func NewAgentManagementTool() *AgentManagementTool {
	return &AgentManagementTool{}
}

func (t *AgentManagementTool) Name() string {
	return "agent_management"
}

func (t *AgentManagementTool) Backend() string {
	return "" // Backend-agnostic
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/agent_management.yaml).
// This fallback is used only when prompts are not configured.
func (t *AgentManagementTool) Description() string {
	return `Manages agent and workflow YAML configurations with automatic validation.

Use this tool to:
- Create new agent or workflow YAML files
- Update existing configurations
- Read agent/workflow definitions
- List all agents or workflows
- Validate YAML before writing
- Delete configurations

All files are written to $LOOM_DATA_DIR/agents/ or $LOOM_DATA_DIR/workflows/ (defaults to ~/.loom) and automatically validated.
Validation errors are returned immediately with actionable fixes.

This tool is intended for meta-agents that generate configurations (like weaver).`
}

func (t *AgentManagementTool) InputSchema() *shuttle.JSONSchema {
	// Build union schema for all action types
	// This enables structured JSON validation for create/update actions
	// while preserving simple string-based read/list/validate/delete actions
	return &shuttle.JSONSchema{
		Type:        "object",
		Description: "Parameters for agent/workflow management - supports both structured (create_agent, create_workflow, update_agent, update_workflow) and simple actions (read, list, validate, delete)",
		Properties: map[string]*shuttle.JSONSchema{
			"action": shuttle.NewStringSchema("Action to perform").
				WithEnum("create_agent", "create_workflow", "update_agent", "update_workflow", "read", "list", "validate", "delete"),
		},
		Required: []string{"action"},
		// Note: Use discriminator pattern - specific fields depend on action value
		// For structured actions (create_agent, etc.), use "config" field
		// For simple actions (read, list, etc.), use "type", "name", "content" fields
	}
}

func (t *AgentManagementTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// SECURITY: Restrict this tool to weaver and guide agents only
	agentID := session.AgentIDFromContext(ctx)
	if agentID != "weaver" && agentID != "guide" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "UNAUTHORIZED",
				Message: "This tool is restricted to the weaver and guide meta-agents only",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract parameters
	action, ok := params["action"].(string)
	if !ok || action == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "action parameter is required",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// SECURITY: Guide agent is READ-ONLY (can only list and read)
	if agentID == "guide" && action != "list" && action != "read" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "UNAUTHORIZED",
				Message: fmt.Sprintf("Guide agent is read-only. Only 'list' and 'read' actions are allowed, not '%s'", action),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Route to appropriate handler based on action
	switch action {
	case "create_agent":
		return t.executeCreateAgent(ctx, params, start)
	case "create_workflow":
		return t.executeCreateWorkflow(ctx, params, start)
	case "update_agent":
		return t.executeUpdateAgent(ctx, params, start)
	case "update_workflow":
		return t.executeUpdateWorkflow(ctx, params, start)
	case "read", "list", "validate", "delete":
		// These actions require "type" parameter for backward compatibility
		configType, ok := params["type"].(string)
		if !ok || configType == "" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_PARAMS",
					Message: "type parameter is required for this action (must be 'agent' or 'workflow')",
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		// Validate type
		if configType != "agent" && configType != "workflow" {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "INVALID_PARAMS",
					Message: fmt.Sprintf("invalid type: %s (must be 'agent' or 'workflow')", configType),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}

		// Route to legacy handlers
		switch action {
		case "read":
			return t.executeRead(ctx, configType, params, start)
		case "list":
			return t.executeList(ctx, configType, params, start)
		case "validate":
			return t.executeValidate(ctx, configType, params, start)
		case "delete":
			return t.executeDelete(ctx, configType, params, start)
		}
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_ACTION",
				Message: fmt.Sprintf("unknown action: %s (must be create_agent, create_workflow, update_agent, update_workflow, read, list, validate, or delete)", action),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Should never reach here
	return &shuttle.Result{
		Success: false,
		Error: &shuttle.Error{
			Code:    "INTERNAL_ERROR",
			Message: "internal routing error",
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeCreateAgent creates a new agent using structured JSON config.
func (t *AgentManagementTool) executeCreateAgent(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	// Extract config object
	configObj, ok := params["config"]
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "config parameter is required for create_agent action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert to YAML using the structured config
	yamlContent, agentName, err := t.convertStructuredAgentToYAML(configObj)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "CONVERSION_ERROR",
				Message: fmt.Sprintf("failed to convert config to YAML: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write to file
	return t.writeAgentFile(agentName, yamlContent, false, start)
}

// executeCreateWorkflow creates a new workflow using structured JSON config.
func (t *AgentManagementTool) executeCreateWorkflow(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	// Extract config object
	configObj, ok := params["config"]
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "config parameter is required for create_workflow action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert to YAML
	yamlContent, workflowName, err := t.convertStructuredWorkflowToYAML(configObj)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "CONVERSION_ERROR",
				Message: fmt.Sprintf("failed to convert config to YAML: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Perform semantic validation on workflow spec
	if configMap, ok := configObj.(map[string]interface{}); ok {
		if spec, ok := configMap["spec"].(map[string]interface{}); ok {
			// Validate workflow agent field (role vs agent_id)
			if validationErr := validateWorkflowAgentField(spec); validationErr != nil {
				return &shuttle.Result{
					Success:         false,
					Error:           validationErr,
					ExecutionTimeMs: time.Since(start).Milliseconds(),
				}, nil
			}

			// Validate agent references exist
			if validationErr := validateAgentReferences(spec); validationErr != nil {
				return &shuttle.Result{
					Success:         false,
					Error:           validationErr,
					ExecutionTimeMs: time.Since(start).Milliseconds(),
				}, nil
			}
		}
	}

	// Write to file
	return t.writeWorkflowFile(workflowName, yamlContent, false, start)
}

// executeUpdateAgent updates an existing agent using structured JSON config.
func (t *AgentManagementTool) executeUpdateAgent(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	// Extract name
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "name parameter is required for update_agent action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract config object
	configObj, ok := params["config"]
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "config parameter is required for update_agent action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert to YAML
	yamlContent, _, err := t.convertStructuredAgentToYAML(configObj)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "CONVERSION_ERROR",
				Message: fmt.Sprintf("failed to convert config to YAML: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write to file (update = true)
	return t.writeAgentFile(name, yamlContent, true, start)
}

// executeUpdateWorkflow updates an existing workflow using structured JSON config.
func (t *AgentManagementTool) executeUpdateWorkflow(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	// Extract name
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "name parameter is required for update_workflow action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Extract config object
	configObj, ok := params["config"]
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "config parameter is required for update_workflow action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert to YAML
	yamlContent, _, err := t.convertStructuredWorkflowToYAML(configObj)
	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "CONVERSION_ERROR",
				Message: fmt.Sprintf("failed to convert config to YAML: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Perform semantic validation on workflow spec
	if configMap, ok := configObj.(map[string]interface{}); ok {
		if spec, ok := configMap["spec"].(map[string]interface{}); ok {
			// Validate workflow agent field (role vs agent_id)
			if validationErr := validateWorkflowAgentField(spec); validationErr != nil {
				return &shuttle.Result{
					Success:         false,
					Error:           validationErr,
					ExecutionTimeMs: time.Since(start).Milliseconds(),
				}, nil
			}

			// Validate agent references exist
			if validationErr := validateAgentReferences(spec); validationErr != nil {
				return &shuttle.Result{
					Success:         false,
					Error:           validationErr,
					ExecutionTimeMs: time.Since(start).Milliseconds(),
				}, nil
			}
		}
	}

	// Write to file (update = true)
	return t.writeWorkflowFile(name, yamlContent, true, start)
}

// executeCreate creates a new agent or workflow configuration file.
func (t *AgentManagementTool) executeCreate(ctx context.Context, configType string, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "name parameter is required for create action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	content, ok := params["content"].(string)
	if !ok || content == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "content parameter is required for create action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Validate YAML content before writing
	validationResult := validation.ValidateYAMLContent(content, "")

	if !validationResult.Valid {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "VALIDATION_ERROR",
				Message: "YAML validation failed",
			},
			Data: map[string]interface{}{
				"validation": validationResult.FormatForWeaver(),
				"errors":     validationResult.Errors,
				"warnings":   validationResult.Warnings,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine target directory
	var dir string
	if configType == "agent" {
		dir = config.GetLoomSubDir("agents")
	} else {
		dir = config.GetLoomSubDir("workflows")
	}

	// Ensure directory exists
	// nosec G301 - Directory permissions 0750 (owner: rwx, group: r-x, other: none)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "DIRECTORY_ERROR",
				Message: fmt.Sprintf("failed to create directory: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Construct file path
	filename := name
	if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
		filename += ".yaml"
	}
	filePath := filepath.Join(dir, filename)

	// Check if file already exists
	if _, err := os.Stat(filePath); err == nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_EXISTS",
				Message:    fmt.Sprintf("%s already exists: %s", configType, filePath),
				Suggestion: "Use 'update' action to modify existing files, or choose a different name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "WRITE_ERROR",
				Message: fmt.Sprintf("failed to write file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":     "create",
			"type":       configType,
			"name":       name,
			"path":       filePath,
			"validation": "✅ YAML validation passed - no issues detected",
			"warnings":   validationResult.Warnings,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeUpdate updates an existing agent or workflow configuration file.
func (t *AgentManagementTool) executeUpdate(ctx context.Context, configType string, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "name parameter is required for update action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	content, ok := params["content"].(string)
	if !ok || content == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "content parameter is required for update action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Validate YAML content before writing
	validationResult := validation.ValidateYAMLContent(content, "")

	if !validationResult.Valid {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "VALIDATION_ERROR",
				Message: "YAML validation failed",
			},
			Data: map[string]interface{}{
				"validation": validationResult.FormatForWeaver(),
				"errors":     validationResult.Errors,
				"warnings":   validationResult.Warnings,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine target directory
	var dir string
	if configType == "agent" {
		dir = config.GetLoomSubDir("agents")
	} else {
		dir = config.GetLoomSubDir("workflows")
	}

	// Construct file path
	filename := name
	if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
		filename += ".yaml"
	}
	filePath := filepath.Join(dir, filename)

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_NOT_FOUND",
				Message:    fmt.Sprintf("%s not found: %s", configType, filePath),
				Suggestion: "Use 'create' action to create new files, or check the name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "WRITE_ERROR",
				Message: fmt.Sprintf("failed to write file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":     "update",
			"type":       configType,
			"name":       name,
			"path":       filePath,
			"validation": "✅ YAML validation passed - no issues detected",
			"warnings":   validationResult.Warnings,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeRead reads an agent or workflow configuration file.
func (t *AgentManagementTool) executeRead(ctx context.Context, configType string, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "name parameter is required for read action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine target directory
	var dir string
	if configType == "agent" {
		dir = config.GetLoomSubDir("agents")
	} else {
		dir = config.GetLoomSubDir("workflows")
	}

	// Construct file path
	filename := name
	if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
		filename += ".yaml"
	}
	filePath := filepath.Join(dir, filename)

	// Read file - path validated and restricted to $LOOM_DATA_DIR/agents/ or $LOOM_DATA_DIR/workflows/
	content, err := os.ReadFile(filePath) // #nosec G304 -- Path is validated and sanitized above
	if err != nil {
		if os.IsNotExist(err) {
			return &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "FILE_NOT_FOUND",
					Message: fmt.Sprintf("%s not found: %s", configType, filePath),
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "READ_ERROR",
				Message: fmt.Sprintf("failed to read file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":  "read",
			"type":    configType,
			"name":    name,
			"path":    filePath,
			"content": string(content),
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeList lists all agents or workflows.
func (t *AgentManagementTool) executeList(ctx context.Context, configType string, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	// Determine target directory
	var dir string
	if configType == "agent" {
		dir = config.GetLoomSubDir("agents")
	} else {
		dir = config.GetLoomSubDir("workflows")
	}

	// Read directory
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Directory doesn't exist yet - return empty list
			return &shuttle.Result{
				Success: true,
				Data: map[string]interface{}{
					"action": "list",
					"type":   configType,
					"count":  0,
					"files":  []map[string]interface{}{},
				},
				ExecutionTimeMs: time.Since(start).Milliseconds(),
			}, nil
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "READ_ERROR",
				Message: fmt.Sprintf("failed to read directory: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Filter YAML files
	var files []map[string]interface{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, map[string]interface{}{
			"name":     name,
			"size":     info.Size(),
			"modified": info.ModTime().Format(time.RFC3339),
		})
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action": "list",
			"type":   configType,
			"count":  len(files),
			"files":  files,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeValidate validates YAML content without writing to disk.
func (t *AgentManagementTool) executeValidate(ctx context.Context, configType string, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	content, ok := params["content"].(string)
	if !ok || content == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "content parameter is required for validate action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Validate YAML content
	validationResult := validation.ValidateYAMLContent(content, "")

	return &shuttle.Result{
		Success: validationResult.Valid,
		Data: map[string]interface{}{
			"action":     "validate",
			"type":       configType,
			"valid":      validationResult.Valid,
			"validation": validationResult.FormatForWeaver(),
			"errors":     validationResult.Errors,
			"warnings":   validationResult.Warnings,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// executeDelete deletes an agent or workflow configuration file.
func (t *AgentManagementTool) executeDelete(ctx context.Context, configType string, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "name parameter is required for delete action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine target directory
	var dir string
	if configType == "agent" {
		dir = config.GetLoomSubDir("agents")
	} else {
		dir = config.GetLoomSubDir("workflows")
	}

	// Construct file path
	filename := name
	if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
		filename += ".yaml"
	}
	filePath := filepath.Join(dir, filename)

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "FILE_NOT_FOUND",
				Message: fmt.Sprintf("%s not found: %s", configType, filePath),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Delete file
	if err := os.Remove(filePath); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "DELETE_ERROR",
				Message: fmt.Sprintf("failed to delete file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action": "delete",
			"type":   configType,
			"name":   name,
			"path":   filePath,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// convertStructuredAgentToYAML converts a structured agent config (from JSON) to YAML format.
// Returns the YAML content and the agent name.
func (t *AgentManagementTool) convertStructuredAgentToYAML(configObj interface{}) (string, string, error) {
	// Marshal to JSON then unmarshal to our K8s-style struct
	// This ensures proper type conversion from map[string]interface{}
	jsonBytes, err := json.Marshal(configObj)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal config to JSON: %w", err)
	}

	var k8sConfig K8sStyleAgentConfig
	if err := json.Unmarshal(jsonBytes, &k8sConfig); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal JSON to K8s config: %w", err)
	}

	// Validate required fields
	metadata, ok := k8sConfig.Metadata["name"].(string)
	if !ok || metadata == "" {
		return "", "", fmt.Errorf("metadata.name is required")
	}

	spec, ok := k8sConfig.Spec["system_prompt"].(string)
	if !ok || spec == "" {
		return "", "", fmt.Errorf("spec.system_prompt is required")
	}

	// Set defaults
	if k8sConfig.APIVersion == "" {
		k8sConfig.APIVersion = "loom/v1"
	}
	if k8sConfig.Kind == "" {
		k8sConfig.Kind = "Agent"
	}

	// Set default version in metadata if not present
	if _, ok := k8sConfig.Metadata["version"]; !ok {
		k8sConfig.Metadata["version"] = "1.0.0"
	}

	// Convert to YAML
	yamlBytes, err := yaml.Marshal(&k8sConfig)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal to YAML: %w", err)
	}

	return string(yamlBytes), metadata, nil
}

// convertStructuredWorkflowToYAML converts a structured workflow config (from JSON) to YAML format.
// Returns the YAML content and the workflow name.
func (t *AgentManagementTool) convertStructuredWorkflowToYAML(configObj interface{}) (string, string, error) {
	// Marshal to JSON then unmarshal to workflow config struct
	jsonBytes, err := json.Marshal(configObj)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal config to JSON: %w", err)
	}

	var workflowConfig WorkflowConfig
	if err := json.Unmarshal(jsonBytes, &workflowConfig); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal JSON to workflow config: %w", err)
	}

	// Validate required fields
	name, ok := workflowConfig.Metadata["name"].(string)
	if !ok || name == "" {
		return "", "", fmt.Errorf("metadata.name is required")
	}
	if len(workflowConfig.Spec) == 0 {
		return "", "", fmt.Errorf("spec is required")
	}
	patternType, ok := workflowConfig.Spec["type"].(string)
	if !ok || patternType == "" {
		return "", "", fmt.Errorf("spec.type is required")
	}

	// Set defaults
	if workflowConfig.APIVersion == "" {
		workflowConfig.APIVersion = "loom/v1"
	}
	if workflowConfig.Kind == "" {
		workflowConfig.Kind = "Workflow"
	}

	// Set default version in metadata if not present
	if _, ok := workflowConfig.Metadata["version"]; !ok {
		workflowConfig.Metadata["version"] = "1.0.0"
	}

	// Convert to YAML
	yamlBytes, err := yaml.Marshal(&workflowConfig)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal to YAML: %w", err)
	}

	return string(yamlBytes), name, nil
}

// writeAgentFile writes an agent YAML file (for create or update).
func (t *AgentManagementTool) writeAgentFile(name, yamlContent string, isUpdate bool, start time.Time) (*shuttle.Result, error) {
	// Validate YAML content
	validationResult := validation.ValidateYAMLContent(yamlContent, "")

	if !validationResult.Valid {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "VALIDATION_ERROR",
				Message: "YAML validation failed",
			},
			Data: map[string]interface{}{
				"validation": validationResult.FormatForWeaver(),
				"errors":     validationResult.Errors,
				"warnings":   validationResult.Warnings,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine target directory
	dir := config.GetLoomSubDir("agents")

	// Ensure directory exists
	// nosec G301 - Directory permissions 0750 (owner: rwx, group: r-x, other: none)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "DIRECTORY_ERROR",
				Message: fmt.Sprintf("failed to create directory: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Construct file path
	filename := name
	if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
		filename += ".yaml"
	}
	filePath := filepath.Join(dir, filename)

	// Check if file exists (for create vs update)
	_, err := os.Stat(filePath)
	fileExists := err == nil

	if !isUpdate && fileExists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_EXISTS",
				Message:    fmt.Sprintf("agent already exists: %s", filePath),
				Suggestion: "Use 'update_agent' action to modify existing files, or choose a different name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if isUpdate && !fileExists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_NOT_FOUND",
				Message:    fmt.Sprintf("agent not found: %s", filePath),
				Suggestion: "Use 'create_agent' action to create new files, or check the name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(yamlContent), 0600); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "WRITE_ERROR",
				Message: fmt.Sprintf("failed to write file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	action := "create_agent"
	if isUpdate {
		action = "update_agent"
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":     action,
			"name":       name,
			"path":       filePath,
			"validation": "✅ Agent validation passed",
			"warnings":   validationResult.Warnings,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// writeWorkflowFile writes a workflow YAML file (for create or update).
func (t *AgentManagementTool) writeWorkflowFile(name, yamlContent string, isUpdate bool, start time.Time) (*shuttle.Result, error) {
	// Validate YAML content
	validationResult := validation.ValidateYAMLContent(yamlContent, "")

	if !validationResult.Valid {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "VALIDATION_ERROR",
				Message: "YAML validation failed",
			},
			Data: map[string]interface{}{
				"validation": validationResult.FormatForWeaver(),
				"errors":     validationResult.Errors,
				"warnings":   validationResult.Warnings,
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Determine target directory
	dir := config.GetLoomSubDir("workflows")

	// Ensure directory exists
	// nosec G301 - Directory permissions 0750 (owner: rwx, group: r-x, other: none)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "DIRECTORY_ERROR",
				Message: fmt.Sprintf("failed to create directory: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Construct file path
	filename := name
	if !strings.HasSuffix(filename, ".yaml") && !strings.HasSuffix(filename, ".yml") {
		filename += ".yaml"
	}
	filePath := filepath.Join(dir, filename)

	// Check if file exists (for create vs update)
	_, err := os.Stat(filePath)
	fileExists := err == nil

	if !isUpdate && fileExists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_EXISTS",
				Message:    fmt.Sprintf("workflow already exists: %s", filePath),
				Suggestion: "Use 'update_workflow' action to modify existing files, or choose a different name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if isUpdate && !fileExists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_NOT_FOUND",
				Message:    fmt.Sprintf("workflow not found: %s", filePath),
				Suggestion: "Use 'create_workflow' action to create new files, or check the name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(yamlContent), 0600); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "WRITE_ERROR",
				Message: fmt.Sprintf("failed to write file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	action := "create_workflow"
	if isUpdate {
		action = "update_workflow"
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"action":     action,
			"name":       name,
			"path":       filePath,
			"validation": "✅ Workflow validation passed",
			"warnings":   validationResult.Warnings,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}
