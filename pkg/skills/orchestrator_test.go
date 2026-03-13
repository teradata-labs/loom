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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestOrchestrator_MaxConcurrentSkills(t *testing.T) {
	// Create 5 skills with varying confidence.
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

	// Activate 4 skills. The default max is 3, so the lowest confidence
	// should be evicted after the 4th activation.
	confidences := []float64{0.9, 0.5, 0.8, 0.7}
	for i := 0; i < 4; i++ {
		orch.ActivateSkill(sessionID, skills[i], "test", "", confidences[i])
	}

	actives := orch.GetActiveSkills(sessionID)
	require.Len(t, actives, 3, "should evict to default max of 3")

	// The lowest confidence was 0.5 (skill-1), it should be evicted.
	for _, a := range actives {
		assert.NotEqual(t, "skill-1", a.Skill.Name, "lowest confidence skill should be evicted")
	}
}

func TestOrchestrator_FormatActiveSkillsForLLM(t *testing.T) {
	skillA := &Skill{
		Name:  "skill-a",
		Title: "Skill A",
		Prompt: SkillPrompt{
			Instructions: "Instructions for A.",
		},
	}
	skillB := &Skill{
		Name:  "skill-b",
		Title: "Skill B",
		Prompt: SkillPrompt{
			Instructions: "Instructions for B.",
		},
	}

	orch := newTestOrchestrator(skillA, skillB)
	sessionID := "sess-format"

	// No active skills.
	output := orch.FormatActiveSkillsForLLM(sessionID, 10000)
	assert.Empty(t, output)

	// Activate both.
	orch.ActivateSkill(sessionID, skillA, "test", "", 1.0)
	orch.ActivateSkill(sessionID, skillB, "test", "", 0.9)

	output = orch.FormatActiveSkillsForLLM(sessionID, 10000)
	assert.Contains(t, output, "## Skill: Skill A")
	assert.Contains(t, output, "Instructions for A.")
	assert.Contains(t, output, "## Skill: Skill B")
	assert.Contains(t, output, "Instructions for B.")
	assert.Contains(t, output, "---", "skills should be separated by ---")
}

func TestOrchestrator_FormatActiveSkillsForLLM_TokenBudget(t *testing.T) {
	// Create a skill with large instructions.
	bigSkill := &Skill{
		Name:  "big-skill",
		Title: "Big Skill",
		Prompt: SkillPrompt{
			Instructions: strings.Repeat("x", 1000),
		},
	}

	orch := newTestOrchestrator(bigSkill)
	sessionID := "sess-budget"
	orch.ActivateSkill(sessionID, bigSkill, "test", "", 1.0)

	// Budget of 10 tokens = 40 chars, which is not enough for the big skill.
	output := orch.FormatActiveSkillsForLLM(sessionID, 10)
	assert.Empty(t, output, "skill should be skipped when it exceeds token budget")

	// Budget of 10000 tokens = 40000 chars, which is enough.
	output = orch.FormatActiveSkillsForLLM(sessionID, 10000)
	assert.NotEmpty(t, output)
	assert.Contains(t, output, "## Skill: Big Skill")
}

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
			_ = orch.FormatActiveSkillsForLLM(sess, 5000)
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, rest := parseSlashCommand(tc.msg)
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
