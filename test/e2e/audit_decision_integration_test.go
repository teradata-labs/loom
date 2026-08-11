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

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage/postgres"
)

// defaultPostgresDSN is the storage surface's real postgres, stood up by the pack
// prepare (test/e2e/docker-compose.yml on :5433) — the same database the running
// looms server uses.
const defaultPostgresDSN = "postgres://loom_test:loom_test_pass@localhost:5433/loom_test?sslmode=disable"

// postgresDSN returns the DSN for the real integration postgres, honoring an
// override env, else the pack default.
func postgresDSN() string {
	if dsn := os.Getenv("LOOM_E2E_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	if dsn := os.Getenv("TEST_POSTGRES_URL"); dsn != "" {
		return dsn
	}
	return defaultPostgresDSN
}

// auditTestPool opens a pgxpool to the real integration postgres and ensures the
// schema is migrated. The pool is closed via t.Cleanup.
func auditTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, postgresDSN())
	require.NoError(t, err, "failed to connect to integration postgres at %s", postgresDSN())
	t.Cleanup(pool.Close)

	migrator, err := postgres.NewMigrator(pool, observability.NewNoOpTracer())
	require.NoError(t, err, "failed to create migrator")
	require.NoError(t, migrator.MigrateUp(ctx), "failed to run migrations")

	return pool
}

// TestE2E_ToolExecutions_AuditDecision verifies the D-6 audit trail on persisted
// tool_executions rows against real postgres: the admission decision each governed
// call carries, that exactly the matching calls (including a denied one) become
// audit records, that non-matching calls carry none, and that the rows are
// per-user isolated.
//
// The audit binding runs upstream (pkg/shuttle admission chain → executor stamps
// result.Metadata["admission.decision"] → agent reads it into
// ToolExecution.AdmissionDecision); at the storage surface a matched call is one
// whose ToolExecution carries a non-empty AdmissionDecision, and a non-matching
// call is one that carries none. This drives SaveToolExecution with both and reads
// the persisted rows back.
func TestE2E_ToolExecutions_AuditDecision(t *testing.T) {
	if !isPostgres() {
		t.Skip("audit-decision RLS assertions require the postgres backend")
	}

	pool := auditTestPool(t)
	store := postgres.NewSessionStore(pool, observability.NewNoOpTracer(), nil)

	userA := uniqueTestID("user-a")
	userB := uniqueTestID("user-b")
	sessionID := uniqueTestID("sess-audit")

	ctxA := postgres.ContextWithUserID(context.Background(), userA)
	require.NoError(t, store.SaveSession(ctxA, &agent.Session{
		ID:        sessionID,
		AgentID:   "test-agent",
		UserID:    userA,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}), "userA should be able to create the session")
	t.Cleanup(func() {
		_ = store.DeleteSession(postgres.ContextWithUserID(context.Background(), userA), sessionID)
	})

	// Matching (audited) calls: two allowed + one denied. The denied call's run
	// outcome is a failure (Success=false, permission_denied) yet its admission
	// decision is "deny" — decision is distinct from outcome (ac1).
	audited := []agent.ToolExecution{
		{
			ToolName:          "list_files",
			Input:             map[string]interface{}{"path": "/"},
			Result:            &shuttle.Result{Success: true},
			AdmissionDecision: "allow",
		},
		{
			ToolName:          "read_file",
			Input:             map[string]interface{}{"path": "/etc/hosts"},
			Result:            &shuttle.Result{Success: true},
			AdmissionDecision: "allow",
		},
		{
			ToolName: "delete_all",
			Input:    map[string]interface{}{"path": "/"},
			Result: &shuttle.Result{
				Success: false,
				Error: &shuttle.Error{
					Code:    "permission_denied",
					Message: "permission_denied: blocked by admission policy",
				},
			},
			AdmissionDecision: "deny",
		},
	}
	// Non-matching calls: the audit matcher selected no calls, so these carry no
	// decision and must not become audit records (ac3).
	nonMatching := []agent.ToolExecution{
		{
			ToolName: "ping",
			Input:    map[string]interface{}{},
			Result:   &shuttle.Result{Success: true},
		},
		{
			ToolName: "status",
			Input:    map[string]interface{}{},
			Result:   &shuttle.Result{Success: true},
		},
	}
	wantAudited := len(audited)         // 3 matching calls -> 3 audit records
	wantNonMatching := len(nonMatching) // 2 non-matching calls -> 0 audit records

	for _, exec := range append(append([]agent.ToolExecution{}, audited...), nonMatching...) {
		require.NoError(t, store.SaveToolExecution(ctxA, sessionID, exec),
			"userA should be able to persist tool execution %q", exec.ToolName)
	}

	ctx := context.Background()

	// ac2: one audit record per matching call — count(rows with a decision) equals
	// the number of matching calls (SC-004), the denied call included.
	var auditRecordCount int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM tool_executions WHERE session_id=$1 AND admission_decision IS NOT NULL",
		sessionID).Scan(&auditRecordCount))
	assert.Equal(t, wantAudited, auditRecordCount,
		"count(audit records) must equal count(matching calls), denied call included")

	// ac3: the non-matching calls persisted rows but no audit records (NULL decision).
	var nonMatchingRows int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM tool_executions WHERE session_id=$1 AND admission_decision IS NULL",
		sessionID).Scan(&nonMatchingRows))
	assert.Equal(t, wantNonMatching, nonMatchingRows,
		"non-matching calls must persist rows carrying no admission decision (not audit records)")

	// ac1: the denied call's record carries decision "deny", distinct from its
	// failed run outcome (error column = permission_denied).
	var denyDecision, denyError string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT admission_decision, error FROM tool_executions WHERE session_id=$1 AND tool_name='delete_all'",
		sessionID).Scan(&denyDecision, &denyError))
	assert.Equal(t, "deny", denyDecision,
		"a denied call's record must show a deny admission decision")
	assert.Contains(t, denyError, "permission_denied",
		"the denied call's run outcome (error) must be present and distinct from its deny decision")

	// ac4: per-user isolation of the persisted rows (real postgres user_id scoping).
	// userA sees exactly its own executions; userB sees none.
	statsA, err := store.GetStats(ctxA)
	require.NoError(t, err)
	assert.Equal(t, wantAudited+wantNonMatching, statsA.ToolExecutionCount,
		"userA must see all of its own tool executions")

	ctxB := postgres.ContextWithUserID(context.Background(), userB)
	statsB, err := store.GetStats(ctxB)
	require.NoError(t, err)
	assert.Equal(t, 0, statsB.ToolExecutionCount,
		"userB must see zero of userA's tool executions (incl. audit records)")
}
