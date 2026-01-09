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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenDB_Unencrypted(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Open unencrypted database (default)
	db, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: false,
	})
	require.NoError(t, err)
	require.NotNil(t, db)
	defer db.Close()

	// Test that we can create a table and insert data
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (name) VALUES (?)", "test_value")
	require.NoError(t, err)

	// Verify data
	var name string
	err = db.QueryRow("SELECT name FROM test WHERE id = 1").Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "test_value", name)
}

func TestOpenDB_Encrypted(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_encrypted.db")

	testKey := "test-encryption-key-12345"

	// Open encrypted database
	db, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		EncryptionKey:   testKey,
	})
	require.NoError(t, err)
	require.NotNil(t, db)
	defer db.Close()

	// Test that we can create a table and insert data
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (name) VALUES (?)", "encrypted_value")
	require.NoError(t, err)

	// Verify data
	var name string
	err = db.QueryRow("SELECT name FROM test WHERE id = 1").Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "encrypted_value", name)

	// Close the database
	db.Close()

	// Try to open with wrong key - should fail
	dbWrongKey, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		EncryptionKey:   "wrong-key",
	})
	if err == nil {
		dbWrongKey.Close()
		t.Fatal("Expected error when opening encrypted DB with wrong key")
	}
	assert.Error(t, err)
	// SQLCipher returns "file is not a database" when the key is wrong
	assert.True(t,
		err.Error() == "failed to set encryption key: file is not a database" ||
			err.Error() == "failed to verify encryption key (wrong key or corrupted database): file is not a database",
		"Expected database error, got: %s", err.Error())

	// Try to open encrypted DB without encryption - should fail
	dbUnencrypted, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: false,
	})
	if err == nil {
		// If it opens, trying to query should fail
		var testName string
		queryErr := dbUnencrypted.QueryRow("SELECT name FROM test WHERE id = 1").Scan(&testName)
		dbUnencrypted.Close()
		assert.Error(t, queryErr, "Expected error when reading encrypted DB without key")
	}
}

func TestOpenDB_EncryptedFromEnvVar(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_env.db")

	testKey := "env-encryption-key-67890"

	// Set environment variable
	os.Setenv("LOOM_DB_KEY", testKey)
	defer os.Unsetenv("LOOM_DB_KEY")

	// Open encrypted database without explicit key (should use env var)
	db, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		// EncryptionKey not set - should use LOOM_DB_KEY
	})
	require.NoError(t, err)
	require.NotNil(t, db)
	defer db.Close()

	// Test that we can create a table and insert data
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, value TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (value) VALUES (?)", "from_env")
	require.NoError(t, err)

	// Verify data
	var value string
	err = db.QueryRow("SELECT value FROM test WHERE id = 1").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, "from_env", value)
}

func TestOpenDB_EncryptedNoKey(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_nokey.db")

	// Ensure env var is not set
	os.Unsetenv("LOOM_DB_KEY")

	// Try to open encrypted database without key - should fail
	db, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		// No key provided
	})
	assert.Error(t, err)
	assert.Nil(t, db)
	assert.Contains(t, err.Error(), "no key provided")
}

func TestNewSessionStoreWithConfig_Encrypted(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "sessions_encrypted.db")

	testKey := "session-key-abc123"

	// Create encrypted session store
	store, err := NewSessionStoreWithConfig(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		EncryptionKey:   testKey,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, store)
	defer store.Close()

	// Verify that the tables were created
	var tableCount int
	err = store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tableCount)
	require.NoError(t, err)
	assert.Greater(t, tableCount, 0, "Expected tables to be created")
}

func TestNewSessionStoreWithConfig_Unencrypted(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "sessions_plain.db")

	// Create unencrypted session store (default)
	store, err := NewSessionStoreWithConfig(DBConfig{
		Path: dbPath,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, store)
	defer store.Close()

	// Verify that the tables were created
	var tableCount int
	err = store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tableCount)
	require.NoError(t, err)
	assert.Greater(t, tableCount, 0, "Expected tables to be created")
}
