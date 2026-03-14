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
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/validation"
	"gopkg.in/yaml.v3"
)

// executeCreateSkill creates a new skill using structured JSON config.
func (t *AgentManagementTool) executeCreateSkill(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	// Extract config object
	configObj, ok := params["config"]
	if !ok {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "config parameter is required for create_skill action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert to YAML using the structured config
	yamlContent, skillName, err := t.convertStructuredSkillToYAML(configObj)
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
	return t.writeSkillFile(skillName, yamlContent, false, start)
}

// executeUpdateSkill updates an existing skill using structured JSON config.
func (t *AgentManagementTool) executeUpdateSkill(ctx context.Context, params map[string]interface{}, start time.Time) (*shuttle.Result, error) {
	// Extract name
	name, ok := params["name"].(string)
	if !ok || name == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "INVALID_PARAMS",
				Message: "name parameter is required for update_skill action",
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
				Message: "config parameter is required for update_skill action",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Convert to YAML
	yamlContent, skillName, err := t.convertStructuredSkillToYAML(configObj)
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

	// Ensure name matches
	if skillName != name {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "NAME_MISMATCH",
				Message: fmt.Sprintf("config name (%s) does not match specified name (%s)", skillName, name),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write to file (update = true)
	return t.writeSkillFile(name, yamlContent, true, start)
}

// convertStructuredSkillToYAML converts a structured skill config to YAML.
func (t *AgentManagementTool) convertStructuredSkillToYAML(configObj interface{}) (string, string, error) {
	configMap, ok := configObj.(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("config must be an object")
	}

	// Extract skill name from metadata
	metadata, ok := configMap["metadata"].(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("metadata field is required")
	}

	name, ok := metadata["name"].(string)
	if !ok || name == "" {
		return "", "", fmt.Errorf("metadata.name is required")
	}

	// Set default apiVersion and kind if not provided
	if _, hasAPI := configMap["apiVersion"]; !hasAPI {
		configMap["apiVersion"] = "loom/v1"
	}
	if _, hasKind := configMap["kind"]; !hasKind {
		configMap["kind"] = "Skill"
	}

	// Convert to YAML
	yamlBytes, err := yaml.Marshal(configMap)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal YAML: %w", err)
	}

	return string(yamlBytes), name, nil
}

// writeSkillFile writes a skill YAML file to $LOOM_DATA_DIR/skills/.
func (t *AgentManagementTool) writeSkillFile(name, yamlContent string, isUpdate bool, start time.Time) (*shuttle.Result, error) {
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
	dir := config.GetLoomSubDir("skills")

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
				Message:    fmt.Sprintf("skill already exists: %s", filePath),
				Suggestion: "Use 'update_skill' action to modify existing files, or choose a different name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if isUpdate && !fileExists {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "FILE_NOT_FOUND",
				Message:    fmt.Sprintf("skill not found: %s", filePath),
				Suggestion: "Use 'create_skill' action to create new files, or check the skill name",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Write file (0644 = owner: rw, group: r, other: r)
	// nosec G306 - File permissions 0644 are intentional for config files
	if err := os.WriteFile(filePath, []byte(yamlContent), 0644); err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:    "WRITE_ERROR",
				Message: fmt.Sprintf("failed to write skill file: %v", err),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Success
	action := "created"
	if isUpdate {
		action = "updated"
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"file_path": filePath,
			"skill":     name,
			"message":   fmt.Sprintf("Skill %s successfully: %s", action, filePath),
			"directory": dir,
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}
