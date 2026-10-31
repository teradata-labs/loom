// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package workspace_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
	pg "github.com/teradata-labs/loom/pkg/storage/postgres"
)

// The cloud's store, exercised the way the cloud gets it: by running the
// migrations and then talking to the schema they produced. This is the only
// test that proves migration 000024 applies at all — including its backfill,
// which adopts sessions that predate workspaces.
//
// Set TEST_POSTGRES_URL to run it.
func TestPostgres_MigrationAppliesAndBackfills(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping the PostgreSQL workspace test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(ctx))

	m, err := pg.NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, m.MigrateUp(ctx), "the migration chain must apply, 000024 included")

	// The backfill is the part that touches an existing deployment's rows, so
	// it is exercised the way it will actually run: roll this migration back,
	// leave sessions behind as an older deployment would have them, and apply
	// it again.
	// Roll back to just before it, however many migrations have come since.
	latest, err := m.CurrentVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, m.MigrateDown(ctx, latest-23), "000024 and what came after it must roll back cleanly")
	_, err = pool.Exec(ctx, `DELETE FROM sessions`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO sessions (id, user_id) VALUES ('pre-1','ann'),('pre-2','ann'),('pre-3','bob')`)
	require.NoError(t, err)
	require.NoError(t, m.MigrateUp(ctx), "re-applying 000024 and after must succeed over existing sessions")

	// Every pre-existing session now lives in its owner's personal workspace,
	// and the two owners did not end up sharing one.
	rows, err := pool.Query(ctx, `SELECT s.id, w.owner_user_id, w.name, w.is_personal
		FROM sessions s JOIN workspaces w ON w.id = s.workspace_id ORDER BY s.id`)
	require.NoError(t, err)
	defer rows.Close()
	adopted := map[string]string{}
	for rows.Next() {
		var id, owner, name string
		var personal bool
		require.NoError(t, rows.Scan(&id, &owner, &name, &personal))
		require.True(t, personal, "%s was adopted into a workspace that is not the personal one", id)
		require.Equal(t, "default", name)
		adopted[id] = owner
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[string]string{"pre-1": "ann", "pre-2": "ann", "pre-3": "bob"}, adopted,
		"each session went to its own owner's personal workspace")

	var personalCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM workspaces WHERE is_personal`).Scan(&personalCount))
	require.Equal(t, 2, personalCount, "one personal workspace per owner, not one per session")

	// The schema the store depends on.
	var col string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'sessions' AND column_name = 'workspace_id'`).Scan(&col))
	require.Equal(t, "workspace_id", col)

	for _, table := range []string{"workspaces", "workspace_repos"} {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, table).Scan(&n))
		require.Equal(t, 1, n, "%s must exist after the migration", table)
	}

	// The personal-workspace index is what keeps "one per owner" true.
	var idx int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE indexname = 'ws_personal'`).Scan(&idx))
	require.Equal(t, 1, idx)
}
