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
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
)

// The workflow holds the session lease only when the storage backend is the
// SQLite file its sessions are in: the lease row references the session's row.
func TestTheWorkflowLeaseNeedsTheSessionsOwnDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOM_DATA_DIR", dir)
	sessions := filepath.Join(dir, "loom.db")
	sqlite := func(path string) *loomv1.StorageConfig {
		return &loomv1.StorageConfig{
			Backend: loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_SQLITE,
			Sqlite:  &loomv1.SQLiteStorageConfig{Path: path},
		}
	}
	require.Equal(t, sessions, filepath.Join(loomconfig.GetLoomDataDir(), "loom.db"))
	other := filepath.Join(dir, "other.db")
	link := filepath.Join(dir, "link.db")
	for _, f := range []string{sessions, other} {
		require.NoError(t, os.WriteFile(f, nil, 0o600))
	}
	require.NoError(t, os.Symlink(sessions, link))

	require.True(t, sqliteBackendAt(sqlite(sessions), sessions), "the same file")
	require.True(t, sqliteBackendAt(sqlite(filepath.Join(dir, "x", "..", "loom.db")), sessions), "the same file, written another way")
	require.True(t, sqliteBackendAt(sqlite(""), sessions), "the backend's default path is the sessions' default path")
	require.True(t, sqliteBackendAt(sqlite(link), sessions), "the same file, through a link")
	require.False(t, sqliteBackendAt(sqlite(other), sessions), "another SQLite file")
	require.False(t, sqliteBackendAt(sqlite(":memory:"), ":memory:"), "two in-memory databases are never one")
	require.False(t, sqliteBackendAt(&loomv1.StorageConfig{
		Backend: loomv1.StorageBackendType_STORAGE_BACKEND_TYPE_POSTGRES,
	}, sessions), "a Postgres backend")
}
