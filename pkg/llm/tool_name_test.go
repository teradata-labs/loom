// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no change needed", "execute_sql", "execute_sql"},
		{"single colon", "vantage-mcp:execute_sql", "vantage-mcp_execute_sql"},
		{"multiple colons", "server:namespace:tool", "server_namespace_tool"},
		{"leading colon", ":tool", "_tool"},
		{"empty string", "", ""},
		{"no special chars", "simple_tool_name", "simple_tool_name"},
		{"dots and dashes preserved", "my.tool-name", "my.tool-name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeToolName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestReverseToolName(t *testing.T) {
	nameMap := map[string]string{
		"vantage-mcp_execute_sql": "vantage-mcp:execute_sql",
		"fs_read_file":            "fs:read_file",
	}

	// Found in map
	assert.Equal(t, "vantage-mcp:execute_sql", ReverseToolName(nameMap, "vantage-mcp_execute_sql"))

	// Not in map - returns input unchanged
	assert.Equal(t, "unknown_tool", ReverseToolName(nameMap, "unknown_tool"))

	// Nil map - returns input unchanged
	assert.Equal(t, "any_tool", ReverseToolName(nil, "any_tool"))
}

func TestBuildToolNameMap(t *testing.T) {
	names := []string{"vantage-mcp:execute_sql", "fs:read_file", "simple_tool"}
	m := BuildToolNameMap(names)

	assert.Equal(t, "vantage-mcp:execute_sql", m["vantage-mcp_execute_sql"])
	assert.Equal(t, "fs:read_file", m["fs_read_file"])
	assert.Equal(t, "simple_tool", m["simple_tool"])
}
