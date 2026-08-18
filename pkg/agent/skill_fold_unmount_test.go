package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/skills"
)

// Fold-unmount contract (HLD §4.5): when a skill's manage_skills load pair is
// folded, deactivateSkillForFold removes the skill's required tools from the
// session's advertised ledger — with refcount semantics computed fresh from the
// remaining active set, and base tools immune. These tests drive the production
// wiring end to end: real library fixtures, real orchestrator activation, the
// real enforceRequiredSkillTools registration, assertions through the
// advertisedTools projection (the array the provider actually receives).

const foldFixtureGamma = `apiVersion: loom/v1
kind: Skill
metadata:
  name: gamma-skill
  title: Gamma Skill
  description: Requires a shared mounted tool and one of its own.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Gamma body.
tools:
  required_tools:
    - shared_x
    - gamma_only
`

const foldFixtureDelta = `apiVersion: loom/v1
kind: Skill
metadata:
  name: delta-skill
  title: Delta Skill
  description: Shares one mounted tool with gamma and requires a base tool.
  domain: general
  risk_level: LOW
trigger:
  mode: MANUAL
prompt:
  instructions: |
    Delta body.
tools:
  required_tools:
    - shared_x
    - base_probe
`

// newFoldRig builds an agent whose base set is {base_probe}, with the two
// fixture skills activatable and their non-base required tools (shared_x,
// gamma_only) registered in the shared registry — the state skill-declared MCP
// mounting produces — then activates both skills for sess and runs the
// production enforceRequiredSkillTools to populate the session ledger.
func newFoldRig(t *testing.T, sess string) *Agent {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gamma-skill.yaml"), []byte(foldFixtureGamma), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "delta-skill.yaml"), []byte(foldFixtureDelta), 0o600))
	lib := skills.NewLibrary(skills.WithSearchPaths(dir))

	a := newAdvertisedToolsAgent(t, "base_probe")
	a.skillOrchestrator = skills.NewOrchestrator(lib)

	// The mounted-tool definitions exist in the registry (what the skill-MCP
	// resolver produces); ledger membership must come from enforce alone.
	a.tools.Register(namedStubTool{name: "shared_x"})
	a.tools.Register(namedStubTool{name: "gamma_only"})

	for _, name := range []string{"gamma-skill", "delta-skill"} {
		sk, err := lib.Load(name)
		require.NoError(t, err)
		require.NotNil(t, a.skillOrchestrator.ActivateSkill(sess, sk, "manual", name, 1.0))
	}
	a.enforceRequiredSkillTools(sess)
	return a
}

func TestFoldUnmount_SharedToolSurvivesOneFold(t *testing.T) {
	const sess = "fold-sess-1"
	a := newFoldRig(t, sess)
	session := &Session{ID: sess}

	assert.ElementsMatch(t, []string{"base_probe", "shared_x", "gamma_only"},
		advertisedNames(a, session), "both skills' tools advertised while active")

	a.deactivateSkillForFold(sess, "gamma-skill")

	assert.ElementsMatch(t, []string{"base_probe", "shared_x"},
		advertisedNames(a, session),
		"gamma's exclusive tool leaves; shared_x survives — delta still requires it")
}

func TestFoldUnmount_ToolGoneWhenLastRequiringSkillFolds(t *testing.T) {
	const sess = "fold-sess-2"
	a := newFoldRig(t, sess)
	session := &Session{ID: sess}

	a.deactivateSkillForFold(sess, "gamma-skill")
	a.deactivateSkillForFold(sess, "delta-skill")

	assert.ElementsMatch(t, []string{"base_probe"}, advertisedNames(a, session),
		"with no requiring skill left, every mounted tool leaves the kernel")
}

func TestFoldUnmount_BaseToolImmune(t *testing.T) {
	const sess = "fold-sess-3"
	a := newFoldRig(t, sess)

	a.deactivateSkillForFold(sess, "delta-skill")
	a.deactivateSkillForFold(sess, "gamma-skill")

	assert.Contains(t, advertisedNames(a, &Session{ID: sess}), "base_probe",
		"a base tool required by a folded skill never leaves its own session")
	assert.Contains(t, advertisedNames(a, &Session{ID: "some-other-session"}), "base_probe",
		"requiring a base tool never scoped it away from other sessions")
}

// Link A of the fold-unmount chain (HLD §4.5), untested until now: a REAL
// releasePressure fold whose region contains a manage_skills load pair must
// fire the skill-deactivation hook with that skill's name and pin the
// folded-skill note onto the summary. (Links B and C — deactivation, ledger
// refcount, advertised-set shrink — are covered by the tests above; the
// chain was also observed live: gateway-opus beat-42 fold dropped the kernel
// 17→12, removing exactly the five mounted TD_ tools.)
func TestReleasePressure_FoldFiresSkillDeactivation(t *testing.T) {
	store, err := NewSessionStore(filepath.Join(t.TempDir(), "s.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	const sessionID = "sess-fold-skill"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-4\nsummary of the old turns"})

	var deactivated []string
	sm.SetSkillDeactivationHook(func(sid, skill string) {
		assert.Equal(t, sessionID, sid, "hook must carry the folding session's id")
		deactivated = append(deactivated, skill)
	})

	persist := func(role, content, toolUseID string, calls []ToolCall, turnStart bool) {
		m := Message{Role: role, Content: content, ToolUseID: toolUseID, ToolCalls: calls}
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
	}

	// Turn 1: the manage_skills load pair the fold must recognize.
	persist("user", "load the skill", "", nil, true)
	persist("assistant", "", "", []ToolCall{{ID: "load-1", Name: "manage_skills",
		Input: map[string]interface{}{"action": "load", "name": "gamma-skill"}}}, false)
	persist("tool", "Skill loaded: gamma-skill", "load-1", nil, false)

	// Compressible bulk (uniform runs tokenize cheaply, so eviction sheds
	// nothing and the pass escalates to FOLD — the escalation this test needs).
	for turn := 2; turn <= 7; turn++ {
		persist("user", fmt.Sprintf("question %d", turn), "", nil, true)
		callID := fmt.Sprintf("c%d", turn)
		persist("assistant", "", "", []ToolCall{{ID: callID, Name: "bulk_scan", Input: map[string]interface{}{}}}, false)
		persist("tool", strings.Repeat("r", 5000), callID, nil, false)
	}

	shed, _, _ := sm.ReleasePressure(ctx, 0)
	require.True(t, shed, "pressure pass must shed")

	require.Equal(t, []string{"gamma-skill"}, deactivated,
		"the fold over the load pair must fire the deactivation hook exactly once")
	assert.Contains(t, sm.summary.text, "[Folded active skill(s): gamma-skill",
		"the summary must pin the folded-skill note — the model's cue to reload")
}
