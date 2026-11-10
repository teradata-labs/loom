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

//go:build fts5

package agent

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/memory"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// compiledRoles renders a compiled context as "role:content" lines, the
// ROM left out, for order assertions.
func compiledRoles(msgs []Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		out = append(out, m.Role+":"+m.Content)
	}
	return out
}

// TestPinTurnNotes_StayInPlaceForTheTurn — a reminder sent on one call of a
// turn is compiled at the position it was sent from on every later call of
// that turn, a note is sent once per turn, and the next turn compiles none of
// them.
func TestPinTurnNotes_StayInPlaceForTheTurn(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})

	sm.PinTurnNotes("recall", "")
	reminder := systemReminderContent("recall")
	assert.Equal(t, []string{"user:q1", "user:" + reminder}, compiledRoles(sm.GetMessagesForLLM()))

	sm.AddMessage(ctx, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "t"}}, Turn: 1})
	sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: "c1", Content: "r1", Turn: 1})
	sm.PinTurnNotes("recall", "soft")
	soft := systemReminderContent("soft")
	assert.Equal(t, []string{"user:q1", "user:" + reminder, "assistant:", "tool:r1", "user:" + soft},
		compiledRoles(sm.GetMessagesForLLM()),
		"the earlier reminder stays where it was sent; only the new note is added, after the rows since")

	sm.PinTurnNotes("recall", "soft")
	assert.Len(t, compiledRoles(sm.GetMessagesForLLM()), 5, "a note already pinned this turn is not sent again")

	sm.AddMessage(ctx, Message{Role: "assistant", Content: "a1", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: "q2", Turn: 2})
	for _, line := range compiledRoles(sm.GetMessagesForLLM()) {
		assert.NotContains(t, line, "<system-reminder>", "a settled turn's reminders are not compiled")
	}
}

// TestPinTurnNotes_PendingTailSkipsPinnedNotes — the pre-send estimate counts
// only the notes not pinned yet; once pinned, they are compiled messages.
func TestPinTurnNotes_PendingTailSkipsPinnedNotes(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})

	sm.SetTransientTail("recall", "soft")
	sm.mu.RLock()
	assert.Equal(t, systemReminderContent("recall", "soft"), sm.transientTail)
	sm.mu.RUnlock()

	sm.PinTurnNotes("recall")
	sm.mu.RLock()
	assert.Empty(t, sm.transientTail, "pinning clears the pending tail")
	sm.mu.RUnlock()

	sm.SetTransientTail("recall", "soft")
	sm.mu.RLock()
	assert.Equal(t, systemReminderContent("soft"), sm.transientTail, "the pinned note is compiled, not pending")
	sm.mu.RUnlock()
}

// TestTurnNotes_SettledThinkingNotReplayedAfterToolChange (review F4, Q1
// wiring): a settled turn's thinking block is replayed while the request
// identity is unchanged, and not once the advertised tool set changes between
// two Chat calls. Removing SetRequestIdentity from setAdvertisedTools fails it.
func TestTurnNotes_SettledThinkingNotReplayedAfterToolChange(t *testing.T) {
	run := func(t *testing.T, changeTools bool) [][]byte {
		stub := &stubAnthropic{}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)
		cfg := DefaultConfig()
		cfg.PatternConfig = DefaultPatternConfig()
		cfg.PatternConfig.UseLLMClassifier = false
		ag := NewAgent(&mockBackend{}, anthropic.NewClient(anthropic.Config{
			APIKey: "test", Model: "claude-haiku-5-5", Endpoint: srv.URL,
		}), WithConfig(cfg))
		ag.RegisterTool(namedStubTool{name: "note_fact"})

		ctx := context.Background()
		_, err := ag.Chat(ctx, "s-tools", "first message")
		require.NoError(t, err)
		if changeTools {
			ag.RegisterTool(namedStubTool{name: "another_tool"})
		}
		_, err = ag.Chat(ctx, "s-tools", "second message")
		require.NoError(t, err)
		reqs := stub.requests()
		require.Len(t, reqs, 4)
		assert.Zero(t, thinkingPrefixFailures(t, reqs), "every replayed thinking block follows the prefix it was written after")
		return reqs
	}

	t.Run("same tools: settled thinking replayed", func(t *testing.T) {
		reqs := run(t, false)
		assert.Contains(t, string(reqs[2]), `"signature":"sig-1"`)
	})
	t.Run("tool set changed: settled thinking left out", func(t *testing.T) {
		reqs := run(t, true)
		assert.NotContains(t, string(reqs[2]), `"signature":"sig-1"`)
	})
}

// recallReliefLLM answers graph memory's side calls itself and captures the
// main calls, which it answers with a short text reply.
type recallReliefLLM struct {
	mu   sync.Mutex
	main [][]llmtypes.Message
}

func (l *recallReliefLLM) Name() string  { return "mock" }
func (l *recallReliefLLM) Model() string { return "mock-v1" }

func (l *recallReliefLLM) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	switch {
	case promptContains(msgs, "Distill this message into a concise search query"):
		return &llmtypes.LLMResponse{Content: "walrus migration"}, nil
	case promptContains(msgs, "Candidate memories:"):
		return &llmtypes.LLMResponse{Content: "1"}, nil
	}
	l.mu.Lock()
	l.main = append(l.main, append([]llmtypes.Message(nil), msgs...))
	l.mu.Unlock()
	return &llmtypes.LLMResponse{Content: "done", Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 5}}, nil
}

// TestTurnNotes_ReliefCountsRecallNoteBeforeFirstSend (review F4, F1
// wiring): the recall note is pinned at the send, after relief ran, so relief
// must count it as the pending tail. A large note on a context just under the
// start mark makes relief shed before the first send; the same context with
// nothing to recall does not shed. Removing the agent's SetTransientTail calls
// fails it.
func TestTurnNotes_ReliefCountsRecallNoteBeforeFirstSend(t *testing.T) {
	run := func(t *testing.T, withMemory bool) (folds int, sent []llmtypes.Message) {
		ctx := context.Background()
		sm := NewSegmentedMemory("ROM", 16000, 1000)
		sm.SetThreshold(16384)
		comp := &countingCompressor{}
		sm.SetCompressor(comp)
		sm.mu.Lock()
		start := sm.startMarkLocked(0)
		sm.mu.Unlock()
		for turn := int64(1); ; turn++ {
			sm.AddMessage(ctx, Message{Role: "user", Content: denseText(fmt.Sprintf("q%d", turn), 10), Turn: turn})
			sm.AddMessage(ctx, Message{Role: "assistant", Content: denseText(fmt.Sprintf("a%d", turn), 10), Turn: turn})
			sm.mu.Lock()
			est := sm.estimateLocked()
			sm.mu.Unlock()
			if est >= start*85/100 {
				break
			}
		}

		store := newTestGraphMemoryStore(t)
		if withMemory {
			_, err := store.Remember(ctx, &memory.Memory{
				AgentID:    "relief-agent",
				Content:    "walrus migration " + denseText("walrus", 150),
				MemoryType: "fact",
				Salience:   0.9,
			})
			require.NoError(t, err)
		}
		gm := DefaultGraphMemoryConfig()
		gm.Enabled = true
		gm.EnableExtraction = false
		gm.MaxContextTokens = 20000

		llm := &recallReliefLLM{}
		ag := reliefSeamAgent(t, llm, "s-relief", sm, WithGraphMemoryStore(store, gm))
		ag.config.Name = "relief-agent"
		ag.config.ContextReliefWarning = false
		_, err := ag.Chat(ctx, "s-relief", "What about the walrus migration?")
		require.NoError(t, err)
		llm.mu.Lock()
		defer llm.mu.Unlock()
		require.NotEmpty(t, llm.main)
		return comp.n(), llm.main[0]
	}

	t.Run("nothing to recall: under the start mark", func(t *testing.T) {
		folds, sent := run(t, false)
		require.Zero(t, folds, "fixture: without a recall note the context is under the start mark")
		for _, m := range sent {
			assert.NotContains(t, m.Content, graphMemoryContextMarker)
		}
	})
	t.Run("large recall note: relief sheds before the send", func(t *testing.T) {
		folds, sent := run(t, true)
		noted := false
		for _, m := range sent {
			if strings.Contains(m.Content, graphMemoryContextMarker) {
				noted = true
			}
		}
		require.True(t, noted, "fixture: the first send carries the recall note")
		assert.Positive(t, folds, "relief must count the pending recall note and shed before the send")
	})
}
