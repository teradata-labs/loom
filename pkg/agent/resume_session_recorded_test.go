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
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/eventlog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// scriptedLLM returns its steps in order; a step may cancel the turn to stand
// in for the process dying during that LLM call.
type scriptedLLM struct {
	mu    sync.Mutex
	steps []func(ctx context.Context) (*llmtypes.LLMResponse, error)
	calls int
}

func (s *scriptedLLM) Chat(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i < len(s.steps) {
		return s.steps[i](ctx)
	}
	return &llmtypes.LLMResponse{Content: "done"}, nil
}
func (s *scriptedLLM) Name() string  { return "scripted" }
func (s *scriptedLLM) Model() string { return "scripted-v1" }

func toolCallStep(calls ...llmtypes.ToolCall) func(context.Context) (*llmtypes.LLMResponse, error) {
	return func(context.Context) (*llmtypes.LLMResponse, error) {
		return &llmtypes.LLMResponse{ToolCalls: calls}, nil
	}
}

// crashStep cancels the turn, standing in for the process dying mid-call.
func crashStep(cancel context.CancelFunc) func(context.Context) (*llmtypes.LLMResponse, error) {
	return func(ctx context.Context) (*llmtypes.LLMResponse, error) {
		cancel()
		return nil, context.Canceled
	}
}

// toolRowFailingStore fails SaveMessage for tool rows while failTools is set,
// the fail-open appendMessage case from review round 1, F7.
type toolRowFailingStore struct {
	SessionStorage
	mu        sync.Mutex
	failTools bool
}

func (s *toolRowFailingStore) SaveMessage(ctx context.Context, sessionID string, msg *Message, turnStart bool) error {
	s.mu.Lock()
	fail := s.failTools && msg.Role == "tool"
	s.mu.Unlock()
	if fail {
		return errors.New("injected: tool row write failed")
	}
	return s.SessionStorage.SaveMessage(ctx, sessionID, msg, turnStart)
}

func newEventLogAgent(t *testing.T, llm LLMProvider, store SessionStorage, log eventlog.EventLogStore, tools ...shuttle.Tool) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	opts := []Option{WithConfig(cfg), WithEventLog(log)}
	if store != nil {
		opts = append(opts, WithMemory(NewMemoryWithStore(store)))
	}
	ag := NewAgent(&mockBackend{}, llm, opts...)
	for _, tool := range tools {
		ag.tools.Register(tool)
	}
	return ag
}

func newSQLiteSessions(t *testing.T) *SessionStore {
	t.Helper()
	sessions, err := NewSessionStore(filepath.Join(t.TempDir(), "sessions.db"), observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })
	return sessions
}

// F1: a turn that ran without the event log must not be resumed by
// dispatching its calls, because resume cannot tell whether they already ran.
func TestResumeSession_UnrecordedTurn_RefusesInsteadOfDispatching(t *testing.T) {
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	ag := newEventLogAgent(t, &mockSimpleLLM{}, nil, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-unrecorded", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	ag.appendMessage(ctx, sess, Message{
		Role:      "assistant",
		ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}},
	}, false)

	_, err := ag.ResumeSession(ctx, "s-unrecorded")
	require.ErrorIs(t, err, ErrTurnNotRecorded)
	require.Zero(t, tool.calls, "an unrecorded call was dispatched")
}

// F7: the tool runs and its result is recorded, its tool row fails to
// persist, the loop records the next LLM intent, and the process dies. A
// resume from the durable state must backfill the call, not run it again.
func TestResumeSession_ToolRowNotPersisted_BackfillsInsteadOfRerunning(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)
	failing := &toolRowFailingStore{SessionStorage: sessions, failTools: true}
	tool := &crashingTool{}

	turnCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	llm := &scriptedLLM{steps: []func(context.Context) (*llmtypes.LLMResponse, error){
		toolCallStep(llmtypes.ToolCall{ID: "c1", Name: "crashy", Input: map[string]interface{}{"n": 1}}),
		crashStep(cancel),
	}}
	first := newEventLogAgent(t, llm, failing, recorder, tool)
	_, err := first.Chat(turnCtx, "s-f7", "run crashy")
	require.Error(t, err, "the turn should end with the simulated crash")
	require.Equal(t, 1, tool.calls)

	// A fresh process: new agent and memory over the same durable stores.
	second := newEventLogAgent(t, &mockSimpleLLM{}, sessions, recorder, tool)
	_, err = second.ResumeSession(context.Background(), "s-f7")
	require.NoError(t, err)
	require.Equal(t, 1, tool.calls, "resume ran the tool a second time")
}

// F8: the process dies during a turn's first LLM call. The user's message is
// saved and unanswered, so resume must answer it instead of reporting that
// there is nothing to resume.
func TestResumeSession_CrashInFirstLLMCall_AnswersTheTurn(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)

	turnCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := newEventLogAgent(t, &scriptedLLM{steps: []func(context.Context) (*llmtypes.LLMResponse, error){crashStep(cancel)}},
		sessions, recorder)
	_, err := first.Chat(turnCtx, "s-f8", "hello")
	require.Error(t, err)

	second := newEventLogAgent(t, &mockSimpleLLM{}, sessions, recorder)
	resp, err := second.ResumeSession(context.Background(), "s-f8")
	require.NoError(t, err)
	require.Equal(t, "Mock response", resp.Content)

	_, err = second.ResumeSession(context.Background(), "s-f8")
	require.ErrorIs(t, err, ErrNothingParked, "a second resume found the answered turn still open")
}

// F8, second shape: an earlier turn completed with tool calls, then the
// process died during the next turn's first LLM call. This used to return
// ErrNotParkedTail.
func TestResumeSession_CrashInFirstLLMCallAfterToolTurn_AnswersTheTurn(t *testing.T) {
	recorder := &recordingEventLog{}
	sessions := newSQLiteSessions(t)
	tool := &crashingTool{}

	turnCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	llm := &scriptedLLM{steps: []func(context.Context) (*llmtypes.LLMResponse, error){
		toolCallStep(llmtypes.ToolCall{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}),
		func(context.Context) (*llmtypes.LLMResponse, error) {
			return &llmtypes.LLMResponse{Content: "first answer"}, nil
		},
		crashStep(cancel),
	}}
	first := newEventLogAgent(t, llm, sessions, recorder, tool)
	_, err := first.Chat(context.Background(), "s-f8b", "run crashy")
	require.NoError(t, err)
	_, err = first.Chat(turnCtx, "s-f8b", "and again?")
	require.Error(t, err)

	second := newEventLogAgent(t, &mockSimpleLLM{}, sessions, recorder, tool)
	resp, err := second.ResumeSession(context.Background(), "s-f8b")
	require.NoError(t, err)
	require.Equal(t, "Mock response", resp.Content)
	require.Equal(t, 1, tool.calls, "resume re-ran the earlier turn's tool")
}

// An unanswered user message whose turn already dispatched tools for a batch
// the session never saved must not be re-run: the LLM could ask for the same
// tools again.
func TestResumeSession_UnansweredTurnWithUnsavedBatch_Refuses(t *testing.T) {
	recorder := &recordingEventLog{}
	tool := &crashingTool{}
	ag := newEventLogAgent(t, &mockSimpleLLM{}, nil, recorder, tool)

	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-unsaved", ag.config.Name, "")
	ag.appendMessage(ctx, sess, Message{Role: "user", Content: "run crashy"}, true)
	// The LLM asked for crashy and it ran, but the assistant message carrying
	// the request never reached the session.
	payload, err := json.Marshal(LLMResponse{ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "crashy", Input: map[string]interface{}{}}}})
	require.NoError(t, err)
	require.NoError(t, recorder.RecordResult(ctx, sess.ID, 0, 0, eventlog.EventLLMCallResult, "", payload))
	require.NoError(t, recorder.RecordIntent(ctx, sess.ID, 0, 0, eventlog.EventToolCallIntent, "c1", []byte(`{"tool_name":"crashy"}`)))

	_, err = ag.ResumeSession(ctx, "s-unsaved")
	require.ErrorIs(t, err, ErrBatchNotSaved)
	require.Zero(t, tool.calls)
}

// The saved assistant message omits query_tool_result calls, so a call's
// index in the saved batch can differ from the batch_position its events were
// recorded under. Resume must read each call's events at the recorded
// position.
func TestMatchRecordedCalls_SkipsCallsTheSavedBatchOmits(t *testing.T) {
	recorded := []ToolCall{
		{Name: queryToolResultName, Input: map[string]interface{}{"id": "x"}},
		{Name: "crashy", Input: map[string]interface{}{"n": 1}},
		{Name: "crashy", Input: map[string]interface{}{"n": 2}},
	}
	saved := []ToolCall{
		{Name: "crashy", Input: map[string]interface{}{"n": float64(1)}},
		{Name: "crashy", Input: map[string]interface{}{"n": float64(2)}},
	}
	positions, ok := matchRecordedCalls(recorded, saved)
	require.True(t, ok)
	require.Equal(t, []int{1, 2}, positions)

	_, ok = matchRecordedCalls(recorded, saved[:1])
	require.False(t, ok, "a saved batch missing a non-query call is a different batch")
	_, ok = matchRecordedCalls(recorded, []ToolCall{{Name: "other", Input: map[string]interface{}{}}})
	require.False(t, ok)
}

// F11: steps left behind by a resolved execution gap are cleared once a later
// turn's reply is saved.
func TestChat_FinishedTurnClearsLeftoverSteps(t *testing.T) {
	recorder := &recordingEventLog{}
	ag := newEventLogAgent(t, &mockSimpleLLM{}, newSQLiteSessions(t), recorder)
	ctx := context.Background()
	sess := ag.memory.GetOrCreateSessionWithAgent(ctx, "s-leftover", ag.config.Name, "")
	require.NoError(t, recorder.RecordIntent(ctx, sess.ID, 0, 0, eventlog.EventToolCallIntent, "old", []byte(`{}`)))

	_, err := ag.Chat(ctx, "s-leftover", "hello")
	require.NoError(t, err)
	events, err := recorder.LoadEvents(ctx, "s-leftover")
	require.NoError(t, err)
	require.Empty(t, events)
}
