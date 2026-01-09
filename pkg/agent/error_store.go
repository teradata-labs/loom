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
package agent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ErrorStore provides persistent storage for tool execution errors.
// Errors are stored with full details allowing agents to retrieve them on demand.
type ErrorStore interface {
	// Store saves an error and returns a unique ID
	Store(ctx context.Context, err *StoredError) (string, error)

	// Get retrieves a specific error by ID
	Get(ctx context.Context, errorID string) (*StoredError, error)

	// List returns errors matching filters (for analytics/debugging)
	List(ctx context.Context, filters ErrorFilters) ([]*StoredError, error)
}

// StoredError represents a tool execution error in storage.
type StoredError struct {
	ID           string          // err_YYYYMMDD_HHMMSS_<random>
	Timestamp    time.Time       // When the error occurred
	SessionID    string          // Session that encountered this error
	ToolName     string          // Name of the tool that failed
	RawError     json.RawMessage // Original error in any format (no assumptions about structure)
	ShortSummary string          // First line or 100 chars for quick reference
}

// ErrorFilters for querying errors.
type ErrorFilters struct {
	SessionID string    // Filter by session
	ToolName  string    // Filter by tool
	StartTime time.Time // Time range start
	EndTime   time.Time // Time range end
	Limit     int       // Max results (0 = unlimited)
}

// SQLiteErrorStore implements ErrorStore with SQLite persistence.
type SQLiteErrorStore struct {
	db     *sql.DB
	mu     sync.RWMutex
	tracer observability.Tracer
}

// NewSQLiteErrorStore creates a new SQLiteErrorStore.
// It opens the same database as SessionStore for error persistence.
func NewSQLiteErrorStore(dbPath string, tracer observability.Tracer) (*SQLiteErrorStore, error) {
	// Open database (same path as SessionStore - SQLite WAL mode handles multiple connections)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	store := &SQLiteErrorStore{
		db:     db,
		tracer: tracer,
	}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize error store schema: %w", err)
	}

	return store, nil
}

// initSchema creates the agent_errors table if it doesn't exist.
func (s *SQLiteErrorStore) initSchema() error {
	ctx := context.Background()
	ctx, span := s.tracer.StartSpan(ctx, "error_store.init_schema")
	defer s.tracer.EndSpan(span)

	schema := `
	CREATE TABLE IF NOT EXISTS agent_errors (
		id TEXT PRIMARY KEY,
		timestamp INTEGER NOT NULL,
		session_id TEXT NOT NULL,
		tool_name TEXT NOT NULL,
		raw_error TEXT NOT NULL,
		short_summary TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_agent_errors_session ON agent_errors(session_id);
	CREATE INDEX IF NOT EXISTS idx_agent_errors_timestamp ON agent_errors(timestamp);
	CREATE INDEX IF NOT EXISTS idx_agent_errors_tool ON agent_errors(tool_name);
	`

	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		span.RecordError(err)
		return err
	}

	return nil
}

// Store saves an error and returns a unique ID.
func (s *SQLiteErrorStore) Store(ctx context.Context, err *StoredError) (string, error) {
	ctx, span := s.tracer.StartSpan(ctx, "error_store.store")
	defer s.tracer.EndSpan(span)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate error ID: err_YYYYMMDD_HHMMSS_<6-char-random>
	now := time.Now()
	randomSuffix, randErr := generateRandomString(6)
	if randErr != nil {
		span.RecordError(randErr)
		return "", fmt.Errorf("failed to generate random ID suffix: %w", randErr)
	}

	errorID := fmt.Sprintf("err_%s_%s",
		now.Format("20060102_150405"),
		randomSuffix)

	// Set ID and timestamp
	err.ID = errorID
	err.Timestamp = now

	// Ensure raw_error is valid JSON
	rawErrorJSON := string(err.RawError)
	if !isValidJSON(rawErrorJSON) {
		// Wrap non-JSON errors in a simple JSON structure
		rawErrorJSON = fmt.Sprintf(`{"message": %q}`, rawErrorJSON)
	}

	// Insert into database
	_, execErr := s.db.ExecContext(ctx,
		`INSERT INTO agent_errors (id, timestamp, session_id, tool_name, raw_error, short_summary)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		err.ID,
		err.Timestamp.Unix(),
		err.SessionID,
		err.ToolName,
		rawErrorJSON,
		err.ShortSummary,
	)

	if execErr != nil {
		span.RecordError(execErr)
		return "", fmt.Errorf("failed to insert error: %w", execErr)
	}

	span.AddEvent("error_stored", map[string]interface{}{
		"error_id":   errorID,
		"tool_name":  err.ToolName,
		"session_id": err.SessionID,
	})

	return errorID, nil
}

// Get retrieves a specific error by ID.
func (s *SQLiteErrorStore) Get(ctx context.Context, errorID string) (*StoredError, error) {
	ctx, span := s.tracer.StartSpan(ctx, "error_store.get")
	defer s.tracer.EndSpan(span)

	span.AddEvent("lookup_error", map[string]interface{}{
		"error_id": errorID,
	})

	s.mu.RLock()
	defer s.mu.RUnlock()

	var stored StoredError
	var timestamp int64
	var rawError string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, timestamp, session_id, tool_name, raw_error, short_summary
		 FROM agent_errors
		 WHERE id = ?`,
		errorID,
	).Scan(
		&stored.ID,
		&timestamp,
		&stored.SessionID,
		&stored.ToolName,
		&rawError,
		&stored.ShortSummary,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("error not found: %s", errorID)
	}
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query error: %w", err)
	}

	stored.Timestamp = time.Unix(timestamp, 0)
	stored.RawError = json.RawMessage(rawError)

	return &stored, nil
}

// List returns errors matching filters.
func (s *SQLiteErrorStore) List(ctx context.Context, filters ErrorFilters) ([]*StoredError, error) {
	ctx, span := s.tracer.StartSpan(ctx, "error_store.list")
	defer s.tracer.EndSpan(span)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build query with filters
	query := `SELECT id, timestamp, session_id, tool_name, raw_error, short_summary FROM agent_errors WHERE 1=1`
	args := []interface{}{}

	if filters.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, filters.SessionID)
	}

	if filters.ToolName != "" {
		query += " AND tool_name = ?"
		args = append(args, filters.ToolName)
	}

	if !filters.StartTime.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, filters.StartTime.Unix())
	}

	if !filters.EndTime.IsZero() {
		query += " AND timestamp <= ?"
		args = append(args, filters.EndTime.Unix())
	}

	query += " ORDER BY timestamp DESC"

	if filters.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filters.Limit)
	}

	span.AddEvent("executing_query", map[string]interface{}{
		"filters": fmt.Sprintf("%+v", filters),
	})

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query errors: %w", err)
	}
	defer rows.Close()

	var errors []*StoredError
	for rows.Next() {
		var stored StoredError
		var timestamp int64
		var rawError string

		if err := rows.Scan(
			&stored.ID,
			&timestamp,
			&stored.SessionID,
			&stored.ToolName,
			&rawError,
			&stored.ShortSummary,
		); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to scan error row: %w", err)
		}

		stored.Timestamp = time.Unix(timestamp, 0)
		stored.RawError = json.RawMessage(rawError)
		errors = append(errors, &stored)
	}

	if err := rows.Err(); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	span.AddEvent("errors_listed", map[string]interface{}{
		"count": len(errors),
	})

	return errors, nil
}

// generateRandomString generates a cryptographically random hex string of specified length.
func generateRandomString(length int) (string, error) {
	bytes := make([]byte, length/2+1)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes)[:length], nil
}

// isValidJSON checks if a string is valid JSON.
func isValidJSON(s string) bool {
	var js interface{}
	return json.Unmarshal([]byte(s), &js) == nil
}

// extractErrorSummary extracts a readable summary from various error formats.
// This is best-effort - works with shuttle.Error, Go errors, or plain strings.
func extractErrorSummary(err interface{}) string {
	switch e := err.(type) {
	case *shuttle.Error:
		// Has code and message
		if e.Code != "" {
			firstLine := extractFirstLine(e.Message, 80)
			return fmt.Sprintf("Code %s: %s", e.Code, firstLine)
		}
		return extractFirstLine(e.Message, 100)

	case error:
		// Just a Go error
		return extractFirstLine(e.Error(), 100)

	case string:
		// Raw string error
		return extractFirstLine(e, 100)

	default:
		// Unknown format - just stringify it
		return extractFirstLine(fmt.Sprintf("%v", e), 100)
	}
}

// extractFirstLine gets first line or maxLen chars, whichever is shorter.
func extractFirstLine(s string, maxLen int) string {
	// Find first newline
	if idx := strings.Index(s, "\n"); idx > 0 && idx < maxLen {
		return s[:idx]
	}

	// No newline found or newline is beyond maxLen
	if len(s) <= maxLen {
		return s
	}

	return s[:maxLen] + "..."
}

// Ensure SQLiteErrorStore implements ErrorStore interface.
var _ ErrorStore = (*SQLiteErrorStore)(nil)
