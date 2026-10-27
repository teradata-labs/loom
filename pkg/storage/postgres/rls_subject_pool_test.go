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
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
)

// migratedAdminPool connects to TEST_POSTGRES_URL and runs every migration,
// skipping the test when the variable is unset. It has no build tag, so store
// tests in the ordinary gate can use it (testPool needs -tags integration).
func migratedAdminPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping PostgreSQL store test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "failed to connect to PostgreSQL")
	t.Cleanup(pool.Close)

	migrator, err := NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx), "failed to run migrations")
	return pool
}

// rlsAppRole is a login role with no SUPERUSER and no BYPASSRLS, so row-level
// security applies to every query it runs. The test container's own user is a
// superuser, and a store connected as that user passes tenant-isolation tests
// whether or not the policies work.
const (
	rlsAppRole     = "loom_rls_app"
	rlsAppPassword = "loom_rls_app_test"
)

// rlsSubjectPool returns a pool that connects to the same database as
// adminPool but as rlsAppRole, creating the role and granting it table access
// if needed. Build a store on this pool to test it the way a production
// deployment on a plain role (such as Supabase's postgres) runs it.
func rlsSubjectPool(t *testing.T, adminPool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	_, err := adminPool.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '`+rlsAppRole+`') THEN
			CREATE ROLE `+rlsAppRole+` LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '`+rlsAppPassword+`';
		END IF;
	END $$;`)
	if err != nil {
		t.Skipf("cannot create the %s role (the test user needs CREATEROLE): %v", rlsAppRole, err)
	}
	for _, stmt := range []string{
		"GRANT USAGE ON SCHEMA public TO " + rlsAppRole,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + rlsAppRole,
		"GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO " + rlsAppRole,
	} {
		_, err := adminPool.Exec(ctx, stmt)
		require.NoError(t, err, "granting %s access: %s", rlsAppRole, stmt)
	}

	cfg := adminPool.Config().Copy()
	cfg.ConnConfig.User = rlsAppRole
	cfg.ConnConfig.Password = rlsAppPassword
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "connecting as %s", rlsAppRole)
	t.Cleanup(pool.Close)

	var bypass bool
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&bypass))
	require.False(t, bypass, "%s must be subject to row-level security", rlsAppRole)
	return pool
}

// requireForcedRLS fails unless table has row-level security both enabled and
// forced. Without FORCE, a table owner that is not a superuser skips the
// policy, which is how Loom connects on hosted Postgres.
func requireForcedRLS(t *testing.T, pool *pgxpool.Pool, table string) {
	t.Helper()
	var enabled, forced bool
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = $1::regclass", table,
	).Scan(&enabled, &forced))
	require.True(t, enabled, "%s: row-level security is not enabled", table)
	require.True(t, forced, "%s: row-level security is not forced", table)
}
