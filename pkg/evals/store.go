// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store manages persistent storage of eval results
type Store struct {
	db *sql.DB
}

// NewStore creates a new eval store
// Use ":memory:" for in-memory database (useful for testing)
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	store := &Store{db: db}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// initSchema creates the database tables
func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS eval_results (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		suite_name TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		run_at TIMESTAMP NOT NULL,
		passed BOOLEAN NOT NULL,
		failure_reason TEXT,

		-- Overall metrics
		total_tests INTEGER NOT NULL,
		passed_tests INTEGER NOT NULL,
		failed_tests INTEGER NOT NULL,
		accuracy REAL NOT NULL,
		total_cost_usd REAL NOT NULL,
		total_latency_ms INTEGER NOT NULL,
		total_tool_calls INTEGER NOT NULL,
		custom_metrics TEXT, -- JSON

		-- Full result as JSON (for detailed analysis)
		result_json TEXT NOT NULL,

		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_eval_suite_name ON eval_results(suite_name);
	CREATE INDEX IF NOT EXISTS idx_eval_agent_id ON eval_results(agent_id);
	CREATE INDEX IF NOT EXISTS idx_eval_run_at ON eval_results(run_at);

	CREATE TABLE IF NOT EXISTS test_case_results (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		eval_result_id INTEGER NOT NULL,
		test_name TEXT NOT NULL,
		passed BOOLEAN NOT NULL,
		failure_reason TEXT,
		actual_output TEXT,
		tools_used TEXT, -- JSON array
		cost_usd REAL NOT NULL,
		latency_ms INTEGER NOT NULL,
		golden_similarity REAL,
		golden_matched BOOLEAN,

		FOREIGN KEY (eval_result_id) REFERENCES eval_results(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_test_eval_result_id ON test_case_results(eval_result_id);
	CREATE INDEX IF NOT EXISTS idx_test_name ON test_case_results(test_name);
	`

	_, err := s.db.Exec(schema)
	return err
}

// Save saves an eval result to the database
func (s *Store) Save(ctx context.Context, result *loomv1.EvalResult) (int64, error) {
	// Serialize custom metrics
	customMetricsJSON, err := json.Marshal(result.Overall.CustomMetrics)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal custom metrics: %w", err)
	}

	// Serialize full result
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal result: %w", err)
	}

	// Start transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Insert eval result
	insertResult := `
		INSERT INTO eval_results (
			suite_name, agent_id, run_at, passed, failure_reason,
			total_tests, passed_tests, failed_tests, accuracy,
			total_cost_usd, total_latency_ms, total_tool_calls,
			custom_metrics, result_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	res, err := tx.ExecContext(ctx, insertResult,
		result.SuiteName,
		result.AgentId,
		result.RunAt.AsTime().UTC(), // Store in UTC for consistent comparisons
		result.Passed,
		result.FailureReason,
		result.Overall.TotalTests,
		result.Overall.PassedTests,
		result.Overall.FailedTests,
		result.Overall.Accuracy,
		result.Overall.TotalCostUsd,
		result.Overall.TotalLatencyMs,
		result.Overall.TotalToolCalls,
		string(customMetricsJSON),
		string(resultJSON),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert eval result: %w", err)
	}

	evalResultID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert ID: %w", err)
	}

	// Insert test case results
	insertTestCase := `
		INSERT INTO test_case_results (
			eval_result_id, test_name, passed, failure_reason,
			actual_output, tools_used, cost_usd, latency_ms,
			golden_similarity, golden_matched
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	for _, testResult := range result.TestResults {
		toolsJSON, err := json.Marshal(testResult.ToolsUsed)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal tools: %w", err)
		}

		var goldenSimilarity *float64
		var goldenMatched *bool
		if testResult.GoldenResult != nil {
			goldenSimilarity = &testResult.GoldenResult.SimilarityScore
			goldenMatched = &testResult.GoldenResult.Matched
		}

		_, err = tx.ExecContext(ctx, insertTestCase,
			evalResultID,
			testResult.TestName,
			testResult.Passed,
			testResult.FailureReason,
			testResult.ActualOutput,
			string(toolsJSON),
			testResult.CostUsd,
			testResult.LatencyMs,
			goldenSimilarity,
			goldenMatched,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert test case result: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return evalResultID, nil
}

// Get retrieves an eval result by ID
func (s *Store) Get(ctx context.Context, id int64) (*loomv1.EvalResult, error) {
	query := `SELECT result_json FROM eval_results WHERE id = ?`

	var resultJSON string
	err := s.db.QueryRowContext(ctx, query, id).Scan(&resultJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("eval result not found: %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query eval result: %w", err)
	}

	var result loomv1.EvalResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

// ListBySuite lists all eval results for a suite
func (s *Store) ListBySuite(ctx context.Context, suiteName string, limit int) ([]*loomv1.EvalResult, error) {
	query := `
		SELECT result_json
		FROM eval_results
		WHERE suite_name = ?
		ORDER BY run_at DESC
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, suiteName, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query eval results: %w", err)
	}
	defer rows.Close()

	var results []*loomv1.EvalResult
	for rows.Next() {
		var resultJSON string
		if err := rows.Scan(&resultJSON); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		var result loomv1.EvalResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal result: %w", err)
		}

		results = append(results, &result)
	}

	return results, rows.Err()
}

// ListByAgent lists all eval results for an agent
func (s *Store) ListByAgent(ctx context.Context, agentID string, limit int) ([]*loomv1.EvalResult, error) {
	query := `
		SELECT result_json
		FROM eval_results
		WHERE agent_id = ?
		ORDER BY run_at DESC
		LIMIT ?
	`

	rows, err := s.db.QueryContext(ctx, query, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query eval results: %w", err)
	}
	defer rows.Close()

	var results []*loomv1.EvalResult
	for rows.Next() {
		var resultJSON string
		if err := rows.Scan(&resultJSON); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		var result loomv1.EvalResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal result: %w", err)
		}

		results = append(results, &result)
	}

	return results, rows.Err()
}

// GetLatest gets the most recent eval result for a suite
func (s *Store) GetLatest(ctx context.Context, suiteName string) (*loomv1.EvalResult, error) {
	query := `
		SELECT result_json
		FROM eval_results
		WHERE suite_name = ?
		ORDER BY run_at DESC
		LIMIT 1
	`

	var resultJSON string
	err := s.db.QueryRowContext(ctx, query, suiteName).Scan(&resultJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no results found for suite: %s", suiteName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query eval result: %w", err)
	}

	var result loomv1.EvalResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

// GetSummary gets a summary of all eval results
func (s *Store) GetSummary(ctx context.Context) (*EvalSummary, error) {
	query := `
		SELECT
			COUNT(*) as total_runs,
			SUM(CASE WHEN passed = 1 THEN 1 ELSE 0 END) as passed_runs,
			AVG(accuracy) as avg_accuracy,
			SUM(total_cost_usd) as total_cost,
			COUNT(DISTINCT suite_name) as total_suites,
			COUNT(DISTINCT agent_id) as total_agents
		FROM eval_results
	`

	var summary EvalSummary
	err := s.db.QueryRowContext(ctx, query).Scan(
		&summary.TotalRuns,
		&summary.PassedRuns,
		&summary.AvgAccuracy,
		&summary.TotalCost,
		&summary.TotalSuites,
		&summary.TotalAgents,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query summary: %w", err)
	}

	return &summary, nil
}

// GetTrends gets accuracy trends over time for a suite
func (s *Store) GetTrends(ctx context.Context, suiteName string, days int) ([]*TrendPoint, error) {
	query := `
		SELECT
			DATE(run_at) as date,
			AVG(accuracy) as avg_accuracy,
			AVG(total_cost_usd) as avg_cost,
			COUNT(*) as runs
		FROM eval_results
		WHERE suite_name = ?
			AND run_at >= datetime('now', '-' || ? || ' days')
		GROUP BY DATE(run_at)
		ORDER BY date ASC
	`

	rows, err := s.db.QueryContext(ctx, query, suiteName, days)
	if err != nil {
		return nil, fmt.Errorf("failed to query trends: %w", err)
	}
	defer rows.Close()

	var trends []*TrendPoint
	for rows.Next() {
		var trend TrendPoint
		var dateStr string
		err := rows.Scan(&dateStr, &trend.AvgAccuracy, &trend.AvgCost, &trend.Runs)
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		trend.Date, _ = time.Parse("2006-01-02", dateStr)
		trends = append(trends, &trend)
	}

	return trends, rows.Err()
}

// DeleteOlderThan deletes eval results older than the specified time
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	// Convert to UTC to ensure consistent comparison with stored timestamps
	query := `DELETE FROM eval_results WHERE run_at < ?`
	result, err := s.db.ExecContext(ctx, query, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("failed to delete old results: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	return count, nil
}

// EvalSummary represents a summary of eval results
type EvalSummary struct {
	TotalRuns   int
	PassedRuns  int
	AvgAccuracy float64
	TotalCost   float64
	TotalSuites int
	TotalAgents int
}

// TrendPoint represents a point in an accuracy trend
type TrendPoint struct {
	Date        time.Time
	AvgAccuracy float64
	AvgCost     float64
	Runs        int
}

// Compare compares two eval results and returns a comparison
func (s *Store) Compare(ctx context.Context, baselineID, candidateID int64) (*Comparison, error) {
	baseline, err := s.Get(ctx, baselineID)
	if err != nil {
		return nil, fmt.Errorf("failed to get baseline: %w", err)
	}

	candidate, err := s.Get(ctx, candidateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get candidate: %w", err)
	}

	return &Comparison{
		Baseline:        baseline,
		Candidate:       candidate,
		AccuracyDelta:   candidate.Overall.Accuracy - baseline.Overall.Accuracy,
		CostDelta:       candidate.Overall.TotalCostUsd - baseline.Overall.TotalCostUsd,
		LatencyDelta:    candidate.Overall.TotalLatencyMs - baseline.Overall.TotalLatencyMs,
		PassedTestDelta: candidate.Overall.PassedTests - baseline.Overall.PassedTests,
	}, nil
}

// Comparison represents a comparison between two eval results
type Comparison struct {
	Baseline        *loomv1.EvalResult
	Candidate       *loomv1.EvalResult
	AccuracyDelta   float64
	CostDelta       float64
	LatencyDelta    int64
	PassedTestDelta int32
}

// CreateMockResult creates a mock eval result for testing
func CreateMockResult(suiteName, agentID string, passed bool) *loomv1.EvalResult {
	return CreateMockResultWithTime(suiteName, agentID, passed, time.Now())
}

// CreateMockResultWithTime creates a mock eval result with a specific timestamp
func CreateMockResultWithTime(suiteName, agentID string, passed bool, runAt time.Time) *loomv1.EvalResult {
	return &loomv1.EvalResult{
		SuiteName: suiteName,
		AgentId:   agentID,
		RunAt:     timestamppb.New(runAt),
		Passed:    passed,
		Overall: &loomv1.EvalMetrics{
			TotalTests:     5,
			PassedTests:    4,
			FailedTests:    1,
			Accuracy:       0.8,
			TotalCostUsd:   0.50,
			TotalLatencyMs: 5000,
			TotalToolCalls: 10,
			CustomMetrics: map[string]float64{
				"cost_efficiency": 800.0,
			},
		},
		TestResults: []*loomv1.TestCaseResult{
			{
				TestName:     "test1",
				Passed:       true,
				ActualOutput: "output1",
				ToolsUsed:    []string{"tool1"},
				CostUsd:      0.10,
				LatencyMs:    1000,
			},
		},
	}
}
