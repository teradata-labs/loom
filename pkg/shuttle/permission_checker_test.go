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
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestPermissionChecker_ModeInitialization(t *testing.T) {
	tests := []struct {
		name           string
		config         PermissionConfig
		expectedMode   loomv1.PermissionMode
		testModeMethod func(*PermissionChecker) bool
	}{
		{
			name: "explicit AUTO_ACCEPT mode",
			config: PermissionConfig{
				Mode: loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
			},
			expectedMode:   loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
			testModeMethod: (*PermissionChecker).InAutoAcceptMode,
		},
		{
			name: "explicit ASK_BEFORE mode",
			config: PermissionConfig{
				Mode: loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
			},
			expectedMode:   loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
			testModeMethod: (*PermissionChecker).InAskBeforeMode,
		},
		{
			name: "explicit PLAN mode",
			config: PermissionConfig{
				Mode: loomv1.PermissionMode_PERMISSION_MODE_PLAN,
			},
			expectedMode:   loomv1.PermissionMode_PERMISSION_MODE_PLAN,
			testModeMethod: (*PermissionChecker).InPlanMode,
		},
		{
			name: "legacy YOLO flag maps to AUTO_ACCEPT",
			config: PermissionConfig{
				YOLO: true,
			},
			expectedMode:   loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
			testModeMethod: (*PermissionChecker).InAutoAcceptMode,
		},
		{
			name: "legacy RequireApproval flag maps to ASK_BEFORE",
			config: PermissionConfig{
				RequireApproval: true,
			},
			expectedMode:   loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
			testModeMethod: (*PermissionChecker).InAskBeforeMode,
		},
		{
			name:           "no flags defaults to AUTO_ACCEPT",
			config:         PermissionConfig{},
			expectedMode:   loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
			testModeMethod: (*PermissionChecker).InAutoAcceptMode,
		},
		{
			name: "explicit mode overrides legacy flags",
			config: PermissionConfig{
				Mode:            loomv1.PermissionMode_PERMISSION_MODE_PLAN,
				YOLO:            true, // Should be ignored
				RequireApproval: true, // Should be ignored
			},
			expectedMode:   loomv1.PermissionMode_PERMISSION_MODE_PLAN,
			testModeMethod: (*PermissionChecker).InPlanMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := NewPermissionChecker(tt.config)

			if pc.GetMode() != tt.expectedMode {
				t.Errorf("GetMode() = %v, want %v", pc.GetMode(), tt.expectedMode)
			}

			if tt.testModeMethod != nil && !tt.testModeMethod(pc) {
				t.Errorf("mode check method returned false, expected true for mode %v", tt.expectedMode)
			}
		})
	}
}

func TestPermissionChecker_SetMode(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		Mode: loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
	})

	// Verify initial mode
	if !pc.InAutoAcceptMode() {
		t.Fatal("expected initial mode to be AUTO_ACCEPT")
	}

	// Switch to PLAN mode
	pc.SetMode(loomv1.PermissionMode_PERMISSION_MODE_PLAN)
	if !pc.InPlanMode() {
		t.Error("expected mode to be PLAN after SetMode")
	}
	if pc.GetMode() != loomv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Errorf("GetMode() = %v, want PLAN", pc.GetMode())
	}

	// Switch to ASK_BEFORE mode
	pc.SetMode(loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE)
	if !pc.InAskBeforeMode() {
		t.Error("expected mode to be ASK_BEFORE after SetMode")
	}

	// Switch back to AUTO_ACCEPT
	pc.SetMode(loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT)
	if !pc.InAutoAcceptMode() {
		t.Error("expected mode to be AUTO_ACCEPT after SetMode")
	}
}

func TestPermissionChecker_CheckPermission_AutoAccept(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		Mode: loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
	})

	ctx := context.Background()

	// All tools should be allowed in AUTO_ACCEPT mode
	tools := []string{"execute_sql", "file_write", "shell_execute", "dangerous_tool"}
	for _, tool := range tools {
		err := pc.CheckPermission(ctx, tool, nil)
		if err != nil {
			t.Errorf("AUTO_ACCEPT mode should allow '%s', got error: %v", tool, err)
		}
	}
}

func TestPermissionChecker_CheckPermission_AskBefore(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		Mode:          loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
		DefaultAction: "deny", // Default deny for testing
	})

	ctx := context.Background()

	// Non-whitelisted tools should require approval (return error for now)
	err := pc.CheckPermission(ctx, "execute_sql", nil)
	if err == nil {
		t.Error("ASK_BEFORE mode with deny default should return error for non-whitelisted tools")
	}
	if err != nil && !contains(err.Error(), "requires user approval") {
		t.Errorf("expected approval error, got: %v", err)
	}
}

func TestPermissionChecker_CheckPermission_Plan(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		Mode: loomv1.PermissionMode_PERMISSION_MODE_PLAN,
	})

	ctx := context.Background()

	// All non-whitelisted tools should return ErrToolExecutionDeferred in PLAN mode
	err := pc.CheckPermission(ctx, "execute_sql", nil)
	if !errors.Is(err, ErrToolExecutionDeferred) {
		t.Errorf("PLAN mode should return ErrToolExecutionDeferred, got: %v", err)
	}

	// Test different tools
	tools := []string{"file_write", "shell_execute", "contact_human"}
	for _, tool := range tools {
		err := pc.CheckPermission(ctx, tool, nil)
		if !errors.Is(err, ErrToolExecutionDeferred) {
			t.Errorf("PLAN mode should return ErrToolExecutionDeferred for '%s', got: %v", tool, err)
		}
	}
}

func TestPermissionChecker_CheckPermission_DisabledTools(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		Mode:          loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
		DisabledTools: []string{"shell_execute", "file_delete"},
	})

	ctx := context.Background()

	// Disabled tools should be blocked in ALL modes
	disabledTools := []string{"shell_execute", "file_delete"}
	for _, tool := range disabledTools {
		err := pc.CheckPermission(ctx, tool, nil)
		if err == nil {
			t.Errorf("disabled tool '%s' should be blocked even in AUTO_ACCEPT mode", tool)
		}
		if err != nil && !contains(err.Error(), "is disabled") {
			t.Errorf("expected disabled error for '%s', got: %v", tool, err)
		}
	}

	// Allowed tools should still work
	err := pc.CheckPermission(ctx, "execute_sql", nil)
	if err != nil {
		t.Errorf("non-disabled tool should be allowed, got: %v", err)
	}

	// Test disabled tools across all modes
	modes := []loomv1.PermissionMode{
		loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
		loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
		loomv1.PermissionMode_PERMISSION_MODE_PLAN,
	}

	for _, mode := range modes {
		pc.SetMode(mode)
		err := pc.CheckPermission(ctx, "shell_execute", nil)
		if err == nil {
			t.Errorf("mode %v: disabled tool should be blocked", mode)
		}
		if err != nil && !contains(err.Error(), "is disabled") {
			t.Errorf("mode %v: expected disabled error, got: %v", mode, err)
		}
	}
}

func TestPermissionChecker_CheckPermission_AllowedTools(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		Mode:         loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE, // Strict mode
		AllowedTools: []string{"execute_sql", "file_read"},
		DefaultAction: "deny",
	})

	ctx := context.Background()

	// Whitelisted tools should be allowed in ALL modes
	allowedTools := []string{"execute_sql", "file_read"}
	for _, tool := range allowedTools {
		err := pc.CheckPermission(ctx, tool, nil)
		if err != nil {
			t.Errorf("whitelisted tool '%s' should be allowed, got: %v", tool, err)
		}
	}

	// Non-whitelisted tools should require approval
	err := pc.CheckPermission(ctx, "file_write", nil)
	if err == nil {
		t.Error("non-whitelisted tool should require approval in ASK_BEFORE mode")
	}

	// Test whitelisted tools across all modes (including PLAN)
	modes := []loomv1.PermissionMode{
		loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
		loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
		loomv1.PermissionMode_PERMISSION_MODE_PLAN,
	}

	for _, mode := range modes {
		pc.SetMode(mode)
		for _, tool := range allowedTools {
			err := pc.CheckPermission(ctx, tool, nil)
			if err != nil {
				t.Errorf("mode %v: whitelisted tool '%s' should be allowed, got: %v", mode, tool, err)
			}
		}
	}
}

func TestPermissionChecker_CheckPermission_DisabledOverridesAllowed(t *testing.T) {
	// Disabled tools should take precedence over allowed tools
	pc := NewPermissionChecker(PermissionConfig{
		Mode:          loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
		AllowedTools:  []string{"execute_sql"},
		DisabledTools: []string{"execute_sql"}, // Same tool in both lists
	})

	ctx := context.Background()

	// Tool should be blocked because disabled takes precedence
	err := pc.CheckPermission(ctx, "execute_sql", nil)
	if err == nil {
		t.Error("disabled tool should be blocked even if in allowed list")
	}
	if err != nil && !contains(err.Error(), "is disabled") {
		t.Errorf("expected disabled error, got: %v", err)
	}
}

func TestPermissionChecker_CheckPermission_DefaultAction(t *testing.T) {
	tests := []struct {
		name          string
		defaultAction string
		expectError   bool
	}{
		{
			name:          "default allow",
			defaultAction: "allow",
			expectError:   false,
		},
		{
			name:          "default deny",
			defaultAction: "deny",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := NewPermissionChecker(PermissionConfig{
				Mode:          loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
				DefaultAction: tt.defaultAction,
			})

			ctx := context.Background()
			err := pc.CheckPermission(ctx, "some_tool", nil)

			if tt.expectError && err == nil {
				t.Error("expected error with deny default action")
			}
			if !tt.expectError && err != nil {
				t.Errorf("expected no error with allow default action, got: %v", err)
			}
		})
	}
}

func TestPermissionChecker_LegacyMethods(t *testing.T) {
	// Test backward compatibility methods
	pc := NewPermissionChecker(PermissionConfig{
		YOLO: true,
	})

	if !pc.IsYOLOMode() {
		t.Error("IsYOLOMode() should return true when YOLO flag is set")
	}

	pc2 := NewPermissionChecker(PermissionConfig{
		RequireApproval: true,
	})

	if !pc2.RequiresApproval() {
		t.Error("RequiresApproval() should return true when RequireApproval flag is set")
	}
}

func TestPermissionChecker_IsToolAllowed(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		AllowedTools: []string{"execute_sql", "file_read"},
	})

	if !pc.IsToolAllowed("execute_sql") {
		t.Error("IsToolAllowed should return true for whitelisted tool")
	}

	if !pc.IsToolAllowed("file_read") {
		t.Error("IsToolAllowed should return true for whitelisted tool")
	}

	if pc.IsToolAllowed("file_write") {
		t.Error("IsToolAllowed should return false for non-whitelisted tool")
	}
}

func TestPermissionChecker_IsToolDisabled(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		DisabledTools: []string{"shell_execute", "file_delete"},
	})

	if !pc.IsToolDisabled("shell_execute") {
		t.Error("IsToolDisabled should return true for disabled tool")
	}

	if !pc.IsToolDisabled("file_delete") {
		t.Error("IsToolDisabled should return true for disabled tool")
	}

	if pc.IsToolDisabled("execute_sql") {
		t.Error("IsToolDisabled should return false for non-disabled tool")
	}
}

func TestPermissionChecker_Concurrent(t *testing.T) {
	// Test concurrent mode switching (runtime behavior)
	pc := NewPermissionChecker(PermissionConfig{
		Mode: loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
	})

	ctx := context.Background()

	// Simulate Canvas AI switching modes during session
	modes := []loomv1.PermissionMode{
		loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
		loomv1.PermissionMode_PERMISSION_MODE_PLAN,
		loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE,
		loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT,
	}

	for _, mode := range modes {
		pc.SetMode(mode)

		// Verify mode was set
		if pc.GetMode() != mode {
			t.Errorf("GetMode() = %v, want %v", pc.GetMode(), mode)
		}

		// Verify behavior matches mode
		err := pc.CheckPermission(ctx, "some_tool", nil)

		switch mode {
		case loomv1.PermissionMode_PERMISSION_MODE_AUTO_ACCEPT:
			if err != nil {
				t.Errorf("AUTO_ACCEPT mode should allow tool, got: %v", err)
			}
		case loomv1.PermissionMode_PERMISSION_MODE_PLAN:
			if !errors.Is(err, ErrToolExecutionDeferred) {
				t.Errorf("PLAN mode should return ErrToolExecutionDeferred, got: %v", err)
			}
		case loomv1.PermissionMode_PERMISSION_MODE_ASK_BEFORE:
			// Expected to fail without callback implementation
			if err == nil {
				t.Error("ASK_BEFORE mode should return error without callback")
			}
		}
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
