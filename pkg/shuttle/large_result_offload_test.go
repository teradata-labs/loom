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
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/storage"
)

// These tests drive the executor's large-result offload site — the second of the
// two sites story D-E governs (the first is agent.formatToolResult). They exercise
// Executor.Execute (which calls handleLargeResult) through the real Tool interface
// with a live SharedMemoryStore, and assert D-E's acceptance criteria for this site:
// one positive 64 KiB default threshold, ≥ threshold stores by reference (retrievable
// by its handle), below stays inline, and the exempt set (manage_skills + the recall
// tools) always enters whole and is never re-offloaded.
//
// Scenario S2 (data-heavy): payloads straddle 64 KiB, and the exempt-set tools return
// oversized data to prove the name guard, not the size, is what spares them.

const kib64 = 64 * 1024 // storage.DefaultSharedMemoryThreshold, in bytes

// sizedResultTool returns a fixed Result under a configurable name, so a test can
// register it as any tool (including an exempt name) and control the serialized
// size of its output.
type sizedResultTool struct {
	name string
	data interface{}
}

func (s *sizedResultTool) Name() string        { return s.name }
func (s *sizedResultTool) Description() string { return "returns a fixed-size result" }
func (s *sizedResultTool) Backend() string     { return "" }
func (s *sizedResultTool) InputSchema() *JSONSchema {
	return NewObjectSchema("sized", nil, nil)
}
func (s *sizedResultTool) Execute(_ context.Context, _ map[string]interface{}) (*Result, error) {
	return &Result{Success: true, Data: s.data}, nil
}

// newOffloadExecutor wires a fresh executor to an isolated shared-memory store,
// keeping the NewExecutor default threshold (SetSharedMemory only overrides the
// threshold when the argument is >= 0, so -1 preserves the default 64 KiB).
func newOffloadExecutor(t *testing.T, tool Tool) (*Executor, *storage.SharedMemoryStore) {
	t.Helper()
	store := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       50 * 1024 * 1024,
		CompressionThreshold: 8 * 1024 * 1024, // above our payloads, so stored bytes are uncompressed
		TTLSeconds:           3600,
	})
	reg := NewRegistry()
	reg.Register(tool)
	exec := NewExecutor(reg)
	exec.SetSharedMemory(store, -1) // -1 => keep the default 64 KiB threshold
	return exec, store
}

// --- AC3: default threshold is a positive 64 KiB at the executor site ---------

// TestExecutorOffload_AC3_DefaultThresholdIsPositive64KiB asserts a freshly
// constructed executor takes its offload threshold from
// storage.DefaultSharedMemoryThreshold, which reads a positive 64 KiB and never
// the legacy −1 (inline-everything) sentinel.
func TestExecutorOffload_AC3_DefaultThresholdIsPositive64KiB(t *testing.T) {
	exec := NewExecutor(NewRegistry())

	assert.Equal(t, int64(kib64), exec.threshold, "executor default threshold is 64 KiB")
	assert.Positive(t, exec.threshold, "executor default threshold is positive, never inline-everything")
	assert.NotEqual(t, int64(-1), exec.threshold, "executor default threshold is never the −1 sentinel")
	assert.Equal(t, int64(kib64), int64(storage.DefaultSharedMemoryThreshold),
		"the shared default constant is a positive 64 KiB")
}

// --- AC1: a result above 64 KiB is stored by reference, retrievable by handle --

// TestExecutorOffload_AC1_LargeResultStoredByReference asserts that a result
// whose serialized size exceeds 64 KiB is replaced with a DataReference plus an
// inline summary, and that the reference resolves back to the full bytes in the
// backing store the recall tool reads.
func TestExecutorOffload_AC1_LargeResultStoredByReference(t *testing.T) {
	const sessionID = "sess-exec-large"
	payload := "EXEC_LARGE_START_" + strings.Repeat("q", kib64+8*1024) + "_EXEC_LARGE_END"
	require.Greater(t, len(payload), kib64, "payload is above the 64 KiB threshold")

	exec, store := newOffloadExecutor(t, &sizedResultTool{name: "web_search", data: payload})

	ctx := session.WithSessionID(context.Background(), sessionID)
	result, err := exec.Execute(ctx, "web_search", map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, result.Success)

	// Stored by reference: a handle is set and the inline Data is a summary string
	// (an inline preview + handle, per AC1).
	require.NotNil(t, result.DataReference, "large result carries a DataReference handle")
	_, ok := result.Data.(string)
	require.True(t, ok, "inline Data is a summary string")

	// Retrievable by its handle from the store the recall tool reads, scoped to the
	// storing session. The executor persists the JSON-serialized result, so the
	// resolved bytes un-marshal back to the original payload intact.
	got, err := store.Get(result.DataReference, sessionID)
	require.NoError(t, err, "the handle resolves in the storing session")
	var restored string
	require.NoError(t, json.Unmarshal(got, &restored), "resolved bytes are the JSON-serialized result")
	assert.Equal(t, payload, restored, "the reference resolves to the original payload")
}

// TestExecutorOffload_AC1_ExactThresholdStoredByReference asserts AC1's "at" half
// (test-arch Entry 5: "≥ 64 KiB is stored by reference"): a result whose serialized
// size is exactly 64 KiB is stored by reference, not held inline. The payload is a
// string of kib64-2 bytes so its JSON serialization (two quote bytes added) is
// exactly kib64 bytes.
func TestExecutorOffload_AC1_ExactThresholdStoredByReference(t *testing.T) {
	const sessionID = "sess-exec-boundary"
	payload := strings.Repeat("a", kib64-2)

	exec, store := newOffloadExecutor(t, &sizedResultTool{name: "web_search", data: payload})

	before := store.Stats().ItemCount
	result, err := exec.Execute(session.WithSessionID(context.Background(), sessionID), "web_search", map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, result.Success)

	require.NotNil(t, result.DataReference, "a result at exactly 64 KiB is stored by reference")
	assert.Greater(t, store.Stats().ItemCount, before, "the boundary result is written to the store by reference")

	got, err := store.Get(result.DataReference, sessionID)
	require.NoError(t, err, "the handle resolves in the storing session")
	var restored string
	require.NoError(t, json.Unmarshal(got, &restored), "resolved bytes are the JSON-serialized result")
	assert.Equal(t, payload, restored, "the reference resolves to the original payload")
}

// --- AC2: a result below 64 KiB stays inline ---------------------------------

// TestExecutorOffload_AC2_SmallResultStaysInline asserts a result whose serialized
// size is below 64 KiB is left in Data verbatim with no reference created.
func TestExecutorOffload_AC2_SmallResultStaysInline(t *testing.T) {
	payload := "small executor result"

	exec, store := newOffloadExecutor(t, &sizedResultTool{name: "web_search", data: payload})

	before := store.Stats().ItemCount
	result, err := exec.Execute(session.WithSessionID(context.Background(), "sess-exec-small"), "web_search", map[string]interface{}{})
	require.NoError(t, err)
	require.True(t, result.Success)

	assert.Nil(t, result.DataReference, "small result creates no reference")
	assert.Equal(t, payload, result.Data, "small result stays inline verbatim")
	assert.Equal(t, before, store.Stats().ItemCount, "nothing is stored for a below-threshold result")
}

// --- AC4 / AC5: the exempt set always enters whole and is never re-offloaded ---

// TestExecutorOffload_AC4_AC5_ExemptToolsEnterWhole asserts that the exempt-set
// tools — manage_skills (the skill body, AC4) and the recall tools
// query_tool_result / get_tool_result (AC5) — are never stored by reference at the
// executor site even when their output is far above 64 KiB. The name guard, not
// the size, is what spares them, so their Data enters whole.
func TestExecutorOffload_AC4_AC5_ExemptToolsEnterWhole(t *testing.T) {
	oversized := strings.Repeat("z", kib64+16*1024)
	require.Greater(t, len(oversized), kib64, "exempt payloads are above the 64 KiB threshold")

	cases := []struct {
		name string
		ac   string
	}{
		{"manage_skills", "AC4: skill body enters whole regardless of size"},
		{"query_tool_result", "AC5: recall result never re-offloaded"},
		{"get_tool_result", "AC5: recall result never re-offloaded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec, store := newOffloadExecutor(t, &sizedResultTool{name: tc.name, data: oversized})

			before := store.Stats().ItemCount
			result, err := exec.Execute(session.WithSessionID(context.Background(), "sess-exec-exempt"), tc.name, map[string]interface{}{})
			require.NoError(t, err)
			require.True(t, result.Success)

			assert.Nil(t, result.DataReference, "%s: exempt tool is not stored by reference", tc.ac)
			assert.Equal(t, oversized, result.Data, "%s: exempt tool output enters whole", tc.ac)
			assert.Equal(t, before, store.Stats().ItemCount, "%s: exempt tool stores nothing", tc.ac)
		})
	}
}
