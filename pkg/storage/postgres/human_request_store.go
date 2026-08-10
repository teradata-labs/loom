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

	// A pending request must carry an expiry: a zero ExpiresAt would make the
	// row permanently approvable, and the resolve CAS's expiry guard is keyed
	// on the stored value. Both in-repo producers always stamp one; this guards
	// exported-API callers.
	if req.Status == "pending" && req.ExpiresAt.IsZero() {
		return fmt.Errorf("pending human request %s has no expiry", req.ID)
	}

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

	// An empty parameter map is stored as NULL, matching how a pre-params row reads.
	var paramsJSON []byte
	if len(req.Params) > 0 {
		paramsJSON, err = json.Marshal(req.Params)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal params: %w", err)
		}
	}

	// Store timeout as milliseconds in the database
	timeoutMs := req.Timeout.Milliseconds()

	err = execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			INSERT INTO human_requests (id, user_id, agent_id, session_id, question, context_json,
				request_type, priority, timeout_ms, created_at, expires_at,
				status, response, response_data_json, responded_at, responded_by,
				kind, summary, params_json, params_truncated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
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
			nullableString(req.Kind),
			nullableString(req.Summary),
			nullableString(string(paramsJSON)),
			req.ParamsTruncated,
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
				status, response, response_data_json, responded_at, responded_by,
				kind, summary, params_json, params_truncated
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
				status, response, response_data_json, responded_at, responded_by,
				kind, summary, params_json, params_truncated
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
				status, response, response_data_json, responded_at, responded_by,
				kind, summary, params_json, params_truncated
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

// RespondToRequest resolves a pending, non-expired request exactly once via a
// single atomic conditional update. On an already-decided or expired request it
// is a no-op returning nil, so the caller reads current state via Get. Errors
// only on a missing row / store failure. Expiry is judged against the DB clock
// (now() <= expires_at), so no external timer is involved; RLS scopes the
// access to the caller's user_id.
func (s *HumanRequestStore) RespondToRequest(ctx context.Context, requestID, status, response, respondedBy string, responseData map[string]interface{}) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_human_store.respond")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("request_id", requestID)
	span.SetAttribute("status", status)

	var responseDataJSON []byte
	if responseData != nil {
		var err error
		responseDataJSON, err = json.Marshal(responseData)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal response data: %w", err)
		}
	}

	var resolved bool
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		// Resolve only a pending, non-expired row. The expiry guard is the
		// store's own — nothing in the caller's payload can lift it; terminal
		// closes past expiry go through ExpireRequest.
		tag, err := tx.Exec(ctx, `
			UPDATE human_requests
			SET status = $1, response = $2, response_data_json = $3,
				responded_at = now(), responded_by = $4
			WHERE id = $5 AND user_id = $6
				AND status = 'pending'
				AND expires_at > now()`,
			status,
			nullableString(response),
			nullableBytes(responseDataJSON),
			nullableString(respondedBy),
			requestID,
			userID,
		)
		if err != nil {
			return fmt.Errorf("failed to respond to human request: %w", err)
		}
		resolved = tag.RowsAffected() == 1
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}

	if resolved {
		span.SetAttribute("resolved", true)
		return nil
	}

	// No row resolved: the request is missing, already-decided, or expired. A
	// successful RLS-scoped Get proves existence → no-op (caller reads current
	// state); absence is the only error case.
	existing, err := s.Get(ctx, requestID)
	if err != nil {
		span.RecordError(err)
		return err
	}
	if existing == nil {
		span.SetAttribute("error", "not_found")
		return fmt.Errorf("request not found: %s", requestID)
	}
	span.SetAttribute("resolved", false)
	return nil
}

// ExpireRequest terminally closes a pending row as "timeout" regardless of its
// expiry — the harness's close for abandoned or swept rows. A resolved row
// matches zero rows and is left untouched (closing is not resolving); a
// missing row is a no-op. Tenant-scoped like every other operation.
func (s *HumanRequestStore) ExpireRequest(ctx context.Context, requestID, respondedBy string) error {
	ctx, span := s.tracer.StartSpan(ctx, "pg_human_store.expire")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("request_id", requestID)

	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		_, err := tx.Exec(ctx, `
			UPDATE human_requests
			SET status = 'timeout', response = NULL, response_data_json = NULL,
				responded_at = now(), responded_by = $1
			WHERE id = $2 AND user_id = $3 AND status = 'pending'`,
			nullableString(respondedBy),
			requestID,
			userID,
		)
		if err != nil {
			return fmt.Errorf("failed to expire human request: %w", err)
		}
		return nil
	})
	if err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

// Close is a no-op; the pool is managed by the backend.
func (s *HumanRequestStore) Close() error {
	return nil
}

// scanHumanRequest reads a single human request from a pgx.Row. A missing row
// yields (nil, nil) so callers distinguish absence from a store failure.
func scanHumanRequest(row pgx.Row) (*shuttle.HumanRequest, error) {
	req, err := scanHumanRequestRow(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return req, nil
}

// scanHumanRequests reads multiple human requests from pgx.Rows.
func scanHumanRequests(rows pgx.Rows) ([]*shuttle.HumanRequest, error) {
	var requests []*shuttle.HumanRequest
	for rows.Next() {
		req, err := scanHumanRequestRow(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// scanHumanRequestRow reads one human request from a scannable row, expecting
// the column order shared by every SELECT in this store. It returns
// pgx.ErrNoRows unwrapped so single-row callers can treat absence as a no-op.
func scanHumanRequestRow(row pgx.Row) (*shuttle.HumanRequest, error) {
	var (
		req              shuttle.HumanRequest
		contextJSON      []byte
		responseDataJSON []byte
		response         *string
		respondedBy      *string
		kind             *string
		summary          *string
		paramsJSON       *string
		paramsTruncated  *bool
		timeoutMs        int64
	)

	err := row.Scan(
		&req.ID, &req.AgentID, &req.SessionID, &req.Question, &contextJSON,
		&req.RequestType, &req.Priority, &timeoutMs, &req.CreatedAt, &req.ExpiresAt,
		&req.Status, &response, &responseDataJSON, &req.RespondedAt, &respondedBy,
		&kind, &summary, &paramsJSON, &paramsTruncated,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
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
	// NULL kind/summary (pre-migration rows) leave the zero value ""; consumers
	// treat an empty kind as "question". The context_json origin discriminator
	// backstops a rolled-back kind column: an approval row is recognisable even
	// after the column is gone, instead of confidently re-reading as a question.
	if kind != nil {
		req.Kind = *kind
	}
	if summary != nil {
		req.Summary = *summary
	}
	// A NULL params_json — a question, or a row written before the column
	// existed — leaves Params nil. params_truncated carries a false default, so
	// a pre-params row reads false rather than NULL; NULL is still tolerated.
	if paramsJSON != nil && *paramsJSON != "" {
		if err := json.Unmarshal([]byte(*paramsJSON), &req.Params); err != nil {
			return nil, fmt.Errorf("failed to unmarshal human request params: %w", err)
		}
	}
	if paramsTruncated != nil {
		req.ParamsTruncated = *paramsTruncated
	}

	if len(contextJSON) > 0 {
		if err := json.Unmarshal(contextJSON, &req.Context); err != nil {
			return nil, fmt.Errorf("failed to unmarshal human request context: %w", err)
		}
	}
	if req.Kind == "" {
		req.Kind = kindFromContext(req.Context)
	}
	if len(responseDataJSON) > 0 {
		if err := json.Unmarshal(responseDataJSON, &req.ResponseData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal human request response data: %w", err)
		}
	}

	return &req, nil
}

// durationFromMs converts milliseconds to time.Duration.
func durationFromMs(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// kindFromContext recovers the origin discriminator the creator duplicated into
// context_json, so a row whose kind column was dropped by a migration rollback
// is still recognisably an approval rather than defaulting to a question.
func kindFromContext(ctx map[string]interface{}) string {
	if k, ok := ctx["kind"].(string); ok {
		return k
	}
	return ""
}

// Compile-time check: HumanRequestStore implements shuttle.HumanRequestStore.
var _ shuttle.HumanRequestStore = (*HumanRequestStore)(nil)
