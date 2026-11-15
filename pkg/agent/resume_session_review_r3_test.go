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
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// A resume whose replay is refused (ErrBatchNotSaved for an earlier unsaved
// step) must leave the session exactly as it was: the replayed assistant row
// used to be appended before the refusal ran, so a refused resume silently
// grew the session by one row and traced replayed_llm_response it never used.
func TestReview590R3_RefusedResumeAddsNoReplayedRow(t *testing.T) {
	store := newSQLiteSessions(t)
	recorder := &recordingEventLog{}
	ctx := context.Background()
	const id = "s-r3-q3"
	seed := newEventLogAgent(t, &scriptedLLM{}, store, recorder, &crashingTool{})
	sess := seed.memory.GetOrCreateSessionWithAgent(ctx, id, seed.config.Name, "")
	seed.appendMessage(ctx, sess, Message{Role: "user", Content: "go"}, true)
	turn := lastMessageTurn(sess)
	p0, _ := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": float64(1)}}}})
	recordLLMResponse(t, recorder, id, 0, turn, p0)
	require.NoError(t, recorder.RecordIntent(ctx, id, 0, 0, eventlog.EventToolCallIntent, "c1", []byte(`{"tool_name":"crashy"}`)))
	require.NoError(t, recorder.RecordResult(ctx, id, 0, 0, eventlog.EventToolCallResult, "c1", []byte(`{"success":true}`)))
	tool := Message{Role: "tool", ToolUseID: "c1", Content: "ok"} // tool row saved; its assistant row did not
	require.NoError(t, store.SaveMessage(ctx, id, &tool, false))
	p1, _ := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{{ID: "c2", Name: "crashy", Input: map[string]interface{}{"n": float64(2)}}}})
	recordLLMResponse(t, recorder, id, 1, turn, p1)
	before, _ := store.ListMessagesBySeqRange(ctx, id, 1, math.MaxInt64)
	fresh := newEventLogAgent(t, &scriptedLLM{}, store, recorder, &crashingTool{})
	_, err := fresh.ResumeSession(ctx, id)
	require.ErrorIs(t, err, ErrBatchNotSaved)
	after, _ := store.ListMessagesBySeqRange(ctx, id, 1, math.MaxInt64)
	require.Len(t, after, len(before), "a refused resume added a replayed assistant row")
}

// An llm_call_intent recorded before answered_turn existed carries no turn:
// the replay must refuse (fail closed) and the interrupted call is made again.
func TestReview590R3_IntentWithoutAnsweredTurnIsNotReplayed(t *testing.T) {
	store := newSQLiteSessions(t)
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, store, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-r3-no-turn", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	// A pre-upgrade intent: no answered_turn in its payload. Its response was
	// recorded but the assistant message never saved and no call started.
	payload, err := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}}}})
	require.NoError(t, err)
	require.NoError(t, recorder.RecordIntent(ctx, "s-r3-no-turn", 0, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)))
	require.NoError(t, recorder.RecordResult(ctx, "s-r3-no-turn", 0, 0, eventlog.EventLLMCallResult, "", payload))

	resp, err := ag.ResumeSession(ctx, "s-r3-no-turn")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)
	require.Zero(t, tool.calls, "the recorded batch ran although the intent carries no answered_turn")
	require.Equal(t, 1, llm.calls, "the interrupted call is made again")
	sess = ag.memory.GetOrCreateSessionWithAgent(ctx, "s-r3-no-turn", ag.config.Name, "")
	require.Zero(t, countToolBatches(sess), "the recorded batch was appended to the session")
}
