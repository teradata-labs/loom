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

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// CheckpointStore persists and retrieves WorkflowCheckpoint records keyed by
// (caller, workflow ID). It gives stage-boundary HITL-gate resume (see
// hitl_gate.go, resume.go in pkg/orchestration) a durable copy beyond the
// CLI's local checkpoint file: the CLI writes both, reads the file first, and
// falls back to the store when the file is gone (cmd/looms/cmd_workflow.go).
//
// Checkpoints belong to the caller identity in ctx (types.UserIDFromContext).
// Each caller sees only its own, and two callers may use the same workflow ID
// independently, since workflow IDs are often set by hand in the YAML.
//
// LoadCheckpoint returns (nil, nil) for a workflow ID with no saved
// checkpoint, matching the no-error-on-absence convention used elsewhere in
// this codebase (e.g. shuttle.HumanRequestStore.Get).
type CheckpointStore interface {
	// SaveCheckpoint persists ckpt, replacing any checkpoint the caller already
	// holds for the same WorkflowId (a workflow has at most one live
	// checkpoint at a time; a new suspension supersedes the last one).
	SaveCheckpoint(ctx context.Context, ckpt *loomv1.WorkflowCheckpoint) error

	// LoadCheckpoint retrieves the caller's checkpoint for workflowID, or
	// (nil, nil) if the caller has none.
	LoadCheckpoint(ctx context.Context, workflowID string) (*loomv1.WorkflowCheckpoint, error)
}
