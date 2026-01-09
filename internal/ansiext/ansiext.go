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
// Package ansiext provides ANSI escape sequence utilities.
package ansiext

import (
	"strings"
)

// Strip removes ANSI escape sequences from a string.
func Strip(s string) string {
	// Simple implementation - strips common ANSI sequences
	result := s
	// Remove common ANSI escape sequences
	for strings.Contains(result, "\x1b[") {
		start := strings.Index(result, "\x1b[")
		end := start + 2
		// Find the end of the escape sequence
		for end < len(result) && result[end] != 'm' && result[end] != 'K' && result[end] != 'J' && result[end] != 'H' {
			end++
		}
		if end < len(result) {
			end++
		}
		result = result[:start] + result[end:]
	}
	return result
}

// Width returns the visible width of a string (excluding ANSI codes).
func Width(s string) int {
	return len(Strip(s))
}

// Escape escapes ANSI sequences in a string for safe rendering.
func Escape(s string) string {
	// Replace escape characters with their escaped representation
	return strings.ReplaceAll(s, "\x1b", "\\x1b")
}
