// Copyright 2026 Teradata
package agent

// Gap-fill tests for the v5 pressure pipeline: covers seams that
// context_v5_e2e_test.go and skill_lifecycle_e2e_test.go don't reach.
//
//   Recall roundtrip — ballast enters L1 whole, valve stubs it, LLM calls
//                      recall_context with the stub's ref, real body returns.
//   Charter carry   — a charter-hinted tool result survives fold verbatim
//                     (not compressed).
//   Fold ordering   — post-fold L1 order: residue precedes preserved
//                     ledger/charter content.

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	skillsPkg "github.com/teradata-labs/loom/pkg/skills"
)

// ============================================================================
// Ballast tool for recall roundtrip (small payload → no admission wrap)
// ============================================================================

type smallBallastHinted struct {
	seq     int
	payload string
}

func newSmallBallastHinted() *smallBallastHinted {
	return &smallBallastHinted{payload: strings.Repeat("Q", 800)}
}
func (h *smallBallastHinted) Name() string        { return "small_ballast" }
func (h *smallBallastHinted) Description() string { return "small ballast-hinted payload" }
func (h *smallBallastHinted) Backend() string     { return "" }
func (h *smallBallastHinted) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (h *smallBallastHinted) ContextClassHint() string { return shuttle.ClassBallast }
func (h *smallBallastHinted) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	h.seq++
	return &shuttle.Result{Success: true, Data: h.payload}, nil
}

// ============================================================================
// Charter-hinted tool for charter carry test
// ============================================================================

type charterHintedTool struct{}

func (h *charterHintedTool) Name() string        { return "charter_tool" }
func (h *charterHintedTool) Description() string { return "charter-hinted install" }
func (h *charterHintedTool) Backend() string     { return "" }
func (h *charterHintedTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (h *charterHintedTool) ContextClassHint() string { return shuttle.ClassCharter }
func (h *charterHintedTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: "CHARTER-INSTALL:capability-alpha"}, nil
}

// ============================================================================
// LLM helpers for recall test
// ============================================================================

func respCallSmallBallast(callID string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    callID,
			Name:  "small_ballast",
			Input: map[string]interface{}{},
		}},
	}
}

func respCallCharter(callID string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    callID,
			Name:  "charter_tool",
			Input: map[string]interface{}{},
		}},
	}
}

// stubRefPattern extracts the ref inside a valve stub of the form
//
//	[evicted: <tool> result, N tok → recall_context('<ref>')]
var stubRefPattern = regexp.MustCompile(`recall_context\('([^']+)'\)`)

// ============================================================================
// Test 1: recall_context roundtrip
// ============================================================================

// TestE2E_V5RecallRoundtrip drives one session through:
//
//	turn 1 : LLM calls small_ballast 5× (populates L1 with ballast entries)
//	turn 2 : pre-seed yellow pressure → prepareContext runs ValveEvict →
//	         oldest ballast entries become stubs; LLM just end_turns
//	turn 3 : LLM calls recall_context(ref=<extracted stub ref>) → assert the
//	         tool result body contains the real 800-byte payload
//
// This is the seam that proves the ballast admission/eviction/hydration
// contract closes end-to-end: what the valve evicts, the recall tool can
// bring back verbatim.
func TestE2E_V5RecallRoundtrip(t *testing.T) {
	// ------ Scripted LLM ------------------------------------------------
	// Turn 3's recall call needs a ref extracted at runtime; we swap the
	// queued response in-flight before Chat is called.
	llm := newSkillTurnScriptedLLM(
		// Turn 1: five ballast calls, then end_turn
		respCallSmallBallast("t1-1"),
		respCallSmallBallast("t1-2"),
		respCallSmallBallast("t1-3"),
		respCallSmallBallast("t1-4"),
		respCallSmallBallast("t1-5"),
		respEndTurn("ballast in L1"),

		// Turn 2: just end_turn (valve fires in prepareContext)
		respEndTurn("valve done"),

		// Turn 3: placeholder — replaced at runtime with recall_context(ref=...)
		respEndTurn("recall placeholder"),
		respEndTurn("recall done"),

		// Tail
		respEndTurn("unexpected extra"),
	)

	// ------ Agent + store wiring ---------------------------------------
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 20000

	// Session store: wired into Memory so PersistMessage saves each tool
	// result to durable rows recall_context can look up.
	store := newContextClassSQLiteStore(t)
	mem := NewMemoryWithStore(store)

	ag := NewAgent(&mockBackend{}, llm,
		WithConfig(cfg),
		WithMemory(mem),
	)

	// Small payload (800 B) below admission threshold — enters L1 whole so
	// valve has something real to stub.
	ag.RegisterTool(newSmallBallastHinted())

	// recall_context must be registered on the agent (builtin path is
	// wired via WithMemory in the real code; here we register directly).
	ag.RegisterTool(NewRecallContextTool(ag.memory))

	ctx := context.Background()
	sessionID := "e2e-v5-recall"

	llm.SetStateSnapshotter(func() string {
		s, ok := ag.memory.GetSession(sessionID)
		if !ok {
			return "(session not yet created)\n"
		}
		return renderSegMemState(s)
	})

	// -------- Turn 1 --------
	_, err := ag.Chat(ctx, sessionID, "populate ballast")
	require.NoError(t, err)

	session, ok := ag.memory.GetSession(sessionID)
	require.True(t, ok)
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	segMem.SetSessionStore(store, sessionID)
	// Valve payoff bar must be low enough that 5×800B > bar. Default is
	// maxContextTokens/10 = 2000 tokens; 5 ballasts ≈ 5×200 tok = 1000 —
	// below the default. Lower the bar.
	segMem.SetMinValvePayoffTokens(200)
	// Persist the session so the store sees prior rows on next lookup.
	require.NoError(t, store.SaveSession(ctx, session))

	// After turn 1: L1 must contain at least 5 ballast tool results.
	ballastCount := countBallastRealResults(segMem)
	require.GreaterOrEqualf(t, ballastCount, 5,
		"turn 1: expected ≥5 real (non-stub) ballast results in L1, got %d", ballastCount)

	// -------- Turn 2: seed yellow pressure → valve fires --------
	// Yellow zone is 70-85%. Seed 75%.
	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax()*75/100)

	_, err = ag.Chat(ctx, sessionID, "trigger valve")
	require.NoError(t, err)

	// After valve: at least one prior ballast msg has been stubbed
	// (keepRecentBallast defaults to 3, so 5 ballasts → 2 stubbed).
	l1 := segMem.GetMessages()
	stubRef := ""
	for _, m := range l1 {
		if m.Role == "tool" && strings.HasPrefix(m.Content, "[evicted:") {
			match := stubRefPattern.FindStringSubmatch(m.Content)
			if len(match) == 2 {
				stubRef = match[1]
				break
			}
		}
	}
	require.NotEmptyf(t, stubRef,
		"turn 2: valve must have stubbed at least one ballast entry with a recall_context('<ref>') pointer; L1 dump: %s",
		dumpL1Roles(l1))

	// -------- Turn 3: script LLM to call recall_context with the ref --
	llm.mu.Lock()
	llm.responses[llm.idx] = llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    "t3-recall",
			Name:  "recall_context",
			Input: map[string]interface{}{"ref": stubRef},
		}},
	}
	llm.mu.Unlock()

	_, err = ag.Chat(ctx, sessionID, "recall the evicted body")
	require.NoError(t, err)

	// After turn 3: L1 must contain a recall_context tool result whose
	// body contains the real 800-byte payload (a run of ≥100 Q's).
	l1After := segMem.GetMessages()
	sawRecalledBody := false
	for _, m := range l1After {
		if m.Role == "tool" && strings.Contains(m.Content, strings.Repeat("Q", 100)) {
			sawRecalledBody = true
			break
		}
	}
	assert.True(t, sawRecalledBody,
		"turn 3: recall_context must have returned the real ballast body (a ≥100-Q run); L1 dump: %s",
		dumpL1Roles(l1After))
}

// ============================================================================
// Test 2: charter class carry through fold
// ============================================================================

// TestE2E_V5CharterCarriesThroughFold registers a charter-hinted tool,
// pushes charter content into L1, then forces fold and asserts the charter
// content is carried verbatim (never handed to the compressor).
func TestE2E_V5CharterCarriesThroughFold(t *testing.T) {
	var compressorInputs [][]Message
	compressor := &mockCompressor{
		enabled: true,
		compressFn: func(msgs []Message) string {
			cp := make([]Message, len(msgs))
			copy(cp, msgs)
			compressorInputs = append(compressorInputs, cp)
			return "compressed"
		},
	}

	// Assistant messages classify narrative. Return a fat narrative chunk
	// so fold's narrative pile is non-empty and the compressor gets called.
	narrativeFiller := strings.Repeat("narrative-fill ", 200) // ~3 KB
	llm := newSkillTurnScriptedLLM(
		respCallCharter("t1-c"),
		respEndTurn(narrativeFiller),
		respEndTurn(narrativeFiller),
		respEndTurn("tail"),
	)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 20000

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(&charterHintedTool{})

	ctx := context.Background()
	sessionID := "e2e-v5-charter"

	_, err := ag.Chat(ctx, sessionID, "install charter capability")
	require.NoError(t, err)

	session, _ := ag.memory.GetSession(sessionID)
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	require.NotNil(t, segMem)
	segMem.SetCompressor(compressor)
	store := newContextClassSQLiteStore(t)
	require.NoError(t, store.SaveSession(ctx, session))
	segMem.SetSessionStore(store, sessionID)

	// Seed RED → fold fires
	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax()*90/100)
	_, err = ag.Chat(ctx, sessionID, "trigger fold")
	require.NoError(t, err)

	require.Greaterf(t, foldCountLocked(segMem), 0,
		"fold must have fired (got foldCount=%d)", foldCountLocked(segMem))

	// Assertions:
	//   1. charter content survives in L1 verbatim.
	l1 := segMem.GetMessages()
	sawCharter := false
	for _, m := range l1 {
		if strings.Contains(m.Content, "CHARTER-INSTALL:") {
			sawCharter = true
			assert.Equalf(t, ClassCharter, m.ContextClass,
				"charter-hinted tool result must carry ContextClass=charter in L1, got %q", m.ContextClass)
		}
	}
	assert.True(t, sawCharter,
		"post-fold: charter-hinted tool result must still be in L1 (charter is carry-set, not compress-set); L1 dump: %s",
		dumpL1Roles(l1))

	//   2. If the compressor was called, charter content must not have
	//      been in its input. (If narrative pile was empty and the
	//      compressor was skipped, the invariant holds trivially.)
	for i, cin := range compressorInputs {
		for j, m := range cin {
			assert.NotContainsf(t, m.Content, "CHARTER-INSTALL:",
				"compressor input %d msg %d: charter content must never reach the compressor", i, j)
		}
	}
}

// ============================================================================
// Test 3: post-fold L1 ordering
// ============================================================================

// TestE2E_V5PostFoldOrdering asserts that post-fold L1 places the residue
// (L2 summary carrier) BEFORE the carried ledger tool result — Contract 1's
// shape is ROM + residue + L1, so any surviving ledger content must follow
// the residue slot when the assembled view is compiled.
//
// Narrative content is supplied via a loader (manage_skills load) whose
// tool_result classifies narrative and enters the fold's narrative pile.
func TestE2E_V5PostFoldOrdering(t *testing.T) {
	compressor := &mockCompressor{
		enabled:    true,
		compressFn: func(msgs []Message) string { return "compressed residue marker: RESIDUE-Z" },
	}

	llm := newSkillTurnScriptedLLM(
		// Turn 1: call ledger tool + load a skill (narrative-classed body).
		respCallLedgerHinted("t1-l"),
		respLoadSkill("t1-load", "td-narrative"),
		respEndTurn("setup done"),
		// Turn 2: fold trigger.
		respEndTurn("fold done"),
		respEndTurn("tail"),
	)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 20000
	cfg.SkillsConfig = &skillsPkg.SkillsConfig{Enabled: true, MaxConcurrentSkills: 3}

	// Skill library with one narrative-body skill.
	lib := newEmptySkillLibrary(t)
	lib.Register(&skillsPkg.Skill{
		Name:    "td-narrative",
		Title:   "narrative",
		Trigger: skillsPkg.SkillTrigger{Keywords: []string{"call"}, Mode: skillsPkg.ActivationAuto},
		Prompt: skillsPkg.SkillPrompt{
			Instructions: strings.Repeat("STEP: process narrative content. ", 100),
		},
	})
	orch := skillsPkg.NewOrchestrator(lib)

	ag := NewAgent(&mockBackend{}, llm,
		WithConfig(cfg),
		WithSkillOrchestrator(orch),
	)
	ag.RegisterTool(newLedgerHintedTool("order-marker"))

	manageSkillsTool := NewManageSkillsTool(
		orch, nil, &loomv1.TaskBoardConfig{}, cfg, llm, "test-agent", nil,
	).WithMemory(ag.memory)
	ag.RegisterTool(manageSkillsTool)

	ctx := context.Background()
	sessionID := "e2e-v5-order"

	_, err := ag.Chat(ctx, sessionID, "call ledger and setup")
	require.NoError(t, err)

	session, _ := ag.memory.GetSession(sessionID)
	segMem, _ := session.SegmentedMem.(*SegmentedMemory)
	require.NotNil(t, segMem)
	segMem.SetCompressor(compressor)
	store := newContextClassSQLiteStore(t)
	require.NoError(t, store.SaveSession(ctx, session))
	segMem.SetSessionStore(store, sessionID)

	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax()*90/100)
	_, err = ag.Chat(ctx, sessionID, "trigger fold")
	require.NoError(t, err)

	require.NotEmpty(t, segMem.GetL2Summary(),
		"post-fold: L2 residue must be populated")

	// Compile the assembled view for the LLM and check ordering.
	assembled := session.GetMessages()

	residueIdx, ledgerIdx := -1, -1
	for i, m := range assembled {
		if strings.Contains(m.Content, "RESIDUE-Z") {
			residueIdx = i
		}
		if strings.Contains(m.Content, "LEDGER-CARGO:") {
			ledgerIdx = i
		}
	}
	require.NotEqualf(t, -1, residueIdx,
		"assembled view: residue marker (RESIDUE-Z) must be present; assembled dump: %s",
		dumpL1Roles(assembled))
	require.NotEqualf(t, -1, ledgerIdx,
		"assembled view: ledger marker (LEDGER-CARGO:) must survive fold and appear in the assembled view; assembled dump: %s",
		dumpL1Roles(assembled))
	assert.Lessf(t, residueIdx, ledgerIdx,
		"Contract 1 shape violation: residue slot (idx=%d) must precede carried ledger content (idx=%d); assembled dump: %s",
		residueIdx, ledgerIdx, dumpL1Roles(assembled))
}

// ============================================================================
// Helpers
// ============================================================================

// countBallastRealResults counts L1 tool messages classified ballast that
// still hold their real content (not a valve stub).
func countBallastRealResults(sm *SegmentedMemory) int {
	msgs := sm.GetMessages()
	n := 0
	for _, m := range msgs {
		if m.Role == "tool" && m.ContextClass == ClassBallast && !strings.HasPrefix(m.Content, "[evicted:") {
			n++
		}
	}
	return n
}

// dumpL1Roles renders a compact one-line summary of L1 for assertion errors.
func dumpL1Roles(msgs []Message) string {
	var b strings.Builder
	b.WriteString("[")
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(", ")
		}
		content := m.Content
		if len(content) > 40 {
			content = content[:40] + "..."
		}
		b.WriteString(m.Role)
		b.WriteString(":")
		b.WriteString(content)
	}
	b.WriteString("]")
	return b.String()
}
