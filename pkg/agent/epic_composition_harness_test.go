// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License").
//
// TER-633 — Loom context & skill management: COMPOSITION suite (whole-feature).
//
// This file and its zz_epic_eacN siblings are the epic-level composition suite.
// They are NOT product code: run.sh stages them into the built loom tree at
// pkg/agent/ (package agent, in-process, mock-free), runs `GOWORK=off go test
// -tags fts5 -run TestEpic ./pkg/agent/`, then removes them. They reuse the
// task-delivered in-package helpers (readDumpRecords, loadCall, finalTurn,
// activeSkillNames, advertisedNames, toolNamesInRecord, buildRestoreAgent,
// newTestErrorStore, storeOwnedError, loadMarkerFor, captureZap,
// onlyMutationRecord, …) rather than redeclaring them.
//
// Every scenario drives a REAL Agent (NewAgent + a scripted deterministic
// mockToolCallingLLM) with a real skill orchestrator/library + real pattern
// library from on-disk fixtures, the M9 context-dump switch ON, across a
// scripted multi-turn Agent.Chat, and observes emergent whole-feature behavior
// through the single M9 dump instrument ({session_id, turn, messages, tools}
// per provider call). Per the ratified test architecture (plan/25-test-arch.md).

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/storage"
)

// ---------------------------------------------------------------------------
// Fixture skills — a self-consistent bound set exercising every AC surface.
// Written to a temp dir and loaded by a real skills.Library. Mirrors the
// delivered fixture YAML shape (metadata.risk_level, tools.required_tools,
// pattern_refs) so it parses identically.
// ---------------------------------------------------------------------------

const (
	epicSkillBasic     = "epic-basic"
	epicSkillHighRisk  = "epic-highrisk"
	epicSkillTooled    = "epic-tooled"
	epicSkillPatterned = "epic-patterned"

	epicBasicDesc     = "Basic grant helper."
	epicHighRiskDesc  = "High-risk grant executor."
	epicTooledDesc    = "Grant skill needing web_search."
	epicPatternedDesc = "Grant skill with a join pattern."

	// epicRequiredTool is a builtin the tooled skill declares as required. A
	// builtin is scoped-by-default, so it is absent from a session's advertised
	// tools until the skill loads and registers it into that session's ledger.
	epicRequiredTool = "web_search"

	// epicPatternRef is a pattern the patterned skill references and that exists
	// in the fixture pattern library.
	epicPatternRef = "epic-join"

	// epicEmitToolName is the composition suite's scriptable tool: it returns a
	// tool-result of a caller-controlled size and shape (string / composite map /
	// oversized), the raw material for the large-result and budget scenarios.
	epicEmitToolName = "emit"
)

var epicSkillFixtures = map[string]string{
	epicSkillBasic + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: ` + epicSkillBasic + `
  title: Epic Basic
  description: ` + epicBasicDesc + `
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Epic basic grant instructions body.
  constraints:
    - Keep responses terse.
`,
	epicSkillHighRisk + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: ` + epicSkillHighRisk + `
  title: Epic High Risk
  description: ` + epicHighRiskDesc + `
  domain: ops
  risk_level: HIGH
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Epic high-risk grant instructions body.
`,
	epicSkillTooled + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: ` + epicSkillTooled + `
  title: Epic Tooled
  description: ` + epicTooledDesc + `
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Epic tooled grant instructions body.
tools:
  required_tools:
    - ` + epicRequiredTool + `
`,
	epicSkillPatterned + ".yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: ` + epicSkillPatterned + `
  title: Epic Patterned
  description: ` + epicPatternedDesc + `
  domain: analytics
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Epic patterned grant instructions body.
pattern_refs:
  - ` + epicPatternRef + `
`,
}

var epicPatternFixtures = map[string]string{
	epicPatternRef + ".yaml": `name: ` + epicPatternRef + `
title: Epic Join Pattern
description: Join strategy pattern used to exercise on-demand pattern retrieval.
category: analytics
use_cases:
  - Verifying on-demand pattern retrieval
`,
}

// epicWriteSkills writes the fixture skill library to a temp dir and returns it.
func epicWriteSkills(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "epic-skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range epicSkillFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// epicWritePatterns writes the fixture pattern library to a temp dir.
func epicWritePatterns(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "epic-patterns")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range epicPatternFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	return dir
}

// epicBindings binds every fixture skill LAZY: the skill is named in the ROM
// menu (static, byte-stable) but never pushed — it enters the conversation only
// when the agent calls manage_skills(load). This is the pull-only design under
// test; Enabled must be true for the menu to render.
func epicBindings() []skills.SkillBinding {
	return []skills.SkillBinding{
		{Name: epicSkillBasic, Mode: skills.BindingLazy},
		{Name: epicSkillHighRisk, Mode: skills.BindingLazy},
		{Name: epicSkillTooled, Mode: skills.BindingLazy},
		{Name: epicSkillPatterned, Mode: skills.BindingLazy},
	}
}

// ---------------------------------------------------------------------------
// epicEmitTool — a scriptable tool producing controlled tool-results.
//   input {"bytes": N}       -> a string result of ~N bytes (inline vs offload)
//   input {"map": true}      -> a composite map result (JSON render check)
//   input {"text": "..."}    -> that exact string result (verbatim render check)
//   default                  -> a small "emit-ok" string
// ---------------------------------------------------------------------------

type epicEmitTool struct{}

func (t *epicEmitTool) Name() string { return epicEmitToolName }
func (t *epicEmitTool) Description() string {
	return "Emit a controlled tool-result for composition tests."
}
func (t *epicEmitTool) Backend() string { return "" }
func (t *epicEmitTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}

func (t *epicEmitTool) Execute(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	if n, ok := epicInt(params["bytes"]); ok && n > 0 {
		const filler = "epic filler row of grant audit text\n" // 36 bytes
		var sb strings.Builder
		sb.WriteString("EMIT_START\n")
		for sb.Len() < n {
			sb.WriteString(filler)
		}
		sb.WriteString("EMIT_END")
		return &shuttle.Result{Success: true, Data: sb.String()}, nil
	}
	if b, ok := params["map"].(bool); ok && b {
		note, _ := params["text"].(string)
		return &shuttle.Result{Success: true, Data: map[string]interface{}{
			"kind":  "composite",
			"count": 3,
			"rows":  []interface{}{"grant-a", "grant-b", "grant-c"},
			"note":  note,
		}}, nil
	}
	if s, ok := params["text"].(string); ok && s != "" {
		return &shuttle.Result{Success: true, Data: s}, nil
	}
	return &shuttle.Result{Success: true, Data: "emit-ok"}, nil
}

func epicInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// epicEmitCall scripts an emit tool call with the given input.
func epicEmitCall(id string, input map[string]interface{}) mockLLMResponse {
	return mockLLMResponse{toolCalls: []ToolCall{{ID: id, Name: epicEmitToolName, Input: input}}}
}

// epicLoadPatternCall scripts a load_pattern call.
func epicLoadPatternCall(id, reference string) mockLLMResponse {
	return mockLLMResponse{toolCalls: []ToolCall{{
		ID: id, Name: "load_pattern",
		Input: map[string]interface{}{"reference": reference},
	}}}
}

// epicListCall scripts a manage_skills(list) call.
func epicListCall(id string) mockLLMResponse {
	return mockLLMResponse{toolCalls: []ToolCall{{
		ID: id, Name: "manage_skills",
		Input: map[string]interface{}{"action": "list"},
	}}}
}

// ---------------------------------------------------------------------------
// epicRig — one unified real Agent with the whole feature wired: skill
// orchestrator+library (bound, pull-only), pattern library, shared-memory
// offload (64 KiB default), optional durable session store + error store, the
// M9 dump ON to a per-run temp sink, and a controllable context window.
// ---------------------------------------------------------------------------

type epicOpts struct {
	dump                 bool
	checker              *shuttle.PermissionChecker
	store                *SessionStore
	errStore             ErrorStore
	maxContextTokens     int
	reservedOutputTokens int
	sharedThreshold      int64 // 0 => leave 64 KiB default; nonzero => override
	orchOpts             []skills.OrchestratorOption
}

type epicRig struct {
	agent      *Agent
	orch       *skills.Orchestrator
	lib        *skills.Library
	shared     *storage.SharedMemoryStore
	sinkDir    string
	skillsDir  string
	patternDir string
}

// epicNewRig builds the unified composition Agent. sinkDir isolation is set via
// LOOM_DEBUG_DIR so each rig's dump lands in its own temp dir; the dump is
// enabled through cfg.Debug.ContextDump (config path, independent of env).
func epicNewRig(t *testing.T, llm LLMProvider, o epicOpts) *epicRig {
	t.Helper()

	skillsDir := epicWriteSkills(t)
	patternDir := epicWritePatterns(t)

	lib := skills.NewLibrary(skills.WithSearchPaths(skillsDir))
	orch := skills.NewOrchestrator(lib, o.orchOpts...)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.PatternsDir = patternDir
	cfg.Debug.ContextDump = o.dump
	// Pull-only: LAZY bindings render the ROM menu without any per-turn push.
	cfg.SkillsConfig = &skills.SkillsConfig{Enabled: true, Bindings: epicBindings()}
	if o.maxContextTokens > 0 {
		cfg.MaxContextTokens = o.maxContextTokens
		cfg.ReservedOutputTokens = o.reservedOutputTokens
	}

	opts := []Option{WithConfig(cfg), WithSkillOrchestrator(orch), WithoutSelfCorrection()}
	if o.checker != nil {
		opts = append(opts, WithPermissionChecker(o.checker))
	}
	if o.store != nil {
		opts = append(opts, WithMemory(NewMemoryWithStore(o.store)))
	}
	if o.errStore != nil {
		opts = append(opts, WithErrorStore(o.errStore))
	}

	sinkDir := ""
	if o.dump {
		sinkDir = redirectCtxDumpSink(t)
	}

	ag := NewAgent(&mockBackend{}, llm, opts...)
	if o.maxContextTokens > 0 {
		ag.memory.SetContextLimits(o.maxContextTokens, o.reservedOutputTokens)
	}

	shared := storage.NewSharedMemoryStore(&storage.Config{
		MaxMemoryBytes:       50 * 1024 * 1024,
		CompressionThreshold: 8 * 1024 * 1024,
		TTLSeconds:           3600,
	})
	ag.SetSharedMemory(shared)
	if o.sharedThreshold != 0 {
		ag.SetSharedMemoryThreshold(o.sharedThreshold)
	}

	// Register the scriptable emit tool (post-construction; a non-builtin the
	// scenarios call for controlled tool-results).
	ag.tools.Register(&epicEmitTool{})

	return &epicRig{
		agent:      ag,
		orch:       orch,
		lib:        lib,
		shared:     shared,
		sinkDir:    sinkDir,
		skillsDir:  skillsDir,
		patternDir: patternDir,
	}
}

// epicChat drives one user turn end-to-end.
func (r *epicRig) chat(t *testing.T, sessionID, msg string) *Response {
	t.Helper()
	resp, err := r.agent.Chat(context.Background(), sessionID, msg)
	require.NoError(t, err)
	return resp
}

// epicSession returns the live session for direct segment/active-set reads.
func (r *epicRig) session(t *testing.T, sessionID string) *Session {
	t.Helper()
	s := r.agent.memory.GetOrCreateSessionWithAgent(
		context.Background(), sessionID, r.agent.config.Name, "")
	require.NotNil(t, s)
	return s
}

// ---------------------------------------------------------------------------
// Dump readers — filtered by agent id so a test that constructs more than one
// Agent (e.g. the restart harness) can read each agent's own records even when
// they share a sink dir.
// ---------------------------------------------------------------------------

// epicReadDump returns the dump records the given agent wrote to sinkDir.
func epicReadDump(t *testing.T, sinkDir string, ag *Agent) []contextDumpRecord {
	t.Helper()
	want := "context-dump-" // prefix
	matches, err := filepath.Glob(filepath.Join(sinkDir, want+"*-"+sanitizeForFilename(ag.id)+".jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "a dump sink file exists for agent %q", ag.id)
	require.Len(t, matches, 1, "exactly one sink file for agent %q", ag.id)

	data, err := os.ReadFile(matches[0])
	require.NoError(t, err)

	var recs []contextDumpRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rc contextDumpRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rc))
		recs = append(recs, rc)
	}
	return recs
}

// epicSystemMessages returns the system-role messages of a dump record, in order.
func epicSystemMessages(rec contextDumpRecord) []Message {
	var out []Message
	for _, m := range rec.Messages {
		if m.Role == "system" {
			out = append(out, m)
		}
	}
	return out
}

// epicROM returns the ROM system message content (messages[0], the static head).
func epicROM(rec contextDumpRecord) string {
	sys := epicSystemMessages(rec)
	if len(sys) == 0 {
		return ""
	}
	return sys[0].Content
}

// epicL2Summary returns the "Previous conversation summary: …" system message
// content if compaction has produced one, else "".
func epicL2Summary(rec contextDumpRecord) string {
	for _, m := range epicSystemMessages(rec) {
		if strings.HasPrefix(m.Content, "Previous conversation summary: ") {
			return m.Content
		}
	}
	return ""
}

// epicToolNames returns the sorted advertised tool names in a dump record.
func epicToolNames(rec contextDumpRecord) []string {
	names := make([]string, 0, len(rec.Tools))
	for _, tl := range rec.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

// epicJSON marshals for byte-comparison of message/tool slices.
func epicJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// epicIsPrefixExtension reports whether cur is a strict prefix-extension of prev:
// every prev message byte-identical at the same index, cur no shorter. Used to
// prove append-only growth between compactions.
func epicIsPrefixExtension(t *testing.T, prev, cur []Message) bool {
	t.Helper()
	if len(cur) < len(prev) {
		return false
	}
	for i := range prev {
		if epicJSON(t, prev[i]) != epicJSON(t, cur[i]) {
			return false
		}
	}
	return true
}

// epicRunSQLResult / metadata helper for the manage_skills load marker read.
func epicLoadMarker(results []*shuttle.Result, skill string) map[string]interface{} {
	for _, r := range results {
		if r == nil || r.Metadata == nil {
			continue
		}
		if name, _ := r.Metadata["skill"].(string); name == skill {
			return r.Metadata
		}
	}
	return nil
}

// epicMemStats reads the session's segmented-memory stats map.
func epicMemStats(t *testing.T, s *Session) map[string]interface{} {
	t.Helper()
	sm, ok := s.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "session carries a *SegmentedMemory")
	return sm.GetMemoryStats()
}

func epicStatInt(m map[string]interface{}, key string) int {
	switch n := m[key].(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func epicStatFloat(m map[string]interface{}, key string) float64 {
	switch n := m[key].(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

// epicFatalf is a labelled failure used by the scenario drivers.
func epicFatalf(t *testing.T, format string, a ...interface{}) {
	t.Helper()
	t.Fatalf(format, a...)
}

// epicRealJudge reports whether the credential-bound arms (real bedrock LLM /
// judge) should execute. Off on this host (no local LLM surface, per test-arch
// Entry 10 GAP); flipped on in dev/CI where AWS creds exist.
func epicRealJudge() bool {
	v := strings.TrimSpace(os.Getenv("LOOM_EPIC_REAL_JUDGE"))
	return v == "1" || strings.EqualFold(v, "true")
}

var _ = fmt.Sprintf // keep fmt imported for scenario files that format evidence
