// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// recordLLMResponse writes the llm_call_intent and llm_call_result events the
// loop writes around one model call.
func recordLLMResponse(t *testing.T, log eventlog.EventLogStore, sessionID string, step int, payload []byte) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, log.RecordIntent(ctx, sessionID, step, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)))
	require.NoError(t, log.RecordResult(ctx, sessionID, step, 0, eventlog.EventLLMCallResult, "", payload))
}

func countToolBatches(sess *Session) (batches int) {
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			batches++
		}
	}
	return batches
}

// The model answered with tool calls and the recorded response is in the log,
// but the process died before the assistant message was saved or any call was
// started. Resume must use the recorded response: one model call (the reply
// after the tools), not two, and the tool runs exactly once.
func TestResumeSession_RecordedResponseIsReplayedNotRequestedAgain(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)
	tool := &crashingTool{}
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, sessions, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	payload, err := json.Marshal(LLMResponse{
		Content:   "running it",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}}},
	})
	require.NoError(t, err)
	recordLLMResponse(t, recorder, "s-replay", 0, payload)

	resp, err := ag.ResumeSession(ctx, "s-replay")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)
	require.Equal(t, 1, llm.calls, "only the reply after the tool needed the model; the recorded response was asked for again")
	require.Equal(t, 1, tool.calls)

	sess = ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay", ag.config.Name, "")
	require.Equal(t, 1, countToolBatches(sess), "the replayed batch is saved once")
	require.NotNil(t, toolMessage(sess, "c1"))
	var content string
	for _, m := range rawSessionMessages(sess) {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			content = m.Content
		}
	}
	require.Equal(t, "running it", content, "the rebuilt message keeps the response's text")

	// A second resume has nothing left to do.
	_, err = ag.ResumeSession(ctx, "s-replay")
	require.ErrorIs(t, err, ErrNothingParked)
}

// The batch was saved normally: resume must not add a second copy of it.
func TestResumeSession_SavedBatchIsNotReplayedAgain(t *testing.T) {
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, nil, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-saved", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)

	_, err := ag.ResumeSession(ctx, "s-replay-saved")
	require.NoError(t, err)
	sess = ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-saved", ag.config.Name, "")
	require.Equal(t, 1, countToolBatches(sess), "a saved batch was added again")
	require.Equal(t, 1, tool.calls)
}

// A recorded response without tool calls, or one that cannot be decoded, is not
// replayed: the existing path asks the model again.
func TestResumeSession_NotReplayed_TextOnlyOrUndecodable(t *testing.T) {
	for name, payload := range map[string][]byte{
		"text only":   []byte(`{"content":"hello"}`),
		"undecodable": []byte(`not json`),
	} {
		t.Run(name, func(t *testing.T) {
			recorder := &recordingEventLog{}
			tool := &crashingTool{}
			llm := &scriptedLLM{}
			ag := newEventLogAgent(t, llm, nil, recorder, tool)
			ctx := context.Background()
			sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-no", ag.config.Name, "")
			ag.appendMessage(ctx, sess, Message{Role: "user", Content: "hi"}, true)
			recordLLMResponse(t, recorder, "s-replay-no", 0, payload)

			resp, err := ag.ResumeSession(ctx, "s-replay-no")
			require.NoError(t, err)
			require.Equal(t, "done", resp.Content)
			require.Equal(t, 1, llm.calls, "the model is asked again")
			require.Zero(t, tool.calls)
		})
	}
}

// The turn already ran one batch (saved, answered by its tool row); the second
// response is recorded but unsaved. Replay puts it after the first batch.
func TestResumeSession_RecordedResponseAfterAnAnsweredBatch_IsReplayed(t *testing.T) {
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, nil, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-2", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy twice"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	ag.appendMessage(ctx, sess, Message{Role: "tool", ToolUseID: "c1", Content: "ok"}, false)
	payload, err := json.Marshal(LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{ID: "c2", Name: "crashy", Input: map[string]interface{}{"n": 2}}},
	})
	require.NoError(t, err)
	recordLLMResponse(t, recorder, "s-replay-2", 1, payload)

	_, err = ag.ResumeSession(ctx, "s-replay-2")
	require.NoError(t, err)
	require.Equal(t, 1, llm.calls, "only the final reply needed the model")
	require.Equal(t, 1, tool.calls, "c2 ran; c1 already had its row")
	sess = ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-2", ag.config.Name, "")
	require.Equal(t, 2, countToolBatches(sess))
	require.NotNil(t, toolMessage(sess, "c2"))
}

// The recorded batch was saved but later folded out of the live view by compression.
// It is saved, not lost: replay must not add a second copy.
func TestResumeSession_FoldedBatchIsNotReplayedAgain(t *testing.T) {
	kept := llmtypes.ToolCall{ID: "toolu_kept", Name: "crashy", Input: map[string]interface{}{"n": float64(1)}}
	filler := strings.Repeat("grant audit 123 db=SALES cpu=456; ", 100)

	store := newSQLiteSessions(t)
	ctx := context.Background()
	const sessionID = "sess-replay-folded"
	require.NoError(t, store.SaveSession(ctx, &Session{ID: sessionID, Context: map[string]interface{}{}}))

	sm := NewSegmentedMemory("ROM", 6000, 600)
	sm.SetThreshold(6000)
	sm.SetSessionStore(store, sessionID)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-999\nfolded conversation"})
	save := func(m Message, turnStart bool) {
		require.NoError(t, store.SaveMessage(ctx, sessionID, &m, turnStart))
		sm.AddMessage(ctx, m)
	}
	save(Message{Role: "user", Content: "turn 1: " + filler}, true)
	save(Message{Role: "assistant", ToolCalls: []llmtypes.ToolCall{kept}}, false)
	for turn := 2; turn <= 7; turn++ {
		save(Message{Role: "user", Content: fmt.Sprintf("turn %d: ", turn) + filler}, true)
		save(Message{Role: "assistant", Content: fmt.Sprintf("noted %d", turn)}, false)
	}
	shed, _, _ := sm.ReleasePressure(ctx, 0)
	require.True(t, shed, "the conversation must be heavy enough to fold")
	save(Message{Role: "user", Content: "turn 8: " + filler}, true) // a new turn, unanswered

	recorder := &recordingEventLog{}
	payload, err := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{kept}})
	require.NoError(t, err)
	recordLLMResponse(t, recorder, sessionID, 0, payload) // the folded batch's response is still in the log
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, store, recorder, &crashingTool{})

	_, err = ag.ResumeSession(ctx, sessionID)
	require.NoError(t, err)
	rows, err := store.ListMessagesBySeqRange(ctx, sessionID, 1, 1<<40)
	require.NoError(t, err)
	copies := 0
	for _, m := range rows {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.ToolCalls[0].ID == kept.ID {
			copies++
		}
	}
	require.Equal(t, 1, copies, "the folded batch was replayed as a second copy")
	require.Equal(t, 1, llm.calls, "the unanswered turn is answered by a fresh model call")
}

// Every call of the batch has its tool row and the next response has not been
// asked for yet: the batch is saved, so replay adds nothing.
func TestResumeSession_AnsweredSavedBatchIsNotReplayedAgain(t *testing.T) {
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, nil, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-answered", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)
	recordTailBatch(t, recorder, sess, 0)
	ag.appendMessage(ctx, sess, Message{Role: "tool", ToolUseID: "c1", Content: "ok"}, false)

	_, err := ag.ResumeSession(ctx, "s-replay-answered")
	require.NoError(t, err)
	sess = ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-answered", ag.config.Name, "")
	require.Equal(t, 1, countToolBatches(sess))
	require.Zero(t, tool.calls)
}

// The turn already has its final reply (its events were not pruned). A recorded
// response that no message holds must not be appended after that reply.
func TestResumeSession_FinishedTurnIsNotReplayedInto(t *testing.T) {
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	ag := newEventLogAgent(t, &scriptedLLM{}, nil, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-finished", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "hi"}, true)
	ag.appendMessage(ctx, sess, Message{Role: "assistant", Content: "hello"}, false)
	payload, err := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}}})
	require.NoError(t, err)
	recordLLMResponse(t, recorder, "s-replay-finished", 0, payload)

	_, err = ag.ResumeSession(ctx, "s-replay-finished")
	require.ErrorIs(t, err, ErrNothingParked)
	require.Zero(t, countToolBatches(ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-finished", ag.config.Name, "")))
	require.Zero(t, tool.calls)
}
