// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package checkpoint defines the CheckpointStore interface for persisting
// and retrieving WorkflowCheckpoint records. It is a leaf package with no
// internal loom imports beyond the generated proto, mirroring pkg/task's
// TaskStore interface, so storage backends can implement it without pulling
// in heavier packages (in particular pkg/orchestration, which itself depends
// on pkg/agent).
package checkpoint

import (
	"context"
	"errors"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ErrWorkflowIDRequired is returned by every CheckpointStore method given an
// empty workflow ID. A checkpoint stored under "" could never be looked up
// again, so the stores refuse it instead of writing an unreachable row.
var ErrWorkflowIDRequired = errors.New("checkpoint: workflow ID is required")

// CheckpointStore persists and retrieves WorkflowCheckpoint records keyed by
// (caller, workflow ID). It gives stage-boundary HITL-gate resume (see
// hitl_gate.go, resume.go in pkg/orchestration) a durable copy beyond the
// CLI's local checkpoint file: the CLI writes both, reads the file first when
// given one, and reads the store when given a workflow ID instead
// (cmd/looms/cmd_workflow.go).
//
// Checkpoints belong to the caller identity in ctx (types.UserIDFromContext).
// Each caller sees only its own, and two callers may use the same workflow ID
// independently, since workflow IDs are often set by hand in the YAML.
//
// A checkpoint is keyed by workflow ID alone within a caller, so two
// suspended runs of one caller that share a workflow ID (a fixed workflow_id
// in the YAML) overwrite each other: the last SaveCheckpoint wins and the
// earlier run can no longer be resumed from the store. Generated IDs
// ("<pattern>-<8 hex>") do not collide in practice.
//
// A stored checkpoint stays resumable until DeleteCheckpoint removes it. The
// resume path calls it once the workflow completes or is rejected, so a
// finished workflow cannot be resumed a second time from the store.
//
// LoadCheckpoint returns (nil, nil) for a workflow ID with no saved
// checkpoint, matching the no-error-on-absence convention used elsewhere in
// this codebase (e.g. shuttle.HumanRequestStore.Get).
type CheckpointStore interface {
	// SaveCheckpoint persists ckpt, replacing any checkpoint the caller already
	// holds for the same WorkflowId (a workflow has at most one live
	// checkpoint at a time; a new suspension supersedes the last one). It
	// returns ErrWorkflowIDRequired for a nil checkpoint or an empty
	// WorkflowId.
	SaveCheckpoint(ctx context.Context, ckpt *loomv1.WorkflowCheckpoint) error

	// LoadCheckpoint retrieves the caller's checkpoint for workflowID, or
	// (nil, nil) if the caller has none. It returns ErrWorkflowIDRequired for
	// an empty workflowID.
	LoadCheckpoint(ctx context.Context, workflowID string) (*loomv1.WorkflowCheckpoint, error)

	// DeleteCheckpoint removes the caller's checkpoint for workflowID. Deleting
	// a checkpoint the caller does not hold is not an error, so a retried
	// delete is safe. It returns ErrWorkflowIDRequired for an empty
	// workflowID.
	DeleteCheckpoint(ctx context.Context, workflowID string) error
}
