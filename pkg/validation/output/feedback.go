// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package output

import (
	"fmt"
	"strconv"
	"strings"
)

// maxPreviousOutputChars bounds how much of the failed output is echoed back
// into the retry prompt.
const maxPreviousOutputChars = 500

// FeedbackParams carries everything needed to build a retry-feedback prompt
// after output validation fails.
type FeedbackParams struct {
	// PreviousOutput is the output that failed validation (truncated to
	// maxPreviousOutputChars in the prompt).
	PreviousOutput string

	// Failure describes why validation failed.
	Failure string

	// Requirement is the JSON schema or acceptance-criteria text shown to the
	// model when IncludeValidValues is set.
	Requirement string

	// RequirementIsSchema selects schema-specific instructions (output ONLY
	// JSON) over generic criteria instructions.
	RequirementIsSchema bool

	// IncludeValidValues includes Requirement in the prompt.
	IncludeValidValues bool

	// Attempt is the 1-based retry attempt number.
	Attempt int

	// MaxRetries is the retry cap for this policy.
	MaxRetries int

	// Template optionally overrides the default prompt. Supported variables:
	// {{error}}, {{previous_output}}, {{attempt}}, {{max_retries}}.
	Template string
}

// BuildRetryFeedback constructs a retry prompt that explains what went wrong
// and shows the expected output format. It contains no "original task"
// section — CONTINUE-mode retries happen inside a live conversation that
// already carries the task; FRESH-mode callers append their own task section.
func BuildRetryFeedback(p FeedbackParams) string {
	if p.Template != "" {
		return substituteFeedbackVars(p)
	}

	var sb strings.Builder

	fmt.Fprintf(&sb, "⚠️ OUTPUT VALIDATION FAILED (retry %d of %d)\n\n", p.Attempt, p.MaxRetries)

	sb.WriteString("YOUR PREVIOUS OUTPUT:\n---\n")
	sb.WriteString(truncate(p.PreviousOutput, maxPreviousOutputChars))
	sb.WriteString("\n---\n\n")

	sb.WriteString("WHY IT FAILED:\n")
	sb.WriteString(p.Failure)
	sb.WriteString("\n\n")

	if p.IncludeValidValues && p.Requirement != "" {
		if p.RequirementIsSchema {
			sb.WriteString("REQUIRED JSON SCHEMA:\n")
			sb.WriteString(p.Requirement)
			sb.WriteString("\n\n")
			sb.WriteString("WHAT TO DO:\n")
			sb.WriteString("1. Your output MUST be valid JSON conforming to the schema above.\n")
			sb.WriteString("2. Output ONLY the JSON object — no markdown, no explanation, no code fences.\n")
			sb.WriteString("3. Ensure all required fields are present and have the correct types.\n")
		} else {
			sb.WriteString("ACCEPTANCE CRITERIA:\n")
			sb.WriteString(p.Requirement)
			sb.WriteString("\n\n")
			sb.WriteString("WHAT TO DO:\n")
			sb.WriteString("1. Revise your answer so it satisfies every criterion above.\n")
			sb.WriteString("2. Respond with the corrected answer only — no meta-commentary about the failure.\n")
		}
	} else {
		sb.WriteString("WHAT TO DO:\n")
		sb.WriteString("1. Ensure your output satisfies the validation criteria.\n")
		sb.WriteString("2. If the validation expects a specific format (JSON, structured data, etc.),\n")
		sb.WriteString("   output ONLY that format with no surrounding explanation.\n")
	}

	return sb.String()
}

// substituteFeedbackVars renders a custom feedback template. Unknown
// {{variables}} are left intact so template mistakes are visible, not silent.
func substituteFeedbackVars(p FeedbackParams) string {
	r := strings.NewReplacer(
		"{{error}}", p.Failure,
		"{{previous_output}}", truncate(p.PreviousOutput, maxPreviousOutputChars),
		"{{attempt}}", strconv.Itoa(p.Attempt),
		"{{max_retries}}", strconv.Itoa(p.MaxRetries),
	)
	return r.Replace(p.Template)
}

// truncate shortens s to at most n characters, appending a marker when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "... (truncated)"
}
