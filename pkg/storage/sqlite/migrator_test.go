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
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
)

// newTestDB creates a temporary SQLite database for testing.
// The database is opened with foreign keys enabled and WAL mode for
// realistic migration testing conditions.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// tableExists checks whether a table with the given name exists in the database.
func tableExists(t *testing.T, db *sql.DB, tableName string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?",
		tableName,
	).Scan(&count)
	require.NoError(t, err)
	return count > 0
}

func TestMigrateUp_FreshDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)

	err = migrator.MigrateUp(ctx)
	require.NoError(t, err)

	// Verify schema_migrations table exists
	assert.True(t, tableExists(t, db, "schema_migrations"),
		"schema_migrations table should exist after MigrateUp")

	// Verify CurrentVersion returns 1
	version, err := migrator.CurrentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, version, "version should be 1 after applying initial migration")

	// Verify all expected tables exist
	expectedTables := []string{
		"sessions",
		"messages",
		"tool_executions",
		"memory_snapshots",
		"artifacts",
		"agent_errors",
		"sql_result_metadata",
		"human_requests",
	}
	for _, table := range expectedTables {
		assert.True(t, tableExists(t, db, table),
			"table %q should exist after MigrateUp", table)
	}

	// Verify PendingMigrations returns empty list
	pending, err := migrator.PendingMigrations(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "no migrations should be pending after MigrateUp")
}

func TestMigrateUp_Idempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)

	// First call
	err = migrator.MigrateUp(ctx)
	require.NoError(t, err)

	versionAfterFirst, err := migrator.CurrentVersion(ctx)
	require.NoError(t, err)

	// Second call should succeed without error
	err = migrator.MigrateUp(ctx)
	require.NoError(t, err)

	versionAfterSecond, err := migrator.CurrentVersion(ctx)
	require.NoError(t, err)

	assert.Equal(t, versionAfterFirst, versionAfterSecond,
		"version should be identical after running MigrateUp twice")
}

func TestBootstrap_PreMigrationDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Simulate a pre-migration database: create the sessions table manually
	// with some data, but no schema_migrations table.
	_, err := db.ExecContext(ctx, `
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			name TEXT,
			agent_id TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		"INSERT INTO sessions (id, name, agent_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		"sess-001", "test session", "agent-1", 1700000000, 1700000000,
	)
	require.NoError(t, err)

	// Verify schema_migrations does NOT exist yet
	assert.False(t, tableExists(t, db, "schema_migrations"),
		"schema_migrations should not exist in a pre-migration database")

	// Create migrator and run MigrateUp
	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)

	err = migrator.MigrateUp(ctx)
	require.NoError(t, err)

	// Verify schema_migrations was created
	assert.True(t, tableExists(t, db, "schema_migrations"),
		"schema_migrations should exist after bootstrap + MigrateUp")

	// Verify version is 1 (bootstrapped)
	version, err := migrator.CurrentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, version,
		"version should be 1 after bootstrapping a pre-migration database")

	// Verify the original data in sessions is still present
	var sessionName string
	err = db.QueryRowContext(ctx,
		"SELECT name FROM sessions WHERE id = ?", "sess-001",
	).Scan(&sessionName)
	require.NoError(t, err)
	assert.Equal(t, "test session", sessionName,
		"pre-existing session data should survive bootstrap migration")
}

func TestPendingMigrations_FreshDB(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)

	// ensureMigrationsTable must be called before PendingMigrations,
	// because PendingMigrations queries schema_migrations.
	err = migrator.ensureMigrationsTable(ctx)
	require.NoError(t, err)

	pending, err := migrator.PendingMigrations(ctx)
	require.NoError(t, err)

	// On a fresh DB with no applied migrations, all loaded migrations should be pending.
	assert.NotEmpty(t, pending, "fresh DB should have pending migrations")
	assert.Equal(t, 1, pending[0].Version,
		"first pending migration should be version 1")
}

func TestCurrentVersion_AfterMigrateUp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)

	err = migrator.MigrateUp(ctx)
	require.NoError(t, err)

	version, err := migrator.CurrentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, version,
		"CurrentVersion should return 1 after applying all migrations")
}

func TestMigrateDown(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	migrator, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)

	// First migrate up
	err = migrator.MigrateUp(ctx)
	require.NoError(t, err)

	version, err := migrator.CurrentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, version, "should be at version 1 before rollback")

	// Now migrate down 1 step
	err = migrator.MigrateDown(ctx, 1)
	require.NoError(t, err)

	// Verify CurrentVersion returns 0
	version, err = migrator.CurrentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, version,
		"CurrentVersion should return 0 after rolling back all migrations")

	// Verify all user tables are dropped
	droppedTables := []string{
		"sessions",
		"messages",
		"tool_executions",
		"memory_snapshots",
		"artifacts",
		"agent_errors",
		"sql_result_metadata",
		"human_requests",
	}
	for _, table := range droppedTables {
		assert.False(t, tableExists(t, db, table),
			"table %q should not exist after MigrateDown", table)
	}
}

func TestNewMigrator_NilTracer(t *testing.T) {
	db := newTestDB(t)

	// Passing nil tracer should not panic; NewMigrator falls back to NoOpTracer.
	migrator, err := NewMigrator(db, nil)
	require.NoError(t, err)
	require.NotNil(t, migrator, "migrator should not be nil when tracer is nil")

	// Verify operations still work with the nil-safe tracer fallback.
	ctx := context.Background()
	err = migrator.MigrateUp(ctx)
	require.NoError(t, err)

	version, err := migrator.CurrentVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, version, "migration should succeed with nil tracer fallback")
}
