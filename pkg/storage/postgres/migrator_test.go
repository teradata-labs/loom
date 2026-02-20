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
package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	require.NoError(t, err)
	require.NotEmpty(t, migrations, "should have embedded migrations")

	// Verify we have all 9 migrations
	assert.Len(t, migrations, 9, "should have 9 migration versions")

	// Verify ordering
	for i := 1; i < len(migrations); i++ {
		assert.Greater(t, migrations[i].Version, migrations[i-1].Version,
			"migrations should be in ascending order")
	}

	// Verify each migration has up SQL
	for _, m := range migrations {
		assert.NotEmpty(t, m.UpSQL, "migration %d should have up SQL", m.Version)
		assert.NotEmpty(t, m.DownSQL, "migration %d should have down SQL", m.Version)
		assert.NotEmpty(t, m.Description, "migration %d should have a description", m.Version)
	}
}

func TestLoadMigrations_SpecificVersions(t *testing.T) {
	migrations, err := loadMigrations()
	require.NoError(t, err)

	versions := make(map[int]Migration)
	for _, m := range migrations {
		versions[m.Version] = m
	}

	// Check specific migrations exist
	tests := []struct {
		version     int
		description string
		upContains  string
	}{
		{1, "initial_schema", "CREATE TABLE IF NOT EXISTS sessions"},
		{2, "fts_indexes", "content_search tsvector"},
		{3, "rls_policies", "ENABLE ROW LEVEL SECURITY"},
		{4, "soft_delete", "purge_soft_deleted"},
		{5, "rls_with_check", "WITH CHECK"},
		{6, "user_id_and_fixes", "ADD COLUMN IF NOT EXISTS user_id"},
		{7, "user_rls_policies", "user_id = current_setting"},
		{8, "tool_exec_snapshot_user_id", "ALTER TABLE tool_executions ADD COLUMN"},
		{9, "session_name", "ADD COLUMN IF NOT EXISTS name"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			m, ok := versions[tt.version]
			require.True(t, ok, "migration version %d should exist", tt.version)
			assert.Contains(t, m.UpSQL, tt.upContains,
				"migration %d up SQL should contain expected content", tt.version)
		})
	}
}
