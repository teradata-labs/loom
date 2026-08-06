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

//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
)

// viewReaderRole is a non-superuser, NOLOGIN role used to read the analytics
// views the way an external consumer (e.g. Dreambase via Supabase) would: a
// restricted role that is *subject* to row-level security. The Postgres
// superuser bypasses RLS entirely, so isolation can only be observed through a
// role like this. The analytics views carry no user_id filter of their own;
// they delegate isolation to the caller's RLS, and `security_invoker = true`
// guarantees they never elevate beyond the caller.
const viewReaderRole = "loom_view_reader"

// scalarInt64 runs a single-value query as the connection's default role
// (superuser in tests) inside an RLS-context transaction.
func scalarInt64(t *testing.T, pool *pgxpool.Pool, userID, query string) int64 {
	t.Helper()
	var n int64
	ctx := ContextWithUserID(context.Background(), userID)
	err := execInTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, query).Scan(&n)
	})
	require.NoError(t, err, "query failed for user %s: %s", userID, query)
	return n
}

// ensureViewReaderRole creates the restricted reader role (idempotent) and
// grants it SELECT on the base tables and views, so it can read through the
// security_invoker views while remaining subject to RLS.
func ensureViewReaderRole(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	err := execInTxNoRLS(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DO $$ BEGIN
			IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'loom_view_reader') THEN
				CREATE ROLE loom_view_reader NOLOGIN;
			END IF;
		END $$;`); err != nil {
			return err
		}
		for _, rel := range []string{
			"sessions", "messages", "tool_executions", "tasks",
			"cost_per_agent_day", "tool_outcomes", "task_throughput",
		} {
			if _, err := tx.Exec(ctx, "GRANT SELECT ON "+rel+" TO loom_view_reader"); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err, "failed to provision %s role", viewReaderRole)
}

// scalarInt64AsReader runs a single-value query as the restricted, RLS-subject
// reader role with app.current_user_id set to userID. This mirrors how an
// external analytics consumer reads the views with RLS in force.
func scalarInt64AsReader(t *testing.T, pool *pgxpool.Pool, userID, query string) int64 {
	t.Helper()
	var n int64
	err := execInTxNoRLS(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_user_id', $1, true)", userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+viewReaderRole); err != nil {
			return err
		}
		return tx.QueryRow(ctx, query).Scan(&n)
	})
	require.NoError(t, err, "reader query failed for user %s: %s", userID, query)
	return n
}

// TestAnalyticsViews_Exist verifies migration 000014 applied: all three views
// exist, are queryable, and carry security_invoker=true (the security contract).
func TestAnalyticsViews_Exist(t *testing.T) {
	pool := testPool(t)
	user := uniqueID("user-views")

	for _, view := range []string{"cost_per_agent_day", "tool_outcomes", "task_throughput"} {
		_ = scalarInt64(t, pool, user, "SELECT COUNT(*) FROM "+view)
	}

	assert.Equal(t, int64(3), scalarInt64(t, pool, user,
		"SELECT COUNT(*) FROM pg_views WHERE viewname IN ('cost_per_agent_day','tool_outcomes','task_throughput')"),
		"all three analytics views must exist as views")

	// Every view MUST be security_invoker=true so it never bypasses base-table RLS.
	assert.Equal(t, int64(3), scalarInt64(t, pool, user, `
		SELECT COUNT(*) FROM pg_class
		WHERE relkind = 'v'
		  AND relname IN ('cost_per_agent_day','tool_outcomes','task_throughput')
		  AND reloptions @> ARRAY['security_invoker=true']`),
		"all three views must be defined WITH (security_invoker = true)")
}

// TestAnalyticsViews_CostPerAgentDay_RLS verifies that, read through a restricted
// RLS-subject role, cost_per_agent_day only aggregates rows the querying user can
// see — one user's message counts never leak into another user's totals.
func TestAnalyticsViews_CostPerAgentDay_RLS(t *testing.T) {
	pool := testPool(t)
	ensureViewReaderRole(t, pool)
	store := testSessionStore(t, pool)

	userA := uniqueID("user-a")
	userB := uniqueID("user-b")
	sessA := uniqueID("sess-a")
	sessB := uniqueID("sess-b")

	createTestSession(t, store, userA, sessA, "agent-a")
	createTestSession(t, store, userB, sessB, "agent-b")

	createTestMessage(t, store, userA, sessA, "a1")
	createTestMessage(t, store, userA, sessA, "a2")
	createTestMessage(t, store, userA, sessA, "a3")
	createTestMessage(t, store, userB, sessB, "b1")

	const q = "SELECT COALESCE(SUM(messages), 0)::bigint FROM cost_per_agent_day"
	assert.Equal(t, int64(3), scalarInt64AsReader(t, pool, userA, q),
		"User A must see exactly their own 3 messages, none of User B's")
	assert.Equal(t, int64(1), scalarInt64AsReader(t, pool, userB, q),
		"User B must see exactly their own 1 message, none of User A's")
}

// TestAnalyticsViews_ToolOutcomes_RLS verifies tool_outcomes respects RLS when
// read through the restricted role: a tool execution recorded by one user is not
// counted for another.
func TestAnalyticsViews_ToolOutcomes_RLS(t *testing.T) {
	pool := testPool(t)
	ensureViewReaderRole(t, pool)
	store := testSessionStore(t, pool)

	userA := uniqueID("user-a")
	userB := uniqueID("user-b")
	sessA := uniqueID("sess-a")

	createTestSession(t, store, userA, sessA, "agent-a")

	ctxA := ContextWithUserID(context.Background(), userA)
	require.NoError(t, store.SaveToolExecution(ctxA, sessA, agent.ToolExecution{
		ToolName: "view_test_tool",
		Input:    map[string]interface{}{"k": "v"},
	}), "User A should record a successful tool execution")

	const q = "SELECT COALESCE(SUM(success_count), 0)::bigint FROM tool_outcomes WHERE tool_name = 'view_test_tool'"
	assert.Equal(t, int64(1), scalarInt64AsReader(t, pool, userA, q),
		"User A must see their 1 successful tool execution")
	assert.Equal(t, int64(0), scalarInt64AsReader(t, pool, userB, q),
		"User B must NOT see User A's tool execution")
}
