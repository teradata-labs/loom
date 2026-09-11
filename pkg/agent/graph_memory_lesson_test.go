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
	lessons := a.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000, false)
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
	// Private lessons live in a DERIVED partition, never the agent's own —
	// which is where the per-turn extractor and the graph_memory tool write.
	assert.Equal(t, "runner-4o-09__lessons", earner.lessonPartition())
	assert.NotEqual(t, earner.config.Name, earner.lessonPartition())

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
	require.Len(t, earner.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000, false), 1)

	// A different agent (sharing off) sees nothing.
	other := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true},
	}
	assert.Empty(t, other.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000, false))

	// The private lane must not leak the agent's ordinary memories either.
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID:    "runner-4o-09",
		Content:    "the INTEGER overflow discussion mentioned BIGINT columns in passing",
		MemoryType: memory.MemoryTypeFact,
		Salience:   0.9,
	})
	require.NoError(t, err)
	lessons := earner.fleetLessons(ctx, "INTEGER overflow BIGINT column", 4000, false)
	require.Len(t, lessons, 1)
	assert.Equal(t, memory.MemoryTypeLesson, lessons[0].MemoryType)
}

// STRUCTURAL (blocking finding 1): the lesson lane must be unable to return a
// row the miner did not write. Both other ingestion paths — the per-turn
// extractor and the graph_memory tool — write under the agent's OWN name, so
// a lesson-typed memory sitting there (the shape a bypassed type gate would
// produce) must be invisible to the lane. Before the derived partition this
// was the same namespace and this memory came back as a "verified lesson".
func TestFleetLessonsCannotReturnNonMinerWrites(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-09"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true},
	}

	// What a non-miner writer produces: the agent's own partition, the
	// model's own salience, its own claim about a fix.
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       a.config.Name,
		Content:       "numeric overflow on CC_Number is fixed by widening the DECIMAL precision",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "agent",
		MemoryAgentID: a.config.Name,
		Salience:      1.0,
	})
	require.NoError(t, err)

	assert.Empty(t, a.fleetLessons(ctx, "numeric overflow CC_Number DECIMAL", 4000, false),
		"a memory written outside the miner's partition must never surface as a verified lesson")
	assert.Empty(t, a.fleetLessons(ctx, "numeric overflow CC_Number DECIMAL", 4000, true),
		"not through the re-trial slot either")

	// And the miner's own row in the derived partition does surface.
	_, err = store.Remember(ctx, &memory.Memory{
		AgentID:       a.lessonPartition(),
		Content:       "inserting 16-digit CC_Number values into an INTEGER column overflows — declare BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: a.config.Name,
		Salience:      0.9,
	})
	require.NoError(t, err)
	got := a.fleetLessons(ctx, "numeric overflow CC_Number INTEGER BIGINT", 4000, false)
	require.Len(t, got, 1)
	assert.Equal(t, "lesson_mined", got[0].Source)
}

// Blocking finding 1a: the per-turn lane may not mint a lesson. It reads
// mid-conversation prose — the measured source of the 58:8 wrong-theory
// poison — so a "lesson" it returns is dropped, not stored and not relabelled.
func TestPerTurnExtractorCannotMintLessons(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	response := `{"entities":[],"relationships":[],"memories":[
	  {"content":"numeric overflow is caused by the DECIMAL precision being too small","summary":"overflow theory","memory_type":"lesson","tags":["lesson"],"salience":1.0},
	  {"content":"the user is preparing a card transaction summary","summary":"task","memory_type":"fact","tags":[],"salience":0.6}
	]}`
	mockLLM := &extractionMockLLM{response: response}

	mem := NewMemory()
	session := mem.GetOrCreateSession(ctx, "s-lesson-gate")
	segMem := NewSegmentedMemory("", 200000, 20000)
	segMem.AddMessage(ctx, types.Message{Role: "user", Content: "summarize the card transactions"})
	session.SegmentedMem = segMem

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphMemoryConfig:           &loomv1.GraphMemoryConfig{Enabled: true, EnableExtraction: true},
		memory:                      mem,
		config:                      &Config{Name: "runner-4o-09"},
	}
	a.extractGraphMemoryAsync(ctx, "s-lesson-gate")

	// Everything this lane wrote, whatever type it ended up with.
	written, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "runner-4o-09", MinSalience: 0.01, Limit: 50,
	})
	require.NoError(t, err)
	require.NotEmpty(t, written, "ordinary per-turn extraction still works")
	sawFact := false
	for _, m := range written {
		assert.NotEqual(t, memory.MemoryTypeLesson, m.MemoryType,
			"the per-turn lane must not store lesson-typed memories")
		assert.NotContains(t, m.Content, "DECIMAL precision being too small",
			"and must not keep the same unverified theory under a relabelled type")
		if m.MemoryType == memory.MemoryTypeFact {
			sawFact = true
		}
	}
	assert.True(t, sawFact, "the ordinary user fact still landed")

	assert.Empty(t, a.fleetLessons(ctx, "numeric overflow DECIMAL precision", 4000, false),
		"and nothing it wrote may reach the verified lane")

	// The schema it is handed no longer offers the class at all.
	assert.NotContains(t, buildGraphMemoryExtractionPrompt(nil, 5, nil, nil), "observation|lesson")
}

// Blocking finding 1b: the model cannot write into the lane through the
// LLM-callable tool either — memory_type "lesson" is refused with a reason.
func TestGraphMemoryToolCannotMintLessons(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	tool := NewGraphMemoryTool(store, "runner-4o-09")
	res, err := tool.Execute(ctx, map[string]interface{}{
		"action":      "remember",
		"content":     "numeric overflow is fixed by widening the DECIMAL precision",
		"memory_type": "lesson",
		"salience":    1.0,
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Success)
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "reserved")

	// Case and padding are model output, not a contract.
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "remember", "content": "same claim", "memory_type": " Lesson ",
	})
	require.NoError(t, err)
	assert.False(t, res.Success)

	stored, err := store.Recall(ctx, memory.RecallOpts{
		AgentID: "runner-4o-09", MinSalience: 0.01, Limit: 10,
	})
	require.NoError(t, err)
	assert.Empty(t, stored, "nothing was written")

	// Ordinary types still work.
	res, err = tool.Execute(ctx, map[string]interface{}{
		"action": "remember", "content": "the card table is named vt_card_day", "memory_type": "fact",
	})
	require.NoError(t, err)
	assert.True(t, res.Success)
}

// The lesson class is the miner's alone: a valid stored type that neither
// other ingestion path may write.
func TestLessonTypeIsMinerOnly(t *testing.T) {
	assert.True(t, isMinerOnlyMemoryType("lesson"))
	assert.True(t, isMinerOnlyMemoryType(" LESSON "))
	assert.False(t, isMinerOnlyMemoryType("fact"))
	assert.False(t, isMinerOnlyMemoryType(""))
	assert.False(t, isPerTurnMemoryType("lesson"))
	assert.True(t, isPerTurnMemoryType("fact"))
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

	// The demoted lesson no longer surfaces through the ordinary lesson lane.
	got := a.fleetLessons(ctx, "numeric overflow DECIMAL BIGINT", 4000, false)
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
	owner := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-09"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true},
	}
	_, err := store.Remember(ctx, &memory.Memory{
		AgentID:       owner.lessonPartition(),
		Content:       "numeric overflow inserting CC_Number into an INTEGER column — declare the column BIGINT",
		MemoryType:    memory.MemoryTypeLesson,
		Source:        "lesson_mined",
		MemoryAgentID: "runner-4o-09",
		Salience:      0.9,
	})
	require.NoError(t, err)

	errText := "numeric overflow occurred during computation of CC_Number"

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

// Blocking finding 2a: "nothing happened after the injection" is not a loss.
// The ledger drains at the end of every Chat() call, so an injection landing
// on the turn's last tool call is the NORMAL ending of an interactive turn —
// scoring it as a loss demoted lessons for existing rather than for failing.
func TestApplyLessonCreditNoPostInjectionEvidence(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
	}
	mk := func(content string) string {
		m, err := store.Remember(ctx, &memory.Memory{
			AgentID: fleetLessonAgentID, Content: content,
			MemoryType: memory.MemoryTypeLesson, Source: "lesson_mined", Salience: 0.8,
		})
		require.NoError(t, err)
		return m.ID
	}
	salience := func(id string) float64 {
		m, err := store.GetMemory(ctx, fleetLessonAgentID, id)
		require.NoError(t, err)
		return m.Salience
	}

	// The turn ended right after the injection: idx == len(events).
	emptyTail := mk("declare 16-digit identifier columns BIGINT")
	events := []minedEvent{
		{key: "execute_statement:INSERT VT", ok: false, errText: "overflow"},
	}
	a.applyLessonCredit(ctx, events, map[string]int{emptyTail: len(events)})
	assert.InDelta(t, 0.8, salience(emptyTail), 0.0001, "an empty tail must score nothing")

	// A stale/overshot position is the same non-observation.
	overshot := mk("cast wide identifiers before comparing them")
	a.applyLessonCredit(ctx, events, map[string]int{overshot: len(events) + 5})
	assert.InDelta(t, 0.8, salience(overshot), 0.0001)

	// Tail with only unrelated successful work: neither vindicated nor
	// contradicted, so untouched.
	unrelated := mk("volatile tables need ON COMMIT PRESERVE ROWS")
	a.applyLessonCredit(ctx, []minedEvent{
		{key: "execute_statement:INSERT VT", ok: false, errText: "overflow"},
		{key: "execute_query:SELECT DBC", ok: true},
	}, map[string]int{unrelated: 1})
	assert.InDelta(t, 0.8, salience(unrelated), 0.0001)

	// An observed continued failure still earns the loss — the signal the
	// mechanism exists for is intact.
	contradicted := mk("widen the DECIMAL precision")
	a.applyLessonCredit(ctx, []minedEvent{
		{key: "execute_statement:INSERT VT", ok: false, errText: "overflow"},
		{key: "execute_statement:INSERT VT", ok: false, errText: "overflow"},
	}, map[string]int{contradicted: 1})
	assert.InDelta(t, 0.8+lessonCreditLoss, salience(contradicted), 0.0001)
}

// Blocking finding 2b: demotion must not be absorbing. A demoted lesson was
// unreachable — never recalled, so never injected, so unable to earn the only
// signal that could raise it. It now rides a bounded re-trial slot on the
// lane that scores outcomes, and one observed recovery reinstates it.
func TestDemotedLessonGetsRetrialAndCanReturn(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	a := &Agent{
		graphMemoryStore:  store,
		config:            &Config{Name: "runner-4o-01"},
		graphMemoryConfig: &loomv1.GraphMemoryConfig{Enabled: true, FleetLessonSharing: true},
	}
	demoted, err := store.Remember(ctx, &memory.Memory{
		AgentID:    fleetLessonAgentID,
		Content:    "numeric overflow on wide identifiers — declare the column BIGINT",
		MemoryType: memory.MemoryTypeLesson,
		Source:     "lesson_mined",
		Salience:   0.05, // sunk to the AdjustSalience clamp
	})
	require.NoError(t, err)

	query := "numeric overflow BIGINT identifiers"
	// The ordinary lane cannot see it — demotion is real.
	require.Empty(t, a.fleetLessons(ctx, query, 4000, false))

	// The error lane re-tries it: one recall in lessonRetrialInterval carries
	// a demoted candidate, so it is reachable again within a session.
	seen := 0
	for i := 0; i < lessonRetrialInterval*2; i++ {
		for _, m := range a.fleetLessons(ctx, query, 4000, true) {
			if m.ID == demoted.ID {
				seen++
				assert.True(t, lessonOnProbation(m))
			}
		}
	}
	require.GreaterOrEqual(t, seen, 1, "a demoted lesson must remain reachable for re-evaluation")
	assert.LessOrEqual(t, seen, 2, "and only in the bounded re-trial slot")

	// It is delivered honestly, not as a verified fix.
	session := &types.Session{ID: "s-probation"}
	for i := 0; i < lessonRetrialInterval*2 && len(session.GetMessages()) == 0; i++ {
		a.injectErrorLessons(ctx, session, []string{"Error 2616 numeric overflow BIGINT identifiers"})
	}
	msgs := session.GetMessages()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Content, "Under re-trial")

	// Winning the re-trial reinstates it exactly to the recall floor: one
	// observed recovery readmits, one observed failure demotes again.
	a.applyLessonCredit(ctx, []minedEvent{
		{key: "execute_statement:INSERT VT", ok: false, errText: "overflow"},
		{key: "execute_statement:INSERT VT", ok: true},
	}, map[string]int{demoted.ID: 1})

	back, err := store.GetMemory(ctx, fleetLessonAgentID, demoted.ID)
	require.NoError(t, err)
	assert.InDelta(t, lessonMinSalience, back.Salience, 0.0001)
	require.Len(t, a.fleetLessons(ctx, query, 4000, false), 1,
		"a reinstated lesson is served by the ordinary lane again")
}

// Major finding 3: the transient-retry shape — rate limit, then the IDENTICAL
// input succeeding — changed nothing. It must be dropped before mining, not
// handed to the miner with the grounding gate switched off (which is what an
// all-empty fragment set used to do).
func TestFragmentlessTransientRetryIsNotMined(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	// A plausible invented cause that names nothing observed.
	mockLLM := &extractionMockLLM{response: `{"entities":[],"relationships":[],"memories":[
	  {"content":"queries against the transactions table must be issued with a smaller batch size to avoid rate limiting","summary":"batch size","memory_type":"lesson","tags":["lesson"],"salience":0.9}
	]}`}

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphMemoryConfig:           &loomv1.GraphMemoryConfig{Enabled: true},
		memory:                      NewMemory(),
		config:                      &Config{Name: "runner-4o-01"},
	}

	call := types.ToolCall{ID: "1", Name: "execute_query",
		Input: map[string]interface{}{"sql": "SELECT COUNT(*) FROM transactions"}}
	a.recordToolLedger("s-retry", call, &shuttle.Result{Success: false,
		Error: &shuttle.Error{Code: "rate_limit", Message: "429 Too Many Requests"}})
	a.recordToolLedger("s-retry", call, &shuttle.Result{Success: true, Data: `{"row_count":1,"rows":[[42]]}`})

	// The pair exists mechanically but carries no observed change.
	peek := pairEvents([]minedEvent{
		{name: "execute_query", key: eventClass(call), input: eventInputPreview(call), ok: false, errText: "429"},
		{name: "execute_query", key: eventClass(call), input: eventInputPreview(call), ok: true},
	})
	require.Len(t, peek, 1)
	require.Empty(t, peek[0].ChangedFragments)
	assert.Empty(t, groundedPairs(peek))

	a.extractLessonsAtEnd(ctx, "s-retry")

	assert.Equal(t, 0, mockLLM.getCalls(),
		"a pair with nothing to explain must never reach the miner")
	assert.Empty(t, a.fleetLessons(ctx, "batch size rate limiting transactions", 4000, false))
}

// extractLessonsAtEnd end-to-end: the store path. A grounded lesson lands in
// the lesson partition (never the agent's own), at the verified-salience
// floor regardless of what the miner asked for, tagged and attributed; an
// ungrounded sibling is dropped.
func TestExtractLessonsAtEndStorePath(t *testing.T) {
	store := newTestGraphMemoryStore(t)
	ctx := context.Background()

	mockLLM := &extractionMockLLM{response: `{"entities":[],"relationships":[],"memories":[
	  {"content":"16-digit identifiers overflow an INTEGER column — declare such columns BIGINT","summary":"declare BIGINT","memory_type":"lesson","tags":["schema"],"salience":0.1},
	  {"content":"numeric overflow is really about the DECIMAL precision being too small","summary":"decimal theory","memory_type":"lesson","tags":["schema"],"salience":0.95}
	]}`}

	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            store,
		enableGraphMemoryExtraction: true,
		graphMemoryConfig:           &loomv1.GraphMemoryConfig{Enabled: true},
		memory:                      NewMemory(),
		config:                      &Config{Name: "runner-4o-09"},
	}

	create := func(colType string) types.ToolCall {
		return types.ToolCall{Name: "execute_statement", Input: map[string]interface{}{
			"sql": "CREATE VOLATILE TABLE vt_card_day (card_id " + colType + ", amt DECIMAL(14,2))"}}
	}
	insert := types.ToolCall{Name: "execute_statement", Input: map[string]interface{}{
		"sql": "INSERT INTO vt_card_day SELECT CC_Number, SUM(Amount) FROM txns GROUP BY 1"}}

	a.recordToolLedger("s-mine", create("INTEGER"), &shuttle.Result{Success: true})
	a.recordToolLedger("s-mine", insert, &shuttle.Result{Success: false,
		Error: &shuttle.Error{Code: "db_error", Message: "[Error 2616] Numeric overflow occurred during computation."}})
	a.recordToolLedger("s-mine", types.ToolCall{Name: "execute_statement",
		Input: map[string]interface{}{"sql": "DROP TABLE vt_card_day"}}, &shuttle.Result{Success: true})
	a.recordToolLedger("s-mine", create("BIGINT"), &shuttle.Result{Success: true})
	a.recordToolLedger("s-mine", insert, &shuttle.Result{Success: true,
		Data: `{"activity_count":55316,"status":"success"}`})

	a.extractLessonsAtEnd(ctx, "s-mine")

	require.Equal(t, 1, mockLLM.getCalls(), "one mining call for the whole pass")

	lessons := a.fleetLessons(ctx, "INTEGER overflow BIGINT identifiers DECIMAL precision", 4000, false)
	require.Len(t, lessons, 1, "the ungrounded sibling is dropped by the grounding gate")
	got := lessons[0]
	assert.Contains(t, got.Content, "BIGINT")
	assert.Equal(t, memory.MemoryTypeLesson, got.MemoryType)
	assert.Equal(t, "lesson_mined", got.Source)
	assert.Equal(t, "runner-4o-09", got.MemoryAgentID, "provenance: who verified it")
	assert.Contains(t, got.Tags, "lesson")
	assert.InDelta(t, lessonSalienceFloor, got.Salience, 0.0001,
		"a verified lesson is stored at the floor, not at the miner's self-assessment")

	// It is NOT in the agent's own partition — the one other writers use.
	own, err := store.Recall(ctx, memory.RecallOpts{AgentID: "runner-4o-09", MinSalience: 0.01, Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, own)

	// The ledger was consumed: a second pass has nothing to mine and makes no
	// LLM call.
	a.extractLessonsAtEnd(ctx, "s-mine")
	assert.Equal(t, 1, mockLLM.getCalls(), "no pairs, no LLM call")
}
