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

// Package sqlite provides utility functions for SQLite database operations
// such as online backup and integrity verification.
package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver" // registers "sqlite3" driver
)

// Backup creates a safe online backup of a SQLite database using VACUUM INTO.
// VACUUM INTO produces a clean, defragmented copy of the database while allowing
// concurrent reads on the source. The backup file is named with a timestamp
// suffix (e.g., "loom.db.backup.20260224T153000"). On failure, any partially
// written backup file is removed before returning.
func Backup(dbPath string) (backupPath string, err error) {
	backupPath = dbPath + ".backup." + time.Now().Format("20060102T150405")

	srcDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return "", fmt.Errorf("backup: open source database %q: %w", dbPath, err)
	}
	defer func() { _ = srcDB.Close() }()

	if _, err := srcDB.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return "", fmt.Errorf("backup: set busy_timeout on %q: %w", dbPath, err)
	}

	if _, err := srcDB.Exec("VACUUM INTO ?", backupPath); err != nil {
		_ = os.Remove(backupPath) // best-effort cleanup
		return "", fmt.Errorf("backup: vacuum into %q from %q: %w", backupPath, dbPath, err)
	}

	if err := srcDB.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("backup: close source database %q: %w", dbPath, err)
	}

	if err := VerifyBackup(backupPath); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("backup: verification failed for %q: %w", backupPath, err)
	}

	return backupPath, nil
}

// VerifyBackup opens a SQLite database file and runs PRAGMA integrity_check to
// confirm the file is a valid, uncorrupted SQLite database.
func VerifyBackup(backupPath string) error {
	db, err := sql.Open("sqlite3", backupPath)
	if err != nil {
		return fmt.Errorf("verify backup: open %q: %w", backupPath, err)
	}
	defer func() { _ = db.Close() }()

	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("verify backup: integrity check on %q: %w", backupPath, err)
	}

	if result != "ok" {
		return fmt.Errorf("verify backup: integrity check failed on %q: %s", backupPath, result)
	}

	return nil
}
