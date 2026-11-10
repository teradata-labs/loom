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
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMigrationsFromRejectsDuplicateVersions(t *testing.T) {
	t.Parallel()
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	tests := []struct {
		name      string
		files     fstest.MapFS
		wantErr   []string // substrings of the error; nil means no error
		wantCount int
	}{
		{
			name: "distinct versions load",
			files: fstest.MapFS{
				"migrations/000001_base.up.sql":     file("CREATE TABLE a (x INT);"),
				"migrations/000001_base.down.sql":   file("DROP TABLE a;"),
				"migrations/000002_second.up.sql":   file("CREATE TABLE b (x INT);"),
				"migrations/000002_second.down.sql": file("DROP TABLE b;"),
			},
			wantCount: 2,
		},
		{
			name: "two up files at one version",
			files: fstest.MapFS{
				"migrations/000001_base.up.sql":                  file("CREATE TABLE a (x INT);"),
				"migrations/000002_decision_shadow.up.sql":       file("CREATE TABLE decision_shadow (x INT);"),
				"migrations/000002_decision_shadow_error.up.sql": file("ALTER TABLE decision_shadow ADD COLUMN error TEXT;"),
			},
			wantErr: []string{"duplicate migration version 2", "000002_decision_shadow.up.sql", "000002_decision_shadow_error.up.sql"},
		},
		{
			name: "two down files at one version",
			files: fstest.MapFS{
				"migrations/000003_a.up.sql":   file("SELECT 1;"),
				"migrations/000003_a.down.sql": file("SELECT 1;"),
				"migrations/000003_b.down.sql": file("SELECT 1;"),
			},
			wantErr: []string{"duplicate migration version 3", "000003_a.down.sql", "000003_b.down.sql"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := loadMigrationsFrom(tt.files)
			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.Len(t, got, tt.wantCount)
				return
			}
			require.Error(t, err)
			for _, s := range tt.wantErr {
				assert.Contains(t, err.Error(), s)
			}
		})
	}
}

// TestEmbeddedMigrationsHaveUniqueVersions fails the build's tests, not a
// fresh database at startup, when two embedded migrations share a version.
func TestEmbeddedMigrationsHaveUniqueVersions(t *testing.T) {
	t.Parallel()
	_, err := loadMigrations()
	require.NoError(t, err)
}
