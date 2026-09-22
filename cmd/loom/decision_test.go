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

package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/storage/sqlite"
)

// seedTelemetryDB builds a loom.db with a session and a few tool executions,
// the way a running server leaves it.
func seedTelemetryDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loom.db")
	db, err := sql.Open("sqlite3", sqlitedriver.DSN(path, sqlitedriver.Options{BusyTimeoutMS: 5000, WAL: true, ForeignKeys: true}))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	migrator, err := sqlite.NewMigrator(db, observability.NewNoOpTracer())
	require.NoError(t, err)
	require.NoError(t, migrator.MigrateUp(ctx))

	now := time.Now().UnixMilli()
	_, err = db.ExecContext(ctx, `INSERT INTO sessions (id, name, agent_id, created_at, updated_at) VALUES ('sess-1', 'n', 'a', ?, ?)`, now, now)
	require.NoError(t, err)
	rows := []struct {
		tool  string
		input string
		err   *string
	}{
		{tool: "teradata:connect", input: `{"host":"h"}`, err: ptr(`MCP_CALL_FAILED: {"code":"session_handle_budget_full"}`)},
		{tool: "teradata:execute_query", input: `{"sql":"SELECT 1"}`, err: nil},
		{tool: "teradata:execute_query", input: `{"sql":"SELEC 1"}`, err: ptr("Syntax error: expected something between the beginning of the request and 'SELEC'")},
		{tool: "teradata:describe_table", input: `{"table":"nope"}`, err: ptr("Object 'nope' does not exist")},
	}
	for i, r := range rows {
		_, err = db.ExecContext(ctx, `INSERT INTO tool_executions (session_id, tool_name, input_json, error, execution_time_ms, timestamp) VALUES ('sess-1', ?, ?, ?, 5, ?)`,
			r.tool, r.input, r.err, now+int64(i))
		require.NoError(t, err)
	}
	return path
}

func ptr(s string) *string { return &s }

func resetDecisionFlags(t *testing.T) {
	t.Helper()
	prev := []any{decisionDBPath, decisionSite, decisionLimit, decisionSince, decisionDecider, decisionProvider, decisionModel, decisionDryRun, decisionErrors, decisionSample}
	t.Cleanup(func() {
		decisionDBPath = prev[0].(string)
		decisionSite = prev[1].(string)
		decisionLimit = prev[2].(int)
		decisionSince = prev[3].(string)
		decisionDecider = prev[4].(string)
		decisionProvider = prev[5].(string)
		decisionModel = prev[6].(string)
		decisionDryRun = prev[7].(bool)
		decisionErrors = prev[8].(bool)
		decisionSample = prev[9].(string)
	})
}

func TestDecisionReplaySampleFlag(t *testing.T) {
	resetDecisionFlags(t)
	path := seedTelemetryDB(t)
	decisionDBPath, decisionDecider, decisionLimit, decisionDryRun, decisionErrors = path, "mock", 100, true, false

	decisionSample = "random"
	var out bytes.Buffer
	decisionReplayCmd.SetOut(&out)
	require.NoError(t, runDecisionReplay(decisionReplayCmd, nil))
	assert.Contains(t, out.String(), "4 executions", "random sampling still reaches every row when the limit exceeds them")

	decisionSample = "shuffled"
	assert.Error(t, runDecisionReplay(decisionReplayCmd, nil), "unknown sample order is rejected")
}

func TestDecisionReplayAndReport(t *testing.T) {
	resetDecisionFlags(t)
	path := seedTelemetryDB(t)

	// Dry run: builds requests, writes nothing.
	decisionDBPath, decisionDecider, decisionLimit, decisionDryRun, decisionErrors = path, "mock", 100, true, false
	var out bytes.Buffer
	decisionReplayCmd.SetOut(&out)
	require.NoError(t, runDecisionReplay(decisionReplayCmd, nil))
	assert.Contains(t, out.String(), "(dry run)")
	assert.Contains(t, out.String(), "4 executions")

	// Real replay with the mock decider (answers not_a_failure for everything).
	decisionDryRun = false
	out.Reset()
	require.NoError(t, runDecisionReplay(decisionReplayCmd, nil))
	assert.Contains(t, out.String(), "8 shadow rows written", "2 questions × 4 executions")
	assert.Contains(t, out.String(), "0 decider errors")

	// Errors-only replay reads three rows.
	decisionErrors = true
	out.Reset()
	require.NoError(t, runDecisionReplay(decisionReplayCmd, nil))
	assert.Contains(t, out.String(), "replaying 3 tool executions")

	// Report over both replays.
	decisionSite, decisionLimit, decisionSince = sites.SiteFailureKind, 0, ""
	var rep bytes.Buffer
	decisionReportCmd.SetOut(&rep)
	require.NoError(t, runDecisionReport(decisionReportCmd, nil))
	md := rep.String()
	assert.Contains(t, md, "# Decision shadow report: tool.failure_kind")
	assert.Contains(t, md, "| Rows | 14 |", "8 rows from the full replay + 6 from the errors-only replay")
	assert.Contains(t, md, "## Confusion")
	assert.Contains(t, md, sites.KindServerSaturated)
	assert.Contains(t, md, sites.KindBadInput)
	assert.Contains(t, md, sites.KindNotFound)

	// Since filter that keeps everything.
	decisionSince = "2000-01-01T00:00:00Z"
	rep.Reset()
	require.NoError(t, runDecisionReport(decisionReportCmd, nil))
	assert.Contains(t, rep.String(), "| Rows | 14 |", "RFC3339 lower bound in the past keeps all rows")

	decisionSince = "not-a-duration"
	assert.Error(t, runDecisionReport(decisionReportCmd, nil))
}

func TestDecisionReplayConcurrent(t *testing.T) {
	resetDecisionFlags(t)
	path := seedTelemetryDB(t)
	decisionDBPath, decisionDecider, decisionLimit, decisionDryRun, decisionErrors = path, "mock", 100, false, false
	decisionConcurrency = 4
	t.Cleanup(func() { decisionConcurrency = 0 })
	var out bytes.Buffer
	decisionReplayCmd.SetOut(&out)
	require.NoError(t, runDecisionReplay(decisionReplayCmd, nil))
	assert.Contains(t, out.String(), "8 shadow rows written")
	assert.Contains(t, out.String(), "4 workers")

	decisionSite, decisionLimit, decisionSince = sites.SiteFailureKind, 0, ""
	var rep bytes.Buffer
	decisionReportCmd.SetOut(&rep)
	require.NoError(t, runDecisionReport(decisionReportCmd, nil))
	assert.Contains(t, rep.String(), "| Rows | 8 |")
}

func TestDecisionReportEmptyAndMissingDB(t *testing.T) {
	resetDecisionFlags(t)
	path := seedTelemetryDB(t)
	decisionDBPath, decisionSite, decisionLimit, decisionSince = path, "nothing.here", 0, ""
	var rep bytes.Buffer
	decisionReportCmd.SetOut(&rep)
	require.NoError(t, runDecisionReport(decisionReportCmd, nil))
	assert.Contains(t, rep.String(), "no shadow rows for site")

	decisionDBPath = filepath.Join(t.TempDir(), "missing.db")
	assert.Error(t, runDecisionReport(decisionReportCmd, nil))
	assert.Error(t, runDecisionReplay(decisionReplayCmd, nil))
}

func TestBuildReplayDeciderRejectsUnknown(t *testing.T) {
	resetDecisionFlags(t)
	decisionDecider = "oracle"
	_, err := buildReplayDecider(context.Background())
	assert.Error(t, err)
}

func TestParseSince(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"24h", "7d", "90m", "2026-01-02T03:04:05Z"} {
		_, err := parseSince(s)
		assert.NoError(t, err, s)
	}
	_, err := parseSince("yesterday")
	assert.Error(t, err)
	d, err := parseSince("2d")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(-48*time.Hour), d, time.Minute)
}
