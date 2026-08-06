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
package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
)

// These tests drive story D-I's per-mutation debug logging on the loom.component
// surface: a real Agent over the scripted mockToolCallingLLM, with the shared
// context-dump switch (story D-H) read at each context-mutation point. Every
// mutation site emits its debug log through the global zap logger, so an
// observer core installed via zap.ReplaceGlobals captures exactly what the site
// wrote — message and structured fields — and each AC asserts that record.
//
// The switch gates all five sites: config.Debug.ContextDump ON emits, OFF
// (AC6) emits nothing even though the mutation still happens. The env switch
// (LOOM_CONTEXT_DUMP) is forced off in the OFF drives so only the config flag
// under test decides emission.
//
//   AC1 compaction         -> "context mutation: compaction"
//   AC2 skill load          -> "context mutation: skill load"
//   AC3 large-result store  -> "context mutation: large-result offload" (agent site;
//                              the executor site lives in pkg/shuttle)
//   AC4 tool assembly       -> "context mutation: tool assembly"
//   AC5 restore re-fire      -> "context mutation: restore re-fire"
//   AC6 switch off           -> none of the above emitted

const (
	msgCompaction    = "context mutation: compaction"
	msgSkillLoad     = "context mutation: skill load"
	msgLargeResult   = "context mutation: large-result offload"
	msgToolAssembly  = "context mutation: tool assembly"
	msgRestoreRefire = "context mutation: restore re-fire"
)

// --- observer + field helpers ------------------------------------------------

// captureZap installs an observer core as the global zap logger and returns the
// captured-log handle plus a restore func to defer. Every zap.L() Debug from the
// mutation sites lands in the returned ObservedLogs.
func captureZap(t *testing.T) (*observer.ObservedLogs, func()) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	return logs, restore
}

// onlyMutationRecord returns the single logged record with the given message,
// failing if there is not exactly one.
func onlyMutationRecord(t *testing.T, logs *observer.ObservedLogs, message string) map[string]interface{} {
	t.Helper()
	entries := logs.FilterMessage(message).All()
	require.Len(t, entries, 1, "exactly one %q record is emitted", message)
	return entries[0].ContextMap()
}

// intField reads an integer field. zap encodes zap.Int/zap.Int64 as int64 in the
// observer's context map.
func intField(t *testing.T, m map[string]interface{}, key string) int64 {
	t.Helper()
	v, ok := m[key]
	require.Truef(t, ok, "record carries field %q", key)
	n, ok := v.(int64)
	require.Truef(t, ok, "field %q is an integer (got %T)", key, v)
	return n
}

// strField reads a string field.
func strField(t *testing.T, m map[string]interface{}, key string) string {
	t.Helper()
	v, ok := m[key]
	require.Truef(t, ok, "record carries field %q", key)
	s, ok := v.(string)
	require.Truef(t, ok, "field %q is a string (got %T)", key, v)
	return s
}

// boolField reads a boolean field.
func boolField(t *testing.T, m map[string]interface{}, key string) bool {
	t.Helper()
	v, ok := m[key]
	require.Truef(t, ok, "record carries field %q", key)
	b, ok := v.(bool)
	require.Truef(t, ok, "field %q is a boolean (got %T)", key, v)
	return b
}

// fieldStrings coerces a zap.Strings field (an []interface{} in the observer's
// context map) to a []string.
func fieldStrings(t *testing.T, m map[string]interface{}, key string) []string {
	t.Helper()
	v, ok := m[key]
	require.Truef(t, ok, "record carries field %q", key)
	switch s := v.(type) {
	case []string:
		return s
	case []interface{}:
		out := make([]string, 0, len(s))
		for _, e := range s {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	t.Fatalf("field %q is not a string slice (got %T)", key, v)
	return nil
}

// --- rigs --------------------------------------------------------------------

// newDebugRenderAgent builds an agent with the context-dump switch set to dump,
// wired to an isolated shared-memory store so the offload path (and its debug
// log) operate on a private partition. It mirrors the D-E render rig but toggles
// the debug switch under test.
func newDebugRenderAgent(t *testing.T, dump bool) (*Agent, *storage.SharedMemoryStore) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.Debug.ContextDump = dump

	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg))
	store := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       50 * 1024 * 1024,
		CompressionThreshold: 8 * 1024 * 1024,
		TTLSeconds:           3600,
	})
	ag.SetSharedMemory(store)
	return ag, store
}

// ctxDebugProbeTool is a trivial registered tool used to assert its name reaches
// the advertised-names field of the tool-assembly debug log.
type ctxDebugProbeTool struct{}

func (ctxDebugProbeTool) Name() string        { return "ctx_debug_probe" }
func (ctxDebugProbeTool) Description() string { return "probe tool for the tool-assembly log" }
func (ctxDebugProbeTool) Backend() string     { return "" }
func (ctxDebugProbeTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (ctxDebugProbeTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}

// forceEnvDumpOff forces the LOOM_CONTEXT_DUMP env switch off (and re-reads the
// cached value) so an OFF drive's emission is decided solely by the config flag
// under test, never by an ambient env override.
func forceEnvDumpOff(t *testing.T) {
	t.Helper()
	t.Setenv(envContextDumpVar, "")
	resetCtxDumpEnvOnce()
}

// --- AC1: compaction emits a debug log with the switch on --------------------

// TestContextDebug_AC1_CompactionEmitsDebugLogOn drives a real compaction on a
// session's SegmentedMemory with the switch on and asserts the debug log carries
// the session id + turn, the compressed batch size, the boundary-adjustment flag,
// the L1/L2 token counts before→after, and the cumulative L2→Swap eviction count.
func TestContextDebug_AC1_CompactionEmitsDebugLogOn(t *testing.T) {
	const sessionID = "sess-di-ac1"
	ag, _ := newDebugRenderAgent(t, true)

	ctx := session.WithSessionID(context.Background(), sessionID)
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, sessionID, ag.config.Name, "")
	require.NotNil(t, sess)
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "the session carries a *SegmentedMemory")

	// A user/assistant/user run so CompactMemory keeps the newest user message in
	// L1 and compresses the two before it — a non-zero batch with the boundary
	// moved off the newest user message.
	segMem.AddMessage(ctx, Message{Role: "user", Content: "first question that fills the kernel"})
	segMem.AddMessage(ctx, Message{Role: "assistant", Content: "first answer that fills the kernel"})
	segMem.AddMessage(ctx, Message{Role: "user", Content: "second question kept pinned in L1"})

	logs, restore := captureZap(t)
	defer restore()

	compressed, _ := segMem.CompactMemory(ctx)
	require.Equal(t, 2, compressed, "the two pre-boundary messages were compacted")

	rec := onlyMutationRecord(t, logs, msgCompaction)

	assert.Equal(t, sessionID, strField(t, rec, "session_id"), "log carries the session id")
	assert.GreaterOrEqual(t, intField(t, rec, "turn"), int64(0), "log carries the turn")
	assert.Equal(t, int64(2), intField(t, rec, "batch_size"), "log carries the compressed batch size")
	assert.True(t, boolField(t, rec, "boundary_adjusted"),
		"log reports the boundary was adjusted to keep the newest user message in L1")

	// L1/L2 token counts before→after: L1 shrank as its messages folded into L2.
	l1Before := intField(t, rec, "l1_tokens_before")
	l1After := intField(t, rec, "l1_tokens_after")
	assert.Less(t, l1After, l1Before, "L1 token count fell across the compaction")
	assert.GreaterOrEqual(t, intField(t, rec, "l2_tokens_after"), intField(t, rec, "l2_tokens_before"),
		"L2 token count did not fall across the compaction")

	// The L2→Swap eviction count is carried (zero here — nothing was evicted).
	assert.GreaterOrEqual(t, intField(t, rec, "swap_evictions"), int64(0),
		"log carries the cumulative L2→Swap eviction count")
}

// --- AC2: skill load emits a debug log with the switch on --------------------

// TestContextDebug_AC2_SkillLoadEmitsDebugLogOn drives a manage_skills(load)
// through the Chat loop with the switch on and asserts the debug log carries the
// session id + turn, the skill name, its body size, the count of tools the skill
// registered, and the active-set delta. tooled-skill declares one required tool,
// so tools_registered is 1 and the active set grows by one.
func TestContextDebug_AC2_SkillLoadEmitsDebugLogOn(t *testing.T) {
	const sessionID = "sess-di-ac2"
	redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "tooled-skill"),
		finalTurn(),
	}}
	rig := buildManageSkillsRig(t, llm, nil, true)

	logs, restore := captureZap(t)
	defer restore()

	_, err := rig.agent.Chat(context.Background(), sessionID, "load the tooled skill")
	require.NoError(t, err)

	rec := onlyMutationRecord(t, logs, msgSkillLoad)

	assert.Equal(t, sessionID, strField(t, rec, "session_id"), "log carries the session id")
	assert.GreaterOrEqual(t, intField(t, rec, "turn"), int64(1),
		"log carries the in-flight turn (>=1 after the first provider call)")
	assert.Equal(t, "tooled-skill", strField(t, rec, "name"), "log carries the loaded skill name")
	assert.Positive(t, intField(t, rec, "body_bytes"), "log carries the skill body size")
	assert.Equal(t, int64(1), intField(t, rec, "tools_registered"),
		"log carries the count of tools the skill registered")
	assert.Equal(t, int64(1), intField(t, rec, "active_set_delta"),
		"log carries the active-set delta of the load")
}

// --- AC3: large-result storage emits a debug log with the switch on ----------

// TestContextDebug_AC3_LargeResultOffloadEmitsDebugLogOn drives the agent offload
// site (formatToolResult) with a payload above the 64 KiB threshold and the switch
// on, and asserts the debug log carries the session id + turn, the reference id the
// result was stored under, and the payload size against the offload threshold. A
// warm-up provider call advances the per-session turn so the log carries a live
// turn number rather than the pre-conversation zero.
func TestContextDebug_AC3_LargeResultOffloadEmitsDebugLogOn(t *testing.T) {
	const sessionID = "sess-di-ac3"
	redirectCtxDumpSink(t)

	ag, _ := newDebugRenderAgent(t, true)

	// Warm up the per-session turn counter with one provider call.
	_, err := ag.Chat(context.Background(), sessionID, "warm up the turn counter")
	require.NoError(t, err)

	payload := "DI_AC3_START\n" + strings.Repeat("large-result filler line\n", 4000) + "DI_AC3_END"
	require.Greater(t, len(payload), agentKiB64, "payload is above the 64 KiB threshold")

	logs, restore := captureZap(t)
	defer restore()

	ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)
	rendered := ag.formatToolResult(ctx, sessionID, "web_search",
		&shuttle.Result{Success: true, Data: payload}, nil)
	require.Contains(t, rendered, "stored in memory", "the large result was stored by reference")

	rec := onlyMutationRecord(t, logs, msgLargeResult)

	assert.Equal(t, sessionID, strField(t, rec, "session_id"), "log carries the session id")
	assert.GreaterOrEqual(t, intField(t, rec, "turn"), int64(1),
		"log carries the in-flight turn (>=1 after the warm-up provider call)")
	assert.NotEmpty(t, strField(t, rec, "reference_id"), "log carries the stored reference id")

	threshold := intField(t, rec, "threshold_bytes")
	assert.Equal(t, int64(agentKiB64), threshold, "log carries the 64 KiB offload threshold")
	assert.GreaterOrEqual(t, intField(t, rec, "size_bytes"), threshold,
		"log carries the payload size, at or above the threshold that triggered offload")
}

// --- AC4: per-session tool assembly emits a debug log with the switch on ------

// TestContextDebug_AC4_ToolAssemblyEmitsDebugLogOn drives one Chat turn with the
// switch on and a probe tool registered, and asserts the tool-assembly debug log
// carries the session id + turn and the advertised tool names — the exact
// projection about to be sent to the provider.
func TestContextDebug_AC4_ToolAssemblyEmitsDebugLogOn(t *testing.T) {
	const sessionID = "sess-di-ac4"
	redirectCtxDumpSink(t)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.Debug.ContextDump = true

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(ctxDebugProbeTool{})

	logs, restore := captureZap(t)
	defer restore()

	_, err := ag.Chat(context.Background(), sessionID, "hello")
	require.NoError(t, err)

	rec := onlyMutationRecord(t, logs, msgToolAssembly)

	assert.Equal(t, sessionID, strField(t, rec, "session_id"), "log carries the session id")
	assert.Equal(t, int64(1), intField(t, rec, "turn"), "log carries the turn (1 on the first provider call)")

	advertised := fieldStrings(t, rec, "advertised")
	assert.Contains(t, advertised, "ctx_debug_probe", "log carries the advertised probe tool")
	assert.Equal(t, int64(len(advertised)), intField(t, rec, "count"),
		"the advertised count matches the advertised-names field")
}

// --- AC5: restore re-fire emits a debug log with the switch on ----------------

// TestContextDebug_AC5_RestoreReFireEmitsDebugLogOn runs the durable-store restart
// scenario (a live agent builds durable records, then a fresh switch-on agent
// replays and re-fires) and asserts the restore re-fire debug log carries the
// session id + turn, the count of loads re-activated (alpha-skill + tooled-skill),
// and the count of disclosure tools re-registered (get_error_details +
// query_tool_result). Only the restored agent has the switch on, so the pass emits
// exactly one record.
func TestContextDebug_AC5_RestoreReFireEmitsDebugLogOn(t *testing.T) {
	logs, restore := captureZap(t)
	defer restore()

	sc := runRestoreScenario(t)

	rec := onlyMutationRecord(t, logs, msgRestoreRefire)

	assert.Equal(t, sc.sessionID, strField(t, rec, "session_id"), "log carries the session id")
	assert.GreaterOrEqual(t, intField(t, rec, "turn"), int64(0), "log carries the turn")
	assert.Equal(t, int64(2), intField(t, rec, "loads_reactivated"),
		"log carries the two durable skill loads the re-fire re-activated")
	assert.Equal(t, int64(2), intField(t, rec, "tools_reregistered"),
		"log carries the two disclosure tools (error + large-result) the re-fire re-registered")
}

// --- AC6: with the switch off, none of the debug logs are emitted -------------

// TestContextDebug_AC6_SwitchOffEmitsNoDebugLogs asserts that at every mutation
// site, with the switch off, the mutation still happens but its debug log is not
// emitted. The env switch is forced off so only the config flag decides emission.
func TestContextDebug_AC6_SwitchOffEmitsNoDebugLogs(t *testing.T) {
	t.Run("compaction", func(t *testing.T) {
		forceEnvDumpOff(t)
		const sessionID = "sess-di-ac6-compaction"
		ag, _ := newDebugRenderAgent(t, false)

		ctx := session.WithSessionID(context.Background(), sessionID)
		sess := ag.memory.GetOrCreateSessionWithAgent(ctx, sessionID, ag.config.Name, "")
		segMem := sess.SegmentedMem.(*SegmentedMemory)
		segMem.AddMessage(ctx, Message{Role: "user", Content: "first question"})
		segMem.AddMessage(ctx, Message{Role: "assistant", Content: "first answer"})
		segMem.AddMessage(ctx, Message{Role: "user", Content: "second question"})

		logs, restore := captureZap(t)
		defer restore()

		compressed, _ := segMem.CompactMemory(ctx)
		require.Equal(t, 2, compressed, "the compaction still happened")
		assert.Zero(t, logs.FilterMessage(msgCompaction).Len(), "no compaction log with the switch off")
	})

	t.Run("skill_load", func(t *testing.T) {
		forceEnvDumpOff(t)
		const sessionID = "sess-di-ac6-skill"
		redirectCtxDumpSink(t)

		llm := &mockToolCallingLLM{responses: []mockLLMResponse{
			loadCall("c1", "tooled-skill"),
			finalTurn(),
		}}
		rig := buildManageSkillsRig(t, llm, nil, false)

		logs, restore := captureZap(t)
		defer restore()

		_, err := rig.agent.Chat(context.Background(), sessionID, "load the tooled skill")
		require.NoError(t, err)
		require.NotEmpty(t, activeSkillNames(rig.orch, sessionID), "the skill load still happened")
		assert.Zero(t, logs.FilterMessage(msgSkillLoad).Len(), "no skill-load log with the switch off")
	})

	t.Run("large_result", func(t *testing.T) {
		forceEnvDumpOff(t)
		const sessionID = "sess-di-ac6-large"
		ag, _ := newDebugRenderAgent(t, false)

		payload := "AC6_LARGE_START\n" + strings.Repeat("large-result filler line\n", 4000) + "AC6_LARGE_END"
		require.Greater(t, len(payload), agentKiB64, "payload is above the 64 KiB threshold")

		logs, restore := captureZap(t)
		defer restore()

		ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)
		rendered := ag.formatToolResult(ctx, sessionID, "web_search",
			&shuttle.Result{Success: true, Data: payload}, nil)
		require.Contains(t, rendered, "stored in memory", "the offload still happened")
		assert.Zero(t, logs.FilterMessage(msgLargeResult).Len(), "no large-result log with the switch off")
	})

	t.Run("tool_assembly", func(t *testing.T) {
		forceEnvDumpOff(t)
		const sessionID = "sess-di-ac6-tools"

		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		cfg.Debug.ContextDump = false

		llm := &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}
		ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
		ag.RegisterTool(ctxDebugProbeTool{})

		logs, restore := captureZap(t)
		defer restore()

		_, err := ag.Chat(context.Background(), sessionID, "hello")
		require.NoError(t, err)

		sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, ag.config.Name, "")
		require.NotEmpty(t, advertisedNames(ag, sess), "tools were still assembled for the turn")
		assert.Zero(t, logs.FilterMessage(msgToolAssembly).Len(), "no tool-assembly log with the switch off")
	})

	t.Run("restore_refire", func(t *testing.T) {
		forceEnvDumpOff(t)
		ctx := context.Background()
		redirectCtxDumpSink(t)
		skillsDir := writeSkillFixtures(t)

		store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions.db"), observability.NewNoOpTracer())
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })

		const sessionID = "sess-di-ac6-restore"

		// Live agent (switch off) writes the durable records the re-fire keys on.
		liveLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
			loadCall("c-alpha", "alpha-skill"),
			loadCall("c-tooled", "tooled-skill"),
			rawToolCall("c-err", flakyProbeName, map[string]interface{}{}),
			rawToolCall("c-big", bulkScanName, map[string]interface{}{}),
		}}
		liveAgent, _ := buildRestoreAgent(t, liveLLM, store, skillsDir, newTestErrorStore(t), false)
		_, err = liveAgent.Chat(ctx, sessionID, "load the skills and run both probes")
		require.NoError(t, err)

		// Restart with a fresh agent, switch still off: the re-fire runs during
		// replay but emits nothing.
		logs, restore := captureZap(t)
		defer restore()

		restoredLLM := &mockToolCallingLLM{}
		restoredAgent, restoredOrch := buildRestoreAgent(t, restoredLLM, store, skillsDir, newTestErrorStore(t), false)
		restoredSession := restoredAgent.memory.GetOrCreateSessionWithAgent(ctx, sessionID, restoredAgent.config.Name, "")
		require.NotNil(t, restoredSession)

		require.NotEmpty(t, activeSkillNames(restoredOrch, sessionID),
			"the re-fire still re-activated the durable loads")
		assert.Zero(t, logs.FilterMessage(msgRestoreRefire).Len(), "no restore re-fire log with the switch off")
	})
}
