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
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/internal/sqlitedriver"
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
	defer func() { _ = db.Close() }()

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
	defer func() { _ = db.Close() }()

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
	_ = db.Close()

	// Try to open with wrong key - should fail
	dbWrongKey, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		EncryptionKey:   "wrong-key",
	})
	if err == nil {
		_ = dbWrongKey.Close()
		t.Fatal("Expected error when opening encrypted DB with wrong key")
	}
	assert.Error(t, err)
	// The key rides in the DSN, so a wrong key is rejected when the first
	// connection is opened — i.e. at Ping. SQLCipher reports it as "file is
	// not a database".
	assert.Equal(t,
		"failed to verify encryption key (wrong key or corrupted database): file is not a database",
		err.Error())

	// Try to open encrypted DB without encryption - should fail
	dbUnencrypted, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: false,
	})
	if err == nil {
		// If it opens, trying to query should fail
		var testName string
		queryErr := dbUnencrypted.QueryRow("SELECT name FROM test WHERE id = 1").Scan(&testName)
		_ = dbUnencrypted.Close()
		assert.Error(t, queryErr, "Expected error when reading encrypted DB without key")
	}
}

func TestOpenDB_EncryptedFromEnvVar(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_env.db")

	testKey := "env-encryption-key-67890"

	// Set environment variable
	_ = os.Setenv("LOOM_DB_KEY", testKey)
	defer func() { _ = os.Unsetenv("LOOM_DB_KEY") }()

	// Open encrypted database without explicit key (should use env var)
	db, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		// EncryptionKey not set - should use LOOM_DB_KEY
	})
	require.NoError(t, err)
	require.NotNil(t, db)
	defer func() { _ = db.Close() }()

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
	_ = os.Unsetenv("LOOM_DB_KEY")

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
	defer func() { _ = store.Close() }()

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
	defer func() { _ = store.Close() }()

	// Verify that the tables were created
	var tableCount int
	err = store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tableCount)
	require.NoError(t, err)
	assert.Greater(t, tableCount, 0, "Expected tables to be created")
}

// TestOpenDB_EncryptedPooledConnections is the regression test for the
// PRAGMA-key half of the pooled-connection defect (issues #366, #370).
//
// OpenDB used to set the key with db.Exec after opening, which keys only the
// connection that happens to run the statement. The pool here is
// database/sql's default — unlimited — so the second connection the pool
// opened was unkeyed and every query on it failed with "file is not a
// database". Before the fix this test failed on conns 1..3; the key now rides
// in the DSN, so the driver applies it as each connection is opened.
func TestOpenDB_EncryptedPooledConnections(t *testing.T) {
	if !sqlitedriver.EncryptionSupported {
		t.Skip("SQLCipher requires CGO")
	}

	dbPath := filepath.Join(t.TempDir(), "pooled_encrypted.db")
	db, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		EncryptionKey:   "pooled-key-abc123",
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec("CREATE TABLE secret (id INTEGER PRIMARY KEY, v TEXT)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO secret (v) VALUES ('classified')")
	require.NoError(t, err)

	// Hold each connection while acquiring the next so the pool is forced to
	// open fresh ones instead of handing back the keyed one.
	const pinned = 4
	conns := make([]*sql.Conn, 0, pinned)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < pinned; i++ {
		c, err := db.Conn(context.Background())
		require.NoError(t, err, "conn %d: every pooled connection must be keyed", i)
		conns = append(conns, c)

		var v string
		require.NoError(t,
			c.QueryRowContext(context.Background(), "SELECT v FROM secret WHERE id = 1").Scan(&v),
			"conn %d: query on a pooled connection must not fail", i)
		assert.Equal(t, "classified", v, "conn %d", i)
	}
	require.Equal(t, pinned, db.Stats().OpenConnections,
		"connections must be distinct for this test to prove anything")
}

// TestOpenDB_EncryptedConcurrent exercises the same defect the way production
// hits it: concurrent goroutines, which make database/sql open more than one
// connection on its own.
func TestOpenDB_EncryptedConcurrent(t *testing.T) {
	if !sqlitedriver.EncryptionSupported {
		t.Skip("SQLCipher requires CGO")
	}

	dbPath := filepath.Join(t.TempDir(), "concurrent_encrypted.db")
	db, err := OpenDB(DBConfig{
		Path:            dbPath,
		EncryptDatabase: true,
		EncryptionKey:   "concurrent-key-xyz",
	})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec("CREATE TABLE secret (id INTEGER PRIMARY KEY, v TEXT)")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO secret (v) VALUES ('classified')")
	require.NoError(t, err)

	const readers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, readers)
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(reader int) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				var v string
				if err := db.QueryRow("SELECT v FROM secret WHERE id = 1").Scan(&v); err != nil {
					errCh <- fmt.Errorf("reader %d: %w", reader, err)
					return
				}
			}
		}(r)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent read on encrypted database failed: %v", err)
	}
}
