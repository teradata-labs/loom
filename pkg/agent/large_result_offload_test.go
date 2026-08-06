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
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/storage"
)

// These tests drive story D-E's first offload/render site — Agent.formatToolResult —
// directly, and its integrated seat, the M9 context dump (what the model actually
// saw). The executor site (Executor.handleLargeResult) is covered in
// pkg/shuttle/large_result_offload_test.go; both sites share the single positive
// 64 KiB default and the one exempt set.
//
// formatToolResult's acceptance criteria at this site:
//   - AC1: a result at/above 64 KiB is stored by reference (inline summary + handle),
//     retrievable via the recall tool (query_tool_result).
//   - AC2: a result below 64 KiB stays inline and directly readable.
//   - AC3: the default threshold reads a positive 64 KiB, never −1.
//   - AC4: the manage_skills load body always enters whole regardless of size.
//   - AC5: query_tool_result / get_tool_result results always enter whole and are
//     never re-offloaded.
//   - AC6: a string result renders verbatim; a composite result renders as JSON;
//     never fmt.Sprintf("%v", …) of a Go map.
//
// Scenario S2 (data-heavy): payloads straddle 64 KiB; string, composite-map, and
// skill-body shapes are all exercised.

const agentKiB64 = 64 * 1024 // storage.DefaultSharedMemoryThreshold, in bytes

// newRenderAgent builds a minimal agent for direct formatToolResult calls, with an
// isolated shared-memory store wired so the offload path (and the recall tool that
// reads it) operate on a private partition.
func newRenderAgent(t *testing.T) (*Agent, *storage.SharedMemoryStore) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg))

	store := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       50 * 1024 * 1024,
		CompressionThreshold: 8 * 1024 * 1024,
		TTLSeconds:           3600,
	})
	ag.SetSharedMemory(store)
	return ag, store
}

// refIDPattern extracts the reference_id the offload summary surfaces for recall.
var refIDPattern = regexp.MustCompile(`reference_id='([^']+)'`)

// --- AC3: default threshold is a positive 64 KiB at the agent site ------------

// TestAgentOffload_AC3_DefaultThresholdIsPositive64KiB asserts a freshly built
// agent's offload threshold reads a positive 64 KiB from
// storage.DefaultSharedMemoryThreshold and is never the legacy −1 sentinel.
func TestAgentOffload_AC3_DefaultThresholdIsPositive64KiB(t *testing.T) {
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})

	assert.Equal(t, int64(agentKiB64), ag.sharedMemoryThreshold, "agent default threshold is 64 KiB")
	assert.Positive(t, ag.sharedMemoryThreshold, "agent default threshold is positive, never inline-everything")
	assert.NotEqual(t, int64(-1), ag.sharedMemoryThreshold, "agent default threshold is never the −1 sentinel")
	assert.Equal(t, int64(agentKiB64), int64(storage.DefaultSharedMemoryThreshold),
		"the shared default constant is a positive 64 KiB")
}

// --- AC1: a result above 64 KiB is stored by reference and recall-retrievable --

// TestAgentOffload_AC1_LargeResultStoredByReferenceAndRecallable asserts a string
// result above 64 KiB is replaced with an inline summary carrying a handle (not the
// raw payload), and that the handle the summary advertises resolves the full bytes
// back through the recall tool (query_tool_result), scoped to the storing session.
func TestAgentOffload_AC1_LargeResultStoredByReferenceAndRecallable(t *testing.T) {
	const sessionID = "sess-agent-large"
	payload := "AGENT_LARGE_START\n" + strings.Repeat("row of filler text here\n", 4000) + "AGENT_LARGE_END"
	require.Greater(t, len(payload), agentKiB64, "payload is above the 64 KiB threshold")

	ag, store := newRenderAgent(t)
	ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)

	rendered := ag.formatToolResult(ctx, sessionID, "web_search",
		&shuttle.Result{Success: true, Data: payload}, nil)

	// Stored by reference: the render is a compact by-reference summary (an inline
	// preview + handle, per AC1), far smaller than the whole payload.
	assert.Less(t, len(rendered), len(payload), "render is a preview summary, not the whole payload")
	assert.Contains(t, rendered, "stored in memory", "summary states the result was stored by reference")

	// The summary surfaces a handle; recall it via query_tool_result in the same
	// session and reassemble the paginated lines back into the original bytes.
	m := refIDPattern.FindStringSubmatch(rendered)
	require.Len(t, m, 2, "summary advertises a reference_id handle for recall")
	refID := m[1]

	recall := NewQueryToolResultTool(nil, store)
	recallRes, err := recall.Execute(session.WithSessionID(context.Background(), sessionID),
		map[string]interface{}{"reference_id": refID, "offset": float64(0), "limit": float64(1_000_000)})
	require.NoError(t, err)
	require.True(t, recallRes.Success, "recall of the stored handle succeeds")

	data, ok := recallRes.Data.(map[string]interface{})
	require.True(t, ok, "recall returns a paginated text envelope")
	linesAny, ok := data["lines"].([]string)
	require.True(t, ok, "recall envelope carries the text lines")
	assert.Equal(t, payload, strings.Join(linesAny, "\n"), "the handle resolves to the original bytes via recall")
}

// TestAgentOffload_AC1_ExactThresholdStoredByReference asserts AC1's "at" half
// (test-arch Entry 5: "≥ 64 KiB is stored by reference"): a result whose rendered
// size is exactly 64 KiB is stored by reference (summary + handle), not held inline.
func TestAgentOffload_AC1_ExactThresholdStoredByReference(t *testing.T) {
	const sessionID = "sess-agent-boundary"
	payload := strings.Repeat("a", agentKiB64)
	require.Equal(t, agentKiB64, len(payload), "payload renders to exactly 64 KiB")

	ag, store := newRenderAgent(t)
	ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)

	before := store.Stats().ItemCount
	rendered := ag.formatToolResult(ctx, sessionID, "web_search",
		&shuttle.Result{Success: true, Data: payload}, nil)

	assert.NotEqual(t, payload, rendered, "a result at exactly 64 KiB is not left inline verbatim")
	assert.Contains(t, rendered, "stored in memory", "a result at exactly 64 KiB is stored by reference")
	assert.Greater(t, store.Stats().ItemCount, before, "the boundary result is written to the store by reference")
}

// --- AC2: a result below 64 KiB stays inline and directly readable ------------

// TestAgentOffload_AC2_SmallResultStaysInline asserts a below-threshold string
// result is returned verbatim, with nothing written to the store.
func TestAgentOffload_AC2_SmallResultStaysInline(t *testing.T) {
	const sessionID = "sess-agent-small"
	payload := "a small, directly readable tool result"

	ag, store := newRenderAgent(t)
	ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)

	before := store.Stats().ItemCount
	rendered := ag.formatToolResult(ctx, sessionID, "web_search",
		&shuttle.Result{Success: true, Data: payload}, nil)

	assert.Equal(t, payload, rendered, "below-threshold result stays inline verbatim")
	assert.Equal(t, before, store.Stats().ItemCount, "nothing is stored for a below-threshold result")
}

// --- AC6: string verbatim, composite JSON, never %v of a Go map ---------------

// TestAgentRender_AC6_StringVerbatimCompositeJSONNeverGoMap asserts the render
// branch: a string Data comes back byte-for-byte, and a composite (map) Data comes
// back as JSON that round-trips — never Go's fmt "%v" map syntax.
func TestAgentRender_AC6_StringVerbatimCompositeJSONNeverGoMap(t *testing.T) {
	const sessionID = "sess-agent-render"
	ag, _ := newRenderAgent(t)
	ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)

	t.Run("string-verbatim", func(t *testing.T) {
		payload := "verbatim: line1\nline2\twith tabs and \"quotes\""
		rendered := ag.formatToolResult(ctx, sessionID, "some_tool",
			&shuttle.Result{Success: true, Data: payload}, nil)
		assert.Equal(t, payload, rendered, "a string result renders verbatim")
	})

	t.Run("composite-json-never-go-map", func(t *testing.T) {
		// %v would render this as `map[alpha:1 beta:2]`; JSON renders `{"alpha":1,"beta":2}`.
		composite := map[string]interface{}{"beta": 2, "alpha": 1}
		rendered := ag.formatToolResult(ctx, sessionID, "some_tool",
			&shuttle.Result{Success: true, Data: composite}, nil)

		assert.NotContains(t, rendered, "map[", "a composite result is never rendered as a Go map (%v)")
		assert.True(t, strings.HasPrefix(strings.TrimSpace(rendered), "{"), "a composite result renders as a JSON object")

		var back map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(rendered), &back), "composite render is valid JSON")
		assert.Equal(t, float64(1), back["alpha"])
		assert.Equal(t, float64(2), back["beta"])
	})
}

// --- AC5: recall tool results always enter whole and are never re-offloaded ----

// TestAgentOffload_AC5_RecallResultsEnterWhole asserts that an oversized output
// named for a recall tool (query_tool_result / get_tool_result) is returned whole
// and is not stored by reference — the guard that breaks the offload→recall→offload
// loop.
func TestAgentOffload_AC5_RecallResultsEnterWhole(t *testing.T) {
	const sessionID = "sess-agent-recall"
	oversized := "RECALL_WHOLE_" + strings.Repeat("y", agentKiB64+16*1024)
	require.Greater(t, len(oversized), agentKiB64, "recall payload is above the 64 KiB threshold")

	for _, name := range []string{"query_tool_result", "get_tool_result"} {
		t.Run(name, func(t *testing.T) {
			ag, store := newRenderAgent(t)
			ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)

			before := store.Stats().ItemCount
			rendered := ag.formatToolResult(ctx, sessionID, name,
				&shuttle.Result{Success: true, Data: oversized}, nil)

			assert.Equal(t, oversized, rendered, "%s output enters whole, never re-offloaded", name)
			assert.Equal(t, before, store.Stats().ItemCount, "%s output is not stored by reference", name)
		})
	}
}

// --- AC4: the manage_skills load body always enters whole regardless of size ---

// buildBigSkillRig builds an agent whose skill library holds one skill with a body
// well above 64 KiB, wired through the orchestrator so manage_skills is registered.
// The per-turn push is disabled so the body reaches the conversation only as a
// manage_skills(load) tool-result — the surface the D-E exemption protects.
func buildBigSkillRig(t *testing.T) *manageSkillsRig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// A single ~80 KiB instruction line (no embedded newlines) under a YAML literal
	// block, so the loaded body renders far above the 64 KiB offload threshold.
	bigBody := "BIG_SKILL_BODY_START_" + strings.Repeat("x", 80*1024) + "_BIG_SKILL_BODY_END"
	yaml := "apiVersion: loom/v1\n" +
		"kind: Skill\n" +
		"metadata:\n" +
		"  name: big-skill\n" +
		"  title: Big Skill\n" +
		"  description: A skill whose body exceeds the offload threshold.\n" +
		"  domain: general\n" +
		"  risk_level: LOW\n" +
		"trigger:\n" +
		"  mode: MANUAL\n" +
		"prompt:\n" +
		"  instructions: |\n" +
		"    " + bigBody + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big-skill.yaml"), []byte(yaml), 0o600))

	lib := skills.NewLibrary(skills.WithSearchPaths(dir))
	orch := skills.NewOrchestrator(lib)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.SkillsConfig = &skills.SkillsConfig{Enabled: false}

	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{}, WithConfig(cfg), WithSkillOrchestrator(orch))
	return &manageSkillsRig{agent: ag, orch: orch, library: lib}
}

// TestAgentOffload_AC4_SkillBodyEntersWhole loads a skill whose real load body is
// above 64 KiB and asserts formatToolResult returns that body whole under the
// manage_skills exemption — the load-as-message contract is never broken by offload.
func TestAgentOffload_AC4_SkillBodyEntersWhole(t *testing.T) {
	const sessionID = "sess-agent-skillbody"
	rig := buildBigSkillRig(t)

	loadRes := invokeManageSkills(t, rig, sessionID,
		map[string]interface{}{"action": "load", "name": "big-skill"})
	require.True(t, loadRes.Success)
	data, ok := loadRes.Data.(string)
	require.True(t, ok, "the load result Data is a string")
	assert.Equal(t, "Skill loaded: big-skill", data,
		"the tool_result is the short confirmation — the body rides the sidecar")

	body, ok := loadRes.Metadata["text_body"].(string)
	require.True(t, ok, "the body sidecar is present in Metadata[text_body]")
	require.Greater(t, len(body), agentKiB64, "the real skill body is above the 64 KiB threshold")
	assert.Contains(t, body, "BIG_SKILL_BODY_START_", "the sidecar body is whole — head intact")
	assert.Contains(t, body, "_BIG_SKILL_BODY_END", "the sidecar body is whole — tail intact")

	// The sidecar bypasses tool-result offload entirely (it enters as a user
	// message), and the small confirmation renders inline, never by-reference.
	ctx := newCtxDumpContext(sessionID, observability.NewNoOpTracer(), nil)
	rendered := rig.agent.formatToolResult(ctx, sessionID, "manage_skills", loadRes, nil)
	assert.Equal(t, data, rendered, "the confirmation enters inline, untouched by offload")
}

// --- M9 integrated seat: what the model actually saw --------------------------

// bigDumpTool returns an above-threshold string, so the M9 dump proves the model
// saw the by-reference summary rather than the raw payload.
type bigDumpTool struct{ payload string }

func (b *bigDumpTool) Name() string        { return "big_dump_tool" }
func (b *bigDumpTool) Description() string { return "returns a large result" }
func (b *bigDumpTool) Backend() string     { return "" }
func (b *bigDumpTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (b *bigDumpTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: b.payload}, nil
}

// compositeDumpTool returns a small Go map, so the M9 dump proves the model saw
// JSON rather than fmt "%v" map syntax.
type compositeDumpTool struct{}

func (c *compositeDumpTool) Name() string        { return "composite_dump_tool" }
func (c *compositeDumpTool) Description() string { return "returns a small composite result" }
func (c *compositeDumpTool) Backend() string     { return "" }
func (c *compositeDumpTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (c *compositeDumpTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: map[string]interface{}{"beta": 2, "alpha": 1}}, nil
}

// toolMessages returns the content of every tool-role message across all dump records.
func toolMessages(recs []contextDumpRecord) []string {
	var out []string
	for _, rec := range recs {
		for _, m := range rec.Messages {
			if m.Role == "tool" {
				out = append(out, m.Content)
			}
		}
	}
	return out
}

// driveToolChat runs one Chat that calls the given tool once and then finalizes,
// with the M9 context dump ON, and returns the dump records for the run.
func driveToolChat(t *testing.T, tool shuttle.Tool, callID string) []contextDumpRecord {
	t.Helper()
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{{ID: callID, Name: tool.Name(), Input: map[string]interface{}{}}}},
		{content: "done"},
	}}

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.Debug.ContextDump = true

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(tool)

	_, err := ag.Chat(context.Background(), "sess-m9", "run the tool")
	require.NoError(t, err)

	return readDumpRecords(t, sinkDir)
}

// TestAgentOffload_M9_ModelSeesByReferenceAndCleanRender asserts, through the M9
// context dump (the integrated seat), that the tool-result message the model sees
// is the by-reference summary for a large result (AC1) and clean JSON — never a Go
// map (%v) — for a composite result (AC6).
func TestAgentOffload_M9_ModelSeesByReferenceAndCleanRender(t *testing.T) {
	t.Run("large-result-seen-by-reference", func(t *testing.T) {
		payload := "M9_LARGE_START\n" + strings.Repeat("dump filler line\n", 5000) + "M9_LARGE_END"
		require.Greater(t, len(payload), agentKiB64, "payload is above the 64 KiB threshold")

		recs := driveToolChat(t, &bigDumpTool{payload: payload}, "m9-large")
		msgs := toolMessages(recs)
		require.NotEmpty(t, msgs, "the model saw a tool-result message")

		joined := strings.Join(msgs, "\n")
		assert.NotContains(t, joined, "dump filler line", "the model never saw the raw large payload")
		assert.Contains(t, joined, "stored in memory", "the model saw the by-reference summary")
	})

	t.Run("composite-result-seen-as-json", func(t *testing.T) {
		recs := driveToolChat(t, &compositeDumpTool{}, "m9-composite")
		msgs := toolMessages(recs)
		require.NotEmpty(t, msgs, "the model saw a tool-result message")

		joined := strings.Join(msgs, "\n")
		assert.NotContains(t, joined, "map[", "the model never saw a Go map (%v) rendering")

		var found bool
		for _, c := range msgs {
			if strings.Contains(c, `{"alpha":1,"beta":2}`) {
				found = true
			}
		}
		assert.True(t, found, "the model saw the composite result rendered as JSON")
	})
}
