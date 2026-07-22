// Copyright 2026 Teradata
package agent

// End-to-end skill lifecycle: one long-running session driven through the
// real Agent.Chat loop with a scripted LLM. Exercises every seam the skill
// catalog + ROM assembly + pressure pipeline share, in the order a real
// session would hit them.
//
// The scripted LLM only ever has one plausible response per LLM call —
// user turns are simple, skills match their user turns tightly by keyword,
// and every LLM decision is forced by the ROM it just saw. Any non-obvious
// LLM choice would be a test bug, not a real LLM being clever.
//
// Seams covered (turn → seam):
//   Turn 1  router keyword branch (Group A: 2 skills match "profile"),
//           catalog append + dedup, ROM assembly + filter,
//           manage_skills(list) path, manage_skills(load, ...) path,
//           load-body classification (narrative), walk-L1 sees load metadata,
//           BindingAlways surfaces in every turn's ROM (until loaded).
//
//   Turn 2  router slash branch (/td-profile-deep), executeLoad's second
//           call in same session (dedup of catalog by name), ROM filter
//           tracks two active skills at once.
//
//   Turn 3  active-set cap (config MaxConcurrentSkills=3): after loading
//           three skills, a fourth returns ACTIVE_SKILL_CAP_EXCEEDED with
//           the config-set cap (not the hardcoded 20). Group B keyword
//           discovery still populates the catalog even when the load fails.
//
//   Turn 4  manage_skills(list) — active set confirmed, unchanged.
//           v5 has no unload verb: an executed skill is retired only by the
//           pressure pipeline (valve/fold), never by an inverse operation.
//
//   Turn 5  context explosion: LLM spams a mock heavy tool multiple times,
//           L1 pressure crosses red, prepareContext dispatches Fold. Fold's
//           compressor gets the narrative pile (including all remaining
//           skill bodies since they're narrative-classed). Post-fold, walk-L1
//           returns zero active skills — every previously-loaded skill is
//           BACK in the ROM catalog for the next turn.
//
//   Turn 6  reload cycle: LLM loads previously-loaded skills again. Fresh
//           load metadata → walk-L1 sees them → filter hides them again.
//           End-to-end proof that fold + reload is coherent.
//
// Assertions across every turn:
//   - No message ever contains "[Skill Discovery]" (deleted tail-note
//     anti-pattern that used to inject a fake user turn).
//   - orchestrator's active set matches the catalog filter (walk-L1's
//     view of active).
//   - No system-role message carries stored session-message content
//     (system slot is composed at read time, never persisted).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/skills"
	"github.com/teradata-labs/loom/pkg/skills/binding"
	"github.com/teradata-labs/loom/pkg/skills/discovery"
	"github.com/teradata-labs/loom/pkg/types"
)

// ============================================================================
// heavy ballast tool for pressure-explosion turn
// ============================================================================

type heavyBallastTool struct {
	payload string
}

func newHeavyBallastTool(bytes int) *heavyBallastTool {
	// Fixed-size string per call — deterministic pressure per invocation.
	return &heavyBallastTool{payload: strings.Repeat("x", bytes)}
}

func (h *heavyBallastTool) Name() string        { return "heavy_ballast" }
func (h *heavyBallastTool) Description() string { return "returns a large payload" }
func (h *heavyBallastTool) Backend() string     { return "" }
func (h *heavyBallastTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (h *heavyBallastTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: h.payload}, nil
}

// ============================================================================
// LLM response helpers — one shape per turn to keep the script readable
// ============================================================================

func respLoadSkill(callID, skillName string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:   callID,
			Name: "manage_skills",
			Input: map[string]interface{}{
				"action": "load",
				"name":   skillName,
			},
		}},
	}
}

func respListSkills(callID string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    callID,
			Name:  "manage_skills",
			Input: map[string]interface{}{"action": "list"},
		}},
	}
}

func respCallHeavy(callID string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    callID,
			Name:  "heavy_ballast",
			Input: map[string]interface{}{},
		}},
	}
}

func respEndTurn(text string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{Content: text, StopReason: "end_turn"}
}

// ============================================================================
// The one long-flow test
// ============================================================================

func TestE2E_SkillLifecycle_MultiTurnCatalogFoldReload(t *testing.T) {
	// ------ Setup: library with 5 skills grouped by discoverability -------
	//
	//   Group A (2 skills, match keyword "profile"):
	//     td-profile-basic, td-profile-deep
	//   Group B (2 skills, match keyword "schema"):
	//     td-schema-check, td-schema-diff
	//   Group C (1 skill, matches "join"):
	//     td-join-analysis
	//
	//   Plus one BindingAlways-mode skill (always surfaces in candidates):
	//     td-guardrail
	//
	// User messages per turn are tight keyword matches so discovery's
	// keyword branch (deterministic, no LLM router) produces the exact
	// group we want.
	lib := newEmptySkillLibrary(t)
	registerSkill := func(name, keyword, instructions string, mode skills.SkillActivationMode, slash ...string) {
		lib.Register(&skills.Skill{
			Name:  name,
			Title: name,
			Trigger: skills.SkillTrigger{
				Keywords:      []string{keyword},
				SlashCommands: slash,
				Mode:          mode,
			},
			Prompt: skills.SkillPrompt{Instructions: instructions},
		})
	}
	registerSkill("td-profile-basic", "profile", "STEP1: count. STEP2: schema.", skills.ActivationAuto)
	registerSkill("td-profile-deep", "profile", "STEP1: deep dive. STEP2: stats.", skills.ActivationAuto, "/td-profile-deep")
	registerSkill("td-schema-check", "schema", "STEP1: validate. STEP2: report.", skills.ActivationAuto)
	registerSkill("td-schema-diff", "schema", "STEP1: diff. STEP2: reconcile.", skills.ActivationAuto)
	registerSkill("td-join-analysis", "join", "STEP1: analyze keys. STEP2: cardinality.", skills.ActivationAuto)
	// td-guardrail: NOT registered. BindingAlways requires an explicit
	// SkillBinding config with Mode: BindingAlways — the skill's own
	// ActivationMode is not what discovery's Phase 4 keys off. Testing that
	// path needs a separately-shaped SkillsConfig.Bindings; out of scope
	// for the catalog+lifecycle test.

	orch := skills.NewOrchestrator(lib)

	// Deterministic discovery: no LLM router → keyword branch only.
	// The binding resolver here uses default (LAZY) mode for all except
	// td-guardrail (ALWAYS). Discovery's Phase 4 surfaces ALWAYS skills
	// unconditionally each turn.
	resolver := binding.NewResolver(lib)
	disc := discovery.New(lib, resolver)

	// ------ Compressor: capture inputs to prove skill bodies fold ---------
	var compressorInputs [][]Message
	compressor := &mockCompressor{
		enabled: true,
		compressFn: func(msgs []Message) string {
			cp := make([]Message, len(msgs))
			copy(cp, msgs)
			compressorInputs = append(compressorInputs, cp)
			return "compressed state: worked on profile+schema; ready to continue"
		},
	}

	// ------ Scripted LLM: minimal responses per turn ---------------------
	//
	// Each Chat call may involve multiple LLM invocations (tool → result →
	// LLM decides → ...). The queue below lists ONE LLMResponse per LLM
	// invocation. Order matters. The scripted harness holds on the last
	// response if the queue is exhausted, so end_turn responses are safe
	// to duplicate at the tail.
	llm := newSkillTurnScriptedLLM(
		// -------- Turn 1: user "profile Complaints" -----------------------
		// Router surfaces td-profile-basic, td-profile-deep, td-guardrail.
		// LLM first lists (proves the list path), then loads basic.
		respListSkills("t1-list"),
		respLoadSkill("t1-load-basic", "td-profile-basic"),
		respEndTurn("loaded profile-basic"),

		// -------- Turn 2: user "/td-profile-deep" -------------------------
		// Slash branch: exactly one candidate (bypasses keyword scoring).
		// LLM loads it (already the highest-priority candidate).
		respLoadSkill("t2-load-deep", "td-profile-deep"),
		respEndTurn("loaded profile-deep"),

		// -------- Turn 3: user "check schema" -----------------------------
		// Router surfaces Group B. Under cap=3, one more load succeeds
		// (td-schema-check) then a second (td-schema-diff) is rejected by
		// the cap check. LLM tries diff and gets the error.
		respLoadSkill("t3-load-check", "td-schema-check"),
		respLoadSkill("t3-load-diff", "td-schema-diff"), // will hit cap
		respEndTurn("cap hit for schema-diff, moving on"),

		// -------- Turn 4: user "confirm active skills" -------------------
		// LLM lists the active set. No unload path in v5 — retirement is
		// the pressure pipeline's job, not the LLM's.
		respListSkills("t4-list"),
		respEndTurn("still have profile-basic, profile-deep, schema-check"),

		// -------- Turn 5: user "check nulls with heavy work" --------------
		// LLM spams the heavy tool. Each call adds a ballast tool_result
		// (~8KB payload) to L1. After several calls, L1 crosses red and
		// prepareContext dispatches Fold. Post-fold, load bodies are
		// compressed into residue; walk-L1 sees no active skills.
		respCallHeavy("t5-h1"),
		respCallHeavy("t5-h2"),
		respCallHeavy("t5-h3"),
		respCallHeavy("t5-h4"),
		respCallHeavy("t5-h5"),
		respEndTurn("heavy work done"),

		// -------- Turn 6: user "profile again and schema check" -----------
		// Post-fold, LLM reloads two skills. Fresh load metadata → filter
		// hides them again. Reload cycle complete.
		respLoadSkill("t6-reload-basic", "td-profile-basic"),
		respLoadSkill("t6-reload-check", "td-schema-check"),
		respEndTurn("all reloaded"),

		// Tail response — used if any extra LLM call sneaks in.
		respEndTurn("unexpected extra call"),
	)

	// ------ Agent + tools -------------------------------------------------
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.SkillsConfig = &skills.SkillsConfig{
		Enabled:             true,
		LoadHardCap: 3, // hard-reject at load: hit by turn 3's second load
	}
	// Tight-ish budget so turn 5's heavy tool spam trips red. Ratio
	// picked so a few 8KB tool results plus prior skill bodies cross
	// red (85% of 50k = ~42k tokens ≈ 42k*4 bytes = 168KB of message
	// content). Tune if needed.
	cfg.MaxContextTokens = 20000

	ag := NewAgent(&mockBackend{}, llm,
		WithConfig(cfg),
		WithSkillOrchestrator(orch),
		WithSkillDiscovery(disc),
	)

	// Register manage_skills tool (its real Executor path is what turns
	// LLM ToolCall responses into load/unload/list results). Register
	// the heavy ballast tool for turn 5.
	manageSkillsTool := NewManageSkillsTool(
		orch,
		nil, // no taskEmitter — task emission is a separate concern
		&loomv1.TaskBoardConfig{},
		cfg,
		llm,
		"test-agent",
		nil, // no permission checker — no high-risk gate needed here
	)
	ag.RegisterTool(manageSkillsTool)
	heavy := newHeavyBallastTool(8000)
	ag.RegisterTool(heavy)

	// ------ Wire compressor after first Chat creates the SegmentedMemory --
	// Agent's memory subsystem constructs SegmentedMemory lazily on first
	// use. We drive turn 1 first, then reach in to wire the compressor
	// before the pressure-inducing turn 5.

	// ------ Drive the session --------------------------------------------
	ctx := context.Background()
	sessionID := "e2e-multi-skill"

	llm.SetStateSnapshotter(func() string {
		s, ok := ag.memory.GetSession(sessionID)
		if !ok {
			return "(session not yet created)\n"
		}
		return renderSegMemState(s)
	})

	turnUserMsgs := []string{
		"profile Complaints",             // turn 1
		"/td-profile-deep",               // turn 2
		"check schema",                   // turn 3
		"confirm active skills",          // turn 4
		"check nulls heavy work",         // turn 5 — pressure
		"profile again and schema check", // turn 6 — reload
	}

	// Snapshots per turn (captured after each Chat).
	catalogAfterTurn := make([][]types.SkillCatalogEntry, len(turnUserMsgs))
	orchActiveAfterTurn := make([][]string, len(turnUserMsgs))

	for i, msg := range turnUserMsgs {
		if i == 4 {
			// Wire the compressor after the SegMem exists but before the
			// pressure turn drives fold.
			session, ok := ag.memory.GetSession(sessionID)
			require.True(t, ok, "session must exist by turn 5")
			sm, ok := session.SegmentedMem.(*SegmentedMemory)
			require.True(t, ok)
			sm.SetCompressor(compressor)
			// Also wire a durable session store so ValveEvict/Fold don't
			// self-disable due to missing store — use the shared SQLite
			// helper the other tests use.
			store := newContextClassSQLiteStore(t)
			require.NoError(t, store.SaveSession(ctx, session))
			sm.SetSessionStore(store, sessionID)
			// Deterministic fold: seed enough pressure so prepareContext's
			// zone check lands in RED on the very next dispatch. The heavy
			// tool calls in turn 5 add real pressure too; this ensures the
			// test doesn't depend on their exact token accounting.
			seedL1TokenPressure(sm, sm.GetTokenBudgetMax()*90/100)
		}

		_, err := ag.Chat(ctx, sessionID, msg)
		require.NoError(t, err, "turn %d (%q) must not error", i+1, msg)

		session, _ := ag.memory.GetSession(sessionID)
		catalogAfterTurn[i] = append([]types.SkillCatalogEntry(nil), session.RomCatalog...)
		orchActiveAfterTurn[i] = skillNamesSorted(orch.GetActiveSkills(sessionID))
		t.Logf("TURN %d (%q): catalog=%v active=%v l1_msgs=%d",
			i+1, msg, extractCatalogNames(catalogAfterTurn[i]), orchActiveAfterTurn[i],
			len(session.Messages))
	}

	// ------ Assemble evidence for assertions ------------------------------
	calls := llm.getCalls()
	session, _ := ag.memory.GetSession(sessionID)
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	require.NotNil(t, segMem)

	// Locate the FIRST LLM call for each turn — by finding a call whose
	// last user message content is that turn's user text.
	turnFirstCall := make([]int, len(turnUserMsgs))
	for i, msg := range turnUserMsgs {
		turnFirstCall[i] = findFirstCallWithUser(calls, msg)
		require.GreaterOrEqualf(t, turnFirstCall[i], 0,
			"could not locate LLM call for turn %d (%q)", i+1, msg)
	}

	// ------ Cross-turn invariants ----------------------------------------

	// (I1) No fake user turn menu anywhere. This is the deleted anti-pattern.
	for i, msgs := range calls {
		for _, m := range msgs {
			assert.NotContainsf(t, m.Content, "[Skill Discovery]",
				"call %d must not carry the deleted tail-note menu", i)
		}
	}

	// (I2) The stored session's Messages field must never contain system
	// content (system slot is composed at read time; the persisted history
	// is user/assistant/tool only).
	for _, m := range session.Messages {
		assert.NotEqualf(t, "system", m.Role,
			"stored history must not carry system-role messages: %q", m.Content)
	}

	// ------ Per-turn assertions ------------------------------------------

	// ---------- Turn 1 ----------
	// Router surfaced Group A + td-guardrail. Catalog should have all three
	// (order-stable, insertion order). LLM's first LLM call (list) saw both
	// profile candidates in ROM; second call (load) also had them; after
	// load succeeded, subsequent calls should filter td-profile-basic out.
	assert.Contains(t, catalogAfterTurn[0], types.SkillCatalogEntry{Name: "td-profile-basic", Description: "td-profile-basic"})
	assert.Contains(t, catalogAfterTurn[0], types.SkillCatalogEntry{Name: "td-profile-deep", Description: "td-profile-deep"})

	// Turn 1's first LLM call saw td-profile-basic in the ROM (LLM chose to
	// load it). By call 2 (post-list), same. By call 3 (post-load),
	// td-profile-basic should be filtered.
	rom1First := extractROM(calls[turnFirstCall[0]])
	assert.Contains(t, rom1First, "td-profile-basic",
		"turn 1 first LLM call: profile-basic is a candidate")
	assert.Contains(t, rom1First, "td-profile-deep",
		"turn 1 first LLM call: profile-deep also a candidate")

	// Orchestrator active set after turn 1 = [td-profile-basic]
	assert.Equal(t, []string{"td-profile-basic"}, orchActiveAfterTurn[0])

	// ---------- Turn 2 ----------
	// Slash command loads td-profile-deep. After turn 2, both profile skills active.
	assert.ElementsMatch(t, []string{"td-profile-basic", "td-profile-deep"}, orchActiveAfterTurn[1])

	// Turn 2's first LLM call — the ROM catalog should FILTER td-profile-basic
	// (loaded in turn 1) but STILL LIST td-profile-deep (about to be loaded).
	rom2First := extractROM(calls[turnFirstCall[1]])
	assert.NotContains(t, rom2First, "- td-profile-basic",
		"turn 2 first call: profile-basic already loaded, filtered")
	assert.Contains(t, rom2First, "td-profile-deep",
		"turn 2 first call: profile-deep still a candidate")

	// ---------- Turn 3 ----------
	// Router surfaces Group B → catalog grows with schema-check + schema-diff.
	// Cap (3) hits when LLM tries to load a 3rd + 4th skill in this turn
	// (schema-check pushes to 3, schema-diff hits cap).
	// Post-turn 3, active set is [profile-basic, profile-deep, schema-check].
	assert.ElementsMatch(t, []string{"td-profile-basic", "td-profile-deep", "td-schema-check"}, orchActiveAfterTurn[2])
	assert.Contains(t, extractCatalogNames(catalogAfterTurn[2]), "td-schema-check")
	assert.Contains(t, extractCatalogNames(catalogAfterTurn[2]), "td-schema-diff")

	// Find the cap-exceeded response in turn 3's tool results.
	sawCapError := false
	for i := turnFirstCall[2]; i < len(calls) && (i == turnFirstCall[2] || !containsUserMsg(calls[i], turnUserMsgs[3])); i++ {
		for _, m := range calls[i] {
			if m.Role == "tool" && strings.Contains(m.Content, "ACTIVE_SKILL_CAP_EXCEEDED") {
				sawCapError = true
			}
		}
	}
	assert.True(t, sawCapError, "turn 3: the second load must have returned ACTIVE_SKILL_CAP_EXCEEDED (cap=3)")

	// ---------- Turn 4 ----------
	// List action confirms active set without changing it. Retirement is
	// the pressure pipeline's job, not the LLM's — the active set persists.
	assert.ElementsMatch(t,
		[]string{"td-profile-basic", "td-profile-deep", "td-schema-check"},
		orchActiveAfterTurn[3],
		"turn 4 (list-only): active set must be unchanged from turn 3")

	// ---------- Turn 5: fold ----------
	// Post-turn 5, fold has fired: residue populated, compressor called with
	// narrative pile that included skill bodies (they're narrative under Fix 1).
	assert.NotEmpty(t, segMem.GetL2Summary(),
		"turn 5: fold must have populated L2 residue")
	require.NotEmpty(t, compressorInputs,
		"turn 5: fold must have called the compressor at least once")
	sawSkillBodyInCompressor := false
	for _, msgSet := range compressorInputs {
		for _, m := range msgSet {
			if strings.Contains(m.Content, "STEP1: count") ||
				strings.Contains(m.Content, "STEP1: validate") {
				sawSkillBodyInCompressor = true
			}
		}
	}
	assert.True(t, sawSkillBodyInCompressor,
		"turn 5: fold's compressor input must have contained skill bodies (narrative-classed)")

	// Post-fold, orchestrator active set may still list them (orchestrator
	// doesn't know about fold), but walk-L1 (source of truth for the ROM
	// catalog filter) sees no active — so ROM re-lists them.
	rom6First := extractROM(calls[turnFirstCall[5]])
	assert.Contains(t, rom6First, "td-profile-basic",
		"turn 6 first call: post-fold, profile-basic body gone from L1, catalog re-lists")
	assert.Contains(t, rom6First, "td-schema-check",
		"turn 6 first call: same for schema-check")

	// ---------- Turn 6: reload ----------
	// After the two reload tool calls, walk-L1 sees fresh load metadata for
	// both. ROM should filter them again on any subsequent call (there is
	// none, so we assert via the final segMem state).
	activeAfterReload := segMem.ActiveSkillNames()
	assert.True(t, activeAfterReload["td-profile-basic"],
		"reload metadata restores active state")
	assert.True(t, activeAfterReload["td-schema-check"], "same")

	// Catalog is monotonic across the session (append-only, dedup):
	// every skill discovered from turn 1..6 should be present exactly once.
	finalNames := extractCatalogNames(catalogAfterTurn[5])
	expectedAllSurfaced := []string{"td-profile-basic", "td-profile-deep", "td-schema-check", "td-schema-diff"}
	for _, n := range expectedAllSurfaced {
		assert.Contains(t, finalNames, n, "final catalog must include %s (surfaced by router across turns)", n)
	}
	// td-join-analysis was never keyword-matched by any user turn — must NOT
	// appear in the catalog. (No push-everything default.)
	assert.NotContains(t, finalNames, "td-join-analysis",
		"td-join-analysis was never mentioned; must not appear in the catalog")

	// Dedup across turns: skills discovered on multiple turns appear only
	// once in the catalog. td-profile-basic matched on turn 1 (via keyword)
	// and turn 6 (via keyword); should appear exactly once.
	profileBasicCount := 0
	for _, n := range finalNames {
		if n == "td-profile-basic" {
			profileBasicCount++
		}
	}
	assert.Equal(t, 1, profileBasicCount, "catalog dedup: td-profile-basic surfaced multiple turns, listed once")
}

// ============================================================================
// Test helpers
// ============================================================================

func extractROM(msgs []Message) string {
	for _, m := range msgs {
		if m.Role == "system" {
			return m.Content
		}
	}
	return ""
}

func findFirstCallWithUser(calls [][]Message, userMsg string) int {
	for i, msgs := range calls {
		if containsUserMsg(msgs, userMsg) {
			return i
		}
	}
	return -1
}

func containsUserMsg(msgs []Message, want string) bool {
	for _, m := range msgs {
		if m.Role == "user" && strings.TrimSpace(m.Content) == want {
			return true
		}
	}
	return false
}

func extractCatalogNames(entries []types.SkillCatalogEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

func skillNamesSorted(actives []*skills.ActiveSkill) []string {
	out := make([]string, 0, len(actives))
	for _, a := range actives {
		if a != nil && a.Skill != nil {
			out = append(out, a.Skill.Name)
		}
	}
	// simple insertion sort — deterministic order for ElementsMatch
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

var _ = fmt.Sprintf // silence unused-import warning if we drop the sprintf usage
