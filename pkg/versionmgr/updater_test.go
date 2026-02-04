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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdaterAtomicWorkflow tests the full atomic update workflow
func TestUpdaterAtomicWorkflow(t *testing.T) {
	// Create a temporary repo root with VERSION file
	tmpDir := t.TempDir()

	// Create VERSION file
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("1.0.0\n"), 0644))

	// Create a few other files to update
	goVersionFile := filepath.Join(tmpDir, "version.go")
	require.NoError(t, os.WriteFile(goVersionFile, []byte(`package version
var Version = "1.0.0"
`), 0644))

	readmeFile := filepath.Join(tmpDir, "README.md")
	require.NoError(t, os.WriteFile(readmeFile, []byte(`# Test
**Version**: v1.0.0
`), 0644))

	// Create updater
	updater := NewUpdater(tmpDir, false, false)
	require.NotNil(t, updater)

	// Update to new version
	newVersion := Version{Major: 2, Minor: 0, Patch: 0}
	results, err := updater.UpdateAllFiles(newVersion)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Verify VERSION file updated
	content, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "2.0.0")

	// Verify backups were cleaned up
	assert.NoFileExists(t, versionFile+".bak")
	assert.NoFileExists(t, goVersionFile+".bak")
	assert.NoFileExists(t, readmeFile+".bak")

	// Verify all results succeeded
	for _, result := range results {
		if result.Target.Path == versionFile || result.Target.Path == goVersionFile || result.Target.Path == readmeFile {
			assert.True(t, result.Success, "Expected %s to succeed", result.Target.Path)
			assert.NoError(t, result.Error)
		}
	}
}

// TestUpdaterDryRun tests that dry-run mode doesn't modify files
func TestUpdaterDryRun(t *testing.T) {
	tmpDir := t.TempDir()

	// Create VERSION file
	versionFile := filepath.Join(tmpDir, "VERSION")
	originalContent := []byte("1.0.0\n")
	require.NoError(t, os.WriteFile(versionFile, originalContent, 0644))

	// Create updater in dry-run mode
	updater := NewUpdater(tmpDir, true, false)

	// Attempt update
	newVersion := Version{Major: 2, Minor: 0, Patch: 0}
	results, err := updater.UpdateAllFiles(newVersion)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Verify file NOT modified
	content, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Equal(t, string(originalContent), string(content), "File should not be modified in dry-run mode")

	// Verify no backups created
	assert.NoFileExists(t, versionFile+".bak")

	// Verify all results marked as success (dry-run doesn't fail)
	for _, result := range results {
		if result.Target.Path == versionFile {
			assert.True(t, result.Success)
			assert.NoError(t, result.Error)
		}
	}
}

// TestUpdaterRollback tests that changes are rolled back on failure
func TestUpdaterRollback(t *testing.T) {
	tmpDir := t.TempDir()

	// Create VERSION file
	versionFile := filepath.Join(tmpDir, "VERSION")
	originalContent := []byte("1.0.0\n")
	require.NoError(t, os.WriteFile(versionFile, originalContent, 0644))

	// Create a broken file that will fail to update (no write permission)
	brokenFile := filepath.Join(tmpDir, "broken.txt")
	require.NoError(t, os.WriteFile(brokenFile, []byte("test"), 0444)) // Read-only

	// Override GetAllTargets to include the broken file
	// This is a bit tricky - we'll test with actual files instead

	// Actually, let's test rollback by creating a malformed file
	// Create a file with content that can't be parsed
	goVersionFile := filepath.Join(tmpDir, "internal/version/version.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(goVersionFile), 0755))
	goOriginal := []byte(`package version
var Version = "1.0.0"
`)
	require.NoError(t, os.WriteFile(goVersionFile, goOriginal, 0644))

	// Backup the original VERSION content
	versionOriginal, err := os.ReadFile(versionFile)
	require.NoError(t, err)

	// Make one file read-only to cause update failure
	require.NoError(t, os.Chmod(goVersionFile, 0444))
	defer func() {
		_ = os.Chmod(goVersionFile, 0644) // Restore permissions for cleanup
	}()

	// Create updater
	updater := NewUpdater(tmpDir, false, false)

	// Attempt update (should fail due to read-only file)
	newVersion := Version{Major: 2, Minor: 0, Patch: 0}
	_, err = updater.UpdateAllFiles(newVersion)
	assert.Error(t, err, "Update should fail due to read-only file")

	// Verify VERSION file was rolled back to original content
	content, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Equal(t, string(versionOriginal), string(content), "VERSION file should be rolled back")

	// Verify no backup files remain
	assert.NoFileExists(t, versionFile+".bak")
}

// TestReadCanonicalVersion tests reading the VERSION file
func TestReadCanonicalVersion(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		expected    Version
		expectError bool
	}{
		{
			name:     "standard version",
			content:  "1.2.3\n",
			expected: Version{Major: 1, Minor: 2, Patch: 3},
		},
		{
			name:     "version without newline",
			content:  "2.5.7",
			expected: Version{Major: 2, Minor: 5, Patch: 7},
		},
		{
			name:     "version with whitespace",
			content:  "  1.0.0  \n",
			expected: Version{Major: 1, Minor: 0, Patch: 0},
		},
		{
			name:        "invalid version",
			content:     "invalid\n",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			versionFile := filepath.Join(tmpDir, "VERSION")
			require.NoError(t, os.WriteFile(versionFile, []byte(tt.content), 0644))

			result, err := ReadCanonicalVersion(tmpDir)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

// TestReadCanonicalVersion_MissingFile tests error handling for missing VERSION file
func TestReadCanonicalVersion_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ReadCanonicalVersion(tmpDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read VERSION file")
}

// TestWriteCanonicalVersion tests writing the VERSION file
func TestWriteCanonicalVersion(t *testing.T) {
	tmpDir := t.TempDir()

	version := Version{Major: 1, Minor: 2, Patch: 3}
	err := WriteCanonicalVersion(tmpDir, version)
	require.NoError(t, err)

	// Verify file created and content correct
	versionFile := filepath.Join(tmpDir, "VERSION")
	content, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Equal(t, "1.2.3\n", string(content))

	// Verify we can read it back
	readVersion, err := ReadCanonicalVersion(tmpDir)
	require.NoError(t, err)
	assert.True(t, version.Equal(readVersion))
}

// TestUpdaterVerifyUpdates tests the verification phase
func TestUpdaterVerifyUpdates(t *testing.T) {
	tmpDir := t.TempDir()

	// Create VERSION file
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("1.0.0\n"), 0644))

	// Create updater
	updater := NewUpdater(tmpDir, false, false)

	// Get targets
	targets := GetAllTargets(tmpDir)

	// Should succeed when file has correct version
	err := updater.verifyUpdates(targets, Version{Major: 1, Minor: 0, Patch: 0})
	assert.NoError(t, err)

	// Should fail when file has different version
	err = updater.verifyUpdates(targets, Version{Major: 2, Minor: 0, Patch: 0})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "verification failed")
}

// TestUpdaterBackupAndRestore tests backup creation and cleanup
func TestUpdaterBackupAndRestore(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	originalContent := []byte("original content")
	require.NoError(t, os.WriteFile(testFile, originalContent, 0644))

	// Create updater
	updater := NewUpdater(tmpDir, false, true) // Verbose mode

	// Create backup
	backupPath := testFile + ".bak"
	err := updater.createBackup(testFile, backupPath)
	require.NoError(t, err)

	// Verify backup exists and has same content
	backupContent, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	assert.Equal(t, originalContent, backupContent)

	// Modify original file
	require.NoError(t, os.WriteFile(testFile, []byte("modified"), 0644))

	// Test rollback
	backups := map[string]string{testFile: backupPath}
	updater.rollbackBackups(backups)

	// Verify original content restored
	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, originalContent, content)

	// Backup should NOT exist after rollback (it was renamed to original)
	assert.NoFileExists(t, backupPath)

	// Test cleanup with a fresh backup
	require.NoError(t, updater.createBackup(testFile, backupPath))
	assert.FileExists(t, backupPath)

	updater.cleanupBackups(backups)

	// Backup should be removed after cleanup
	assert.NoFileExists(t, backupPath)
}

// TestUpdaterMissingFiles tests that missing files are handled gracefully
func TestUpdaterMissingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create only VERSION file (other files in GetAllTargets() don't exist)
	versionFile := filepath.Join(tmpDir, "VERSION")
	require.NoError(t, os.WriteFile(versionFile, []byte("1.0.0\n"), 0644))

	// Create updater
	updater := NewUpdater(tmpDir, false, false)

	// Update should succeed (missing files are skipped)
	newVersion := Version{Major: 2, Minor: 0, Patch: 0}
	results, err := updater.UpdateAllFiles(newVersion)
	require.NoError(t, err)

	// Verify VERSION file was updated
	content, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "2.0.0")

	// Verify results include skipped files
	hasSkippedFiles := false
	for _, result := range results {
		if result.Success && result.Error == nil && result.Target.Path != versionFile {
			hasSkippedFiles = true
		}
	}
	assert.True(t, hasSkippedFiles, "Should have some skipped files in results")
}

// TestNewUpdater tests updater construction
func TestNewUpdater(t *testing.T) {
	updater := NewUpdater("/tmp/test", true, true)
	require.NotNil(t, updater)
	assert.Equal(t, "/tmp/test", updater.repoRoot)
	assert.True(t, updater.dryRun)
	assert.True(t, updater.verbose)

	updater2 := NewUpdater("/other/path", false, false)
	require.NotNil(t, updater2)
	assert.Equal(t, "/other/path", updater2.repoRoot)
	assert.False(t, updater2.dryRun)
	assert.False(t, updater2.verbose)
}

// TestUpdateResult tests the UpdateResult struct
func TestUpdateResult(t *testing.T) {
	target := FileTarget{
		Path:        "/tmp/test.txt",
		Description: "Test file",
		UpdateFunc:  nil,
		ExtractFunc: nil,
	}

	// Success result
	result := UpdateResult{
		Target:  target,
		Success: true,
		Error:   nil,
	}
	assert.True(t, result.Success)
	assert.NoError(t, result.Error)

	// Failure result
	testErr := fmt.Errorf("test error")
	result2 := UpdateResult{
		Target:  target,
		Success: false,
		Error:   testErr,
	}
	assert.False(t, result2.Success)
	assert.Equal(t, testErr, result2.Error)
}
