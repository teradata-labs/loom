// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatWorkflowMetadata(t *testing.T) {
	assert.Empty(t, formatWorkflowMetadata(nil))
	out := formatWorkflowMetadata(map[string]string{
		"join.overflow":           "pre_reduce",
		"agent_count":             "3",
		"history.entries_reduced": "2",
		"long":                    strings.Repeat("x", 300),
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Equal(t, "Metadata:", lines[0])
	assert.Equal(t, []string{"  agent_count: 3", "  history.entries_reduced: 2", "  join.overflow: pre_reduce"}, lines[1:4], "sorted")
	assert.True(t, strings.HasSuffix(lines[4], "..."), "long values are cut")
	assert.Less(t, len(lines[4]), 220)
}
