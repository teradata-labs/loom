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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestMigrationFromPreV100 simulates upgrading from a very old database
// that's missing agent_id, parent_session_id, session_context, and session_id columns
func TestMigrationFromPreV100(t *testing.T) {
	// Create temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_pre_v100.db")

	// Create very old schema (without migrated columns)
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Enable foreign keys
	_, err = db.Exec("PRAGMA foreign_keys=ON")
	require.NoError(t, err)

	// Create old schema WITHOUT: agent_id, parent_session_id, session_context, session_id in artifacts
	schema := `
	CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		context_json TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		total_cost_usd REAL DEFAULT 0,
		total_tokens INTEGER DEFAULT 0
	);

	CREATE TABLE messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT,
		tool_calls_json TEXT,
		tool_use_id TEXT,
		tool_result_json TEXT,
		timestamp INTEGER NOT NULL,
		token_count INTEGER DEFAULT 0,
		cost_usd REAL DEFAULT 0,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE TABLE artifacts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		source TEXT NOT NULL,
		source_agent_id TEXT,
		purpose TEXT,
		content_type TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		checksum TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		last_accessed_at INTEGER,
		access_count INTEGER DEFAULT 0,
		tags TEXT,
		metadata_json TEXT,
		deleted_at INTEGER
	);

	CREATE TABLE tool_executions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		input_json TEXT,
		result_json TEXT,
		error TEXT,
		execution_time_ms INTEGER,
		timestamp INTEGER NOT NULL,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);
	`

	_, err = db.Exec(schema)
	require.NoError(t, err)

	// Insert test data
	ctx := context.Background()
	_, err = db.ExecContext(ctx,
		"INSERT INTO sessions (id, context_json, created_at, updated_at) VALUES (?, ?, ?, ?)",
		"test-session", "{}", 1000000, 1000000)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		"INSERT INTO messages (session_id, role, content, timestamp, token_count, cost_usd) VALUES (?, ?, ?, ?, ?, ?)",
		"test-session", "user", "test message", 1000000, 10, 0.01)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		"INSERT INTO artifacts (id, name, path, source, content_type, size_bytes, checksum, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		"artifact1", "test.txt", "/tmp/test.txt", "user", "text/plain", 100, "abc123", 1000000, 1000000)
	require.NoError(t, err)

	// Close the database
	db.Close()

	// Now try to open it with NewSessionStore (this should migrate it)
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(dbPath, tracer)
	require.NoError(t, err, "Failed to create session store with migration")
	defer store.Close()

	// Verify all migrations worked
	var count int

	// Check agent_id column in sessions
	err = store.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='agent_id'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "agent_id column should exist after migration")

	// Check parent_session_id column in sessions
	err = store.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='parent_session_id'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "parent_session_id column should exist after migration")

	// Check session_context column in messages
	err = store.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name='session_context'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "session_context column should exist after migration")

	// Check session_id column in artifacts
	err = store.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('artifacts') WHERE name='session_id'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "session_id column should exist after migration")

	// Verify all indexes were created
	indexes := []string{
		"idx_sessions_agent",
		"idx_sessions_parent",
		"idx_messages_context",
		"idx_artifacts_session",
	}

	for _, indexName := range indexes {
		var indexExists int
		err = store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", indexName).Scan(&indexExists)
		require.NoError(t, err)
		require.Equal(t, 1, indexExists, "Index %s should exist after migration", indexName)
	}

	// Verify we can still read the old data
	sessions, err := store.ListSessions(ctx)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, "test-session", sessions[0])

	// Verify we can read messages
	messages, err := store.LoadMessages(ctx, "test-session")
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "test message", messages[0].Content)
}
