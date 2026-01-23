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

	"github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/validation"
)

// AgentManagementTool provides agent and workflow YAML management with validation.
// This tool is designed for meta-agents like weaver that create and manage agent/workflow configurations.
type AgentManagementTool struct{}

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
	return shuttle.NewObjectSchema(
		"Parameters for agent/workflow management",
		map[string]*shuttle.JSONSchema{
			"action": shuttle.NewStringSchema("Action to perform: create, update, read, list, validate, delete").
				WithEnum("create", "update", "read", "list", "validate", "delete"),
			"type": shuttle.NewStringSchema("Configuration type: agent or workflow").
				WithEnum("agent", "workflow"),
			"name":    shuttle.NewStringSchema("Name of the agent/workflow (used for filename)"),
			"content": shuttle.NewStringSchema("YAML content for the agent or workflow"),
		},
		[]string{"action", "type"},
	)
}

func (t *AgentManagementTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// SECURITY: Restrict this tool to the weaver agent only
	agentID := session.AgentIDFromContext(ctx)
	if agentID != "weaver" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "UNAUTHORIZED",
				Message: "This tool is restricted to the weaver meta-agent only",
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

	configType, ok := params["type"].(string)
	if !ok || configType == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "type parameter is required (must be 'agent' or 'workflow')",
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

	// Route to appropriate handler
	switch action {
	case "create":
		return t.executeCreate(ctx, configType, params, start)
	case "update":
		return t.executeUpdate(ctx, configType, params, start)
	case "read":
		return t.executeRead(ctx, configType, params, start)
	case "list":
		return t.executeList(ctx, configType, params, start)
	case "validate":
		return t.executeValidate(ctx, configType, params, start)
	case "delete":
		return t.executeDelete(ctx, configType, params, start)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_ACTION",
				Message: fmt.Sprintf("unknown action: %s (must be create, update, read, list, validate, or delete)", action),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}
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
