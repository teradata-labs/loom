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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// This mirrors the guard in human_request_store_test.go's
// testHumanRequestStore: these tests run in the ORDINARY gate (no build
// tag); CI provides a postgres service and sets TEST_POSTGRES_URL, a local
// run without one skips.

func testCheckpointStore(t *testing.T) (*CheckpointStore, context.Context, string) {
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

	store := NewCheckpointStore(pool, observability.NewNoOpTracer())
	userID := hrUniqueID("ckpt-user")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM workflow_checkpoints WHERE user_id = $1", userID)
	})
	return store, ContextWithUserID(context.Background(), userID), userID
}

func TestCheckpointStore_SaveAndLoad(t *testing.T) {
	store, ctx, _ := testCheckpointStore(t)

	ckpt := &loomv1.WorkflowCheckpoint{
		WorkflowId:        "wf-checkpoint-test",
		CheckpointVersion: 1,
		NextStageIndex:    3,
	}

	if err := store.SaveCheckpoint(ctx, ckpt); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	got, err := store.LoadCheckpoint(ctx, "wf-checkpoint-test")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got == nil {
		t.Fatal("LoadCheckpoint: got nil, want the saved checkpoint")
	}
	if got.WorkflowId != "wf-checkpoint-test" || got.NextStageIndex != 3 {
		t.Fatalf("LoadCheckpoint: got %+v, want workflow_id=wf-checkpoint-test next_stage_index=3", got)
	}

	// A second save for the same workflow_id replaces the row (upsert), not
	// a duplicate — this is the "at most one live checkpoint per workflow"
	// invariant the interface doc promises.
	ckpt.NextStageIndex = 5
	if err := store.SaveCheckpoint(ctx, ckpt); err != nil {
		t.Fatalf("SaveCheckpoint (replace): %v", err)
	}
	got2, err := store.LoadCheckpoint(ctx, "wf-checkpoint-test")
	if err != nil {
		t.Fatalf("LoadCheckpoint (after replace): %v", err)
	}
	if got2.NextStageIndex != 5 {
		t.Fatalf("LoadCheckpoint (after replace): got next_stage_index=%d, want 5", got2.NextStageIndex)
	}
}

func TestCheckpointStore_LoadMissing_ReturnsNilNil(t *testing.T) {
	store, ctx, _ := testCheckpointStore(t)

	got, err := store.LoadCheckpoint(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("LoadCheckpoint: got error %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("LoadCheckpoint: got %+v, want nil", got)
	}
}
