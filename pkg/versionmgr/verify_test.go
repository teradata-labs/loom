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

// TestDetectDrift_AllInSync tests drift detection when all files match
func TestDetectDrift_AllInSync(t *testing.T) {
	tmpDir := t.TempDir()

	// Create VERSION file
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("1.2.3\n"), 0644))

	// Create matching version.go
	goVersionFile := filepath.Join(tmpDir, "internal/version/version.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(goVersionFile), 0755))
	require.NoError(t, os.WriteFile(goVersionFile, []byte(`package version
var Version = "1.2.3"
`), 0644))

	// Create matching README.md
	readmeFile := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmeFile, []byte(`# Test
**Version**: v1.2.3
`), 0644))

	// Run drift detection
	report, err := DetectDrift(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Note: Other files in GetAllTargets() will be missing, which counts as drift
	// So we verify that the files we created are in sync, but overall report may show drift
	assert.Greater(t, report.FilesInSync, 0, "Should have at least VERSION file in sync")

	// Verify canonical version
	assert.Equal(t, Version{Major: 1, Minor: 2, Patch: 3}, report.CanonicalVersion)

	// Verify created files are marked as in sync
	for _, file := range report.Files {
		if file.Exists && (file.Target.Path == versionFile || file.Target.Path == goVersionFile || file.Target.Path == readmeFile) {
			assert.True(t, file.InSync, "File %s should be in sync", file.Target.Path)
		}
	}
}

// TestDetectDrift_WithDrift tests drift detection when versions don't match
func TestDetectDrift_WithDrift(t *testing.T) {
	tmpDir := t.TempDir()

	// Create VERSION file with 2.0.0
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("2.0.0\n"), 0644))

	// Create version.go with 1.0.0 (out of sync)
	goVersionFile := filepath.Join(tmpDir, "internal/version/version.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(goVersionFile), 0755))
	require.NoError(t, os.WriteFile(goVersionFile, []byte(`package version
var Version = "1.0.0"
`), 0644))

	// Create README.md with 1.5.0 (also out of sync)
	readmeFile := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmeFile, []byte(`# Test
**Version**: v1.5.0
`), 0644))

	// Run drift detection
	report, err := DetectDrift(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify report shows drift
	assert.True(t, report.HasDrift, "Should detect drift when versions don't match")
	assert.Greater(t, report.FilesWithDrift, 0, "Should have files with drift")
	assert.Equal(t, 1, report.ExitCode(), "Exit code should be 1 when drift detected")

	// Verify canonical version is 2.0.0
	assert.Equal(t, Version{Major: 2, Minor: 0, Patch: 0}, report.CanonicalVersion)

	// Find drifted files in report
	foundGoVersion := false
	foundReadme := false
	for _, file := range report.Files {
		if strings.Contains(file.Target.Path, "version.go") && file.Exists {
			foundGoVersion = true
			if len(file.ParsedVersions) > 0 {
				assert.Equal(t, Version{Major: 1, Minor: 0, Patch: 0}, file.ParsedVersions[0])
			}
			assert.False(t, file.InSync, "version.go should be out of sync")
		}
		if strings.Contains(file.Target.Path, "README.md") && file.Exists {
			foundReadme = true
			if len(file.ParsedVersions) > 0 {
				assert.Equal(t, Version{Major: 1, Minor: 5, Patch: 0}, file.ParsedVersions[0])
			}
			assert.False(t, file.InSync, "README.md should be out of sync")
		}
	}
	assert.True(t, foundGoVersion, "Should find drifted version.go")
	assert.True(t, foundReadme, "Should find drifted README.md")
}

// TestDetectDrift_MissingFiles tests drift detection with missing files
func TestDetectDrift_MissingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create only VERSION file (all other files missing)
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("1.0.0\n"), 0644))

	// Run drift detection
	report, err := DetectDrift(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify missing files are reported
	assert.Greater(t, report.MissingFiles, 0, "Should detect missing files")
	assert.Equal(t, 1, report.ExitCode(), "Exit code should be 1 when files missing")

	// Verify at least one file is marked as missing
	foundMissing := false
	for _, file := range report.Files {
		if !file.Exists {
			foundMissing = true
			assert.False(t, file.InSync, "Missing files should not be marked as in sync")
		}
	}
	assert.True(t, foundMissing, "Should have at least one missing file")
}

// TestDetectDrift_MissingCanonicalVersion tests error when VERSION file missing
func TestDetectDrift_MissingCanonicalVersion(t *testing.T) {
	tmpDir := t.TempDir()

	// Don't create VERSION file
	_, err := DetectDrift(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read canonical version")
}

// TestDetectDrift_MalformedFile tests drift detection with unparseable version
func TestDetectDrift_MalformedFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create VERSION file
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("1.0.0\n"), 0644))

	// Create version.go with malformed version
	goVersionFile := filepath.Join(tmpDir, "internal/version/version.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(goVersionFile), 0755))
	require.NoError(t, os.WriteFile(goVersionFile, []byte(`package version
var Version = "invalid-version"
`), 0644))

	// Run drift detection
	report, err := DetectDrift(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify error is reported for malformed file
	foundError := false
	for _, file := range report.Files {
		if strings.Contains(file.Target.Path, "version.go") {
			foundError = file.Error != nil
			assert.False(t, file.InSync, "File with parse error should not be in sync")
		}
	}
	assert.True(t, foundError, "Should have error for malformed file")
}

// TestFileVersionInfo tests the FileVersionInfo struct
func TestFileVersionInfo(t *testing.T) {
	target := FileTarget{
		Path:        "/tmp/test.txt",
		Description: "Test file",
	}

	// Test file that exists and is in sync
	info := FileVersionInfo{
		Target:         target,
		Exists:         true,
		Versions:       []string{"1.2.3"},
		ParsedVersions: []Version{{Major: 1, Minor: 2, Patch: 3}},
		InSync:         true,
		Error:          nil,
	}
	assert.True(t, info.Exists)
	assert.True(t, info.InSync)
	assert.NoError(t, info.Error)
	assert.Len(t, info.Versions, 1)
	assert.Len(t, info.ParsedVersions, 1)

	// Test file that exists but has drift
	info2 := FileVersionInfo{
		Target:         target,
		Exists:         true,
		Versions:       []string{"1.0.0"},
		ParsedVersions: []Version{{Major: 1, Minor: 0, Patch: 0}},
		InSync:         false,
		Error:          nil,
	}
	assert.True(t, info2.Exists)
	assert.False(t, info2.InSync)

	// Test missing file
	info3 := FileVersionInfo{
		Target: target,
		Exists: false,
		InSync: false,
	}
	assert.False(t, info3.Exists)
	assert.False(t, info3.InSync)
}

// TestDriftReport_ExitCode tests exit code calculation
func TestDriftReport_ExitCode(t *testing.T) {
	tests := []struct {
		name             string
		hasDrift         bool
		missingFiles     int
		expectedExitCode int
	}{
		{
			name:             "all in sync",
			hasDrift:         false,
			missingFiles:     0,
			expectedExitCode: 0,
		},
		{
			name:             "has drift",
			hasDrift:         true,
			missingFiles:     0,
			expectedExitCode: 1,
		},
		{
			name:             "missing files",
			hasDrift:         false,
			missingFiles:     2,
			expectedExitCode: 1,
		},
		{
			name:             "drift and missing",
			hasDrift:         true,
			missingFiles:     1,
			expectedExitCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &DriftReport{
				HasDrift:     tt.hasDrift,
				MissingFiles: tt.missingFiles,
			}
			assert.Equal(t, tt.expectedExitCode, report.ExitCode())
		})
	}
}

// TestDriftReport_PrintSummary tests summary printing (no assertion, just coverage)
func TestDriftReport_PrintSummary(t *testing.T) {
	// Test with no drift
	report := &DriftReport{
		CanonicalVersion: Version{Major: 1, Minor: 2, Patch: 3},
		HasDrift:         false,
		MissingFiles:     0,
		FilesInSync:      10,
	}
	report.PrintSummary() // Just ensure it doesn't panic

	// Test with drift
	report2 := &DriftReport{
		CanonicalVersion: Version{Major: 1, Minor: 2, Patch: 3},
		HasDrift:         true,
		FilesWithDrift:   3,
		MissingFiles:     0,
	}
	report2.PrintSummary() // Just ensure it doesn't panic
}

// TestDriftReport_PrintReport tests full report printing (coverage only)
func TestDriftReport_PrintReport(t *testing.T) {
	tmpDir := t.TempDir()

	target := FileTarget{
		Path:        filepath.Join(tmpDir, "test.txt"),
		Description: "Test file",
	}

	// Test report with various scenarios
	report := &DriftReport{
		CanonicalVersion: Version{Major: 2, Minor: 0, Patch: 0},
		TotalFiles:       3,
		FilesInSync:      1,
		FilesWithDrift:   1,
		MissingFiles:     1,
		HasDrift:         true,
		Files: []FileVersionInfo{
			{
				Target:         target,
				Exists:         true,
				Versions:       []string{"2.0.0"},
				ParsedVersions: []Version{{Major: 2, Minor: 0, Patch: 0}},
				InSync:         true,
			},
			{
				Target:         target,
				Exists:         true,
				Versions:       []string{"1.0.0"},
				ParsedVersions: []Version{{Major: 1, Minor: 0, Patch: 0}},
				InSync:         false,
			},
			{
				Target: target,
				Exists: false,
				InSync: false,
			},
		},
	}

	// Print with verbose=false
	report.PrintReport(false)

	// Print with verbose=true
	report.PrintReport(true)

	// Just verify it doesn't panic
}

// TestDriftReport_MultipleVersions tests file with multiple version references
func TestDriftReport_MultipleVersions(t *testing.T) {
	tmpDir := t.TempDir()

	// Create VERSION file
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("2.0.0\n"), 0644))

	// Create Homebrew formula with multiple version references
	homebrewFile := filepath.Join(tmpDir, "packaging/macos/homebrew/loom.rb")
	require.NoError(t, os.MkdirAll(filepath.Dir(homebrewFile), 0755))
	require.NoError(t, os.WriteFile(homebrewFile, []byte(`class Loom < Formula
  version "1.5.0"
  url "https://github.com/test/loom/releases/download/v1.5.0/loom.tar.gz"
end
`), 0644))

	// Run drift detection
	report, err := DetectDrift(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify drift is detected
	assert.True(t, report.HasDrift)

	// Find the Homebrew formula in report
	foundFormula := false
	for _, file := range report.Files {
		if strings.Contains(file.Target.Path, "loom.rb") {
			foundFormula = true
			// Homebrew formula extracts version from both "version" and URLs
			// Should have version 1.5.0
			assert.False(t, file.InSync, "Homebrew formula should be out of sync")
			assert.Greater(t, len(file.ParsedVersions), 0)
		}
	}
	assert.True(t, foundFormula, "Should find Homebrew formula in report")
}

// TestDriftReport_Initialization tests DriftReport structure
func TestDriftReport_Initialization(t *testing.T) {
	report := &DriftReport{
		CanonicalVersion: Version{Major: 1, Minor: 0, Patch: 0},
		Files:            make([]FileVersionInfo, 0),
		HasDrift:         false,
		TotalFiles:       13,
		FilesInSync:      0,
		FilesWithDrift:   0,
		MissingFiles:     0,
	}

	assert.Equal(t, Version{Major: 1, Minor: 0, Patch: 0}, report.CanonicalVersion)
	assert.False(t, report.HasDrift)
	assert.Equal(t, 13, report.TotalFiles)
	assert.Equal(t, 0, report.FilesInSync)
	assert.Equal(t, 0, report.FilesWithDrift)
	assert.Equal(t, 0, report.MissingFiles)
	assert.Empty(t, report.Files)
}
