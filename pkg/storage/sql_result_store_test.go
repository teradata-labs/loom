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

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSQLResultStore(t *testing.T) *SQLResultStore {
	t.Helper()

	store, err := NewSQLResultStore(&SQLResultStoreConfig{
		DBPath: filepath.Join(t.TempDir(), "test.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testSQLResultData() map[string]interface{} {
	return map[string]interface{}{
		"columns": []interface{}{"name", "value"},
		"rows": []interface{}{
			[]interface{}{"alpha", "1"},
			[]interface{}{"beta", "2"},
		},
	}
}

// tableExists reports whether a table is present in sqlite_master.
func tableExists(t *testing.T, store *SQLResultStore, tableName string) bool {
	t.Helper()

	var count int
	err := store.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tableName,
	).Scan(&count)
	require.NoError(t, err)
	return count > 0
}

// Regression test: MCP tool results use IDs like "mcp_<serverName>_<ts>" where
// the server name may contain hyphens. Before sanitizing at creation time, the
// raw table name "tool_result_mcp_my-server_..." broke CREATE TABLE (hyphen is
// invalid in an unquoted SQLite identifier), so storing the result failed.
func TestSQLResultStore_HyphenatedMCPServerName(t *testing.T) {
	t.Parallel()

	store := newTestSQLResultStore(t)
	ctx := context.Background()

	id := "mcp_my-server_1234567890"
	ref, err := store.Store(ctx, id, testSQLResultData())
	require.NoError(t, err, "Store must handle IDs containing hyphens")
	require.NotNil(t, ref)
	assert.Equal(t, id, ref.Id)

	meta, err := store.GetMetadata(ctx, id)
	require.NoError(t, err)
	assert.NotContains(t, meta.TableName, "-", "stored table name must be a sanitized identifier")
	assert.Equal(t, "tool_result_mcp_my_server_1234567890", meta.TableName)
	assert.True(t, tableExists(t, store, meta.TableName), "sanitized table must exist")

	// The query_tool_result builtin interpolates meta.TableName into SQL;
	// verify that round-trip works.
	rows, err := store.Query(ctx, id, fmt.Sprintf("SELECT * FROM %s LIMIT 100", meta.TableName))
	require.NoError(t, err)
	require.NotNil(t, rows)

	// Delete must drop the sanitized table, not a divergent raw name.
	require.NoError(t, store.Delete(ctx, id))
	assert.False(t, tableExists(t, store, meta.TableName), "Delete must drop the result table")
}

// A crafted ID with SQL metacharacters must not escape the identifier position.
func TestSQLResultStore_MaliciousIDSanitized(t *testing.T) {
	t.Parallel()

	store := newTestSQLResultStore(t)
	ctx := context.Background()

	id := `x"; DROP TABLE sql_result_metadata;--`
	_, err := store.Store(ctx, id, testSQLResultData())
	require.NoError(t, err)

	assert.True(t, tableExists(t, store, "sql_result_metadata"),
		"metadata table must survive a crafted ID")

	meta, err := store.GetMetadata(ctx, id)
	require.NoError(t, err)
	for _, c := range meta.TableName {
		validChar := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
		assert.True(t, validChar, "table name char %q must be sanitized", c)
	}
	assert.True(t, tableExists(t, store, meta.TableName))

	require.NoError(t, store.Delete(ctx, id))
	assert.False(t, tableExists(t, store, meta.TableName))
	assert.True(t, tableExists(t, store, "sql_result_metadata"))
}

// Defense-in-depth: a tampered table_name in the metadata table must not reach
// GetMetadata consumers or the DROP TABLE statements unsanitized.
func TestSQLResultStore_TamperedMetadataTableName(t *testing.T) {
	t.Parallel()

	store := newTestSQLResultStore(t)
	ctx := context.Background()

	id := "tampered"
	_, err := store.Store(ctx, id, testSQLResultData())
	require.NoError(t, err)

	injected := `tool_result_x; DROP TABLE sql_result_metadata;--`
	_, err = store.db.Exec(`UPDATE sql_result_metadata SET table_name = ? WHERE id = ?`, injected, id)
	require.NoError(t, err)

	meta, err := store.GetMetadata(ctx, id)
	require.NoError(t, err)
	assert.NotContains(t, meta.TableName, ";", "GetMetadata must return a sanitized table name")
	assert.NotContains(t, meta.TableName, " ")

	require.NoError(t, store.Delete(ctx, id))
	assert.True(t, tableExists(t, store, "sql_result_metadata"),
		"metadata table must survive a tampered table_name")
}

func TestSanitizeIdentifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "clean identifier unchanged", input: "tool_result_abc123", want: "tool_result_abc123"},
		{name: "hyphens replaced", input: "tool_result_mcp_my-server_1", want: "tool_result_mcp_my_server_1"},
		{name: "sql metacharacters replaced", input: `x"; DROP TABLE y;--`, want: "x___DROP_TABLE_y___"},
		{name: "spaces replaced", input: "a b c", want: "a_b_c"},
		{name: "leading digit prefixed", input: "1abc", want: "col_1abc"},
		{name: "empty becomes col", input: "", want: "col"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeIdentifier(tt.input)
			assert.Equal(t, tt.want, got)

			// Result must always be a safe SQL identifier.
			assert.NotEmpty(t, got)
			for _, c := range got {
				validChar := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
				assert.True(t, validChar, "char %q in %q", c, got)
			}
			assert.False(t, strings.ContainsAny(got, `"';- `))
		})
	}
}
