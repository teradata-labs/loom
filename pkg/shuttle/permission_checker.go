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
	"errors"
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

var (
	// ErrToolExecutionDeferred is returned in PLAN mode to indicate tool should be added to plan
	ErrToolExecutionDeferred = errors.New("tool execution deferred to execution plan")
)

// PermissionChecker checks if a tool can be executed based on configuration.
type PermissionChecker struct {
	mode           loomv1.PermissionMode // Current permission mode (runtime configurable)
	allowedTools   map[string]bool       // Set of tool names that are always allowed
	disabledTools  map[string]bool       // Set of tool names that are never allowed
	defaultAction  string                // "allow" or "deny" - default action on timeout/no response
	timeoutSeconds int                   // How long to wait for user response
}

// PermissionConfig holds permission configuration.
type PermissionConfig struct {
	Mode            loomv1.PermissionMode // Permission mode (optional, UNSPECIFIED uses legacy flags for backward compat)
	RequireApproval bool                  // Deprecated: Mapped to ASK_BEFORE mode if Mode is UNSPECIFIED
	YOLO            bool                  // Deprecated: Mapped to AUTO_ACCEPT mode if Mode is UNSPECIFIED
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

	// Determine permission mode (prefer explicit mode over legacy flags)
	mode := config.Mode
	if mode == loomv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED {
		// Map legacy flags to modes for backward compatibility
		if config.YOLO {
			mode = loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT
		} else if config.RequireApproval {
			mode = loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE
		} else {
			// Default: auto-accept if neither flag set
			mode = loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT
		}
	}

	return &PermissionChecker{
		mode:           mode,
		allowedTools:   allowedMap,
		disabledTools:  disabledMap,
		defaultAction:  config.DefaultAction,
		timeoutSeconds: config.TimeoutSeconds,
	}
}

// CheckPermission checks if a tool can be executed based on the current permission mode.
// Returns nil if allowed, error if denied or deferred (plan mode).
func (pc *PermissionChecker) CheckPermission(ctx context.Context, toolName string, params map[string]interface{}) error {
	// Check disabled tools first (applies to all modes)
	if pc.disabledTools[toolName] {
		return fmt.Errorf("tool '%s' is disabled by configuration (tools.permissions.disabled_tools)", toolName)
	}

	// Check allowed tools (whitelist) - always allow regardless of mode
	if pc.allowedTools[toolName] {
		return nil
	}

	// Mode-specific permission logic
	switch pc.mode {
	case loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT:
		// Auto-approve all tools (YOLO mode)
		return nil

	case loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE:
		// Request user approval for each tool
		// TODO: Implement actual permission request callback mechanism
		// For now, use default action
		if pc.defaultAction == "allow" {
			return nil
		}
		return fmt.Errorf("tool '%s' requires user approval (permission_mode=ASK_BEFORE) but permission request mechanism is not yet implemented. To bypass: use permission_mode=AUTO_ACCEPT or add '%s' to tools.permissions.allowed_tools", toolName, toolName)

	case loomv1.PermissionMode_PERMISSION_MODE_PLAN:
		// In plan mode, tools are collected into a plan, not executed immediately
		// Return special error that agent will recognize to defer execution
		return ErrToolExecutionDeferred

	case loomv1.PermissionMode_PERMISSION_MODE_UNSPECIFIED:
		// Should not happen after NewPermissionChecker, but handle gracefully
		// Default to AUTO_ACCEPT for safety
		return nil

	default:
		return fmt.Errorf("unknown permission mode: %v", pc.mode)
	}
}

// IsYOLOMode returns true if in AUTO_ACCEPT mode.
// Deprecated: Use InAutoAcceptMode() or check GetMode() == PERMISSION_MODE_AUTO_ACCEPT instead.
func (pc *PermissionChecker) IsYOLOMode() bool {
	return pc.mode == loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT
}

// RequiresApproval returns true if in ASK_BEFORE mode.
// Deprecated: Use InAskBeforeMode() or check GetMode() == PERMISSION_MODE_ASK_BEFORE instead.
func (pc *PermissionChecker) RequiresApproval() bool {
	return pc.mode == loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE
}

// IsToolAllowed returns true if a tool is explicitly allowed (whitelist).
func (pc *PermissionChecker) IsToolAllowed(toolName string) bool {
	return pc.allowedTools[toolName]
}

// IsToolDisabled returns true if a tool is explicitly disabled (blacklist).
func (pc *PermissionChecker) IsToolDisabled(toolName string) bool {
	return pc.disabledTools[toolName]
}

// SetMode updates the permission mode at runtime.
// This allows Canvas AI and other clients to switch modes during a session.
func (pc *PermissionChecker) SetMode(mode loomv1.PermissionMode) {
	pc.mode = mode
}

// GetMode returns the current permission mode.
func (pc *PermissionChecker) GetMode() loomv1.PermissionMode {
	return pc.mode
}

// InPlanMode returns true if currently in PLAN mode.
// In plan mode, tool calls are collected into an execution plan rather than executed immediately.
func (pc *PermissionChecker) InPlanMode() bool {
	return pc.mode == loomv1.PermissionMode_PERMISSION_MODE_PLAN
}

// InAskBeforeMode returns true if currently in ASK_BEFORE mode.
func (pc *PermissionChecker) InAskBeforeMode() bool {
	return pc.mode == loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE
}

// InAutoAcceptMode returns true if currently in AUTO_ACCEPT mode (YOLO).
func (pc *PermissionChecker) InAutoAcceptMode() bool {
	return pc.mode == loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT
}
