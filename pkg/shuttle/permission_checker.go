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
	"strings"
)

// PermissionChecker checks if a tool can be executed based on configuration.
// Entries in the allow/deny lists match a tool name either exactly or, when the
// entry ends with "*", by prefix. This lets a whole MCP server's tools be
// trusted with a single pattern (e.g. "opendata:*" matches "opendata:query"),
// rather than enumerating every tool name. A bare "*" matches everything.
type PermissionChecker struct {
	requireApproval bool
	yolo            bool
	allowedExact    map[string]bool // tool names always allowed (exact)
	allowedPrefix   []string        // allowed prefixes from "<prefix>*" entries
	disabledExact   map[string]bool // tool names never allowed (exact)
	disabledPrefix  []string        // disabled prefixes from "<prefix>*" entries
	defaultAction   string          // "allow" or "deny" - default action on timeout/no response
	timeoutSeconds  int             // How long to wait for user response
}

// splitPatterns separates a tool-pattern list into exact-match names and
// prefix patterns (from trailing-"*" entries; a bare "*" yields an empty prefix
// that matches everything).
func splitPatterns(list []string) (exact map[string]bool, prefixes []string) {
	exact = make(map[string]bool)
	for _, t := range list {
		if pfx, ok := strings.CutSuffix(t, "*"); ok {
			prefixes = append(prefixes, pfx)
		} else {
			exact[t] = true
		}
	}
	return exact, prefixes
}

// matchPattern reports whether name matches any exact name or prefix pattern.
func matchPattern(name string, exact map[string]bool, prefixes []string) bool {
	if exact[name] {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
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
	// Split into exact-match sets and prefix patterns (for "<prefix>*" entries).
	allowedExact, allowedPrefix := splitPatterns(config.AllowedTools)
	disabledExact, disabledPrefix := splitPatterns(config.DisabledTools)

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
		allowedExact:    allowedExact,
		allowedPrefix:   allowedPrefix,
		disabledExact:   disabledExact,
		disabledPrefix:  disabledPrefix,
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
	if matchPattern(toolName, pc.disabledExact, pc.disabledPrefix) {
		return fmt.Errorf("tool '%s' is disabled by configuration (tools.permissions.disabled_tools)", toolName)
	}

	// Check if tool is in allowed list (whitelist)
	if matchPattern(toolName, pc.allowedExact, pc.allowedPrefix) {
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

// IsToolAllowed returns true if a tool is explicitly allowed (whitelist),
// matching exact names and "<prefix>*" patterns.
func (pc *PermissionChecker) IsToolAllowed(toolName string) bool {
	return matchPattern(toolName, pc.allowedExact, pc.allowedPrefix)
}

// IsToolDisabled returns true if a tool is explicitly disabled (blacklist),
// matching exact names and "<prefix>*" patterns.
func (pc *PermissionChecker) IsToolDisabled(toolName string) bool {
	return matchPattern(toolName, pc.disabledExact, pc.disabledPrefix)
}

// RequiresApproval returns true if user approval is required for tools.
func (pc *PermissionChecker) RequiresApproval() bool {
	return pc.requireApproval
}
