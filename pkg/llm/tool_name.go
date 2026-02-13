// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package llm

import "strings"

// SanitizeToolName converts a tool name to LLM-provider-compatible format.
// Many LLM providers require tool names to match restricted patterns:
//   - Azure OpenAI: ^[a-zA-Z0-9_.\-]+$
//   - Bedrock: ^[a-zA-Z0-9_-]{1,64}$
//   - Gemini: ^[a-zA-Z_][a-zA-Z0-9_]*$
//
// MCP tools use colon namespacing (e.g., "vantage-mcp:execute_sql")
// which breaks these patterns. This function replaces colons with underscores.
func SanitizeToolName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, ch := range name {
		if ch == ':' {
			b.WriteRune('_')
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// BuildToolNameMap creates a bidirectional mapping from sanitized → original tool names.
// Returns the map and a slice of sanitized names (useful for debugging).
func BuildToolNameMap(names []string) map[string]string {
	m := make(map[string]string, len(names))
	for _, name := range names {
		sanitized := SanitizeToolName(name)
		m[sanitized] = name
	}
	return m
}

// ReverseToolName maps a sanitized tool name back to its original.
// Returns the original name if found, otherwise returns the sanitized name unchanged.
func ReverseToolName(nameMap map[string]string, sanitizedName string) string {
	if original, exists := nameMap[sanitizedName]; exists {
		return original
	}
	return sanitizedName
}
