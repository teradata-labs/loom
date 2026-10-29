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
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/eventlog"
	"github.com/teradata-labs/loom/pkg/observability"
)

// EventLogStore implements eventlog.EventLogStore using PostgreSQL. Every
// query runs through execInTx, so app.current_user_id is set and the
// execution_events / execution_leases RLS policies apply. Queries also filter
// on user_id explicitly, so isolation does not depend on RLS alone.
type EventLogStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewEventLogStore creates a new PostgreSQL-backed execution event log store.
func NewEventLogStore(pool *pgxpool.Pool, tracer observability.Tracer) *EventLogStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &EventLogStore{pool: pool, tracer: tracer}
}

// Compile-time interface check.
var _ eventlog.EventLogStore = (*EventLogStore)(nil)

func (s *EventLogStore) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.record_intent")
	defer s.tracer.EndSpan(span)
	return recordSpanError(span, s.insertEvent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload))
}

func (s *EventLogStore) RecordResult(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.record_result")
	defer s.tracer.EndSpan(span)
	return recordSpanError(span, s.insertEvent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload))
}

// insertEvent is shared by RecordIntent and RecordResult. On a key conflict it
// compares payloads: the same payload is a retry of this write (no-op), a
// different one is eventlog.ErrEventConflict. The RLS WITH CHECK rejects a
// session the caller cannot see.
func (s *EventLogStore) insertEvent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		tag, err := tx.Exec(ctx, `
			INSERT INTO execution_events (user_id, session_id, step_index, batch_position, event_type, tool_use_id, payload)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)
			ON CONFLICT (session_id, step_index, batch_position, event_type) DO NOTHING`,
			userID, sessionID, stepIndex, batchPosition, string(eventType), toolUseID, payload,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var same bool
		err = tx.QueryRow(ctx, `
			SELECT payload = $6::jsonb FROM execution_events
			WHERE user_id = $1 AND session_id = $2 AND step_index = $3 AND batch_position = $4 AND event_type = $5`,
			userID, sessionID, stepIndex, batchPosition, string(eventType), payload,
		).Scan(&same)
		if errors.Is(err, pgx.ErrNoRows) {
			// The row this insert collided with is not visible to the caller.
			return fmt.Errorf("session %s: %w", sessionID, eventlog.ErrEventConflict)
		}
		if err != nil {
			return err
		}
		if !same {
			return eventlog.ErrEventConflict
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record %s event (step %d, position %d): %w", eventType, stepIndex, batchPosition, err)
	}
	return nil
}

func (s *EventLogStore) LoadEvents(ctx context.Context, sessionID string) ([]eventlog.ExecutionEvent, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.load_events")
	defer s.tracer.EndSpan(span)

	var out []eventlog.ExecutionEvent
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT session_id, step_index, batch_position, event_type, COALESCE(tool_use_id, ''), payload, created_at
			FROM execution_events
			WHERE user_id = $1 AND session_id = $2
			ORDER BY step_index ASC, batch_position ASC, event_type ASC`,
			UserIDFromContext(ctx), sessionID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e eventlog.ExecutionEvent
			var eventType string
			if err := rows.Scan(&e.SessionID, &e.StepIndex, &e.BatchPosition, &eventType, &e.ToolUseID, &e.Payload, &e.CreatedAt); err != nil {
				return fmt.Errorf("scan event: %w", err)
			}
			e.EventType = eventlog.EventType(eventType)
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, recordSpanError(span, fmt.Errorf("load events: %w", err))
	}
	return out, nil
}

func (s *EventLogStore) NextStepIndex(ctx context.Context, sessionID string) (int, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.next_step_index")
	defer s.tracer.EndSpan(span)

	var maxStep *int
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT MAX(step_index) FROM execution_events WHERE user_id = $1 AND session_id = $2`,
			UserIDFromContext(ctx), sessionID,
		).Scan(&maxStep)
	})
	if err != nil {
		return 0, recordSpanError(span, fmt.Errorf("next step index: %w", err))
	}
	if maxStep == nil {
		return 0, nil
	}
	return *maxStep + 1, nil
}

// AcquireSessionLease inserts the lease, or takes over one that has expired
// or that holder already holds. The conditional ON CONFLICT ... WHERE makes
// RowsAffected the acquire signal. Expiry is computed with the database clock.
func (s *EventLogStore) AcquireSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.acquire_session_lease")
	defer s.tracer.EndSpan(span)

	won, err := s.execLease(ctx, `
		INSERT INTO execution_leases (user_id, session_id, holder, expires_at)
		VALUES ($1, $2, $3, now() + $4 * interval '1 microsecond')
		ON CONFLICT (user_id, session_id) DO UPDATE
			SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
			WHERE execution_leases.expires_at < now() OR execution_leases.holder = EXCLUDED.holder`,
		sessionID, holder, ttl)
	if err != nil {
		return false, recordSpanError(span, fmt.Errorf("acquire session lease: %w", err))
	}
	return won, nil
}

func (s *EventLogStore) RenewSessionLease(ctx context.Context, sessionID, holder string, ttl time.Duration) (bool, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.renew_session_lease")
	defer s.tracer.EndSpan(span)

	held, err := s.execLease(ctx, `
		UPDATE execution_leases SET expires_at = now() + $4 * interval '1 microsecond'
		WHERE user_id = $1 AND session_id = $2 AND holder = $3`,
		sessionID, holder, ttl)
	if err != nil {
		return false, recordSpanError(span, fmt.Errorf("renew session lease: %w", err))
	}
	return held, nil
}

// execLease runs a lease statement taking (user, session, holder, ttl in
// microseconds) and reports whether it changed exactly one row.
func (s *EventLogStore) execLease(ctx context.Context, query, sessionID, holder string, ttl time.Duration) (bool, error) {
	var changed bool
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, query, UserIDFromContext(ctx), sessionID, holder, ttl.Microseconds())
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() == 1
		return nil
	})
	return changed, err
}

func (s *EventLogStore) ReleaseSessionLease(ctx context.Context, sessionID, holder string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.release_session_lease")
	defer s.tracer.EndSpan(span)

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM execution_leases WHERE user_id = $1 AND session_id = $2 AND holder = $3`,
			UserIDFromContext(ctx), sessionID, holder,
		)
		return err
	})
	if err != nil {
		return recordSpanError(span, fmt.Errorf("release session lease: %w", err))
	}
	return nil
}

func (s *EventLogStore) PruneResolvedStep(ctx context.Context, sessionID string, stepIndex int) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.prune_resolved_step")
	defer s.tracer.EndSpan(span)
	err := s.deleteEvents(ctx,
		`DELETE FROM execution_events WHERE user_id = $1 AND session_id = $2 AND step_index = $3`, sessionID, stepIndex)
	if err != nil {
		return recordSpanError(span, fmt.Errorf("prune resolved step: %w", err))
	}
	return nil
}

func (s *EventLogStore) PruneStepsBefore(ctx context.Context, sessionID string, stepIndex int) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg.event_log.prune_steps_before")
	defer s.tracer.EndSpan(span)
	err := s.deleteEvents(ctx,
		`DELETE FROM execution_events WHERE user_id = $1 AND session_id = $2 AND step_index < $3`, sessionID, stepIndex)
	if err != nil {
		return recordSpanError(span, fmt.Errorf("prune steps before: %w", err))
	}
	return nil
}

func (s *EventLogStore) deleteEvents(ctx context.Context, query, sessionID string, stepIndex int) error {
	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, query, UserIDFromContext(ctx), sessionID, stepIndex)
		return err
	})
}

// recordSpanError records a non-nil err on span and returns err unchanged.
func recordSpanError(span *observability.Span, err error) error {
	if err != nil {
		span.RecordError(err)
	}
	return err
}
