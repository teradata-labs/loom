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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

func callMsg(id, tool, sql string) types.Message {
	in := map[string]interface{}{}
	if sql != "" {
		in["sql"] = sql
	}
	return types.Message{Role: "assistant", ToolCalls: []types.ToolCall{{ID: id, Name: tool, Input: in}}}
}
func resultMsg(id string, ok bool, errMsg string) types.Message {
	r := &shuttle.Result{Success: ok}
	if !ok {
		r.Error = &shuttle.Error{Code: "db_error", Message: errMsg}
	}
	return types.Message{Role: "tool", ToolUseID: id, ToolResult: r}
}

// A failure followed by a success of the same statement class yields exactly
// one pair carrying both inputs — the diff IS the lesson.
func TestMineLessonPairsErrorThenFix(t *testing.T) {
	msgs := []types.Message{
		callMsg("1", "execute_statement", "INSERT INTO vt_card_day SELECT CC_Number FROM t"),
		resultMsg("1", false, "[Error 2616] Numeric overflow occurred during computation."),
		callMsg("2", "execute_statement", "INSERT INTO vt_card_day SELECT CAST(CC_Number AS BIGINT) FROM t"),
		resultMsg("2", true, ""),
	}
	pairs := mineLessonPairs(msgs)
	require.Len(t, pairs, 1)
	assert.Contains(t, pairs[0].ErrorText, "2616")
	assert.Contains(t, pairs[0].FailingIn, "CC_Number FROM t")
	assert.Contains(t, pairs[0].SucceedsIn, "BIGINT")
}

// An unresolved failure mints nothing — that is the whole point.
func TestMineLessonPairsUnresolvedFailure(t *testing.T) {
	msgs := []types.Message{
		callMsg("1", "execute_statement", "INSERT INTO vt_x SELECT a FROM t"),
		resultMsg("1", false, "overflow"),
		// Success on a DIFFERENT class (different table) must not pair.
		callMsg("2", "execute_statement", "INSERT INTO vt_other SELECT a FROM t"),
		resultMsg("2", true, ""),
	}
	assert.Empty(t, mineLessonPairs(msgs))
}

// Only the first failure and the fixing success pair up; repeats of the same
// failing class don't multiply pairs.
func TestMineLessonPairsOnePerClass(t *testing.T) {
	msgs := []types.Message{
		callMsg("1", "execute_statement", "INSERT INTO vt_a SELECT x FROM t"),
		resultMsg("1", false, "overflow attempt 1"),
		callMsg("2", "execute_statement", "INSERT INTO vt_a SELECT y FROM t"),
		resultMsg("2", false, "overflow attempt 2"),
		callMsg("3", "execute_statement", "INSERT INTO vt_a SELECT z FROM t"),
		resultMsg("3", true, ""),
		callMsg("4", "execute_statement", "INSERT INTO vt_a SELECT z2 FROM t"),
		resultMsg("4", true, ""),
	}
	pairs := mineLessonPairs(msgs)
	require.Len(t, pairs, 1)
	assert.Contains(t, pairs[0].ErrorText, "attempt 1", "earliest failure wins")
	assert.Contains(t, pairs[0].SucceedsIn, "SELECT z FROM", "first success after failure wins")
}

// The trap's real shape: the INSERT fails, the fix happens in OTHER
// statements (DROP + re-CREATE with BIGINT), then the identical INSERT
// succeeds. The pair must carry those intervening calls — without them the
// diff is empty and the miner is blind to cross-statement fixes.
func TestMineLessonPairsCrossStatementFix(t *testing.T) {
	ins := "INSERT INTO vt_card_day SELECT CC_Number FROM t"
	msgs := []types.Message{
		callMsg("1", "execute_statement", ins),
		resultMsg("1", false, "[Error 2616] Numeric overflow occurred during computation."),
		callMsg("2", "execute_statement", "DROP TABLE vt_card_day"),
		resultMsg("2", true, ""),
		callMsg("3", "execute_statement", "CREATE VOLATILE TABLE vt_card_day (card_id BIGINT, txns INTEGER, amt DECIMAL(14,2)) ON COMMIT PRESERVE ROWS"),
		resultMsg("3", true, ""),
		callMsg("4", "execute_statement", ins),
		resultMsg("4", true, ""),
	}
	pairs := mineLessonPairs(msgs)
	require.Len(t, pairs, 1)
	assert.Equal(t, pairs[0].FailingIn, pairs[0].SucceedsIn, "identical inputs — the fix is elsewhere")
	require.NotEmpty(t, pairs[0].Intervening)
	joined := strings.Join(pairs[0].Intervening, " ")
	assert.Contains(t, joined, "BIGINT", "the re-CREATE carrying the fix must be visible")

	prompt := buildLessonMiningPrompt(pairs, "")
	assert.Contains(t, prompt, "CALL BETWEEN FAILURE AND SUCCESS")
	assert.Contains(t, prompt, "name THAT change")
}

// Intervening calls cap at the most recent few — the fix is adjacent to
// the success.
func TestMineLessonPairsInterveningCap(t *testing.T) {
	msgs := []types.Message{
		callMsg("1", "execute_statement", "INSERT INTO vt_a SELECT x FROM t"),
		resultMsg("1", false, "boom"),
	}
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs,
			callMsg(id, "execute_query", "SELECT probe"+id+" FROM other"+id),
			resultMsg(id, true, ""))
	}
	msgs = append(msgs,
		callMsg("9", "execute_statement", "INSERT INTO vt_a SELECT y FROM t"),
		resultMsg("9", true, ""))
	pairs := mineLessonPairs(msgs)
	require.Len(t, pairs, 1)
	assert.LessOrEqual(t, len(pairs[0].Intervening), maxInterveningCalls)
}

// The execution-time ledger survives what the compiled view loses, and
// take-consumes exactly once. Concurrency-safe (parallel tool execution).
func TestToolLedgerRecordAndTake(t *testing.T) {
	a := &Agent{enableGraphMemoryExtraction: true}
	tc := types.ToolCall{ID: "1", Name: "execute_statement",
		Input: map[string]interface{}{"sql": "INSERT INTO vt_a SELECT x FROM t"}}
	a.recordToolLedger("s1", tc, &shuttle.Result{Success: false,
		Error: &shuttle.Error{Code: "db_error", Message: "[Error 2616] overflow"}})
	tc2 := types.ToolCall{ID: "2", Name: "execute_statement",
		Input: map[string]interface{}{"sql": "CREATE VOLATILE TABLE vt_a (card_id BIGINT)"}}
	a.recordToolLedger("s1", tc2, &shuttle.Result{Success: true})
	a.recordToolLedger("s1", tc, &shuttle.Result{Success: true,
		Data: `{"activity_count":55316,"status":"success"}`})

	events := a.takeToolLedger("s1")
	require.Len(t, events, 3)
	assert.Contains(t, events[2].resultPreview, "55316", "successful results carry a preview")
	pairs := pairEvents(events)
	require.Len(t, pairs, 1)
	assert.Contains(t, pairs[0].ErrorText, "2616")
	assert.Contains(t, pairs[0].SucceedsResult, "55316")
	require.NotEmpty(t, pairs[0].Intervening)
	assert.Contains(t, pairs[0].Intervening[0], "BIGINT")

	assert.Empty(t, a.takeToolLedger("s1"), "take consumes")
}

// The measured poison scenario, mechanical layer: a date-filter "fix" whose
// INSERT succeeded with activity_count 0 (it inserted NOTHING) must not
// close a pair — no lesson gets minted from an error silenced by matching
// no rows.
func TestVacuousSuccessBlocksPairing(t *testing.T) {
	a := &Agent{enableGraphMemoryExtraction: true}
	tc := types.ToolCall{ID: "1", Name: "execute_statement",
		Input: map[string]interface{}{"sql": "INSERT INTO vt SELECT ... WHERE CAST(TrxDateTime AS DATE) = DATE '2004-07-15'"}}
	a.recordToolLedger("s2", tc, &shuttle.Result{Success: false,
		Error: &shuttle.Error{Code: "db_error", Message: "[Error 2666] Invalid date supplied"}})
	tcFixed := types.ToolCall{ID: "2", Name: "execute_statement",
		Input: map[string]interface{}{"sql": "INSERT INTO vt SELECT ... WHERE SUBSTR(TrxDateTime,1,10) = '2004-07-15'"}}
	a.recordToolLedger("s2", tcFixed, &shuttle.Result{Success: true,
		Data: `{"activity_count":0,"status":"success"}`})

	events := a.takeToolLedger("s2")
	assert.True(t, events[1].vacuous, "activity_count 0 on an INSERT must be judged vacuous")
	assert.Empty(t, pairEvents(events), "a vacuous success must not close a pair")
}

// The model layer (Fix C): where mechanics abstain (prose result), the miner
// is shown what the "fix" actually returned plus the skip rule.
func TestLessonMiningPromptShowsResultAndSkipRule(t *testing.T) {
	a := &Agent{enableGraphMemoryExtraction: true}
	tc := types.ToolCall{ID: "1", Name: "execute_statement",
		Input: map[string]interface{}{"sql": "INSERT INTO vt SELECT ... WHERE CAST(TrxDateTime AS DATE) = DATE '2004-07-15'"}}
	a.recordToolLedger("s3", tc, &shuttle.Result{Success: false,
		Error: &shuttle.Error{Code: "db_error", Message: "[Error 2666] Invalid date supplied"}})
	tcFixed := types.ToolCall{ID: "2", Name: "execute_statement",
		Input: map[string]interface{}{"sql": "INSERT INTO vt SELECT ... WHERE SUBSTR(TrxDateTime,1,10) = '2004-07-15'"}}
	// Prose payload: the convention matcher abstains, so the pair survives
	// mechanically and the model layer is the guard under test.
	a.recordToolLedger("s3", tcFixed, &shuttle.Result{Success: true,
		Data: `Statement completed. 0 rows inserted.`})

	pairs := pairEvents(a.takeToolLedger("s3"))
	require.Len(t, pairs, 1)

	prompt := buildLessonMiningPrompt(pairs, "")
	assert.Contains(t, prompt, "SUCCEEDED RESULT: Statement completed. 0 rows inserted.",
		"the miner must see what the fix actually returned")
	assert.Contains(t, prompt, "a success that did no work is NOT a fix")
}

// A tool implementing shuttle.VacuousResultJudge wins over the convention
// matcher — exact endpoint knowledge stays with the endpoint's adapter.
type vacuousJudgingTool struct {
	shuttle.MockTool
}

func (v *vacuousJudgingTool) VacuousSuccess(_ map[string]interface{}, result *shuttle.Result) (bool, bool) {
	data, _ := result.Data.(string)
	// This tool knows its own schema: "outcome":"noop" means nothing happened
	// (a shape no generic convention would recognize).
	return strings.Contains(data, `"outcome":"noop"`), true
}

func TestToolJudgeOverridesConvention(t *testing.T) {
	reg := shuttle.NewRegistry()
	tool := &vacuousJudgingTool{MockTool: shuttle.MockTool{MockName: "custom_api"}}
	reg.Register(tool)
	a := &Agent{enableGraphMemoryExtraction: true, tools: reg}

	tc := types.ToolCall{ID: "1", Name: "custom_api", Input: map[string]interface{}{}}
	a.recordToolLedger("s4", tc, &shuttle.Result{Success: false,
		Error: &shuttle.Error{Code: "api_error", Message: "bad filter"}})
	a.recordToolLedger("s4", tc, &shuttle.Result{Success: true, Data: `{"outcome":"noop"}`})

	events := a.takeToolLedger("s4")
	assert.True(t, events[1].vacuous, "the tool's own judge must be honored")
	assert.Empty(t, pairEvents(events))
}

func TestToolLedgerConcurrent(t *testing.T) {
	a := &Agent{enableGraphMemoryExtraction: true}
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				a.recordToolLedger("s", types.ToolCall{Name: "t",
					Input: map[string]interface{}{}}, &shuttle.Result{Success: true})
			}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	assert.LessOrEqual(t, len(a.takeToolLedger("s")), maxLedgerEvents)
}

func TestSQLClass(t *testing.T) {
	assert.Equal(t, "INSERT VT_CARD_DAY", sqlClass("insert into vt_card_day (a,b) select ..."))
	assert.Equal(t, "CREATE VT_X", sqlClass("CREATE VOLATILE TABLE vt_x (a int)")) // keys on the actual table name
	assert.Equal(t, "SELECT T", sqlClass("SELECT * FROM t WHERE 1=1"))
	assert.Equal(t, "", sqlClass("  "))
}

// The mining prompt states the auditor stance, the evidence contract, and
// the skip rules.
func TestLessonMiningPrompt(t *testing.T) {
	p := buildLessonMiningPrompt([]lessonPair{{Tool: "x", FailingIn: "{a}", ErrorText: "boom", SucceedsIn: "{b}"}}, "")
	assert.Contains(t, p, "CLAIMED fix")
	assert.Contains(t, p, "AUDITING")
	assert.Contains(t, p, "what it traded away")
	assert.Contains(t, p, "Skip an item")
	assert.Contains(t, p, `"memory_type": "lesson"`)
	assert.NotContains(t, p, "THE CONVERSATION'S TASK", "no task block when task is empty")
}

// The auditor trio: task background, downstream harm evidence, and the
// warning-not-fix rule — the information a healthy-looking defensive cast
// hides (measured: card_id NULLed fleet-wide while activity_count read
// 55316; the proof sat three ledger events later, unseen).
func TestLessonMiningPromptAuditorTrio(t *testing.T) {
	pair := lessonPair{
		Tool:           "execute_statement",
		FailingIn:      `{"sql":"INSERT INTO vt_card_day SELECT CAST(CC_Number AS INTEGER) ..."}`,
		ErrorText:      "[Error 2616] Numeric overflow occurred during computation",
		SucceedsIn:     `{"sql":"INSERT INTO vt_card_day SELECT CASE WHEN ... ELSE NULL END ..."}`,
		SucceedsResult: `{"activity_count":55316,"status":"success"}`,
		Downstream: []string{
			`{"sql":"SELECT COUNT(DISTINCT card_id) FROM vt_card_day"} RETURNED {"row_count":1,"rows":[[0]]}`,
		},
		ChangedFragments: []string{"CASE", "NULL"},
	}
	task := "Create a summary of card transactions for 2004-07-15: how many cards, total amount."
	p := buildLessonMiningPrompt([]lessonPair{pair}, task)

	assert.Contains(t, p, "THE CONVERSATION'S TASK")
	assert.Contains(t, p, "how many cards, total amount")
	assert.Contains(t, p, "lessons must still generalize")
	assert.Contains(t, p, `LATER USE OF THE SAME OBJECT: {"sql":"SELECT COUNT(DISTINCT card_id)`)
	assert.Contains(t, p, "record the lesson as a WARNING against that change")
	assert.Contains(t, p, "safe only when the column is not used as a key")
}

// downstreamUses pulls later successful reads of the pair's target object,
// with results — and nothing about other objects.
func TestDownstreamUses(t *testing.T) {
	events := []minedEvent{
		{key: "execute_statement:INSERT VT_CARD_DAY", ok: false, errText: "overflow",
			input: `{"sql":"INSERT INTO vt_card_day ..."}`},
		{key: "execute_statement:INSERT VT_CARD_DAY", ok: true,
			input:         `{"sql":"INSERT INTO vt_card_day ... CASE ... NULL ..."}`,
			resultPreview: `{"activity_count":55316}`},
		{key: "execute_query:SELECT OTHER_TABLE", ok: true,
			input:         `{"sql":"SELECT * FROM other_table"}`,
			resultPreview: `{"row_count":5}`},
		{key: "execute_query:SELECT VT_CARD_DAY", ok: true,
			input:         `{"sql":"SELECT COUNT(DISTINCT card_id) FROM vt_card_day"}`,
			resultPreview: `{"row_count":1,"rows":[[0]]}`},
	}
	uses := downstreamUses(events, 1, events[1].key)
	require.Len(t, uses, 1)
	assert.Contains(t, uses[0], "COUNT(DISTINCT card_id)")
	assert.Contains(t, uses[0], `RETURNED {"row_count":1,"rows":[[0]]}`)

	// No target identifier in the class: no downstream evidence.
	assert.Nil(t, downstreamUses(events, 1, "some_tool"))
}

// The per-turn prompt is the user-facts lane only: mid-conversation beliefs
// must never mint lessons (measured 58:8 wrong:right without this split).
func TestPerTurnPromptExcludesLessons(t *testing.T) {
	msgs := []types.Message{{Role: "user", Content: "create the volatile table"}}
	p := buildGraphMemoryExtractionPrompt(msgs, 10, nil, nil)
	assert.Contains(t, p, "IGNORE the assistant's process notes")
	assert.NotContains(t, p, "LESSONS:")
	assert.Contains(t, p, "separate verified pass")
}

// Lessons live in the fleet-shared partition and surface through the
// dedicated lane for ANY agent — pass-7 measured per-agent lessons never
// recalled at all (invisible to 9/12 agents, outranked by echoes for the
// rest; access_count stayed 0).
func TestFleetLessonsSharedAcrossAgents(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       fleetLessonAgentID,
		Content:       "inserting 16-digit identifiers into an INTEGER column overflows — declare such columns BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: "runner-4o-09",
		Salience:      0.9,
	})
	require.NoError(t, err)

	// A DIFFERENT agent gets the lesson through the dedicated lane —
	// but only when fleet sharing is opted in.
	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
	}
	lessons := a.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000)
	require.Len(t, lessons, 1)
	assert.Contains(t, lessons[0].Content, "BIGINT")
}

// Sharing is opt-in: by default lessons are stored under the agent's own ID
// and another agent's lane must NOT see them.
func TestFleetLessonSharingIsOptIn(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	earner := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-09"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true},
	}
	assert.Equal(t, "runner-4o-09", earner.lessonPartition())

	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       earner.lessonPartition(),
		Content:       "inserting 16-digit identifiers into an INTEGER column overflows — declare such columns BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: "runner-4o-09",
		Salience:      0.9,
	})
	require.NoError(t, err)

	// The earner still recalls its own private lesson through the lane.
	require.Len(t, earner.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000), 1)

	// A different agent (sharing off) sees nothing.
	other := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true},
	}
	assert.Empty(t, other.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000))

	// The private lane must not leak the agent's ordinary memories either.
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID:    "runner-4o-09",
		Content:    "the INTEGER overflow discussion mentioned BIGINT columns in passing",
		MemoryType: memory.MemoryTypeFact,
		Salience:   0.9,
	})
	require.NoError(t, err)
	lessons := earner.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000)
	require.Len(t, lessons, 1)
	assert.Equal(t, memory.MemoryTypeLesson, lessons[0].MemoryType)
}

// The lesson type must survive ingestion instead of coercing to fact.
func TestLessonTypeIsValid(t *testing.T) {
	assert.True(t, isValidMemoryType("lesson"))
	assert.False(t, isValidMemoryType("hunch"))
}

func TestErrorLessonQuery(t *testing.T) {
	q := errorLessonQuery(`Error 2616: numeric overflow occurred during computation (column "CC_Number")`)
	assert.Contains(t, q, "overflow")
	assert.Contains(t, q, "CC_Number")
	assert.NotContains(t, q, `"`)
	assert.NotContains(t, q, "(")
	// Caps runaway error dumps; repeats dedupe to a single term.
	long := errorLessonQuery(strings.Repeat("verylongword ", 50) + strings.Repeat("uniqueword%d ", 30))
	assert.LessOrEqual(t, len(strings.Fields(long)), 24)
	assert.Equal(t, "verylongword", errorLessonQuery(strings.Repeat("verylongword ", 50)))
	assert.Equal(t, "", errorLessonQuery("!!! ()"))
}

// Regression: the words naming the actual failure must survive the term cap
// even when the error arrives wrapped in transport boilerplate. Measured
// live: a 12-term cap filled with wrapper words and "Numeric overflow" never
// entered the query, so the error lane recalled only generic lessons.
func TestErrorLessonQueryRealWrappedError(t *testing.T) {
	errText := `tool error: {"code":"db_error","message":"[Version 20.0.56] [Session 100746] ` +
		`[Teradata Database] [Error 2616] Numeric overflow occurred during computation.\n ` +
		`at github.com/Teradata-TIO/go-teradata.MakeError ErrorUtil.go:100"}`
	q := errorLessonQuery(errText)
	assert.Contains(t, q, "Numeric")
	assert.Contains(t, q, "overflow")
	assert.Contains(t, q, "computation")
	assert.Contains(t, q, "2616")
	// Deduped: "error" appears many times in the wrapper but once in the query.
	assert.Equal(t, 1, strings.Count(strings.ToLower(q), " error "))
}

// The error-triggered lane: an error text pulls the matching lesson into the
// conversation, repeats inject nothing, and the injection cap holds.
func TestInjectErrorLessons(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       fleetLessonAgentID,
		Content:       "numeric overflow inserting CC_Number into an INTEGER column — declare the column BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: "runner-4o-09",
		Salience:      0.9,
	})
	require.NoError(t, err)

	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
	}
	session := &types.Session{ID: "s-err-1"}

	errText := "Error 2616: numeric overflow occurred during computation of CC_Number"
	a.injectErrorLessons(ctx, session, []string{errText})

	msgs := session.GetMessages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "system", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "BIGINT")
	assert.Contains(t, msgs[0].Content, "matched to the error just hit")

	// Same error again: the lesson was already shown — nothing new lands.
	a.injectErrorLessons(ctx, session, []string{errText})
	assert.Len(t, session.GetMessages(), 1)

	// A different session gets its own delivery.
	session2 := &types.Session{ID: "s-err-2"}
	a.injectErrorLessons(ctx, session2, []string{errText})
	assert.Len(t, session2.GetMessages(), 1)

	// Teardown clears tracking.
	a.clearErrorLessonState(session.ID)
	a.errorLessonMu.Lock()
	_, present := a.errorLessonState[session.ID]
	a.errorLessonMu.Unlock()
	assert.False(t, present)
}

// The literal card-trap recovery: the re-CREATE swaps INTEGER for BIGINT.
// The diff must expose BIGINT so the lesson can be grounded in it.
func TestTokenDiffAndChangedFragments(t *testing.T) {
	added, removed := tokenDiff(
		`CREATE VOLATILE TABLE vt (card_id INTEGER, txns INTEGER, amt DECIMAL(14,2))`,
		`CREATE VOLATILE TABLE vt (card_id BIGINT, txns INTEGER, amt DECIMAL(18,2))`,
	)
	assert.Contains(t, added, "BIGINT")
	assert.Contains(t, added, "DECIMAL(18,2")
	assert.Contains(t, removed, "INTEGER")

	events := []minedEvent{
		{name: "execute_statement", key: "execute_statement:CREATE:VT", ok: true,
			input: `{"sql":"CREATE VOLATILE TABLE vt (card_id INTEGER, amt DECIMAL(14,2))"}`},
		{name: "execute_statement", key: "execute_statement:INSERT:VT", ok: false,
			input:   `{"sql":"INSERT INTO vt SELECT CC_Number, SUM(Amount) FROM t GROUP BY 1"}`,
			errText: "Error 2616 Numeric overflow occurred during computation"},
		{name: "execute_statement", key: "execute_statement:DROP:VT", ok: true,
			input: `{"sql":"DROP TABLE vt"}`},
		{name: "execute_statement", key: "execute_statement:CREATE:VT", ok: true,
			input: `{"sql":"CREATE VOLATILE TABLE vt (card_id BIGINT, amt DECIMAL(14,2))"}`},
		{name: "execute_statement", key: "execute_statement:INSERT:VT", ok: true,
			input: `{"sql":"INSERT INTO vt SELECT CC_Number, SUM(Amount) FROM t GROUP BY 1"}`},
	}
	pairs := pairEvents(events)
	require.Len(t, pairs, 1)
	// The INSERT itself never changed — the fragment comes from the
	// intervening re-CREATE diffed against the original CREATE.
	assert.Contains(t, pairs[0].ChangedFragments, "BIGINT")

	grounding := lessonGroundingTokens(pairs)
	assert.True(t, lessonIsGrounded("declare card_id as BIGINT to avoid overflow", grounding))
	assert.False(t, lessonIsGrounded("widen the DECIMAL precision to avoid overflow", grounding),
		"a lesson naming none of the observed changes must be dropped")
}

// Outcome credit end-to-end: a lesson injected before a recovery gains
// salience; one injected into a conversation that never recovers loses
// enough to sink below the lesson lane's recall floor.
func TestApplyLessonCredit(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	mkLesson := func(content string) string {
		m, err := store.Remember(ctx, &memory.Memory{
			AgentID:    fleetLessonAgentID,
			Content:    content,
			MemoryType: memory.MemoryTypeLesson,
			Source:     "lesson_mined",
			Salience:   0.4,
		})
		require.NoError(t, err)
		return m.ID
	}
	winID := mkLesson("numeric overflow on card columns — declare card_id BIGINT")
	lossID := mkLesson("numeric overflow — widen the DECIMAL precision")

	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
	}

	failThenRecover := []minedEvent{
		{key: "execute_statement:INSERT:VT", ok: false, errText: "overflow"},
		{key: "execute_statement:INSERT:VT", ok: true},
	}
	a.applyLessonCredit(ctx, failThenRecover, map[string]int{winID: 1})

	failForever := []minedEvent{
		{key: "execute_statement:INSERT:VT", ok: false, errText: "overflow"},
		{key: "execute_statement:INSERT:VT", ok: false, errText: "overflow"},
	}
	// A vacuous "recovery" (the error went away because the call did no
	// work) must score exactly like no recovery at all.
	vacuousRecovery := []minedEvent{
		{key: "execute_statement:INSERT:VT", ok: false, errText: "invalid date"},
		{key: "execute_statement:INSERT:VT", ok: true, vacuous: true},
	}
	// Four losing conversations sink the bad lesson below the floor.
	a.applyLessonCredit(ctx, vacuousRecovery, map[string]int{lossID: 1})
	for range [3]int{} {
		a.applyLessonCredit(ctx, failForever, map[string]int{lossID: 1})
	}

	win, err := store.GetMemory(ctx, fleetLessonAgentID, winID)
	require.NoError(t, err)
	assert.InDelta(t, 0.42, win.Salience, 0.001)
	loss, err := store.GetMemory(ctx, fleetLessonAgentID, lossID)
	require.NoError(t, err)
	assert.Less(t, loss.Salience, lessonMinSalience)

	// The demoted lesson no longer surfaces through the lesson lane.
	got := a.fleetLessons(ctx, "numeric overflow DECIMAL BIGINT", 4000)
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	assert.True(t, ids[winID])
	assert.False(t, ids[lossID], "demoted lesson must sink below the recall floor")
}

// Without fleet sharing, the error lane still serves the earning agent's
// own lessons — and nothing to anyone else.
func TestInjectErrorLessonsPrivate(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       "runner-4o-09",
		Content:       "numeric overflow inserting CC_Number into an INTEGER column — declare the column BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: "runner-4o-09",
		Salience:      0.9,
	})
	require.NoError(t, err)

	errText := "numeric overflow occurred during computation of CC_Number"

	owner := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-09"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true},
	}
	ownerSession := &types.Session{ID: "s-own"}
	owner.injectErrorLessons(ctx, ownerSession, []string{errText})
	assert.Len(t, ownerSession.GetMessages(), 1)

	other := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true},
	}
	otherSession := &types.Session{ID: "s-other"}
	other.injectErrorLessons(ctx, otherSession, []string{errText})
	assert.Empty(t, otherSession.GetMessages())
}
