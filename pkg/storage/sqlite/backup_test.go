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

package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
)

// createTestDBWithData creates a temporary SQLite database populated with a
// test_data table and a single row. It returns the path to the database file.
func createTestDBWithData(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE test_data (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO test_data (id, value) VALUES (1, 'hello')")
	require.NoError(t, err)
	err = db.Close()
	require.NoError(t, err)
	return dbPath
}

func TestBackup_CreatesValidFile(t *testing.T) {
	dbPath := createTestDBWithData(t)

	backupPath, err := Backup(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(backupPath) })

	// Verify the backup file exists on disk
	info, err := os.Stat(backupPath)
	require.NoError(t, err)
	assert.True(t, info.Size() > 0, "backup file should not be empty")

	// Verify the path contains the ".backup." marker
	assert.True(t, strings.Contains(backupPath, ".backup."),
		"backup path %q should contain '.backup.' timestamp segment", backupPath)

	// Verify the backup passes integrity verification
	err = VerifyBackup(backupPath)
	assert.NoError(t, err, "backup should pass integrity check")
}

func TestBackup_ContainsData(t *testing.T) {
	dbPath := createTestDBWithData(t)

	backupPath, err := Backup(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(backupPath) })

	// Open the backup and verify the original data is present
	backupDB, err := sql.Open("sqlite3", backupPath)
	require.NoError(t, err)
	defer func() { _ = backupDB.Close() }()

	var value string
	err = backupDB.QueryRow("SELECT value FROM test_data WHERE id = 1").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, "hello", value,
		"backup database should contain the same data as the source")
}

func TestBackup_NonexistentDB(t *testing.T) {
	// SQLite auto-creates database files in existing directories, so we use a
	// path under a nonexistent directory to force an error from VACUUM INTO.
	nonexistentPath := filepath.Join(t.TempDir(), "no_such_dir", "does_not_exist.db")

	_, err := Backup(nonexistentPath)
	require.Error(t, err, "backup of database in nonexistent directory should return an error")
}

func TestVerifyBackup_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	invalidPath := filepath.Join(dir, "invalid.db")

	// Write random garbage bytes to simulate a corrupt file
	err := os.WriteFile(invalidPath, []byte("this is not a sqlite database"), 0o644)
	require.NoError(t, err)

	err = VerifyBackup(invalidPath)
	require.Error(t, err, "VerifyBackup should fail on a non-SQLite file")
}
