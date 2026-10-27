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
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/checkpoint"
	"github.com/teradata-labs/loom/pkg/observability"
)

// CheckpointStore implements checkpoint.CheckpointStore using PostgreSQL.
type CheckpointStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewCheckpointStore creates a new PostgreSQL-backed checkpoint store.
func NewCheckpointStore(pool *pgxpool.Pool, tracer observability.Tracer) *CheckpointStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &CheckpointStore{pool: pool, tracer: tracer}
}

// SaveCheckpoint upserts ckpt, replacing any existing checkpoint for the same
// WorkflowId.
func (s *CheckpointStore) SaveCheckpoint(ctx context.Context, ckpt *loomv1.WorkflowCheckpoint) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_checkpoint_store.save")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("workflow_id", ckpt.GetWorkflowId())

	data, err := proto.Marshal(ckpt)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	err = execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			INSERT INTO workflow_checkpoints (workflow_id, user_id, checkpoint_version, checkpoint_proto, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (user_id, workflow_id) DO UPDATE
				SET checkpoint_version = EXCLUDED.checkpoint_version,
					checkpoint_proto = EXCLUDED.checkpoint_proto,
					updated_at = now()`,
			ckpt.GetWorkflowId(), userID, ckpt.GetCheckpointVersion(), data,
		)
		if err != nil {
			return fmt.Errorf("failed to save workflow checkpoint: %w", err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// LoadCheckpoint retrieves the checkpoint for workflowID, or (nil, nil) if
// none exists.
func (s *CheckpointStore) LoadCheckpoint(ctx context.Context, workflowID string) (*loomv1.WorkflowCheckpoint, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_checkpoint_store.load")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("workflow_id", workflowID)

	var data []byte
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		row := tx.QueryRow(ctx, `
			SELECT checkpoint_proto FROM workflow_checkpoints
			WHERE workflow_id = $1 AND user_id = $2`,
			workflowID, userID,
		)
		err := row.Scan(&data)
		if err == pgx.ErrNoRows {
			data = nil
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to load workflow checkpoint: %w", err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	if data == nil {
		return nil, nil
	}

	ckpt := &loomv1.WorkflowCheckpoint{}
	if err := proto.Unmarshal(data, ckpt); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to unmarshal workflow checkpoint: %w", err)
	}
	return ckpt, nil
}

// Compile-time check: CheckpointStore implements checkpoint.CheckpointStore.
var _ checkpoint.CheckpointStore = (*CheckpointStore)(nil)
