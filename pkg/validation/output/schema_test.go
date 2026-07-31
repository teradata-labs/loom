// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package output

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractJSON(t *testing.T) {
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
			got := ExtractJSON(tt.text)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateJSONSchema(t *testing.T) {
	t.Parallel()

	objectSchema := `{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"age":{"type":"integer"}}}`

	tests := []struct {
		name        string
		text        string
		schema      string
		wantJSON    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid pure JSON",
			text:     `{"name": "loom"}`,
			schema:   objectSchema,
			wantJSON: `{"name": "loom"}`,
		},
		{
			name:     "valid JSON in prose is extracted",
			text:     "Sure! Here you go: {\"name\": \"loom\"} — done.",
			schema:   objectSchema,
			wantJSON: `{"name": "loom"}`,
		},
		{
			name:        "missing required field",
			text:        `{"age": 3}`,
			schema:      objectSchema,
			wantErr:     true,
			errContains: "schema violations",
		},
		{
			name:        "wrong type",
			text:        `{"name": "loom", "age": "three"}`,
			schema:      objectSchema,
			wantErr:     true,
			errContains: "schema violations",
		},
		{
			name:        "no JSON in output",
			text:        "plain prose only",
			schema:      objectSchema,
			wantErr:     true,
			errContains: "no valid JSON found",
		},
		{
			name:        "syntactically invalid schema",
			text:        `{"name": "loom"}`,
			schema:      `{"type": nonsense`,
			wantErr:     true,
			errContains: "schema validation error",
		},
		{
			name:        "empty input",
			text:        "",
			schema:      objectSchema,
			wantErr:     true,
			errContains: "no valid JSON found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateJSONSchema(tt.text, tt.schema)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantJSON, got)
		})
	}
}
