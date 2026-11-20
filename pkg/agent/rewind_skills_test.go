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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
)

// skillAgent is an agent over store with the fixture skill library and its own
// orchestrator, as each process (or agent object) has.
func skillAgent(t *testing.T, store *SessionStore, dir string, responses ...mockLLMResponse) (*Agent, *skills.Orchestrator) {
	t.Helper()
	orch := skills.NewOrchestrator(skills.NewLibrary(skills.WithSearchPaths(dir)))
	cfg := DefaultConfig()
	cfg.Name = "manager"
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.SkillsConfig = &skills.SkillsConfig{Enabled: false}
	ag := NewAgent(&mockBackend{}, &mockToolCallingLLM{responses: responses}, WithConfig(cfg),
		WithSkillOrchestrator(orch), WithMemory(NewMemoryWithStore(store)))
	return ag, orch
}

func activeNames(orch *skills.Orchestrator, sessionID string) []string {
	var out []string
	for _, as := range orch.GetActiveSkills(sessionID) {
		out = append(out, as.Skill.Name)
	}
	return out
}

func skillStore(t *testing.T) *SessionStore {
	t.Helper()
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "skills.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// A skill loaded in an undone turn is not active after the rewind, and the
// tools only it required leave the session; a skill loaded in a kept turn
// stays, with its tools.
func TestARewindTakesTheSkillsOfItsTurnsWithIt(t *testing.T) {
	store, dir, ctx := skillStore(t), writeSkillFixtures(t), context.Background()
	ag, orch := skillAgent(t, store, dir,
		loadCall("c-alpha", "alpha-skill"), finalTurn(),
		loadCall("c-tooled", "tooled-skill"), finalTurn(),
	)
	_, err := ag.Chat(ctx, "s", "load alpha")
	require.NoError(t, err)
	_, err = ag.Chat(ctx, "s", "load tooled")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"alpha-skill", "tooled-skill"}, activeNames(orch, "s"))
	session := ag.memory.GetOrCreateSessionWithAgent(ctx, "s", ag.config.Name, "")
	require.Contains(t, advertisedNames(ag, session), requiredToolName)

	n, err := ag.RewindSession(ctx, "s", 1)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	session = ag.memory.GetOrCreateSessionWithAgent(ctx, "s", ag.config.Name, "")
	assert.ElementsMatch(t, []string{"alpha-skill"}, activeNames(orch, "s"),
		"the undone turn's skill is still active, or the kept turn's is not")
	assert.NotContains(t, advertisedNames(ag, session), requiredToolName,
		"a tool only the undone skill required is still advertised")
}

// Another agent object holding the session (its own skill orchestrator)
// drops a skill loaded in a turn someone else undid, at its next turn.
func TestARewindTakesTheSkillFromEveryObject(t *testing.T) {
	store, dir, ctx := skillStore(t), writeSkillFixtures(t), context.Background()
	a, orchA := skillAgent(t, store, dir,
		finalTurn(),
		loadCall("c-tooled", "tooled-skill"), finalTurn(),
		finalTurn(),
	)
	b, _ := skillAgent(t, store, dir)
	_, err := a.Chat(ctx, "s", "hello")
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s", "load tooled")
	require.NoError(t, err)
	require.Contains(t, activeNames(orchA, "s"), "tooled-skill")

	_, err = b.RewindSession(ctx, "s", 1)
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s", "next")
	require.NoError(t, err)
	assert.NotContains(t, activeNames(orchA, "s"), "tooled-skill",
		"the object that loaded the skill kept it after another undid the turn")
}

// A skill activated outside the session's history — through the MCP bridge's
// API — has no load row for a reload to re-fire, so a reload leaves it in
// place; only history-backed activations are rebuilt from history.
func TestAnAPIActivatedSkillSurvivesAReload(t *testing.T) {
	store, dir, ctx := skillStore(t), writeSkillFixtures(t), context.Background()
	a, orchA := skillAgent(t, store, dir, finalTurn(), finalTurn())
	b, _ := skillAgent(t, store, dir, finalTurn())
	_, err := a.Chat(ctx, "s", "hello")
	require.NoError(t, err)
	skill, err := orchA.GetLibrary().Load("tooled-skill")
	require.NoError(t, err)
	orchA.ActivateSkill("s", skill, "api", "tooled-skill", 1.0)
	require.Contains(t, activeNames(orchA, "s"), "tooled-skill")

	// Another object takes a turn, so a's copy is stale and reloads.
	_, err = b.Chat(ctx, "s", "from b")
	require.NoError(t, err)
	_, err = a.Chat(ctx, "s", "next")
	require.NoError(t, err)
	assert.Contains(t, activeNames(orchA, "s"), "tooled-skill", "a reload dropped a skill the API activated")
}
