// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package output

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildRetryFeedback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		params      FeedbackParams
		contains    []string
		notContains []string
	}{
		{
			name: "schema requirement included",
			params: FeedbackParams{
				PreviousOutput:      "bad output",
				Failure:             "schema violations: name is required",
				Requirement:         `{"type":"object"}`,
				RequirementIsSchema: true,
				IncludeValidValues:  true,
				Attempt:             1,
				MaxRetries:          3,
			},
			contains: []string{
				"OUTPUT VALIDATION FAILED (retry 1 of 3)",
				"bad output",
				"schema violations: name is required",
				"REQUIRED JSON SCHEMA",
				`{"type":"object"}`,
				"Output ONLY the JSON object",
			},
			notContains: []string{"ORIGINAL TASK"},
		},
		{
			name: "criteria requirement included",
			params: FeedbackParams{
				PreviousOutput:      "bad output",
				Failure:             "acceptance criteria not met: no citation",
				Requirement:         "Answer must cite a table name.",
				RequirementIsSchema: false,
				IncludeValidValues:  true,
				Attempt:             2,
				MaxRetries:          2,
			},
			contains: []string{
				"OUTPUT VALIDATION FAILED (retry 2 of 2)",
				"ACCEPTANCE CRITERIA",
				"Answer must cite a table name.",
				"satisfies every criterion",
			},
			notContains: []string{"REQUIRED JSON SCHEMA", "ORIGINAL TASK"},
		},
		{
			name: "requirement suppressed when IncludeValidValues false",
			params: FeedbackParams{
				PreviousOutput:      "bad output",
				Failure:             "schema violations",
				Requirement:         `{"type":"object","secret":"do-not-show"}`,
				RequirementIsSchema: true,
				IncludeValidValues:  false,
				Attempt:             1,
				MaxRetries:          1,
			},
			contains:    []string{"WHY IT FAILED", "satisfies the validation criteria"},
			notContains: []string{"do-not-show", "REQUIRED JSON SCHEMA"},
		},
		{
			name: "previous output truncated at 500 chars",
			params: FeedbackParams{
				PreviousOutput: strings.Repeat("x", 600),
				Failure:        "too long",
				Attempt:        1,
				MaxRetries:     1,
			},
			contains:    []string{strings.Repeat("x", 500) + "... (truncated)"},
			notContains: []string{strings.Repeat("x", 501)},
		},
		{
			name: "custom template substitutes all variables",
			params: FeedbackParams{
				PreviousOutput: "prev",
				Failure:        "boom",
				Attempt:        2,
				MaxRetries:     5,
				Template:       "err={{error}} out={{previous_output}} try {{attempt}}/{{max_retries}}",
			},
			contains:    []string{"err=boom out=prev try 2/5"},
			notContains: []string{"OUTPUT VALIDATION FAILED"},
		},
		{
			name: "unknown template variables are left visible",
			params: FeedbackParams{
				Failure: "boom",
				Attempt: 1, MaxRetries: 1,
				Template: "reason={{error}} misc={{unknown_var}}",
			},
			contains: []string{"reason=boom", "{{unknown_var}}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := BuildRetryFeedback(tt.params)
			for _, want := range tt.contains {
				assert.Contains(t, got, want)
			}
			for _, notWant := range tt.notContains {
				assert.NotContains(t, got, notWant)
			}
		})
	}
}
