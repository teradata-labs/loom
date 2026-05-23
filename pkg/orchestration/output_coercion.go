// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"encoding/json"
	"regexp"
	"strings"
)

// coerceBranchKey attempts to extract a valid branch key from verbose agent output.
// It applies lightweight text transformations (strip markdown, prefixes, punctuation)
// and checks for standalone word matches. Returns ("", false) if ambiguous (multiple
// keys found) or no match. This avoids burning an LLM retry call for simple formatting issues.
func coerceBranchKey(output string, branchKeys []string) (string, bool) {
	if len(branchKeys) == 0 {
		return "", false
	}

	// Normalize: lowercase, trim whitespace
	cleaned := strings.TrimSpace(strings.ToLower(output))

	// Strip markdown formatting: **, *, `, ```
	cleaned = stripMarkdown(cleaned)

	// Strip common prefix patterns
	cleaned = stripCommonPrefixes(cleaned)

	// Strip trailing punctuation
	cleaned = strings.TrimRight(cleaned, ".,!?;:")

	// Re-trim after stripping
	cleaned = strings.TrimSpace(cleaned)

	// Try exact match on the cleaned string
	for _, key := range branchKeys {
		if cleaned == strings.ToLower(key) {
			return key, true
		}
	}

	// Try word-boundary matching: find keys that appear as standalone words
	var matches []string
	for _, key := range branchKeys {
		pattern := `(?i)\b` + regexp.QuoteMeta(key) + `\b`
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(cleaned) {
			matches = append(matches, key)
		}
	}

	// Unambiguous: exactly one key matched
	if len(matches) == 1 {
		return matches[0], true
	}

	// Ambiguous or no match
	return "", false
}

// stripMarkdown removes common markdown formatting artifacts.
func stripMarkdown(s string) string {
	// Remove code fences
	s = strings.ReplaceAll(s, "```", "")
	// Remove bold markers
	s = strings.ReplaceAll(s, "**", "")
	// Remove italic markers (single *)
	// Be careful not to remove bullet points — only strip surrounding *
	if strings.HasPrefix(s, "*") && strings.HasSuffix(s, "*") && len(s) > 2 {
		s = s[1 : len(s)-1]
	}
	// Remove inline code backticks
	s = strings.ReplaceAll(s, "`", "")
	return s
}

// stripCommonPrefixes removes common LLM output prefixes that precede the actual answer.
func stripCommonPrefixes(s string) string {
	prefixes := []string{
		"the answer is:",
		"the answer is",
		"i classify this as:",
		"i classify this as",
		"based on my analysis,",
		"based on my analysis:",
		"based on the analysis,",
		"based on the analysis:",
		"i would classify this as:",
		"i would classify this as",
		"this is:",
		"the category is:",
		"the category is",
		"my choice is:",
		"my choice is",
		"i choose:",
		"i choose",
		"result:",
		"output:",
	}

	lower := strings.ToLower(s)
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			s = strings.TrimSpace(s[len(prefix):])
			lower = strings.ToLower(s)
		}
	}
	return s
}

// extractJSONFromText attempts to find and parse JSON from mixed text+JSON output.
// Returns the extracted JSON string if found, or empty string if no valid JSON found.
func extractJSONFromText(output string) string {
	// Try parsing the whole output as JSON first
	output = strings.TrimSpace(output)
	if isValidJSON(output) {
		return output
	}

	// Strip markdown code fences: ```json ... ``` or ``` ... ```
	if extracted := extractFromCodeFences(output); extracted != "" && isValidJSON(extracted) {
		return extracted
	}

	// Search for JSON object: find outermost { ... }
	if extracted := extractOutermostJSON(output, '{', '}'); extracted != "" {
		return extracted
	}

	// Search for JSON array: find outermost [ ... ]
	if extracted := extractOutermostJSON(output, '[', ']'); extracted != "" {
		return extracted
	}

	return ""
}

// isValidJSON checks if a string is valid JSON.
func isValidJSON(s string) bool {
	var v interface{}
	return json.Unmarshal([]byte(s), &v) == nil
}

// extractFromCodeFences extracts content from markdown code fences.
func extractFromCodeFences(s string) string {
	// Match ```json\n...\n``` or ```\n...\n```
	re := regexp.MustCompile("(?s)```(?:json)?\\s*\n?(.*?)\n?```")
	matches := re.FindStringSubmatch(s)
	if len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// extractOutermostJSON finds the outermost balanced JSON structure in text.
// If the first balanced candidate is not valid JSON, continues searching
// for the next occurrence.
func extractOutermostJSON(s string, open, close byte) string {
	searchFrom := 0
	for searchFrom < len(s) {
		start := strings.IndexByte(s[searchFrom:], open)
		if start == -1 {
			return ""
		}
		start += searchFrom

		depth := 0
		inString := false
		escaped := false

		for i := start; i < len(s); i++ {
			if escaped {
				escaped = false
				continue
			}

			ch := s[i]
			if ch == '\\' && inString {
				escaped = true
				continue
			}

			if ch == '"' {
				inString = !inString
				continue
			}

			if inString {
				continue
			}

			if ch == open {
				depth++
			} else if ch == close {
				depth--
				if depth == 0 {
					candidate := s[start : i+1]
					if isValidJSON(candidate) {
						return candidate
					}
					// Not valid JSON — continue searching after this candidate
					searchFrom = i + 1
					break
				}
			}
		}

		// If we exited the inner loop without finding a balanced close, stop
		if depth != 0 {
			return ""
		}
	}

	return ""
}
