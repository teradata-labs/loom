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
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// HumanRequestStore implements shuttle.HumanRequestStore using PostgreSQL.
type HumanRequestStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewHumanRequestStore creates a new PostgreSQL-backed human request store.
func NewHumanRequestStore(pool *pgxpool.Pool, tracer observability.Tracer) *HumanRequestStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &HumanRequestStore{
		pool:   pool,
		tracer: tracer,
	}
}

// Store persists a human request.
func (s *HumanRequestStore) Store(ctx context.Context, req *shuttle.HumanRequest) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_human_store.store")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("request_id", req.ID)

	contextJSON, err := json.Marshal(req.Context)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal context: %w", err)
	}

	var responseDataJSON []byte
	if req.ResponseData != nil {
		responseDataJSON, err = json.Marshal(req.ResponseData)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal response data: %w", err)
		}
	}

	// Store timeout as milliseconds in the database
	timeoutMs := req.Timeout.Milliseconds()

	err = execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			INSERT INTO human_requests (id, user_id, agent_id, session_id, question, context_json,
				request_type, priority, timeout_ms, created_at, expires_at,
				status, response, response_data_json, responded_at, responded_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			req.ID,
			userID,
			req.AgentID,
			req.SessionID,
			req.Question,
			contextJSON,
			req.RequestType,
			req.Priority,
			timeoutMs,
			req.CreatedAt,
			req.ExpiresAt,
			req.Status,
			nullableString(req.Response),
			nullableBytes(responseDataJSON),
			req.RespondedAt, // *time.Time, nil-safe
			nullableString(req.RespondedBy),
		)
		if err != nil {
			return fmt.Errorf("failed to store human request: %w", err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// Get retrieves a human request by ID.
func (s *HumanRequestStore) Get(ctx context.Context, id string) (*shuttle.HumanRequest, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_human_store.get")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("request_id", id)

	var result *shuttle.HumanRequest
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		row := tx.QueryRow(ctx, `
			SELECT id, agent_id, session_id, question, context_json,
				request_type, priority, timeout_ms, created_at, expires_at,
				status, response, response_data_json, responded_at, responded_by
			FROM human_requests WHERE id = $1 AND user_id = $2`,
			id, userID,
		)
		var err error
		result, err = scanHumanRequest(row)
		return err
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return result, nil
}

// Update modifies an existing human request.
func (s *HumanRequestStore) Update(ctx context.Context, req *shuttle.HumanRequest) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_human_store.update")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("request_id", req.ID)

	var responseDataJSON []byte
	if req.ResponseData != nil {
		var err error
		responseDataJSON, err = json.Marshal(req.ResponseData)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal response data: %w", err)
		}
	}

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			UPDATE human_requests
			SET status = $1, response = $2, response_data_json = $3,
				responded_at = $4, responded_by = $5
			WHERE id = $6 AND user_id = $7`,
			req.Status,
			nullableString(req.Response),
			nullableBytes(responseDataJSON),
			req.RespondedAt, // *time.Time, nil-safe
			nullableString(req.RespondedBy),
			req.ID,
			userID,
		)
		if err != nil {
			return fmt.Errorf("failed to update human request: %w", err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// ListPending retrieves all pending human requests ordered by creation time.
func (s *HumanRequestStore) ListPending(ctx context.Context) ([]*shuttle.HumanRequest, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_human_store.list_pending")
	defer s.tracer.EndSpan(span)

	var result []*shuttle.HumanRequest
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		rows, err := tx.Query(ctx, `
			SELECT id, agent_id, session_id, question, context_json,
				request_type, priority, timeout_ms, created_at, expires_at,
				status, response, response_data_json, responded_at, responded_by
			FROM human_requests
			WHERE status = 'pending' AND user_id = $1
			ORDER BY created_at ASC`,
			userID,
		)
		if err != nil {
			return fmt.Errorf("failed to list pending requests: %w", err)
		}
		defer rows.Close()

		result, err = scanHumanRequests(rows)
		return err
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return result, nil
}

// ListBySession retrieves all human requests for a session.
func (s *HumanRequestStore) ListBySession(ctx context.Context, sessionID string) ([]*shuttle.HumanRequest, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg_human_store.list_by_session")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("session_id", sessionID)

	var result []*shuttle.HumanRequest
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		rows, err := tx.Query(ctx, `
			SELECT id, agent_id, session_id, question, context_json,
				request_type, priority, timeout_ms, created_at, expires_at,
				status, response, response_data_json, responded_at, responded_by
			FROM human_requests
			WHERE session_id = $1 AND user_id = $2
			ORDER BY created_at DESC`,
			sessionID, userID,
		)
		if err != nil {
			return fmt.Errorf("failed to list session requests: %w", err)
		}
		defer rows.Close()

		result, err = scanHumanRequests(rows)
		return err
	})
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return result, nil
}

// Close is a no-op; the pool is managed by the backend.
func (s *HumanRequestStore) Close() error {
	return nil
}

// scanHumanRequest reads a single human request from a pgx.Row.
func scanHumanRequest(row pgx.Row) (*shuttle.HumanRequest, error) {
	var (
		req              shuttle.HumanRequest
		contextJSON      []byte
		responseDataJSON []byte
		response         *string
		respondedBy      *string
		timeoutMs        int64
	)

	err := row.Scan(
		&req.ID, &req.AgentID, &req.SessionID, &req.Question, &contextJSON,
		&req.RequestType, &req.Priority, &timeoutMs, &req.CreatedAt, &req.ExpiresAt,
		&req.Status, &response, &responseDataJSON, &req.RespondedAt, &respondedBy,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan human request: %w", err)
	}

	req.Timeout = durationFromMs(timeoutMs)

	if response != nil {
		req.Response = *response
	}
	if respondedBy != nil {
		req.RespondedBy = *respondedBy
	}

	if len(contextJSON) > 0 {
		if err := json.Unmarshal(contextJSON, &req.Context); err != nil {
			return nil, fmt.Errorf("failed to unmarshal human request context: %w", err)
		}
	}
	if len(responseDataJSON) > 0 {
		if err := json.Unmarshal(responseDataJSON, &req.ResponseData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal human request response data: %w", err)
		}
	}

	return &req, nil
}

// scanHumanRequests reads multiple human requests from pgx.Rows.
func scanHumanRequests(rows pgx.Rows) ([]*shuttle.HumanRequest, error) {
	var requests []*shuttle.HumanRequest
	for rows.Next() {
		var (
			req              shuttle.HumanRequest
			contextJSON      []byte
			responseDataJSON []byte
			response         *string
			respondedBy      *string
			timeoutMs        int64
		)

		err := rows.Scan(
			&req.ID, &req.AgentID, &req.SessionID, &req.Question, &contextJSON,
			&req.RequestType, &req.Priority, &timeoutMs, &req.CreatedAt, &req.ExpiresAt,
			&req.Status, &response, &responseDataJSON, &req.RespondedAt, &respondedBy,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan human request: %w", err)
		}

		req.Timeout = durationFromMs(timeoutMs)

		if response != nil {
			req.Response = *response
		}
		if respondedBy != nil {
			req.RespondedBy = *respondedBy
		}

		if len(contextJSON) > 0 {
			if err := json.Unmarshal(contextJSON, &req.Context); err != nil {
				return nil, fmt.Errorf("failed to unmarshal human request context: %w", err)
			}
		}
		if len(responseDataJSON) > 0 {
			if err := json.Unmarshal(responseDataJSON, &req.ResponseData); err != nil {
				return nil, fmt.Errorf("failed to unmarshal human request response data: %w", err)
			}
		}

		requests = append(requests, &req)
	}
	return requests, rows.Err()
}

// durationFromMs converts milliseconds to time.Duration.
func durationFromMs(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// Compile-time check: HumanRequestStore implements shuttle.HumanRequestStore.
var _ shuttle.HumanRequestStore = (*HumanRequestStore)(nil)
