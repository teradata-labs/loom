// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// D-6 (Skills v4 management) component acceptance tests, driven directly
// through ManageSkillsTool/ManagePatternsTool.Execute — the real black-box
// entry point the LLM calls as the "manage_skills"/"manage_patterns" tools.
//
// Covers:
//   - manage-skills-patterns-list-load-unload-charter-classed-folder-path
//   - high-risk-load-explicit-gate-not-silent-skip
//   - cap-20-explicit-error-no-implicit-eviction-unload-only-removal-active-set-drives-tools-tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/patterns"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

// newTestPatternOrchestrator builds a patterns.Orchestrator over an
// in-memory library with a single registered "test-pattern", for
// ManagePatternsTool's list/load/unload mirror tests.
func newTestPatternOrchestrator(t *testing.T) *patterns.Orchestrator {
	t.Helper()
	lib := patterns.NewLibrary(nil, "")
	lib.Register(&patterns.Pattern{
		Name:        "test-pattern",
		Title:       "Test Pattern",
		Description: "A test pattern",
		Category:    "analytics",
		Difficulty:  "beginner",
		UseCases:    []string{"testing"},
	})
	return patterns.NewOrchestrator(lib)
}

// newManageSkillsToolForTest builds a ManageSkillsTool over the given
// orchestrator with no task emitter and the given permission checker (nil
// disables the high-risk gate entirely, matching production semantics).
func newManageSkillsToolForTest(orch *skills.Orchestrator, checker *shuttle.PermissionChecker) *ManageSkillsTool {
	return NewManageSkillsTool(orch, nil, nil, DefaultConfig(), nil, "test-agent", checker)
}

func ctxWithSession(id string) context.Context {
	return session.WithSessionID(context.Background(), id)
}

// newEmptySkillLibrary returns a skills.Library with no filesystem or
// embedded source, isolated from whatever this host's real
// $LOOM_SKILLS_DIR/$HOME/.loom/skills happens to contain. skills.Library's
// default-path fallback triggers whenever the resolved search-path list is
// empty (WithSearchPaths() with zero args leaves it empty, it does not
// suppress the default), so tests that want a library seeded only via
// Register must pin LOOM_SKILLS_DIR to an empty temp dir first.
func newEmptySkillLibrary(t *testing.T) *skills.Library {
	t.Helper()
	t.Setenv("LOOM_SKILLS_DIR", t.TempDir())
	return skills.NewLibrary()
}

func resultData(t *testing.T, result *shuttle.Result) map[string]interface{} {
	t.Helper()
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok, "expected result.Data to be a map, got %T", result.Data)
	return data
}

// resultLoadMeta returns the metadata for a manage_skills(load) result. Load
// results carry the skill BODY (markdown string) in Data and operational
// fields (action/skill/source_path/etc.) in Metadata — see executeLoad.
func resultLoadMeta(t *testing.T, result *shuttle.Result) map[string]interface{} {
	t.Helper()
	_, isString := result.Data.(string)
	require.True(t, isString, "expected load result.Data to be the skill body (string), got %T", result.Data)
	require.NotNil(t, result.Metadata, "expected load result.Metadata to carry operational fields")
	return result.Metadata
}

// --- list/load/unload over the real machinery; load carries the folder path ---

func TestManageSkillsTool_Load_ActivatesAndReturnsFolderPath(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	skill := &skills.Skill{
		Name:       "code-review",
		Title:      "Code Review",
		SourcePath: "/skills/code-review.yaml",
	}
	lib.Register(skill)
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)

	ctx := ctxWithSession("sess-load")
	result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "code-review"})
	require.NoError(t, err)
	require.True(t, result.Success, "load must succeed for a plain (non-high-risk) skill under cap")

	meta := resultLoadMeta(t, result)
	assert.Equal(t, "load", meta["action"])
	assert.Equal(t, "activated", meta["status"])
	assert.Equal(t, "code-review", meta["skill"])
	assert.Equal(t, "/skills/code-review.yaml", meta["source_path"],
		"a load result must include the skill's folder path (Seam 4)")

	actives := orch.GetActiveSkills("sess-load")
	require.Len(t, actives, 1, "the load must actually activate the skill over the real orchestrator")
	assert.Equal(t, "code-review", actives[0].Skill.Name)
}

func TestManageSkillsTool_Load_ResultClassifiesNarrative(t *testing.T) {
	// v5 correction: manage_skills/manage_patterns load results tag narrative
	// so fold's LLM compressor summarizes skill bodies into residue under
	// pressure. The pre-fix charter class pinned skill bodies forever.
	assert.Equal(t, ClassNarrative, toolResultClass("manage_skills", nil))
	assert.Equal(t, ClassNarrative, toolResultClass("manage_patterns", nil))
}

// writeSkillYAMLFile writes a minimal valid skill YAML file to dir/name.yaml.
// skills.Library.ListAll only surfaces skills discovered by walking search
// paths/embedded FS (Register-only skills stay invisible to it — Register
// exists purely for Load/Get direct lookup), so exercising manage_skills's
// "list" action black-box requires real on-disk skill files.
func writeSkillYAMLFile(t *testing.T, dir, name, title string) {
	t.Helper()
	content := "apiVersion: loom/v1\nkind: Skill\nmetadata:\n" +
		"  name: " + name + "\n" +
		"  title: " + title + "\n" +
		"  domain: general\n" +
		"prompt:\n" +
		"  instructions: Do the thing for " + name + ".\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0o644))
}

func TestManageSkillsTool_List_ReportsActiveState(t *testing.T) {
	dir := t.TempDir()
	writeSkillYAMLFile(t, dir, "alpha", "Alpha")
	writeSkillYAMLFile(t, dir, "beta", "Beta")
	lib := skills.NewLibrary(skills.WithSearchPaths(dir))
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)

	sessionID := "sess-list"
	ctx := ctxWithSession(sessionID)

	_, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "alpha"})
	require.NoError(t, err)

	result, err := tool.Execute(ctx, map[string]interface{}{"action": "list"})
	require.NoError(t, err)
	require.True(t, result.Success)

	data := resultData(t, result)
	assert.EqualValues(t, 2, data["count"])
	assert.EqualValues(t, 1, data["active_count"])

	items, ok := data["skills"].([]map[string]interface{})
	require.True(t, ok)
	seen := map[string]bool{}
	for _, item := range items {
		name, _ := item["name"].(string)
		active, _ := item["active"].(bool)
		seen[name] = active
	}
	assert.True(t, seen["alpha"], "alpha was loaded and must report active=true")
	assert.False(t, seen["beta"], "beta was never loaded and must report active=false")
}

func TestManageSkillsTool_Unload_RemovesFromActiveSet(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{Name: "temp-skill", Title: "Temp"})
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)

	sessionID := "sess-unload"
	ctx := ctxWithSession(sessionID)

	_, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "temp-skill"})
	require.NoError(t, err)
	require.Len(t, orch.GetActiveSkills(sessionID), 1)

	result, err := tool.Execute(ctx, map[string]interface{}{"action": "unload", "name": "temp-skill"})
	require.NoError(t, err)
	require.True(t, result.Success)

	data := resultData(t, result)
	assert.Equal(t, "unload", data["action"])
	assert.Equal(t, "deactivated", data["status"])
	assert.Equal(t, true, data["was_active"])
	assert.EqualValues(t, 0, data["active_count"])

	assert.Empty(t, orch.GetActiveSkills(sessionID), "unload must be the only thing that removes an active skill")
}

func TestManageSkillsTool_Unload_NotActive_IsIdempotentSuccess(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)

	result, err := tool.Execute(ctxWithSession("sess-idempotent"), map[string]interface{}{
		"action": "unload", "name": "never-loaded",
	})
	require.NoError(t, err)
	require.True(t, result.Success, "unloading a skill that was never active is a no-op success, not an error")
	assert.Equal(t, false, resultData(t, result)["was_active"])
}

func TestManageSkillsTool_Load_UnknownSkill_ReturnsNotFoundError(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)

	result, err := tool.Execute(ctxWithSession("sess-missing"), map[string]interface{}{
		"action": "load", "name": "does-not-exist",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Equal(t, "SKILL_NOT_FOUND", result.Error.Code)
	assert.Empty(t, orch.GetActiveSkills("sess-missing"))
}

// --- high-risk gate: explicit result, not a silent skip ---

func TestManageSkillsTool_Load_HighRisk_ReturnsExplicitGateResult(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{Name: "prod-migration", Title: "Prod Migration", RiskLevel: "HIGH"})
	orch := skills.NewOrchestrator(lib)
	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{YOLO: false})
	tool := newManageSkillsToolForTest(orch, checker)

	sessionID := "sess-highrisk"
	result, err := tool.Execute(ctxWithSession(sessionID), map[string]interface{}{
		"action": "load", "name": "prod-migration",
	})
	require.NoError(t, err, "the gate is a surfaced result, never a Go error")
	require.False(t, result.Success)
	require.NotNil(t, result.Error, "a high-risk load must return an explicit gate result, not a silent skip")
	assert.Equal(t, "HIGH_RISK_APPROVAL_REQUIRED", result.Error.Code)
	assert.Contains(t, result.Error.Message, "prod-migration")
	assert.Contains(t, result.Error.Message, "HIGH")
	assert.NotEmpty(t, result.Error.Suggestion)

	assert.Empty(t, orch.GetActiveSkills(sessionID),
		"a high-risk skill must not be activated until it is explicitly approved")
}

func TestManageSkillsTool_Load_RestrictedRisk_ReturnsExplicitGateResult(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{Name: "restricted-skill", RiskLevel: "RESTRICTED"})
	orch := skills.NewOrchestrator(lib)
	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{YOLO: false})
	tool := newManageSkillsToolForTest(orch, checker)

	result, err := tool.Execute(ctxWithSession("sess-restricted"), map[string]interface{}{
		"action": "load", "name": "restricted-skill",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Equal(t, "HIGH_RISK_APPROVAL_REQUIRED", result.Error.Code)
}

func TestManageSkillsTool_Load_HighRisk_NoPermissionChecker_ActivatesAnyway(t *testing.T) {
	// With no permission checker wired at all (e.g. a backend that never
	// configured one), the gate is skipped entirely rather than blocking
	// every high-risk skill unconditionally — matches production semantics
	// at manage_skills_tool.go's executeLoad.
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{Name: "risky", RiskLevel: "HIGH"})
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)

	result, err := tool.Execute(ctxWithSession("sess-nochecker"), map[string]interface{}{
		"action": "load", "name": "risky",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Len(t, orch.GetActiveSkills("sess-nochecker"), 1)
}

func TestManageSkillsTool_Load_HighRisk_YOLOMode_ActivatesAnyway(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{Name: "risky-yolo", RiskLevel: "RESTRICTED"})
	orch := skills.NewOrchestrator(lib)
	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{YOLO: true})
	tool := newManageSkillsToolForTest(orch, checker)

	result, err := tool.Execute(ctxWithSession("sess-yolo"), map[string]interface{}{
		"action": "load", "name": "risky-yolo",
	})
	require.NoError(t, err)
	require.True(t, result.Success, "YOLO mode must still bypass the high-risk gate")
	assert.Len(t, orch.GetActiveSkills("sess-yolo"), 1)
}

func TestManageSkillsTool_Load_LowRisk_NeverGated(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	for _, rl := range []string{"", "LOW", "MEDIUM"} {
		lib.Register(&skills.Skill{Name: "safe-" + rl, RiskLevel: rl})
	}
	orch := skills.NewOrchestrator(lib)
	checker := shuttle.NewPermissionChecker(shuttle.PermissionConfig{YOLO: false})
	tool := newManageSkillsToolForTest(orch, checker)

	for _, rl := range []string{"", "LOW", "MEDIUM"} {
		name := "safe-" + rl
		result, err := tool.Execute(ctxWithSession("sess-lowrisk"), map[string]interface{}{
			"action": "load", "name": name,
		})
		require.NoError(t, err)
		require.True(t, result.Success, "risk level %q must never trigger the high-risk gate", rl)
	}
}

// --- cap (default 20): explicit error, no implicit eviction, unload-only removal ---

func TestManageSkillsTool_Load_PastCap_ReturnsExplicitErrorNoEviction(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	names := make([]string, skillActiveSafetyCap+1)
	for i := range names {
		names[i] = fmt.Sprintf("skill-%02d", i)
		lib.Register(&skills.Skill{Name: names[i]})
	}
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)
	sessionID := "sess-cap"
	ctx := ctxWithSession(sessionID)

	// Load exactly up to the cap: all must succeed.
	for i := 0; i < skillActiveSafetyCap; i++ {
		result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": names[i]})
		require.NoError(t, err)
		require.True(t, result.Success, "load %d (of cap %d) must succeed", i+1, skillActiveSafetyCap)
	}
	require.Len(t, orch.GetActiveSkills(sessionID), skillActiveSafetyCap)

	// The (cap+1)th distinct skill must be rejected explicitly.
	result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": names[skillActiveSafetyCap]})
	require.NoError(t, err, "the cap is a surfaced result, never a Go error")
	require.False(t, result.Success)
	require.NotNil(t, result.Error)
	assert.Equal(t, "ACTIVE_SKILL_CAP_EXCEEDED", result.Error.Code)
	assert.Contains(t, result.Error.Message, "no skill was evicted")
	assert.NotEmpty(t, result.Error.Suggestion)

	// No implicit eviction: the active set is unchanged at exactly the cap,
	// and every originally-loaded skill is still present.
	actives := orch.GetActiveSkills(sessionID)
	require.Len(t, actives, skillActiveSafetyCap,
		"a rejected load must not silently evict an existing active skill")
	activeNames := make(map[string]bool, len(actives))
	for _, a := range actives {
		activeNames[a.Skill.Name] = true
	}
	for i := 0; i < skillActiveSafetyCap; i++ {
		assert.True(t, activeNames[names[i]], "skill %s must still be active (no implicit eviction)", names[i])
	}
	assert.False(t, activeNames[names[skillActiveSafetyCap]], "the rejected skill must not have been activated")
}

func TestManageSkillsTool_Load_PastCap_UnloadFreesCapacity(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	names := make([]string, skillActiveSafetyCap+1)
	for i := range names {
		names[i] = fmt.Sprintf("cap-skill-%02d", i)
		lib.Register(&skills.Skill{Name: names[i]})
	}
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)
	sessionID := "sess-cap-unload"
	ctx := ctxWithSession(sessionID)

	for i := 0; i < skillActiveSafetyCap; i++ {
		_, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": names[i]})
		require.NoError(t, err)
	}

	// Free capacity via explicit unload — the only sanctioned removal path.
	_, err := tool.Execute(ctx, map[string]interface{}{"action": "unload", "name": names[0]})
	require.NoError(t, err)
	require.Len(t, orch.GetActiveSkills(sessionID), skillActiveSafetyCap-1)

	// The load that previously failed must now succeed.
	result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": names[skillActiveSafetyCap]})
	require.NoError(t, err)
	require.True(t, result.Success, "load must succeed once explicit unload frees capacity")
	require.Len(t, orch.GetActiveSkills(sessionID), skillActiveSafetyCap)
}

func TestManageSkillsTool_Load_AtCap_ReloadingAlreadyActiveSkill_DoesNotCountAgainstCap(t *testing.T) {
	// Re-loading an already-active skill is a replace, not a new activation,
	// so it must never be rejected by the cap check even when the session
	// is already exactly at the cap.
	lib := newEmptySkillLibrary(t)
	names := make([]string, skillActiveSafetyCap)
	for i := range names {
		names[i] = fmt.Sprintf("recap-skill-%02d", i)
		lib.Register(&skills.Skill{Name: names[i]})
	}
	orch := skills.NewOrchestrator(lib)
	tool := newManageSkillsToolForTest(orch, nil)
	sessionID := "sess-recap"
	ctx := ctxWithSession(sessionID)

	for _, name := range names {
		_, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": name})
		require.NoError(t, err)
	}
	require.Len(t, orch.GetActiveSkills(sessionID), skillActiveSafetyCap)

	result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": names[0]})
	require.NoError(t, err)
	require.True(t, result.Success, "re-loading an already-active skill must succeed even exactly at cap")
	assert.Equal(t, true, resultLoadMeta(t, result)["already_active"])
	assert.Len(t, orch.GetActiveSkills(sessionID), skillActiveSafetyCap, "re-load must not grow the active set")
}

// --- active set (populated via manage_skills(load)) still drives required/excluded tools ---

func TestManageSkillsTool_Load_ActiveSetDrivesRequiredAndExcludedTools(t *testing.T) {
	lib := newEmptySkillLibrary(t)
	lib.Register(&skills.Skill{
		Name: "tool-shaping-skill",
		Tools: skills.SkillToolConfig{
			RequiredTools: []string{"web_search"},
			ExcludedTools: []string{"drop-me"},
		},
	})
	orch := skills.NewOrchestrator(lib)

	a := &Agent{
		id:                "test-agent",
		tools:             shuttle.NewRegistry(),
		skillOrchestrator: orch,
	}
	tool := NewManageSkillsTool(orch, nil, nil, DefaultConfig(), nil, a.id, nil)
	a.tools.Register(shuttle.Tool(tool))

	sessionID := "sess-shaping"
	ctx := ctxWithSession(sessionID)

	require.False(t, a.tools.IsRegistered("web_search"), "precondition: web_search not yet registered")

	result, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "tool-shaping-skill"})
	require.NoError(t, err)
	require.True(t, result.Success)

	// The active set populated by manage_skills(load) — not a hand-built
	// ActivateSkill call — must be what these enforcement functions read.
	a.enforceRequiredSkillTools(sessionID)
	assert.True(t, a.tools.IsRegistered("web_search"),
		"required_tools of a skill activated via manage_skills(load) must be auto-registered")

	session := &Session{ID: sessionID}
	filtered := a.applySkillExcludedTools([]shuttle.Tool{
		&shuttle.MockTool{MockName: "keep-me"},
		&shuttle.MockTool{MockName: "drop-me"},
	}, session)
	require.Len(t, filtered, 1)
	assert.Equal(t, "keep-me", filtered[0].Name())
}

// --- manage_patterns mirrors the same list/load/unload shape (no risk/cap semantics) ---

func TestManagePatternsTool_ListLoadUnload(t *testing.T) {
	orch := newTestPatternOrchestrator(t)
	tool := NewManagePatternsTool(orch)
	sessionID := "sess-pattern"
	ctx := ctxWithSession(sessionID)

	loadResult, err := tool.Execute(ctx, map[string]interface{}{"action": "load", "name": "test-pattern"})
	require.NoError(t, err)
	require.True(t, loadResult.Success)
	loadData := resultData(t, loadResult)
	assert.Equal(t, "loaded", loadData["status"])
	assert.Equal(t, "test-pattern", loadData["pattern"])

	listResult, err := tool.Execute(ctx, map[string]interface{}{"action": "list"})
	require.NoError(t, err)
	listData := resultData(t, listResult)
	assert.EqualValues(t, 1, listData["loaded_count"])

	unloadResult, err := tool.Execute(ctx, map[string]interface{}{"action": "unload", "name": "test-pattern"})
	require.NoError(t, err)
	unloadData := resultData(t, unloadResult)
	assert.Equal(t, "unloaded", unloadData["status"])
	assert.Equal(t, true, unloadData["was_loaded"])
}

func TestManagePatternsTool_LoadResultClassifiesNarrative(t *testing.T) {
	assert.Equal(t, ClassNarrative, toolResultClass("manage_patterns", nil))
}
