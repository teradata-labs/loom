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

	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/skills"
)

// Trigger mode MANUAL means the user activates the skill and the model does
// not. These tests drive both halves of that through the real Chat loop: the
// model's route in (the menu, manage_skills list/load) must not reach a MANUAL
// skill, and the user's route in (a leading slash command) must — leaving the
// same conversation rows a model-issued load leaves, because restore replay
// reads those rows to rebuild a reloaded session's active skills.

const (
	manualBodySentinel = "Manual skill instructions body"
	hybridBodySentinel = "Hybrid skill instructions body"
	manualSlashCommand = "/manual-skill"
)

// manualModeFixtures pairs one MANUAL skill (with a slash command, since that
// is its only way in) against one HYBRID skill that stays model-loadable — the
// control that separates "MANUAL is withheld" from "skills are broken".
var manualModeFixtures = map[string]string{
	"manual-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: manual-skill
  title: Manual Skill
  description: Only the user invokes this one.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
  slash_commands:
    - ` + manualSlashCommand + `
prompt:
  instructions: |
    ` + manualBodySentinel + `.
`,
	"hybrid-skill.yaml": `apiVersion: loom/v1
kind: Skill
metadata:
  name: hybrid-skill
  title: Hybrid Skill
  description: The model may pick this one.
  domain: general
  risk_level: LOW
trigger:
  mode: HYBRID
  slash_commands:
    - /hybrid-skill
prompt:
  instructions: |
    ` + hybridBodySentinel + `.
`,
}

type manualModeRig struct {
	agent *Agent
	orch  *skills.Orchestrator
	lib   *skills.Library
}

// buildManualModeRig wires the fixture library with skills ENABLED, so the ROM
// menu renders and a user's slash command is honoured — the configuration a
// deployed agent runs.
func buildManualModeRig(t *testing.T, llm LLMProvider, dump bool) *manualModeRig {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range manualModeFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}

	lib := skills.NewLibrary(skills.WithSearchPaths(dir))
	orch := skills.NewOrchestrator(lib)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.PatternConfig.Enabled = false
	cfg.SkillsConfig = &skills.SkillsConfig{Enabled: true}
	cfg.Debug.ContextDump = dump

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithSkillOrchestrator(orch))
	// Keep tool results and sidecars inline so bodies are visible in the dump.
	ag.SetSharedMemoryThreshold(1 << 20)
	ag.SetSharedMemory(ag.sharedMemory)

	return &manualModeRig{agent: ag, orch: orch, lib: lib}
}

func activeSkillNamesFor(rig *manualModeRig, sessionID string) []string {
	var names []string
	for _, as := range rig.orch.GetActiveSkills(sessionID) {
		if as != nil && as.Skill != nil {
			names = append(names, as.Skill.Name)
		}
	}
	return names
}

// --- the model's route in ----------------------------------------------------

// TestManualMode_MenuOmitsManualSkill asserts the prompt's skill menu names the
// HYBRID skill and withholds the MANUAL one: what the model cannot load, it is
// not offered.
func TestManualMode_MenuOmitsManualSkill(t *testing.T) {
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, false)

	menu := rig.agent.skillMenuPromptSupplement()

	assert.Contains(t, menu, "hybrid-skill", "a model-loadable skill is on the menu")
	assert.NotContains(t, menu, "manual-skill", "a MANUAL skill is not on the menu")
}

// TestManualMode_ModelLoadRefusedAndSkillStaysInactive asserts manage_skills(load)
// refuses a MANUAL skill, names the slash command that does work, and leaves the
// session's active set untouched.
func TestManualMode_ModelLoadRefusedAndSkillStaysInactive(t *testing.T) {
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, false)
	const sessionID = "sess-manual-refused"

	tool := findRegisteredTool(rig.agent, "manage_skills")
	require.NotNil(t, tool)
	res, err := tool.Execute(session.WithSessionID(context.Background(), sessionID),
		map[string]interface{}{"action": "load", "name": "manual-skill"})
	require.NoError(t, err)
	require.NotNil(t, res)

	require.False(t, res.Success, "a model-issued load of a MANUAL skill is refused")
	require.NotNil(t, res.Error)
	assert.Equal(t, "manual_skill", res.Error.Code)
	assert.Contains(t, res.Error.Message, manualSlashCommand,
		"the refusal names the command that does activate it")
	assert.Empty(t, activeSkillNamesFor(rig, sessionID), "the refused load activates nothing")
}

// TestManualMode_ModelLoadOfHybridStillWorks is the control: the gate is about
// the mode, not about loads in general.
func TestManualMode_ModelLoadOfHybridStillWorks(t *testing.T) {
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, false)
	const sessionID = "sess-hybrid-loads"

	tool := findRegisteredTool(rig.agent, "manage_skills")
	require.NotNil(t, tool)
	res, err := tool.Execute(session.WithSessionID(context.Background(), sessionID),
		map[string]interface{}{"action": "load", "name": "hybrid-skill"})
	require.NoError(t, err)

	require.True(t, res.Success, "a HYBRID skill still loads on the model's own call")
	assert.Equal(t, []string{"hybrid-skill"}, activeSkillNamesFor(rig, sessionID))
}

// TestManualMode_ListOmitsManualSkill asserts the library listing the model can
// call matches the menu: no MANUAL skill in it.
func TestManualMode_ListOmitsManualSkill(t *testing.T) {
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, false)

	tool := findRegisteredTool(rig.agent, "manage_skills")
	require.NotNil(t, tool)
	res, err := tool.Execute(session.WithSessionID(context.Background(), "sess-manual-list"),
		map[string]interface{}{"action": "list"})
	require.NoError(t, err)
	require.True(t, res.Success)

	rendered, ok := res.Data.(string)
	require.True(t, ok)
	assert.Contains(t, rendered, "hybrid-skill")
	assert.NotContains(t, rendered, "manual-skill")
}

// TestManualMode_ListSeesRegisteredSkills asserts the listing reports the
// session's real library. An embedder that injects skills with Register() — the
// cloud does, for every database-backed and marketplace skill, and for an
// admin's draft under test — was invisible to the old index-backed listing, so
// the model was told it had a different set of skills than it actually ran.
func TestManualMode_ListSeesRegisteredSkills(t *testing.T) {
	registered := &skills.Skill{
		Name:        "registered-skill",
		Title:       "Registered Skill",
		Description: "Injected the way an embedder injects database-backed skills.",
		Domain:      "general",
		Trigger:     skills.SkillTrigger{Mode: skills.ActivationHybrid},
	}
	listSkills := func(t *testing.T, rig *manualModeRig, sessionID string) string {
		t.Helper()
		tool := findRegisteredTool(rig.agent, "manage_skills")
		require.NotNil(t, tool)
		res, err := tool.Execute(session.WithSessionID(context.Background(), sessionID),
			map[string]interface{}{"action": "list"})
		require.NoError(t, err)
		require.True(t, res.Success)
		rendered, ok := res.Data.(string)
		require.True(t, ok)
		return rendered
	}

	// The cloud's shape: a session library whose skills all arrive by Register()
	// from the database. The index-backed listing reported none of them.
	t.Run("a registered skill is listed", func(t *testing.T) {
		rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, false)
		rig.lib.Register(registered)

		assert.Contains(t, listSkills(t, rig, "sess-registered-only"), "registered-skill")
	})

	// Mixed shape: on-disk skills already indexed, then an injection on top.
	// Both belong to the session, so both are listed.
	t.Run("registered and on-disk skills are listed together", func(t *testing.T) {
		rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, false)
		require.Contains(t, listSkills(t, rig, "sess-mixed"), "hybrid-skill")

		rig.lib.Register(registered)

		rendered := listSkills(t, rig, "sess-mixed")
		assert.Contains(t, rendered, "registered-skill")
		assert.Contains(t, rendered, "hybrid-skill")
	})
}

// --- the user's route in -----------------------------------------------------

// TestManualMode_SlashCommandLoadsManualSkill asserts the user's slash command
// activates the MANUAL skill and puts its instructions in the turn that asked
// for them — before the model answers, not after.
func TestManualMode_SlashCommandLoadsManualSkill(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, true)
	const sessionID = "sess-manual-slash"

	_, err := rig.agent.Chat(context.Background(), sessionID, manualSlashCommand+" profile the table")
	require.NoError(t, err)

	assert.Equal(t, []string{"manual-skill"}, activeSkillNamesFor(rig, sessionID),
		"the slash command activated the skill the model may not load")

	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs, "the turn reached the provider")
	first := recs[0]

	bodies := 0
	for _, m := range first.Messages {
		if strings.Contains(m.Content, manualBodySentinel) {
			assert.Equal(t, "user", m.Role, "the body rides the user-instruction slot")
			bodies++
		}
	}
	assert.Equal(t, 1, bodies, "the model saw the skill body exactly once, on this turn")
}

// TestManualMode_SlashLoadLeavesTheRestoreMarker asserts the rows a slash load
// writes are the pair restore replay keys on: an assistant manage_skills(load)
// call and its "Skill loaded: <name>" tool result. Without them a reloaded
// session would drop the skill while its required tools stayed advertised.
func TestManualMode_SlashLoadLeavesTheRestoreMarker(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, true)

	_, err := rig.agent.Chat(context.Background(), "sess-manual-marker", manualSlashCommand)
	require.NoError(t, err)

	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs)

	var callID string
	for _, m := range recs[0].Messages {
		for _, tc := range m.ToolCalls {
			if tc.Name == "manage_skills" && tc.Input["action"] == "load" && tc.Input["name"] == "manual-skill" {
				callID = tc.ID
			}
		}
	}
	require.NotEmpty(t, callID, "the load is recorded as a manage_skills tool call")

	paired := false
	for _, m := range recs[0].Messages {
		if m.Role == "tool" && m.ToolUseID == callID {
			assert.True(t, strings.HasPrefix(m.Content, "Skill loaded: "),
				"the tool row carries the confirmation restore replay matches on")
			paired = true
		}
	}
	assert.True(t, paired, "the tool result is paired to the call by tool_use_id")
}

// TestManualMode_ModelMayReloadAfterUserActivated asserts the gate withholds a
// skill, not a session: once the user has activated a MANUAL skill, the model
// re-reading it is not a way around the rule.
func TestManualMode_ModelMayReloadAfterUserActivated(t *testing.T) {
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, false)
	const sessionID = "sess-manual-reload"

	_, err := rig.agent.Chat(context.Background(), sessionID, manualSlashCommand)
	require.NoError(t, err)
	require.Equal(t, []string{"manual-skill"}, activeSkillNamesFor(rig, sessionID))

	tool := findRegisteredTool(rig.agent, "manage_skills")
	require.NotNil(t, tool)
	res, err := tool.Execute(session.WithSessionID(context.Background(), sessionID),
		map[string]interface{}{"action": "load", "name": "manual-skill"})
	require.NoError(t, err)
	assert.True(t, res.Success, "an already-active MANUAL skill loads again for the model")
}

// TestManualMode_UnknownSlashCommandLoadsNothing asserts a message that merely
// starts with a slash is ordinary user text: no skill, no synthetic tool rows.
func TestManualMode_UnknownSlashCommandLoadsNothing(t *testing.T) {
	sinkDir := redirectCtxDumpSink(t)
	rig := buildManualModeRig(t, &mockToolCallingLLM{responses: []mockLLMResponse{finalTurn()}}, true)
	const sessionID = "sess-unknown-slash"

	_, err := rig.agent.Chat(context.Background(), sessionID, "/not-a-skill do something")
	require.NoError(t, err)

	assert.Empty(t, activeSkillNamesFor(rig, sessionID), "no skill was activated")

	recs := readDumpRecords(t, sinkDir)
	require.NotEmpty(t, recs)
	for _, m := range recs[0].Messages {
		for _, tc := range m.ToolCalls {
			assert.NotEqual(t, "manage_skills", tc.Name, "no skill load was synthesized")
		}
	}
}
