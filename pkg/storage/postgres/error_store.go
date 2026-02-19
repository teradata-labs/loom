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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// ErrorStore implements agent.ErrorStore using PostgreSQL.
type ErrorStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewErrorStore creates a new PostgreSQL-backed error store.
func NewErrorStore(pool *pgxpool.Pool, tracer observability.Tracer) *ErrorStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &ErrorStore{
		pool:   pool,
		tracer: tracer,
	}
}

// Store persists an agent error and returns its ID.
func (s *ErrorStore) Store(ctx context.Context, storedErr *agent.StoredError) (string, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_error_store.store")
	defer s.tracer.EndSpan(span)

	id := storedErr.ID
	if id == "" {
		id = fmt.Sprintf("err-%d", time.Now().UnixNano())
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO agent_errors (id, timestamp, session_id, tool_name, raw_error, short_summary)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id,
		storedErr.Timestamp,
		storedErr.SessionID,
		storedErr.ToolName,
		storedErr.RawError,
		storedErr.ShortSummary,
	)
	if err != nil {
		span.RecordError(err)
		return "", fmt.Errorf("failed to store error: %w", err)
	}

	span.SetAttribute("error_id", id)
	return id, nil
}

// Get retrieves an error by its ID.
func (s *ErrorStore) Get(ctx context.Context, errorID string) (*agent.StoredError, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_error_store.get")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("error_id", errorID)

	var (
		storedErr agent.StoredError
		timestamp time.Time
	)

	err := s.pool.QueryRow(ctx, `
		SELECT id, timestamp, session_id, tool_name, raw_error, short_summary
		FROM agent_errors WHERE id = $1`,
		errorID,
	).Scan(&storedErr.ID, &timestamp, &storedErr.SessionID, &storedErr.ToolName, &storedErr.RawError, &storedErr.ShortSummary)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to get error: %w", err)
	}

	storedErr.Timestamp = timestamp
	return &storedErr, nil
}

// List retrieves errors matching the given filters.
func (s *ErrorStore) List(ctx context.Context, filters agent.ErrorFilters) ([]*agent.StoredError, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_error_store.list")
	defer s.tracer.EndSpan(span)

	query := "SELECT id, timestamp, session_id, tool_name, raw_error, short_summary FROM agent_errors WHERE 1=1"
	args := []interface{}{}
	argIdx := 1

	if filters.SessionID != "" {
		query += fmt.Sprintf(" AND session_id = $%d", argIdx)
		args = append(args, filters.SessionID)
		argIdx++
	}
	if filters.ToolName != "" {
		query += fmt.Sprintf(" AND tool_name = $%d", argIdx)
		args = append(args, filters.ToolName)
		argIdx++
	}
	if !filters.StartTime.IsZero() {
		query += fmt.Sprintf(" AND timestamp >= $%d", argIdx)
		args = append(args, filters.StartTime)
		argIdx++
	}
	if !filters.EndTime.IsZero() {
		query += fmt.Sprintf(" AND timestamp <= $%d", argIdx)
		args = append(args, filters.EndTime)
		argIdx++
	}

	query += " ORDER BY timestamp DESC"

	if filters.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, filters.Limit)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to list errors: %w", err)
	}
	defer rows.Close()

	var errors []*agent.StoredError
	for rows.Next() {
		var (
			storedErr agent.StoredError
			timestamp time.Time
		)
		if err := rows.Scan(&storedErr.ID, &timestamp, &storedErr.SessionID, &storedErr.ToolName, &storedErr.RawError, &storedErr.ShortSummary); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan error: %w", err)
		}
		storedErr.Timestamp = timestamp
		errors = append(errors, &storedErr)
	}

	return errors, rows.Err()
}

// Close is a no-op; the pool is managed by the backend.
func (s *ErrorStore) Close() error {
	return nil
}

// Compile-time check: ErrorStore implements agent.ErrorStore.
var _ agent.ErrorStore = (*ErrorStore)(nil)
