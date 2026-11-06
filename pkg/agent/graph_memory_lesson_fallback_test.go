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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// storedLessons lists every lesson in the agent's lesson partition,
// demoted ones included.
func storedLessons(t *testing.T, a *Agent) []*memory.Memory {
	t.Helper()
	got, err := a.graphMemoryStore.Recall(context.Background(), memory.RecallOpts{
		AgentID:     a.lessonPartition(),
		MemoryType:  memory.MemoryTypeLesson,
		MinSalience: lessonProbationFloor,
		Limit:       50,
	})
	require.NoError(t, err)
	return got
}

// F1: a chat-only turn (empty ledger) after a tool turn that minted a lesson
// must not re-mine the earlier turn's pair from the session history. Before
// the fix, the empty-ledger fallback walked the whole session, found turn 1's
// pair again, made a second LLM call and stored a duplicate lesson.
func TestChatOnlyTurnDoesNotReMineEarlierPair(t *testing.T) {
	ctx := context.Background()
	mockLLM := &extractionMockLLM{response: `{"entities":[],"relationships":[],"memories":[
	  {"content":"16-digit identifiers overflow an INTEGER column — declare such columns BIGINT","summary":"declare BIGINT","memory_type":"lesson","tags":["schema"],"salience":0.9}
	]}`}
	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            newTestGraphMemoryStore(t),
		enableGraphMemoryExtraction: true,
		graphMemoryConfig:           &loomv1.GraphMemoryConfig{Enabled: true},
		memory:                      NewMemory(),
		config:                      &Config{Name: "runner-chat-only"},
	}
	const sid = "s-chat-only"
	session := a.memory.GetOrCreateSession(ctx, sid)

	// Turn 1: an error→fix transition, in BOTH the session history and the
	// ledger, the way the conversation loop records it.
	turn1 := []struct {
		id, sql, errMsg string
	}{
		{"c1", "CREATE VOLATILE TABLE vt (card_id INTEGER)", ""},
		{"c2", "INSERT INTO vt SELECT CC_Number FROM txns", "[Error 2616] Numeric overflow occurred during computation."},
		{"c3", "DROP TABLE vt", ""},
		{"c4", "CREATE VOLATILE TABLE vt (card_id BIGINT)", ""},
		{"c5", "INSERT INTO vt SELECT CC_Number FROM txns", ""},
	}
	session.AddMessage(ctx, types.Message{Role: "user", Content: "load the card table"})
	for _, c := range turn1 {
		call := callMsg(c.id, "execute_statement", c.sql)
		session.AddMessage(ctx, call)
		session.AddMessage(ctx, resultMsg(c.id, c.errMsg == "", c.errMsg))
		res := &shuttle.Result{Success: c.errMsg == ""}
		if c.errMsg != "" {
			res.Error = &shuttle.Error{Code: "db_error", Message: c.errMsg}
		} else {
			res.Data = `{"activity_count":3}`
		}
		a.recordToolLedger(sid, call.ToolCalls[0], res)
	}
	session.AddMessage(ctx, types.Message{Role: "assistant", Content: "Loaded."})
	a.extractLessonsAtEnd(ctx, sid)
	require.Equal(t, 1, mockLLM.getCalls(), "turn 1 mines its pair once")
	require.Len(t, storedLessons(t, a), 1)

	// Turns 2 and 3: chat only — no tool call, so the ledger is empty.
	for _, text := range []string{"thanks", "great, thanks again"} {
		session.AddMessage(ctx, types.Message{Role: "user", Content: text})
		session.AddMessage(ctx, types.Message{Role: "assistant", Content: "You're welcome."})
		a.extractLessonsAtEnd(ctx, sid)
	}

	assert.Equal(t, 1, mockLLM.getCalls(), "a chat-only turn must make no mining call")
	assert.Len(t, storedLessons(t, a), 1, "exactly one lesson in total — no duplicate")
}

// F1, the case the fallback exists for: a session this agent never recorded
// a ledger for (restored from storage) is mined from its history once, and
// only once.
func TestRestoredSessionFallbackMinesOnce(t *testing.T) {
	ctx := context.Background()
	mockLLM := &extractionMockLLM{response: `{"entities":[],"relationships":[],"memories":[
	  {"content":"16-digit identifiers overflow an INTEGER column — declare such columns BIGINT","summary":"declare BIGINT","memory_type":"lesson","tags":["schema"],"salience":0.9}
	]}`}
	a := &Agent{
		llm:                         mockLLM,
		graphMemoryStore:            newTestGraphMemoryStore(t),
		enableGraphMemoryExtraction: true,
		graphMemoryConfig:           &loomv1.GraphMemoryConfig{Enabled: true},
		memory:                      NewMemory(),
		config:                      &Config{Name: "runner-restored"},
	}
	const sid = "s-restored"
	session := a.memory.GetOrCreateSession(ctx, sid)
	for _, m := range []types.Message{
		{Role: "user", Content: "fix the insert"},
		callMsg("1", "execute_query", "INSERT INTO t VALUES (CAST(x AS INTEGER))"),
		resultMsg("1", false, "numeric overflow"),
		callMsg("2", "execute_query", "INSERT INTO t VALUES (CAST(x AS BIGINT))"),
		resultMsg("2", true, ""),
	} {
		session.AddMessage(ctx, m)
	}

	a.extractLessonsAtEnd(ctx, sid)
	assert.Equal(t, 1, mockLLM.getCalls(), "a session with no recorded ledger falls back to its history")
	a.extractLessonsAtEnd(ctx, sid)
	assert.Equal(t, 1, mockLLM.getCalls(), "the fallback runs at most once per session")

	// DeleteSession forgets the marker with the rest of the per-session state.
	a.markLessonHistoryCovered(sid)
	a.forgetLessonSession(sid)
	a.toolLedgerMu.Lock()
	_, present := a.lessonCoveredSessions[sid]
	a.toolLedgerMu.Unlock()
	assert.False(t, present)
}
