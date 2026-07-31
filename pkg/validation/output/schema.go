// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package output provides agent output validation primitives: JSON extraction
// from mixed prose, JSON Schema validation, retry-feedback prompt building,
// and PASS/FAIL verdict parsing.
//
// It is a leaf package (gojsonschema + stdlib only) shared by the workflow
// pipeline executor (pkg/orchestration) and the agent conversation loop
// (pkg/agent). The parent package pkg/validation validates agent/workflow
// YAML *definitions*; this package validates agent *outputs* at runtime.
package output

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// ExtractJSON attempts to find and parse JSON from mixed text+JSON output.
// Returns the extracted JSON string if found, or empty string if no valid
// JSON was found.
func ExtractJSON(text string) string {
	// Try parsing the whole output as JSON first
	text = strings.TrimSpace(text)
	if isValidJSON(text) {
		return text
	}

	// Strip markdown code fences: ```json ... ``` or ``` ... ```
	if extracted := extractFromCodeFences(text); extracted != "" && isValidJSON(extracted) {
		return extracted
	}

	// Search for JSON object: find outermost { ... }
	if extracted := extractOutermostJSON(text, '{', '}'); extracted != "" {
		return extracted
	}

	// Search for JSON array: find outermost [ ... ]
	if extracted := extractOutermostJSON(text, '[', ']'); extracted != "" {
		return extracted
	}

	return ""
}

// ValidateJSONSchema validates text against a JSON Schema. JSON embedded in
// prose is extracted first. Returns (extractedJSON, nil) if valid so the
// caller may normalize its output, or ("", error) describing the failure.
func ValidateJSONSchema(text string, schema string) (string, error) {
	jsonStr := ExtractJSON(text)
	if jsonStr == "" {
		return "", fmt.Errorf("no valid JSON found in output")
	}

	schemaLoader := gojsonschema.NewStringLoader(schema)
	documentLoader := gojsonschema.NewStringLoader(jsonStr)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return "", fmt.Errorf("schema validation error: %w", err)
	}

	if !result.Valid() {
		var violations []string
		for _, verr := range result.Errors() {
			violations = append(violations, verr.String())
		}
		return "", fmt.Errorf("schema violations: %s", strings.Join(violations, "; "))
	}

	return jsonStr, nil
}

// isValidJSON checks if a string is valid JSON.
func isValidJSON(s string) bool {
	var v any
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
