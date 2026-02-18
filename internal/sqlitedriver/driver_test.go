package sqlitedriver_test

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
)

func TestDriverRegistered(t *testing.T) {
	assert.True(t, slices.Contains(sql.Drivers(), "sqlite3"), "sqlite3 driver should be registered")
}

func TestBasicCRUD(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO test (name) VALUES (?)", "hello")
	require.NoError(t, err)

	var name string
	err = db.QueryRow("SELECT name FROM test WHERE id = 1").Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "hello", name)
}

func TestFTS5Available(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE fts_test USING fts5(content)")
	require.NoError(t, err, "FTS5 should be available")

	_, err = db.Exec("INSERT INTO fts_test (content) VALUES (?)", "hello world")
	require.NoError(t, err)

	var content string
	err = db.QueryRow("SELECT content FROM fts_test WHERE fts_test MATCH 'hello'").Scan(&content)
	require.NoError(t, err)
	assert.Equal(t, "hello world", content)
}
