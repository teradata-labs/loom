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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateChocolateyInstall_DollarEscaping tests the most fragile regex in the codebase.
// This test MUST pass - the $$ → $ escaping is critical for PowerShell variable syntax.
func TestUpdateChocolateyInstall_DollarEscaping(t *testing.T) {
	// Create temp file with PowerShell content
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "chocolateyinstall.ps1")

	original := `$ErrorActionPreference = 'Stop'
$version = '1.0.0'
$url64 = "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.zip"
`

	require.NoError(t, os.WriteFile(testFile, []byte(original), 0644))

	// Update to new version
	newVersion := Version{Major: 2, Minor: 5, Patch: 7}
	err := updateChocolateyInstall(testFile, newVersion)
	require.NoError(t, err)

	// Read result
	content, err := os.ReadFile(testFile)
	require.NoError(t, err)

	result := string(content)

	// CRITICAL: Verify $version (single $, not $$)
	assert.Contains(t, result, "$version = '2.5.7'", "Version should be updated with single $ (PowerShell variable syntax)")
	assert.NotContains(t, result, "$$version = '2.5.7'", "Must not have double $$ (that would be wrong PowerShell syntax)")

	// Verify other content unchanged
	assert.Contains(t, result, "$ErrorActionPreference = 'Stop'")
	assert.Contains(t, result, "$url64 =")
}

// TestVersionFile tests the canonical VERSION file update/extract
func TestVersionFile(t *testing.T) {
	tests := []struct {
		name           string
		inputContent   string
		updateVersion  Version
		expectedOutput string
		expectExtract  []string
		extractError   bool
	}{
		{
			name:           "simple version",
			inputContent:   "1.2.3\n",
			updateVersion:  Version{Major: 2, Minor: 0, Patch: 0},
			expectedOutput: "2.0.0\n",
			expectExtract:  []string{"1.2.3"},
		},
		{
			name:           "version with trailing whitespace",
			inputContent:   "1.2.3  \n",
			updateVersion:  Version{Major: 3, Minor: 1, Patch: 4},
			expectedOutput: "3.1.4\n",
			expectExtract:  []string{"1.2.3"},
		},
		{
			name:           "version without newline",
			inputContent:   "1.2.3",
			updateVersion:  Version{Major: 1, Minor: 0, Patch: 0},
			expectedOutput: "1.0.0\n",
			expectExtract:  []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "VERSION")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractVersionFile(testFile)
			if tt.extractError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectExtract, extracted)
			}

			// Test update
			err = updateVersionFile(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOutput, string(content))
		})
	}
}

// TestGoVersionFile tests internal/version/version.go update/extract
func TestGoVersionFile(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectedMatch string
		expectExtract []string
		extractError  bool
	}{
		{
			name: "standard version",
			inputContent: `package version

// Version is the current version of Loom
var Version = "1.2.3"
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectedMatch: `var Version = "2.0.0"`,
			expectExtract: []string{"1.2.3"},
		},
		{
			name: "version with comments",
			inputContent: `package version

// Version is the current version
var Version = "1.0.0" // Current release
`,
			updateVersion: Version{Major: 1, Minor: 5, Patch: 0},
			expectedMatch: `var Version = "1.5.0"`,
			expectExtract: []string{"1.0.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "version.go")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractGoVersionFile(testFile)
			if tt.extractError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectExtract, extracted)
			}

			// Test update
			err = updateGoVersionFile(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			assert.Contains(t, string(content), tt.expectedMatch)
		})
	}
}

// TestHomebrewFormula tests Homebrew .rb formula update/extract (dual regex patterns)
func TestHomebrewFormula(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectedURL   string
		expectExtract []string
	}{
		{
			name: "standard formula",
			inputContent: `class Loom < Formula
  desc "LLM agent framework"
  version "1.2.3"
  url "https://github.com/teradata-labs/loom/releases/download/v1.2.3/loom-darwin-arm64.tar.gz"
  sha256 "abc123"
end
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectedURL:   "https://github.com/teradata-labs/loom/releases/download/v2.0.0/loom-darwin-arm64.tar.gz",
			expectExtract: []string{"1.2.3"},
		},
		{
			name: "multiple URLs",
			inputContent: `class Loom < Formula
  version "1.0.0"
  url "https://example.com/v1.0.0/mac.tar.gz"
  on_intel do
    url "https://example.com/v1.0.0/intel.tar.gz"
  end
end
`,
			updateVersion: Version{Major: 3, Minor: 0, Patch: 0},
			expectedURL:   "https://example.com/v3.0.0/mac.tar.gz",
			expectExtract: []string{"1.0.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "loom.rb")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractHomebrewFormula(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateHomebrewFormula(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, `version "`+tt.updateVersion.String()+`"`)
			assert.Contains(t, result, tt.expectedURL)
		})
	}
}

// TestChocolateySpec tests Chocolatey .nuspec XML update/extract
func TestChocolateySpec(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard spec",
			inputContent: `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2015/06/nuspec.xsd">
  <metadata>
    <id>loom</id>
    <version>1.2.3</version>
    <releaseNotes>https://github.com/teradata-labs/loom/releases/tag/v1.2.3</releaseNotes>
  </metadata>
</package>
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "loom.nuspec")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractChocolateySpec(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateChocolateySpec(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, "<version>2.0.0</version>")
			assert.Contains(t, result, "https://github.com/teradata-labs/loom/releases/tag/v2.0.0")
		})
	}
}

// TestScoopManifest tests Scoop JSON manifest update/extract
func TestScoopManifest(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard manifest",
			inputContent: `{
    "version": "1.2.3",
    "architecture": {
        "64bit": {
            "url": "https://github.com/teradata-labs/loom/releases/download/v1.2.3/loom-windows-amd64.zip"
        }
    }
}
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "loom.json")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractScoopManifest(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateScoopManifest(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, `"version": "2.0.0"`)
			assert.Contains(t, result, "/v2.0.0/")
		})
	}
}

// TestWingetInstaller tests Winget installer YAML update/extract
func TestWingetInstaller(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard installer",
			inputContent: `PackageIdentifier: Teradata.Loom
PackageVersion: 1.2.3
Installers:
- Architecture: x64
  InstallerUrl: https://github.com/teradata-labs/loom/releases/download/v1.2.3/loom-windows-amd64.zip
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "Teradata.Loom.installer.yaml")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractWingetInstaller(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateWingetInstaller(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, "PackageVersion: 2.0.0")
			assert.Contains(t, result, "/v2.0.0/")
		})
	}
}

// TestWingetLocale tests Winget locale YAML update/extract
func TestWingetLocale(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard locale",
			inputContent: `PackageIdentifier: Teradata.Loom
PackageVersion: 1.2.3
PackageLocale: en-US
ReleaseNotes: See https://github.com/teradata-labs/loom/releases/tag/v1.2.3
ReleaseNotesUrl: https://github.com/teradata-labs/loom/releases/tag/v1.2.3
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "Teradata.Loom.locale.en-US.yaml")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractWingetLocale(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateWingetLocale(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, "PackageVersion: 2.0.0")
			assert.Contains(t, result, "ReleaseNotes: See https://github.com/teradata-labs/loom/releases/tag/v2.0.0")
			assert.Contains(t, result, "ReleaseNotesUrl: https://github.com/teradata-labs/loom/releases/tag/v2.0.0")
		})
	}
}

// TestWingetVersion tests Winget version manifest (Teradata.Loom.yaml) update/extract
func TestWingetVersion(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard version manifest",
			inputContent: `PackageIdentifier: Teradata.Loom
PackageVersion: 1.2.3
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.6.0
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
		{
			name: "version with extra whitespace",
			inputContent: `PackageIdentifier: Teradata.Loom
PackageVersion: 1.0.0
DefaultLocale: en-US
`,
			updateVersion: Version{Major: 3, Minor: 1, Patch: 4},
			expectExtract: []string{"1.0.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "Teradata.Loom.yaml")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractWingetVersion(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateWingetVersion(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, "PackageVersion: "+tt.updateVersion.String())
		})
	}
}

// TestReadmeVersion tests README.md version badge update/extract
func TestReadmeVersion(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard readme",
			inputContent: `# Loom

**Version**: v1.2.3

LLM agent framework.
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "README.md")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractReadmeVersion(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateReadmeVersion(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, "**Version**: v2.0.0")
		})
	}
}

// TestClaudeVersion tests CLAUDE.md version reference update/extract (line 6)
func TestClaudeVersion(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard CLAUDE.md",
			inputContent: `# Loom Project Context

## Overview
Loom is an LLM agent framework.

**Version**: v1.2.3
**Status**: Beta
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "CLAUDE.md")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractClaudeVersion(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateClaudeVersion(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			lines := strings.Split(string(content), "\n")

			// Line 6 (index 5) should have updated version
			require.Greater(t, len(lines), 5, "File should have at least 6 lines")
			assert.Contains(t, lines[5], "**Version**: v2.0.0")
		})
	}
}

// TestDocsReadme tests docs/README.md version reference update/extract
func TestDocsReadme(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		updateVersion Version
		expectExtract []string
	}{
		{
			name: "standard docs readme",
			inputContent: `# Loom Documentation

**Version**: v1.2.3

Documentation for Loom.
`,
			updateVersion: Version{Major: 2, Minor: 0, Patch: 0},
			expectExtract: []string{"1.2.3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "README.md")

			// Test extract
			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractDocsReadme(testFile)
			require.NoError(t, err)
			assert.Equal(t, tt.expectExtract, extracted)

			// Test update
			err = updateDocsReadme(testFile, tt.updateVersion)
			require.NoError(t, err)

			content, err := os.ReadFile(testFile)
			require.NoError(t, err)
			result := string(content)

			assert.Contains(t, result, "**Version**: v2.0.0")
		})
	}
}

// TestChocolateyInstall_Extract tests extraction from chocolateyinstall.ps1
func TestChocolateyInstall_Extract(t *testing.T) {
	tests := []struct {
		name          string
		inputContent  string
		expectExtract []string
		expectError   bool
	}{
		{
			name: "standard install script",
			inputContent: `$ErrorActionPreference = 'Stop'
$version = '1.2.3'
$url64 = "https://github.com/teradata-labs/loom/releases/download/v$version/loom-windows-amd64.zip"
`,
			expectExtract: []string{"1.2.3"},
		},
		{
			name: "version with double quotes",
			inputContent: `$ErrorActionPreference = 'Stop'
$version = "1.0.0"
`,
			expectExtract: nil,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			testFile := filepath.Join(tmpDir, "chocolateyinstall.ps1")

			require.NoError(t, os.WriteFile(testFile, []byte(tt.inputContent), 0644))
			extracted, err := extractChocolateyInstall(testFile)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectExtract, extracted)
			}
		})
	}
}

// TestMissingFile tests that extract functions handle missing files gracefully
func TestMissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	nonexistent := filepath.Join(tmpDir, "nonexistent.txt")

	extractFuncs := []struct {
		name string
		fn   func(string) ([]string, error)
	}{
		{"extractVersionFile", extractVersionFile},
		{"extractGoVersionFile", extractGoVersionFile},
		{"extractHomebrewFormula", extractHomebrewFormula},
		{"extractChocolateySpec", extractChocolateySpec},
		{"extractScoopManifest", extractScoopManifest},
		{"extractWingetInstaller", extractWingetInstaller},
		{"extractWingetLocale", extractWingetLocale},
		{"extractWingetVersion", extractWingetVersion},
		{"extractReadmeVersion", extractReadmeVersion},
		{"extractClaudeVersion", extractClaudeVersion},
		{"extractDocsReadme", extractDocsReadme},
		{"extractChocolateyInstall", extractChocolateyInstall},
	}

	for _, test := range extractFuncs {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.fn(nonexistent)
			assert.Error(t, err, "Should return error for missing file")
		})
	}
}
