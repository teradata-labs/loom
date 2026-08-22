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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	prompt := buildLessonMiningPrompt(pairs)
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

func TestSQLClass(t *testing.T) {
	assert.Equal(t, "INSERT VT_CARD_DAY", sqlClass("insert into vt_card_day (a,b) select ..."))
	assert.Equal(t, "CREATE VT_X", sqlClass("CREATE VOLATILE TABLE vt_x (a int)")) // keys on the actual table name
	assert.Equal(t, "SELECT T", sqlClass("SELECT * FROM t WHERE 1=1"))
	assert.Equal(t, "", sqlClass("  "))
}

// The mining prompt states the evidence contract and the skip rule.
func TestLessonMiningPrompt(t *testing.T) {
	p := buildLessonMiningPrompt([]lessonPair{{Tool: "x", FailingIn: "{a}", ErrorText: "boom", SucceedsIn: "{b}"}})
	assert.Contains(t, p, "VERIFIED fix")
	assert.Contains(t, p, "Skip an item")
	assert.Contains(t, p, `"memory_type": "lesson"`)
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

// The lesson type must survive ingestion instead of coercing to fact.
func TestLessonTypeIsValid(t *testing.T) {
	assert.True(t, isValidMemoryType("lesson"))
	assert.False(t, isValidMemoryType("hunch"))
}
