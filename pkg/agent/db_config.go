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
	"fmt"
	"os"

	_ "github.com/mutecomm/go-sqlcipher/v4" // Auto-registers as "sqlite3"
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
	// Open database using pre-registered "sqlite3" driver from sqlcipher
	db, err := sql.Open("sqlite3", config.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set encryption key if encryption is enabled
	if config.EncryptDatabase {
		// Check for encryption key
		key := config.EncryptionKey
		if key == "" {
			// Fallback to environment variable
			key = os.Getenv("LOOM_DB_KEY")
		}
		if key == "" {
			db.Close()
			return nil, fmt.Errorf("encryption enabled but no key provided (set EncryptionKey or LOOM_DB_KEY env var)")
		}

		// Set encryption key via PRAGMA
		// Note: This must be the first operation after opening the database
		_, err = db.Exec(fmt.Sprintf("PRAGMA key = '%s'", key))
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set encryption key: %w", err)
		}
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		if config.EncryptDatabase {
			return nil, fmt.Errorf("failed to verify encryption key (wrong key or corrupted database): %w", err)
		}
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return db, nil
}
