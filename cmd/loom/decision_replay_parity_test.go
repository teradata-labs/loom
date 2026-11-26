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
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/teradata-labs/loom/internal/sqlitedriver"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/decision/sites"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Review #409: the replay sent an empty error_code with "Code: Message"
// text and treated failures without error detail as successes, so replayed
// rows were not the requests the live agent sent. Each case here is
// persisted through the agent's own SessionStore.SaveToolExecution, read
// back the way `loom decision replay` reads it, and must produce the live
// site's request and reference exactly.
func TestReplayBuildsTheLiveRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.db")
	store, err := agent.NewSessionStore(path, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	require.NoError(t, store.SaveSession(ctx, &agent.Session{ID: "s1", Context: map[string]interface{}{}}))

	cases := []struct {
		tool       string
		execErr    error
		result     *shuttle.Result
		wantFailed bool
	}{
		{tool: "exec_error_only", execErr: errors.New("dial tcp: connection refused"), wantFailed: true},
		{tool: "exec_error_and_result_error", execErr: errors.New("context deadline exceeded"),
			result: &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "TIMEOUT", Message: "slow"}}, wantFailed: true},
		{tool: "result_error", result: &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "MCP_CALL_FAILED", Message: `{"code":"session_handle_budget_full"}`}}, wantFailed: true},
		{tool: "result_error_429", result: &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "429", Message: "Too Many Requests"}}, wantFailed: true},
		{tool: "failed_without_details", result: &shuttle.Result{Success: false}, wantFailed: true},
		{tool: "success", result: &shuttle.Result{Success: true, Data: "rows"}},
		{tool: "success_with_stray_error", result: &shuttle.Result{Success: true, Error: &shuttle.Error{Code: "WARN", Message: "partial"}}},
		{tool: "no_result_no_error"},
	}
	for _, c := range cases {
		require.NoError(t, store.SaveToolExecution(ctx, "s1", agent.ToolExecution{
			ToolName: c.tool, Input: map[string]interface{}{"q": c.tool}, Result: c.result, Error: c.execErr,
		}))
	}

	db, err := sql.Open("sqlite3", sqlitedriver.DSN(path, sqlitedriver.Options{BusyTimeoutMS: 5000}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := loadToolExecutions(ctx, db, 100, false, sampleNewest, 0)
	require.NoError(t, err)
	byTool := make(map[string]toolExecutionRow, len(rows))
	for _, r := range rows {
		byTool[r.toolName] = r
	}

	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			row, ok := byTool[c.tool]
			require.True(t, ok)
			liveSuccess, liveCode, liveText := sites.ToolOutcomeOf(c.execErr, c.result).Fields()
			gotSuccess, gotCode, gotText := replayOutcome(row).Fields()
			assert.Equal(t, liveSuccess, gotSuccess, "success")
			assert.Equal(t, liveCode, gotCode, "error code")
			assert.Equal(t, liveText, gotText, "error text")
			assert.Equal(t, !c.wantFailed, gotSuccess)

			input := map[string]any{"q": c.tool}
			liveReq, err := sites.FailureKindRequest(c.tool, liveCode, liveText, input)
			require.NoError(t, err)
			replayReq, err := sites.FailureKindRequest(row.toolName, gotCode, gotText, input)
			require.NoError(t, err)
			assert.True(t, proto.Equal(liveReq, replayReq), "replayed request differs from the live one")
			assert.Equal(t, sites.FailureKindReference(liveSuccess, liveCode, liveText),
				sites.FailureKindReference(gotSuccess, gotCode, gotText))
		})
	}

	// --errors-only selects exactly the failures, including the failed
	// result that has no error text.
	failures, err := loadToolExecutions(ctx, db, 100, true, sampleNewest, 0)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, r := range failures {
		got[r.toolName] = true
	}
	for _, c := range cases {
		assert.Equal(t, c.wantFailed, got[c.tool], "--errors-only membership of %s", c.tool)
	}
}
