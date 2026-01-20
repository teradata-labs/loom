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
	"io"
	"os"
	"path/filepath"
)

// UpdateResult contains the result of updating a file
type UpdateResult struct {
	Target  FileTarget
	Success bool
	Error   error
}

// Updater handles version updates across all files
type Updater struct {
	repoRoot string
	dryRun   bool
	verbose  bool
}

// NewUpdater creates a new Updater
func NewUpdater(repoRoot string, dryRun, verbose bool) *Updater {
	return &Updater{
		repoRoot: repoRoot,
		dryRun:   dryRun,
		verbose:  verbose,
	}
}

// UpdateAllFiles updates all registered files to the specified version
// Returns list of results and error if any update fails
func (u *Updater) UpdateAllFiles(version Version) ([]UpdateResult, error) {
	targets := GetAllTargets(u.repoRoot)
	results := make([]UpdateResult, 0, len(targets))
	backups := make(map[string]string) // path -> backup path

	// Phase 1: Create backups
	if !u.dryRun {
		for _, target := range targets {
			if _, err := os.Stat(target.Path); err != nil {
				if os.IsNotExist(err) {
					if u.verbose {
						fmt.Printf("⚠️  Skipping %s (file does not exist)\n", target.Path)
					}
					continue
				}
				return results, fmt.Errorf("failed to stat %s: %w", target.Path, err)
			}

			backupPath := target.Path + ".bak"
			if err := u.createBackup(target.Path, backupPath); err != nil {
				// Rollback previous backups
				u.rollbackBackups(backups)
				return results, fmt.Errorf("failed to create backup for %s: %w", target.Path, err)
			}
			backups[target.Path] = backupPath

			if u.verbose {
				fmt.Printf("✓ Backed up %s\n", filepath.Base(target.Path))
			}
		}
	}

	// Phase 2: Update files
	allSuccess := true
	for _, target := range targets {
		// Skip if file doesn't exist
		if _, err := os.Stat(target.Path); os.IsNotExist(err) {
			results = append(results, UpdateResult{
				Target:  target,
				Success: true, // Not an error, just skipped
				Error:   nil,
			})
			continue
		}

		if u.dryRun {
			if u.verbose {
				fmt.Printf("🔍 [DRY RUN] Would update %s to %s\n", target.Description, version.String())
			}
			results = append(results, UpdateResult{
				Target:  target,
				Success: true,
				Error:   nil,
			})
			continue
		}

		// Perform actual update
		err := target.UpdateFunc(target.Path, version)
		success := err == nil

		results = append(results, UpdateResult{
			Target:  target,
			Success: success,
			Error:   err,
		})

		if !success {
			allSuccess = false
			if u.verbose {
				fmt.Printf("❌ Failed to update %s: %v\n", target.Description, err)
			}
		} else if u.verbose {
			fmt.Printf("✓ Updated %s\n", target.Description)
		}
	}

	// Phase 3: Verify or rollback
	if !u.dryRun {
		if !allSuccess {
			fmt.Println("\n⚠️  Some updates failed. Rolling back changes...")
			u.rollbackBackups(backups)
			return results, fmt.Errorf("update failed, all changes rolled back")
		}

		// Verify updates
		if err := u.verifyUpdates(targets, version); err != nil {
			fmt.Println("\n⚠️  Verification failed. Rolling back changes...")
			u.rollbackBackups(backups)
			return results, fmt.Errorf("verification failed: %w", err)
		}

		// Success! Remove backups
		u.cleanupBackups(backups)
	}

	return results, nil
}

// createBackup creates a backup of the source file
func (u *Updater) createBackup(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// rollbackBackups restores all backup files
func (u *Updater) rollbackBackups(backups map[string]string) {
	for original, backup := range backups {
		if err := os.Rename(backup, original); err != nil {
			fmt.Printf("❌ Failed to rollback %s: %v\n", original, err)
		} else if u.verbose {
			fmt.Printf("↩️  Rolled back %s\n", filepath.Base(original))
		}
	}
}

// cleanupBackups removes all backup files
func (u *Updater) cleanupBackups(backups map[string]string) {
	for _, backup := range backups {
		if err := os.Remove(backup); err != nil && u.verbose {
			fmt.Printf("⚠️  Failed to remove backup %s: %v\n", backup, err)
		}
	}
}

// verifyUpdates verifies that all files were updated correctly
func (u *Updater) verifyUpdates(targets []FileTarget, expectedVersion Version) error {
	for _, target := range targets {
		// Skip if file doesn't exist
		if _, err := os.Stat(target.Path); os.IsNotExist(err) {
			continue
		}

		versions, err := target.ExtractFunc(target.Path)
		if err != nil {
			return fmt.Errorf("failed to verify %s: %w", target.Path, err)
		}

		// Check that all extracted versions match expected
		for _, v := range versions {
			parsedVersion, err := ParseVersion(v)
			if err != nil {
				return fmt.Errorf("failed to parse version %q from %s: %w", v, target.Path, err)
			}

			if !parsedVersion.Equal(expectedVersion) {
				return fmt.Errorf("verification failed for %s: expected %s, got %s",
					target.Path, expectedVersion.String(), parsedVersion.String())
			}
		}
	}

	return nil
}

// ReadCanonicalVersion reads the version from the VERSION file
func ReadCanonicalVersion(repoRoot string) (Version, error) {
	versionFile := filepath.Join(repoRoot, "VERSION")
	content, err := os.ReadFile(versionFile)
	if err != nil {
		return Version{}, fmt.Errorf("failed to read VERSION file: %w", err)
	}

	return ParseVersion(string(content))
}

// WriteCanonicalVersion writes the version to the VERSION file
func WriteCanonicalVersion(repoRoot string, version Version) error {
	versionFile := filepath.Join(repoRoot, "VERSION")
	return os.WriteFile(versionFile, []byte(version.String()+"\n"), 0644)
}
