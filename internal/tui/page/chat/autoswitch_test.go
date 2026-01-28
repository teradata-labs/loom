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

package chat

import (
	"testing"
)

func TestParseAgentNameFromToolResult(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected string
	}{
		{
			name:     "simple agent name",
			json:     `{"action":"create","type":"agent","name":"sql-optimizer","path":"/path/to/agent.yaml"}`,
			expected: "sql-optimizer",
		},
		{
			name:     "agent name with .yaml extension",
			json:     `{"action":"create","type":"agent","name":"data-analyzer.yaml","path":"/path/to/agent.yaml"}`,
			expected: "data-analyzer",
		},
		{
			name:     "agent name with .yml extension",
			json:     `{"action":"create","type":"agent","name":"code-reviewer.yml","path":"/path/to/agent.yml"}`,
			expected: "code-reviewer",
		},
		{
			name:     "complex agent name with hyphens and numbers",
			json:     `{"action":"create","type":"agent","name":"teradata-sql-v2-optimizer","validation":"passed"}`,
			expected: "teradata-sql-v2-optimizer",
		},
		{
			name:     "agent name with spaces in path",
			json:     `{"action":"create","type":"agent","name":"my-agent","path":"/Users/test/my agents/agent.yaml"}`,
			expected: "my-agent",
		},
		{
			name:     "missing name field",
			json:     `{"action":"create","type":"agent","path":"/path/to/agent.yaml"}`,
			expected: "",
		},
		{
			name:     "empty name",
			json:     `{"action":"create","type":"agent","name":"","path":"/path/to/agent.yaml"}`,
			expected: "",
		},
		{
			name:     "malformed json",
			json:     `{"action":"create","type":"agent","name":`,
			expected: "",
		},
		{
			name:     "name with unicode characters",
			json:     `{"action":"create","type":"agent","name":"データ分析","path":"/path/to/agent.yaml"}`,
			expected: "データ分析",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseAgentNameFromToolResult(tt.json)
			if result != tt.expected {
				t.Errorf("parseAgentNameFromToolResult() = %q, want %q", result, tt.expected)
			}
		})
	}
}
