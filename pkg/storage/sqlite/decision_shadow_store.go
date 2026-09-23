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

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/observability"
)

// DefaultShadowQueryLimit caps a QueryShadow with Limit 0.
const DefaultShadowQueryLimit = 10_000

// DecisionShadowStore persists decision shadow rows in SQLite
// (migration 000010).
type DecisionShadowStore struct {
	db     *sql.DB
	tracer observability.Tracer
}

// NewDecisionShadowStore wraps an open connection.
func NewDecisionShadowStore(db *sql.DB, tracer observability.Tracer) *DecisionShadowStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &DecisionShadowStore{db: db, tracer: tracer}
}

// Compile-time interface check.
var _ decision.ShadowStore = (*DecisionShadowStore)(nil)

// RecordShadow inserts rows in one transaction.
func (s *DecisionShadowStore) RecordShadow(ctx context.Context, records []*loomv1.DecisionShadowRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.decision_shadow.record")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("rows", len(records))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("decision_shadow: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO decision_shadow (
			recorded_at, site, session_id, question_id, kind,
			candidate_answer, candidate_confidence, candidate_probabilities_json,
			reference_answer, reference_source,
			latency_ms, input_tokens, cost_usd, model, provider, path, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("decision_shadow: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, r := range records {
		if r == nil {
			continue
		}
		probs, err := json.Marshal(r.CandidateProbabilities)
		if err != nil {
			return fmt.Errorf("decision_shadow: marshal probabilities: %w", err)
		}
		recordedAt := time.Now().UTC()
		if r.RecordedAt != nil {
			recordedAt = r.RecordedAt.AsTime()
		}
		if _, err := stmt.ExecContext(ctx,
			recordedAt.UnixMilli(), r.Site, r.SessionId, r.QuestionId, r.Kind,
			r.CandidateAnswer, r.CandidateConfidence, string(probs),
			r.ReferenceAnswer, r.ReferenceSource,
			r.LatencyMs, r.InputTokens, r.CostUsd, r.Model, r.Provider, r.Path.String(), r.Error,
		); err != nil {
			return fmt.Errorf("decision_shadow: insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("decision_shadow: commit: %w", err)
	}
	return nil
}

// QueryShadow returns rows newest first.
func (s *DecisionShadowStore) QueryShadow(ctx context.Context, q decision.ShadowQuery) ([]*loomv1.DecisionShadowRecord, error) {
	ctx, span := s.tracer.StartSpan(ctx, "sqlite.decision_shadow.query")
	defer s.tracer.EndSpan(span)

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultShadowQueryLimit
	}
	where := "WHERE 1=1"
	args := []any{}
	if q.Site != "" {
		where += " AND site = ?"
		args = append(args, q.Site)
	}
	if !q.Since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, q.Since.UTC().UnixMilli())
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, recorded_at, site, session_id, question_id, kind,
		       candidate_answer, candidate_confidence, candidate_probabilities_json,
		       reference_answer, reference_source,
		       latency_ms, input_tokens, cost_usd, model, provider, path, error
		FROM decision_shadow `+where+`
		ORDER BY recorded_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("decision_shadow: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*loomv1.DecisionShadowRecord
	for rows.Next() {
		var (
			id         int64
			recordedMs int64
			probsJSON  string
			path       string
			r          loomv1.DecisionShadowRecord
		)
		if err := rows.Scan(&id, &recordedMs, &r.Site, &r.SessionId, &r.QuestionId, &r.Kind,
			&r.CandidateAnswer, &r.CandidateConfidence, &probsJSON,
			&r.ReferenceAnswer, &r.ReferenceSource,
			&r.LatencyMs, &r.InputTokens, &r.CostUsd, &r.Model, &r.Provider, &path, &r.Error); err != nil {
			return nil, fmt.Errorf("decision_shadow: scan: %w", err)
		}
		r.Id = strconv.FormatInt(id, 10)
		r.RecordedAt = timestamppb.New(time.UnixMilli(recordedMs).UTC())
		if probsJSON != "" && probsJSON != "null" {
			if err := json.Unmarshal([]byte(probsJSON), &r.CandidateProbabilities); err != nil {
				return nil, fmt.Errorf("decision_shadow: probabilities for row %d: %w", id, err)
			}
		}
		r.Path = loomv1.DecisionPath(loomv1.DecisionPath_value[path])
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decision_shadow: rows: %w", err)
	}
	span.SetAttribute("rows", len(out))
	return out, nil
}
