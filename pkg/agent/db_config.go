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
	// Open database using the pre-registered "sqlite3" driver. busy_timeout
	// and foreign_keys are per-connection settings, so they must ride in the
	// DSN to reach every pooled connection — a post-open db.Exec configures
	// only the one connection that runs it. Both PRAGMAs are safe before
	// PRAGMA key on encrypted databases (neither touches data pages).
	dsn := sqlitedriver.DSN(config.Path, sqlitedriver.Options{
		BusyTimeoutMS: 5000,
		ForeignKeys:   true,
	})
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set encryption key if encryption is enabled
	if config.EncryptDatabase && !sqlitedriver.EncryptionSupported {
		return nil, errors.Join(
			fmt.Errorf("database encryption requires CGO (SQLCipher); not available in this build"),
			db.Close(),
		)
	}
	if config.EncryptDatabase {
		// Check for encryption key
		key := config.EncryptionKey
		if key == "" {
			// Fallback to environment variable
			key = os.Getenv("LOOM_DB_KEY")
		}
		if key == "" {
			return nil, errors.Join(
				fmt.Errorf("encryption enabled but no key provided (set EncryptionKey or LOOM_DB_KEY env var)"),
				db.Close(),
			)
		}

		// Set encryption key via PRAGMA
		// Note: This must be the first operation after opening the database
		_, err = db.Exec(fmt.Sprintf("PRAGMA key = '%s'", key))
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("failed to set encryption key: %w", err),
				db.Close(),
			)
		}
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
