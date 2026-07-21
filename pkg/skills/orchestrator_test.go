//go:build fts5

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

package skills

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newTestOrchestrator creates an orchestrator with an in-memory library for testing.
func newTestOrchestrator(skills ...*Skill) *Orchestrator {
	lib := NewLibrary(WithSearchPaths()) // empty paths to avoid defaults
	for _, s := range skills {
		lib.Register(s)
	}
	return NewOrchestrator(lib)
}

func TestOrchestrator_MatchSkills_SlashCommand(t *testing.T) {
	skill := &Skill{
		Name:   "code-review",
		Title:  "Code Review",
		Domain: "code",
		Trigger: SkillTrigger{
			SlashCommands: []string{"/review", "/cr"},
			Mode:          ActivationManual,
		},
		Prompt: SkillPrompt{Instructions: "Review code."},
	}

	orch := newTestOrchestrator(skill)
	config := DefaultSkillsConfig()

	tests := []struct {
		name      string
		msg       string
		wantMatch bool
		wantCmd   string
		wantTrigV string
	}{
		{
			name:      "exact slash command",
			msg:       "/review this code please",
			wantMatch: true,
			wantCmd:   "slash_command",
			wantTrigV: "/review this code please",
		},
		{
			name:      "alternate slash command",
			msg:       "/cr check my PR",
			wantMatch: true,
			wantCmd:   "slash_command",
			wantTrigV: "/cr check my PR",
		},
		{
			name:      "no slash command",
			msg:       "please review this code",
			wantMatch: false,
		},
		{
			name:      "newline-separated slash command",
			msg:       "/review\nsome code here",
			wantMatch: true,
			wantCmd:   "slash_command",
			wantTrigV: "/review some code here",
		},
		{
			name:      "slash command with no rest (no trailing space)",
			msg:       "/review",
			wantMatch: true,
			wantCmd:   "slash_command",
			wantTrigV: "/review",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results, err := orch.MatchSkills("sess-1", tc.msg, config)
			require.NoError(t, err)

			if tc.wantMatch {
				require.NotEmpty(t, results)
				assert.Equal(t, "code-review", results[0].Skill.Name)
				assert.Equal(t, tc.wantCmd, results[0].TriggerType)
				assert.Equal(t, 1.0, results[0].Confidence)
				assert.Equal(t, tc.wantTrigV, results[0].TriggerValue)
			} else {
				// MANUAL mode skill should not match without slash command.
				for _, r := range results {
					assert.NotEqual(t, "code-review", r.Skill.Name)
				}
			}
		})
	}
}

func TestOrchestrator_MatchSkills_Keywords(t *testing.T) {
	skill := &Skill{
		Name:   "sql-helper",
		Title:  "SQL Helper",
		Domain: "sql",
		Trigger: SkillTrigger{
			Keywords: []string{"sql", "query", "database"},
			Mode:     ActivationAuto,
		},
		Prompt: SkillPrompt{Instructions: "Help with SQL."},
	}

	orch := newTestOrchestrator(skill)
	config := DefaultSkillsConfig()

	tests := []struct {
		name      string
		msg       string
		wantMatch bool
	}{
		{
			name:      "matches multiple keywords",
			msg:       "help me write a sql query for the database",
			wantMatch: true,
		},
		{
			name:      "matches all keywords",
			msg:       "help me write a sql query for the database tables",
			wantMatch: true,
		},
		{
			name:      "no keyword match",
			msg:       "deploy my application to kubernetes",
			wantMatch: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results, err := orch.MatchSkills("sess-2", tc.msg, config)
			require.NoError(t, err)

			if tc.wantMatch {
				require.NotEmpty(t, results)
				assert.Equal(t, "sql-helper", results[0].Skill.Name)
				assert.Equal(t, "keyword", results[0].TriggerType)
			} else {
				assert.Empty(t, results)
			}
		})
	}
}

func TestOrchestrator_MatchSkills_AlwaysMode(t *testing.T) {
	alwaysSkill := &Skill{
		Name:   "logging",
		Title:  "Logging",
		Domain: "ops",
		Trigger: SkillTrigger{
			Mode: ActivationAlways,
		},
		Prompt: SkillPrompt{Instructions: "Always log."},
	}

	orch := newTestOrchestrator(alwaysSkill)
	config := DefaultSkillsConfig()

	// ALWAYS skills should match regardless of message content.
	results, err := orch.MatchSkills("sess-3", "anything at all", config)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "logging", results[0].Skill.Name)
	assert.Equal(t, "always", results[0].TriggerType)
	assert.Equal(t, 1.0, results[0].Confidence)
}

func TestOrchestrator_MatchSkills_Disabled(t *testing.T) {
	skill := &Skill{
		Name:   "disabled-skill",
		Title:  "Disabled Skill",
		Domain: "code",
		Trigger: SkillTrigger{
			SlashCommands: []string{"/disabled"},
			Mode:          ActivationManual,
		},
		Prompt: SkillPrompt{Instructions: "Should not match."},
	}

	orch := newTestOrchestrator(skill)
	config := DefaultSkillsConfig()
	config.DisabledSkills = []string{"disabled-skill"}

	results, err := orch.MatchSkills("sess-4", "/disabled test", config)
	require.NoError(t, err)
	assert.Empty(t, results, "disabled skills should not match")
}

func TestOrchestrator_MatchSkills_EnabledFilter(t *testing.T) {
	skillA := &Skill{
		Name:   "skill-a",
		Title:  "Skill A",
		Domain: "code",
		Trigger: SkillTrigger{
			SlashCommands: []string{"/a"},
			Mode:          ActivationManual,
		},
		Prompt: SkillPrompt{Instructions: "A."},
	}
	skillB := &Skill{
		Name:   "skill-b",
		Title:  "Skill B",
		Domain: "code",
		Trigger: SkillTrigger{
			SlashCommands: []string{"/b"},
			Mode:          ActivationManual,
		},
		Prompt: SkillPrompt{Instructions: "B."},
	}

	orch := newTestOrchestrator(skillA, skillB)

	// Only enable skill-a.
	config := DefaultSkillsConfig()
	config.EnabledSkills = []string{"skill-a"}

	// skill-a should match.
	results, err := orch.MatchSkills("sess-5", "/a test", config)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "skill-a", results[0].Skill.Name)

	// skill-b should not match even with correct command.
	results, err = orch.MatchSkills("sess-5", "/b test", config)
	require.NoError(t, err)
	assert.Empty(t, results, "non-enabled skill should not match")
}

func TestOrchestrator_MatchSkills_DisabledConfig(t *testing.T) {
	skill := &Skill{
		Name:   "any-skill",
		Title:  "Any",
		Domain: "code",
		Trigger: SkillTrigger{
			Mode: ActivationAlways,
		},
		Prompt: SkillPrompt{Instructions: "Always."},
	}

	orch := newTestOrchestrator(skill)
	config := DefaultSkillsConfig()
	config.Enabled = false

	results, err := orch.MatchSkills("sess-x", "anything", config)
	require.NoError(t, err)
	assert.Nil(t, results, "disabled config should return nil")
}

func TestOrchestrator_MatchSkills_NilConfig(t *testing.T) {
	skill := &Skill{
		Name:   "always-skill",
		Title:  "Always",
		Domain: "general",
		Trigger: SkillTrigger{
			Mode: ActivationAlways,
		},
		Prompt: SkillPrompt{Instructions: "Always on."},
	}

	orch := newTestOrchestrator(skill)

	// Nil config should use defaults (enabled=true).
	results, err := orch.MatchSkills("sess-nil", "hello", nil)
	require.NoError(t, err)
	require.NotEmpty(t, results)
}

func TestOrchestrator_ActivateDeactivate(t *testing.T) {
	skill := &Skill{
		Name:   "test-skill",
		Title:  "Test",
		Domain: "code",
		Prompt: SkillPrompt{Instructions: "Test."},
	}

	orch := newTestOrchestrator(skill)
	sessionID := "sess-ad"

	// Activate.
	active := orch.ActivateSkill(sessionID, skill, "slash_command", "/test", 1.0)
	require.NotNil(t, active)
	assert.Equal(t, "test-skill", active.Skill.Name)
	assert.Equal(t, sessionID, active.SessionID)
	assert.Equal(t, "slash_command", active.TriggerType)

	// Verify active.
	actives := orch.GetActiveSkills(sessionID)
	require.Len(t, actives, 1)
	assert.Equal(t, "test-skill", actives[0].Skill.Name)

	// Deactivate.
	orch.DeactivateSkill(sessionID, "test-skill")

	actives = orch.GetActiveSkills(sessionID)
	assert.Empty(t, actives)
}

func TestOrchestrator_ActivateSkill_Replaces(t *testing.T) {
	skill := &Skill{
		Name:   "replaceable",
		Title:  "Replaceable",
		Domain: "code",
		Prompt: SkillPrompt{Instructions: "Test."},
	}

	orch := newTestOrchestrator(skill)
	sessionID := "sess-replace"

	// Activate once.
	orch.ActivateSkill(sessionID, skill, "slash_command", "/r", 0.8)

	// Activate same skill again -- should replace, not duplicate.
	orch.ActivateSkill(sessionID, skill, "keyword", "replace", 0.9)

	actives := orch.GetActiveSkills(sessionID)
	require.Len(t, actives, 1)
	assert.Equal(t, "keyword", actives[0].TriggerType)
	assert.InDelta(t, 0.9, actives[0].Confidence, 0.001)
}

func TestActivateSkill_NoImplicitEviction_PastLegacyDefault(t *testing.T) {
	// There is no implicit eviction (O-SKL-3): activating more skills than
	// the legacy default MaxConcurrentSkills (3, still used to bound
	// MatchSkills' candidate count) must never evict an existing active
	// skill. The active set only shrinks via explicit DeactivateSkill.
	skills := make([]*Skill, 5)
	for i := range skills {
		skills[i] = &Skill{
			Name:   fmt.Sprintf("skill-%d", i),
			Title:  fmt.Sprintf("Skill %d", i),
			Domain: "code",
			Prompt: SkillPrompt{Instructions: fmt.Sprintf("Do %d.", i)},
		}
	}

	orch := newTestOrchestrator(skills...)
	sessionID := "sess-max"

	confidences := []float64{0.9, 0.5, 0.8, 0.7}
	for i := 0; i < 4; i++ {
		orch.ActivateSkill(sessionID, skills[i], "test", "", confidences[i])
	}

	actives := orch.GetActiveSkills(sessionID)
	require.Len(t, actives, 4, "no skill may be evicted just because the active count passed the legacy default of 3")

	names := activeSkillNamesForSession(orch, sessionID)
	for i := 0; i < 4; i++ {
		assert.Contains(t, names, fmt.Sprintf("skill-%d", i))
	}
}

// activeSkillNamesForSession returns the set of skill names currently
// active for a session, for assertion convenience.
func activeSkillNamesForSession(o *Orchestrator, sessionID string) []string {
	out := []string{}
	for _, as := range o.GetActiveSkills(sessionID) {
		out = append(out, as.Skill.Name)
	}
	return out
}

// TestOrchestrator_FormatActiveSkillsForLLM and TestOrchestrator_FormatActiveSkillsForLLM_
// TokenBudget are retired by loom D-2 (TER-419): FormatActiveSkillsForLLM is deleted tree-
// wide once its sole call site (pkg/agent's discovery block, agent.go) is removed — the
// skill-body-into-context injection channel it formatted for is gone (Seam 2 deletion
// manifest). There is no successor API in this package; the discovery *menu* D-6 adds is a
// pkg/agent-side tail note, not a pkg/skills formatter.

func TestOrchestrator_CleanupSession(t *testing.T) {
	skill := &Skill{
		Name:   "cleanup-skill",
		Title:  "Cleanup",
		Domain: "code",
		Prompt: SkillPrompt{Instructions: "Clean."},
	}

	orch := newTestOrchestrator(skill)
	sessionID := "sess-cleanup"

	orch.ActivateSkill(sessionID, skill, "test", "", 1.0)
	require.Len(t, orch.GetActiveSkills(sessionID), 1)

	orch.CleanupSession(sessionID)
	assert.Empty(t, orch.GetActiveSkills(sessionID))

	// Cleanup non-existent session should not panic.
	orch.CleanupSession("nonexistent-session")
}

func TestOrchestrator_ConcurrentAccess(t *testing.T) {
	skills := make([]*Skill, 10)
	for i := range skills {
		skills[i] = &Skill{
			Name:   fmt.Sprintf("concurrent-%d", i),
			Title:  fmt.Sprintf("Concurrent %d", i),
			Domain: "code",
			Trigger: SkillTrigger{
				SlashCommands: []string{fmt.Sprintf("/c%d", i)},
				Keywords:      []string{fmt.Sprintf("keyword%d", i)},
				Mode:          ActivationHybrid,
			},
			Prompt: SkillPrompt{Instructions: fmt.Sprintf("Do concurrent %d.", i)},
		}
	}

	orch := newTestOrchestrator(skills...)
	config := DefaultSkillsConfig()

	var wg sync.WaitGroup
	sessions := []string{"s1", "s2", "s3", "s4", "s5"}

	// 10 goroutines activating skills.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sess := sessions[idx%len(sessions)]
			skill := skills[idx%len(skills)]
			orch.ActivateSkill(sess, skill, "test", "", float64(idx)*0.1)
		}(i)
	}

	// 10 goroutines matching skills.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sess := sessions[idx%len(sessions)]
			_, _ = orch.MatchSkills(sess, fmt.Sprintf("/c%d some message", idx%10), config)
		}(i)
	}

	// 5 goroutines reading active skills.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sess := sessions[idx%len(sessions)]
			_ = orch.GetActiveSkills(sess)
		}(i)
	}

	// 5 goroutines deactivating.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sess := sessions[idx%len(sessions)]
			orch.DeactivateSkill(sess, fmt.Sprintf("concurrent-%d", idx))
		}(i)
	}

	// 3 goroutines cleaning up sessions.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			orch.CleanupSession(sessions[idx%len(sessions)])
		}(i)
	}

	wg.Wait()
	// If we get here without -race detector complaining, the test passes.
}

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		wantCmd  string
		wantRest string
	}{
		{
			name:     "simple command with rest",
			msg:      "/review this code",
			wantCmd:  "/review",
			wantRest: "this code",
		},
		{
			name:     "command only no rest",
			msg:      "/help",
			wantCmd:  "/help",
			wantRest: "",
		},
		{
			name:     "command is lowercased",
			msg:      "/REVIEW stuff",
			wantCmd:  "/review",
			wantRest: "stuff",
		},
		{
			name:     "leading whitespace trimmed",
			msg:      "   /review code",
			wantCmd:  "/review",
			wantRest: "code",
		},
		{
			name:     "not a slash command",
			msg:      "review this code",
			wantCmd:  "",
			wantRest: "",
		},
		{
			name:     "empty message",
			msg:      "",
			wantCmd:  "",
			wantRest: "",
		},
		{
			name:     "only whitespace",
			msg:      "   ",
			wantCmd:  "",
			wantRest: "",
		},
		{
			name:     "slash in middle of message",
			msg:      "use /review for reviewing",
			wantCmd:  "",
			wantRest: "",
		},
		{
			name:     "hyphenated command",
			msg:      "/code-review my PR",
			wantCmd:  "/code-review",
			wantRest: "my PR",
		},
		// --- whitespace-separator variants (the newline bug fix) ---
		{
			name:     "newline separator",
			msg:      "/review-py\nload this file",
			wantCmd:  "/review-py",
			wantRest: "load this file",
		},
		{
			name:     "tab separator",
			msg:      "/code-review\tcheck my PR",
			wantCmd:  "/code-review",
			wantRest: "check my PR",
		},
		{
			name:     "CRLF separator",
			msg:      "/review\r\nsome content",
			wantCmd:  "/review",
			wantRest: "some content",
		},
		{
			name:     "carriage return only",
			msg:      "/review\rsome content",
			wantCmd:  "/review",
			wantRest: "some content",
		},
		{
			name:     "multiple spaces between cmd and rest",
			msg:      "/review    lots of spaces",
			wantCmd:  "/review",
			wantRest: "lots of spaces",
		},
		{
			name:     "newline then indented rest",
			msg:      "/review\n  indented content",
			wantCmd:  "/review",
			wantRest: "indented content",
		},
		{
			name:     "tab then spaces before rest",
			msg:      "/review\t   spaced args",
			wantCmd:  "/review",
			wantRest: "spaced args",
		},
		{
			name:     "command only with trailing newline",
			msg:      "/help\n",
			wantCmd:  "/help",
			wantRest: "",
		},
		{
			// TrimSpace at the top of ParseSlashCommand strips trailing whitespace
			// from the whole message before splitting, so trailing spaces on rest
			// are not preserved — same behaviour as the original space-only split.
			name:     "trailing whitespace on msg is trimmed",
			msg:      "/review this code   ",
			wantCmd:  "/review",
			wantRest: "this code",
		},
		{
			name:     "uppercase command with newline separator",
			msg:      "/REVIEW\nsome code",
			wantCmd:  "/review",
			wantRest: "some code",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, rest := ParseSlashCommand(tc.msg)
			assert.Equal(t, tc.wantCmd, cmd)
			assert.Equal(t, tc.wantRest, rest)
		})
	}
}

func TestOrchestrator_GetLibrary(t *testing.T) {
	lib := NewLibrary(WithSearchPaths())
	orch := NewOrchestrator(lib)
	assert.Equal(t, lib, orch.GetLibrary())
}

func TestOrchestrator_DeactivateNonexistent(t *testing.T) {
	orch := newTestOrchestrator()

	// Should not panic.
	orch.DeactivateSkill("no-session", "no-skill")

	// Activate one, deactivate a different one -- should not affect the active one.
	skill := &Skill{
		Name:   "real-skill",
		Title:  "Real",
		Domain: "code",
		Prompt: SkillPrompt{Instructions: "Real."},
	}
	orch.ActivateSkill("sess", skill, "test", "", 1.0)
	orch.DeactivateSkill("sess", "nonexistent-skill")
	actives := orch.GetActiveSkills("sess")
	require.Len(t, actives, 1)
	assert.Equal(t, "real-skill", actives[0].Skill.Name)
}

func TestDefaultSkillsConfig(t *testing.T) {
	config := DefaultSkillsConfig()
	assert.True(t, config.Enabled)
	assert.Equal(t, 3, config.MaxConcurrentSkills)
	assert.InDelta(t, 0.7, config.MinAutoConfidence, 0.001)
	assert.Equal(t, 5, config.ContextBudgetPercent)
}

// TestOrchestrator_LogsActivationLifecycle verifies the orchestrator emits
// info-level log entries for skill activation, replacement, and
// deactivation so operators can trace which skills got picked per turn.
// There is no implicit eviction (O-SKL-3), so no "skill evicted" log is
// ever emitted by ActivateSkill regardless of how many skills are active.
func TestOrchestrator_LogsActivationLifecycle(t *testing.T) {
	core, observed := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)

	skillA := &Skill{Name: "skill-a", Domain: "code", Prompt: SkillPrompt{Instructions: "A."}}
	skillB := &Skill{Name: "skill-b", Domain: "code", Prompt: SkillPrompt{Instructions: "B."}}
	skillC := &Skill{Name: "skill-c", Domain: "code", Prompt: SkillPrompt{Instructions: "C."}}
	skillD := &Skill{Name: "skill-d", Domain: "code", Prompt: SkillPrompt{Instructions: "D."}}

	lib := NewLibrary()
	for _, s := range []*Skill{skillA, skillB, skillC, skillD} {
		lib.Register(s)
	}
	orch := NewOrchestrator(lib, WithOrchestratorLogger(logger))

	// Activate a fresh skill -> "skill activated".
	orch.ActivateSkill("sess", skillA, "slash", "/skill-a", 1.0)
	// Re-activate same skill -> "skill replaced".
	orch.ActivateSkill("sess", skillA, "slash", "/skill-a", 0.9)
	// Activate more skills than the legacy default of 3 -- must not evict.
	orch.ActivateSkill("sess", skillB, "keyword", "b", 0.6)
	orch.ActivateSkill("sess", skillC, "keyword", "c", 0.5)
	orch.ActivateSkill("sess", skillD, "keyword", "d", 0.8)

	require.Len(t, orch.GetActiveSkills("sess"), 4, "no implicit eviction: all four skills stay active")

	// Deactivate -> "skill deactivated".
	orch.DeactivateSkill("sess", skillA.Name)

	got := map[string]bool{}
	for _, e := range observed.All() {
		got[e.Message] = true
	}
	assert.True(t, got["skill activated"], "expected at least one 'skill activated' log; got %v", got)
	assert.True(t, got["skill replaced"], "expected 'skill replaced' log on duplicate ActivateSkill; got %v", got)
	assert.False(t, got["skill evicted"], "ActivateSkill must never log 'skill evicted': there is no implicit eviction")
	assert.True(t, got["skill deactivated"], "expected 'skill deactivated' log; got %v", got)

	// Spot-check that the activate entry carries the routing context.
	for _, e := range observed.FilterMessage("skill activated").All() {
		fields := e.ContextMap()
		if fields["skill"] == "skill-a" {
			assert.Equal(t, "slash", fields["trigger"])
			assert.Equal(t, "/skill-a", fields["trigger_value"])
			assert.Equal(t, "sess", fields["session"])
			return
		}
	}
	t.Fatalf("expected an 'activated' entry for skill-a with trigger=slash; got %v", observed.All())
}
