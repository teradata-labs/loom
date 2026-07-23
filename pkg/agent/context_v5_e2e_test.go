// Copyright 2026 Teradata
package agent

// End-to-end v5 context-shape probes. Complementary to
// skill_lifecycle_e2e_test.go (which drives skill catalog/lifecycle seams):
// this file drives Agent.Chat through the pressure pipeline and asserts on
// the messages the LLM actually saw each turn.
//
// Invariants pinned here (from the v5 Segmented Memory doc):
//
//   Contract 1  — the assembler produces exactly one Role:"system" slot
//                 per LLM call; that slot IS Session.GetMessages's ROM. No
//                 alternate assembler ever synthesizes a system message.
//   Admission   — ballast tool_results ≥ threshold are wrapped into a
//                 storage-reference placeholder before entering L1;
//                 ledger/charter tool_results always enter whole.
//   Fold carry  — after Fold fires, ledger content is carried (never given
//                 to the compressor); narrative+ballast are compressed.
//   Breaker     — 3 folds within a 3-ledger-user-turn window surface as
//                 *RecoverableError with RecoveryAction="reset_context".
//   Isolation   — two sessions on the same Agent share no session state.
//   Discipline  — tail-note carriers (reminders, graph-recall) never
//                 persist into session.Messages; never take Role:"system".
//
// Every assertion inspects captured provider messages from a scripted
// LLM — the same slice Anthropic/OpenAI would have received.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/storage"
	"github.com/teradata-labs/loom/pkg/types"
)

// ============================================================================
// Test tools
// ============================================================================

// heavyBallastHinted opts into ballast-class retention via ContextClassHinter,
// so shuttle admission wraps its results when they exceed the configured
// threshold, and the classifier tags them ballast for fold eviction.
type heavyBallastHinted struct {
	payload string
}

func newHeavyBallastHinted(bytes int) *heavyBallastHinted {
	return &heavyBallastHinted{payload: strings.Repeat("B", bytes)}
}
func (h *heavyBallastHinted) Name() string        { return "heavy_ballast_hinted" }
func (h *heavyBallastHinted) Description() string { return "large ballast-hinted payload" }
func (h *heavyBallastHinted) Backend() string     { return "" }
func (h *heavyBallastHinted) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (h *heavyBallastHinted) ContextClassHint() string { return shuttle.ClassBallast }
func (h *heavyBallastHinted) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: h.payload}, nil
}

// ledgerHintedTool opts into ledger-class retention. Its results must always
// enter L1 whole and be carried across a fold verbatim.
type ledgerHintedTool struct {
	payload string
}

func newLedgerHintedTool(marker string) *ledgerHintedTool {
	return &ledgerHintedTool{payload: "LEDGER-CARGO:" + marker}
}
func (l *ledgerHintedTool) Name() string        { return "ledger_tool" }
func (l *ledgerHintedTool) Description() string { return "returns a ledger-classed payload" }
func (l *ledgerHintedTool) Backend() string     { return "" }
func (l *ledgerHintedTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{Type: "object"}
}
func (l *ledgerHintedTool) ContextClassHint() string { return shuttle.ClassLedger }
func (l *ledgerHintedTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: l.payload}, nil
}

// ============================================================================
// LLM response helpers
// ============================================================================

func respCallLedgerHinted(callID string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    callID,
			Name:  "ledger_tool",
			Input: map[string]interface{}{},
		}},
	}
}

func respCallBallastHinted(callID string) llmtypes.LLMResponse {
	return llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{
			ID:    callID,
			Name:  "heavy_ballast_hinted",
			Input: map[string]interface{}{},
		}},
	}
}

// ============================================================================
// The long-flow context-shape test
// ============================================================================

// TestE2E_V5ContextShape_MultiTurnPressurePipeline drives one session
// through ledger admission, ballast admission-wrapping, three close folds
// (breaker), and asserts Contract-1 shape on every captured LLM call.
func TestE2E_V5ContextShape_MultiTurnPressurePipeline(t *testing.T) {
	// ------ Compressor: capture inputs so fold-carry can be asserted -----
	var compressorInputs [][]Message
	compressor := &mockCompressor{
		enabled: true,
		compressFn: func(msgs []Message) string {
			cp := make([]Message, len(msgs))
			copy(cp, msgs)
			compressorInputs = append(compressorInputs, cp)
			return "compressed narrative+ballast"
		},
	}

	// ------ Scripted LLM -------------------------------------------------
	//
	// Turn 1 : call ledger_tool → end_turn.               (ledger admission)
	// Turn 2 : call heavy_ballast_hinted → end_turn.      (ballast wrap)
	// Turn 3 : end_turn (pressure pre-seeded → fold #1)   (fold #1)
	// Turn 4 : end_turn (pressure pre-seeded → fold #2)   (fold #2)
	// Turn 5 : end_turn (pressure pre-seeded → fold #3)   (fold #3 → breaker)
	//
	// The pressure-turn LLM responses are just end_turn — they do NOT call
	// tools. Fold fires because prepareContext (which runs before dispatch)
	// sees the seeded RED zone and dispatches Fold. Exactly one fold per
	// turn, so 3 close-together turns trip the breaker across turn boundaries
	// as v5 spec intends.
	llm := newSkillTurnScriptedLLM(
		// Turn 1
		respCallLedgerHinted("t1-l"),
		respEndTurn("ledger observed"),

		// Turn 2
		respCallBallastHinted("t2-b"),
		respEndTurn("ballast observed"),

		// Turn 3
		respEndTurn("fold1 done"),

		// Turn 4
		respEndTurn("fold2 done"),

		// Turn 5
		respEndTurn("fold3 done"),

		// Tail.
		respEndTurn("unexpected extra"),
	)

	// ------ Agent -------------------------------------------------------
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 20000

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))

	// Admission wrapping requires shared memory + a positive threshold.
	shared := storage.GetGlobalSharedMemory(&storage.Config{})
	ag.SetSharedMemory(shared)
	ag.SetSharedMemoryThreshold(8 * 1024) // 8 KiB

	// Tools.
	ag.RegisterTool(newLedgerHintedTool("run1"))
	ag.RegisterTool(newHeavyBallastHinted(100 * 1024)) // 100 KiB → wraps

	ctx := context.Background()
	sessionID := "e2e-v5-shape"

	// Wire state snapshotter so LOOM_TEST_DUMP_DIR captures state+context
	// pairs per LLM call for the loom-v5-eval consumer.
	llm.SetStateSnapshotter(func() string {
		s, ok := ag.memory.GetSession(sessionID)
		if !ok {
			return "(session not yet created)\n"
		}
		return renderSegMemState(s)
	})

	// -------- Turn 1: ledger admission --------
	_, err := ag.Chat(ctx, sessionID, "call the ledger tool")
	require.NoError(t, err, "turn 1 ledger admission must not error")

	// Wire compressor + durable store before pressure-inducing turns.
	session, ok := ag.memory.GetSession(sessionID)
	require.True(t, ok, "session must exist after turn 1")
	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	segMem.SetCompressor(compressor)
	store := newContextClassSQLiteStore(t)
	require.NoError(t, store.SaveSession(ctx, session))
	segMem.SetSessionStore(store, sessionID)

	// -------- Turn 2: ballast admission (wrap fires) --------
	_, err = ag.Chat(ctx, sessionID, "call the ballast tool")
	require.NoError(t, err, "turn 2 ballast admission must not error")

	// -------- Turn 3: first fold --------
	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax()*90/100)
	_, err = ag.Chat(ctx, sessionID, "spike 1")
	require.NoError(t, err, "turn 3 fold #1 must not error at Chat boundary")
	foldsAfterT3 := foldCountLocked(segMem)
	require.Greater(t, foldsAfterT3, 0, "turn 3: at least one fold must have fired")

	// ------ Post-fold L1 assertions (fold carry closure) --------------
	// After fold, L1 must:
	//   - carry ledger content verbatim (LEDGER-CARGO: marker present)
	//   - NOT carry raw ballast payload (a 1 KiB run of B's must be gone)
	//   - have residue populated (L2 summary non-empty)
	l1AfterFold := segMem.GetMessages()
	require.NotEmpty(t, segMem.GetL2Summary(),
		"post-fold: L2 residue slot must be populated (compressor output)")
	sawLedgerInL1 := false
	for _, m := range l1AfterFold {
		if strings.Contains(m.Content, "LEDGER-CARGO:") {
			sawLedgerInL1 = true
		}
		assert.NotContainsf(t, m.Content, strings.Repeat("B", 1024),
			"post-fold: L1 msg (role=%s) must not contain raw ballast payload; fold must have compressed it away", m.Role)
	}
	assert.True(t, sawLedgerInL1,
		"post-fold: L1 must still carry the ledger-classed tool result (LEDGER-CARGO: marker)")

	// -------- Turn 4: second fold --------
	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax()*90/100)
	_, err = ag.Chat(ctx, sessionID, "spike 2")
	require.NoError(t, err, "turn 4 fold #2 must not error at Chat boundary")
	foldsAfterT4 := foldCountLocked(segMem)
	require.Greater(t, foldsAfterT4, foldsAfterT3, "turn 4: another fold must have fired")

	// -------- Turn 5: third fold → breaker trips --------
	seedL1TokenPressure(segMem, segMem.GetTokenBudgetMax()*90/100)
	_, err = ag.Chat(ctx, sessionID, "spike 3")
	require.Error(t, err, "turn 5: breaker must surface as an error, not silent success")
	var re *RecoverableError
	require.ErrorAs(t, err, &re,
		"turn 5: breaker error must be a *RecoverableError (got %T: %v)", err, err)
	assert.Equal(t, "reset_context", re.RecoveryAction,
		"breaker recovery action must be reset_context")
	assert.Equal(t, "token_budget_exceeded", re.ErrorType,
		"breaker error type must be token_budget_exceeded")

	// ------ Cross-cutting Contract-1 assertions -----------------------
	calls := llm.getCalls()
	require.NotEmpty(t, calls, "at least one LLM call must have been captured")
	for i, msgs := range calls {
		sysCount := 0
		for _, m := range msgs {
			if m.Role == "system" {
				sysCount++
			}
		}
		assert.Equalf(t, 1, sysCount,
			"call %d: Contract 1 violated — got %d system messages, want 1", i, sysCount)

		for _, m := range msgs {
			if m.Role == "system" {
				assert.NotContainsf(t, m.Content, "[Skill Discovery]",
					"call %d system slot must not carry the deleted tail-note menu", i)
			}
		}
	}

	// ------ Persisted history discipline ------------------------------
	for i, m := range session.Messages {
		assert.NotEqualf(t, "system", m.Role,
			"session.Messages[%d]: persisted history must not carry system-role: %q", i, m.Content)
		assert.NotContainsf(t, m.Content, "[Skill Discovery]",
			"session.Messages[%d]: persisted history must not carry tail-note menu", i)
	}

	// ------ Admission wrapping assertions -----------------------------
	// Ballast tool result: content is a wrapping placeholder, NOT the raw
	// 100 KiB payload.
	ballastResult := findLatestToolResultForToolName(session, "heavy_ballast_hinted")
	require.NotNil(t, ballastResult,
		"session must contain at least one heavy_ballast_hinted tool result")
	assert.NotContains(t, ballastResult.Content, strings.Repeat("B", 1024),
		"admission MUST have wrapped the raw ballast payload — a 1KiB run of B's must not appear in the persisted message")
	// Wrapper carries a DataReference so recall_context can hydrate it later.
	if ballastResult.ToolResult != nil {
		assert.NotNilf(t, ballastResult.ToolResult.DataReference,
			"wrapped ballast result must carry a DataReference")
	}

	// Ledger tool result: enters L1 whole (admission-exempt).
	ledgerResult := findLatestToolResultForToolName(session, "ledger_tool")
	require.NotNil(t, ledgerResult,
		"session must contain at least one ledger_tool result")
	assert.Contains(t, ledgerResult.Content, "LEDGER-CARGO:",
		"ledger tool result must enter L1 whole (admission-exempt), got %q",
		truncateForLog(ledgerResult.Content))

	// ------ Fold carry closure ----------------------------------------
	// Fold's compressor must have been called; its inputs must NOT contain
	// ledger content (ledger is carry-set, not compress-set).
	require.NotEmpty(t, compressorInputs,
		"at least one fold fired → compressor must have been called at least once")
	for i, cinput := range compressorInputs {
		for j, m := range cinput {
			assert.NotContainsf(t, m.Content, "LEDGER-CARGO:",
				"compressor input %d msg %d: ledger content must never be handed to the fold compressor", i, j)
		}
	}

	// ------ Fold-count sanity for the breaker -------------------------
	assert.GreaterOrEqualf(t, foldCountLocked(segMem), 3,
		"breaker path: fold history should contain ≥3 folds by the time the breaker trips (got %d)",
		foldCountLocked(segMem))
}

// ============================================================================
// Multi-session isolation
// ============================================================================

// TestE2E_V5MultiSessionIsolation drives two sessions on the same Agent and
// asserts each session's L1 contains only that session's own user turn.
func TestE2E_V5MultiSessionIsolation(t *testing.T) {
	llm := newSkillTurnScriptedLLM(
		respCallLedgerHinted("a-l"),
		respEndTurn("A done"),
		respCallLedgerHinted("b-l"),
		respEndTurn("B done"),
		respEndTurn("tail"),
	)

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false

	ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg))
	ag.RegisterTool(newLedgerHintedTool("shared-payload"))

	ctx := context.Background()

	_, err := ag.Chat(ctx, "sess-A", "A: call ledger")
	require.NoError(t, err)
	_, err = ag.Chat(ctx, "sess-B", "B: call ledger")
	require.NoError(t, err)

	sA, ok := ag.memory.GetSession("sess-A")
	require.True(t, ok)
	sB, ok := ag.memory.GetSession("sess-B")
	require.True(t, ok)

	// Each session's flat history mentions ONLY its own user message.
	assertContainsUser(t, sA.Messages, "A: call ledger")
	assertNotContainsUser(t, sA.Messages, "B: call ledger")
	assertContainsUser(t, sB.Messages, "B: call ledger")
	assertNotContainsUser(t, sB.Messages, "A: call ledger")

	// Distinct SegmentedMemory instances.
	assert.NotSame(t, sA.SegmentedMem, sB.SegmentedMem,
		"each session must have its own SegmentedMemory instance")
}

// ============================================================================
// Helpers
// ============================================================================

// findLatestToolResultForToolName walks session.Messages backwards for the
// most recent tool-role message whose paired assistant tool_call names
// toolName. Returns nil if no such tool result exists.
func findLatestToolResultForToolName(s *types.Session, toolName string) *Message {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := &s.Messages[i]
		if m.Role != "tool" {
			continue
		}
		callName := precedingToolCallName(s.Messages, i)
		if callName == toolName {
			return m
		}
	}
	return nil
}

// foldCountLocked returns len(sm.foldTurnHistory) under the SegmentedMemory
// read lock. Package-internal state access; test-only.
func foldCountLocked(sm *SegmentedMemory) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.foldTurnHistory)
}

// assertContainsUser / assertNotContainsUser look for a user-role message
// with content equal (trimmed) to want.
func assertContainsUser(t *testing.T, messages []Message, want string) {
	t.Helper()
	for _, m := range messages {
		if m.Role == "user" && strings.TrimSpace(m.Content) == want {
			return
		}
	}
	t.Errorf("expected user message %q, not found", want)
}
func assertNotContainsUser(t *testing.T, messages []Message, want string) {
	t.Helper()
	for _, m := range messages {
		if m.Role == "user" && strings.TrimSpace(m.Content) == want {
			t.Errorf("did not expect user message %q, but found it", want)
			return
		}
	}
}
