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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSkill(t *testing.T) {
	// Use the real example file as reference.
	path := filepath.Join("..", "..", "examples", "skills", "code-review.yaml")

	// Skip if the example file doesn't exist (e.g., in CI without full checkout).
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("example file not found: %s", path)
	}

	skill, err := LoadSkill(path)
	require.NoError(t, err)

	// Metadata
	assert.Equal(t, "code-review", skill.Name)
	assert.Equal(t, "Code Review", skill.Title)
	assert.Equal(t, "Analyze code for bugs, style issues, and improvements", skill.Description)
	assert.Equal(t, "1.0.0", skill.Version)
	assert.Equal(t, "code", skill.Domain)
	assert.Equal(t, "loom", skill.Author)
	assert.Equal(t, map[string]string{"category": "development"}, skill.Labels)

	// Trigger
	assert.Equal(t, []string{"/review", "/code-review"}, skill.Trigger.SlashCommands)
	assert.Contains(t, skill.Trigger.Keywords, "review this code")
	assert.Contains(t, skill.Trigger.Keywords, "code review")
	assert.Equal(t, ActivationHybrid, skill.Trigger.Mode)
	assert.InDelta(t, 0.7, skill.Trigger.MinConfidence, 0.001)
	assert.Equal(t, []string{"code_review"}, skill.Trigger.IntentCategories)

	// Prompt
	assert.Contains(t, skill.Prompt.Instructions, "Analyze the provided code")
	assert.Len(t, skill.Prompt.Constraints, 3)
	assert.NotEmpty(t, skill.Prompt.OutputFormat)
	assert.Len(t, skill.Prompt.Examples, 1)
	assert.Equal(t, "Review this Go function for issues", skill.Prompt.Examples[0].UserInput)

	// Tools
	assert.Equal(t, []string{"file_read"}, skill.Tools.RequiredTools)
	assert.Equal(t, []string{"file_read", "shell_execute"}, skill.Tools.PreferredOrder)

	// Sticky
	assert.True(t, skill.Sticky)
}

func TestLoadSkill_InvalidYAML(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "completely invalid yaml",
			content: "{{{{ not yaml at all",
			wantErr: "failed to parse skill YAML",
		},
		{
			name:    "valid yaml but invalid structure",
			content: "- just\n- a\n- list\n",
			wantErr: "failed to parse skill YAML",
		},
		{
			name:    "empty file",
			content: "",
			wantErr: "metadata.name is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "bad.yaml")
			err := os.WriteFile(path, []byte(tc.content), 0o644)
			require.NoError(t, err)

			_, err = LoadSkill(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadSkill_FileNotFound(t *testing.T) {
	_, err := LoadSkill("/nonexistent/path/skill.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read skill file")
}

func TestLoadSkill_Validation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "missing name",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  domain: code
prompt:
  instructions: Do something.`,
			wantErr: "metadata.name is required",
		},
		{
			name: "non-kebab-case name",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: Code Review
  domain: code
prompt:
  instructions: Do something.`,
			wantErr: "metadata.name must be kebab-case",
		},
		{
			name: "name with uppercase",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: CodeReview
  domain: code
prompt:
  instructions: Do something.`,
			wantErr: "metadata.name must be kebab-case",
		},
		{
			name: "missing domain",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: test-skill
prompt:
  instructions: Do something.`,
			wantErr: "metadata.domain is required",
		},
		{
			name: "invalid domain",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: test-skill
  domain: kubernetes
prompt:
  instructions: Do something.`,
			wantErr: "invalid domain",
		},
		{
			name: "invalid mode",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: test-skill
  domain: code
trigger:
  mode: TURBO
prompt:
  instructions: Do something.`,
			wantErr: "invalid trigger.mode",
		},
		{
			name: "empty instructions",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: test-skill
  domain: code
prompt:
  instructions: ""`,
			wantErr: "prompt.instructions is required",
		},
		{
			name: "whitespace-only instructions",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: test-skill
  domain: code
prompt:
  instructions: "   "`,
			wantErr: "prompt.instructions is required",
		},
		{
			name: "too many skill_refs",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: test-skill
  domain: code
prompt:
  instructions: Do something.
skill_refs:
  - ref1
  - ref2
  - ref3`,
			wantErr: "skill_refs max depth is 2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "skill.yaml")
			err := os.WriteFile(path, []byte(tc.yaml), 0o644)
			require.NoError(t, err)

			_, err = LoadSkill(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadSkill_ValidCases(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantMode SkillActivationMode
		wantConf float64
		wantVer  string
	}{
		{
			name: "minimal valid skill",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: minimal
  domain: general
prompt:
  instructions: Do the thing.`,
			wantMode: ActivationManual, // default
			wantConf: 0.7,              // default
			wantVer:  "1.0.0",          // default
		},
		{
			name: "explicit AUTO mode",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: auto-skill
  domain: sql
trigger:
  mode: AUTO
  min_confidence: 0.9
prompt:
  instructions: Analyze queries.`,
			wantMode: ActivationAuto,
			wantConf: 0.9,
			wantVer:  "1.0.0",
		},
		{
			name: "all valid domains accepted",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: data-quality-skill
  domain: data-quality
  version: "2.0.0"
prompt:
  instructions: Check data quality.`,
			wantMode: ActivationManual,
			wantConf: 0.7,
			wantVer:  "2.0.0",
		},
		{
			name: "two skill_refs allowed",
			yaml: `apiVersion: loom/v1
kind: Skill
metadata:
  name: composed-skill
  domain: code
prompt:
  instructions: Compose things.
skill_refs:
  - ref1
  - ref2`,
			wantMode: ActivationManual,
			wantConf: 0.7,
			wantVer:  "1.0.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "skill.yaml")
			err := os.WriteFile(path, []byte(tc.yaml), 0o644)
			require.NoError(t, err)

			skill, err := LoadSkill(path)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMode, skill.Trigger.Mode)
			assert.InDelta(t, tc.wantConf, skill.Trigger.MinConfidence, 0.001)
			assert.Equal(t, tc.wantVer, skill.Version)
		})
	}
}

func TestLoadSkillLibrary(t *testing.T) {
	libraryYAML := `apiVersion: loom/v1
kind: SkillLibrary
metadata:
  name: test-library
  version: "1.0.0"
  description: Test library
skills:
  - apiVersion: loom/v1
    kind: Skill
    metadata:
      name: lib-skill-a
      title: Lib Skill A
      domain: code
    prompt:
      instructions: Do A.
  - apiVersion: loom/v1
    kind: Skill
    metadata:
      name: lib-skill-b
      title: Lib Skill B
      domain: sql
    prompt:
      instructions: Do B.`

	dir := t.TempDir()
	path := filepath.Join(dir, "library.yaml")
	err := os.WriteFile(path, []byte(libraryYAML), 0o644)
	require.NoError(t, err)

	skills, err := LoadSkillLibrary(path)
	require.NoError(t, err)
	require.Len(t, skills, 2)
	assert.Equal(t, "lib-skill-a", skills[0].Name)
	assert.Equal(t, "lib-skill-b", skills[1].Name)
	assert.Equal(t, "code", skills[0].Domain)
	assert.Equal(t, "sql", skills[1].Domain)
}

func TestLoadSkillLibrary_InvalidCases(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "wrong apiVersion",
			yaml: `apiVersion: loom/v2
kind: SkillLibrary
metadata:
  name: test
skills:
  - metadata:
      name: s
      domain: code
    prompt:
      instructions: x`,
			wantErr: "unsupported apiVersion",
		},
		{
			name: "wrong kind",
			yaml: `apiVersion: loom/v1
kind: SkillBundle
metadata:
  name: test
skills:
  - metadata:
      name: s
      domain: code
    prompt:
      instructions: x`,
			wantErr: "kind must be 'SkillLibrary'",
		},
		{
			name: "missing library name",
			yaml: `apiVersion: loom/v1
kind: SkillLibrary
metadata:
  version: "1.0.0"
skills:
  - metadata:
      name: s
      domain: code
    prompt:
      instructions: x`,
			wantErr: "metadata.name is required",
		},
		{
			name: "empty skills list",
			yaml: `apiVersion: loom/v1
kind: SkillLibrary
metadata:
  name: empty-lib
skills: []`,
			wantErr: "must contain at least one skill",
		},
		{
			name: "invalid skill in library",
			yaml: `apiVersion: loom/v1
kind: SkillLibrary
metadata:
  name: bad-lib
skills:
  - metadata:
      name: Bad Name
      domain: code
    prompt:
      instructions: Do something.`,
			wantErr: "invalid skill at index 0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "library.yaml")
			err := os.WriteFile(path, []byte(tc.yaml), 0o644)
			require.NoError(t, err)

			_, err = LoadSkillLibrary(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestSkillToYAML_RoundTrip(t *testing.T) {
	original := &Skill{
		Name:        "round-trip",
		Title:       "Round Trip Test",
		Description: "Tests YAML round trip",
		Version:     "2.0.0",
		Domain:      "code",
		Author:      "tester",
		Labels:      map[string]string{"env": "test"},
		Trigger: SkillTrigger{
			SlashCommands:    []string{"/rt"},
			Keywords:         []string{"round", "trip"},
			IntentCategories: []string{"testing"},
			Mode:             ActivationHybrid,
			MinConfidence:    0.8,
		},
		Prompt: SkillPrompt{
			Instructions: "Do the round trip.",
			Constraints:  []string{"Be accurate", "Be fast"},
			OutputFormat: "## Results\n- Items",
			Examples: []SkillExample{
				{
					UserInput:      "test input",
					ExpectedOutput: "test output",
					Explanation:    "because reasons",
				},
			},
		},
		Tools: SkillToolConfig{
			RequiredTools:  []string{"file_read"},
			PreferredOrder: []string{"file_read", "shell_execute"},
			ExcludedTools:  []string{"web_search"},
			MCPServers:     []string{"my-server"},
		},
		PatternRefs:     []string{"pattern-a"},
		SkillRefs:       []string{"skill-x"},
		MaxPromptTokens: 1500,
		Sticky:          true,
		Backend:         "teradata",
	}

	// Serialize to YAML.
	data, err := SkillToYAML(original)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	// Write to temp file and load back.
	dir := t.TempDir()
	path := filepath.Join(dir, "round-trip.yaml")
	err = os.WriteFile(path, data, 0o644)
	require.NoError(t, err)

	loaded, err := LoadSkill(path)
	require.NoError(t, err)

	// Compare all fields.
	assert.Equal(t, original.Name, loaded.Name)
	assert.Equal(t, original.Title, loaded.Title)
	assert.Equal(t, original.Description, loaded.Description)
	assert.Equal(t, original.Version, loaded.Version)
	assert.Equal(t, original.Domain, loaded.Domain)
	assert.Equal(t, original.Author, loaded.Author)
	assert.Equal(t, original.Labels, loaded.Labels)

	// Trigger
	assert.Equal(t, original.Trigger.SlashCommands, loaded.Trigger.SlashCommands)
	assert.Equal(t, original.Trigger.Keywords, loaded.Trigger.Keywords)
	assert.Equal(t, original.Trigger.IntentCategories, loaded.Trigger.IntentCategories)
	assert.Equal(t, original.Trigger.Mode, loaded.Trigger.Mode)
	assert.InDelta(t, original.Trigger.MinConfidence, loaded.Trigger.MinConfidence, 0.001)

	// Prompt
	assert.Equal(t, original.Prompt.Instructions, loaded.Prompt.Instructions)
	assert.Equal(t, original.Prompt.Constraints, loaded.Prompt.Constraints)
	assert.Equal(t, original.Prompt.OutputFormat, loaded.Prompt.OutputFormat)
	require.Len(t, loaded.Prompt.Examples, 1)
	assert.Equal(t, original.Prompt.Examples[0].UserInput, loaded.Prompt.Examples[0].UserInput)
	assert.Equal(t, original.Prompt.Examples[0].ExpectedOutput, loaded.Prompt.Examples[0].ExpectedOutput)
	assert.Equal(t, original.Prompt.Examples[0].Explanation, loaded.Prompt.Examples[0].Explanation)

	// Tools
	assert.Equal(t, original.Tools.RequiredTools, loaded.Tools.RequiredTools)
	assert.Equal(t, original.Tools.PreferredOrder, loaded.Tools.PreferredOrder)
	assert.Equal(t, original.Tools.ExcludedTools, loaded.Tools.ExcludedTools)
	assert.Equal(t, original.Tools.MCPServers, loaded.Tools.MCPServers)

	// Other fields
	assert.Equal(t, original.PatternRefs, loaded.PatternRefs)
	assert.Equal(t, original.SkillRefs, loaded.SkillRefs)
	assert.Equal(t, original.MaxPromptTokens, loaded.MaxPromptTokens)
	assert.Equal(t, original.Sticky, loaded.Sticky)
	assert.Equal(t, original.Backend, loaded.Backend)
}

func TestValidateSkillYAML(t *testing.T) {
	tests := []struct {
		name    string
		sy      SkillYAML
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid minimal",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "test-skill", Domain: "code"},
				Prompt:   SkillPromptYAML{Instructions: "Do something."},
			},
			wantErr: false,
		},
		{
			name: "valid with all domains",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "my-skill", Domain: "analytics"},
				Prompt:   SkillPromptYAML{Instructions: "Analyze."},
			},
			wantErr: false,
		},
		{
			name: "valid with ALWAYS mode",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "always-on", Domain: "general"},
				Trigger:  SkillTriggerYAML{Mode: "ALWAYS"},
				Prompt:   SkillPromptYAML{Instructions: "Always active."},
			},
			wantErr: false,
		},
		{
			name: "valid empty mode defaults",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "default-mode", Domain: "ops"},
				Trigger:  SkillTriggerYAML{Mode: ""},
				Prompt:   SkillPromptYAML{Instructions: "Default."},
			},
			wantErr: false,
		},
		{
			name: "invalid: empty name",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "", Domain: "code"},
				Prompt:   SkillPromptYAML{Instructions: "Do something."},
			},
			wantErr: true,
			errMsg:  "metadata.name is required",
		},
		{
			name: "invalid: uppercase name",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "MySkill", Domain: "code"},
				Prompt:   SkillPromptYAML{Instructions: "Do something."},
			},
			wantErr: true,
			errMsg:  "metadata.name must be kebab-case",
		},
		{
			name: "invalid: name with spaces",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "my skill", Domain: "code"},
				Prompt:   SkillPromptYAML{Instructions: "Do something."},
			},
			wantErr: true,
			errMsg:  "metadata.name must be kebab-case",
		},
		{
			name: "invalid: empty domain",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "test-skill", Domain: ""},
				Prompt:   SkillPromptYAML{Instructions: "Do something."},
			},
			wantErr: true,
			errMsg:  "metadata.domain is required",
		},
		{
			name: "invalid: unknown domain",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "test-skill", Domain: "blockchain"},
				Prompt:   SkillPromptYAML{Instructions: "Do something."},
			},
			wantErr: true,
			errMsg:  "invalid domain",
		},
		{
			name: "invalid: bad mode",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "test-skill", Domain: "code"},
				Trigger:  SkillTriggerYAML{Mode: "FAST"},
				Prompt:   SkillPromptYAML{Instructions: "Do something."},
			},
			wantErr: true,
			errMsg:  "invalid trigger.mode",
		},
		{
			name: "invalid: empty instructions",
			sy: SkillYAML{
				Metadata: SkillMetadataYAML{Name: "test-skill", Domain: "code"},
				Prompt:   SkillPromptYAML{Instructions: ""},
			},
			wantErr: true,
			errMsg:  "prompt.instructions is required",
		},
		{
			name: "invalid: 3 skill_refs",
			sy: SkillYAML{
				Metadata:  SkillMetadataYAML{Name: "test-skill", Domain: "code"},
				Prompt:    SkillPromptYAML{Instructions: "Do something."},
				SkillRefs: []string{"a", "b", "c"},
			},
			wantErr: true,
			errMsg:  "skill_refs max depth is 2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSkillYAML(&tc.sy)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoadSkill_EnvVarExpansion(t *testing.T) {
	t.Setenv("LOOM_TEST_DOMAIN", "sql")
	t.Setenv("LOOM_TEST_INSTR", "Do the expanded thing.")

	yamlContent := `apiVersion: loom/v1
kind: Skill
metadata:
  name: env-skill
  domain: $LOOM_TEST_DOMAIN
prompt:
  instructions: $LOOM_TEST_INSTR`

	dir := t.TempDir()
	path := filepath.Join(dir, "env-skill.yaml")
	err := os.WriteFile(path, []byte(yamlContent), 0o644)
	require.NoError(t, err)

	skill, err := LoadSkill(path)
	require.NoError(t, err)
	assert.Equal(t, "sql", skill.Domain)
	assert.Equal(t, "Do the expanded thing.", skill.Prompt.Instructions)
}
