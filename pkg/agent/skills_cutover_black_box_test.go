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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/skills"
	skillbinding "github.com/teradata-labs/loom/pkg/skills/binding"
)

// These tests assert story D-B: the skills cutover from a per-turn push to a
// static ROM menu plus an on-demand manage_skills(load) pull. They drive the real
// Agent.Chat loop with a scripted, deterministic LLM (mockToolCallingLLM), a real
// skill Library/Orchestrator with a KNOWN bound-skill set, and the M9 context
// dump — the exact (messages, tools) dispatched per provider call — switched ON.
// Every acceptance criterion is asserted against the captured dump records.
//
//	AC1: at session creation ROM lists the bound skills as a `name — description`
//	     menu, matched against the binding resolver's view of the configuration.
//	AC2: ROM (menu included) is byte-identical on every turn of the session.
//	AC3: the system slot holds only ROM (+ the cumulative L2 summary after
//	     compaction) — no per-turn skill/pattern/findings block ever appears.
//	AC4: a skill body enters the context only as a manage_skills(load) tool-result
//	     appended to L1, prefixed "Skill loaded: <name>", verbatim body.
//	AC5: between two compactions each turn's messages are a strict prefix-extension
//	     of the previous turn's — prior messages byte-identical, only the new turn
//	     appended.
//	AC6: the sole operation that rewrites already-compiled messages is a compaction.

// --- bound-skill set ---------------------------------------------------------

// skillsCutoverBoundBindings is the KNOWN bound-skill set for these tests: three
// of the fixture library's skills, bound by exact name. The other fixtures
// (pattern-skill, tooled-skill, danger-skill) are deliberately left unbound so
// the ROM menu is a checkable subset, not the whole library.
var skillsCutoverBoundBindings = []skills.SkillBinding{
	{Name: "alpha-skill", Mode: skills.BindingLazy},
	{Name: "beta-skill", Mode: skills.BindingLazy},
	{Name: "gamma-skill", Mode: skills.BindingLazy},
}

// skillsCutoverBoundMenu maps each bound skill to the fixture description the ROM
// menu must render after the em-dash. Fixed values guard against a wrong list
// passing (e.g. the resolver returning skills the config never bound).
var skillsCutoverBoundMenu = map[string]string{
	"alpha-skill": "First plain test skill.",
	"beta-skill":  "Second plain test skill.",
	"gamma-skill": "Third plain test skill.",
}

// skillsCutoverUnboundSkills are fixture library skills the config does NOT bind;
// none may appear in the ROM menu.
var skillsCutoverUnboundSkills = []string{"pattern-skill", "tooled-skill", "danger-skill"}

// alphaBodySentinel / betaBodySentinel are unique fragments of each skill's
// rendered instructions body (FormatForLLM output). They appear in a load
// tool-result but never in the ROM menu, which carries only name + description.
const (
	alphaBodySentinel = "Alpha skill instructions body"
	betaBodySentinel  = "Beta skill instructions body"
)

// --- rig ---------------------------------------------------------------------

// skillsCutoverRig bundles the driven agent with the live library, orchestrator,
// and the config it was built from, so a test can re-resolve the bindings the ROM
// menu was rendered from.
type skillsCutoverRig struct {
	agent *Agent
	orch  *skills.Orchestrator
	lib   *skills.Library
	cfg   *Config
}

// buildSkillsCutoverRig constructs an agent over the scripted LLM with the fixture
// library/orchestrator wired and the bound-skill set attached. SkillsConfig is
// Enabled so the ROM menu renders; the legacy pattern push is off. dump toggles
// the M9 context dump. Tool results are kept inline (high offload threshold) so
// skill bodies stay visible in the dump and message growth is pure append.
func buildSkillsCutoverRig(t *testing.T, llm LLMProvider, dump bool) *skillsCutoverRig {
	t.Helper()

	dir := writeSkillFixtures(t)
	lib := skills.NewLibrary(skills.WithSearchPaths(dir))
	orch := skills.NewOrchestrator(lib)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false // deterministic scripted LLM
	cfg.PatternConfig.Enabled = false          // no legacy pattern push
	cfg.SkillsConfig = &skills.SkillsConfig{
		Enabled:  true, // render the static ROM skill menu
		Bindings: skillsCutoverBoundBindings,
	}
	cfg.Debug.ContextDump = dump

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithSkillOrchestrator(orch))
	// Keep tool results inline so skill bodies are visible in the dump and message
	// growth is a pure append (not offloaded to a shared-memory reference).
	ag.SetSharedMemoryThreshold(1 << 20)
	ag.SetSharedMemory(ag.sharedMemory)

	return &skillsCutoverRig{agent: ag, orch: orch, lib: lib, cfg: cfg}
}

// --- dump helpers ------------------------------------------------------------

// firstSystemMessage returns the content of a record's first system message —
// the ROM slot the byte-stability and menu assertions inspect.
func firstSystemMessage(t *testing.T, rec contextDumpRecord) string {
	t.Helper()
	for _, m := range rec.Messages {
		if m.Role == "system" {
			return m.Content
		}
	}
	t.Fatalf("turn %d dump record carries no system message", rec.Turn)
	return ""
}

// messageJSON serializes one message to its dump byte-shape for byte-identity
// comparison between turns.
func messageJSON(t *testing.T, m Message) string {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

// isPrefixExtension reports whether cur is a prefix-extension of prev: every
// message of prev is byte-identical to the same-index message of cur, and cur is
// no shorter. A false result means already-compiled messages were rewritten.
func isPrefixExtension(t *testing.T, prev, cur []Message) bool {
	t.Helper()
	if len(cur) < len(prev) {
		return false
	}
	for i := range prev {
		if messageJSON(t, prev[i]) != messageJSON(t, cur[i]) {
			return false
		}
	}
	return true
}

// recordHasL2Summary reports whether a record's system slot carries the
// cumulative L2 summary — the signature that compaction has run.
func recordHasL2Summary(rec contextDumpRecord) bool {
	for _, m := range rec.Messages {
		if m.Role == "system" && strings.HasPrefix(m.Content, l2SummaryPrefix) {
			return true
		}
	}
	return false
}

// firstL2Index returns the index of the first record whose system slot carries
// the L2 summary — the compaction boundary — or -1 if none does.
func firstL2Index(recs []contextDumpRecord) int {
	for i, rec := range recs {
		if recordHasL2Summary(rec) {
			return i
		}
	}
	return -1
}

// --- S1 drive ----------------------------------------------------------------

// runSkillsCutoverS1 drives the S1 grant workflow on one session — grant a skill
// (manage_skills load), do tool work, compact L1 into L2, then grant a second
// skill — with the dump ON, and returns every captured provider-call record. It
// exercises the pre- and post-compaction epochs the append-only and compaction
// invariants (AC2/AC3/AC5/AC6) assert against.
func runSkillsCutoverS1(t *testing.T) []contextDumpRecord {
	t.Helper()
	const sessionID = "sess-db-s1"
	sinkDir := redirectCtxDumpSink(t)

	responses := []mockLLMResponse{
		// grant 1: pull a skill body via manage_skills(load)
		loadCall("g1", "alpha-skill"), finalTurn(),
	}
	// work: accumulate several tool-results so compaction has L1 to age out
	for i := 0; i < 6; i++ {
		responses = append(responses, fetchDataCall("f"+string(rune('a'+i))))
	}
	responses = append(responses, finalTurn())
	// grant 2: pull a second skill body after compaction
	responses = append(responses, loadCall("g2", "beta-skill"), finalTurn())

	llm := &mockToolCallingLLM{responses: responses}
	rig := buildSkillsCutoverRig(t, llm, true)
	rig.agent.RegisterTool(&bulkDataTool{payload: "small work result row"})

	// grant 1
	loadResp, err := rig.agent.Chat(context.Background(), sessionID, "load the alpha skill")
	require.NoError(t, err)
	require.Len(t, manageSkillsResults(loadResp), 1, "grant 1 pulled one skill")

	// work
	workResp, err := rig.agent.Chat(context.Background(), sessionID, "crunch the data")
	require.NoError(t, err)
	require.Equal(t, 6, countToolExecutions(workResp, "fetch_data"), "the work phase produced six tool-results")

	// compaction: age the earlier turns out of L1 into the L2 summary
	segMem := sessionSegmentedMemory(t, rig.agent, sessionID)
	segMem.SetCompressor(&mockCompressor{
		enabled:    true,
		compressFn: func(msgs []Message) string { return "S1-L2-SUMMARY" },
	})
	segMem.CompactMemory(context.Background())
	require.Contains(t, segMem.GetL2Summary(), "S1-L2-SUMMARY", "compaction produced an L2 summary")

	// grant 2
	grantResp, err := rig.agent.Chat(context.Background(), sessionID, "load the beta skill")
	require.NoError(t, err)
	require.Len(t, manageSkillsResults(grantResp), 1, "grant 2 pulled one skill")

	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs, "the S1 drive captured provider-call records")
	return recs
}

// --- AC1: ROM lists the bound skills as a name — description menu -------------

// TestSkillsCutover_AC1_ROMContainsBoundSkillMenuAtSessionCreation asserts the
// turn-1 ROM renders each bound skill as a `- name — description` line, matched
// against the binding resolver's view of the configured bindings, with unbound
// library skills and all skill bodies absent.
func TestSkillsCutover_AC1_ROMContainsBoundSkillMenuAtSessionCreation(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}
	rig := buildSkillsCutoverRig(t, llm, true)

	_, err := rig.agent.Chat(context.Background(), "sess-db-ac1", "hello")
	require.NoError(t, err)

	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs)
	rom := firstSystemMessage(t, recs[0])

	// The menu the ROM renders is exactly what the resolver resolves from the
	// configured bindings.
	resolved, err := skillbinding.NewResolver(rig.lib).Resolve(rig.cfg.SkillsConfig)
	require.NoError(t, err)
	require.Len(t, resolved, len(skillsCutoverBoundBindings), "resolver resolves the bound set")

	assert.Contains(t, rom, "AVAILABLE SKILLS", "ROM carries the skill menu header")
	for _, rb := range resolved {
		line := fmt.Sprintf("- %s — %s", rb.Skill.Name, rb.Skill.Description)
		assert.Contains(t, rom, line,
			"ROM lists bound skill %q as `name — description`", rb.Skill.Name)
	}
	// Fixed descriptions: a resolver returning the wrong skills would miss these.
	for name, desc := range skillsCutoverBoundMenu {
		assert.Contains(t, rom, fmt.Sprintf("- %s — %s", name, desc),
			"ROM menu line for %q matches the fixture description", name)
	}
	// Unbound library skills never appear in the menu.
	for _, name := range skillsCutoverUnboundSkills {
		assert.NotContains(t, rom, "- "+name+" —",
			"unbound skill %q must not appear in the ROM menu", name)
	}
	// The menu names skills without loading any body: no instructions in the ROM.
	assert.NotContains(t, rom, alphaBodySentinel,
		"ROM carries the name/description menu, never a skill body")
}

// --- AC2: ROM byte-identical every turn, static skill list included ----------

// TestSkillsCutover_AC2_ROMByteIdenticalEveryTurnWithStaticSkillList asserts the
// ROM segment — the static skill menu included — is byte-identical on every turn
// of the S1 session, across the load, work, compaction, and second-grant turns.
func TestSkillsCutover_AC2_ROMByteIdenticalEveryTurnWithStaticSkillList(t *testing.T) {
	recs := runSkillsCutoverS1(t)
	require.Greater(t, len(recs), 1, "multiple turns are captured to compare")

	rom0 := firstSystemMessage(t, recs[0])
	// The static skill list is part of the byte-stable ROM being compared.
	require.Contains(t, rom0, "AVAILABLE SKILLS", "turn-1 ROM carries the static skill menu")
	require.Contains(t, rom0, "- alpha-skill — First plain test skill.",
		"turn-1 ROM carries the bound-skill menu list")

	for _, rec := range recs {
		assert.Equal(t, rom0, firstSystemMessage(t, rec),
			"turn %d ROM is byte-identical to turn 1 (static skill list included)", rec.Turn)
	}
}

// --- AC3: system slot = ROM (+ cumulative L2) only; no per-turn block ---------

// TestSkillsCutover_AC3_SystemSlotIsROMPlusL2OnlyNoPerTurnSkillBlock asserts that
// on every captured turn the system slot holds only the ROM plus, after
// compaction, the cumulative L2 summary — never a per-turn skill, pattern, or
// findings block — and that no loaded skill body ever reaches a system message.
func TestSkillsCutover_AC3_SystemSlotIsROMPlusL2OnlyNoPerTurnSkillBlock(t *testing.T) {
	recs := runSkillsCutoverS1(t)

	// ROM stable across turns; only the L2 summary joins it in the system slot;
	// no pattern or findings block appears in any compiled message.
	assertSystemSlotROMOnly(t, recs)
	require.True(t, someRecordHasL2(recs),
		"a post-compaction turn carried the L2 summary — the ROM+L2 case was exercised")

	// The per-turn skill push is gone: a loaded skill body never enters a system
	// message (it lives only in an L1 tool-result, asserted by AC4).
	for _, rec := range recs {
		for _, m := range rec.Messages {
			if m.Role != "system" {
				continue
			}
			assert.NotContains(t, m.Content, alphaBodySentinel,
				"turn %d: no skill body in the system slot", rec.Turn)
			assert.NotContains(t, m.Content, betaBodySentinel,
				"turn %d: no skill body in the system slot", rec.Turn)
		}
	}
}

// --- AC4: a skill body enters only as a load tool-result in L1 ----------------

// TestSkillsCutover_AC4_SkillBodyEntersOnlyAsLoadToolResultInL1 asserts a skill
// body reaches the conversation only through a manage_skills(load) tool-result:
// the tool's own Result is the confirmation prefix plus the verbatim FormatForLLM
// body, and under the Chat drive that content appears solely in a tool-role
// message appended to L1 — never in a system/user/assistant message.
func TestSkillsCutover_AC4_SkillBodyEntersAsUserSidecarConfirmationAsToolResult(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c1", "alpha-skill"),
		finalTurn(),
	}}
	rig := buildSkillsCutoverRig(t, llm, true)

	// Tool contract: the load Result splits — Data carries only the short
	// confirmation; the verbatim body rides Metadata["text_body"] (the sidecar
	// the agent loop appends as a user-role message).
	skill, err := rig.lib.Load("alpha-skill")
	require.NoError(t, err)

	tool := findRegisteredTool(rig.agent, "manage_skills")
	require.NotNil(t, tool, "manage_skills is registered")
	direct, err := tool.Execute(session.WithSessionID(context.Background(), "sess-db-ac4-direct"),
		map[string]interface{}{"action": "load", "name": "alpha-skill"})
	require.NoError(t, err)
	require.True(t, direct.Success)
	data, ok := direct.Data.(string)
	require.True(t, ok, "load Data is a string")
	assert.Equal(t, "Skill loaded: alpha-skill", data,
		"load Data is the confirmation only — the body never rides the tool_result")
	body, ok := direct.Metadata["text_body"].(string)
	require.True(t, ok, "load carries the body sidecar in Metadata[text_body]")
	assert.Equal(t, skill.FormatForLLM(), body, "the sidecar is the verbatim FormatForLLM body")

	// Pull semantics under the Chat drive: the body enters the context exactly
	// once, as a user-role sidecar; the tool_result in context is confirmation-only.
	_, err = rig.agent.Chat(context.Background(), "sess-db-ac4", "load the alpha skill")
	require.NoError(t, err)

	const marker = "Skill loaded: alpha-skill"
	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs)
	final := recs[len(recs)-1]
	sidecars, confirmations := 0, 0
	for _, m := range final.Messages {
		if strings.Contains(m.Content, alphaBodySentinel) {
			assert.Equal(t, "user", m.Role,
				"turn %d: the skill body may appear only as a user-role sidecar, not role %q",
				final.Turn, m.Role)
			sidecars++
		}
		if m.Role == "tool" && strings.Contains(m.Content, marker) {
			assert.NotContains(t, m.Content, alphaBodySentinel,
				"the tool_result carries the confirmation only, never the body")
			confirmations++
		}
	}
	assert.Equal(t, 1, sidecars, "the body entered exactly once, as the user-role sidecar")
	assert.Equal(t, 1, confirmations, "the load tool_result (confirmation) is in the context")
}

// TestSkillsCutover_SidecarAdjacency_ParallelToolBatch asserts the drain-order
// law: when the model fires manage_skills(load) IN PARALLEL with another tool,
// every tool_result of the batch lands before the skill-body sidecar — the
// sidecar never splits a tool_use from its tool_result (Anthropic pairing).
func TestSkillsCutover_SidecarAdjacency_ParallelToolBatch(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)

	llm := &mockToolCallingLLM{responses: []mockLLMResponse{
		{toolCalls: []llmtypes.ToolCall{
			{ID: "c1", Name: "manage_skills",
				Input: map[string]interface{}{"action": "load", "name": "alpha-skill"}},
			{ID: "c2", Name: "calculator",
				Input: map[string]interface{}{"expression": "2+2"}},
		}},
		finalTurn(),
	}}
	rig := buildSkillsCutoverRig(t, llm, true)

	_, err := rig.agent.Chat(context.Background(), "sess-sidecar-adjacency", "load alpha and compute")
	require.NoError(t, err)

	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs)
	final := recs[len(recs)-1]

	lastToolIdx, sidecarIdx := -1, -1
	for i, m := range final.Messages {
		if m.Role == "tool" {
			lastToolIdx = i
		}
		if m.Role == "user" && strings.Contains(m.Content, alphaBodySentinel) {
			require.Equal(t, -1, sidecarIdx, "the body sidecar appears exactly once")
			sidecarIdx = i
		}
	}
	require.GreaterOrEqual(t, lastToolIdx, 0, "the batch's tool_results are in the context")
	require.GreaterOrEqual(t, sidecarIdx, 0, "the body sidecar is in the context")
	assert.Greater(t, sidecarIdx, lastToolIdx,
		"the sidecar drains after EVERY tool_result of the batch")
}

// --- AC5: messages are a prefix-extension between compactions ------------------

// TestSkillsCutover_AC5_MessagesArePrefixExtensionBetweenCompactions asserts that
// within each compaction epoch of the S1 run — before and after the single
// compaction — every turn's messages are a strict prefix-extension of the
// previous turn's: prior messages byte-identical, only the new turn appended.
func TestSkillsCutover_AC5_MessagesArePrefixExtensionBetweenCompactions(t *testing.T) {
	recs := runSkillsCutoverS1(t)
	require.True(t, someRecordHasL2(recs), "compaction fired — an epoch boundary exists")

	boundary := firstL2Index(recs)
	require.Greater(t, boundary, 0, "a pre-compaction epoch exists")
	require.Less(t, boundary, len(recs), "a post-compaction epoch exists")

	assertEpochPrefixExtension(t, recs[:boundary], "pre-compaction")
	assertEpochPrefixExtension(t, recs[boundary:], "post-compaction")
}

// assertEpochPrefixExtension asserts every consecutive turn in one compaction
// epoch is a prefix-extension of the prior, and that the epoch's messages grew —
// proof the turns only appended, never rewrote.
func assertEpochPrefixExtension(t *testing.T, epoch []contextDumpRecord, label string) {
	t.Helper()
	require.GreaterOrEqual(t, len(epoch), 2, "%s epoch has multiple turns to compare", label)
	for i := 1; i < len(epoch); i++ {
		assert.True(t, isPrefixExtension(t, epoch[i-1].Messages, epoch[i].Messages),
			"%s: turn %d messages are a strict prefix-extension of turn %d",
			label, epoch[i].Turn, epoch[i-1].Turn)
	}
	assert.Greater(t, len(epoch[len(epoch)-1].Messages), len(epoch[0].Messages),
		"%s: messages grew by appending across the epoch", label)
}

// --- AC6: only a compaction rewrites already-compiled messages ----------------

// TestSkillsCutover_AC6_OnlyCompactionRewritesCompiledMessages asserts that
// across the whole S1 run exactly one turn-to-turn transition rewrites
// already-compiled messages, and that transition is the compaction — the turn
// where the L2 summary first appears. Every other transition is a pure L1 append.
func TestSkillsCutover_AC6_OnlyCompactionRewritesCompiledMessages(t *testing.T) {
	recs := runSkillsCutoverS1(t)
	require.True(t, someRecordHasL2(recs), "compaction fired — a rewrite is expected exactly once")
	require.Greater(t, len(recs), 1, "multiple turns are captured to compare")

	rewriteCount := 0
	rewriteIsCompaction := false
	for i := 1; i < len(recs); i++ {
		if isPrefixExtension(t, recs[i-1].Messages, recs[i].Messages) {
			continue // pure append — L1 grew, prior messages untouched
		}
		rewriteCount++
		// The rewrite coincides with the compaction: the L2 summary is absent from
		// the prior turn and present on this one.
		if !recordHasL2Summary(recs[i-1]) && recordHasL2Summary(recs[i]) {
			rewriteIsCompaction = true
		}
	}

	assert.Equal(t, 1, rewriteCount, "exactly one turn-to-turn rewrite across the run")
	assert.True(t, rewriteIsCompaction,
		"the sole rewrite of already-compiled messages is the compaction")
}
