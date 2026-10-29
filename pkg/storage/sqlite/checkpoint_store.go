// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/checkpoint"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/types"
)

// CheckpointStore implements checkpoint.CheckpointStore for SQLite.
type CheckpointStore struct {
	db     *sql.DB
	tracer observability.Tracer
}

// NewCheckpointStore creates a new SQLite-backed checkpoint store.
func NewCheckpointStore(db *sql.DB, tracer observability.Tracer) *CheckpointStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &CheckpointStore{db: db, tracer: tracer}
}

// SaveCheckpoint upserts ckpt, replacing any existing checkpoint the caller
// (types.UserIDOrDefault) holds for the same WorkflowId.
func (s *CheckpointStore) SaveCheckpoint(ctx context.Context, ckpt *loomv1.WorkflowCheckpoint) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.checkpoint.save")
	defer s.tracer.EndSpan(span)

	if ckpt.GetWorkflowId() == "" {
		return checkpoint.ErrWorkflowIDRequired
	}
	data, err := proto.Marshal(ckpt)
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_checkpoints (user_id, workflow_id, checkpoint_version, checkpoint_proto, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(user_id, workflow_id) DO UPDATE SET
			checkpoint_version = excluded.checkpoint_version,
			checkpoint_proto = excluded.checkpoint_proto,
			updated_at = excluded.updated_at`,
		types.UserIDOrDefault(ctx), ckpt.GetWorkflowId(), ckpt.GetCheckpointVersion(), data,
	)
	if err != nil {
		return fmt.Errorf("failed to save workflow checkpoint: %w", err)
	}
	return nil
}

// LoadCheckpoint retrieves the caller's checkpoint for workflowID, or
// (nil, nil) if the caller has none.
func (s *CheckpointStore) LoadCheckpoint(ctx context.Context, workflowID string) (*loomv1.WorkflowCheckpoint, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.checkpoint.load")
	defer s.tracer.EndSpan(span)

	if workflowID == "" {
		return nil, checkpoint.ErrWorkflowIDRequired
	}
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT checkpoint_proto FROM workflow_checkpoints WHERE user_id = ? AND workflow_id = ?`,
		types.UserIDOrDefault(ctx), workflowID,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load workflow checkpoint: %w", err)
	}

	ckpt := &loomv1.WorkflowCheckpoint{}
	if err := proto.Unmarshal(data, ckpt); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workflow checkpoint: %w", err)
	}
	return ckpt, nil
}

// DeleteCheckpoint removes the caller's checkpoint for workflowID. It is not an
// error if the caller has none.
func (s *CheckpointStore) DeleteCheckpoint(ctx context.Context, workflowID string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.checkpoint.delete")
	defer s.tracer.EndSpan(span)

	if workflowID == "" {
		return checkpoint.ErrWorkflowIDRequired
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM workflow_checkpoints WHERE user_id = ? AND workflow_id = ?`,
		types.UserIDOrDefault(ctx), workflowID,
	); err != nil {
		return fmt.Errorf("failed to delete workflow checkpoint: %w", err)
	}
	return nil
}

// Compile-time check: CheckpointStore implements checkpoint.CheckpointStore.
var _ checkpoint.CheckpointStore = (*CheckpointStore)(nil)
