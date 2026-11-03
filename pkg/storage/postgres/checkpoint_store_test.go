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
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/checkpoint"
	"github.com/teradata-labs/loom/pkg/observability"
)

// This mirrors the guard in human_request_store_test.go's
// testHumanRequestStore: these tests run in the ORDINARY gate (no build
// tag); CI provides a postgres service and sets TEST_POSTGRES_URL, a local
// run without one skips.

func testCheckpointStore(t *testing.T) (*CheckpointStore, context.Context, string) {
	t.Helper()
	pool := migratedAdminPool(t)
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

// TestCheckpointStore_TenantIsolation_RLSSubject runs the store as a role that
// row-level security applies to (see rlsSubjectPool). Two tenants use the same
// workflow ID, which is common because the ID is often set by hand in the
// workflow YAML: each must see only its own row, and one tenant's save must
// not touch the other's.
func TestCheckpointStore_TenantIsolation_RLSSubject(t *testing.T) {
	adminPool := migratedAdminPool(t)
	appPool := rlsSubjectPool(t, adminPool)
	store := NewCheckpointStore(appPool, observability.NewNoOpTracer())

	userA, userB := hrUniqueID("ckpt-a"), hrUniqueID("ckpt-b")
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(),
			"DELETE FROM workflow_checkpoints WHERE user_id = ANY($1)", []string{userA, userB})
	})
	ctxA := ContextWithUserID(context.Background(), userA)
	ctxB := ContextWithUserID(context.Background(), userB)

	require.NoError(t, store.SaveCheckpoint(ctxA, &loomv1.WorkflowCheckpoint{WorkflowId: "shared-id", NextStageIndex: 1}))

	got, err := store.LoadCheckpoint(ctxB, "shared-id")
	require.NoError(t, err)
	require.Nil(t, got, "user B read user A's checkpoint")

	require.NoError(t, store.SaveCheckpoint(ctxB, &loomv1.WorkflowCheckpoint{WorkflowId: "shared-id", NextStageIndex: 2}),
		"user B's save of a workflow ID user A also uses must not collide with A's row")

	gotA, err := store.LoadCheckpoint(ctxA, "shared-id")
	require.NoError(t, err)
	require.NotNil(t, gotA)
	require.EqualValues(t, 1, gotA.NextStageIndex, "user B's save replaced user A's checkpoint")

	gotB, err := store.LoadCheckpoint(ctxB, "shared-id")
	require.NoError(t, err)
	require.NotNil(t, gotB)
	require.EqualValues(t, 2, gotB.NextStageIndex)

	// The policy, not only the store's WHERE clause, must hide A's row: an
	// unfiltered read as B sees exactly one row.
	var visible int
	require.NoError(t, execInTx(ctxB, appPool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM workflow_checkpoints WHERE workflow_id = 'shared-id'").Scan(&visible)
	}))
	require.Equal(t, 1, visible, "row-level security let user B see another tenant's checkpoint")
}

func TestWorkflowCheckpoints_RLSForced(t *testing.T) {
	requireForcedRLS(t, migratedAdminPool(t), "workflow_checkpoints")
}

func TestCheckpointStore_Delete(t *testing.T) {
	store, ctx, _ := testCheckpointStore(t)

	require.NoError(t, store.SaveCheckpoint(ctx, &loomv1.WorkflowCheckpoint{WorkflowId: "wf-delete"}))
	require.NoError(t, store.DeleteCheckpoint(ctx, "wf-delete"))

	got, err := store.LoadCheckpoint(ctx, "wf-delete")
	require.NoError(t, err)
	require.Nil(t, got, "checkpoint still loadable after delete")

	require.NoError(t, store.DeleteCheckpoint(ctx, "wf-delete"),
		"deleting a checkpoint that is already gone must not be an error")
}

// TestCheckpointStore_DeleteIsTenantScoped_RLSSubject checks, as a role that
// row-level security applies to, that one tenant deleting a workflow ID leaves
// another tenant's checkpoint under the same ID alone.
func TestCheckpointStore_DeleteIsTenantScoped_RLSSubject(t *testing.T) {
	adminPool := migratedAdminPool(t)
	store := NewCheckpointStore(rlsSubjectPool(t, adminPool), observability.NewNoOpTracer())

	userA, userB := hrUniqueID("ckpt-a"), hrUniqueID("ckpt-b")
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(),
			"DELETE FROM workflow_checkpoints WHERE user_id = ANY($1)", []string{userA, userB})
	})
	ctxA := ContextWithUserID(context.Background(), userA)
	ctxB := ContextWithUserID(context.Background(), userB)

	require.NoError(t, store.SaveCheckpoint(ctxA, &loomv1.WorkflowCheckpoint{WorkflowId: "shared-id", NextStageIndex: 1}))
	require.NoError(t, store.SaveCheckpoint(ctxB, &loomv1.WorkflowCheckpoint{WorkflowId: "shared-id", NextStageIndex: 2}))

	require.NoError(t, store.DeleteCheckpoint(ctxA, "shared-id"))

	gotA, err := store.LoadCheckpoint(ctxA, "shared-id")
	require.NoError(t, err)
	require.Nil(t, gotA, "user A's checkpoint survived A's delete")
	gotB, err := store.LoadCheckpoint(ctxB, "shared-id")
	require.NoError(t, err)
	require.NotNil(t, gotB, "user A's delete removed user B's checkpoint")
	require.EqualValues(t, 2, gotB.NextStageIndex)
}

// TestCheckpointStore_RejectsEmptyWorkflowID checks that a checkpoint with no
// workflow ID is refused instead of stored under a key nothing can look up.
func TestCheckpointStore_RejectsEmptyWorkflowID(t *testing.T) {
	store, ctx, _ := testCheckpointStore(t)

	require.ErrorIs(t, store.SaveCheckpoint(ctx, &loomv1.WorkflowCheckpoint{}), checkpoint.ErrWorkflowIDRequired)
	require.ErrorIs(t, store.SaveCheckpoint(ctx, nil), checkpoint.ErrWorkflowIDRequired)
	_, err := store.LoadCheckpoint(ctx, "")
	require.ErrorIs(t, err, checkpoint.ErrWorkflowIDRequired)
	require.ErrorIs(t, store.DeleteCheckpoint(ctx, ""), checkpoint.ErrWorkflowIDRequired)
}
