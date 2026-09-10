// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/taskctx"
)

// TestMessages_TaskIDStampsAndRoundTrips_Postgres pins both halves of
// messages.task_id on Postgres. Round 5 added the stamp; the read-back did not
// exist until round 6, so between the two the column was write-only here and
// every Message.TaskID read back empty while SQLite populated it — and the
// architecture doc claimed both backends were complete. Runs in the ordinary
// gate: CI sets TEST_POSTGRES_URL, a local run without one skips.
func TestMessages_TaskIDStampsAndRoundTrips_Postgres(t *testing.T) {
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

	store := NewSessionStore(pool, observability.NewNoOpTracer(), zap.NewNop())
	userID := hrUniqueID("msg-user")
	sessionID := hrUniqueID("msg-sess")
	_, err = pool.Exec(ctx, "INSERT INTO sessions (id, agent_id) VALUES ($1, 'agent-1')", sessionID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM messages WHERE user_id = $1", userID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM sessions WHERE id = $1", sessionID)
	})
	userCtx := ContextWithUserID(ctx, userID)

	// Explicit wins; ambient fills in; absent stays empty — the same three
	// cases the human-request store pins, on the table that carries the bulk
	// of the timeline.
	require.NoError(t, store.SaveMessage(userCtx, sessionID,
		&agent.Message{Role: "user", Content: "explicit", TaskID: "task-explicit"}, true))
	ambient := taskctx.ContextWithAttribution(userCtx, taskctx.Attribution{TaskID: "task-ambient", SessionID: sessionID})
	require.NoError(t, store.SaveMessage(ambient, sessionID,
		&agent.Message{Role: "assistant", Content: "ambient"}, false))
	require.NoError(t, store.SaveMessage(userCtx, sessionID,
		&agent.Message{Role: "assistant", Content: "unattributed"}, false))

	msgs, err := store.LoadMessages(userCtx, sessionID)
	require.NoError(t, err, "LoadMessages must scan every selected column, task_id included")
	require.Len(t, msgs, 3)
	require.Equal(t, "task-explicit", msgs[0].TaskID, "an explicit TaskID round-trips")
	require.Equal(t, "task-ambient", msgs[1].TaskID, "ambient attribution is stamped and read back")
	require.Empty(t, msgs[2].TaskID, "no attribution reads back as empty, never as an error")

	// The other read paths share the projection; a scoped range read must carry it too.
	ranged, err := store.ListMessagesBySeqRange(userCtx, sessionID, 1, 1<<62)
	require.NoError(t, err)
	require.Len(t, ranged, 3)
	require.Equal(t, "task-explicit", ranged[0].TaskID)
}
