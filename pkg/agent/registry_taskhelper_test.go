// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
	"github.com/teradata-labs/loom/pkg/task"
	"go.uber.org/zap"
)

// newTaskSubsystem builds a fully-migrated SQLite-backed task subsystem
// suitable for unit tests that need a real task.Manager + Decomposer pair.
// The db is closed on test cleanup. Kept in its own file to localize the
// storage/sqlite + sqlitedriver imports.
func newTaskSubsystem(t *testing.T) (*sql.DB, *task.Manager, *task.Decomposer) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tasks.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	tracer := observability.NewNoOpTracer()
	mig, err := sqlite.NewMigrator(db, tracer)
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))

	store := sqlite.NewTaskStore(db, tracer)
	mgr := task.NewManager(store, nil, tracer, zap.NewNop())
	dec := task.NewDecomposer(mgr, tracer, zap.NewNop())
	return db, mgr, dec
}
