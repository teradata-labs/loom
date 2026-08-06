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
package artifacts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
		wantErr   bool
	}{
		{"uuid", "550e8400-e29b-41d4-a716-446655440000", false},
		{"underscore prefix", "sess_rt", false},
		{"dots and dashes", "a.b-c_9", false},
		{"empty", "", true},
		{"dot", ".", true},
		{"dotdot", "..", true},
		{"traversal prefix", "../x", true},
		{"embedded dotdot", "a..b", true},
		{"forward slash", "a/b", true},
		{"backslash", `a\b`, true},
		{"absolute path", "/etc/passwd", true},
		{"space", "a b", true},
		{"null byte", "a\x00b", true},
		{"non-ascii", "über", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSessionID(tt.sessionID)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetArtifactDir_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)

	for _, id := range []string{"../evil", "..", "a/b", `a\b`} {
		_, err := GetArtifactDir(id, SourceAgent)
		require.Error(t, err, "session ID %q must be rejected", id)
	}

	dir, err := GetArtifactDir("550e8400-e29b-41d4-a716-446655440000", SourceAgent)
	require.NoError(t, err)
	sessionsRoot := filepath.Join(root, "artifacts", "sessions")
	require.True(t, strings.HasPrefix(dir, sessionsRoot+string(filepath.Separator)),
		"artifact dir %q must stay under %q", dir, sessionsRoot)
}

func TestGetScratchpadDir_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", root)

	for _, id := range []string{"../evil", "..", "a/b", `a\b`} {
		_, err := GetScratchpadDir(id)
		require.Error(t, err, "session ID %q must be rejected", id)
	}

	dir, err := GetScratchpadDir("sess_ok")
	require.NoError(t, err)
	sessionsRoot := filepath.Join(root, "artifacts", "sessions")
	require.True(t, strings.HasPrefix(dir, sessionsRoot+string(filepath.Separator)),
		"scratchpad dir %q must stay under %q", dir, sessionsRoot)
}
