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
package shuttle

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/session"
)

// These tests cover story D-I's per-mutation debug log at the executor offload
// site (Executor.handleLargeResult) — the second of the two large-result offload
// sites (the first, agent.formatToolResult, is covered in pkg/agent). The agent
// layer wires the executor's debug hooks via SetContextDebug; here they are wired
// directly, driven through the real Tool interface (Executor.Execute), and the log
// is captured with a zap observer.
//
//   AC3 switch on  -> a >= 64 KiB result stored by reference emits a debug log
//                     carrying session id + turn, the reference id, and the size
//                     against the offload threshold.
//   AC6 switch off -> the same offload emits no debug log.

const msgLargeResultOffload = "context mutation: large-result offload"

// captureZapShuttle installs an observer core as the global zap logger and returns
// the captured-log handle plus a restore func to defer.
func captureZapShuttle(t *testing.T) (*observer.ObservedLogs, func()) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	return logs, restore
}

// --- AC3: the executor offload site emits a debug log with the switch on ------

// TestContextDebug_AC3_ExecutorOffloadEmitsDebugLogOn drives an above-threshold
// tool result through Executor.Execute with the debug hooks wired and enabled, and
// asserts the offload debug log carries the session id, the turn (from the injected
// turn source), the stored reference id, and the payload size at or above the 64
// KiB offload threshold.
func TestContextDebug_AC3_ExecutorOffloadEmitsDebugLogOn(t *testing.T) {
	const sessionID = "sess-di-exec-on"
	const turn = 7

	payload := "DI_EXEC_START_" + strings.Repeat("q", kib64+8*1024) + "_DI_EXEC_END"
	require.Greater(t, len(payload), kib64, "payload is above the 64 KiB threshold")

	exec, _ := newOffloadExecutor(t, &sizedResultTool{name: "web_search", data: payload})
	exec.SetContextDebug(func() bool { return true }, func(string) int { return turn })

	logs, restore := captureZapShuttle(t)
	defer restore()

	ctx := session.WithSessionID(context.Background(), sessionID)
	result, err := exec.Execute(ctx, "web_search", map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotNil(t, result.DataReference, "the result was stored by reference")

	entries := logs.FilterMessage(msgLargeResultOffload).All()
	require.Len(t, entries, 1, "exactly one offload log is emitted")
	rec := entries[0].ContextMap()

	assert.Equal(t, sessionID, rec["session_id"], "log carries the session id")
	assert.Equal(t, int64(turn), rec["turn"], "log carries the turn from the injected source")
	refID, ok := rec["reference_id"].(string)
	require.True(t, ok, "log carries a reference id")
	assert.NotEmpty(t, refID, "the reference id is non-empty")

	threshold, ok := rec["threshold_bytes"].(int64)
	require.True(t, ok, "log carries the threshold")
	assert.Equal(t, int64(kib64), threshold, "log carries the 64 KiB offload threshold")
	size, ok := rec["size_bytes"].(int64)
	require.True(t, ok, "log carries the size")
	assert.GreaterOrEqual(t, size, threshold, "log carries the payload size, at or above the threshold")
}

// --- AC6: with the switch off, the executor offload site emits no debug log ---

// TestContextDebug_AC6_ExecutorOffloadNoDebugLogOff drives the same above-threshold
// offload with the debug hooks reporting the switch off, and asserts the result is
// still stored by reference while no offload debug log is emitted.
func TestContextDebug_AC6_ExecutorOffloadNoDebugLogOff(t *testing.T) {
	const sessionID = "sess-di-exec-off"

	payload := "DI_EXEC_OFF_" + strings.Repeat("q", kib64+8*1024) + "_END"
	require.Greater(t, len(payload), kib64, "payload is above the 64 KiB threshold")

	exec, _ := newOffloadExecutor(t, &sizedResultTool{name: "web_search", data: payload})
	exec.SetContextDebug(func() bool { return false }, func(string) int { return 7 })

	logs, restore := captureZapShuttle(t)
	defer restore()

	ctx := session.WithSessionID(context.Background(), sessionID)
	result, err := exec.Execute(ctx, "web_search", map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.NotNil(t, result.DataReference, "the offload still happened")

	assert.Zero(t, logs.FilterMessage(msgLargeResultOffload).Len(),
		"no offload log with the switch off")
}
