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

//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrator_FreshDatabaseBootstrap is the regression test for the
// upgrade-bootstrap bug: on a database with no schema_migrations table,
// CurrentVersion must report 0 — and PendingMigrations must list every
// migration — instead of erroring with `relation "schema_migrations" does not
// exist` (SQLSTATE 42P01). Without the fix, `looms upgrade [--dry-run]` could
// not introspect a brand-new Postgres/Supabase database.
//
// The test isolates in a fresh, empty schema and pins the connection
// search_path to it, so schema_migrations is absent without touching public.
func TestMigrator_FreshDatabaseBootstrap(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("boot_test_%d", os.Getpid())

	// Admin pool (default search_path) to create/drop the isolated schema.
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		admin.Close()
	})

	// Migrator pool pinned to the empty schema only (public excluded), so the
	// unqualified schema_migrations lookup finds nothing — a fresh DB.
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, fmt.Sprintf("SET search_path TO %s", schema))
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	m, err := NewMigrator(pool, nil)
	require.NoError(t, err)

	v, err := m.CurrentVersion(ctx)
	require.NoError(t, err, "CurrentVersion must not error on a fresh DB (no schema_migrations)")
	assert.Equal(t, 0, v, "a fresh database should report version 0")

	pending, err := m.PendingMigrations(ctx)
	require.NoError(t, err, "PendingMigrations must not error on a fresh DB")
	assert.Len(t, pending, len(m.migrations), "all migrations should be pending on a fresh DB")
}
