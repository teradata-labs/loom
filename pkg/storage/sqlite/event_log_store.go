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
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/observability"
)

// EventLogStore implements eventlog.EventLogStore using SQLite.
type EventLogStore struct {
	db     *sql.DB
	tracer observability.Tracer
}

// NewEventLogStore creates a new SQLite-backed execution event log store.
func NewEventLogStore(db *sql.DB, tracer observability.Tracer) *EventLogStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &EventLogStore{db: db, tracer: tracer}
}

// Compile-time interface check.
var _ eventlog.EventLogStore = (*EventLogStore)(nil)

func (s *EventLogStore) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.record_intent")
	defer s.tracer.EndSpan(span)
	return s.insertEvent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}

func (s *EventLogStore) RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.record_result")
	defer s.tracer.EndSpan(span)
	return s.insertEvent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}

func (s *EventLogStore) insertEvent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO execution_events (session_id, step_index, batch_position, event_type, tool_use_id, payload)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?)`,
		sessionID, stepIndex, batchPosition, string(eventType), toolUseID, string(payload),
	)
	if err != nil {
		return fmt.Errorf("record %s event: %w", eventType, err)
	}
	return nil
}

func (s *EventLogStore) LoadEvents(ctx context.Context, sessionID string) ([]eventlog.ExecutionEvent, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.load_events")
	defer s.tracer.EndSpan(span)

	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, step_index, batch_position, event_type, COALESCE(tool_use_id, ''), payload, created_at
		FROM execution_events
		WHERE session_id = ?
		ORDER BY step_index ASC, batch_position ASC, event_type ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []eventlog.ExecutionEvent
	for rows.Next() {
		var e eventlog.ExecutionEvent
		var eventType, payload, createdAt string
		if err := rows.Scan(&e.SessionID, &e.StepIndex, &e.BatchPosition, &eventType, &e.ToolUseID, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.EventType = eventlog.EventType(eventType)
		e.Payload = []byte(payload)
		e.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		// Also parse "2006-01-02 15:04:05" format used by SQLite datetime().
		if e.CreatedAt.IsZero() {
			e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *EventLogStore) NextStepIndex(ctx context.Context, sessionID string) (int, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.next_step_index")
	defer s.tracer.EndSpan(span)

	var maxStep sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(step_index) FROM execution_events WHERE session_id = ?`, sessionID).Scan(&maxStep)
	if err != nil {
		return 0, fmt.Errorf("next step index: %w", err)
	}
	if !maxStep.Valid {
		return 0, nil
	}
	return int(maxStep.Int64) + 1, nil
}

func (s *EventLogStore) ClaimSessionResume(ctx context.Context, sessionID, claimToken string, leaseTTL time.Duration) (bool, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.claim_session_resume")
	defer s.tracer.EndSpan(span)

	// resume_claimed_at stores the lease's *expiry* instant (now + leaseTTL),
	// not the claim instant. Storing the claim instant and re-deriving
	// staleness from the reclaiming caller's own leaseTTL (cutoff = now -
	// leaseTTL) breaks when a claim was originally taken with a different
	// TTL than the one a later caller passes (e.g. a short-lived claim
	// reclaimed by a caller using the default TTL): the reclaimer's cutoff
	// would not reach far enough back to see the original claim as stale
	// even though its own lease already expired. Storing the expiry instant
	// makes staleness a simple "has this already passed" check that doesn't
	// depend on which TTL the reclaiming caller happens to use.
	now := time.Now()
	expiresAt := now.Add(leaseTTL).Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET resume_claimed_by = ?, resume_claim_token = ?, resume_claimed_at = ?
		WHERE id = ?
		  AND (resume_claimed_by IS NULL OR resume_claimed_at < ?)`,
		claimToken, claimToken, expiresAt, sessionID, nowStr,
	)
	if err != nil {
		return false, fmt.Errorf("claim session resume: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim session resume: rows affected: %w", err)
	}
	return affected == 1, nil
}

func (s *EventLogStore) ReleaseSessionResume(ctx context.Context, sessionID, claimToken string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.release_session_resume")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET resume_claimed_by = NULL, resume_claim_token = NULL, resume_claimed_at = NULL
		WHERE id = ? AND resume_claim_token = ?`,
		sessionID, claimToken,
	)
	if err != nil {
		return fmt.Errorf("release session resume: %w", err)
	}
	return nil
}

func (s *EventLogStore) PruneResolvedStep(ctx context.Context, sessionID string, stepIndex int) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.prune_resolved_step")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx, `DELETE FROM execution_events WHERE session_id = ? AND step_index = ?`, sessionID, stepIndex)
	if err != nil {
		return fmt.Errorf("prune resolved step: %w", err)
	}
	return nil
}
