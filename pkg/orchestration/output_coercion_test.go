// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCoerceBranchKey(t *testing.T) {
	t.Parallel()

	branchKeys := []string{"bug", "feature", "refactor"}

	tests := []struct {
		name      string
		output    string
		keys      []string
		wantKey   string
		wantFound bool
	}{
		{
			name:      "exact match",
			output:    "bug",
			keys:      branchKeys,
			wantKey:   "bug",
			wantFound: true,
		},
		{
			name:      "case insensitive match",
			output:    "BUG",
			keys:      branchKeys,
			wantKey:   "bug",
			wantFound: true,
		},
		{
			name:      "with trailing punctuation",
			output:    "bug.",
			keys:      branchKeys,
			wantKey:   "bug",
			wantFound: true,
		},
		{
			name:      "with markdown bold",
			output:    "**bug**",
			keys:      branchKeys,
			wantKey:   "bug",
			wantFound: true,
		},
		{
			name:      "with backticks",
			output:    "`bug`",
			keys:      branchKeys,
			wantKey:   "bug",
			wantFound: true,
		},
		{
			name:      "with common prefix - the answer is",
			output:    "The answer is: bug",
			keys:      branchKeys,
			wantKey:   "bug",
			wantFound: true,
		},
		{
			name:      "with common prefix - I classify this as",
			output:    "I classify this as feature",
			keys:      branchKeys,
			wantKey:   "feature",
			wantFound: true,
		},
		{
			name:      "with common prefix - based on my analysis",
			output:    "Based on my analysis, refactor",
			keys:      branchKeys,
			wantKey:   "refactor",
			wantFound: true,
		},
		{
			name:      "standalone word in sentence",
			output:    "This looks like a bug report to me",
			keys:      branchKeys,
			wantKey:   "bug",
			wantFound: true,
		},
		{
			name:      "no match",
			output:    "I'm not sure what category this falls into",
			keys:      branchKeys,
			wantKey:   "",
			wantFound: false,
		},
		{
			name:      "ambiguous - multiple keys present",
			output:    "This could be either a bug or a feature",
			keys:      branchKeys,
			wantKey:   "",
			wantFound: false,
		},
		{
			name:      "empty output",
			output:    "",
			keys:      branchKeys,
			wantKey:   "",
			wantFound: false,
		},
		{
			name:      "empty keys",
			output:    "bug",
			keys:      []string{},
			wantKey:   "",
			wantFound: false,
		},
		{
			name:      "whitespace only",
			output:    "   ",
			keys:      branchKeys,
			wantKey:   "",
			wantFound: false,
		},
		{
			name:      "multi-word branch key",
			output:    "The answer is: new feature",
			keys:      []string{"new feature", "bug fix", "refactor"},
			wantKey:   "new feature",
			wantFound: true,
		},
		{
			name:      "key with special regex chars",
			output:    "The answer is: c++",
			keys:      []string{"c++", "go", "rust"},
			wantKey:   "c++",
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key, found := coerceBranchKey(tt.output, tt.keys)
			assert.Equal(t, tt.wantFound, found, "found mismatch")
			if found {
				assert.Equal(t, tt.wantKey, key, "key mismatch")
			}
		})
	}
}

func TestExtractJSONFromText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "plain JSON object",
			text: `{"key": "value"}`,
			want: `{"key": "value"}`,
		},
		{
			name: "plain JSON array",
			text: `[1, 2, 3]`,
			want: `[1, 2, 3]`,
		},
		{
			name: "JSON in markdown code fence",
			text: "Here is the result:\n```json\n{\"key\": \"value\"}\n```\nDone.",
			want: `{"key": "value"}`,
		},
		{
			name: "JSON embedded in prose",
			text: "Here is the analysis:\n{\"result\": \"success\", \"count\": 42}\nThat's the output.",
			want: `{"result": "success", "count": 42}`,
		},
		{
			name: "nested JSON",
			text: `Some text {"outer": {"inner": "value"}} more text`,
			want: `{"outer": {"inner": "value"}}`,
		},
		{
			name: "no JSON at all",
			text: "This is just plain text with no JSON",
			want: "",
		},
		{
			name: "invalid JSON",
			text: `{"key": "unclosed`,
			want: "",
		},
		{
			name: "empty string",
			text: "",
			want: "",
		},
		{
			name: "JSON array in prose",
			text: "The records are: [1, 2, 3] and that's it.",
			want: "[1, 2, 3]",
		},
		{
			name: "code fence without json label",
			text: "```\n{\"key\": \"value\"}\n```",
			want: `{"key": "value"}`,
		},
		{
			name: "first balanced braces invalid, second valid",
			text: `Some text {invalid} but here is real JSON {"key":"val"}`,
			want: `{"key":"val"}`,
		},
		{
			name: "escaped quotes in JSON string",
			text: `{"key": "value with \"quotes\""}`,
			want: `{"key": "value with \"quotes\""}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractJSONFromText(tt.text)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStripMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bold", "**test**", "test"},
		{"backtick", "`test`", "test"},
		{"code fence", "```test```", "test"},
		{"plain", "test", "test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, stripMarkdown(tt.input))
		})
	}
}

func TestStripCommonPrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"the answer is:", "The answer is: bug", "bug"},
		{"i classify this as", "I classify this as feature", "feature"},
		{"based on my analysis,", "Based on my analysis, refactor", "refactor"},
		{"no prefix", "bug", "bug"},
		{"result:", "Result: something", "something"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, stripCommonPrefixes(tt.input))
		})
	}
}
