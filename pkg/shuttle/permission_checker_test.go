// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionChecker_WildcardAndExactMatching(t *testing.T) {
	pc := NewPermissionChecker(PermissionConfig{
		RequireApproval: true,
		DefaultAction:   "deny",
		AllowedTools:    []string{"web_search", "opendata:*"},
		DisabledTools:   []string{"shell_execute", "danger:*"},
	})

	tests := []struct {
		name    string
		tool    string
		allowed bool
	}{
		{"exact allow", "web_search", true},
		{"prefix allow (opendata server)", "opendata:query", true},
		{"prefix allow another opendata tool", "opendata:search_datasets", true},
		{"exact disable", "shell_execute", false},
		{"prefix disable", "danger:rm", false},
		{"unlisted denied by default", "file_read", false},
		// disabled prefix must win even if an allow prefix would also match
		{"disable beats allow on overlap", "danger:opendata", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := pc.CheckPermission(context.Background(), tc.tool, nil)
			if tc.allowed {
				assert.NoError(t, err, "%s should be allowed", tc.tool)
			} else {
				require.Error(t, err, "%s should be denied", tc.tool)
			}
		})
	}

	// Helper predicates honor patterns too.
	assert.True(t, pc.IsToolAllowed("opendata:figures"))
	assert.False(t, pc.IsToolAllowed("file_write"))
	assert.True(t, pc.IsToolDisabled("danger:anything"))
}

func TestPermissionChecker_Advertisable(t *testing.T) {
	// Lab-style config: approval required, default deny, with an allow-list and a
	// hard-disabled set. A tool is advertised to the LLM only if it could run.
	lab := NewPermissionChecker(PermissionConfig{
		RequireApproval: true,
		DefaultAction:   "deny",
		AllowedTools:    []string{"web_search", "opendata:*"},
		DisabledTools:   []string{"shell_execute", "danger:*"},
	})
	labCases := []struct {
		name string
		tool string
		want bool
	}{
		{"allow-listed exact is advertised", "web_search", true},
		{"allow-listed prefix is advertised", "opendata:query", true},
		{"hard-disabled is hidden", "shell_execute", false},
		{"disabled prefix is hidden", "danger:rm", false},
		{"unlisted + approval-required + deny is hidden", "file_read", false},
		{"disabled wins over allow overlap", "danger:opendata", false},
	}
	for _, tc := range labCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lab.Advertisable(tc.tool))
			// Invariant: anything advertised must actually pass CheckPermission,
			// and anything hidden must be denied — the two must never disagree.
			err := lab.CheckPermission(context.Background(), tc.tool, nil)
			assert.Equal(t, tc.want, err == nil,
				"Advertisable(%q) must match CheckPermission outcome", tc.tool)
		})
	}

	// require_approval=false: unlisted tools are advertised (they'd be allowed).
	open := NewPermissionChecker(PermissionConfig{
		RequireApproval: false,
		DisabledTools:   []string{"shell_execute"},
	})
	assert.True(t, open.Advertisable("anything"), "unlisted tool advertised when approval not required")
	assert.False(t, open.Advertisable("shell_execute"), "disabled tool hidden even when approval not required")

	// default_action=allow: unlisted tools are advertised even with approval on.
	defAllow := NewPermissionChecker(PermissionConfig{
		RequireApproval: true,
		DefaultAction:   "allow",
		DisabledTools:   []string{"shell_execute"},
	})
	assert.True(t, defAllow.Advertisable("unlisted_tool"), "unlisted advertised when default action is allow")
	assert.False(t, defAllow.Advertisable("shell_execute"), "disabled still hidden under default allow")

	// YOLO advertises everything, including disabled.
	yolo := NewPermissionChecker(PermissionConfig{YOLO: true, DisabledTools: []string{"shell_execute"}})
	assert.True(t, yolo.Advertisable("shell_execute"), "YOLO advertises everything")

	// A nil checker advertises everything (no restriction configured).
	var nilPC *PermissionChecker
	assert.True(t, nilPC.Advertisable("anything"), "nil checker advertises everything")
}

func TestPermissionChecker_BareWildcardAndYOLO(t *testing.T) {
	// A bare "*" in allowed_tools allows everything (require_approval still set).
	allowAll := NewPermissionChecker(PermissionConfig{
		RequireApproval: true,
		DefaultAction:   "deny",
		AllowedTools:    []string{"*"},
	})
	assert.NoError(t, allowAll.CheckPermission(context.Background(), "anything_at_all", nil))

	// YOLO bypasses everything, including disabled lists.
	yolo := NewPermissionChecker(PermissionConfig{
		YOLO:          true,
		DisabledTools: []string{"shell_execute"},
	})
	assert.NoError(t, yolo.CheckPermission(context.Background(), "shell_execute", nil))
}
