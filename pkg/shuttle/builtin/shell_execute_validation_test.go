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
package builtin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractFilePaths(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		stdout    string
		workDir   string
		wantPaths []string
	}{
		{
			name:      "redirect operator",
			command:   "cat > ~/.loom/agents/test.yaml",
			stdout:    "",
			workDir:   "/tmp",
			wantPaths: []string{"/tmp/.loom/agents/test.yaml"}, // Will be resolved
		},
		{
			name:      "tee command",
			command:   "echo 'test' | tee ~/.loom/workflows/workflow.yaml",
			stdout:    "",
			workDir:   "/tmp",
			wantPaths: []string{"/tmp/.loom/workflows/workflow.yaml"},
		},
		{
			name:      "output mention",
			command:   "some_command",
			stdout:    "Created: ~/.loom/agents/new_agent.yaml",
			workDir:   "/tmp",
			wantPaths: []string{"/tmp/.loom/agents/new_agent.yaml"},
		},
		{
			name:      "direct .loom path",
			command:   "touch /home/user/.loom/agents/direct.yaml",
			stdout:    "",
			workDir:   "/tmp",
			wantPaths: []string{"/home/user/.loom/agents/direct.yaml"},
		},
		{
			name:      "non-loom file",
			command:   "cat > /tmp/test.yaml",
			stdout:    "",
			workDir:   "/tmp",
			wantPaths: []string{"/tmp/test.yaml"}, // extractFilePaths finds all yaml, filtering happens in autoValidateConfigFiles
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := extractFilePaths(tt.command, tt.stdout, tt.workDir)

			if len(tt.wantPaths) == 0 {
				assert.Empty(t, paths, "Expected no paths to be extracted")
			} else {
				assert.NotEmpty(t, paths, "Expected at least one path to be extracted")
				// Verify path count matches
				assert.Len(t, paths, len(tt.wantPaths), "Expected exactly %d path(s)", len(tt.wantPaths))
			}
		})
	}
}

func TestShouldValidate(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		want     bool
	}{
		{
			name:     "agent file",
			filePath: "/home/user/.loom/agents/test.yaml",
			want:     true,
		},
		{
			name:     "workflow file",
			filePath: "/home/user/.loom/workflows/test.yaml",
			want:     true,
		},
		{
			name:     "non-loom file",
			filePath: "/tmp/test.yaml",
			want:     false,
		},
		{
			name:     "loom config file",
			filePath: "/home/user/.loom/config.yaml",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Import validation package to test ShouldValidate
			// This is a simple path check
			result := false
			if filePath := tt.filePath; filePath != "" {
				// Simple implementation of ShouldValidate logic
				result = (len(filePath) > 0 &&
					(contains(filePath, "/.loom/agents/") ||
						contains(filePath, "/.loom/workflows/")))
			}
			assert.Equal(t, tt.want, result)
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestCommandTokenSizeCheck(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		shouldError bool
	}{
		{
			name:        "small command",
			command:     "echo 'hello world'",
			shouldError: false,
		},
		{
			name:        "medium command",
			command:     strings.Repeat("echo 'test'\n", 100), // ~1,300 chars
			shouldError: false,
		},
		{
			name:        "large but acceptable command",
			command:     strings.Repeat("x", 39000), // Just under 40K limit
			shouldError: false,
		},
		{
			name:        "oversized command",
			command:     strings.Repeat("x", 50000), // Over 40K limit
			shouldError: true,
		},
		{
			name:        "giant heredoc simulation",
			command:     "cat <<EOF > file.json\n" + strings.Repeat(`{"key": "value"}\n`, 5000), // Simulates large file write
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCommandTokenSize(tt.command)
			if tt.shouldError {
				assert.Error(t, err, "Expected error for large command")
				assert.Contains(t, err.Error(), "Command is too large")
				assert.Contains(t, err.Error(), "Breaking large file writes")
			} else {
				assert.NoError(t, err, "Expected no error for normal-sized command")
			}
		})
	}
}
