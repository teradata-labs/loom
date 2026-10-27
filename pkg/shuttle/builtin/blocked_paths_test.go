// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchBlockedPathFold(t *testing.T) {
	blocked := []string{"/etc", "C:/Windows/System32"}

	tests := []struct {
		name     string
		path     string
		foldCase bool
		want     bool
	}{
		{"exact entry", "/etc", false, true},
		{"child of entry", "/etc/passwd", false, true},
		{"sibling sharing prefix", "/etcetera/file", false, false},
		{"unrelated", "/home/user/file", false, false},
		{"windows native separators", `C:\Windows\System32\drivers`, false, true},
		{"windows case differs, exact mode", "c:/windows/system32", false, false},
		{"windows lower-case, fold mode", "c:/windows/system32/config/sam", true, true},
		{"windows upper-case native, fold mode", `C:\WINDOWS\System32`, true, true},
		{"fold mode sibling sharing prefix", "C:/Windows/System32x", true, false},
		{"fold mode unrelated", "D:/data/file", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, got := matchBlockedPathFold(tt.path, blocked, tt.foldCase)
			assert.Equal(t, tt.want, got)
			if got {
				assert.Contains(t, blocked, entry)
			}
		})
	}
}
