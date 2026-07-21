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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

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

// --- Discovery: candidate menu appears only as a tail note, never force-activates ---

func TestSkillDiscovery_MenuAppearsAsTailNoteOnly_NeverActivates(t *testing.T) {
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

	// Discovery never activates: the always-on skill matched a candidate,
	// but the active set must remain empty until an explicit
	// manage_skills(load) call.
	assert.Empty(t, orch.GetActiveSkills(sessionID),
		"discovery must never force-activate a candidate skill")

	calls := llm.getCalls()
	require.NotEmpty(t, calls, "the scripted LLM must have been called at least once")

	sent := calls[0]
	require.NotEmpty(t, sent, "the LLM call must have received a non-empty message slice")

	last := sent[len(sent)-1]
	assert.Equal(t, "user", last.Role,
		"the discovery menu must be carried as a Role:\"user\" tail note, never system")
	assert.Contains(t, last.Content, "[Skill Discovery]",
		"the tail note sent to the LLM must contain the discovery menu")
	assert.Contains(t, last.Content, "always-on-skill")
	assert.Contains(t, last.Content, "not active",
		"the menu text must make explicit that candidates are not active")

	// The tail note is a local, per-call construct only: it must never be
	// persisted into the session's stored message history, nor smuggled in
	// as system-role content (there is no inject channel left — D-2 deleted
	// FormatActiveSkillsForLLM/InjectSkills).
	session, ok := ag.memory.GetSession(sessionID)
	require.True(t, ok)
	for _, m := range session.GetMessages() {
		assert.NotContains(t, m.Content, "[Skill Discovery]",
			"the discovery menu tail note must never be persisted to session history, system or otherwise")
		if m.Role == "system" {
			assert.NotContains(t, m.Content, "always-on-skill",
				"no system-role message may carry skill content (no inject channel)")
		}
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
		assert.Equal(t, ClassCharter, m.ContextClass,
			"a manage_skills load result must be a charter-classed event")
		assert.Equal(t, "/skills/explicit-load-skill.yaml", meta["source_path"],
			"the persisted load event must carry the skill's folder path")
		assert.Contains(t, body, "Do the explicit thing.",
			"load result Data must be the raw skill body markdown, not a metadata blob")
	}
	require.True(t, found, "expected to find the manage_skills load tool-result message in session history")
}
