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
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/observability"
)

// testSkillYAML returns valid skill YAML for testing.
func testSkillYAML(name, domain, title string, slashCmds []string, keywords []string) string {
	yaml := "apiVersion: loom/v1\nkind: Skill\nmetadata:\n"
	yaml += "  name: " + name + "\n"
	yaml += "  title: " + title + "\n"
	yaml += "  description: Test skill for " + name + "\n"
	yaml += "  domain: " + domain + "\n"
	yaml += "  version: \"1.0.0\"\n"
	yaml += "trigger:\n"
	if len(slashCmds) > 0 {
		yaml += "  slash_commands:\n"
		for _, cmd := range slashCmds {
			yaml += "    - " + cmd + "\n"
		}
	}
	if len(keywords) > 0 {
		yaml += "  keywords:\n"
		for _, kw := range keywords {
			yaml += "    - " + kw + "\n"
		}
	}
	yaml += "  mode: MANUAL\n"
	yaml += "prompt:\n"
	yaml += "  instructions: Do the thing for " + name + ".\n"
	return yaml
}

// writeSkillFile writes a skill YAML file to a directory.
func writeSkillFile(t *testing.T, dir, name, domain, title string, slashCmds, keywords []string) {
	t.Helper()
	content := testSkillYAML(name, domain, title, slashCmds, keywords)
	err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0o644)
	require.NoError(t, err)
}

func TestNewLibrary_DefaultSearchPaths(t *testing.T) {
	// Temporarily set env var.
	t.Setenv("LOOM_SKILLS_DIR", "/tmp/test-loom-skills")

	lib := NewLibrary()
	assert.NotNil(t, lib)
	assert.Contains(t, lib.searchPaths, "/tmp/test-loom-skills")
}

func TestNewLibrary_WithSearchPaths(t *testing.T) {
	lib := NewLibrary(WithSearchPaths("/a", "/b"))
	assert.Equal(t, []string{"/a", "/b"}, lib.searchPaths)
}

func TestNewLibrary_WithTracer(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	lib := NewLibrary(WithTracer(tracer))
	assert.NotNil(t, lib.tracer)
}

func TestLibrary_Register_And_Get(t *testing.T) {
	lib := NewLibrary(WithSearchPaths()) // empty paths to avoid defaulting

	skill := &Skill{
		Name:   "test-skill",
		Title:  "Test Skill",
		Domain: "code",
	}

	lib.Register(skill)

	got := lib.Get("test-skill")
	require.NotNil(t, got)
	assert.Equal(t, "test-skill", got.Name)

	assert.Nil(t, lib.Get("nonexistent"))
}

// Get method for backward compat - the old library had it
func (l *Library) Get(name string) *Skill {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.skillCache[name]
}

func TestLibrary_LoadFromFilesystem(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "code-review", "code", "Code Review", []string{"/review"}, []string{"review", "code"})

	lib := NewLibrary(WithSearchPaths(dir))

	skill, err := lib.Load("code-review")
	require.NoError(t, err)
	assert.Equal(t, "code-review", skill.Name)
	assert.Equal(t, "Code Review", skill.Title)

	// Second load should hit cache.
	skill2, err := lib.Load("code-review")
	require.NoError(t, err)
	assert.Equal(t, skill, skill2)
}

func TestLibrary_Load_NotFound(t *testing.T) {
	lib := NewLibrary(WithSearchPaths(t.TempDir()))
	_, err := lib.Load("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "skill not found")
}

func TestLibrary_ListAll(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "alpha-skill", "code", "Alpha Skill", nil, nil)
	writeSkillFile(t, dir, "beta-skill", "sql", "Beta Skill", nil, nil)

	lib := NewLibrary(WithSearchPaths(dir))

	summaries := lib.ListAll()
	require.Len(t, summaries, 2)
	// Should be sorted alphabetically.
	assert.Equal(t, "alpha-skill", summaries[0].Name)
	assert.Equal(t, "beta-skill", summaries[1].Name)

	// Second call should return cached result.
	summaries2 := lib.ListAll()
	assert.Equal(t, summaries, summaries2)
}

func TestLibrary_Search(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "sql-optimize", "sql", "SQL Optimizer", nil, []string{"sql", "optimize", "performance"})
	writeSkillFile(t, dir, "code-review", "code", "Code Review", nil, []string{"review", "code", "quality"})

	lib := NewLibrary(WithSearchPaths(dir))

	tests := []struct {
		name      string
		query     string
		wantFirst string
		wantLen   int
	}{
		{
			name:      "match sql keyword",
			query:     "sql optimize",
			wantFirst: "sql-optimize",
			wantLen:   1,
		},
		{
			name:      "match code keyword",
			query:     "code review",
			wantFirst: "code-review",
			wantLen:   1,
		},
		{
			name:    "no match",
			query:   "kubernetes deploy",
			wantLen: 0,
		},
		{
			name:    "empty query returns all",
			query:   "",
			wantLen: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			results := lib.Search(tc.query)
			assert.Len(t, results, tc.wantLen)
			if tc.wantLen > 0 && tc.wantFirst != "" {
				assert.Equal(t, tc.wantFirst, results[0].Skill.Name)
			}
		})
	}
}

func TestLibrary_FindBySlashCommand(t *testing.T) {
	lib := NewLibrary(WithSearchPaths()) // empty to prevent defaults
	lib.Register(&Skill{
		Name:  "code-review",
		Title: "Code Review",
		Trigger: SkillTrigger{
			SlashCommands: []string{"/review", "/cr"},
		},
	})
	lib.Register(&Skill{
		Name:  "sql-optimize",
		Title: "SQL Optimize",
		Trigger: SkillTrigger{
			SlashCommands: []string{"/sql-opt"},
		},
	})

	tests := []struct {
		cmd      string
		wantName string
		wantOk   bool
	}{
		{"/review", "code-review", true},
		{"/cr", "code-review", true},
		{"/REVIEW", "code-review", true}, // case-insensitive
		{"/sql-opt", "sql-optimize", true},
		{"/unknown", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			skill, ok := lib.FindBySlashCommand(tc.cmd)
			assert.Equal(t, tc.wantOk, ok)
			if tc.wantOk {
				assert.Equal(t, tc.wantName, skill.Name)
			}
		})
	}
}

func TestLibrary_FindByKeywords(t *testing.T) {
	lib := NewLibrary(WithSearchPaths())
	lib.Register(&Skill{
		Name: "sql-help",
		Trigger: SkillTrigger{
			Keywords: []string{"sql", "query", "database"},
		},
	})
	lib.Register(&Skill{
		Name: "code-help",
		Trigger: SkillTrigger{
			Keywords: []string{"code", "function", "debug"},
		},
	})

	results := lib.FindByKeywords("help me write a sql query for the database")
	require.NotEmpty(t, results)
	assert.Equal(t, "sql-help", results[0].Skill.Name)
	// sql-help matches 3/3 keywords -> score 1.0
	assert.InDelta(t, 1.0, results[0].Score, 0.01)
}

func TestLibrary_ListByDomain(t *testing.T) {
	lib := NewLibrary(WithSearchPaths())
	lib.Register(&Skill{Name: "skill-a", Domain: "sql"})
	lib.Register(&Skill{Name: "skill-b", Domain: "code"})
	lib.Register(&Skill{Name: "skill-c", Domain: "sql"})

	sqlSkills := lib.ListByDomain("sql")
	assert.Len(t, sqlSkills, 2)

	codeSkills := lib.ListByDomain("code")
	assert.Len(t, codeSkills, 1)

	noneSkills := lib.ListByDomain("ops")
	assert.Empty(t, noneSkills)
}

func TestLibrary_WriteSkill(t *testing.T) {
	dir := t.TempDir()
	lib := NewLibrary(WithSearchPaths(dir))

	skill := &Skill{
		Name:    "new-skill",
		Title:   "New Skill",
		Domain:  "code",
		Version: "1.0.0",
		Trigger: SkillTrigger{
			Mode: ActivationManual,
		},
		Prompt: SkillPrompt{
			Instructions: "Do something useful.",
		},
	}

	err := lib.WriteSkill(skill)
	require.NoError(t, err)

	// Verify file was created.
	_, err = os.Stat(filepath.Join(dir, "new-skill.yaml"))
	require.NoError(t, err)

	// Verify it can be loaded back.
	loaded, err := lib.Load("new-skill")
	require.NoError(t, err)
	assert.Equal(t, "new-skill", loaded.Name)
	assert.Equal(t, "New Skill", loaded.Title)
}

func TestLibrary_WriteSkill_NoWritableDir(t *testing.T) {
	lib := NewLibrary(WithSearchPaths()) // no search paths at all
	lib.searchPaths = nil                // force empty

	skill := &Skill{Name: "test", Domain: "code", Prompt: SkillPrompt{Instructions: "x"}}
	err := lib.WriteSkill(skill)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no writable skills directory")
}

func TestLibrary_InvalidateCache(t *testing.T) {
	lib := NewLibrary(WithSearchPaths())
	lib.Register(&Skill{Name: "cached-skill", Domain: "code"})

	assert.NotNil(t, lib.Get("cached-skill"))

	lib.InvalidateCache()
	assert.Nil(t, lib.Get("cached-skill"))
}

func TestLibrary_RemoveFromCache(t *testing.T) {
	lib := NewLibrary(WithSearchPaths())
	lib.Register(&Skill{Name: "a", Domain: "code"})
	lib.Register(&Skill{Name: "b", Domain: "code"})

	lib.RemoveFromCache("a")
	assert.Nil(t, lib.Get("a"))
	assert.NotNil(t, lib.Get("b"))
}

func TestLibrary_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "concurrent-skill", "code", "Concurrent", []string{"/concurrent"}, []string{"test"})

	lib := NewLibrary(WithSearchPaths(dir))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = lib.Load("concurrent-skill")
			lib.ListAll()
			lib.Search("concurrent")
			lib.FindBySlashCommand("/concurrent")
			lib.FindByKeywords("test concurrent")
			lib.ListByDomain("code")
		}()
	}

	// Concurrent writes too.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lib.InvalidateCache()
		}()
	}

	wg.Wait()
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"sql optimize performance", []string{"sql", "optimize", "performance"}},
		{"the, quick-brown_fox", []string{"quick", "brown", "fox"}},
		{"a an the", []string{}}, // all stop words / too short
		{"", []string{}},         // empty
		{"SQL/Code.Review", []string{"sql", "code", "review"}}, // delimiter chars
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := tokenize(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestScoreSkill(t *testing.T) {
	skill := &Skill{
		Name:        "sql-optimize",
		Title:       "SQL Optimizer",
		Description: "Optimize SQL queries for better performance",
		Domain:      "sql",
		Trigger: SkillTrigger{
			Keywords: []string{"optimize", "performance", "slow"},
		},
	}

	tests := []struct {
		name     string
		tokens   []string
		wantGt   float64
		wantZero bool
	}{
		{"exact name match", []string{"sql"}, 0, false},
		{"title match gets boost", []string{"optimizer"}, 0.5, false},
		{"no match", []string{"kubernetes"}, 0, true},
		{"multiple matches", []string{"sql", "optimize"}, 1.0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score := scoreSkill(skill, tc.tokens)
			if tc.wantZero {
				assert.Equal(t, 0.0, score)
			} else {
				assert.Greater(t, score, tc.wantGt)
			}
		})
	}
}
