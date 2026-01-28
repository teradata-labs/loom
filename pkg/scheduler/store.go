// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Store persists scheduled workflows and execution history to SQLite.
// Uses WAL mode for concurrent read/write access.
type Store struct {
	db     *sql.DB
	mu     sync.RWMutex
	logger *zap.Logger
}

// NewStore creates a new scheduler store with SQLite backend.
// The dbPath should point to $LOOM_DATA_DIR/scheduler.db.
func NewStore(ctx context.Context, dbPath string, logger *zap.Logger) (*Store, error) {
	// Open database with SQLite-specific pragmas
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL", dbPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	store := &Store{
		db:     db,
		logger: logger,
	}

	// Initialize schema
	if err := store.initSchema(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// initSchema creates the database tables if they don't exist.
func (s *Store) initSchema(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS scheduled_workflows (
		id TEXT PRIMARY KEY,
		workflow_name TEXT NOT NULL,
		yaml_path TEXT,
		pattern_json TEXT NOT NULL,
		schedule_json TEXT NOT NULL,
		last_execution_at INTEGER DEFAULT 0,
		next_execution_at INTEGER DEFAULT 0,
		current_execution_id TEXT,
		total_executions INTEGER DEFAULT 0,
		successful_executions INTEGER DEFAULT 0,
		failed_executions INTEGER DEFAULT 0,
		skipped_executions INTEGER DEFAULT 0,
		last_status TEXT,
		last_error TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_next_execution ON scheduled_workflows(next_execution_at);
	CREATE INDEX IF NOT EXISTS idx_workflow_name ON scheduled_workflows(workflow_name);
	CREATE INDEX IF NOT EXISTS idx_yaml_path ON scheduled_workflows(yaml_path);

	CREATE TABLE IF NOT EXISTS schedule_executions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		schedule_id TEXT NOT NULL,
		execution_id TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		completed_at INTEGER DEFAULT 0,
		status TEXT NOT NULL,
		error TEXT,
		duration_ms INTEGER DEFAULT 0,
		FOREIGN KEY (schedule_id) REFERENCES scheduled_workflows(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_schedule_id ON schedule_executions(schedule_id);
	CREATE INDEX IF NOT EXISTS idx_execution_id ON schedule_executions(execution_id);
	CREATE INDEX IF NOT EXISTS idx_started_at ON schedule_executions(started_at);
	`

	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Create persists a new scheduled workflow.
func (s *Store) Create(ctx context.Context, schedule *loomv1.ScheduledWorkflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal pattern and schedule to JSON
	patternJSON, err := protojson.Marshal(schedule.Pattern)
	if err != nil {
		return fmt.Errorf("failed to marshal pattern: %w", err)
	}

	scheduleJSON, err := protojson.Marshal(schedule.Schedule)
	if err != nil {
		return fmt.Errorf("failed to marshal schedule: %w", err)
	}

	query := `
		INSERT INTO scheduled_workflows (
			id, workflow_name, yaml_path, pattern_json, schedule_json,
			last_execution_at, next_execution_at, current_execution_id,
			total_executions, successful_executions, failed_executions, skipped_executions,
			last_status, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	var stats *loomv1.ScheduleStats
	if schedule.Stats != nil {
		stats = schedule.Stats
	} else {
		stats = &loomv1.ScheduleStats{}
	}

	_, err = s.db.ExecContext(ctx, query,
		schedule.Id,
		schedule.WorkflowName,
		schedule.YamlPath,
		string(patternJSON),
		string(scheduleJSON),
		schedule.LastExecutionAt,
		schedule.NextExecutionAt,
		schedule.CurrentExecutionId,
		stats.TotalExecutions,
		stats.SuccessfulExecutions,
		stats.FailedExecutions,
		stats.SkippedExecutions,
		stats.LastStatus,
		stats.LastError,
		schedule.CreatedAt,
		schedule.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to insert schedule: %w", err)
	}

	return nil
}

// Get retrieves a scheduled workflow by ID.
func (s *Store) Get(ctx context.Context, id string) (*loomv1.ScheduledWorkflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, workflow_name, yaml_path, pattern_json, schedule_json,
		       last_execution_at, next_execution_at, current_execution_id,
		       total_executions, successful_executions, failed_executions, skipped_executions,
		       last_status, last_error, created_at, updated_at
		FROM scheduled_workflows
		WHERE id = ?
	`

	row := s.db.QueryRowContext(ctx, query, id)

	var (
		schedule           loomv1.ScheduledWorkflow
		patternJSON        string
		scheduleJSON       string
		lastStatus         sql.NullString
		lastError          sql.NullString
		currentExecutionID sql.NullString
		yamlPath           sql.NullString
		totalExecs         int32
		successfulExecs    int32
		failedExecs        int32
		skippedExecs       int32
	)

	err := row.Scan(
		&schedule.Id,
		&schedule.WorkflowName,
		&yamlPath,
		&patternJSON,
		&scheduleJSON,
		&schedule.LastExecutionAt,
		&schedule.NextExecutionAt,
		&currentExecutionID,
		&totalExecs,
		&successfulExecs,
		&failedExecs,
		&skippedExecs,
		&lastStatus,
		&lastError,
		&schedule.CreatedAt,
		&schedule.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("schedule not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query schedule: %w", err)
	}

	// Set nullable fields
	if yamlPath.Valid {
		schedule.YamlPath = yamlPath.String
	}
	if currentExecutionID.Valid {
		schedule.CurrentExecutionId = currentExecutionID.String
	}

	// Unmarshal pattern
	schedule.Pattern = &loomv1.WorkflowPattern{}
	if err := protojson.Unmarshal([]byte(patternJSON), schedule.Pattern); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pattern: %w", err)
	}

	// Unmarshal schedule config
	schedule.Schedule = &loomv1.ScheduleConfig{}
	if err := protojson.Unmarshal([]byte(scheduleJSON), schedule.Schedule); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schedule: %w", err)
	}

	// Build stats
	schedule.Stats = &loomv1.ScheduleStats{
		TotalExecutions:      totalExecs,
		SuccessfulExecutions: successfulExecs,
		FailedExecutions:     failedExecs,
		SkippedExecutions:    skippedExecs,
	}
	if lastStatus.Valid {
		schedule.Stats.LastStatus = lastStatus.String
	}
	if lastError.Valid {
		schedule.Stats.LastError = lastError.String
	}

	return &schedule, nil
}

// Update modifies an existing scheduled workflow.
func (s *Store) Update(ctx context.Context, schedule *loomv1.ScheduledWorkflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal pattern and schedule to JSON
	patternJSON, err := protojson.Marshal(schedule.Pattern)
	if err != nil {
		return fmt.Errorf("failed to marshal pattern: %w", err)
	}

	scheduleJSON, err := protojson.Marshal(schedule.Schedule)
	if err != nil {
		return fmt.Errorf("failed to marshal schedule: %w", err)
	}

	query := `
		UPDATE scheduled_workflows
		SET workflow_name = ?, yaml_path = ?, pattern_json = ?, schedule_json = ?,
		    last_execution_at = ?, next_execution_at = ?, current_execution_id = ?,
		    total_executions = ?, successful_executions = ?, failed_executions = ?, skipped_executions = ?,
		    last_status = ?, last_error = ?, updated_at = ?
		WHERE id = ?
	`

	var stats *loomv1.ScheduleStats
	if schedule.Stats != nil {
		stats = schedule.Stats
	} else {
		stats = &loomv1.ScheduleStats{}
	}

	result, err := s.db.ExecContext(ctx, query,
		schedule.WorkflowName,
		schedule.YamlPath,
		string(patternJSON),
		string(scheduleJSON),
		schedule.LastExecutionAt,
		schedule.NextExecutionAt,
		schedule.CurrentExecutionId,
		stats.TotalExecutions,
		stats.SuccessfulExecutions,
		stats.FailedExecutions,
		stats.SkippedExecutions,
		stats.LastStatus,
		stats.LastError,
		time.Now().Unix(),
		schedule.Id,
	)

	if err != nil {
		return fmt.Errorf("failed to update schedule: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule not found: %s", schedule.Id)
	}

	return nil
}

// Delete removes a scheduled workflow from the store.
func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.ExecContext(ctx, "DELETE FROM scheduled_workflows WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete schedule: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule not found: %s", id)
	}

	return nil
}

// List returns all scheduled workflows, optionally filtered by enabled status.
func (s *Store) List(ctx context.Context) ([]*loomv1.ScheduledWorkflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, workflow_name, yaml_path, pattern_json, schedule_json,
		       last_execution_at, next_execution_at, current_execution_id,
		       total_executions, successful_executions, failed_executions, skipped_executions,
		       last_status, last_error, created_at, updated_at
		FROM scheduled_workflows
		ORDER BY next_execution_at ASC
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*loomv1.ScheduledWorkflow

	for rows.Next() {
		var (
			schedule           loomv1.ScheduledWorkflow
			patternJSON        string
			scheduleJSON       string
			lastStatus         sql.NullString
			lastError          sql.NullString
			currentExecutionID sql.NullString
			yamlPath           sql.NullString
			totalExecs         int32
			successfulExecs    int32
			failedExecs        int32
			skippedExecs       int32
		)

		err := rows.Scan(
			&schedule.Id,
			&schedule.WorkflowName,
			&yamlPath,
			&patternJSON,
			&scheduleJSON,
			&schedule.LastExecutionAt,
			&schedule.NextExecutionAt,
			&currentExecutionID,
			&totalExecs,
			&successfulExecs,
			&failedExecs,
			&skippedExecs,
			&lastStatus,
			&lastError,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan schedule: %w", err)
		}

		// Set nullable fields
		if yamlPath.Valid {
			schedule.YamlPath = yamlPath.String
		}
		if currentExecutionID.Valid {
			schedule.CurrentExecutionId = currentExecutionID.String
		}

		// Unmarshal pattern
		schedule.Pattern = &loomv1.WorkflowPattern{}
		if err := protojson.Unmarshal([]byte(patternJSON), schedule.Pattern); err != nil {
			return nil, fmt.Errorf("failed to unmarshal pattern: %w", err)
		}

		// Unmarshal schedule config
		schedule.Schedule = &loomv1.ScheduleConfig{}
		if err := protojson.Unmarshal([]byte(scheduleJSON), schedule.Schedule); err != nil {
			return nil, fmt.Errorf("failed to unmarshal schedule: %w", err)
		}

		// Build stats
		schedule.Stats = &loomv1.ScheduleStats{
			TotalExecutions:      totalExecs,
			SuccessfulExecutions: successfulExecs,
			FailedExecutions:     failedExecs,
			SkippedExecutions:    skippedExecs,
		}
		if lastStatus.Valid {
			schedule.Stats.LastStatus = lastStatus.String
		}
		if lastError.Valid {
			schedule.Stats.LastError = lastError.String
		}

		schedules = append(schedules, &schedule)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating schedules: %w", err)
	}

	return schedules, nil
}

// UpdateCurrentExecution sets or clears the current execution ID for a schedule.
func (s *Store) UpdateCurrentExecution(ctx context.Context, scheduleID, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `UPDATE scheduled_workflows SET current_execution_id = ?, updated_at = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, executionID, time.Now().Unix(), scheduleID)
	if err != nil {
		return fmt.Errorf("failed to update current execution: %w", err)
	}

	return nil
}

// UpdateNextExecution sets the next scheduled execution time.
func (s *Store) UpdateNextExecution(ctx context.Context, scheduleID string, nextExecution int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `UPDATE scheduled_workflows SET next_execution_at = ?, updated_at = ? WHERE id = ?`
	_, err := s.db.ExecContext(ctx, query, nextExecution, time.Now().Unix(), scheduleID)
	if err != nil {
		return fmt.Errorf("failed to update next execution: %w", err)
	}

	return nil
}

// RecordSuccess increments successful execution count and updates stats.
func (s *Store) RecordSuccess(ctx context.Context, scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		UPDATE scheduled_workflows
		SET total_executions = total_executions + 1,
		    successful_executions = successful_executions + 1,
		    last_execution_at = ?,
		    last_status = 'success',
		    last_error = '',
		    updated_at = ?
		WHERE id = ?
	`

	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, query, now, now, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to record success: %w", err)
	}

	return nil
}

// RecordFailure increments failed execution count and stores error.
func (s *Store) RecordFailure(ctx context.Context, scheduleID, errorMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		UPDATE scheduled_workflows
		SET total_executions = total_executions + 1,
		    failed_executions = failed_executions + 1,
		    last_execution_at = ?,
		    last_status = 'failed',
		    last_error = ?,
		    updated_at = ?
		WHERE id = ?
	`

	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, query, now, errorMsg, now, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to record failure: %w", err)
	}

	return nil
}

// IncrementSkipped increments the skipped execution counter.
func (s *Store) IncrementSkipped(ctx context.Context, scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		UPDATE scheduled_workflows
		SET skipped_executions = skipped_executions + 1,
		    last_status = 'skipped',
		    updated_at = ?
		WHERE id = ?
	`

	_, err := s.db.ExecContext(ctx, query, time.Now().Unix(), scheduleID)
	if err != nil {
		return fmt.Errorf("failed to increment skipped: %w", err)
	}

	return nil
}

// RecordExecution stores an execution record for audit trail.
func (s *Store) RecordExecution(ctx context.Context, exec *loomv1.ScheduleExecution, scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		INSERT INTO schedule_executions (schedule_id, execution_id, started_at, completed_at, status, error, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		scheduleID,
		exec.ExecutionId,
		exec.StartedAt,
		exec.CompletedAt,
		exec.Status,
		exec.Error,
		exec.DurationMs,
	)

	if err != nil {
		return fmt.Errorf("failed to record execution: %w", err)
	}

	return nil
}

// GetExecutionHistory retrieves execution history for a schedule.
func (s *Store) GetExecutionHistory(ctx context.Context, scheduleID string, limit int) ([]*loomv1.ScheduleExecution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT execution_id, started_at, completed_at, status, error, duration_ms
		FROM schedule_executions
		WHERE schedule_id = ?
		ORDER BY started_at DESC
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, scheduleID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query execution history: %w", err)
	}
	defer rows.Close()

	// Initialize as empty slice (not nil) for JSON serialization
	executions := make([]*loomv1.ScheduleExecution, 0)

	for rows.Next() {
		var (
			exec     loomv1.ScheduleExecution
			errorMsg sql.NullString
		)

		err := rows.Scan(
			&exec.ExecutionId,
			&exec.StartedAt,
			&exec.CompletedAt,
			&exec.Status,
			&errorMsg,
			&exec.DurationMs,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan execution: %w", err)
		}

		if errorMsg.Valid {
			exec.Error = errorMsg.String
		}

		executions = append(executions, &exec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating executions: %w", err)
	}

	return executions, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		return s.db.Close()
	}

	return nil
}

// GetDueSchedules returns all schedules that should execute now.
// A schedule is due if its next_execution_at is <= current time and it's enabled.
func (s *Store) GetDueSchedules(ctx context.Context, currentTime int64) ([]*loomv1.ScheduledWorkflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT id, workflow_name, yaml_path, pattern_json, schedule_json,
		       last_execution_at, next_execution_at, current_execution_id,
		       total_executions, successful_executions, failed_executions, skipped_executions,
		       last_status, last_error, created_at, updated_at
		FROM scheduled_workflows
		WHERE next_execution_at > 0 AND next_execution_at <= ?
		ORDER BY next_execution_at ASC
	`

	rows, err := s.db.QueryContext(ctx, query, currentTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query due schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*loomv1.ScheduledWorkflow

	for rows.Next() {
		var (
			schedule           loomv1.ScheduledWorkflow
			patternJSON        string
			scheduleJSON       string
			lastStatus         sql.NullString
			lastError          sql.NullString
			currentExecutionID sql.NullString
			yamlPath           sql.NullString
			totalExecs         int32
			successfulExecs    int32
			failedExecs        int32
			skippedExecs       int32
		)

		err := rows.Scan(
			&schedule.Id,
			&schedule.WorkflowName,
			&yamlPath,
			&patternJSON,
			&scheduleJSON,
			&schedule.LastExecutionAt,
			&schedule.NextExecutionAt,
			&currentExecutionID,
			&totalExecs,
			&successfulExecs,
			&failedExecs,
			&skippedExecs,
			&lastStatus,
			&lastError,
			&schedule.CreatedAt,
			&schedule.UpdatedAt,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan schedule: %w", err)
		}

		// Set nullable fields
		if yamlPath.Valid {
			schedule.YamlPath = yamlPath.String
		}
		if currentExecutionID.Valid {
			schedule.CurrentExecutionId = currentExecutionID.String
		}

		// Unmarshal pattern
		schedule.Pattern = &loomv1.WorkflowPattern{}
		if err := protojson.Unmarshal([]byte(patternJSON), schedule.Pattern); err != nil {
			s.logger.Error("Failed to unmarshal pattern for schedule",
				zap.String("schedule_id", schedule.Id),
				zap.Error(err))
			continue
		}

		// Unmarshal schedule config
		schedule.Schedule = &loomv1.ScheduleConfig{}
		if err := protojson.Unmarshal([]byte(scheduleJSON), schedule.Schedule); err != nil {
			s.logger.Error("Failed to unmarshal schedule config",
				zap.String("schedule_id", schedule.Id),
				zap.Error(err))
			continue
		}

		// Only include enabled schedules
		if !schedule.Schedule.Enabled {
			continue
		}

		// Build stats
		schedule.Stats = &loomv1.ScheduleStats{
			TotalExecutions:      totalExecs,
			SuccessfulExecutions: successfulExecs,
			FailedExecutions:     failedExecs,
			SkippedExecutions:    skippedExecs,
		}
		if lastStatus.Valid {
			schedule.Stats.LastStatus = lastStatus.String
		}
		if lastError.Valid {
			schedule.Stats.LastError = lastError.String
		}

		schedules = append(schedules, &schedule)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating schedules: %w", err)
	}

	return schedules, nil
}
