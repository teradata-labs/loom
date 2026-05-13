// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

// newTestAgentWithSkills returns a minimal Agent with a skills orchestrator
// wired in for the per-turn enforcement tests. Avoids spinning up an LLM
// or storage backend.
func newTestAgentWithSkills(t *testing.T) *Agent {
	t.Helper()
	a := &Agent{
		id:                "test-agent",
		tools:             shuttle.NewRegistry(),
		skillOrchestrator: skills.NewOrchestrator(skills.NewLibrary()),
	}
	return a
}

func TestEnforceRequiredSkillTools_AutoRegistersBuiltin(t *testing.T) {
	a := newTestAgentWithSkills(t)
	sessionID := "sess-1"

	// Register a skill that requires the "web_search" builtin tool. The
	// agent has not yet registered it.
	skill := &skills.Skill{
		Name: "needs-web",
		Tools: skills.SkillToolConfig{
			RequiredTools: []string{"web_search"},
		},
	}
	a.skillOrchestrator.ActivateSkill(sessionID, skill, "test", "", 1.0)

	require.False(t, a.tools.IsRegistered("web_search"),
		"precondition: web_search must not be registered yet")

	a.enforceRequiredSkillTools(sessionID)

	assert.True(t, a.tools.IsRegistered("web_search"),
		"web_search must be auto-registered from the builtin set")
}

func TestEnforceRequiredSkillTools_UnknownToolLogsAndContinues(t *testing.T) {
	a := newTestAgentWithSkills(t)
	sessionID := "sess-2"

	skill := &skills.Skill{
		Name: "needs-fictional",
		Tools: skills.SkillToolConfig{
			RequiredTools: []string{"this-tool-does-not-exist"},
		},
	}
	a.skillOrchestrator.ActivateSkill(sessionID, skill, "test", "", 1.0)

	// Must not panic; must not register the fictional tool.
	a.enforceRequiredSkillTools(sessionID)
	assert.False(t, a.tools.IsRegistered("this-tool-does-not-exist"))
}

func TestEnforceRequiredSkillTools_SkipAlreadyRegistered(t *testing.T) {
	// If the tool is already registered, enforcement is a no-op for it
	// (no double-registration, no warning).
	a := newTestAgentWithSkills(t)
	sessionID := "sess-3"

	pre := &shuttle.MockTool{MockName: "web_search"}
	a.tools.Register(pre)

	skill := &skills.Skill{
		Name: "duplicate",
		Tools: skills.SkillToolConfig{
			RequiredTools: []string{"web_search"},
		},
	}
	a.skillOrchestrator.ActivateSkill(sessionID, skill, "test", "", 1.0)

	a.enforceRequiredSkillTools(sessionID)

	got, ok := a.tools.Get("web_search")
	require.True(t, ok)
	// The pre-registered tool must still be there (not replaced).
	assert.Same(t, pre, got, "enforcement must not clobber a pre-registered tool")
}

func TestApplySkillExcludedTools_FiltersForActiveSkill(t *testing.T) {
	a := newTestAgentWithSkills(t)
	sessionID := "sess-4"
	session := &Session{ID: sessionID}

	t1 := &shuttle.MockTool{MockName: "keep-me"}
	t2 := &shuttle.MockTool{MockName: "drop-me"}
	t3 := &shuttle.MockTool{MockName: "also-keep"}

	skill := &skills.Skill{
		Name: "filtering",
		Tools: skills.SkillToolConfig{
			ExcludedTools: []string{"drop-me"},
		},
	}
	a.skillOrchestrator.ActivateSkill(sessionID, skill, "test", "", 1.0)

	out := a.applySkillExcludedTools([]shuttle.Tool{t1, t2, t3}, session)

	require.Len(t, out, 2)
	names := []string{out[0].Name(), out[1].Name()}
	assert.Contains(t, names, "keep-me")
	assert.Contains(t, names, "also-keep")
	assert.NotContains(t, names, "drop-me")
}

func TestApplySkillExcludedTools_UnionsAcrossActiveSkills(t *testing.T) {
	// Two active skills each excluding different tools; the filter unions.
	a := newTestAgentWithSkills(t)
	sessionID := "sess-5"
	session := &Session{ID: sessionID}

	skillA := &skills.Skill{
		Name:  "A",
		Tools: skills.SkillToolConfig{ExcludedTools: []string{"x"}},
	}
	skillB := &skills.Skill{
		Name:  "B",
		Tools: skills.SkillToolConfig{ExcludedTools: []string{"y"}},
	}
	a.skillOrchestrator.ActivateSkill(sessionID, skillA, "test", "", 1.0)
	a.skillOrchestrator.ActivateSkill(sessionID, skillB, "test", "", 1.0)

	out := a.applySkillExcludedTools([]shuttle.Tool{
		&shuttle.MockTool{MockName: "x"},
		&shuttle.MockTool{MockName: "y"},
		&shuttle.MockTool{MockName: "z"},
	}, session)
	require.Len(t, out, 1)
	assert.Equal(t, "z", out[0].Name())
}

func TestApplySkillExcludedTools_NoActiveSkills_PassesThrough(t *testing.T) {
	a := newTestAgentWithSkills(t)
	session := &Session{ID: "sess-6"}

	in := []shuttle.Tool{&shuttle.MockTool{MockName: "x"}}
	out := a.applySkillExcludedTools(in, session)
	assert.Equal(t, in, out)
}

func TestApplySkillExcludedTools_NilOrchestrator_NoOp(t *testing.T) {
	a := &Agent{
		id:    "noop",
		tools: shuttle.NewRegistry(),
	}
	in := []shuttle.Tool{&shuttle.MockTool{MockName: "x"}}
	out := a.applySkillExcludedTools(in, &Session{ID: "any"})
	assert.Equal(t, in, out)
}
