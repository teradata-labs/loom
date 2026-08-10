// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package shuttle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/observability"
)

// SQLiteHumanRequestStore provides persistent SQLite storage for human requests.
// Suitable for production deployments with request history and audit trail.
type SQLiteHumanRequestStore struct {
	db     *sql.DB
	mu     sync.RWMutex
	tracer observability.Tracer
}

// SQLiteConfig configures the SQLite store.
type SQLiteConfig struct {
	Path   string               // Database file path (default: ":memory:")
	Tracer observability.Tracer // Tracer for observability (default: NoOpTracer)
}

// NewSQLiteHumanRequestStore creates a new SQLite-backed human request store.
func NewSQLiteHumanRequestStore(config SQLiteConfig) (*SQLiteHumanRequestStore, error) {
	if config.Path == "" {
		config.Path = ":memory:"
	}
	if config.Tracer == nil {
		config.Tracer = observability.NewNoOpTracer()
	}

	// Open database
	db, err := sql.Open("sqlite3", config.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close() // #nosec G104 -- best-effort cleanup on error path
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close() // #nosec G104 -- best-effort cleanup on error path
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	store := &SQLiteHumanRequestStore{
		db:     db,
		tracer: config.Tracer,
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		_ = db.Close() // #nosec G104 -- best-effort cleanup on error path
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// initSchema creates the database schema if it doesn't exist.
func (s *SQLiteHumanRequestStore) initSchema() error {
	ctx := context.Background()
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.init_schema")
	defer s.tracer.EndSpan(span)

	schema := `
	CREATE TABLE IF NOT EXISTS human_requests (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		question TEXT NOT NULL,
		context_json TEXT,
		request_type TEXT NOT NULL,
		priority TEXT NOT NULL,
		timeout_ms INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		status TEXT NOT NULL,
		response TEXT,
		response_data_json TEXT,
		responded_at INTEGER,
		responded_by TEXT,
		kind TEXT,
		summary TEXT,
		params_json TEXT,
		params_truncated BOOLEAN DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_human_requests_status ON human_requests(status);
	CREATE INDEX IF NOT EXISTS idx_human_requests_session ON human_requests(session_id);
	CREATE INDEX IF NOT EXISTS idx_human_requests_agent ON human_requests(agent_id);
	CREATE INDEX IF NOT EXISTS idx_human_requests_priority ON human_requests(priority);
	CREATE INDEX IF NOT EXISTS idx_human_requests_created ON human_requests(created_at);
	CREATE INDEX IF NOT EXISTS idx_human_requests_expires ON human_requests(expires_at);
	`

	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		span.RecordError(err)
		return err
	}

	// Columns added after the initial shape shipped: CREATE TABLE IF NOT EXISTS
	// is a no-op against an existing hitl.db, so each is applied with a
	// pragma_table_info guard (this store is opened standalone — no migration
	// runner ever reaches it).
	added := map[string]string{
		"kind":             "ALTER TABLE human_requests ADD COLUMN kind TEXT",
		"summary":          "ALTER TABLE human_requests ADD COLUMN summary TEXT",
		"params_json":      "ALTER TABLE human_requests ADD COLUMN params_json TEXT",
		"params_truncated": "ALTER TABLE human_requests ADD COLUMN params_truncated BOOLEAN DEFAULT 0",
	}
	for col, ddl := range added {
		var n int
		row := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('human_requests') WHERE name = ?`, col)
		if err := row.Scan(&n); err != nil {
			span.RecordError(err)
			return err
		}
		if n == 0 {
			if _, err := s.db.ExecContext(ctx, ddl); err != nil {
				span.RecordError(err)
				return fmt.Errorf("add column %s: %w", col, err)
			}
		}
	}

	span.SetAttribute("success", true)
	return nil
}

// Store saves a new human request to the database.
func (s *SQLiteHumanRequestStore) Store(ctx context.Context, req *HumanRequest) error {
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.store")
	defer s.tracer.EndSpan(span)

	span.SetAttribute("request_id", req.ID)
	span.SetAttribute("request_type", req.RequestType)
	span.SetAttribute("priority", req.Priority)

	// A pending request must carry an expiry: a zero ExpiresAt would make the
	// row permanently approvable, and the resolve CAS's expiry guard is keyed
	// on the stored value. Both in-repo producers always stamp one; this guards
	// exported-API callers.
	if req.Status == "pending" && req.ExpiresAt.IsZero() {
		return fmt.Errorf("pending human request %s has no expiry", req.ID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal JSON fields
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

	// Convert time to Unix milliseconds
	createdAtMs := req.CreatedAt.UnixMilli()
	expiresAtMs := req.ExpiresAt.UnixMilli()
	timeoutMs := req.Timeout.Milliseconds()

	var respondedAtMs *int64
	if req.RespondedAt != nil {
		ms := req.RespondedAt.UnixMilli()
		respondedAtMs = &ms
	}

	// An empty parameter map is stored as NULL, matching how a row written
	// before params existed reads back.
	var paramsJSON *string
	if len(req.Params) > 0 {
		encoded, err := json.Marshal(req.Params)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal params: %w", err)
		}
		encodedStr := string(encoded)
		paramsJSON = &encodedStr
	}

	query := `
		INSERT INTO human_requests (
			id, agent_id, session_id, question, context_json,
			request_type, priority, timeout_ms, created_at, expires_at,
			status, response, response_data_json, responded_at, responded_by,
			kind, summary, params_json, params_truncated
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = s.db.ExecContext(ctx, query,
		req.ID, req.AgentID, req.SessionID, req.Question, string(contextJSON),
		req.RequestType, req.Priority, timeoutMs, createdAtMs, expiresAtMs,
		req.Status, req.Response, string(responseDataJSON), respondedAtMs, req.RespondedBy,
		req.Kind, req.Summary, paramsJSON, req.ParamsTruncated,
	)

	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return fmt.Errorf("failed to insert request: %w", err)
	}

	span.SetAttribute("success", true)
	return nil
}

// Get retrieves a human request by ID.
func (s *SQLiteHumanRequestStore) Get(ctx context.Context, id string) (*HumanRequest, error) {
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.get")
	defer s.tracer.EndSpan(span)

	span.SetAttribute("request_id", id)

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, agent_id, session_id, question, context_json,
			   request_type, priority, timeout_ms, created_at, expires_at,
			   status, response, response_data_json, responded_at, responded_by,
			   kind, summary, params_json, params_truncated
		FROM human_requests
		WHERE id = ?
	`

	req, err := s.scanRequest(s.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		span.SetAttribute("success", false)
		span.SetAttribute("error", "not_found")
		return nil, fmt.Errorf("request not found: %s", id)
	}
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return nil, err
	}

	span.SetAttribute("success", true)
	span.SetAttribute("status", req.Status)
	return req, nil
}

// Update updates an existing human request.
func (s *SQLiteHumanRequestStore) Update(ctx context.Context, req *HumanRequest) error {
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.update")
	defer s.tracer.EndSpan(span)

	span.SetAttribute("request_id", req.ID)
	span.SetAttribute("status", req.Status)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal response data JSON
	var responseDataJSON []byte
	var err error
	if req.ResponseData != nil {
		responseDataJSON, err = json.Marshal(req.ResponseData)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal response data: %w", err)
		}
	}

	var respondedAtMs *int64
	if req.RespondedAt != nil {
		ms := req.RespondedAt.UnixMilli()
		respondedAtMs = &ms
	}

	query := `
		UPDATE human_requests
		SET status = ?, response = ?, response_data_json = ?,
		    responded_at = ?, responded_by = ?
		WHERE id = ?
	`

	result, err := s.db.ExecContext(ctx, query,
		req.Status, req.Response, string(responseDataJSON),
		respondedAtMs, req.RespondedBy, req.ID,
	)

	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return fmt.Errorf("failed to update request: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		span.RecordError(err)
		return err
	}

	if rowsAffected == 0 {
		span.SetAttribute("success", false)
		span.SetAttribute("error", "not_found")
		return fmt.Errorf("request not found: %s", req.ID)
	}

	span.SetAttribute("success", true)
	return nil
}

// ListPending returns all pending requests.
func (s *SQLiteHumanRequestStore) ListPending(ctx context.Context) ([]*HumanRequest, error) {
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.list_pending")
	defer s.tracer.EndSpan(span)

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, agent_id, session_id, question, context_json,
			   request_type, priority, timeout_ms, created_at, expires_at,
			   status, response, response_data_json, responded_at, responded_by,
			   kind, summary, params_json, params_truncated
		FROM human_requests
		WHERE status = 'pending'
		ORDER BY created_at ASC
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return nil, fmt.Errorf("failed to query pending requests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var requests []*HumanRequest
	for rows.Next() {
		req, err := s.scanRequest(rows)
		if err != nil {
			span.RecordError(err)
			return nil, err
		}
		requests = append(requests, req)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, err
	}

	span.SetAttribute("success", true)
	span.SetAttribute("count", len(requests))
	return requests, nil
}

// ListBySession returns all requests for a session.
func (s *SQLiteHumanRequestStore) ListBySession(ctx context.Context, sessionID string) ([]*HumanRequest, error) {
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.list_by_session")
	defer s.tracer.EndSpan(span)

	span.SetAttribute("session_id", sessionID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, agent_id, session_id, question, context_json,
			   request_type, priority, timeout_ms, created_at, expires_at,
			   status, response, response_data_json, responded_at, responded_by,
			   kind, summary, params_json, params_truncated
		FROM human_requests
		WHERE session_id = ?
		ORDER BY created_at DESC
	`

	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return nil, fmt.Errorf("failed to query session requests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var requests []*HumanRequest
	for rows.Next() {
		req, err := s.scanRequest(rows)
		if err != nil {
			span.RecordError(err)
			return nil, err
		}
		requests = append(requests, req)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, err
	}

	span.SetAttribute("success", true)
	span.SetAttribute("count", len(requests))
	return requests, nil
}

// RespondToRequest resolves a pending, non-expired request exactly once.
// On an already-decided or expired request it is a no-op returning nil, so the
// caller reads current state via Get. Errors only on a missing request.
func (s *SQLiteHumanRequestStore) RespondToRequest(ctx context.Context, requestID, status, response, respondedBy string, responseData map[string]interface{}) error {
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.respond")
	defer s.tracer.EndSpan(span)

	span.SetAttribute("request_id", requestID)
	span.SetAttribute("status", status)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal response data
	var responseDataJSON []byte
	if responseData != nil {
		var err error
		responseDataJSON, err = json.Marshal(responseData)
		if err != nil {
			span.RecordError(err)
			return fmt.Errorf("failed to marshal response data: %w", err)
		}
	}

	now := time.Now().UnixMilli()

	// Atomic conditional update: resolve only a pending, non-expired row. A
	// decided or expired row matches zero rows and is left untouched, and under
	// concurrency exactly one writer affects the row. The expiry guard is the
	// store's own — nothing in the caller's payload can lift it; terminal
	// closes past expiry go through ExpireRequest.
	query := `
		UPDATE human_requests
		SET status = ?, response = ?, response_data_json = ?,
		    responded_at = ?, responded_by = ?
		WHERE id = ? AND status = 'pending' AND expires_at > ?
	`

	result, err := s.db.ExecContext(ctx, query,
		status, response, string(responseDataJSON),
		now, respondedBy, requestID, now,
	)
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return fmt.Errorf("failed to respond to request: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		span.RecordError(err)
		return err
	}
	if rowsAffected == 1 {
		span.SetAttribute("success", true)
		return nil
	}

	// No row resolved: the request is missing, already-decided, or expired.
	// Existence makes this a no-op; absence is the only error.
	var exists int
	err = s.db.QueryRowContext(ctx, "SELECT 1 FROM human_requests WHERE id = ?", requestID).Scan(&exists)
	if err == sql.ErrNoRows {
		span.SetAttribute("success", false)
		span.SetAttribute("error", "not_found")
		return fmt.Errorf("request not found: %s", requestID)
	}
	if err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return fmt.Errorf("failed to check request status: %w", err)
	}

	span.SetAttribute("success", true)
	return nil
}

// ExpireRequest terminally closes a pending row as "timeout" regardless of its
// expiry — the harness's close for abandoned or swept rows. A resolved row
// matches zero rows and is left untouched (closing is not resolving); a
// missing row is a no-op.
func (s *SQLiteHumanRequestStore) ExpireRequest(ctx context.Context, requestID, respondedBy string) error {
	ctx, span := s.tracer.StartSpan(ctx, "hitl_store.expire")
	defer s.tracer.EndSpan(span)

	span.SetAttribute("request_id", requestID)

	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		UPDATE human_requests
		SET status = 'timeout', response = '', response_data_json = '',
		    responded_at = ?, responded_by = ?
		WHERE id = ? AND status = 'pending'
	`
	if _, err := s.db.ExecContext(ctx, query, time.Now().UnixMilli(), respondedBy, requestID); err != nil {
		span.RecordError(err)
		span.SetAttribute("success", false)
		return fmt.Errorf("failed to expire request: %w", err)
	}
	span.SetAttribute("success", true)
	return nil
}

// Close closes the database connection.
func (s *SQLiteHumanRequestStore) Close() error {
	return s.db.Close()
}

// scanRequest scans a row into a HumanRequest, expecting the column order
// shared by every SELECT in this store. A missing row yields sql.ErrNoRows
// wrapped, so callers can tell absence from a decode failure.
func (s *SQLiteHumanRequestStore) scanRequest(row interface {
	Scan(dest ...interface{}) error
}) (*HumanRequest, error) {
	var req HumanRequest
	var contextJSON, responseDataJSON string
	var timeoutMs int64
	var createdAtMs, expiresAtMs int64
	var respondedAtMs sql.NullInt64
	var kind, summary, paramsJSON sql.NullString
	var paramsTruncated sql.NullBool

	err := row.Scan(
		&req.ID, &req.AgentID, &req.SessionID, &req.Question, &contextJSON,
		&req.RequestType, &req.Priority, &timeoutMs, &createdAtMs, &expiresAtMs,
		&req.Status, &req.Response, &responseDataJSON, &respondedAtMs, &req.RespondedBy,
		&kind, &summary, &paramsJSON, &paramsTruncated,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to scan row: %w", err)
	}

	// Unmarshal JSON fields
	if contextJSON != "" {
		if err := json.Unmarshal([]byte(contextJSON), &req.Context); err != nil {
			return nil, fmt.Errorf("failed to unmarshal context: %w", err)
		}
	}

	if responseDataJSON != "" {
		if err := json.Unmarshal([]byte(responseDataJSON), &req.ResponseData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response data: %w", err)
		}
	}

	// Convert Unix milliseconds to time
	req.CreatedAt = time.UnixMilli(createdAtMs)
	req.ExpiresAt = time.UnixMilli(expiresAtMs)
	req.Timeout = time.Duration(timeoutMs) * time.Millisecond

	if respondedAtMs.Valid {
		t := time.UnixMilli(respondedAtMs.Int64)
		req.RespondedAt = &t
	}

	// NULL kind/summary leaves the zero value ""; consumers treat "" as
	// "question". The context_json origin discriminator backstops a dropped
	// kind column so an approval row never re-reads as a question.
	if kind.Valid {
		req.Kind = kind.String
	}
	if req.Kind == "" {
		if k, ok := req.Context["kind"].(string); ok {
			req.Kind = k
		}
	}
	if summary.Valid {
		req.Summary = summary.String
	}

	// A NULL params_json — a question, or a row written before the column
	// existed — leaves Params nil. params_truncated carries a false default, so
	// a pre-params row reads false rather than NULL; NULL is still tolerated.
	if paramsJSON.Valid && paramsJSON.String != "" {
		if err := json.Unmarshal([]byte(paramsJSON.String), &req.Params); err != nil {
			return nil, fmt.Errorf("failed to unmarshal params: %w", err)
		}
	}
	if paramsTruncated.Valid {
		req.ParamsTruncated = paramsTruncated.Bool
	}

	return &req, nil
}
