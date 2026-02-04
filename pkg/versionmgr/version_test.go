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

package versionmgr

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseVersion tests version parsing with various formats
func TestParseVersion(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    Version
		expectError bool
	}{
		{
			name:     "standard version",
			input:    "1.2.3",
			expected: Version{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:     "version with v prefix",
			input:    "v1.2.3",
			expected: Version{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:     "major.minor only",
			input:    "2.5",
			expected: Version{Major: 2, Minor: 5, Patch: 0},
		},
		{
			name:     "major.minor with v prefix",
			input:    "v2.5",
			expected: Version{Major: 2, Minor: 5, Patch: 0},
		},
		{
			name:     "version with whitespace",
			input:    "  1.2.3  ",
			expected: Version{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:     "zero version",
			input:    "0.0.0",
			expected: Version{Major: 0, Minor: 0, Patch: 0},
		},
		{
			name:     "large version numbers",
			input:    "100.200.300",
			expected: Version{Major: 100, Minor: 200, Patch: 300},
		},
		{
			name:        "empty string",
			input:       "",
			expectError: true,
		},
		{
			name:        "only whitespace",
			input:       "   ",
			expectError: true,
		},
		{
			name:        "only v prefix",
			input:       "v",
			expectError: true,
		},
		{
			name:        "single number",
			input:       "1",
			expectError: true,
		},
		{
			name:        "four parts",
			input:       "1.2.3.4",
			expectError: true,
		},
		{
			name:        "non-numeric major",
			input:       "x.2.3",
			expectError: true,
		},
		{
			name:        "non-numeric minor",
			input:       "1.x.3",
			expectError: true,
		},
		{
			name:        "non-numeric patch",
			input:       "1.2.x",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseVersion(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestVersionString tests String() and WithV() methods
func TestVersionString(t *testing.T) {
	tests := []struct {
		name           string
		version        Version
		expectedString string
		expectedWithV  string
	}{
		{
			name:           "standard version",
			version:        Version{Major: 1, Minor: 2, Patch: 3},
			expectedString: "1.2.3",
			expectedWithV:  "v1.2.3",
		},
		{
			name:           "zero version",
			version:        Version{Major: 0, Minor: 0, Patch: 0},
			expectedString: "0.0.0",
			expectedWithV:  "v0.0.0",
		},
		{
			name:           "no patch",
			version:        Version{Major: 2, Minor: 5, Patch: 0},
			expectedString: "2.5.0",
			expectedWithV:  "v2.5.0",
		},
		{
			name:           "large numbers",
			version:        Version{Major: 100, Minor: 200, Patch: 300},
			expectedString: "100.200.300",
			expectedWithV:  "v100.200.300",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedString, tt.version.String())
			assert.Equal(t, tt.expectedWithV, tt.version.WithV())
		})
	}
}

// TestBumpMajor tests major version bumping and reset behavior
func TestBumpMajor(t *testing.T) {
	tests := []struct {
		name     string
		version  Version
		expected Version
	}{
		{
			name:     "bump from 1.2.3",
			version:  Version{Major: 1, Minor: 2, Patch: 3},
			expected: Version{Major: 2, Minor: 0, Patch: 0},
		},
		{
			name:     "bump from 0.5.10",
			version:  Version{Major: 0, Minor: 5, Patch: 10},
			expected: Version{Major: 1, Minor: 0, Patch: 0},
		},
		{
			name:     "bump from 9.9.9",
			version:  Version{Major: 9, Minor: 9, Patch: 9},
			expected: Version{Major: 10, Minor: 0, Patch: 0},
		},
		{
			name:     "bump resets minor and patch",
			version:  Version{Major: 5, Minor: 99, Patch: 99},
			expected: Version{Major: 6, Minor: 0, Patch: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.version.BumpMajor()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestBumpMinor tests minor version bumping and patch reset
func TestBumpMinor(t *testing.T) {
	tests := []struct {
		name     string
		version  Version
		expected Version
	}{
		{
			name:     "bump from 1.2.3",
			version:  Version{Major: 1, Minor: 2, Patch: 3},
			expected: Version{Major: 1, Minor: 3, Patch: 0},
		},
		{
			name:     "bump from 0.0.0",
			version:  Version{Major: 0, Minor: 0, Patch: 0},
			expected: Version{Major: 0, Minor: 1, Patch: 0},
		},
		{
			name:     "bump from 5.9.99",
			version:  Version{Major: 5, Minor: 9, Patch: 99},
			expected: Version{Major: 5, Minor: 10, Patch: 0},
		},
		{
			name:     "bump preserves major",
			version:  Version{Major: 100, Minor: 0, Patch: 0},
			expected: Version{Major: 100, Minor: 1, Patch: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.version.BumpMinor()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestBumpPatch tests patch version bumping (no resets)
func TestBumpPatch(t *testing.T) {
	tests := []struct {
		name     string
		version  Version
		expected Version
	}{
		{
			name:     "bump from 1.2.3",
			version:  Version{Major: 1, Minor: 2, Patch: 3},
			expected: Version{Major: 1, Minor: 2, Patch: 4},
		},
		{
			name:     "bump from 0.0.0",
			version:  Version{Major: 0, Minor: 0, Patch: 0},
			expected: Version{Major: 0, Minor: 0, Patch: 1},
		},
		{
			name:     "bump from 5.9.99",
			version:  Version{Major: 5, Minor: 9, Patch: 99},
			expected: Version{Major: 5, Minor: 9, Patch: 100},
		},
		{
			name:     "bump preserves major and minor",
			version:  Version{Major: 100, Minor: 200, Patch: 5},
			expected: Version{Major: 100, Minor: 200, Patch: 6},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.version.BumpPatch()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestVersionEqual tests version equality comparison
func TestVersionEqual(t *testing.T) {
	tests := []struct {
		name     string
		v1       Version
		v2       Version
		expected bool
	}{
		{
			name:     "equal versions",
			v1:       Version{Major: 1, Minor: 2, Patch: 3},
			v2:       Version{Major: 1, Minor: 2, Patch: 3},
			expected: true,
		},
		{
			name:     "different major",
			v1:       Version{Major: 1, Minor: 2, Patch: 3},
			v2:       Version{Major: 2, Minor: 2, Patch: 3},
			expected: false,
		},
		{
			name:     "different minor",
			v1:       Version{Major: 1, Minor: 2, Patch: 3},
			v2:       Version{Major: 1, Minor: 3, Patch: 3},
			expected: false,
		},
		{
			name:     "different patch",
			v1:       Version{Major: 1, Minor: 2, Patch: 3},
			v2:       Version{Major: 1, Minor: 2, Patch: 4},
			expected: false,
		},
		{
			name:     "zero versions",
			v1:       Version{Major: 0, Minor: 0, Patch: 0},
			v2:       Version{Major: 0, Minor: 0, Patch: 0},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.v1.Equal(tt.v2))
			// Test symmetry
			assert.Equal(t, tt.expected, tt.v2.Equal(tt.v1))
		})
	}
}

// TestVersionCompare tests version comparison
func TestVersionCompare(t *testing.T) {
	tests := []struct {
		name     string
		v1       Version
		v2       Version
		expected int
	}{
		{
			name:     "equal versions",
			v1:       Version{Major: 1, Minor: 2, Patch: 3},
			v2:       Version{Major: 1, Minor: 2, Patch: 3},
			expected: 0,
		},
		{
			name:     "v1 greater by major",
			v1:       Version{Major: 2, Minor: 0, Patch: 0},
			v2:       Version{Major: 1, Minor: 9, Patch: 9},
			expected: 1,
		},
		{
			name:     "v1 less by major",
			v1:       Version{Major: 1, Minor: 9, Patch: 9},
			v2:       Version{Major: 2, Minor: 0, Patch: 0},
			expected: -1,
		},
		{
			name:     "v1 greater by minor",
			v1:       Version{Major: 1, Minor: 3, Patch: 0},
			v2:       Version{Major: 1, Minor: 2, Patch: 9},
			expected: 1,
		},
		{
			name:     "v1 less by minor",
			v1:       Version{Major: 1, Minor: 2, Patch: 9},
			v2:       Version{Major: 1, Minor: 3, Patch: 0},
			expected: -1,
		},
		{
			name:     "v1 greater by patch",
			v1:       Version{Major: 1, Minor: 2, Patch: 4},
			v2:       Version{Major: 1, Minor: 2, Patch: 3},
			expected: 1,
		},
		{
			name:     "v1 less by patch",
			v1:       Version{Major: 1, Minor: 2, Patch: 3},
			v2:       Version{Major: 1, Minor: 2, Patch: 4},
			expected: -1,
		},
		{
			name:     "zero vs non-zero",
			v1:       Version{Major: 0, Minor: 0, Patch: 0},
			v2:       Version{Major: 0, Minor: 0, Patch: 1},
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.v1.Compare(tt.v2)
			assert.Equal(t, tt.expected, result)

			// Test inverse symmetry
			if tt.expected != 0 {
				inverseResult := tt.v2.Compare(tt.v1)
				assert.Equal(t, -tt.expected, inverseResult)
			}
		})
	}
}

// TestVersionBumpingSequence tests realistic version bump sequences
func TestVersionBumpingSequence(t *testing.T) {
	// Start with 1.0.0
	v := Version{Major: 1, Minor: 0, Patch: 0}
	assert.Equal(t, "1.0.0", v.String())

	// Bump patch: 1.0.0 → 1.0.1
	v = v.BumpPatch()
	assert.Equal(t, "1.0.1", v.String())

	// Bump patch: 1.0.1 → 1.0.2
	v = v.BumpPatch()
	assert.Equal(t, "1.0.2", v.String())

	// Bump minor: 1.0.2 → 1.1.0
	v = v.BumpMinor()
	assert.Equal(t, "1.1.0", v.String())

	// Bump patch: 1.1.0 → 1.1.1
	v = v.BumpPatch()
	assert.Equal(t, "1.1.1", v.String())

	// Bump major: 1.1.1 → 2.0.0
	v = v.BumpMajor()
	assert.Equal(t, "2.0.0", v.String())

	// Verify WithV works
	assert.Equal(t, "v2.0.0", v.WithV())
}

// TestParseVersionRoundTrip tests parsing and string conversion
func TestParseVersionRoundTrip(t *testing.T) {
	tests := []string{
		"1.2.3",
		"0.0.0",
		"100.200.300",
		"v1.2.3",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			parsed, err := ParseVersion(input)
			require.NoError(t, err)

			// String() should not have 'v' prefix
			output := parsed.String()
			assert.NotContains(t, output, "v")

			// WithV() should have 'v' prefix
			outputWithV := parsed.WithV()
			assert.True(t, strings.HasPrefix(outputWithV, "v"))

			// Re-parsing should yield same result
			reparsed, err := ParseVersion(output)
			require.NoError(t, err)
			assert.True(t, parsed.Equal(reparsed))
		})
	}
}
