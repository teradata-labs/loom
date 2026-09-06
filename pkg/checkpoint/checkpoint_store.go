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
// workflow ID. It gives stage-boundary HITL-gate resume (see hitl_gate.go,
// resume.go in pkg/orchestration) a durable home beyond a caller-managed
// local file: the CLI's checkpoint file (cmd/looms/cmd_workflow.go) remains
// a fallback for environments with no StorageBackend, not the primary
// persistence mechanism.
//
// LoadCheckpoint returns (nil, nil) for a workflow ID with no saved
// checkpoint, matching the no-error-on-absence convention used elsewhere in
// this codebase (e.g. shuttle.HumanRequestStore.Get).
type CheckpointStore interface {
	// SaveCheckpoint persists ckpt, replacing any existing checkpoint for the
	// same WorkflowId (a workflow has at most one live checkpoint at a time —
	// a new suspension supersedes the last one).
	SaveCheckpoint(ctx context.Context, ckpt *loomv1.WorkflowCheckpoint) error

	// LoadCheckpoint retrieves the checkpoint for workflowID, or (nil, nil) if
	// none exists.
	LoadCheckpoint(ctx context.Context, workflowID string) (*loomv1.WorkflowCheckpoint, error)
}
