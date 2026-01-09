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
	"fmt"

	"github.com/teradata-labs/loom/pkg/observability"
)

// InitSelfImprovementSchema creates database tables for self-improvement tracking.
// This extends the existing metaagent_deployments table with runtime metrics
// and improvement history tracking.
func InitSelfImprovementSchema(ctx context.Context, db *sql.DB, tracer observability.Tracer) error {
	ctx, span := tracer.StartSpan(ctx, "metaagent.learning.init_self_improvement_schema")
	defer tracer.EndSpan(span)

	schema := `
	-- Runtime pattern effectiveness (complements metaagent_deployments)
	-- Tracks pattern usage across all agents, not just metaagent-generated ones
	-- Supports A/B testing and canary deployments via variant tracking
	CREATE TABLE IF NOT EXISTS pattern_effectiveness (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		pattern_name TEXT NOT NULL,
		variant TEXT DEFAULT 'default',  -- A/B testing variant (default, control, treatment, etc.)
		domain TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		window_start INTEGER NOT NULL,  -- Unix timestamp
		window_end INTEGER NOT NULL,

		-- Usage metrics
		total_usages INTEGER NOT NULL,
		success_count INTEGER NOT NULL,
		failure_count INTEGER NOT NULL,
		success_rate REAL NOT NULL,

		-- Performance metrics
		avg_cost_usd REAL,
		avg_latency_ms INTEGER,
		error_types_json TEXT,  -- JSON map of error type → count

		-- Judge evaluation metrics (from multi-judge evaluation)
		judge_pass_rate REAL,  -- Percentage that passed judges (0-1)
		judge_avg_score REAL,  -- Average score across evaluations (0-100)
		judge_criterion_scores_json TEXT,  -- JSON map: "safety" -> 0.92, "cost" -> 0.85, etc.

		-- Context
		llm_provider TEXT,
		llm_model TEXT,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),

		UNIQUE(pattern_name, variant, agent_id, window_start)
	);

	-- Indexes for pattern_effectiveness
	CREATE INDEX IF NOT EXISTS idx_pattern_effectiveness_pattern
		ON pattern_effectiveness(pattern_name);
	CREATE INDEX IF NOT EXISTS idx_pattern_effectiveness_variant
		ON pattern_effectiveness(variant);
	CREATE INDEX IF NOT EXISTS idx_pattern_effectiveness_pattern_variant
		ON pattern_effectiveness(pattern_name, variant);
	CREATE INDEX IF NOT EXISTS idx_pattern_effectiveness_agent
		ON pattern_effectiveness(agent_id);
	CREATE INDEX IF NOT EXISTS idx_pattern_effectiveness_domain
		ON pattern_effectiveness(domain);
	CREATE INDEX IF NOT EXISTS idx_pattern_effectiveness_window
		ON pattern_effectiveness(window_start, window_end);
	CREATE INDEX IF NOT EXISTS idx_pattern_effectiveness_success_rate
		ON pattern_effectiveness(success_rate);

	-- Improvement history
	-- Tracks all improvement proposals and their outcomes
	CREATE TABLE IF NOT EXISTS improvement_history (
		id TEXT PRIMARY KEY,  -- UUID
		type TEXT NOT NULL,   -- pattern_add, pattern_remove, parameter_tune, template_adjust, config_adjust
		description TEXT,
		confidence REAL,
		impact TEXT,  -- low, medium, high, critical
		details_json TEXT,  -- JSON: PatternChange, expected deltas, rationale

		-- Application tracking
		status TEXT NOT NULL,  -- pending, approved, applying, applied, canary_testing, rolled_back, rejected
		created_at INTEGER NOT NULL,
		applied_at INTEGER,
		applied_by TEXT,  -- "meta-agent" or "human:{user_id}"

		-- Target
		target_agent_id TEXT NOT NULL,
		domain TEXT NOT NULL,
		target_pattern TEXT,

		-- Pre/post metrics (for effectiveness tracking)
		pre_success_rate REAL,
		post_success_rate REAL,
		pre_cost_usd REAL,
		post_cost_usd REAL,
		pre_latency_ms INTEGER,
		post_latency_ms INTEGER,

		-- Rollback tracking
		rolled_back INTEGER DEFAULT 0,
		rollback_reason TEXT,
		rollback_at INTEGER,

		-- Canary test info (JSON)
		canary_test_json TEXT
	);

	-- Indexes for improvement_history
	CREATE INDEX IF NOT EXISTS idx_improvement_history_agent
		ON improvement_history(target_agent_id);
	CREATE INDEX IF NOT EXISTS idx_improvement_history_domain
		ON improvement_history(domain);
	CREATE INDEX IF NOT EXISTS idx_improvement_history_status
		ON improvement_history(status);
	CREATE INDEX IF NOT EXISTS idx_improvement_history_created_at
		ON improvement_history(created_at);
	CREATE INDEX IF NOT EXISTS idx_improvement_history_type
		ON improvement_history(type);

	-- Config snapshots (for rollback)
	-- Stores previous configurations before applying improvements
	CREATE TABLE IF NOT EXISTS config_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		improvement_id TEXT NOT NULL,
		target_agent_id TEXT NOT NULL,
		pattern_name TEXT,

		-- Configs (YAML strings)
		previous_config TEXT NOT NULL,
		new_config TEXT NOT NULL,
		created_at INTEGER NOT NULL,

		FOREIGN KEY (improvement_id) REFERENCES improvement_history(id) ON DELETE CASCADE
	);

	-- Indexes for config_snapshots
	CREATE INDEX IF NOT EXISTS idx_config_snapshots_improvement
		ON config_snapshots(improvement_id);
	CREATE INDEX IF NOT EXISTS idx_config_snapshots_agent
		ON config_snapshots(target_agent_id);
	CREATE INDEX IF NOT EXISTS idx_config_snapshots_created_at
		ON config_snapshots(created_at);
	`

	_, err := db.ExecContext(ctx, schema)
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("failed to initialize self-improvement schema: %w", err)
	}

	span.Status = observability.Status{Code: observability.StatusOK, Message: "Self-improvement schema initialized"}
	tracer.RecordMetric("metaagent.schema.init", 1.0, map[string]string{
		"success": "true",
	})

	return nil
}
