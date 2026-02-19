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
package backend

import (
	"context"
	"path/filepath"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSQLiteBackend_Default(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.SQLiteStorageConfig{
		Path: dbPath,
	}

	backend, err := NewSQLiteBackend(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	// Verify all stores are non-nil
	assert.NotNil(t, backend.SessionStorage(), "SessionStorage should not be nil")
	assert.NotNil(t, backend.ErrorStore(), "ErrorStore should not be nil")
	assert.NotNil(t, backend.ArtifactStore(), "ArtifactStore should not be nil")
	assert.NotNil(t, backend.ResultStore(), "ResultStore should not be nil")
	assert.NotNil(t, backend.HumanRequestStore(), "HumanRequestStore should not be nil")
}

func TestNewSQLiteBackend_NilConfig(t *testing.T) {
	// nil config should use defaults - creates DB in default data dir
	backend, err := NewSQLiteBackend(nil, nil)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()
}

func TestSQLiteBackend_Ping(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.SQLiteStorageConfig{
		Path: dbPath,
	}

	backend, err := NewSQLiteBackend(cfg, nil)
	require.NoError(t, err)
	defer backend.Close()

	ctx := context.Background()
	err = backend.Ping(ctx)
	assert.NoError(t, err, "Ping should succeed on a healthy backend")
}

func TestSQLiteBackend_Migrate(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.SQLiteStorageConfig{
		Path: dbPath,
	}

	backend, err := NewSQLiteBackend(cfg, nil)
	require.NoError(t, err)
	defer backend.Close()

	ctx := context.Background()
	err = backend.Migrate(ctx)
	assert.NoError(t, err, "Migrate should be a no-op for SQLite")
}

func TestSQLiteBackend_Close(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.SQLiteStorageConfig{
		Path: dbPath,
	}

	backend, err := NewSQLiteBackend(cfg, nil)
	require.NoError(t, err)

	err = backend.Close()
	assert.NoError(t, err, "Close should succeed without error")
}

func TestSQLiteBackend_InterfaceCompliance(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.SQLiteStorageConfig{
		Path: dbPath,
	}

	backend, err := NewSQLiteBackend(cfg, nil)
	require.NoError(t, err)
	defer backend.Close()

	var _ StorageBackend = backend
}

func TestNewStorageBackend_SQLite(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.StorageConfig{
		Backend: loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE,
		Sqlite: &loomv1.SQLiteStorageConfig{
			Path: dbPath,
		},
	}

	backend, err := NewStorageBackend(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	_, ok := backend.(*SQLiteBackend)
	assert.True(t, ok, "Should return *SQLiteBackend for SQLite config")
}

func TestNewStorageBackend_NilConfig(t *testing.T) {
	backend, err := NewStorageBackend(nil, nil)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	_, ok := backend.(*SQLiteBackend)
	assert.True(t, ok, "nil config should default to SQLiteBackend")
}

func TestNewStorageBackend_UnspecifiedType(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.StorageConfig{
		Backend: loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_UNSPECIFIED,
		Sqlite: &loomv1.SQLiteStorageConfig{
			Path: dbPath,
		},
	}

	backend, err := NewStorageBackend(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, backend)
	defer backend.Close()

	_, ok := backend.(*SQLiteBackend)
	assert.True(t, ok, "UNSPECIFIED should default to SQLiteBackend")
}

func TestNewStorageBackend_PostgresRequiresConfig(t *testing.T) {
	cfg := &loomv1.StorageConfig{
		Backend: loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_POSTGRES,
	}

	backend, err := NewStorageBackend(cfg, nil)
	assert.Error(t, err, "Postgres without config should return error")
	assert.Nil(t, backend)
	assert.Contains(t, err.Error(), "requires postgres configuration")
}

func TestSQLiteBackend_SessionStorageOperations(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &loomv1.SQLiteStorageConfig{
		Path: dbPath,
	}

	backend, err := NewSQLiteBackend(cfg, nil)
	require.NoError(t, err)
	defer backend.Close()

	ctx := context.Background()

	// Verify we can list sessions (should be empty)
	sessions, err := backend.SessionStorage().ListSessions(ctx)
	require.NoError(t, err)
	assert.Empty(t, sessions, "New database should have no sessions")

	// Verify stats work
	stats, err := backend.SessionStorage().GetStats(ctx)
	require.NoError(t, err)
	assert.NotNil(t, stats)
}
