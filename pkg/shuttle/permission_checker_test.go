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
