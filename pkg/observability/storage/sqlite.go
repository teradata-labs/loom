// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build fts5

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4" // SQLite driver with encryption support
)

// SQLiteStorage provides persistent trace storage using SQLite.
// Thread-safe for concurrent access. Suitable for production use.
type SQLiteStorage struct {
	db     *sql.DB
	dbPath string
}

// NewSQLiteStorage creates a new SQLite storage backend
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path cannot be empty")
	}

	// Open database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	storage := &SQLiteStorage{
		db:     db,
		dbPath: dbPath,
	}

	// Initialize schema
	if err := storage.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return storage, nil
}

// initSchema creates database tables if they don't exist
func (s *SQLiteStorage) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS evals (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		suite TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_evals_status ON evals(status);
	CREATE INDEX IF NOT EXISTS idx_evals_created_at ON evals(created_at);

	CREATE TABLE IF NOT EXISTS eval_runs (
		id TEXT PRIMARY KEY,
		eval_id TEXT NOT NULL,
		query TEXT,
		model TEXT,
		configuration_json TEXT,
		response TEXT,
		execution_time_ms INTEGER NOT NULL,
		token_count INTEGER NOT NULL,
		success INTEGER NOT NULL,
		error_message TEXT,
		session_id TEXT,
		timestamp INTEGER NOT NULL,
		FOREIGN KEY (eval_id) REFERENCES evals(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_eval_runs_eval_id ON eval_runs(eval_id);
	CREATE INDEX IF NOT EXISTS idx_eval_runs_timestamp ON eval_runs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_eval_runs_session_id ON eval_runs(session_id);

	CREATE TABLE IF NOT EXISTS eval_metrics (
		eval_id TEXT PRIMARY KEY,
		total_runs INTEGER NOT NULL,
		successful_runs INTEGER NOT NULL,
		failed_runs INTEGER NOT NULL,
		success_rate REAL NOT NULL,
		avg_execution_time_ms REAL NOT NULL,
		total_tokens INTEGER NOT NULL,
		avg_tokens_per_run REAL NOT NULL,
		total_cost REAL NOT NULL,
		first_run_timestamp INTEGER NOT NULL,
		last_run_timestamp INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		FOREIGN KEY (eval_id) REFERENCES evals(id) ON DELETE CASCADE
	);
	`

	_, err := s.db.Exec(schema)
	return err
}

// CreateEval creates a new evaluation session
func (s *SQLiteStorage) CreateEval(ctx context.Context, eval *Eval) error {
	if eval == nil {
		return fmt.Errorf("eval cannot be nil")
	}
	if eval.ID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	// Set timestamps if not provided
	if eval.CreatedAt == 0 {
		eval.CreatedAt = time.Now().Unix()
	}
	if eval.UpdatedAt == 0 {
		eval.UpdatedAt = eval.CreatedAt
	}

	query := `
		INSERT INTO evals (id, name, suite, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		eval.ID, eval.Name, eval.Suite, eval.Status, eval.CreatedAt, eval.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create eval: %w", err)
	}

	return nil
}

// UpdateEvalStatus updates the status of an evaluation
func (s *SQLiteStorage) UpdateEvalStatus(ctx context.Context, evalID string, status string) error {
	if evalID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	query := `
		UPDATE evals
		SET status = ?, updated_at = ?
		WHERE id = ?
	`

	result, err := s.db.ExecContext(ctx, query, status, time.Now().Unix(), evalID)
	if err != nil {
		return fmt.Errorf("failed to update eval status: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("eval not found: %s", evalID)
	}

	return nil
}

// CreateEvalRun stores a new trace/span
func (s *SQLiteStorage) CreateEvalRun(ctx context.Context, run *EvalRun) error {
	if run == nil {
		return fmt.Errorf("eval run cannot be nil")
	}
	if run.ID == "" {
		return fmt.Errorf("eval run ID cannot be empty")
	}
	if run.EvalID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	query := `
		INSERT INTO eval_runs (
			id, eval_id, query, model, configuration_json, response,
			execution_time_ms, token_count, success, error_message,
			session_id, timestamp
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		run.ID, run.EvalID, run.Query, run.Model, run.ConfigurationJSON, run.Response,
		run.ExecutionTimeMS, run.TokenCount, boolToInt(run.Success), run.ErrorMessage,
		run.SessionID, run.Timestamp)
	if err != nil {
		return fmt.Errorf("failed to create eval run: %w", err)
	}

	return nil
}

// CalculateEvalMetrics calculates aggregated metrics for an evaluation
func (s *SQLiteStorage) CalculateEvalMetrics(ctx context.Context, evalID string) (*EvalMetrics, error) {
	if evalID == "" {
		return nil, fmt.Errorf("eval ID cannot be empty")
	}

	query := `
		SELECT
			COUNT(*) as total_runs,
			COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0) as successful_runs,
			COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) as failed_runs,
			AVG(execution_time_ms) as avg_execution_time_ms,
			COALESCE(SUM(token_count), 0) as total_tokens,
			AVG(token_count) as avg_tokens_per_run,
			MIN(timestamp) as first_run_timestamp,
			MAX(timestamp) as last_run_timestamp
		FROM eval_runs
		WHERE eval_id = ?
	`

	metrics := &EvalMetrics{
		EvalID:    evalID,
		UpdatedAt: time.Now().Unix(),
	}

	var avgExecTime, avgTokens sql.NullFloat64
	var firstRun, lastRun sql.NullInt64

	err := s.db.QueryRowContext(ctx, query, evalID).Scan(
		&metrics.TotalRuns,
		&metrics.SuccessfulRuns,
		&metrics.FailedRuns,
		&avgExecTime,
		&metrics.TotalTokens,
		&avgTokens,
		&firstRun,
		&lastRun,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate metrics: %w", err)
	}

	// Handle NULL values
	if avgExecTime.Valid {
		metrics.AvgExecutionTimeMS = avgExecTime.Float64
	}
	if avgTokens.Valid {
		metrics.AvgTokensPerRun = avgTokens.Float64
	}
	if firstRun.Valid {
		metrics.FirstRunTimestamp = firstRun.Int64
	}
	if lastRun.Valid {
		metrics.LastRunTimestamp = lastRun.Int64
	}

	// Calculate success rate
	if metrics.TotalRuns > 0 {
		metrics.SuccessRate = float64(metrics.SuccessfulRuns) / float64(metrics.TotalRuns)
	}

	return metrics, nil
}

// UpsertEvalMetrics stores or updates metrics for an evaluation
func (s *SQLiteStorage) UpsertEvalMetrics(ctx context.Context, metrics *EvalMetrics) error {
	if metrics == nil {
		return fmt.Errorf("metrics cannot be nil")
	}
	if metrics.EvalID == "" {
		return fmt.Errorf("eval ID cannot be empty")
	}

	query := `
		INSERT INTO eval_metrics (
			eval_id, total_runs, successful_runs, failed_runs, success_rate,
			avg_execution_time_ms, total_tokens, avg_tokens_per_run, total_cost,
			first_run_timestamp, last_run_timestamp, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(eval_id) DO UPDATE SET
			total_runs = excluded.total_runs,
			successful_runs = excluded.successful_runs,
			failed_runs = excluded.failed_runs,
			success_rate = excluded.success_rate,
			avg_execution_time_ms = excluded.avg_execution_time_ms,
			total_tokens = excluded.total_tokens,
			avg_tokens_per_run = excluded.avg_tokens_per_run,
			total_cost = excluded.total_cost,
			first_run_timestamp = excluded.first_run_timestamp,
			last_run_timestamp = excluded.last_run_timestamp,
			updated_at = excluded.updated_at
	`

	_, err := s.db.ExecContext(ctx, query,
		metrics.EvalID, metrics.TotalRuns, metrics.SuccessfulRuns, metrics.FailedRuns,
		metrics.SuccessRate, metrics.AvgExecutionTimeMS, metrics.TotalTokens,
		metrics.AvgTokensPerRun, metrics.TotalCost, metrics.FirstRunTimestamp,
		metrics.LastRunTimestamp, metrics.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to upsert metrics: %w", err)
	}

	return nil
}

// Close closes the SQLite database connection
func (s *SQLiteStorage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// boolToInt converts bool to int for SQLite storage
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Compile-time interface check
var _ Storage = (*SQLiteStorage)(nil)
