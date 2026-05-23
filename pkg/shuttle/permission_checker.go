// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package shuttle

import (
	"context"
	"fmt"
)

// PermissionChecker checks if a tool can be executed based on configuration.
type PermissionChecker struct {
	requireApproval bool
	yolo            bool
	allowedTools    map[string]bool // Set of tool names that are always allowed
	disabledTools   map[string]bool // Set of tool names that are never allowed
	defaultAction   string          // "allow" or "deny" - default action on timeout/no response
	timeoutSeconds  int             // How long to wait for user response
}

// PermissionConfig holds permission configuration.
type PermissionConfig struct {
	RequireApproval bool
	YOLO            bool
	AllowedTools    []string
	DisabledTools   []string
	DefaultAction   string // "allow" or "deny"
	TimeoutSeconds  int
}

// NewPermissionChecker creates a new permission checker.
func NewPermissionChecker(config PermissionConfig) *PermissionChecker {
	// Convert slices to maps for O(1) lookups
	allowedMap := make(map[string]bool)
	for _, tool := range config.AllowedTools {
		allowedMap[tool] = true
	}

	disabledMap := make(map[string]bool)
	for _, tool := range config.DisabledTools {
		disabledMap[tool] = true
	}

	// Set defaults
	if config.DefaultAction == "" {
		config.DefaultAction = "deny"
	}
	if config.TimeoutSeconds == 0 {
		config.TimeoutSeconds = 300
	}

	return &PermissionChecker{
		requireApproval: config.RequireApproval,
		yolo:            config.YOLO,
		allowedTools:    allowedMap,
		disabledTools:   disabledMap,
		defaultAction:   config.DefaultAction,
		timeoutSeconds:  config.TimeoutSeconds,
	}
}

// CheckPermission checks if a tool can be executed.
// Returns nil if allowed, error if denied.
func (pc *PermissionChecker) CheckPermission(ctx context.Context, toolName string, params map[string]interface{}) error {
	// YOLO mode bypasses all checks
	if pc.yolo {
		return nil
	}

	// Check if tool is disabled (blacklist takes precedence)
	if pc.disabledTools[toolName] {
		return fmt.Errorf("tool '%s' is disabled by configuration (tools.permissions.disabled_tools)", toolName)
	}

	// Check if tool is in allowed list (whitelist)
	if pc.allowedTools[toolName] {
		return nil // Always allow whitelisted tools
	}

	// If require_approval is false, allow by default
	if !pc.requireApproval {
		return nil
	}

	// require_approval is true and tool is not in allowed list
	// TODO: Implement actual permission request mechanism (tracked as tech debt)
	// For now, use default action
	if pc.defaultAction == "allow" {
		return nil
	}

	// Default action is "deny" - tool requires approval but callback mechanism not implemented
	return fmt.Errorf("tool '%s' requires user approval (tools.permissions.require_approval=true) but permission request mechanism is not yet implemented. To bypass: set tools.permissions.yolo=true or add '%s' to tools.permissions.allowed_tools", toolName, toolName)
}

// IsYOLOMode returns true if YOLO mode is enabled.
func (pc *PermissionChecker) IsYOLOMode() bool {
	return pc.yolo
}

// IsToolAllowed returns true if a tool is explicitly allowed (whitelist).
func (pc *PermissionChecker) IsToolAllowed(toolName string) bool {
	return pc.allowedTools[toolName]
}

// IsToolDisabled returns true if a tool is explicitly disabled (blacklist).
func (pc *PermissionChecker) IsToolDisabled(toolName string) bool {
	return pc.disabledTools[toolName]
}

// RequiresApproval returns true if user approval is required for tools.
func (pc *PermissionChecker) RequiresApproval() bool {
	return pc.requireApproval
}
