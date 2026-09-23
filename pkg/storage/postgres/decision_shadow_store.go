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
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/decision"
	"github.com/teradata-labs/loom/pkg/observability"
)

// DefaultShadowQueryLimit caps a QueryShadow with Limit 0.
const DefaultShadowQueryLimit = 10_000

// DecisionShadowStore persists decision shadow rows in PostgreSQL
// (migration 000025). Rows are tenant-scoped through the same RLS setting the
// other per-user tables use; the user id comes from the context.
type DecisionShadowStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewDecisionShadowStore wraps a pool.
func NewDecisionShadowStore(pool *pgxpool.Pool, tracer observability.Tracer) *DecisionShadowStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &DecisionShadowStore{pool: pool, tracer: tracer}
}

// Compile-time interface check.
var _ decision.ShadowStore = (*DecisionShadowStore)(nil)

// RecordShadow inserts rows in one tenant-scoped transaction.
func (s *DecisionShadowStore) RecordShadow(ctx context.Context, records []*loomv1.DecisionShadowRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx, span := s.tracer.StartSpan(ctx, "pg.decision_shadow.record")
	defer s.tracer.EndSpan(span)
	span.SetAttribute("rows", len(records))

	return execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		userID := UserIDFromContext(ctx)
		batch := &pgx.Batch{}
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
			batch.Queue(`
				INSERT INTO decision_shadow (
					recorded_at, site, session_id, question_id, kind,
					candidate_answer, candidate_confidence, candidate_probabilities_json,
					reference_answer, reference_source,
					latency_ms, input_tokens, cost_usd, model, provider, path, user_id
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
				recordedAt, r.Site, r.SessionId, r.QuestionId, r.Kind,
				r.CandidateAnswer, r.CandidateConfidence, probs,
				r.ReferenceAnswer, r.ReferenceSource,
				r.LatencyMs, r.InputTokens, r.CostUsd, r.Model, r.Provider, r.Path.String(), userID,
			)
		}
		results := tx.SendBatch(ctx, batch)
		for i := 0; i < batch.Len(); i++ {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return fmt.Errorf("decision_shadow: insert: %w", err)
			}
		}
		// Close must succeed before the transaction commits; a batch error
		// surfaces here.
		if err := results.Close(); err != nil {
			return fmt.Errorf("decision_shadow: batch close: %w", err)
		}
		return nil
	})
}

// QueryShadow returns rows newest first, within the caller's tenant.
func (s *DecisionShadowStore) QueryShadow(ctx context.Context, q decision.ShadowQuery) ([]*loomv1.DecisionShadowRecord, error) {
	ctx, span := s.tracer.StartSpan(ctx, "pg.decision_shadow.query")
	defer s.tracer.EndSpan(span)

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultShadowQueryLimit
	}
	// Explicit tenant predicate in addition to RLS, as every other store in
	// this package does: the query is correct even where RLS is not in force
	// (an owner role before FORCE, or a policy dropped by hand). execInTx has
	// already refused an empty user ID by the time this runs.
	args := []any{UserIDFromContext(ctx)}
	where := "WHERE user_id = $1"
	if q.Site != "" {
		args = append(args, q.Site)
		where += fmt.Sprintf(" AND site = $%d", len(args))
	}
	if !q.Since.IsZero() {
		args = append(args, q.Since.UTC())
		where += fmt.Sprintf(" AND recorded_at >= $%d", len(args))
	}
	args = append(args, limit)
	limitArg := fmt.Sprintf("$%d", len(args))

	var out []*loomv1.DecisionShadowRecord
	err := execInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, recorded_at, site, session_id, question_id, kind,
			       candidate_answer, candidate_confidence, candidate_probabilities_json,
			       reference_answer, reference_source,
			       latency_ms, input_tokens, cost_usd, model, provider, path
			FROM decision_shadow `+where+`
			ORDER BY recorded_at DESC, id DESC
			LIMIT `+limitArg, args...)
		if err != nil {
			return fmt.Errorf("decision_shadow: query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id         int64
				recordedAt time.Time
				probsJSON  []byte
				path       string
				r          loomv1.DecisionShadowRecord
			)
			if err := rows.Scan(&id, &recordedAt, &r.Site, &r.SessionId, &r.QuestionId, &r.Kind,
				&r.CandidateAnswer, &r.CandidateConfidence, &probsJSON,
				&r.ReferenceAnswer, &r.ReferenceSource,
				&r.LatencyMs, &r.InputTokens, &r.CostUsd, &r.Model, &r.Provider, &path); err != nil {
				return fmt.Errorf("decision_shadow: scan: %w", err)
			}
			r.Id = strconv.FormatInt(id, 10)
			r.RecordedAt = timestamppb.New(recordedAt.UTC())
			if len(probsJSON) > 0 && string(probsJSON) != "null" {
				if err := json.Unmarshal(probsJSON, &r.CandidateProbabilities); err != nil {
					return fmt.Errorf("decision_shadow: probabilities for row %d: %w", id, err)
				}
			}
			r.Path = loomv1.DecisionPath(loomv1.DecisionPath_value[path])
			out = append(out, &r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	span.SetAttribute("rows", len(out))
	return out, nil
}
