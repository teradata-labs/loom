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
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
)

// baseProbeTool stands in for a tool the agent registers at construction — a
// base tool every session is entitled to see, and one a skill also declares as
// required.
type baseProbeTool struct{}

func (b *baseProbeTool) Name() string        { return "base_probe" }
func (b *baseProbeTool) Description() string { return "a base tool every session advertises" }
func (b *baseProbeTool) Backend() string     { return "" }
func (b *baseProbeTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (b *baseProbeTool) Execute(_ context.Context, _ map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "probe ok"}, nil
}

// buildBaseToolAgent constructs an agent over the shared store whose BASE tool
// set includes base_probe, with the fixture skill library wired.
func buildBaseToolAgent(t *testing.T, llm LLMProvider, store *SessionStore, skillsDir string) *Agent {
	t.Helper()

	lib := skills.NewLibrary(skills.WithSearchPaths(skillsDir))
	orch := skills.NewOrchestrator(lib)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.Enabled = false
	cfg.PatternConfig.UseLLMClassifier = false

	a := NewAgent(&mockBackend{}, llm,
		WithConfig(cfg),
		WithSkillOrchestrator(orch),
		WithMemory(NewMemoryWithStore(store)),
		WithoutSelfCorrection(),
	)
	// Registered AFTER construction and BEFORE any traffic — the way embedders
	// (e.g. loom-cloud) wire their own tools. It is a base tool: every session
	// is entitled to see it.
	a.tools.Register(&baseProbeTool{})
	return a
}

// TestRestore_BaseToolStaysVisibleToOtherSessions pins the invariant stated at
// registerSessionTool: requiring a tool for one session never hides it from
// another. The dangerous ordering is a FRESH process whose first action is
// restoring a session — the restore re-fires that session's skill, which
// registers the skill's required tools. If the base set were captured lazily at
// the top of the conversation loop, it would still be empty at that moment and
// a base tool would be marked session-scoped for the process's lifetime.
func TestRestore_BaseToolStaysVisibleToOtherSessions(t *testing.T) {
	ctx := context.Background()
	skillsDir := writeSkillFixtures(t)

	store, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	const restoredID = "sess-restored"
	const otherID = "sess-other"

	// --- live process: a session loads the skill that requires the base tool ---
	liveLLM := &mockToolCallingLLM{responses: []mockLLMResponse{
		loadCall("c-base", "base-tooled-skill"),
		finalTurn(),
	}}
	live := buildBaseToolAgent(t, liveLLM, store, skillsDir)
	_, err = live.Chat(ctx, restoredID, "load the base tooled skill")
	require.NoError(t, err)

	// --- restart: a fresh agent over the same store, whose FIRST action is a
	// restore of that session (the re-fire runs inside GetOrCreateSession) ---
	restoredLLM := &mockToolCallingLLM{}
	restored := buildBaseToolAgent(t, restoredLLM, store, skillsDir)

	session := restored.memory.GetOrCreateSessionWithAgent(ctx, restoredID, restored.config.Name, "")
	require.NotNil(t, session, "the session is restored from the durable store")

	// A different session on that same agent must still advertise the base tool.
	otherSession := restored.memory.GetOrCreateSessionWithAgent(ctx, otherID, restored.config.Name, "")
	require.NotNil(t, otherSession)
	assert.Contains(t, advertisedNames(restored, otherSession), "base_probe",
		"a base tool required by another session's restored skill stays visible to every session")

	// And the restored session sees it too.
	assert.Contains(t, advertisedNames(restored, session), "base_probe",
		"the restoring session advertises the base tool as well")
}
