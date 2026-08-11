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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// These tests assert story D-J: the compiled system slot never carries a pattern
// block or a findings block, on any turn, regardless of how many tool-results
// accumulate. They drive the real Agent.Chat loop with a scripted, deterministic
// LLM (mockToolCallingLLM) and read the M9 context-dump — the exact (messages,
// tools) dispatched to the provider per call — asserting the invariant against
// every captured turn.
//
// A pattern block is a system message headed "# Relevant Pattern Guidance"; a
// findings block is a system message headed "## Verified Findings (Working
// Memory)". Neither may appear in the compiled context. The system slot carries
// only the ROM (stable per session) plus the cumulative L2 summary.

// patternGuidanceHeader is the header a pattern block carries. Its appearance in
// any compiled message is an AC1 breach.
const patternGuidanceHeader = "# Relevant Pattern Guidance"

// findingsBlockHeader is the header a findings block carries. Its appearance in
// any compiled message is an AC2 breach.
const findingsBlockHeader = "## Verified Findings (Working Memory)"

// l2SummaryPrefix identifies the summary system message — since the compile
// emits the summary's newest version as-is (HLD §5.2 step 3), the tests
// install summaries carrying this sentinel prefix.
const l2SummaryPrefix = "S1-L2-SUMMARY"

// --- rig: a tool that accumulates tool-results -------------------------------

// bulkDataTool returns a fixed payload per call, so a scripted drive can
// accumulate many large tool-results across turns.
type bulkDataTool struct {
	payload string
}

func (t *bulkDataTool) Name() string        { return "fetch_data" }
func (t *bulkDataTool) Description() string { return "Returns a chunk of data for a query" }
func (t *bulkDataTool) Backend() string     { return "" }
func (t *bulkDataTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"query": {Type: "string"}},
	}
}
func (t *bulkDataTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: t.payload}, nil
}

// fetchDataCall scripts one assistant turn that calls fetch_data.
func fetchDataCall(id string) mockLLMResponse {
	return mockLLMResponse{toolCalls: []llmtypes.ToolCall{{
		ID:    id,
		Name:  "fetch_data",
		Input: map[string]interface{}{"query": "SELECT * FROM t"},
	}}}
}

// countToolExecutions returns how many times the named tool ran in a response.
func countToolExecutions(resp *Response, name string) int {
	n := 0
	for _, e := range resp.ToolExecutions {
		if e.ToolName == name {
			n++
		}
	}
	return n
}

// --- invariant: system-slot-ROM-only -----------------------------------------

// assertSystemSlotROMOnly is the D-J invariant assertion. Across every captured
// provider call it asserts: (1) no compiled message, in any role, carries the
// pattern block or findings block; and (2) the system slot holds only the
// ROM — the first system message, stable across turns — plus, at most, the
// cumulative L2 summary. Any other system-slot content is a breach.
func assertSystemSlotROMOnly(t *testing.T, recs []contextDumpRecord) {
	t.Helper()
	require.NotEmpty(t, recs, "at least one provider-call dump is expected")

	var rom string
	romSeen := false
	for _, rec := range recs {
		firstSystemSeen := false
		for _, m := range rec.Messages {
			// The block headers may never appear in any compiled message,
			// whatever its role.
			assert.NotContains(t, m.Content, patternGuidanceHeader,
				"turn %d: a pattern block must never appear in the compiled context", rec.Turn)
			assert.NotContains(t, m.Content, findingsBlockHeader,
				"turn %d: a findings block must never appear in the compiled context", rec.Turn)

			if m.Role != "system" {
				continue
			}
			if !firstSystemSeen {
				firstSystemSeen = true
				if !romSeen {
					rom, romSeen = m.Content, true
				} else {
					assert.Equal(t, rom, m.Content,
						"turn %d: the ROM system message is stable across turns", rec.Turn)
				}
				continue
			}
			// The only system-slot content beyond the ROM is the cumulative L2
			// summary.
			assert.True(t, strings.HasPrefix(m.Content, l2SummaryPrefix),
				"turn %d: system slot carries only ROM + L2 summary, got a system message %q",
				rec.Turn, head(m.Content, 80))
		}
	}
}

// someRecordHasL2 reports whether any captured turn's system slot carried the
// cumulative L2 summary — evidence that compaction ran and was captured.
func someRecordHasL2(recs []contextDumpRecord) bool {
	for _, rec := range recs {
		for _, m := range rec.Messages {
			if m.Role == "system" && strings.HasPrefix(m.Content, l2SummaryPrefix) {
				return true
			}
		}
	}
	return false
}

// head returns the first n characters of s for readable failure messages.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// --- AC1 + AC2: full-lifecycle system slot stays ROM-only (Method Entry 2) ----

// TestChannelsDeleted_S1_SystemSlotROMOnlyAcrossLifecycle drives one session
// through the S1 lifecycle — load a pattern, do tool work, compact L1 into L2,
// then grant a second pattern — and asserts on EVERY captured provider call that
// the system slot holds only the ROM plus the cumulative L2 summary: no pattern
// block (AC1) and no findings block (AC2) is ever compiled in.
func TestChannelsDeleted_S1_SystemSlotROMOnlyAcrossLifecycle(t *testing.T) {
	const sessionID = "sess-dj-s1"
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		// load: pull a pattern via the on-demand tool
		loadPatternCall("p1", samplePatternRef), finalTurn(),
		// work: accumulate tool-results
		fetchDataCall("f1"), fetchDataCall("f2"), fetchDataCall("f3"), fetchDataCall("f4"), finalTurn(),
		// second grant: pull another pattern (after compaction)
		loadPatternCall("p2", samplePatternRef), finalTurn(),
	}}
	// Dump ON; small results kept inline so the work phase stays under the L1
	// budget and compaction is driven explicitly below.
	ag := buildLoadPatternAgent(t, llm, true, 1<<20)
	ag.RegisterTool(&bulkDataTool{payload: "small work result row"})

	// load
	loadResp, err := ag.Chat(context.Background(), sessionID, "load the sample pattern")
	require.NoError(t, err)
	require.Equal(t, 1, countToolExecutions(loadResp, "load_pattern"), "the load phase pulled one pattern")

	// work
	workResp, err := ag.Chat(context.Background(), sessionID, "analyze the fetched data")
	require.NoError(t, err)
	require.Equal(t, 4, countToolExecutions(workResp, "fetch_data"), "the work phase produced four tool-results")

	// fold: install a summary version directly (compaction triggers are
	// deleted — fold is the only writer, exercised via relief elsewhere)
	segMem := sessionSegmentedMemory(t, ag, sessionID)
	segMem.setSummary(1, "S1-L2-SUMMARY")
	require.Contains(t, segMem.GetL2Summary(), "S1-L2-SUMMARY", "the summary version installed")

	// second grant
	grantResp, err := ag.Chat(context.Background(), sessionID, "load the sample pattern again")
	require.NoError(t, err)
	require.Equal(t, 1, countToolExecutions(grantResp, "load_pattern"), "the second grant pulled one pattern")

	// The invariant holds on every captured turn — including the post-compaction
	// turns that carry an L2 summary in the system slot.
	recs := readDumpRecords(t, sinkDir)
	assertSystemSlotROMOnly(t, recs)
	assert.True(t, someRecordHasL2(recs),
		"a post-compaction turn carried the L2 summary — the ROM+L2 case was exercised")
}

// --- AC2: no findings block regardless of tool-result volume (Method Entry 5) -

// TestChannelsDeleted_S2_NoFindingsBlockUnderHeavyToolResults drives the S2
// data-heavy scenario: many large tool-results (~8 KiB each) accumulate well past
// 64 KiB in one session. It asserts no findings block is ever compiled into the
// system slot despite the volume — the "regardless of how many tool results
// accumulated" clause of AC2.
func TestChannelsDeleted_S2_NoFindingsBlockUnderHeavyToolResults(t *testing.T) {
	const (
		sessionID   = "sess-dj-s2"
		fetchCalls  = 12
		payloadSize = 8 * 1024
	)
	sinkDir := redirectCtxDumpSink(t)

	responses := make([]mockLLMResponse, 0, fetchCalls+1)
	for i := 0; i < fetchCalls; i++ {
		responses = append(responses, fetchDataCall("f"+string(rune('a'+i))))
	}
	responses = append(responses, finalTurn())
	llm := &mockToolCallingLLM{responses: responses}

	// Dump ON; a high offload threshold keeps each result inline, so the payload
	// volume genuinely enters the compiled context.
	ag := buildLoadPatternAgent(t, llm, true, 1<<20)
	ag.RegisterTool(&bulkDataTool{payload: strings.Repeat("D", payloadSize)})

	resp, err := ag.Chat(context.Background(), sessionID, "crunch the whole dataset")
	require.NoError(t, err)

	// The volume the invariant must survive: many tool-results, straddling 64 KiB.
	got := countToolExecutions(resp, "fetch_data")
	require.Equal(t, fetchCalls, got, "every scripted fetch_data ran")
	require.Greater(t, got*payloadSize, 64*1024, "accumulated tool-results straddle 64 KiB")

	recs := readDumpRecords(t, sinkDir)
	assertSystemSlotROMOnly(t, recs)
}

// --- AC1: a pulled pattern never reaches the system slot (Method Entry 8) ------

// TestChannelsDeleted_Entry8_PatternLandsInL1NotSystemSlot reconfirms AC1 from the
// pull side: loading a pattern via load_pattern lands its body in L1 as a
// tool-result message and never as a system-slot injection. The pattern body
// (located by its title sentinel) appears only in a tool-role message, and no
// pattern block appears in the system slot on any turn.
func TestChannelsDeleted_Entry8_PatternLandsInL1NotSystemSlot(t *testing.T) {
	const sessionID = "sess-dj-entry8"
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadPatternCall("c1", samplePatternRef),
		finalTurn(),
	}}
	// High threshold keeps the small pattern inline, so its full body is visible
	// in the tool-result message.
	ag := buildLoadPatternAgent(t, llm, true, 1<<20)

	_, err := ag.Chat(context.Background(), sessionID, "load the sample pattern")
	require.NoError(t, err)

	recs := readDumpRecords(t, sinkDir)

	// System slot stays ROM-only — no pattern block from the pull.
	assertSystemSlotROMOnly(t, recs)

	// The pattern body reached the context only as a tool-result message in L1.
	sawInToolMessage := false
	for _, rec := range recs {
		for _, m := range rec.Messages {
			if strings.Contains(m.Content, patternTitleSentinel) {
				assert.Equal(t, "tool", m.Role,
					"turn %d: the pattern body may appear only in a tool-role message, not role %q",
					rec.Turn, m.Role)
				if m.Role == "tool" {
					sawInToolMessage = true
				}
			}
		}
	}
	assert.True(t, sawInToolMessage, "the pulled pattern landed in L1 as a tool-result, never the system slot")
}

// --- AC1: the enabled per-turn selection path stays ROM-only -----------------

// enabledSelectionSentinel is embedded in the fixture pattern's title, so any
// appearance of the pattern body in a compiled message is detectable.
const enabledSelectionSentinel = "DJ-ENABLED-SELECTION-SENTINEL"

// enabledSelectionPatternYAML is a pattern whose keywords strongly match the
// drive message below, so the keyword classifier selects it with high confidence.
const enabledSelectionPatternYAML = `name: workflow_multi_agent
title: Multi-Agent Workflow Pattern ` + enabledSelectionSentinel + `
description: Design and implement multi-agent workflows with coordinators and sub-agents for data processing.
category: workflow
use_cases:
  - Multi-agent data processing pipelines
  - Distributed task execution
  - Agent collaboration systems
`

// writeEnabledSelectionFixture writes the selection fixture pattern to a fresh
// temp dir and returns the directory for Config.PatternsDir.
func writeEnabledSelectionFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "patterns")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow_multi_agent.yaml"),
		[]byte(enabledSelectionPatternYAML), 0o600))
	return dir
}

// TestChannelsDeleted_EnabledSelectionPath_NoSystemSlotInjection drives the
// per-turn pattern selection path with PatternConfig.Enabled=true, which loads a
// pattern for effectiveness tracking once it crosses the confidence threshold. It
// first confirms the drive message actually selects the pattern (so that branch
// runs), then asserts the compiled system slot stays ROM-only and the selected
// pattern's body never enters any message.
func TestChannelsDeleted_EnabledSelectionPath_NoSystemSlotInjection(t *testing.T) {
	const sessionID = "sess-dj-enabled"
	const driveMessage = "Create a multi-agent workflow for data processing"
	sinkDir := redirectCtxDumpSink(t)

	cfg := DefaultConfig()
	cfg.PatternsDir = writeEnabledSelectionFixture(t)
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false // deterministic keyword classifier
	cfg.PatternConfig.Enabled = true           // exercise the per-turn selection path
	cfg.PatternConfig.MinConfidence = 0.0      // any keyword match counts as a selection
	cfg.Debug.ContextDump = true

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))

	// Precondition: the drive message selects the pattern, so the enabled per-turn
	// selection reaches the confidence-threshold branch that loads it. Without this
	// the ROM-only assertion below would pass vacuously.
	orch := ag.GetOrchestrator()
	require.NotNil(t, orch)
	intent, _ := orch.ClassifyIntent(driveMessage, map[string]interface{}{"backend_type": "mock", "session_id": sessionID})
	name, conf := orch.RecommendPattern(driveMessage, intent)
	require.NotEmpty(t, name, "the drive message selects a pattern (exercises the selection branch)")
	require.GreaterOrEqual(t, conf, cfg.PatternConfig.MinConfidence)

	_, err := ag.Chat(context.Background(), sessionID, driveMessage)
	require.NoError(t, err)

	recs := readDumpRecords(t, sinkDir)
	// System slot stays ROM-only even though per-turn selection ran and selected a
	// pattern — no pattern block (AC1), no findings block (AC2).
	assertSystemSlotROMOnly(t, recs)
	// The selected pattern's body never entered any compiled message.
	for _, rec := range recs {
		for _, m := range rec.Messages {
			assert.NotContains(t, m.Content, enabledSelectionSentinel,
				"turn %d: the selected pattern's body must not enter the context via the push path", rec.Turn)
		}
	}
}
