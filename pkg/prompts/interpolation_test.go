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
	"testing"
)

func TestInterpolate(t *testing.T) {
	tests := []struct {
		name     string
		template string
		vars     map[string]interface{}
		want     string
	}{
		{
			name:     "Simple string substitution",
			template: "Hello {{.name}}!",
			vars:     map[string]interface{}{"name": "World"},
			want:     "Hello World!",
		},
		{
			name:     "Multiple variables",
			template: "{{.greeting}} {{.name}}, you are a {{.role}}",
			vars: map[string]interface{}{
				"greeting": "Hello",
				"name":     "Alice",
				"role":     "developer",
			},
			want: "Hello Alice, you are a developer",
		},
		{
			name:     "Integer values",
			template: "Processing {{.count}} items",
			vars:     map[string]interface{}{"count": 42},
			want:     "Processing 42 items",
		},
		{
			name:     "Float values",
			template: "Cost: ${{.cost}}",
			vars:     map[string]interface{}{"cost": 10.50},
			want:     "Cost: $10.5",
		},
		{
			name:     "Boolean values",
			template: "Enabled: {{.enabled}}",
			vars:     map[string]interface{}{"enabled": true},
			want:     "Enabled: true",
		},
		{
			name:     "String slice",
			template: "Tools: {{.tools}}",
			vars:     map[string]interface{}{"tools": []string{"hammer", "wrench", "saw"}},
			want:     "Tools: hammer, wrench, saw",
		},
		{
			name:     "Missing variable keeps placeholder",
			template: "Hello {{.name}}, your role is {{.role}}",
			vars:     map[string]interface{}{"name": "Bob"},
			want:     "Hello Bob, your role is {{.role}}",
		},
		{
			name:     "No variables",
			template: "Static text with no placeholders",
			vars:     map[string]interface{}{"unused": "value"},
			want:     "Static text with no placeholders",
		},
		{
			name:     "Nil vars map",
			template: "Static text {{.var}}",
			vars:     nil,
			want:     "Static text {{.var}}",
		},
		{
			name:     "Escapes newlines",
			template: "Message: {{.text}}",
			vars:     map[string]interface{}{"text": "Line 1\nLine 2\r\nLine 3"},
			want:     "Message: Line 1 Line 2 Line 3",
		},
		{
			name:     "Escapes null bytes",
			template: "Data: {{.data}}",
			vars:     map[string]interface{}{"data": "hello\x00world"},
			want:     "Data: helloworld",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Interpolate(tt.template, tt.vars)
			if got != tt.want {
				t.Errorf("Interpolate() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestEscapeValue(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"float", 3.14, "3.14"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"string slice", []string{"a", "b", "c"}, "a, b, c"},
		{"with newlines", "line1\nline2", "line1 line2"},
		{"with tabs", "col1\tcol2", "col1 col2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeValue(tt.value)
			if got != tt.want {
				t.Errorf("escapeValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEscapeString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no special chars", "hello world", "hello world"},
		{"unix newline", "line1\nline2", "line1 line2"},
		{"windows newline", "line1\r\nline2", "line1 line2"},
		{"tab", "col1\tcol2", "col1 col2"},
		{"null byte", "hello\x00world", "helloworld"},
		{"multiple special chars", "a\nb\tc\x00d\r\ne", "a b cd e"}, // null byte removed, not replaced
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeString(tt.input)
			if got != tt.want {
				t.Errorf("escapeString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func BenchmarkInterpolate(b *testing.B) {
	template := "You are a {{.role}} agent for {{.backend}}. Session: {{.session_id}}. Cost threshold: {{.threshold}}"
	vars := map[string]interface{}{
		"role":       "SQL",
		"backend":    "Teradata",
		"session_id": "sess-12345",
		"threshold":  10.50,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Interpolate(template, vars)
	}
}
