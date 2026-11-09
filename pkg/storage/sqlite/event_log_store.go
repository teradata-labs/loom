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
	"github.com/teradata-labs/loom/pkg/types"
)

// EventLogStore implements eventlog.EventLogStore using SQLite. SQLite has no
// row-level security, so every query filters on the caller's user ID
// (types.UserIDOrDefault), the rule SQLite sessions use.
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

// secondsPerDay converts a TTL to the julianday() units execution_leases uses.
const secondsPerDay = 86400.0

func (s *EventLogStore) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.record_intent")
	defer s.tracer.EndSpan(span)
	return recordSpanError(span, s.insertEvent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload))
}

func (s *EventLogStore) RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.record_result")
	defer s.tracer.EndSpan(span)
	return recordSpanError(span, s.insertEvent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload))
}

// insertEvent is shared by RecordIntent and RecordResult. On a key conflict it
// compares payloads: the same payload is a retry of this write (no-op), a
// different one is eventlog.ErrEventConflict.
func (s *EventLogStore) insertEvent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	userID := types.UserIDOrDefault(ctx)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO execution_events (user_id, session_id, step_index, batch_position, event_type, tool_use_id, payload)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?)
		ON CONFLICT (user_id, session_id, step_index, batch_position, event_type) DO NOTHING`,
		userID, sessionID, stepIndex, batchPosition, string(eventType), toolUseID, string(payload),
	)
	if err != nil {
		return fmt.Errorf("record %s event (step %d, position %d): %w", eventType, stepIndex, batchPosition, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("record %s event: rows affected: %w", eventType, err)
	} else if n == 1 {
		return nil
	}

	var existing string
	err = s.db.QueryRowContext(ctx, `
		SELECT payload FROM execution_events
		WHERE user_id = ? AND session_id = ? AND step_index = ? AND batch_position = ? AND event_type = ?`,
		userID, sessionID, stepIndex, batchPosition, string(eventType),
	).Scan(&existing)
	if err != nil {
		return fmt.Errorf("record %s event: reading the conflicting row: %w", eventType, err)
	}
	if existing != string(payload) {
		return fmt.Errorf("record %s event (step %d, position %d): %w", eventType, stepIndex, batchPosition, eventlog.ErrEventConflict)
	}
	return nil
}

func (s *EventLogStore) LoadEvents(ctx context.Context, sessionID string) ([]eventlog.ExecutionEvent, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.load_events")
	defer s.tracer.EndSpan(span)

	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, step_index, batch_position, event_type, COALESCE(tool_use_id, ''), payload, created_at
		FROM execution_events
		WHERE user_id = ? AND session_id = ?
		ORDER BY step_index ASC, batch_position ASC, event_type ASC`,
		types.UserIDOrDefault(ctx), sessionID,
	)
	if err != nil {
		return nil, recordSpanError(span, fmt.Errorf("load events: %w", err))
	}
	defer func() { _ = rows.Close() }()

	var out []eventlog.ExecutionEvent
	for rows.Next() {
		var e eventlog.ExecutionEvent
		var eventType, payload, createdAt string
		if err := rows.Scan(&e.SessionID, &e.StepIndex, &e.BatchPosition, &eventType, &e.ToolUseID, &payload, &createdAt); err != nil {
			return nil, recordSpanError(span, fmt.Errorf("scan event: %w", err))
		}
		e.EventType = eventlog.EventType(eventType)
		e.Payload = []byte(payload)
		// created_at is written by strftime('%Y-%m-%dT%H:%M:%fZ'), which is
		// RFC 3339 in UTC with milliseconds.
		if e.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, recordSpanError(span, fmt.Errorf("parse created_at %q: %w", createdAt, err))
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, recordSpanError(span, fmt.Errorf("load events: %w", err))
	}
	return out, nil
}

func (s *EventLogStore) NextStepIndex(ctx context.Context, sessionID string) (int, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.next_step_index")
	defer s.tracer.EndSpan(span)

	var maxStep sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(step_index) FROM execution_events WHERE user_id = ? AND session_id = ?`,
		types.UserIDOrDefault(ctx), sessionID,
	).Scan(&maxStep)
	if err != nil {
		return 0, recordSpanError(span, fmt.Errorf("next step index: %w", err))
	}
	if !maxStep.Valid {
		return 0, nil
	}
	return int(maxStep.Int64) + 1, nil
}

// AcquireSessionLease inserts the lease, or takes over one that has expired
// or that holder already holds. Expiry is a julianday() value on the database
// clock.
func (s *EventLogStore) AcquireSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.acquire_session_lease")
	defer s.tracer.EndSpan(span)

	won, err := s.execLease(ctx, `
		INSERT INTO execution_leases (user_id, session_id, holder, expires_at)
		VALUES (?1, ?2, ?3, julianday('now') + ?4)
		ON CONFLICT (user_id, session_id) DO UPDATE
			SET holder = excluded.holder, expires_at = excluded.expires_at
			WHERE execution_leases.expires_at < julianday('now') OR execution_leases.holder = excluded.holder`,
		sessionID, holder, ttl)
	if err != nil {
		return false, recordSpanError(span, fmt.Errorf("acquire session lease: %w", err))
	}
	return won, nil
}

func (s *EventLogStore) RenewSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.renew_session_lease")
	defer s.tracer.EndSpan(span)

	held, err := s.execLease(ctx, `
		UPDATE execution_leases SET expires_at = julianday('now') + ?4
		WHERE user_id = ?1 AND session_id = ?2 AND holder = ?3`,
		sessionID, holder, ttl)
	if err != nil {
		return false, recordSpanError(span, fmt.Errorf("renew session lease: %w", err))
	}
	return held, nil
}

// execLease runs a lease statement taking (?1 user, ?2 session, ?3 holder,
// ?4 ttl in days) and reports whether it changed exactly one row.
func (s *EventLogStore) execLease(ctx context.Context, query, sessionID, holder string, ttl time.Duration) (bool, error) {
	res, err := s.db.ExecContext(ctx, query,
		types.UserIDOrDefault(ctx), sessionID, holder, ttl.Seconds()/secondsPerDay)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *EventLogStore) ReleaseSessionLease(ctx context.Context, sessionID, holder string) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.release_session_lease")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM execution_leases WHERE user_id = ? AND session_id = ? AND holder = ?`,
		types.UserIDOrDefault(ctx), sessionID, holder,
	)
	if err != nil {
		return recordSpanError(span, fmt.Errorf("release session lease: %w", err))
	}
	return nil
}

func (s *EventLogStore) PruneResolvedStep(ctx context.Context, sessionID string, stepIndex int) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.prune_resolved_step")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM execution_events WHERE user_id = ? AND session_id = ? AND step_index = ?`,
		types.UserIDOrDefault(ctx), sessionID, stepIndex)
	if err != nil {
		return recordSpanError(span, fmt.Errorf("prune resolved step: %w", err))
	}
	return nil
}

func (s *EventLogStore) PruneStepsBefore(ctx context.Context, sessionID string, stepIndex int) error {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.event_log.prune_steps_before")
	defer s.tracer.EndSpan(span)

	_, err := s.db.ExecContext(ctx,
		`DELETE FROM execution_events WHERE user_id = ? AND session_id = ? AND step_index < ?`,
		types.UserIDOrDefault(ctx), sessionID, stepIndex)
	if err != nil {
		return recordSpanError(span, fmt.Errorf("prune steps before: %w", err))
	}
	return nil
}

// recordSpanError records a non-nil err on span and returns err unchanged.
func recordSpanError(span *observability.Span, err error) error {
	if err != nil {
		span.RecordError(err)
	}
	return err
}
