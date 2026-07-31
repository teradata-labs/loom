// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package output

import "strings"

// ParseVerdict parses a strict first-line verdict from an LLM evaluation
// response: `PASS`, or `FAIL: <reason>` (bare `FAIL` is accepted with an
// empty reason). Matching is case-insensitive on the token but requires the
// verdict to be the first non-empty line. ok is false when the response is
// malformed — callers must treat that as inconclusive (fail-open), never as
// a failed validation.
func ParseVerdict(response string) (pass bool, reason string, ok bool) {
	line := firstNonEmptyLine(response)
	if line == "" {
		return false, "", false
	}

	upper := strings.ToUpper(line)
	switch {
	case upper == "PASS":
		return true, "", true
	case upper == "FAIL":
		return false, "", true
	case strings.HasPrefix(upper, "FAIL:"):
		return false, strings.TrimSpace(line[len("FAIL:"):]), true
	default:
		return false, "", false
	}
}

// firstNonEmptyLine returns the first non-blank line of s, trimmed.
func firstNonEmptyLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
