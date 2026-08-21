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
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// backpressureResult builds a tool error carrying the machine-readable
// park-and-wake contract (the shape teradata-mcp's fail() emits).
func backpressureResult(code string, retryAfterS int64, waitParam string, maxWaitS int64) json.RawMessage {
	payload := map[string]any{"code": code, "message": "capacity", "retryable": true}
	if retryAfterS > 0 {
		payload["retry_after_s"] = retryAfterS
	}
	if waitParam != "" {
		payload["wait_param"] = waitParam
		payload["max_wait_s"] = maxWaitS
	}
	text, _ := json.Marshal(payload)
	r, _ := json.Marshal(protocol.CallToolResult{IsError: true, Content: []protocol.Content{
		{Type: "text", Text: string(text)},
	}})
	return r
}

func plainCodeResult(code string) json.RawMessage {
	r, _ := json.Marshal(protocol.CallToolResult{IsError: true, Content: []protocol.Content{
		{Type: "text", Text: fmt.Sprintf(`{"code":%q,"message":"task failure"}`, code)},
	}})
	return r
}

// TestBackpressureServerPark: a budget-full error naming wait_param re-invokes
// immediately with the wait argument set, so the retry parks server-side. One
// Execute call, two wire attempts, the model sees only success (issue #354).
func TestBackpressureServerPark(t *testing.T) {
	ft := newWaitTransport(
		backpressureResult("session_handle_budget_full", 412, "wait_s", 300),
		successResult("connected"),
	)
	adapter := waitAdapter(t, ft)

	res, err := adapter.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success, "frozen call must succeed after server park: %+v", res)
	assert.Equal(t, 2, ft.calls(), "exactly one re-invoke")

	// The retry parked server-side: wait_s set, clamped to the server's cap
	// (no ctx deadline → remaining budget far exceeds max_wait_s).
	params := ft.lastCallParams()
	require.NotNil(t, params)
	assert.Equal(t, float64(300), params["wait_s"], "wait_s = min(remaining, max_wait_s)")
}

// TestBackpressureWaitClampsToDeadline: with a context deadline tighter than
// the server's max_wait_s, the parked wait asks only for the time the caller
// actually has.
func TestBackpressureWaitClampsToDeadline(t *testing.T) {
	ft := newWaitTransport(
		backpressureResult("session_handle_budget_full", 0, "wait_s", 300),
		successResult("connected"),
	)
	adapter := waitAdapter(t, ft)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success)

	params := ft.lastCallParams()
	require.NotNil(t, params)
	waitS, ok := params["wait_s"].(float64)
	require.True(t, ok, "wait_s missing: %v", params)
	assert.LessOrEqual(t, waitS, float64(8), "wait_s must not exceed remaining budget (deadline - margin)")
	assert.GreaterOrEqual(t, waitS, float64(1))
}

// TestBackpressurePollWithoutWaitParam: a retryable error with no wait_param
// polls client-side — sleeping retry_after_s (floored at 1s) — and succeeds
// on the re-invoke.
func TestBackpressurePollWithoutWaitParam(t *testing.T) {
	ft := newWaitTransport(
		backpressureResult("server_busy", 1, "", 0),
		successResult("ok"),
	)
	adapter := waitAdapter(t, ft)

	start := time.Now()
	res, err := adapter.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, res.Success)
	assert.Equal(t, 2, ft.calls())
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "poll path must sleep retry_after_s first")
	_, has := ft.lastCallParams()["wait_s"]
	assert.False(t, has, "no wait param may be invented")
}

// TestBackpressureConditionChangeSurfaces: when the retried call fails with a
// task-level (non-retryable) error, the freeze ends and that error reaches
// the model — waiting out someone else's SQL mistake helps nobody.
func TestBackpressureConditionChangeSurfaces(t *testing.T) {
	ft := newWaitTransport(
		backpressureResult("session_handle_budget_full", 0, "wait_s", 300),
		plainCodeResult("unknown_session_handle"),
	)
	adapter := waitAdapter(t, ft)

	res, err := adapter.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "unknown_session_handle")
	assert.Equal(t, 2, ft.calls(), "the changed condition must not be retried")
}

// TestBackpressureBudgetExhausted: a condition that never clears surfaces the
// newest backpressure error once the ctx-derived budget runs out, well before
// the context itself dies.
func TestBackpressureBudgetExhausted(t *testing.T) {
	// Enough scripted refusals to outlast the budget; wait_param scripted so
	// each attempt is an immediate re-invoke (the fake answers instantly).
	results := make([]json.RawMessage, 0, 16)
	for i := 0; i < 16; i++ {
		results = append(results, backpressureResult("session_handle_budget_full", 0, "wait_s", 300))
	}
	ft := newWaitTransport(results...)
	adapter := waitAdapter(t, ft)

	// deadline 3s − 2s margin → ~1s budget → wait_s floors at 1 per attempt.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	res, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "session_handle_budget_full")
	assert.Less(t, time.Since(start), 3*time.Second, "must give up before the context dies")
	assert.GreaterOrEqual(t, ft.calls(), 2, "at least one frozen re-invoke happened")
}
