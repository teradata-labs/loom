// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// recordLLMResponse writes the llm_call_intent and llm_call_result events the
// loop writes around one model call.
// recordLLMResponse records a step's LLM intent and result the way the loop
// does: the intent names the turn the response answers (answered_turn).
func recordLLMResponse(t *testing.T, log eventlog.EventLogStore, sessionID string, step int, answeredTurn int64, payload []byte) {
	t.Helper()
	ctx := context.Background()
	intent := []byte(fmt.Sprintf(`{"model":"test","answered_turn":%d}`, answeredTurn))
	require.NoError(t, log.RecordIntent(ctx, sessionID, step, 0, eventlog.EventLLMCallIntent, "", intent))
	require.NoError(t, log.RecordResult(ctx, sessionID, step, 0, eventlog.EventLLMCallResult, "", payload))
}

// lastMessageTurn is the Turn of the session's last message, the turn a response
// recorded now answers.
func lastMessageTurn(sess *Session) int64 {
	msgs := rawSessionMessages(sess)
	return msgs[len(msgs)-1].Turn
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
	recordLLMResponse(t, recorder, "s-replay", 0, lastMessageTurn(sess), payload)

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
			recordLLMResponse(t, recorder, "s-replay-no", 0, lastMessageTurn(sess), payload)

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
	recordLLMResponse(t, recorder, "s-replay-2", 1, lastMessageTurn(sess), payload)

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
	turn8 := Message{Role: "user", Content: "turn 8: " + filler} // a new turn, unanswered
	require.NoError(t, store.SaveMessage(ctx, sessionID, &turn8, true))
	sm.AddMessage(ctx, turn8)
	require.NotZero(t, turn8.Turn, "the store stamps the turn")

	recorder := &recordingEventLog{}
	payload, err := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{kept}})
	require.NoError(t, err)
	recordLLMResponse(t, recorder, sessionID, 0, turn8.Turn, payload) // the folded batch's response is still in the log
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
	recordLLMResponse(t, recorder, "s-replay-finished", 0, lastMessageTurn(sess), payload)

	_, err = ag.ResumeSession(ctx, "s-replay-finished")
	require.ErrorIs(t, err, ErrNothingParked)
	require.Zero(t, countToolBatches(ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-finished", ag.config.Name, "")))
	require.Zero(t, tool.calls)
}

// A turn the loop already failed (here: the output-token circuit breaker's
// exit) leaves its recorded response in the log with no tool intents, because
// pruneFinishedTurn runs only on success. That stale result must not be
// replayed as the answer to a LATER turn: the model chose that batch for the
// failed turn, not for the new user message. The replay candidate must be the
// response to the highest recorded llm_call_intent — the call the crash
// actually interrupted.
func TestResumeSession_StaleResultFromEarlierTurnIsNotReplayed(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)
	tool := &crashingTool{}
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, sessions, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-stale", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "first turn"}, true)
	// The failed turn: the model hit max_tokens with one truncated tool call,
	// the response was recorded, and the turn ended before the assistant
	// message was saved and before any call started.
	payload, err := json.Marshal(LLMResponse{
		StopReason: "max_tokens",
		Content:    "runn",
		ToolCalls:  []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}}},
	})
	require.NoError(t, err)
	recordLLMResponse(t, recorder, "s-replay-stale", 0, lastMessageTurn(sess), payload)
	// A later turn: the user's message is saved, its first LLM call recorded
	// only its intent, and the process died mid-call.
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "second turn"}, true)
	require.NoError(t, recorder.RecordIntent(ctx, "s-replay-stale", 1, 0, eventlog.EventLLMCallIntent, "", []byte(`{"model":"test"}`)))

	resp, err := ag.ResumeSession(ctx, "s-replay-stale")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)
	require.Zero(t, tool.calls, "the failed turn's recorded batch ran as the answer to the new turn")
	require.Equal(t, 1, llm.calls, "the interrupted call is made again")
	sess = ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-stale", ag.config.Name, "")
	require.Zero(t, countToolBatches(sess), "the stale batch was appended to the session")
}

// cbTruncatedTool demands an argument, so a max_tokens response whose call
// lacks it classifies as truncated — the input the circuit breaker fires on.
type cbTruncatedTool struct{ calls int }

func (t *cbTruncatedTool) Name() string        { return "needs_query" }
func (t *cbTruncatedTool) Description() string { return "requires a query argument" }
func (t *cbTruncatedTool) InputSchema() *shuttle.JSONSchema {
	return &shuttle.JSONSchema{
		Type:       "object",
		Properties: map[string]*shuttle.JSONSchema{"q": {Type: "string"}},
		Required:   []string{"q"},
	}
}
func (t *cbTruncatedTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	t.calls++
	return &shuttle.Result{Success: true, Data: "ok"}, nil
}
func (t *cbTruncatedTool) Backend() string { return "" }

// The real way the stale result of the previous test is produced: the loop
// records a truncated response, the output-token circuit breaker trips and
// ends the turn before the assistant message is appended. A resume of that
// same failed turn must not dispatch the truncated calls the breaker refused.
func TestResumeSession_CircuitBreakerExitResultIsNotReplayed(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)
	tool := &cbTruncatedTool{}
	buildAgent := func(llm *scriptedLLM) *Agent {
		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		cfg.OutputTokenCBThreshold = 1
		ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithEventLog(recorder), WithMemory(NewMemoryWithStore(sessions)))
		ag.tools.Register(tool)
		return ag
	}

	// The turn trips the breaker: the response is recorded, the truncated call
	// is refused, the turn ends.
	first := buildAgent(&scriptedLLM{steps: []func(context.Context) (*llmtypes.LLMResponse, error){
		func(context.Context) (*llmtypes.LLMResponse, error) {
			return &llmtypes.LLMResponse{
				StopReason: "max_tokens",
				Content:    "runn",
				ToolCalls:  []llmtypes.ToolCall{{ID: "c1", Name: "needs_query", Input: map[string]interface{}{}}},
			}, nil
		},
	}})
	_, err := first.Chat(context.Background(), "s-replay-cb", "go")
	require.ErrorContains(t, err, "circuit breaker")
	require.Zero(t, tool.calls, "the breaker refused the truncated call")
	events, err := recorder.LoadEvents(context.Background(), "s-replay-cb")
	require.NoError(t, err)
	require.Empty(t, events, "the breaker exit left the recorded response in the log")

	// The failed turn's own resume: the truncated batch must not dispatch.
	second := buildAgent(&scriptedLLM{})
	resp, err := second.ResumeSession(context.Background(), "s-replay-cb")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)
	require.Zero(t, tool.calls, "resume dispatched the truncated calls the breaker refused")
}

// pruneFailingLog fails every PruneResolvedStep, the best-effort prune at the
// breaker exit.
type pruneFailingLog struct{ *recordingEventLog }

func (l *pruneFailingLog) PruneResolvedStep(context.Context, string, int) error {
	return errors.New("event log unavailable")
}

// The breaker exit's prune is best-effort. When it fails the refused response
// stays in the log as the highest step, and the failed turn's own resume must
// still not dispatch its truncated calls (review round 2, Q4).
func TestResumeSession_BreakerPruneFailure_TruncatedBatchNotReplayed(t *testing.T) {
	recorder := &pruneFailingLog{recordingEventLog: &recordingEventLog{}}
	sessions := newSQLiteSessions(t)
	tool := &cbTruncatedTool{}
	buildAgent := func(llm *scriptedLLM) *Agent {
		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		cfg.OutputTokenCBThreshold = 1
		ag := NewAgent(&mockBackend{}, llm, WithConfig(cfg), WithEventLog(recorder), WithMemory(NewMemoryWithStore(sessions)))
		ag.tools.Register(tool)
		return ag
	}

	first := buildAgent(&scriptedLLM{steps: []func(context.Context) (*llmtypes.LLMResponse, error){
		func(context.Context) (*llmtypes.LLMResponse, error) {
			return &llmtypes.LLMResponse{
				StopReason: "max_tokens",
				Content:    "runn",
				ToolCalls:  []llmtypes.ToolCall{{ID: "c1", Name: "needs_query", Input: map[string]interface{}{}}},
			}, nil
		},
	}})
	_, err := first.Chat(context.Background(), "s-replay-cb-prune", "go")
	require.ErrorContains(t, err, "circuit breaker")
	events, err := recorder.LoadEvents(context.Background(), "s-replay-cb-prune")
	require.NoError(t, err)
	require.NotEmpty(t, events, "the failing prune must leave the refused response in the log")

	second := buildAgent(&scriptedLLM{})
	resp, err := second.ResumeSession(context.Background(), "s-replay-cb-prune")
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)
	require.Zero(t, tool.calls, "resume dispatched the truncated calls the breaker refused")
}

// r2IntentFailingLog fails llm_call_intent writes on demand, standing in for
// the turn that dies before its own intent is recorded.
type r2IntentFailingLog struct {
	*recordingEventLog
	failIntent bool
}

func (l *r2IntentFailingLog) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	if l.failIntent && eventType == eventlog.EventLLMCallIntent {
		return errors.New("event log unavailable")
	}
	return l.recordingEventLog.RecordIntent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}

// A later turn whose own intent write fails leaves the earlier turn's step as
// the highest intent, and its message as the unanswered tail: the replay must
// still not run the earlier turn's recorded batch as the answer to the later
// message — the model never saw it (review round 2, F5).
func TestReview590R2_StaleCrashedTurnResultReplayedIntoLaterTurn(t *testing.T) {
	log := &r2IntentFailingLog{recordingEventLog: &recordingEventLog{}}
	sessions := newSQLiteSessions(t)
	tool := &crashingTool{}
	llm := &scriptedLLM{}
	ag := newEventLogAgent(t, llm, sessions, log, tool)
	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-r2", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "first turn"}, true)
	payload, err := json.Marshal(LLMResponse{Content: "running it",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}}}})
	require.NoError(t, err)
	recordLLMResponse(t, log, "s-r2", 0, lastMessageTurn(sess), payload)
	log.failIntent = true
	_, err = ag.Chat(ctx, "s-r2", "second turn")
	require.Error(t, err)
	log.failIntent = false
	require.Zero(t, llm.calls)
	_, _ = ag.ResumeSession(ctx, "s-r2")
	require.Zero(t, tool.calls, "turn 1's recorded batch ran as the answer to turn 2's message")
	require.Equal(t, 1, llm.calls, "the later turn must be answered by a fresh model call, not the replay")
}

// intentCapturingLog keeps every llm_call_intent payload, even after the step
// is pruned.
type intentCapturingLog struct {
	*recordingEventLog
	intents [][]byte
}

func (l *intentCapturingLog) RecordIntent(ctx context.Context, sessionID string, stepIndex, batchPosition int, eventType eventlog.EventType, toolUseID string, payload []byte) error {
	if eventType == eventlog.EventLLMCallIntent {
		l.intents = append(l.intents, append([]byte(nil), payload...))
	}
	return l.recordingEventLog.RecordIntent(ctx, sessionID, stepIndex, batchPosition, eventType, toolUseID, payload)
}

// The loop stamps each LLM intent with the turn it answers: a Chat with its own
// user message's Turn, a resume with the session's tail Turn. A wrong stamp
// would make the replay gate refuse every real replay.
func TestRecordLLMIntent_StampsTheAnsweredTurn(t *testing.T) {
	log := &intentCapturingLog{recordingEventLog: &recordingEventLog{}}
	ag := newEventLogAgent(t, &scriptedLLM{}, newSQLiteSessions(t), log, &crashingTool{})
	ctx := context.Background()
	answered := func(i int) int64 {
		var p struct {
			AnsweredTurn *int64 `json:"answered_turn"`
		}
		require.NoError(t, json.Unmarshal(log.intents[i], &p))
		require.NotNil(t, p.AnsweredTurn, "intent %d carries no answered_turn", i)
		return *p.AnsweredTurn
	}

	_, err := ag.Chat(ctx, "s-stamp", "first")
	require.NoError(t, err)
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-stamp", ag.config.Name, "")
	require.Len(t, log.intents, 1)
	require.Equal(t, rawSessionMessages(sess)[0].Turn, answered(0), "Chat stamps its user message's turn")

	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "second"}, true)
	tail := lastMessageTurn(sess)
	require.NotEqual(t, answered(0), tail)
	_, err = ag.ResumeSession(ctx, "s-stamp")
	require.NoError(t, err)
	require.Len(t, log.intents, 2)
	require.Equal(t, tail, answered(1), "a resume stamps the tail turn it answers")
}

// assistantRowFailingStore fails every assistant-row write, the fail-open
// appendMessage case for the replayed message.
type assistantRowFailingStore struct {
	SessionStorage
}

func (s *assistantRowFailingStore) SaveMessage(ctx context.Context, sessionID string, msg *Message, turnStart bool) error {
	if msg.Role == "assistant" {
		return errors.New("injected: assistant row write failed")
	}
	return s.SessionStorage.SaveMessage(ctx, sessionID, msg, turnStart)
}

// The replayed assistant message can fail to persist. Resume must then apply
// the loop's own durability gates: no park on a batch whose assistant row
// never saved (a request row must never strand against a missing batch), and
// the step's events kept for a later resume. The batch carries one ungoverned
// call and one Ask-classified one: the ungoverned call proves the inline
// dispatch, the Ask one proves no request row was minted (without a blocking
// resolver its body fails closed at execution, which is the loop's own
// non-durable-batch behavior).
func TestResumeSession_UnpersistedReplaySkipsParkAndKeepsStep(t *testing.T) {
	recorder := &recordingEventLog{}
	failing := &assistantRowFailingStore{SessionStorage: newSQLiteSessions(t)}
	tool := &crashingTool{}
	gated := &countingTool{name: "gated"}

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	store := shuttle.NewInMemoryHumanRequestStore()
	ag := NewAgent(&mockBackend{}, &scriptedLLM{},
		WithConfig(cfg), WithEventLog(recorder), WithMemory(NewMemoryWithStore(failing)),
		WithHITLPark(store, 0, NewProgressNotifier()),
		WithAdmissionHooks(shuttle.NewChain([]shuttle.Hook{scopedAskHook{tool: "gated"}}, nil, nil)))
	ag.tools.Register(tool)
	ag.tools.Register(gated)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-replay-nopark", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	payload, err := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{
		{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}},
		{ID: "c2", Name: "gated", Input: map[string]interface{}{"v": "w"}},
	}})
	require.NoError(t, err)
	recordLLMResponse(t, recorder, "s-replay-nopark", 0, lastMessageTurn(sess), payload)

	_, err = ag.ResumeSession(ctx, "s-replay-nopark")
	require.NoError(t, err, "resume parked on a batch whose assistant message never saved")
	require.Equal(t, 1, tool.calls, "the ungoverned call was not dispatched inline after the park was skipped")
	require.Zero(t, gated.runs.Load(), "the Ask-classified call ran without a human decision")
	reqs, err := store.ListBySession(ctx, "s-replay-nopark")
	require.NoError(t, err)
	require.Empty(t, reqs, "a human request was raised against an unsaved batch")

	// The step is kept: its assistant row is missing, so its events stay for a
	// later resume. (The final reply failed to persist too, so the finished
	// turn's prune did not run either.)
	events, err := recorder.LoadEvents(ctx, "s-replay-nopark")
	require.NoError(t, err)
	require.NotEmpty(t, events, "the step was pruned while its assistant message was not saved")
}

// A replayed batch whose query_tool_result call already started (its intent is
// in the log) and whose second call never started. Observed behavior, settled
// for the review: the started query call is treated like any other
// unknown-outcome call — an execution gap under stop, a failed result under
// fail — never re-run against the new process's empty in-turn store. A query
// call that never started would dispatch fresh and get the query tool's
// not_this_turn answer.
func TestResumeSession_ReplayWithStartedQueryToolResult(t *testing.T) {
	fixture := func(t *testing.T, ag *Agent, recorder *recordingEventLog, id string) {
		t.Helper()
		ctx := context.Background()
		sess := ag.memory.GetOrCreateSessionWithAgent(ctx, id, ag.config.Name, "")
		ag.appendMessage(ctx, sess, Message{Role: "user", Content: "read the offload"}, true)
		payload, err := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{
			{ID: "q1", Name: queryToolResultName, Input: map[string]interface{}{"message_id": 2}},
			{ID: "c2", Name: "crashy", Input: map[string]interface{}{"n": 1}},
		}})
		require.NoError(t, err)
		recordLLMResponse(t, recorder, id, 0, lastMessageTurn(sess), payload)
		require.NoError(t, recorder.RecordIntent(ctx, id, 0, 0, eventlog.EventToolCallIntent, "q1", []byte(`{"tool_name":"query_tool_result"}`)))
	}

	t.Run("stop surfaces a gap for the started query call", func(t *testing.T) {
		recorder := &recordingEventLog{}
		tool := &crashingTool{}
		ag := newEventLogAgent(t, &scriptedLLM{}, nil, recorder, tool)
		fixture(t, ag, recorder, "s-replay-query-stop")

		_, err := ag.ResumeSession(context.Background(), "s-replay-query-stop")
		var gap *ExecutionGap
		require.ErrorAs(t, err, &gap)
		require.Equal(t, "q1", gap.ToolUseID, "the started query call is surfaced, not re-run")
		require.Zero(t, tool.calls)
	})
	t.Run("fail closes the query call and dispatches the never-started one", func(t *testing.T) {
		recorder := &recordingEventLog{}
		tool := &crashingTool{}
		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		ag := NewAgent(&mockBackend{}, &scriptedLLM{}, WithConfig(cfg), WithEventLog(recorder), WithUnknownOutcomePolicy(UnknownOutcomeFail))
		ag.tools.Register(tool)
		fixture(t, ag, recorder, "s-replay-query-fail")

		_, err := ag.ResumeSession(context.Background(), "s-replay-query-fail")
		require.NoError(t, err)
		require.Equal(t, 1, tool.calls, "only the never-started call ran")
		sess := ag.memory.GetOrCreateSessionWithAgent(context.Background(), "s-replay-query-fail", ag.config.Name, "")
		m := toolMessage(sess, "q1")
		if m == nil || m.ToolResult == nil {
			t.Fatalf("no tool message for q1: %+v", m)
		}
		require.False(t, m.ToolResult.Success)
		require.Equal(t, "outcome_unknown", m.ToolResult.Error.Code)
	})
}
