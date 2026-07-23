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

// D-5 (Fold, breaker & restore) — capstone (SC-002) component acceptance test.
//
// Drives the full B1-B18 worked trace from plan/attachments/loom-segmented-memory-redesign.md
// §7 (Doug's "new-data-access" onboarding) as a table-driven walk over the real single-writer
// seam — Agent.prepareContext, the real (never-a-Claude-SDK-mock) runtime entry point
// gates-drive-looms-runtime binds to at component level (D-1 precedent) — asserting that
// memory is mutated ONLY inside prepareContext's dispatch and NEVER below the yellow zone,
// across the whole trace, regardless of message count.
//
// Each beat's own budget-percentage crossing is forced deterministically via seedBudgetPct
// (built on the same seedL1TokenPressure primitive D-1's own pressure-path tests use) so the
// trace reproduces the doc's exact zone transitions (green through B9, YELLOW at B10, green
// again through B13, YELLOW at B14, RED at B15, green again through B18) without depending on
// the tokenizer's byte-to-token ratio for hand-picked filler text — the messages themselves
// stay real and structurally faithful (charter/ledger/ballast/narrative, real tool_use/
// tool_result pairs), so the fold at B15 is asserted against real carry-set content.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedBudgetPct forces sm's BudgetPct() to approximately pct by overriding its cached L1
// token count while accounting for ROM/L2/tool-schema tokens already counted — the same
// "decouple pressure from content" pattern seedL1TokenPressure documents, extended so a
// multi-beat trace can hit a specific zone at each checkpoint regardless of what accumulated
// (including a real post-fold L2 residue) before it.
func seedBudgetPct(sm *SegmentedMemory, pct float64) {
	total := sm.GetTokenBudgetMax()
	target := int(float64(total) * pct / 100)
	l1Target := target - sm.cachedL2Tokens - sm.cachedROMTokens - sm.cachedToolSchemaTokens
	if l1Target < 0 {
		l1Target = 0
	}
	seedL1TokenPressure(sm, l1Target)
}

func addSessionToolPair(ctx context.Context, session *Session, toolUseID, toolName, content string, class ContextClass) {
	session.AddMessage(ctx, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: toolUseID, Name: toolName}}, Timestamp: time.Now()})
	session.AddMessage(ctx, Message{Role: "tool", Content: content, ToolUseID: toolUseID, ContextClass: class, Timestamp: time.Now()})
}

func addSessionMessage(ctx context.Context, session *Session, role string, class ContextClass, content string) {
	session.AddMessage(ctx, Message{Role: role, Content: content, ContextClass: class, Timestamp: time.Now()})
}

func TestCapstone_B1ThroughB18_SingleWriterNeverMutatesBelowYellow(t *testing.T) {
	ctx := context.Background()
	ag := NewAgent(&mockBackend{}, &mockSimpleLLM{})
	sm := NewSegmentedMemory("", 20000, 0) // budget 20000, ROM empty (Kernel/ROM baseline not load-bearing for this trace)
	sm.SetCompressor(&mockCompressor{enabled: true})
	sm.SetMinValvePayoffTokens(1) // the exact payoff constant is D-4's own suite's concern; only the zone-dispatch sequencing is under test here
	sm.SetKeepRecentBallast(1)
	sessionID := "capstone-b1-b18"
	session := &Session{ID: sessionID, SegmentedMem: sm}

	// ValveEvict refuses to evict anything without a durable store wired (a
	// stub would be unrecoverable, C-022), and Fold's persistence step is
	// likewise store-gated — wire a real in-process SQLite store so both the
	// yellow-zone valve beats and the red-zone fold beat behave exactly as
	// they would in production.
	store := newContextClassSQLiteStore(t)
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: make(map[string]interface{}), CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	sm.SetSessionStore(store, sessionID)

	yellowPct, redPct := sm.ZoneThresholds()
	require.Equal(t, 70.0, yellowPct)
	require.Equal(t, 85.0, redPct)

	assertNoMutation := func(t *testing.T, label string) {
		t.Helper()
		beforeL1 := sm.GetMessages()
		beforeL2 := sm.GetL2Summary()
		_, err := ag.prepareContext(ctx, session)
		require.NoError(t, err, "%s: prepareContext must not error below red", label)
		assert.Equal(t, beforeL1, sm.GetMessages(), "%s: nothing may mutate L1 below the yellow zone", label)
		assert.Equal(t, beforeL2, sm.GetL2Summary(), "%s: nothing may mutate L2 below the yellow zone", label)
	}

	assertValveOnly := func(t *testing.T, label string) {
		t.Helper()
		beforeL1Len := len(sm.GetMessages())
		beforeL2 := sm.GetL2Summary()
		_, err := ag.prepareContext(ctx, session)
		require.NoError(t, err, "%s: prepareContext must not error in the yellow zone", label)
		assert.Equal(t, beforeL1Len, len(sm.GetMessages()), "%s: the valve must only rewrite Content in place, never add/remove messages", label)
		assert.Equal(t, beforeL2, sm.GetL2Summary(), "%s: the valve must never touch L2 — only fold may", label)
	}

	// --- B1: USER kickoff (ledger). ---
	addSessionMessage(ctx, session, "user", ClassLedger, "Give data scientists read-only access to test_nda_titanic_db.")

	// --- B2: ASSISTANT loads the skill (charter). check before: 3,040/20,000=15.2% -> green. ---
	seedBudgetPct(sm, 15.2)
	assertNoMutation(t, "before B2")
	addSessionToolPair(ctx, session, "t-charter", "manage_skills", "Skill: New Data Access. GATE 3: no DDL without approval.", ClassCharter)

	// --- B3: ASSISTANT discovers db/tables (ballastA). check before: 9,055/20,000=45.275% -> green. ---
	seedBudgetPct(sm, 45.275)
	assertNoMutation(t, "before B3")
	addSessionToolPair(ctx, session, "t-ballastA", "query_data", "DBC, test_nda_titanic_db tables", ClassBallast)

	// --- B4: ASSISTANT reads columns (ballastB). check before: 9,409/20,000=47.045% -> green. ---
	seedBudgetPct(sm, 47.045)
	assertNoMutation(t, "before B4")
	addSessionToolPair(ctx, session, "t-ballastB", "query_data", "PassengerId INT, Name VARCHAR(200) ...", ClassBallast)

	// --- B5: ASSISTANT classifies sensitivity (narrative). check before: 11,177/20,000=55.885% -> green. ---
	seedBudgetPct(sm, 55.885)
	assertNoMutation(t, "before B5")
	addSessionMessage(ctx, session, "assistant", ClassNarrative, "Name=HIGH PII; Cabin=MEDIUM location.")

	// --- B6: ASSISTANT readiness report, gate1 question (narrative). check before: 11,979/20,000=59.895% -> green. ---
	seedBudgetPct(sm, 59.895)
	assertNoMutation(t, "before B6")
	addSessionMessage(ctx, session, "assistant", ClassNarrative, "READINESS REPORT. GATE 1 - reply approved.")

	// --- B7: USER approved (gate1, ledger). ---
	addSessionMessage(ctx, session, "user", ClassLedger, "approved")

	// --- B8: ASSISTANT generates SQL, gate2 question (narrative). check before: 12,884/20,000=64.42% -> green. ---
	seedBudgetPct(sm, 64.42)
	assertNoMutation(t, "before B8")
	addSessionMessage(ctx, session, "assistant", ClassNarrative, "CREATE ROLE data_science; ... GATE 2 - reply approved.")

	// --- B9: USER approved (gate2, ledger). ---
	addSessionMessage(ctx, session, "user", ClassLedger, "approved")

	// --- B10: check before: 14,289/20,000=71.445% -> YELLOW -> VALVE fires (oldest ballast, ballastA, evicted). ---
	seedBudgetPct(sm, 71.445)
	assertValveOnly(t, "before B10")
	stubbedA := messageByToolUseID(t, sm.GetMessages(), "t-ballastA")
	assert.True(t, strings.HasPrefix(stubbedA.Content, evictedStubPrefix), "before B10: the valve must evict the oldest ballast candidate")
	untouchedB := messageByToolUseID(t, sm.GetMessages(), "t-ballastB")
	assert.False(t, strings.HasPrefix(untouchedB.Content, evictedStubPrefix), "before B10: the newest ballast item must be protected")
	addSessionMessage(ctx, session, "assistant", ClassNarrative, "GATE 3 - I will execute the setup SQL. Approve?")

	// --- B11: USER approved (gate3, ledger). ---
	addSessionMessage(ctx, session, "user", ClassLedger, "approved")

	// --- B12: check before: 12,364/20,000=61.82% -> green. ASSISTANT executes (mutating tool -> ledger). ---
	seedBudgetPct(sm, 61.82)
	assertNoMutation(t, "before B12")
	addSessionToolPair(ctx, session, "t-exec", "run_migration", "CREATE ROLE — OK; CREATE VIEW — OK; GRANT — OK", ClassLedger)

	// --- B13: check before: 12,660/20,000=63.3% -> green. ASSISTANT recalls + runs tests (ballastC, ballastD). ---
	seedBudgetPct(sm, 63.3)
	assertNoMutation(t, "before B13")
	addSessionToolPair(ctx, session, "t-ballastC", "recall_context", "PassengerId INT, Name VARCHAR(200) ... (recalled)", ClassBallast)
	addSessionToolPair(ctx, session, "t-ballastD", "run_query", "TOP 5 rows: (1,3,'male',22.0,7.25,'S')", ClassBallast)

	// --- B14: check before: 16,092/20,000=80.46% -> YELLOW again -> VALVE reclaims ballastB/ballastC (ballastD protected). ---
	seedBudgetPct(sm, 80.46)
	assertValveOnly(t, "before B14")
	for _, id := range []string{"t-ballastB", "t-ballastC"} {
		m := messageByToolUseID(t, sm.GetMessages(), id)
		assert.True(t, strings.HasPrefix(m.Content, evictedStubPrefix), "before B14: %s must now be evicted too", id)
	}
	dNow := messageByToolUseID(t, sm.GetMessages(), "t-ballastD")
	assert.False(t, strings.HasPrefix(dNow.Content, evictedStubPrefix), "before B14: the newest ballast item must still be protected")
	addSessionMessage(ctx, session, "assistant", ClassNarrative, "Positive SELECT ok; counts match.")
	addSessionToolPair(ctx, session, "t-ballastE", "run_query", "second test dump", ClassBallast)

	// --- B15: check before: 17,517/20,000=87.585% -> RED -> FOLD, the trace's only fold. ---
	seedBudgetPct(sm, 87.585)
	l1BeforeFold := sm.GetMessages()
	_, err := ag.prepareContext(ctx, session)
	require.NoError(t, err, "before B15: this fold must succeed — it is the trace's only fold, the breaker must not engage")

	folded := sm.GetMessages()
	assert.Less(t, len(folded), len(l1BeforeFold), "before B15: the fold must shrink L1 to the carry set")
	assert.Equal(t, "user", folded[0].Role, "before B15: the post-fold carry must start with a user message")
	assert.NotEmpty(t, sm.GetL2Summary(), "before B15: the fold must populate L2 with the residue")

	// Charter and every ledger gate/approval/exec-record must survive the
	// fold verbatim; every ballast item (stubbed or not) must be gone.
	charterMsg := messageByToolUseID(t, folded, "t-charter")
	assert.Contains(t, charterMsg.Content, "Skill: New Data Access")
	execMsg := messageByToolUseID(t, folded, "t-exec")
	assert.Contains(t, execMsg.Content, "CREATE ROLE — OK")
	for _, id := range []string{"t-ballastA", "t-ballastB", "t-ballastC", "t-ballastD", "t-ballastE"} {
		for _, m := range folded {
			assert.NotEqual(t, id, m.ToolUseID, "before B15: ballast %s must not survive the fold, stubbed or not", id)
		}
	}

	addSessionMessage(ctx, session, "assistant", ClassNarrative, "All tests green. GATE 4 - approve as final acceptance?")

	// --- B16: USER approved (gate4, ledger) — the compiled context renders the fold residue as a fixed head from here on. ---
	addSessionMessage(ctx, session, "user", ClassLedger, "approved")

	// --- B17: USER asks a later question (ledger). ---
	addSessionMessage(ctx, session, "user", ClassLedger, "remind me — why exactly did we exclude Cabin?")

	// --- B18: check before: ~60.8% -> green. ASSISTANT recalls the pre-fold reasoning and answers. ---
	seedBudgetPct(sm, 60.8)
	assertNoMutation(t, "before B18")
	addSessionToolPair(ctx, session, "t-ballastF", "recall_context", "Cabin — quasi-identifier, excluded", ClassBallast)
	addSessionMessage(ctx, session, "assistant", ClassNarrative, "We excluded Cabin because it's a quasi-identifier.")

	// The breaker must never have engaged across this trace — exactly one fold occurred.
	assert.Len(t, sm.foldTurnHistory, 1, "the trace contains exactly one fold; the breaker must never have tripped")
}
