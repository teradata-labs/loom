// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorkspaceTool_RequiredArgsFor(t *testing.T) {
	tool := &WorkspaceTool{}
	cases := []struct {
		action string
		want   []string
	}{
		{"write", []string{"filename", "content"}},
		{"read", []string{"filename"}},
		{"delete", []string{"filename"}},
		{"search", []string{"query"}},
		{"list", nil},
		{"", nil},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			assert.Equal(t, c.want, tool.RequiredArgsFor(map[string]interface{}{"action": c.action}))
		})
	}
}
