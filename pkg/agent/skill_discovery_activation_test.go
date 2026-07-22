// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// D-6 (Skills v4 management) component acceptance tests driving a full
// Agent.Chat turn through a scripted LLMProvider — the real black-box entry
// point for the discovery-menu-as-tail-note and explicit-activation seams.
//
// Covers:
//   - discovery-menu-tail-note-only-no-force-activate-no-inject
//   - manage-skills-patterns-list-load-unload-charter-classed-folder-path
//     (end-to-end: a real Chat-driven load produces a charter-classed
//     Message carrying the skill's folder path)

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

// dumpLLMCall prints the full messages slice handed to the scripted LLM
// when LOOM_TEST_DUMP_CONTEXT=1 is set in the env. Off by default so normal
// test runs stay quiet. Prints role, content (full), tool_calls, and
// tool_use_id per message — the exact artifact the provider layer would
// have converted into an Anthropic/OpenAI request body.
func dumpLLMCall(callIdx int, messages []Message) {
	if os.Getenv("LOOM_TEST_DUMP_CONTEXT") != "1" {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n===== LLM CALL #%d =====\n", callIdx)
	for i, m := range messages {
		fmt.Fprintf(&b, "[%d] role=%s", i, m.Role)
		if m.ToolUseID != "" {
			fmt.Fprintf(&b, " tool_use_id=%s", m.ToolUseID)
		}
		if m.ContextClass != "" {
			fmt.Fprintf(&b, " class=%s", m.ContextClass)
		}
		b.WriteString("\n")
		if m.Content != "" {
			b.WriteString("  content: ")
			b.WriteString(strings.ReplaceAll(m.Content, "\n", "\n           "))
			b.WriteString("\n")
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "  tool_call: id=%s name=%s input=%v\n", tc.ID, tc.Name, tc.Input)
		}
	}
	fmt.Fprintf(&b, "===== END CALL #%d =====\n", callIdx)
	fmt.Print(b.String())
}

// skillTurnScriptedLLM is a minimal LLMProvider fake that returns queued
// responses in order (holding on the last one once exhausted) and records
// every messages slice it was called with, so a test can inspect exactly
// what was sent to the model for a given turn.
type skillTurnScriptedLLM struct {
	mu        sync.Mutex
	responses []LLMResponse
	idx       int
	calls     [][]Message
}

func newSkillTurnScriptedLLM(responses ...LLMResponse) *skillTurnScriptedLLM {
	return &skillTurnScriptedLLM{responses: responses}
}

func (m *skillTurnScriptedLLM) Chat(ctx context.Context, messages []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cp := make([]Message, len(messages))
	copy(cp, messages)
	m.calls = append(m.calls, cp)

	dumpLLMCall(len(m.calls), messages)

	if len(m.responses) == 0 {
		return &LLMResponse{Content: "done", StopReason: "end_turn"}, nil
	}
	idx := m.idx
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	} else {
		m.idx++
	}
	resp := m.responses[idx]
	return &resp, nil
}

func (m *skillTurnScriptedLLM) Name() string  { return "skill-turn-scripted" }
func (m *skillTurnScriptedLLM) Model() string { return "skill-turn-scripted-model" }

func (m *skillTurnScriptedLLM) getCalls() [][]Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]Message, len(m.calls))
	copy(out, m.calls)
	return out
}

// --- Discovery: candidates surface in the ROM catalog, never force-activate ---
//
// The discovery menu previously injected as a Role:"user" tail note (a fake
// user turn appended after the real user message). That shape caused two
// bugs: (1) the LLM treated the menu as the human's request, obeyed the
// "call manage_skills(load, ...)" text, and re-loaded already-active skills
// every turn; (2) two consecutive user messages corrupted the conversation
// shape the Anthropic API expects (alternation).
//
// The fix: router candidates are appended to session.RomCatalog (append-only,
// dedup); Session.GetMessages composes the ROM slot = base ROM +
// [Available Skills] entries filtered against SegmentedMemory.ActiveSkillNames
// (skills currently loaded). No tail-note menu is injected. No fake user turn.
func TestSkillDiscovery_CandidatesLandInROMCatalog_NeverActivate(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{
		Name:  "always-on-skill",
		Title: "Always On",
		Trigger: skills.SkillTrigger{
			Mode: skills.ActivationAlways,
		},
		Prompt: skills.SkillPrompt{Instructions: "Stay quiet unless asked."},
	})
	orch := skills.NewOrchestrator(lib)

	llm := newSkillTurnScriptedLLM(LLMResponse{Content: "hello back", StopReason: "end_turn"})

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithSkillOrchestrator(orch))

	sessionID := "sess-discovery"
	resp, err := ag.Chat(context.Background(), sessionID, "hi there")
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Discovery never activates — the active set stays empty until an
	// explicit manage_skills(load) call.
	assert.Empty(t, orch.GetActiveSkills(sessionID),
		"discovery must never force-activate a candidate skill")

	// Candidates landed in the session's ROM catalog.
	session, ok := ag.memory.GetSession(sessionID)
	require.True(t, ok)
	catalogNames := make([]string, 0, len(session.RomCatalog))
	for _, e := range session.RomCatalog {
		catalogNames = append(catalogNames, e.Name)
	}
	assert.Contains(t, catalogNames, "always-on-skill",
		"the router's candidate must be in the session's ROM catalog")

	// The LLM saw the catalog in the SYSTEM slot (not as a fake user turn).
	calls := llm.getCalls()
	require.NotEmpty(t, calls, "the scripted LLM must have been called at least once")
	sent := calls[0]
	require.NotEmpty(t, sent, "the LLM call must have received a non-empty message slice")

	require.Equal(t, "system", sent[0].Role,
		"first message must be the system-role ROM")
	assert.Contains(t, sent[0].Content, "[Available Skills]",
		"the ROM must carry the skill catalog section")
	assert.Contains(t, sent[0].Content, "always-on-skill",
		"the candidate skill must appear in the ROM catalog")

	// FORBIDDEN: no message carries the old tail-note "[Skill Discovery]"
	// header — the anti-pattern is gone. Also no message duplicates the
	// menu content as a user turn after the real user message.
	for _, m := range sent {
		assert.NotContains(t, m.Content, "[Skill Discovery]",
			"the tail-note menu is deleted; skills live in the ROM catalog now")
	}

	// The persisted session must not carry the ROM catalog content in any
	// message — the catalog lives in Session.RomCatalog (a session field),
	// never in a stored Message.
	for _, m := range session.GetMessages() {
		if m.Role == "system" {
			// System-role output IS composed at read-time by GetMessages;
			// it's not a persisted Message. The stored history holds no
			// system message.
			continue
		}
		assert.NotContains(t, m.Content, "always-on-skill",
			"stored session messages must not carry ROM catalog content")
	}
}

func TestSkillDiscovery_NoCandidates_NoMenuInTailNote(t *testing.T) {
	// A library with no matching skill at all: the tail note (if any, e.g.
	// from other beat content) must not contain a discovery section.
	lib := newEmptySkillLibrary(t)
	orch := skills.NewOrchestrator(lib)

	llm := newSkillTurnScriptedLLM(LLMResponse{Content: "ok", StopReason: "end_turn"})
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithSkillOrchestrator(orch))

	_, err := ag.Chat(context.Background(), "sess-empty", "hi")
	require.NoError(t, err)

	calls := llm.getCalls()
	require.NotEmpty(t, calls)
	for _, m := range calls[0] {
		assert.NotContains(t, m.Content, "[Skill Discovery]")
	}
}

// --- Activation: only manage_skills(load) activates; the resulting event is charter-classed and carries the folder path ---

func TestSkillActivation_ManageSkillsLoad_ActivatesAndClassifiesCharterWithFolderPath(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{
		Name:       "explicit-load-skill",
		Title:      "Explicit Load",
		SourcePath: "/skills/explicit-load-skill.yaml",
		Prompt:     skills.SkillPrompt{Instructions: "Do the explicit thing."},
	})
	orch := skills.NewOrchestrator(lib)

	llm := newSkillTurnScriptedLLM(
		LLMResponse{
			Content:    "",
			StopReason: "tool_use",
			ToolCalls: []ToolCall{
				{
					ID:   "call_1",
					Name: "manage_skills",
					Input: map[string]interface{}{
						"action": "load",
						"name":   "explicit-load-skill",
					},
				},
			},
		},
		LLMResponse{Content: "Loaded the skill.", StopReason: "end_turn"},
	)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithSkillOrchestrator(orch))

	sessionID := "sess-activate"
	resp, err := ag.Chat(context.Background(), sessionID, "please load explicit-load-skill")
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.ToolExecutions, 1)
	require.True(t, resp.ToolExecutions[0].Result.Success)

	// The active set is now driven by the explicit manage_skills(load) call.
	actives := orch.GetActiveSkills(sessionID)
	require.Len(t, actives, 1)
	assert.Equal(t, "explicit-load-skill", actives[0].Skill.Name)

	// The persisted tool-result message is charter-classed and carries the
	// skill's folder path in its ToolResult.
	session, ok := ag.memory.GetSession(sessionID)
	require.True(t, ok)
	var found bool
	for _, m := range session.GetMessages() {
		if m.Role != "tool" || m.ToolResult == nil {
			continue
		}
		// Load result: Data is the skill body (string); operational fields
		// (skill/source_path/etc.) live in Metadata. See executeLoad.
		body, isString := m.ToolResult.Data.(string)
		if !isString || m.ToolResult.Metadata == nil {
			continue
		}
		meta := m.ToolResult.Metadata
		if meta["skill"] != "explicit-load-skill" {
			continue
		}
		found = true
		assert.Equal(t, ClassNarrative, m.ContextClass,
			"a manage_skills load result must be narrative-classed so fold summarizes into residue")
		assert.Equal(t, "/skills/explicit-load-skill.yaml", meta["source_path"],
			"the persisted load event must carry the skill's folder path")
		assert.Contains(t, body, "Do the explicit thing.",
			"load result Data must be the raw skill body markdown, not a metadata blob")
	}
	require.True(t, found, "expected to find the manage_skills load tool-result message in session history")
}
