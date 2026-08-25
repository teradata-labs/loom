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
package agent

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/teradata-labs/loom/internal/sqlitedriver"
)

// DBConfig holds database configuration including optional encryption.
type DBConfig struct {
	// Path to the SQLite database file
	Path string

	// EncryptDatabase enables SQLCipher encryption at rest.
	// When true, requires EncryptionKey to be set.
	// Default: false (opt-in for enterprise deployments)
	EncryptDatabase bool

	// EncryptionKey is the encryption key for SQLCipher.
	// Can be provided directly or via LOOM_DB_KEY environment variable.
	// Required when EncryptDatabase is true.
	EncryptionKey string
}

// OpenDB opens a SQLite database with optional encryption support.
// Returns a *sql.DB connection or an error.
//
// Uses SQLCipher driver for all connections (handles both encrypted and unencrypted).
// When encryption is disabled (default), no key is set.
// When encryption is enabled, uses SQLCipher with the provided key.
//
// Example without encryption (default):
//
//	db, err := OpenDB(DBConfig{Path: "sessions.db"})
//
// Example with encryption:
//
//	db, err := OpenDB(DBConfig{
//	    Path: "sessions.db",
//	    EncryptDatabase: true,
//	    EncryptionKey: os.Getenv("LOOM_DB_KEY"),
//	})
func OpenDB(config DBConfig) (*sql.DB, error) {
	// busy_timeout and foreign_keys are per-connection settings, so they must
	// ride in the DSN to reach every pooled connection — a post-open db.Exec
	// configures only the one connection that runs it.
	opts := sqlitedriver.Options{
		BusyTimeoutMS: 5000,
		ForeignKeys:   true,
	}
	dsn := sqlitedriver.DSN(config.Path, opts)

	// PRAGMA key is per-connection too, and gets it worse: an unkeyed
	// connection to an encrypted database fails outright with "file is not a
	// database". Setting the key with db.Exec after opening keyed only the
	// connection that ran the statement, and this pool is database/sql's
	// default (unlimited), so the second concurrent query on an encrypted
	// store failed. The key rides in the DSN so the driver applies it as each
	// connection is opened.
	if config.EncryptDatabase {
		if !sqlitedriver.EncryptionSupported {
			return nil, fmt.Errorf("database encryption requires CGO (SQLCipher); not available in this build")
		}

		// Check for encryption key
		key := config.EncryptionKey
		if key == "" {
			// Fallback to environment variable
			key = os.Getenv("LOOM_DB_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("encryption enabled but no key provided (set EncryptionKey or LOOM_DB_KEY env var)")
		}

		keyedDSN, err := sqlitedriver.EncryptedDSN(config.Path, key, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to set encryption key: %w", err)
		}
		dsn = keyedDSN
	}

	// Open database using the pre-registered "sqlite3" driver.
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		closeErr := db.Close()
		if config.EncryptDatabase {
			return nil, errors.Join(
				fmt.Errorf("failed to verify encryption key (wrong key or corrupted database): %w", err),
				closeErr,
			)
		}
		return nil, errors.Join(
			fmt.Errorf("failed to open database: %w", err),
			closeErr,
		)
	}

	// Foreign keys are enabled via the DSN above: PRAGMA foreign_keys is
	// per-connection (default OFF), and only the DSN reaches every pooled
	// connection.

	return db, nil
}
