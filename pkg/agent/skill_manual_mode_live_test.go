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

	"github.com/teradata-labs/loom/pkg/llm/bedrock"
	"github.com/teradata-labs/loom/pkg/skills"
)

// The MANUAL rule decides what a real model is shown and what it may pull. The
// scripted tests pin the mechanism; this one puts a live model in front of it,
// because the failure the rule exists to stop (TER-1031) was a model reading a
// skill description and deciding to load it — a judgement no mock makes.
//
// Opt-in: LOOM_LIVE_SKILL_TEST=1 plus AWS credentials for Bedrock. Skipped
// otherwise, so it never runs in CI or on a developer's default `go test`.
//
//	LOOM_LIVE_SKILL_TEST=1 AWS_PROFILE=bedrock go test -tags fts5 ./pkg/agent/ \
//	  -run TestManualMode_Live -v
func TestManualMode_Live(t *testing.T) {
	if os.Getenv("LOOM_LIVE_SKILL_TEST") != "1" {
		t.Skip("set LOOM_LIVE_SKILL_TEST=1 (needs Bedrock credentials) to run")
	}

	model := os.Getenv("LOOM_LIVE_MODEL")
	if model == "" {
		model = "anthropic.claude-sonnet-4-5-20250929-v1:0"
	}
	llm, err := bedrock.NewClientForModel(bedrock.Config{
		Region:      "us-west-2",
		ModelID:     model,
		MaxTokens:   2048,
		Temperature: 1.0,
	})
	require.NoError(t, err, "bedrock client")

	// The fixtures mirror the reported case: a profiling skill whose description
	// is written to attract the model ("TRIGGER when the user asks…"), and a
	// second skill it may legitimately pull.
	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("live-profile-skill.yaml", `apiVersion: loom/v1
kind: Skill
metadata:
  name: live-profile-skill
  title: Table Profiling
  description: Data profiling for database tables. TRIGGER when the user asks to profile, inspect, summarize or assess a table, or asks about nulls, distributions, duplicates or data quality.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
  slash_commands:
    - /live-profile-skill
prompt:
  instructions: |
    LIVE-PROFILE-BODY-SENTINEL. Profile a table in three steps: row count, column types, null counts.
`)
	// No trigger block at all — the shape nearly every shipped skill has. It
	// must stay model-pullable, or enforcing MANUAL would retire the library.
	write("live-notes-skill.yaml", `apiVersion: loom/v1
kind: Skill
metadata:
  name: live-notes-skill
  title: Note Taking
  description: Note taking. TRIGGER when the user asks to take, format or summarize notes, or to turn something into bullet points.
  domain: general
  risk_level: LOW
prompt:
  instructions: |
    LIVE-NOTES-BODY-SENTINEL. Write notes as short bullets, newest first.
`)

	newRig := func() (*Agent, *skills.Orchestrator) {
		lib := skills.NewLibrary(skills.WithSearchPaths(dir))
		orch := skills.NewOrchestrator(lib)
		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.Enabled = false
		cfg.SkillsConfig = &skills.SkillsConfig{Enabled: true}
		ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithSkillOrchestrator(orch))
		return ag, orch
	}
	active := func(orch *skills.Orchestrator, sessionID string) []string {
		var names []string
		for _, as := range orch.GetActiveSkills(sessionID) {
			if as != nil && as.Skill != nil {
				names = append(names, as.Skill.Name)
			}
		}
		return names
	}

	// The menu is what a live model reads before deciding. It must name the
	// skill it may pull and withhold the one reserved for the user.
	t.Run("the model is shown only what it may load", func(t *testing.T) {
		ag, _ := newRig()
		menu := ag.skillMenuPromptSupplement()
		assert.Contains(t, menu, "live-notes-skill")
		assert.NotContains(t, menu, "live-profile-skill")
	})

	// The reported failure, put to a real model: the exact question that pulled
	// td-data-profile in with no slash command.
	t.Run("a plain question does not pull the manual skill", func(t *testing.T) {
		ag, orch := newRig()
		const sessionID = "live-plain"

		resp, err := ag.Chat(context.Background(), sessionID,
			"Can you profile the online_retail table end-to-end?")
		require.NoError(t, err)

		assert.NotContains(t, active(orch, sessionID), "live-profile-skill",
			"a MANUAL skill must not activate without the user's command")
		assert.NotContains(t, resp.Content, "LIVE-PROFILE-BODY-SENTINEL",
			"the skill body never reached the model")
		t.Logf("model replied: %s", strings.TrimSpace(resp.Content))
	})

	// The other half of the rule, and the one that decides whether this is
	// shippable: a skill that declares no mode keeps working the way it does
	// today. Nearly every skill on a deployed site is that shape, so if the
	// undeclared default withheld them, enforcing MANUAL would empty the
	// library instead of fixing the bug.
	t.Run("a skill with no declared mode is still pulled by the model", func(t *testing.T) {
		ag, orch := newRig()
		const sessionID = "live-undeclared"

		resp, err := ag.Chat(context.Background(), sessionID,
			"Please turn this into short bullet notes: we shipped the parser, fixed two bugs, and the deploy is tomorrow.")
		require.NoError(t, err)

		assert.Contains(t, active(orch, sessionID), "live-notes-skill",
			"an undeclared mode must leave the skill model-pullable")
		t.Logf("model replied: %s", strings.TrimSpace(resp.Content))
	})

	// The user's route, end to end: the harness loads the skill and the live
	// model answers from its instructions.
	t.Run("the slash command loads it and the model follows it", func(t *testing.T) {
		ag, orch := newRig()
		const sessionID = "live-slash"

		resp, err := ag.Chat(context.Background(), sessionID,
			"/live-profile-skill online_retail — what are the steps you will run?")
		require.NoError(t, err)

		assert.Contains(t, active(orch, sessionID), "live-profile-skill",
			"the slash command activated the skill")
		t.Logf("model replied: %s", strings.TrimSpace(resp.Content))
	})
}
