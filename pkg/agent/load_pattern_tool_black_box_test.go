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
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// These tests drive the load_pattern builtin (story D-P) through the real
// Agent.Chat loop with a scripted, deterministic LLM (mockToolCallingLLM), a real
// pattern Library/Orchestrator loaded from on-disk YAML fixtures, and the M9
// context dump switched ON. They assert D-P's acceptance criteria against the
// tool's own Result and the per-provider-call dump records — the on-demand pull
// surface (test-arch Entry 8, the M9 dump).
//
// Per-turn pattern selection is left off here (PatternConfig.Enabled=false), so
// the ONLY way a pattern can reach the context is a load_pattern tool-result —
// which is exactly the pull semantics under test. load_pattern is registered
// whenever a pattern library is configured, independent of that switch.
//
// The system-slot pattern injection these tests prove load_pattern never produces
// was removed: GetMessagesForLLM no longer renders a "# Relevant Pattern Guidance"
// block, and no setter populates SegmentedMemory's active-pattern slot, so
// GetActivePattern stays empty. load_pattern must leave that slot empty — the
// pattern body appears only as an L1 tool-result.

// --- fixtures ---------------------------------------------------------------

// patternTitleSentinel is embedded in the sample pattern's title, so its rendered
// FormatForLLM header ("## Pattern: ...") carries a token unique to this suite.
// Tests locate the pattern body in dump records by this marker.
const patternTitleSentinel = "LOADPATTERN-SENTINEL-TITLE"

// samplePatternRef is the reference (file base name) the model passes to
// load_pattern; it is what manage_skills(load) would surface from a skill's
// PatternRefs.
const samplePatternRef = "sample-pattern"

// bigPatternRef names a pattern whose rendered body is tens of KiB — larger than
// the offload byte threshold the AC2 offload test configures.
const bigPatternRef = "big-pattern"

// samplePatternYAML is a small, valid pattern. Its title carries the sentinel so
// the rendered body is locatable in the message stream.
const samplePatternYAML = `name: sample-pattern
title: Sample Pattern ` + patternTitleSentinel + `
description: A small pattern used to exercise on-demand load_pattern retrieval.
category: analytics
use_cases:
  - Verifying on-demand pattern retrieval
`

// writeLoadPatternFixtures writes the pattern library to a fresh temp dir and
// returns the directory for Config.PatternsDir. The big pattern's body is padded
// far past any small byte threshold so it exercises the size-threshold offload.
func writeLoadPatternFixtures(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "patterns")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(dir, samplePatternRef+".yaml"),
		[]byte(samplePatternYAML), 0o600))

	// A single-line plain YAML scalar of ~42 KiB — no colons/newlines, so it
	// parses as one description value and renders large enough to cross the
	// offload threshold.
	bigDesc := strings.Repeat("filler ", 6000)
	bigYAML := "name: " + bigPatternRef + "\ntitle: Big Pattern\ndescription: " + bigDesc + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, bigPatternRef+".yaml"),
		[]byte(bigYAML), 0o600))

	return dir
}

// --- rig --------------------------------------------------------------------

// buildLoadPatternAgent constructs an agent over the scripted LLM with the
// fixture pattern library wired and the legacy pattern push disabled. dump toggles
// the M9 context dump. threshold, when >= 0, is applied as the shared-memory byte
// threshold (and re-wired into the executor) so a test can force large results to
// offload by reference or keep small ones inline.
func buildLoadPatternAgent(t *testing.T, llm LLMProvider, dump bool, threshold int64) *Agent {
	t.Helper()

	dir := writeLoadPatternFixtures(t)

	cfg := DefaultConfig()
	cfg.PatternsDir = dir
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false // deterministic scripted LLM
	// Pull-only: disable the legacy intent-match + system-slot injection so a
	// pattern reaches the context solely through a load_pattern tool-result.
	cfg.PatternConfig.Enabled = false
	cfg.Debug.ContextDump = dump

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))

	if threshold >= 0 {
		ag.SetSharedMemoryThreshold(threshold)
		// Re-wire the executor and memory with the new threshold (SetSharedMemory
		// re-reads sharedMemoryThreshold when wiring the executor).
		ag.SetSharedMemory(ag.sharedMemory)
	}

	return ag
}

// loadPatternCall scripts one assistant turn that calls load_pattern(reference).
func loadPatternCall(id, reference string) mockLLMResponse {
	return mockLLMResponse{toolCalls: []llmtypes.ToolCall{{
		ID:    id,
		Name:  "load_pattern",
		Input: map[string]interface{}{"reference": reference},
	}}}
}

// loadPatternResults returns, in order, the Result of every load_pattern
// execution in a response.
func loadPatternResults(resp *Response) []*shuttle.Result {
	var out []*shuttle.Result
	for _, e := range resp.ToolExecutions {
		if e.ToolName == "load_pattern" {
			out = append(out, e.Result)
		}
	}
	return out
}

// sessionSegmentedMemory returns the session's SegmentedMemory — the holder of
// the system-slot pattern injection (GetActivePattern) these tests inspect.
func sessionSegmentedMemory(t *testing.T, ag *Agent, sessionID string) *SegmentedMemory {
	t.Helper()
	sess := ag.memory.GetOrCreateSession(context.Background(), sessionID)
	require.NotNil(t, sess)
	segMem, ok := sess.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "session must hold a *SegmentedMemory")
	return segMem
}

// expectedPatternBody loads a pattern through the agent's own library and returns
// its FormatForLLM rendering — the exact content load_pattern must return.
func expectedPatternBody(t *testing.T, ag *Agent, reference string) string {
	t.Helper()
	lib := ag.GetOrchestrator().GetLibrary()
	require.NotNil(t, lib)
	pat, err := lib.Load(reference)
	require.NoError(t, err)
	return pat.FormatForLLM()
}

// --- AC: load_pattern-returns-content-to-L1 ----------------------------------

// TestLoadPattern_ReturnsContentAsToolResultInL1 asserts a load_pattern(reference)
// call returns the referenced pattern's FormatForLLM content, and that under the
// Chat drive that content lands as a tool-result message appended to L1 — never
// in a system/user/assistant message.
func TestLoadPattern_ReturnsContentAsToolResultInL1(t *testing.T) {
	const sessionID = "sess-ac1"
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadPatternCall("c1", samplePatternRef),
		finalTurn(),
	}}
	// High threshold keeps the small pattern inline, so the full body is visible
	// in the tool-result message rather than offloaded to a reference.
	ag := buildLoadPatternAgent(t, llm, true, 1<<20)

	// The tool's OWN Result carries the referenced pattern's content verbatim.
	tool := findRegisteredTool(ag, "load_pattern")
	require.NotNil(t, tool, "load_pattern is registered")
	direct, err := tool.Execute(session.WithSessionID(context.Background(), sessionID),
		map[string]interface{}{"reference": samplePatternRef})
	require.NoError(t, err)
	require.True(t, direct.Success)
	body, ok := direct.Data.(string)
	require.True(t, ok, "load_pattern Data is the rendered pattern body string")
	assert.Equal(t, expectedPatternBody(t, ag, samplePatternRef), body,
		"load_pattern returns the referenced pattern's FormatForLLM content")
	assert.Contains(t, body, patternTitleSentinel)

	// Under the Chat drive the content lands only as a tool-result message in L1.
	_, err = ag.Chat(context.Background(), sessionID, "load the sample pattern")
	require.NoError(t, err)

	recs := readDumpRecords(t, sinkDir)
	sawInToolMessage := false
	for _, rec := range recs {
		for _, m := range rec.Messages {
			if strings.Contains(m.Content, patternTitleSentinel) {
				assert.Equal(t, "tool", m.Role,
					"pattern content may appear only in a tool-role message, not role %q", m.Role)
				if m.Role == "tool" {
					sawInToolMessage = true
				}
			}
		}
	}
	assert.True(t, sawInToolMessage, "the pattern content appears as a tool-result message in L1")
}

// --- AC: load_pattern-never-system-slot --------------------------------------

// TestLoadPattern_NeverSystemSlotInjection asserts a loaded pattern never becomes
// a system-slot injection: the session's active-pattern slot stays empty and no
// system-role message (in particular the "# Relevant Pattern Guidance" injection)
// carries the pattern content — it lives only in a tool-role message.
func TestLoadPattern_NeverSystemSlotInjection(t *testing.T) {
	const sessionID = "sess-ac3"
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadPatternCall("c1", samplePatternRef),
		finalTurn(),
	}}
	ag := buildLoadPatternAgent(t, llm, true, 1<<20)

	_, err := ag.Chat(context.Background(), sessionID, "load the sample pattern")
	require.NoError(t, err)

	// The system-slot pattern injection channel was never touched.
	segMem := sessionSegmentedMemory(t, ag, sessionID)
	assert.Empty(t, segMem.GetActivePattern(),
		"load_pattern must not populate the system-slot active pattern")

	// No system-role message carries the pattern body, and the guidance-injection
	// header never appears. The content lives only in a tool-role message.
	recs := readDumpRecords(t, sinkDir)
	sawInToolMessage := false
	for _, rec := range recs {
		for _, m := range rec.Messages {
			assert.NotContains(t, m.Content, "# Relevant Pattern Guidance",
				"no system-slot pattern-guidance injection may appear")
			if strings.Contains(m.Content, patternTitleSentinel) {
				assert.Equal(t, "tool", m.Role,
					"pattern content must never appear in a %q-role message", m.Role)
				if m.Role == "tool" {
					sawInToolMessage = true
				}
			}
		}
	}
	assert.True(t, sawInToolMessage, "the pattern reached the context as a tool-result, not a system slot")
}

// --- AC: load_pattern-subject-to-threshold-and-compaction --------------------

// TestLoadPattern_LargeOffloadsByReference asserts the load_pattern tool-result
// enters the conversation WHOLE regardless of size (HLD §1: arrival appends;
// large-result offload is a pure render condition of the compile, not an
// arrival event — no reference is ever created).
func TestLoadPattern_LargeOffloadsByReference(t *testing.T) {
	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadPatternCall("big", bigPatternRef),
		finalTurn(),
		loadPatternCall("small", samplePatternRef),
		finalTurn(),
	}}
	// A 4 KiB threshold sits between the small pattern (~hundreds of bytes) and
	// the big pattern (~42 KiB).
	ag := buildLoadPatternAgent(t, llm, false, 4096)

	bigResp, err := ag.Chat(context.Background(), "sess-ac2-big", "load the big pattern")
	require.NoError(t, err)
	bigResults := loadPatternResults(bigResp)
	require.Len(t, bigResults, 1)
	require.True(t, bigResults[0].Success)
	assert.Nil(t, bigResults[0].DataReference,
		"no reference is created at arrival — the result enters whole (HLD §1)")

	smallResp, err := ag.Chat(context.Background(), "sess-ac2-small", "load the sample pattern")
	require.NoError(t, err)
	smallResults := loadPatternResults(smallResp)
	require.Len(t, smallResults, 1)
	require.True(t, smallResults[0].Success)
	assert.Nil(t, smallResults[0].DataReference,
		"a small result likewise carries no reference")
}

// TestLoadPattern_UnknownReferenceErrorNoContext asserts an unknown pattern
// reference returns an error result and that no pattern enters the context: the
// system-slot pattern stays empty and no pattern body appears in the message
// stream.
func TestLoadPattern_UnknownReferenceErrorNoContext(t *testing.T) {
	const sessionID = "sess-ac4"
	const unknownRef = "no-such-pattern"
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadPatternCall("c1", unknownRef),
		finalTurn(),
	}}
	ag := buildLoadPatternAgent(t, llm, true, 1<<20)

	// The tool's OWN Result is an error; no pattern content is produced.
	tool := findRegisteredTool(ag, "load_pattern")
	require.NotNil(t, tool, "load_pattern is registered")
	direct, err := tool.Execute(session.WithSessionID(context.Background(), sessionID),
		map[string]interface{}{"reference": unknownRef})
	require.NoError(t, err)
	require.False(t, direct.Success, "unknown reference returns an error result")
	require.NotNil(t, direct.Error)
	assert.Equal(t, "PATTERN_NOT_FOUND", direct.Error.Code)
	assert.Nil(t, direct.Data, "an error result carries no pattern content")

	// Driving the same unknown load through Chat: no pattern enters the context.
	_, err = ag.Chat(context.Background(), sessionID, "load a pattern that does not exist")
	require.NoError(t, err)

	segMem := sessionSegmentedMemory(t, ag, sessionID)
	assert.Empty(t, segMem.GetActivePattern(),
		"an unknown reference must not populate the system-slot active pattern")

	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs)
	sawErrorInToolMessage := false
	for _, rec := range recs {
		for _, m := range rec.Messages {
			assert.NotContains(t, m.Content, "## Pattern:",
				"no pattern body may enter the context on an unknown reference")
			assert.NotContains(t, m.Content, "# Relevant Pattern Guidance",
				"no system-slot pattern injection on an unknown reference")
			if m.Role == "tool" && strings.Contains(m.Content, "PATTERN_NOT_FOUND") {
				sawErrorInToolMessage = true
			}
		}
	}
	assert.True(t, sawErrorInToolMessage, "the unknown reference surfaces as an error tool-result")
}

// --- AC: load_pattern-base-advertised-when-library-configured ----------------

// TestLoadPattern_BaseAdvertisedWhenLibraryConfigured asserts load_pattern is a
// base advertised tool whenever a pattern library is configured: it is registered
// at construction, advertised from the very first provider call (before any tool
// runs), and callable in the scripted drive. Its registration is gated — suppressing
// the builtin removes it — so the advertisement is a managed decision, not
// unconditional.
func TestLoadPattern_BaseAdvertisedWhenLibraryConfigured(t *testing.T) {
	t.Run("advertised-and-callable", func(t *testing.T) {
		sinkDir := redirectCtxDumpSink(t)
		llm := &mockToolCallingLLM{responses: []mockLLMResponse{
			loadPatternCall("c1", samplePatternRef),
			finalTurn(),
		}}
		ag := buildLoadPatternAgent(t, llm, true, 1<<20)

		require.NotNil(t, findRegisteredTool(ag, "load_pattern"),
			"load_pattern registered when a pattern library is configured")

		resp, err := ag.Chat(context.Background(), "sess-ac5", "load the sample pattern")
		require.NoError(t, err)

		results := loadPatternResults(resp)
		require.Len(t, results, 1)
		assert.True(t, results[0].Success, "load_pattern is callable")

		// Advertised as a base tool from the very first provider call, before the
		// tool executes.
		recs := readDumpRecords(t, sinkDir)
		require.NotEmpty(t, recs)
		assert.True(t, toolNamesInRecord(recs[0])["load_pattern"],
			"load_pattern advertised from the first turn (base tool)")
	})

	t.Run("absent-when-suppressed", func(t *testing.T) {
		dir := writeLoadPatternFixtures(t)
		cfg := DefaultConfig()
		cfg.PatternsDir = dir
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		cfg.PatternConfig.Enabled = false

		ag := NewAgent(&mockBackend{}, &mockSimpleLLM{},
			WithConfig(cfg), WithoutBuiltinTool("load_pattern"))

		assert.Nil(t, findRegisteredTool(ag, "load_pattern"),
			"load_pattern is not registered when the builtin is suppressed")
	})
}
