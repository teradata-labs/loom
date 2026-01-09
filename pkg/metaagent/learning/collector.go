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
package learning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	_ "github.com/mutecomm/go-sqlcipher/v4"
	"github.com/teradata-labs/loom/pkg/observability"
)

// MetricsCollector collects and stores meta-agent deployment metrics
// All database operations are fully instrumented with observability tracing
type MetricsCollector struct {
	db     *sql.DB
	mu     sync.RWMutex
	tracer observability.Tracer
}

// DeploymentMetric represents metrics for a single agent deployment
type DeploymentMetric struct {
	AgentID          string
	Domain           DomainType
	Templates        []string // Which templates were considered
	SelectedTemplate string   // Which template was chosen
	Patterns         []string // Which patterns were selected
	Success          bool     // Did deployment succeed?
	ErrorMessage     string   // If failed, why?
	CostUSD          float64  // Generation cost
	TurnsUsed        int      // How many conversation turns
	CreatedAt        time.Time
	Metadata         map[string]string // Additional metadata
}

// DomainType represents the domain of an agent
type DomainType string

const (
	DomainSQL      DomainType = "sql"
	DomainREST     DomainType = "rest"
	DomainFile     DomainType = "file"
	DomainDocument DomainType = "document"
	DomainETL      DomainType = "etl"
	DomainUnknown  DomainType = "unknown"
)

// NewMetricsCollector creates a new metrics collector with SQLite storage
func NewMetricsCollector(dbPath string, tracer observability.Tracer) (*MetricsCollector, error) {
	ctx := context.Background()
	ctx, span := tracer.StartSpan(ctx, "metaagent.learning.collector.new")
	defer tracer.EndSpan(span)

	// Open SQLite database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	collector := &MetricsCollector{
		db:     db,
		tracer: tracer,
	}

	// Initialize schema
	if err := collector.initSchema(ctx); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	span.SetAttribute("db_path", dbPath)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Metrics collector initialized"}
	return collector, nil
}

// initSchema creates the database schema if it doesn't exist
func (mc *MetricsCollector) initSchema(ctx context.Context) error {
	ctx, span := mc.tracer.StartSpan(ctx, "metaagent.learning.init_schema")
	defer mc.tracer.EndSpan(span)

	schema := `
	CREATE TABLE IF NOT EXISTS metaagent_deployments (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id TEXT NOT NULL,
		domain TEXT NOT NULL,
		templates TEXT,              -- JSON array of template names
		selected_template TEXT,
		patterns TEXT,               -- JSON array of pattern names
		success INTEGER NOT NULL,    -- 0 = failed, 1 = succeeded
		error_message TEXT,
		cost_usd REAL,
		turns_used INTEGER,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		metadata TEXT,               -- JSON object
		session_count INTEGER DEFAULT 0,
		user_rating INTEGER,         -- 1-5 stars
		feedback_comments TEXT       -- User feedback text
	);

	CREATE INDEX IF NOT EXISTS idx_domain ON metaagent_deployments(domain);
	CREATE INDEX IF NOT EXISTS idx_success ON metaagent_deployments(success);
	CREATE INDEX IF NOT EXISTS idx_created_at ON metaagent_deployments(created_at);
	CREATE INDEX IF NOT EXISTS idx_template ON metaagent_deployments(selected_template);
	CREATE INDEX IF NOT EXISTS idx_agent_id ON metaagent_deployments(agent_id);
	`

	_, err := mc.db.ExecContext(ctx, schema)
	if err != nil {
		span.RecordError(err)
		return err
	}

	// Add new columns for existing databases (if they don't exist)
	alterStatements := []string{
		"ALTER TABLE metaagent_deployments ADD COLUMN session_count INTEGER DEFAULT 0",
		"ALTER TABLE metaagent_deployments ADD COLUMN user_rating INTEGER",
		"ALTER TABLE metaagent_deployments ADD COLUMN feedback_comments TEXT",
	}

	for _, stmt := range alterStatements {
		// Ignore errors if columns already exist
		_, _ = mc.db.ExecContext(ctx, stmt)
	}

	// Initialize self-improvement tables (pattern_effectiveness, improvement_history, config_snapshots)
	if err := InitSelfImprovementSchema(ctx, mc.db, mc.tracer); err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to initialize self-improvement schema: %w", err)
	}

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Schema initialized"}
	return nil
}

// RecordDeployment records a deployment metric to the database
func (mc *MetricsCollector) RecordDeployment(ctx context.Context, metric *DeploymentMetric) error {
	ctx, span := mc.tracer.StartSpan(ctx, "metaagent.learning.record_deployment")
	defer mc.tracer.EndSpan(span)

	// Add span attributes for observability
	span.SetAttribute("agent_id", metric.AgentID)
	span.SetAttribute("domain", string(metric.Domain))
	span.SetAttribute("success", metric.Success)
	span.SetAttribute("cost_usd", metric.CostUSD)
	span.SetAttribute("patterns_count", len(metric.Patterns))
	span.SetAttribute("selected_template", metric.SelectedTemplate)

	// Serialize JSON fields
	templatesJSON, err := json.Marshal(metric.Templates)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal templates: %w", err)
	}

	patternsJSON, err := json.Marshal(metric.Patterns)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal patterns: %w", err)
	}

	metadataJSON, err := json.Marshal(metric.Metadata)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Insert into database
	mc.mu.Lock()
	defer mc.mu.Unlock()

	successInt := 0
	if metric.Success {
		successInt = 1
	}

	query := `
		INSERT INTO metaagent_deployments
		(agent_id, domain, templates, selected_template, patterns, success, error_message, cost_usd, turns_used, created_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	result, err := mc.db.ExecContext(ctx, query,
		metric.AgentID,
		string(metric.Domain),
		string(templatesJSON),
		metric.SelectedTemplate,
		string(patternsJSON),
		successInt,
		metric.ErrorMessage,
		metric.CostUSD,
		metric.TurnsUsed,
		metric.CreatedAt.Unix(),
		string(metadataJSON),
	)

	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to insert deployment: %w", err)
	}

	// Get inserted ID
	id, err := result.LastInsertId()
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to get insert id: %w", err)
	}

	span.SetAttribute("deployment_id", id)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Deployment recorded successfully"}

	// Record metric for monitoring
	mc.tracer.RecordMetric("metaagent.deployments.total", 1.0, map[string]string{
		"domain":  string(metric.Domain),
		"success": fmt.Sprintf("%t", metric.Success),
	})

	mc.tracer.RecordMetric("metaagent.deployment.cost_usd", metric.CostUSD, map[string]string{
		"domain": string(metric.Domain),
	})

	return nil
}

// GetSuccessRate returns the success rate for a given domain
// If domain is empty, returns success rate across all domains
func (mc *MetricsCollector) GetSuccessRate(ctx context.Context, domain DomainType) (float64, error) {
	ctx, span := mc.tracer.StartSpan(ctx, "metaagent.learning.get_success_rate")
	defer mc.tracer.EndSpan(span)

	span.SetAttribute("domain", string(domain))

	mc.mu.RLock()
	defer mc.mu.RUnlock()

	var query string
	var args []interface{}

	if domain == "" {
		// All domains
		query = `
			SELECT
				COUNT(*) as total,
				COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0) as successful
			FROM metaagent_deployments
		`
	} else {
		// Specific domain
		query = `
			SELECT
				COUNT(*) as total,
				COALESCE(SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END), 0) as successful
			FROM metaagent_deployments
			WHERE domain = ?
		`
		args = append(args, string(domain))
	}

	var total, successful int
	var err error
	if len(args) > 0 {
		err = mc.db.QueryRowContext(ctx, query, args...).Scan(&total, &successful)
	} else {
		err = mc.db.QueryRowContext(ctx, query).Scan(&total, &successful)
	}

	if err != nil {
		span.RecordError(err)
		return 0, fmt.Errorf("failed to query success rate: %w", err)
	}

	var successRate float64
	if total > 0 {
		successRate = float64(successful) / float64(total)
	}

	span.SetAttribute("total_deployments", total)
	span.SetAttribute("successful_deployments", successful)
	span.SetAttribute("success_rate", successRate)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Success rate calculated"}

	// Record metric
	mc.tracer.RecordMetric("metaagent.success_rate", successRate, map[string]string{
		"domain": string(domain),
	})

	return successRate, nil
}

// GetPatternPerformance returns performance metrics for patterns in a domain
func (mc *MetricsCollector) GetPatternPerformance(ctx context.Context, domain DomainType) (map[string]*PatternMetrics, error) {
	ctx, span := mc.tracer.StartSpan(ctx, "metaagent.learning.get_pattern_performance")
	defer mc.tracer.EndSpan(span)

	span.SetAttribute("domain", string(domain))

	mc.mu.RLock()
	defer mc.mu.RUnlock()

	query := `
		SELECT patterns, success, cost_usd
		FROM metaagent_deployments
		WHERE domain = ?
	`

	rows, err := mc.db.QueryContext(ctx, query, string(domain))
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query pattern performance: %w", err)
	}
	defer rows.Close()

	// Aggregate pattern metrics
	patternStats := make(map[string]*PatternMetrics)

	for rows.Next() {
		var patternsJSON string
		var success int
		var costUSD float64

		if err := rows.Scan(&patternsJSON, &success, &costUSD); err != nil {
			span.RecordError(err)
			continue
		}

		// Deserialize patterns
		var patterns []string
		if err := json.Unmarshal([]byte(patternsJSON), &patterns); err != nil {
			continue
		}

		// Update stats for each pattern
		for _, pattern := range patterns {
			if _, exists := patternStats[pattern]; !exists {
				patternStats[pattern] = &PatternMetrics{
					Pattern: pattern,
				}
			}

			stats := patternStats[pattern]
			stats.UsageCount++
			stats.TotalCost += costUSD

			if success == 1 {
				stats.SuccessCount++
			}
		}
	}

	// Calculate success rates and average costs
	for _, stats := range patternStats {
		if stats.UsageCount > 0 {
			stats.SuccessRate = float64(stats.SuccessCount) / float64(stats.UsageCount)
			stats.AvgCost = stats.TotalCost / float64(stats.UsageCount)
		}
	}

	span.SetAttribute("patterns_analyzed", len(patternStats))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Pattern performance calculated"}

	return patternStats, nil
}

// GetTemplatePerformance returns performance metrics for templates in a domain
func (mc *MetricsCollector) GetTemplatePerformance(ctx context.Context, domain DomainType) (map[string]*TemplateMetrics, error) {
	ctx, span := mc.tracer.StartSpan(ctx, "metaagent.learning.get_template_performance")
	defer mc.tracer.EndSpan(span)

	span.SetAttribute("domain", string(domain))

	mc.mu.RLock()
	defer mc.mu.RUnlock()

	query := `
		SELECT
			selected_template,
			COUNT(*) as usage_count,
			SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) as success_count,
			AVG(cost_usd) as avg_cost
		FROM metaagent_deployments
		WHERE domain = ? AND selected_template IS NOT NULL
		GROUP BY selected_template
	`

	rows, err := mc.db.QueryContext(ctx, query, string(domain))
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query template performance: %w", err)
	}
	defer rows.Close()

	templateStats := make(map[string]*TemplateMetrics)

	for rows.Next() {
		var template string
		var usageCount, successCount int
		var avgCost float64

		if err := rows.Scan(&template, &usageCount, &successCount, &avgCost); err != nil {
			span.RecordError(err)
			continue
		}

		successRate := float64(successCount) / float64(usageCount)

		templateStats[template] = &TemplateMetrics{
			Template:     template,
			UsageCount:   usageCount,
			SuccessCount: successCount,
			SuccessRate:  successRate,
			AvgCost:      avgCost,
		}
	}

	span.SetAttribute("templates_analyzed", len(templateStats))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Template performance calculated"}

	return templateStats, nil
}

// GetRecentFailures returns recent failed deployments for analysis
func (mc *MetricsCollector) GetRecentFailures(ctx context.Context, domain DomainType, limit int) ([]*DeploymentMetric, error) {
	ctx, span := mc.tracer.StartSpan(ctx, "metaagent.learning.get_recent_failures")
	defer mc.tracer.EndSpan(span)

	span.SetAttribute("domain", string(domain))
	span.SetAttribute("limit", limit)

	mc.mu.RLock()
	defer mc.mu.RUnlock()

	query := `
		SELECT
			agent_id, domain, templates, selected_template, patterns,
			error_message, cost_usd, turns_used, created_at, metadata
		FROM metaagent_deployments
		WHERE domain = ? AND success = 0
		ORDER BY created_at DESC
		LIMIT ?
	`

	rows, err := mc.db.QueryContext(ctx, query, string(domain), limit)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("failed to query recent failures: %w", err)
	}
	defer rows.Close()

	var failures []*DeploymentMetric

	for rows.Next() {
		metric := &DeploymentMetric{
			Success:  false,
			Metadata: make(map[string]string), // Initialize metadata map
		}

		var templatesJSON, patternsJSON, metadataJSON string
		var createdAtUnix int64

		err := rows.Scan(
			&metric.AgentID,
			&metric.Domain,
			&templatesJSON,
			&metric.SelectedTemplate,
			&patternsJSON,
			&metric.ErrorMessage,
			&metric.CostUSD,
			&metric.TurnsUsed,
			&createdAtUnix,
			&metadataJSON,
		)

		if err != nil {
			span.RecordError(err)
			continue
		}

		// Deserialize JSON fields (ignore errors for now - will get zero values)
		if len(templatesJSON) > 0 {
			_ = json.Unmarshal([]byte(templatesJSON), &metric.Templates)
		}
		if len(patternsJSON) > 0 {
			_ = json.Unmarshal([]byte(patternsJSON), &metric.Patterns)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal([]byte(metadataJSON), &metric.Metadata)
		}

		metric.CreatedAt = time.Unix(createdAtUnix, 0)

		failures = append(failures, metric)
	}

	span.SetAttribute("failures_found", len(failures))
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Recent failures retrieved"}

	return failures, nil
}

// UpdateDeploymentFeedback updates a deployment record with post-deployment feedback
func (mc *MetricsCollector) UpdateDeploymentFeedback(ctx context.Context, agentID string, feedback interface{}) error {
	// INSTRUMENT: Start span
	ctx, span := mc.tracer.StartSpan(ctx, "metaagent.learning.update_feedback")
	defer mc.tracer.EndSpan(span)

	span.SetAttribute("agent_id", agentID)

	// Extract feedback fields using reflection (handles any struct with these fields)
	// This avoids circular import issues and works with any AgentFeedback struct
	v := reflect.ValueOf(feedback)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if v.Kind() != reflect.Struct {
		span.RecordError(fmt.Errorf("feedback must be a struct"))
		span.Status = observability.Status{Code: observability.StatusError, Message: "Invalid feedback type"}
		return fmt.Errorf("feedback must be a struct, got %T", feedback)
	}

	// Extract fields
	successField := v.FieldByName("Success")
	errorMsgField := v.FieldByName("ErrorMessage")
	turnsField := v.FieldByName("TurnsUsed")
	sessionField := v.FieldByName("SessionCount")
	ratingField := v.FieldByName("UserRating")
	commentsField := v.FieldByName("Comments")

	if !successField.IsValid() {
		span.RecordError(fmt.Errorf("missing Success field"))
		span.Status = observability.Status{Code: observability.StatusError, Message: "Invalid feedback struct"}
		return fmt.Errorf("feedback struct must have Success field")
	}

	success := successField.Bool()
	errorMsg := ""
	if errorMsgField.IsValid() && errorMsgField.Kind() == reflect.String {
		errorMsg = errorMsgField.String()
	}
	turnsUsed := 0
	if turnsField.IsValid() && turnsField.Kind() == reflect.Int {
		turnsUsed = int(turnsField.Int())
	}
	sessionCount := 0
	if sessionField.IsValid() && sessionField.Kind() == reflect.Int {
		sessionCount = int(sessionField.Int())
	}
	userRating := 0
	if ratingField.IsValid() && ratingField.Kind() == reflect.Int {
		userRating = int(ratingField.Int())
	}
	comments := ""
	if commentsField.IsValid() && commentsField.Kind() == reflect.String {
		comments = commentsField.String()
	}

	span.SetAttribute("success", success)

	mc.mu.Lock()
	defer mc.mu.Unlock()

	query := `
		UPDATE metaagent_deployments
		SET success = ?, error_message = ?, turns_used = ?, session_count = ?, user_rating = ?, feedback_comments = ?
		WHERE agent_id = ?
	`

	result, err := mc.db.ExecContext(ctx, query,
		boolToInt(success),
		errorMsg,
		turnsUsed,
		sessionCount,
		userRating,
		comments,
		agentID,
	)

	if err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		return fmt.Errorf("failed to update feedback: %w", err)
	}

	// Check if any rows were updated
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		span.RecordError(err)
		span.Status = observability.Status{Code: observability.StatusError, Message: err.Error()}
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		span.Status = observability.Status{Code: observability.StatusError, Message: "No deployment found with agent_id"}
		return fmt.Errorf("no deployment record found for agent_id: %s", agentID)
	}

	span.SetAttribute("rows_affected", rowsAffected)
	span.Status = observability.Status{Code: observability.StatusOK, Message: "Feedback updated"}

	// Record metrics
	mc.tracer.RecordMetric("metaagent.feedback.updates", 1.0, map[string]string{
		"success": fmt.Sprintf("%t", success),
	})

	return nil
}

// PatternMetrics contains performance metrics for a pattern
type PatternMetrics struct {
	Pattern      string
	UsageCount   int
	SuccessCount int
	SuccessRate  float64
	TotalCost    float64
	AvgCost      float64
}

// TemplateMetrics contains performance metrics for a template
type TemplateMetrics struct {
	Template     string
	UsageCount   int
	SuccessCount int
	SuccessRate  float64
	AvgCost      float64
}

// boolToInt converts a boolean to an integer for SQLite storage
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Close closes the database connection
func (mc *MetricsCollector) Close() error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.db != nil {
		return mc.db.Close()
	}
	return nil
}
