// Copyright 2026 Teradata
package agent

// The "load body enters context" event carries one effect: the skill's
// tool wiring (orchestrator activation + required-tool registration).
// These tests pin the effect at both entry points of the event:
//
//	live load      — executeLoad fires wiring immediately, within the same
//	                 turn the body lands (not a turn later).
//	restore        — replay puts a resident load body back into L1; the
//	                 same effect re-fires in the new process: orchestrator
//	                 re-activated, required tools registered.
//	fold + restore — a folded-out load is NOT resident after restore; no
//	                 activation fires and the catalog re-offers the skill.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/skills"
)

func newWiringSkillLibrary(t *testing.T) *skills.Library {
	t.Helper()
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{
		Name:  "td-wire",
		Title: "wire",
		Trigger: skills.SkillTrigger{
			Keywords: []string{"wire"},
			Mode:     skills.ActivationAuto,
		},
		Prompt: skills.SkillPrompt{Instructions: "STEP: call http_request."},
		Tools:  skills.SkillToolConfig{RequiredTools: []string{"http_request"}},
	})
	return lib
}

func newWiringAgent(t *testing.T, lib *skills.Library, llm *skillTurnScriptedLLM) (*Agent, *skills.Orchestrator) {
	t.Helper()
	orch := skills.NewOrchestrator(lib)
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithSkillOrchestrator(orch))
	return ag, orch
}

// TestSkillWiring_LiveLoad_RequiredToolRegisteredSameTurn drives a single
// Chat turn whose scripted LLM loads td-wire mid-turn, then asserts at the
// LLM boundary: the call BEFORE the load must not advertise http_request,
// the call AFTER the load (same turn) must. Registry state alone is not
// asserted as proof — a real provider only calls advertised tools, so the
// menu the model was handed is the fact that matters.
func TestSkillWiring_LiveLoad_RequiredToolRegisteredSameTurn(t *testing.T) {
	lib := newWiringSkillLibrary(t)
	llm := newSkillTurnScriptedLLM(
		respLoadSkill("t1-load", "td-wire"),
		respEndTurn("loaded"),
	)
	ag, orch := newWiringAgent(t, lib, llm)
	require.False(t, ag.tools.IsRegistered("http_request"),
		"precondition: http_request must not be registered before the load")

	_, err := ag.Chat(context.Background(), "sess-wire-live", "wire it up")
	require.NoError(t, err)

	assert.True(t, ag.tools.IsRegistered("http_request"),
		"load event must register the skill's required tool within the same turn")
	assert.Len(t, orch.GetActiveSkills("sess-wire-live"), 1)

	// Boundary assertion: the menu handed to the model.
	toolsPerCall := llm.getToolsPerCall()
	require.GreaterOrEqual(t, len(toolsPerCall), 2,
		"the turn must contain the load call and a follow-up LLM call")
	assert.NotContains(t, toolsPerCall[0], "http_request",
		"call 1 (issuing the load) must not yet advertise the required tool")
	assert.Contains(t, toolsPerCall[1], "http_request",
		"call 2 (same turn, after the load) must advertise the required tool — "+
			"the load event retakes the advertised-tool snapshot")
}

// TestSkillWiring_Restore_ResidentLoadRefiresEffect persists a session
// whose L1 holds a resident load body, then reopens it through a fresh
// Memory + Agent (new process simulation). Replay re-fires the event:
// orchestrator re-activated, required tool registered — with no new
// tool_result and no task emission.
func TestSkillWiring_Restore_ResidentLoadRefiresEffect(t *testing.T) {
	ctx := context.Background()
	sessionID := "sess-wire-restore"
	store := newContextClassSQLiteStore(t)

	// ---- process 1: load the skill, persist the session ----
	lib1 := newWiringSkillLibrary(t)
	llm1 := newSkillTurnScriptedLLM(
		respLoadSkill("t1-load", "td-wire"),
		respEndTurn("loaded"),
	)
	orch1 := skills.NewOrchestrator(lib1)
	cfg1 := DefaultConfig()
	cfg1.PatternConfig = DefaultPatternConfig()
	cfg1.PatternConfig.UseLLMClassifier = false
	ag1 := NewAgent(&mockBackend{}, llm1,
		WithConfig(cfg1),
		WithSkillOrchestrator(orch1),
		WithMemory(NewMemoryWithStore(store)),
	)
	_, err := ag1.Chat(ctx, sessionID, "wire it up")
	require.NoError(t, err)
	s1, ok := ag1.memory.GetSession(sessionID)
	require.True(t, ok)
	require.NoError(t, store.SaveSession(ctx, s1))

	// ---- process 2: fresh Memory + Agent over the same store ----
	lib2 := newWiringSkillLibrary(t)
	llm2 := newSkillTurnScriptedLLM(respEndTurn("hello back"))
	orch2 := skills.NewOrchestrator(lib2)
	cfg2 := DefaultConfig()
	cfg2.PatternConfig = DefaultPatternConfig()
	cfg2.PatternConfig.UseLLMClassifier = false
	ag2 := NewAgent(&mockBackend{}, llm2,
		WithConfig(cfg2),
		WithSkillOrchestrator(orch2),
		WithMemory(NewMemoryWithStore(store)),
	)
	require.False(t, ag2.tools.IsRegistered("http_request"),
		"precondition: fresh process has no wiring")

	_, err = ag2.Chat(ctx, sessionID, "continue")
	require.NoError(t, err)

	// The replayed load body re-fired the event.
	actives := orch2.GetActiveSkills(sessionID)
	require.Len(t, actives, 1, "restore must re-activate the resident skill")
	assert.Equal(t, "td-wire", actives[0].Skill.Name)
	assert.Equal(t, "replay", actives[0].TriggerType)
	assert.True(t, ag2.tools.IsRegistered("http_request"),
		"restore must re-register the resident skill's required tool")

	// The body is in context exactly once — replay re-fired the EFFECT,
	// not the load itself.
	s2, ok := ag2.memory.GetSession(sessionID)
	require.True(t, ok)
	bodies := 0
	for _, m := range s2.Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "STEP: call http_request.") {
			bodies++
		}
	}
	assert.Equal(t, 1, bodies, "replay must not append a second load body")
}

// TestSkillWiring_Restore_FoldedLoadDoesNotRefire folds the skill's body
// out before persisting. After restore the body is not resident, so no
// activation fires — and the catalog re-offers the skill.
func TestSkillWiring_Restore_FoldedLoadDoesNotRefire(t *testing.T) {
	ctx := context.Background()
	sessionID := "sess-wire-folded"
	store := newContextClassSQLiteStore(t)

	// ---- process 1: load, then fold the body out, persist ----
	lib1 := newWiringSkillLibrary(t)
	llm1 := newSkillTurnScriptedLLM(
		respLoadSkill("t1-load", "td-wire"),
		respEndTurn("loaded"),
		respEndTurn("fold done"),
	)
	orch1 := skills.NewOrchestrator(lib1)
	cfg1 := DefaultConfig()
	cfg1.PatternConfig = DefaultPatternConfig()
	cfg1.PatternConfig.UseLLMClassifier = false
	cfg1.MaxContextTokens = 20000
	ag1 := NewAgent(&mockBackend{}, llm1,
		WithConfig(cfg1),
		WithSkillOrchestrator(orch1),
		WithMemory(NewMemoryWithStore(store)),
	)
	_, err := ag1.Chat(ctx, sessionID, "wire it up")
	require.NoError(t, err)

	s1, ok := ag1.memory.GetSession(sessionID)
	require.True(t, ok)
	segMem, ok := s1.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	segMem.SetCompressor(&mockCompressor{
		enabled:    true,
		compressFn: func([]Message) string { return "compressed" },
	})
	require.NoError(t, store.SaveSession(ctx, s1))
	segMem.SetSessionStore(store, sessionID)

	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax()*90/100)
	_, err = ag1.Chat(ctx, sessionID, "trigger fold")
	require.NoError(t, err)
	require.Empty(t, segMem.ActiveSkillNames(),
		"precondition: fold must have reclaimed the load body")

	// ---- process 2 ----
	lib2 := newWiringSkillLibrary(t)
	llm2 := newSkillTurnScriptedLLM(respEndTurn("hello back"))
	orch2 := skills.NewOrchestrator(lib2)
	cfg2 := DefaultConfig()
	cfg2.PatternConfig = DefaultPatternConfig()
	cfg2.PatternConfig.UseLLMClassifier = false
	cfg2.MaxContextTokens = 20000
	ag2 := NewAgent(&mockBackend{}, llm2,
		WithConfig(cfg2),
		WithSkillOrchestrator(orch2),
		WithMemory(NewMemoryWithStore(store)),
	)

	_, err = ag2.Chat(ctx, sessionID, "wire again please")
	require.NoError(t, err)

	assert.Empty(t, orch2.GetActiveSkills(sessionID),
		"a folded-out load is not resident — restore must not re-activate it")

	// The catalog re-offers the skill (discovery matched "wire" this turn
	// and the walk no longer hides it).
	s2, ok := ag2.memory.GetSession(sessionID)
	require.True(t, ok)
	names := extractCatalogNames(s2.RomCatalog)
	assert.Contains(t, names, "td-wire",
		"catalog must re-offer a skill whose body folded out")
}
