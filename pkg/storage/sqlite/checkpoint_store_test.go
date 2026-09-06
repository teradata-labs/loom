// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sqlite

import (
	"context"
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
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
