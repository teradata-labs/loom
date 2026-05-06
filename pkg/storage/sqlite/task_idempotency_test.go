// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/task"
)

// migratedDB returns a fully-migrated SQLite db for store tests.
func migratedDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath+"?_fk=1&_journal_mode=WAL")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mig, err := NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, mig.MigrateUp(context.Background()))
	return db
}

func TestTask_GetByIdempotencyKey_Roundtrip(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	created, err := store.CreateTask(ctx, &task.Task{
		Title:               "skill-emitted",
		Status:              loomv1.TaskStatus_TASK_STATUS_OPEN,
		SkillIdempotencyKey: "skill:sql-opt|sess:s1|step:0",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	assert.Equal(t, "skill:sql-opt|sess:s1|step:0", created.SkillIdempotencyKey)

	// Lookup by the key returns the same task.
	got, err := store.GetTaskByIdempotencyKey(ctx, "skill:sql-opt|sess:s1|step:0")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)

	// Lookup by a missing key returns (nil, nil), not an error.
	miss, err := store.GetTaskByIdempotencyKey(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, miss)

	// Empty key short-circuits.
	empty, err := store.GetTaskByIdempotencyKey(ctx, "")
	require.NoError(t, err)
	assert.Nil(t, empty)
}

func TestTask_IdempotencyKeyUniqueIndex(t *testing.T) {
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	key := "skill:x|sess:y|step:0"
	_, err := store.CreateTask(ctx, &task.Task{
		Title:               "first",
		Status:              loomv1.TaskStatus_TASK_STATUS_OPEN,
		SkillIdempotencyKey: key,
	})
	require.NoError(t, err)

	// A second insert with the same key must violate the partial unique index.
	_, err = store.CreateTask(ctx, &task.Task{
		Title:               "second",
		Status:              loomv1.TaskStatus_TASK_STATUS_OPEN,
		SkillIdempotencyKey: key,
	})
	require.Error(t, err, "second insert with same idempotency key must violate unique index")
	assert.Contains(t, err.Error(), "UNIQUE")
}

func TestTask_IdempotencyKeyEmptyAllowsMany(t *testing.T) {
	// Partial index covers only non-null keys; many tasks with empty key
	// must coexist without triggering the constraint.
	db := migratedDB(t)
	store := NewTaskStore(db, observability.NewNoOpTracer())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := store.CreateTask(ctx, &task.Task{
			Title:  "no-key",
			Status: loomv1.TaskStatus_TASK_STATUS_OPEN,
		})
		require.NoError(t, err, "tasks without an idempotency key must coexist freely")
	}
}
