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

//go:build fts5

package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
)

// setupUpgradeTestConfig sets the global config variable to a minimal
// configuration backed by a temporary SQLite database and resets the
// package-level flag variables so tests do not leak state.
func setupUpgradeTestConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	config = &Config{
		DataDir: dir,
		Storage: StorageBackendConfig{
			Backend: "sqlite",
			SQLite: SQLiteConfig{
				Path: filepath.Join(dir, "test.db"),
			},
			Migration: MigrationStorageConfig{
				AutoMigrate: true,
			},
		},
	}

	// Save and restore flag state so tests are isolated.
	origDryRun := upgradeDryRun
	origBackupOnly := upgradeBackupOnly
	origNoBackup := upgradeNoBackup
	origYes := upgradeYes

	t.Cleanup(func() {
		upgradeDryRun = origDryRun
		upgradeBackupOnly = origBackupOnly
		upgradeNoBackup = origNoBackup
		upgradeYes = origYes
	})

	// Reset to defaults for this test.
	upgradeDryRun = false
	upgradeBackupOnly = false
	upgradeNoBackup = false
	upgradeYes = false
}

func TestUpgradeCmd_DryRun(t *testing.T) {
	setupUpgradeTestConfig(t)

	upgradeDryRun = true
	upgradeYes = true

	buf := new(bytes.Buffer)
	upgradeCmd.SetOut(buf)
	upgradeCmd.SetErr(buf)

	// Call RunE directly to bypass cobra argument parsing issues.
	err := runUpgrade(upgradeCmd, nil)
	require.NoError(t, err)

	output := buf.String()
	// The output should contain either information about pending migrations
	// or an indication that the database is already up to date.
	hasMigrationInfo := strings.Contains(output, "pending") ||
		strings.Contains(output, "up to date") ||
		strings.Contains(output, "dry-run") ||
		strings.Contains(output, "No changes applied")

	assert.True(t, hasMigrationInfo,
		"dry-run output should indicate pending migrations or up-to-date status; got: %s", output)
}

func TestUpgradeCmd_BackupOnly(t *testing.T) {
	setupUpgradeTestConfig(t)

	upgradeBackupOnly = true

	// Create an actual database file on disk so backup has something to work with.
	dbPath := config.Storage.SQLite.Path
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE test_sentinel (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	err = db.Close()
	require.NoError(t, err)

	buf := new(bytes.Buffer)
	upgradeCmd.SetOut(buf)
	upgradeCmd.SetErr(buf)

	err = runUpgrade(upgradeCmd, nil)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Backup created",
		"backup-only output should confirm backup creation")

	// The output should contain the timestamped backup file path.
	assert.Contains(t, output, ".backup.",
		"output should contain the timestamped backup file path")

	// Extract the backup path from the output and verify its integrity.
	backupPath := extractBackupPathFromOutput(output)
	if backupPath != "" {
		err = sqlite.VerifyBackup(backupPath)
		assert.NoError(t, err, "backup file should pass integrity verification")
	}
}

func TestUpgradeCmd_Flags(t *testing.T) {
	// Verify the upgrade command has the expected flags registered.
	flags := []struct {
		name     string
		flagType string
	}{
		{name: "dry-run", flagType: "bool"},
		{name: "backup-only", flagType: "bool"},
		{name: "no-backup", flagType: "bool"},
		{name: "yes", flagType: "bool"},
	}

	for _, f := range flags {
		t.Run(f.name, func(t *testing.T) {
			flag := upgradeCmd.Flags().Lookup(f.name)
			require.NotNil(t, flag,
				"upgrade command should have a --%s flag", f.name)
			assert.Equal(t, f.flagType, flag.Value.Type(),
				"--%s flag should be of type %s", f.name, f.flagType)
		})
	}
}

// extractBackupPathFromOutput finds the backup file path in command output.
// It expects a line of the form: "Backup created: /path/to/file.db.backup.TIMESTAMP"
func extractBackupPathFromOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, ".backup.") {
			idx := strings.Index(line, ": ")
			if idx >= 0 {
				return strings.TrimSpace(line[idx+2:])
			}
		}
	}
	return ""
}
