// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sqlite

import (
	"context"
	"errors"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/checkpoint"
	"github.com/teradata-labs/loom/pkg/types"
)

// migratedDB (task_idempotency_test.go) already imports the real
// "sqlite3" driver registration (internal/sqlitedriver), so this file
// does not need its own blank driver import.

func TestCheckpointStore_SaveAndLoad(t *testing.T) {
	db := migratedDB(t)
	store := NewCheckpointStore(db, nil)
	ctx := context.Background()

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
	if got == nil || got.WorkflowId != "wf-checkpoint-test" || got.NextStageIndex != 3 {
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
	db := migratedDB(t)
	store := NewCheckpointStore(db, nil)

	got, err := store.LoadCheckpoint(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("LoadCheckpoint: got error %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("LoadCheckpoint: got %+v, want nil", got)
	}
}

// TestCheckpointStore_TenantIsolation covers two tenants that use the same
// workflow ID, which is common because the ID is often set by hand in the
// workflow YAML. Each tenant must read only its own checkpoint, and one
// tenant's save must not replace the other's.
func TestCheckpointStore_TenantIsolation(t *testing.T) {
	db := migratedDB(t)
	store := NewCheckpointStore(db, nil)
	ctxA := types.ContextWithUserID(context.Background(), "user-a")
	ctxB := types.ContextWithUserID(context.Background(), "user-b")

	if err := store.SaveCheckpoint(ctxA, &loomv1.WorkflowCheckpoint{WorkflowId: "shared-id", NextStageIndex: 1}); err != nil {
		t.Fatalf("SaveCheckpoint(A): %v", err)
	}

	got, err := store.LoadCheckpoint(ctxB, "shared-id")
	if err != nil {
		t.Fatalf("LoadCheckpoint(B, before B saves): %v", err)
	}
	if got != nil {
		t.Fatalf("LoadCheckpoint(B): got user A's checkpoint %+v, want nil", got)
	}

	if err := store.SaveCheckpoint(ctxB, &loomv1.WorkflowCheckpoint{WorkflowId: "shared-id", NextStageIndex: 2}); err != nil {
		t.Fatalf("SaveCheckpoint(B): %v", err)
	}

	gotA, err := store.LoadCheckpoint(ctxA, "shared-id")
	if err != nil {
		t.Fatalf("LoadCheckpoint(A): %v", err)
	}
	if gotA == nil || gotA.NextStageIndex != 1 {
		t.Fatalf("LoadCheckpoint(A): got %+v, want next_stage_index=1 (B's save must not replace A's row)", gotA)
	}
	gotB, err := store.LoadCheckpoint(ctxB, "shared-id")
	if err != nil {
		t.Fatalf("LoadCheckpoint(B): %v", err)
	}
	if gotB == nil || gotB.NextStageIndex != 2 {
		t.Fatalf("LoadCheckpoint(B): got %+v, want next_stage_index=2", gotB)
	}
}

// TestCheckpointStore_NoIdentityUsesDefaultUser checks that a context with no
// caller identity reads and writes as types.DefaultUserID, the same owner
// SQLite sessions use, so a local no-auth deployment keeps working.
func TestCheckpointStore_NoIdentityUsesDefaultUser(t *testing.T) {
	db := migratedDB(t)
	store := NewCheckpointStore(db, nil)

	if err := store.SaveCheckpoint(context.Background(), &loomv1.WorkflowCheckpoint{WorkflowId: "wf-default"}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	got, err := store.LoadCheckpoint(types.ContextWithUserID(context.Background(), types.DefaultUserID), "wf-default")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got == nil {
		t.Fatal("LoadCheckpoint as DefaultUserID: got nil, want the checkpoint saved without an identity")
	}
}

func TestCheckpointStore_Delete(t *testing.T) {
	db := migratedDB(t)
	store := NewCheckpointStore(db, nil)
	ctx := context.Background()

	if err := store.SaveCheckpoint(ctx, &loomv1.WorkflowCheckpoint{WorkflowId: "wf-delete"}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	if err := store.DeleteCheckpoint(ctx, "wf-delete"); err != nil {
		t.Fatalf("DeleteCheckpoint: %v", err)
	}
	got, err := store.LoadCheckpoint(ctx, "wf-delete")
	if err != nil {
		t.Fatalf("LoadCheckpoint after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("LoadCheckpoint after delete: got %+v, want nil", got)
	}

	// A retried delete, or a delete of an ID that was never saved, is not an error.
	if err := store.DeleteCheckpoint(ctx, "wf-delete"); err != nil {
		t.Fatalf("DeleteCheckpoint (already gone): %v", err)
	}
}

// TestCheckpointStore_DeleteIsTenantScoped checks that one tenant deleting a
// workflow ID leaves another tenant's checkpoint under the same ID alone.
func TestCheckpointStore_DeleteIsTenantScoped(t *testing.T) {
	db := migratedDB(t)
	store := NewCheckpointStore(db, nil)
	ctxA := types.ContextWithUserID(context.Background(), "user-a")
	ctxB := types.ContextWithUserID(context.Background(), "user-b")

	for _, ctx := range []context.Context{ctxA, ctxB} {
		if err := store.SaveCheckpoint(ctx, &loomv1.WorkflowCheckpoint{WorkflowId: "shared-id"}); err != nil {
			t.Fatalf("SaveCheckpoint: %v", err)
		}
	}
	if err := store.DeleteCheckpoint(ctxA, "shared-id"); err != nil {
		t.Fatalf("DeleteCheckpoint(A): %v", err)
	}

	if got, err := store.LoadCheckpoint(ctxA, "shared-id"); err != nil || got != nil {
		t.Fatalf("LoadCheckpoint(A) after A's delete: got (%+v, %v), want (nil, nil)", got, err)
	}
	if got, err := store.LoadCheckpoint(ctxB, "shared-id"); err != nil || got == nil {
		t.Fatalf("LoadCheckpoint(B) after A's delete: got (%+v, %v), want B's checkpoint", got, err)
	}
}

// TestCheckpointStore_RejectsEmptyWorkflowID checks that a checkpoint with no
// workflow ID is refused instead of stored under a key nothing can look up.
func TestCheckpointStore_RejectsEmptyWorkflowID(t *testing.T) {
	db := migratedDB(t)
	store := NewCheckpointStore(db, nil)
	ctx := context.Background()

	if err := store.SaveCheckpoint(ctx, &loomv1.WorkflowCheckpoint{}); !errors.Is(err, checkpoint.ErrWorkflowIDRequired) {
		t.Fatalf("SaveCheckpoint(empty ID): got %v, want ErrWorkflowIDRequired", err)
	}
	if err := store.SaveCheckpoint(ctx, nil); !errors.Is(err, checkpoint.ErrWorkflowIDRequired) {
		t.Fatalf("SaveCheckpoint(nil): got %v, want ErrWorkflowIDRequired", err)
	}
	if _, err := store.LoadCheckpoint(ctx, ""); !errors.Is(err, checkpoint.ErrWorkflowIDRequired) {
		t.Fatalf("LoadCheckpoint(empty ID): got %v, want ErrWorkflowIDRequired", err)
	}
	if err := store.DeleteCheckpoint(ctx, ""); !errors.Is(err, checkpoint.ErrWorkflowIDRequired) {
		t.Fatalf("DeleteCheckpoint(empty ID): got %v, want ErrWorkflowIDRequired", err)
	}

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_checkpoints`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("workflow_checkpoints has %d rows after refused saves, want 0", rows)
	}
}
