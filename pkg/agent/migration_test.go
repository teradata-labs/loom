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

// TestMigrationFromV100 simulates upgrading from v1.0.0 database
func TestMigrationFromV100(t *testing.T) {
	// Create temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_v100.db")

	// Create v1.0.0 schema (without session_id in artifacts)
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Enable foreign keys
	_, err = db.Exec("PRAGMA foreign_keys=ON")
	require.NoError(t, err)

	// Create v1.0.0 schema
	schema := `
	CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		agent_id TEXT,
		parent_session_id TEXT,
		context_json TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		total_cost_usd REAL DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		FOREIGN KEY (parent_session_id) REFERENCES sessions(id) ON DELETE SET NULL
	);

	CREATE TABLE messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT,
		tool_calls_json TEXT,
		tool_use_id TEXT,
		tool_result_json TEXT,
		session_context TEXT DEFAULT 'direct',
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

	// Close the database
	db.Close()

	// Now try to open it with NewSessionStore (this should migrate it)
	tracer := observability.NewNoOpTracer()
	store, err := NewSessionStore(dbPath, tracer)
	require.NoError(t, err, "Failed to create session store with migration")
	defer store.Close()

	// Verify the migration worked
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('artifacts') WHERE name='session_id'").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "session_id column should exist after migration")

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
