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
package prompts

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Interpolate performs safe variable substitution in a prompt template.
//
// Uses {{.variable_name}} syntax (like Go templates but simpler).
// All values are escaped to prevent prompt injection attacks.
//
// Example:
//
//	template := "You are a {{.role}} agent for {{.backend_type}}"
//	result := Interpolate(template, map[string]interface{}{
//	    "role": "SQL",
//	    "backend_type": "Teradata",
//	})
//	// Returns: "You are a SQL agent for Teradata"
func Interpolate(template string, vars map[string]interface{}) string {
	if vars == nil {
		return template
	}

	// Find all {{.variable}} placeholders
	re := regexp.MustCompile(`\{\{\.(\w+)\}\}`)

	result := re.ReplaceAllStringFunc(template, func(match string) string {
		// Extract variable name
		varName := strings.TrimPrefix(strings.TrimSuffix(match, "}}"), "{{.")

		// Look up value
		value, ok := vars[varName]
		if !ok {
			// Keep placeholder if variable not provided
			return match
		}

		// Convert to string and escape
		return escapeValue(value)
	})

	return result
}

// escapeValue converts a value to string and escapes it to prevent injection.
func escapeValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return escapeString(v)
	case int, int64, int32, float64, float32:
		return fmt.Sprintf("%v", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case []string:
		// Join with commas
		escaped := make([]string, len(v))
		for i, s := range v {
			escaped[i] = escapeString(s)
		}
		return strings.Join(escaped, ", ")
	default:
		// Default: fmt.Sprintf with escaping
		return escapeString(fmt.Sprintf("%v", v))
	}
}

// escapeString escapes special characters to prevent prompt injection.
//
// Implements multiple escaping strategies for production use:
// - Control character removal
// - XML/HTML entity escaping
// - Unicode normalization
// - Prompt injection pattern detection
func escapeString(s string) string {
	// 1. Remove null bytes and invalid UTF-8
	s = strings.ReplaceAll(s, "\x00", "")
	if !utf8.ValidString(s) {
		// Fix invalid UTF-8 by replacing invalid runes
		s = strings.ToValidUTF8(s, "")
	}

	// 2. Normalize line endings (convert to spaces to prevent prompt boundary manipulation)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")

	// 3. Escape XML/HTML entities to prevent injection through markup
	s = html.EscapeString(s)

	// 4. Remove or escape other control characters (C0 and C1 control codes)
	var result strings.Builder
	result.Grow(len(s))
	for _, r := range s {
		// Skip control characters except space
		if unicode.IsControl(r) && r != ' ' {
			continue
		}
		result.WriteRune(r)
	}
	s = result.String()

	// 5. Detect and sanitize common prompt injection patterns
	s = sanitizePromptInjection(s)

	// 6. Collapse multiple spaces
	s = strings.Join(strings.Fields(s), " ")

	// 7. Trim leading/trailing whitespace
	s = strings.TrimSpace(s)

	return s
}

// sanitizePromptInjection detects and removes common prompt injection patterns.
func sanitizePromptInjection(s string) string {
	// Remove common prompt injection delimiters and commands
	injectionPatterns := []string{
		"```",              // Code blocks
		"###",              // Headers
		"---",              // Separators
		"System:",          // System prompts
		"Assistant:",       // Assistant prompts
		"Human:",           // Human prompts
		"[INST]",           // Instruction markers
		"[/INST]",          // Instruction markers
		"<|im_start|>",     // Instruction markers
		"<|im_end|>",       // Instruction markers
		"### Instruction:", // Alpaca-style
		"### Response:",    // Alpaca-style
	}

	for _, pattern := range injectionPatterns {
		// Replace with escaped version (spaces instead of special chars)
		s = strings.ReplaceAll(s, pattern, strings.Repeat(" ", len(pattern)))
	}

	return s
}
