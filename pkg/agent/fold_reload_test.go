// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// talkingCaller is a model that says something in the same message as every
// tool call, then answers once it has the result.
type talkingCaller struct {
	mu sync.Mutex
	n  int
}

func (m *talkingCaller) Name() string  { return "talking" }
func (m *talkingCaller) Model() string { return "talking-model" }
func (m *talkingCaller) Chat(_ context.Context, msgs []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
	usage := Usage{InputTokens: 100, OutputTokens: 10}
	if msgs[len(msgs)-1].Role == "tool" {
		return &LLMResponse{Content: fmt.Sprintf("answer %d", m.n), StopReason: "end_turn", Usage: usage}, nil
	}
	return &LLMResponse{Content: "let me check", StopReason: "tool_use", Usage: usage,
		ToolCalls: []ToolCall{{ID: fmt.Sprintf("call-%d", m.n), Name: "rows", Input: map[string]interface{}{"i": m.n}}}}, nil
}

// storedAgent is an agent over the session store at dbPath; model carries the
// call numbering across agents built over the same store.
func storedAgent(t *testing.T, dbPath string, model *talkingCaller) (*Agent, *SessionStore) {
	t.Helper()
	store, err := NewSessionStore(dbPath, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 200000
	cfg.ReservedOutputTokens = 1000
	ag := NewAgent(&mockBackend{}, model, WithConfig(cfg), WithMemory(NewMemoryWithStore(store)))
	ag.RegisterTool(&shuttle.MockTool{
		MockName: "rows",
		MockExecute: func(context.Context, map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: "id=1 name=widget"}, nil
		},
	})
	return ag, store
}

// contextShape is the compiled context reduced to what the model is sent of
// each row: its role, the call it answers, the calls it makes, its text.
func contextShape(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		var calls []string
		for _, c := range m.ToolCalls {
			calls = append(calls, c.ID)
		}
		out = append(out, fmt.Sprintf("%s|%s|%s|%s", m.Role, m.ToolUseID, strings.Join(calls, ","), m.Content))
	}
	return out
}

func loadedMemory(t *testing.T, dbPath, sessionID string) *SegmentedMemory {
	t.Helper()
	ag, _ := storedAgent(t, dbPath, &talkingCaller{})
	sm, ok := ag.memory.GetOrCreateSessionWithAgent(context.Background(), sessionID, ag.config.Name, "").SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	return sm
}

// A fold keeps a recent assistant row for its words and removes its tool
// traffic. A session loaded from the store after the fold compiles the same
// context as the memory that folded: the call whose result was folded is not
// sent, and nothing tells the model it did not complete.
func TestASessionLoadedAfterAFoldCompilesTheSameContext(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "s.db")
	ag, _ := storedAgent(t, db, &talkingCaller{})
	for i := 1; i <= 6; i++ {
		_, err := ag.Chat(ctx, "s-fold", fmt.Sprintf("question %d", i))
		require.NoError(t, err)
	}
	sess, ok := ag.GetSession("s-fold")
	require.True(t, ok)
	live := sess.SegmentedMem.(*SegmentedMemory)
	live.SetProtectedRecentTurns(6)
	applied, _ := live.ApplyFoldReply(ctx, "the work so far")
	require.True(t, applied, "the fold did not commit")

	want := contextShape(live.GetMessagesForLLM())
	require.Contains(t, strings.Join(want, "\n"), "assistant|||let me check", "the fixture has no kept row that lost its call")

	got := contextShape(loadedMemory(t, db, "s-fold").GetMessagesForLLM())
	assert.Equal(t, want, got, "a session loaded after the fold compiles a different context")
	assert.NotContains(t, strings.Join(got, "\n"), syntheticFailedResult, "a folded result is reported as a call that did not complete")
}

// A call that never finished has no result at all, folded or not: after a
// load it is still sent, answered by the synthetic failed result.
func TestAnUnfinishedCallKeepsItsStandInAfterALoad(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "s.db")
	ag, store := storedAgent(t, db, &talkingCaller{})
	_, err := ag.Chat(ctx, "s-cut", "question 1")
	require.NoError(t, err)
	require.NoError(t, store.SaveMessage(ctx, "s-cut", &Message{Role: "user", Content: "question 2", Turn: 2}, true))
	require.NoError(t, store.SaveMessage(ctx, "s-cut", &Message{Role: "assistant", Content: "let me check", Turn: 2,
		ToolCalls: []ToolCall{{ID: "cut-short", Name: "rows", Input: map[string]interface{}{}}}}, false))

	got := strings.Join(contextShape(loadedMemory(t, db, "s-cut").GetMessagesForLLM()), "\n")
	assert.Contains(t, got, "assistant||cut-short|let me check", "the unfinished call was dropped")
	assert.Contains(t, got, "tool|cut-short||"+syntheticFailedResult, "the unfinished call has no stand-in result")
}

// rawRowStore is a SessionStorage with no reload repair of its own, as a
// host's own store is: LoadSession returns the stored unfolded rows as they
// are, with every call that tool_calls_json holds. Every other operation is
// the SQLite store's.
type rawRowStore struct {
	*SessionStore
}

func (s rawRowStore) LoadSession(ctx context.Context, sessionID string) (*Session, error) {
	sess, err := s.SessionStore.LoadSession(ctx, sessionID)
	if err != nil || sess == nil {
		return sess, err
	}
	rows, err := s.ListMessagesBySeqRange(ctx, sessionID, 1, 1<<62)
	if err != nil {
		return nil, err
	}
	sess.Messages = nil
	for _, m := range rows {
		if !m.Folded {
			sess.Messages = append(sess.Messages, m)
		}
	}
	return sess, nil
}

// The repair runs in the agent's reload, so it does not depend on the store:
// after a fold, a session reloaded from a store that returns the raw unfolded
// rows compiles the same context as the memory that folded.
func TestAReloadThroughAStoreWithNoRepairCompilesTheSameContext(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "s.db")
	ag, store := storedAgent(t, db, &talkingCaller{})
	for i := 1; i <= 6; i++ {
		_, err := ag.Chat(ctx, "s-raw", fmt.Sprintf("question %d", i))
		require.NoError(t, err)
	}
	sess, ok := ag.GetSession("s-raw")
	require.True(t, ok)
	live := sess.SegmentedMem.(*SegmentedMemory)
	live.SetProtectedRecentTurns(6)
	applied, _ := live.ApplyFoldReply(ctx, "the work so far")
	require.True(t, applied, "the fold did not commit")
	want := contextShape(live.GetMessagesForLLM())
	require.Contains(t, strings.Join(want, "\n"), "assistant|||let me check", "the fixture has no kept row that lost its call")

	raw := rawRowStore{SessionStore: store}
	stored, err := raw.LoadSession(ctx, "s-raw")
	require.NoError(t, err)
	require.NotEmpty(t, callsWithNoLoadedResult(stored.Messages), "the raw store must return the calls of the folded results")

	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 200000
	cfg.ReservedOutputTokens = 1000
	fresh := NewAgent(&mockBackend{}, &talkingCaller{}, WithConfig(cfg), WithMemory(NewMemoryWithStore(raw)))
	reloaded, ok := fresh.memory.GetOrCreateSessionWithAgent(ctx, "s-raw", fresh.config.Name, "").SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	got := contextShape(reloaded.GetMessagesForLLM())
	assert.Equal(t, want, got, "a reload through a store with no repair compiles a different context")
	assert.NotContains(t, strings.Join(got, "\n"), syntheticFailedResult, "a folded result is reported as a call that did not complete")
}

// callsWithNoLoadedResult is the test's own count of the calls in msgs that
// no row among them answers.
func callsWithNoLoadedResult(msgs []Message) []string {
	answered := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			answered[m.ToolUseID] = true
		}
	}
	var ids []string
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if !answered[c.ID] {
				ids = append(ids, c.ID)
			}
		}
	}
	return ids
}
