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
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
)

// AdminStore implements agent.AdminStorage using PostgreSQL without RLS.
// All queries use execInTxNoRLS to bypass row-level security policies,
// providing cross-tenant visibility for platform administrators.
type AdminStore struct {
	pool   *pgxpool.Pool
	tracer observability.Tracer
}

// NewAdminStore creates a new admin store with the given connection pool.
func NewAdminStore(pool *pgxpool.Pool, tracer observability.Tracer) *AdminStore {
	if tracer == nil {
		tracer = observability.NewNoOpTracer()
	}
	return &AdminStore{
		pool:   pool,
		tracer: tracer,
	}
}

// ListAllSessions returns sessions across all users (bypasses RLS).
func (s *AdminStore) ListAllSessions(ctx context.Context, limit, offset int) ([]agent.AdminSession, int32, error) {
	ctx, span := s.tracer.StartSpan(ctx, "admin_store.list_all_sessions")
	defer s.tracer.EndSpan(span)

	var sessions []agent.AdminSession
	var totalCount int32

	err := execInTxNoRLS(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Get total count
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM sessions WHERE deleted_at IS NULL",
		).Scan(&totalCount); err != nil {
			return fmt.Errorf("failed to count sessions: %w", err)
		}

		// Query sessions with user_id
		rows, err := tx.Query(ctx, `
			SELECT id, user_id, agent_id, parent_session_id, created_at, updated_at,
			       total_cost_usd, total_tokens
			FROM sessions
			WHERE deleted_at IS NULL
			ORDER BY updated_at DESC
			LIMIT $1 OFFSET $2`,
			limit, offset,
		)
		if err != nil {
			return fmt.Errorf("failed to list sessions: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				id              string
				userID          string
				agentID         *string
				parentSessionID *string
				createdAt       time.Time
				updatedAt       time.Time
				totalCost       float64
				totalTokens     int64
			)

			if err := rows.Scan(&id, &userID, &agentID, &parentSessionID,
				&createdAt, &updatedAt, &totalCost, &totalTokens); err != nil {
				return fmt.Errorf("failed to scan session: %w", err)
			}

			sess := &agent.Session{
				ID:           id,
				CreatedAt:    createdAt,
				UpdatedAt:    updatedAt,
				TotalCostUSD: totalCost,
				TotalTokens:  int(totalTokens),
			}
			if agentID != nil {
				sess.AgentID = *agentID
			}
			if parentSessionID != nil {
				sess.ParentSessionID = *parentSessionID
			}

			sessions = append(sessions, agent.AdminSession{
				Session: sess,
				UserID:  userID,
			})
		}

		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}

	span.SetAttribute("total_count", fmt.Sprintf("%d", totalCount))
	span.SetAttribute("returned_count", fmt.Sprintf("%d", len(sessions)))
	return sessions, totalCount, nil
}

// CountSessionsByUser returns session counts grouped by user_id.
func (s *AdminStore) CountSessionsByUser(ctx context.Context) ([]agent.UserSessionCount, error) {
	ctx, span := s.tracer.StartSpan(ctx, "admin_store.count_sessions_by_user")
	defer s.tracer.EndSpan(span)

	var counts []agent.UserSessionCount

	err := execInTxNoRLS(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT user_id, COUNT(*) as session_count
			FROM sessions
			WHERE deleted_at IS NULL
			GROUP BY user_id
			ORDER BY session_count DESC`)
		if err != nil {
			return fmt.Errorf("failed to count sessions by user: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var userID string
			var count int32
			if err := rows.Scan(&userID, &count); err != nil {
				return fmt.Errorf("failed to scan user count: %w", err)
			}
			counts = append(counts, agent.UserSessionCount{
				UserID:       userID,
				SessionCount: count,
			})
		}

		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	span.SetAttribute("user_count", fmt.Sprintf("%d", len(counts)))
	return counts, nil
}

// GetSystemStats returns aggregate statistics across all users.
func (s *AdminStore) GetSystemStats(ctx context.Context) (*agent.SystemStats, error) {
	ctx, span := s.tracer.StartSpan(ctx, "admin_store.get_system_stats")
	defer s.tracer.EndSpan(span)

	var stats agent.SystemStats

	err := execInTxNoRLS(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Session count
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM sessions WHERE deleted_at IS NULL",
		).Scan(&stats.TotalSessions); err != nil {
			return fmt.Errorf("failed to count sessions: %w", err)
		}

		// Message count
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM messages WHERE deleted_at IS NULL",
		).Scan(&stats.TotalMessages); err != nil {
			return fmt.Errorf("failed to count messages: %w", err)
		}

		// Tool execution count
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM tool_executions",
		).Scan(&stats.TotalToolExecutions); err != nil {
			return fmt.Errorf("failed to count tool executions: %w", err)
		}

		// Distinct users
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(DISTINCT user_id) FROM sessions WHERE deleted_at IS NULL",
		).Scan(&stats.TotalUsers); err != nil {
			return fmt.Errorf("failed to count users: %w", err)
		}

		// Aggregate cost and tokens
		if err := tx.QueryRow(ctx,
			"SELECT COALESCE(SUM(total_cost_usd), 0), COALESCE(SUM(total_tokens), 0) FROM sessions WHERE deleted_at IS NULL",
		).Scan(&stats.TotalCostUSD, &stats.TotalTokens); err != nil {
			return fmt.Errorf("failed to sum costs/tokens: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &stats, nil
}
