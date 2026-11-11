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

	sm.PinTurnNotes(TurnReminder{Kind: ReminderGraphMemory, Text: "recall"}, TurnReminder{})
	reminder := systemReminderContent("recall")
	assert.Equal(t, []string{"user:q1", "user:" + reminder}, compiledRoles(sm.GetMessagesForLLM()))

	sm.AddMessage(ctx, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "t"}}, Turn: 1})
	sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: "c1", Content: "r1", Turn: 1})
	sm.PinTurnNotes(TurnReminder{Kind: ReminderGraphMemory, Text: "recall"}, TurnReminder{Text: "soft"})
	soft := systemReminderContent("soft")
	assert.Equal(t, []string{"user:q1", "user:" + reminder, "assistant:", "tool:r1", "user:" + soft},
		compiledRoles(sm.GetMessagesForLLM()),
		"the earlier reminder stays where it was sent; only the new note is added, after the rows since")

	sm.PinTurnNotes(TurnReminder{Kind: ReminderGraphMemory, Text: "recall"}, TurnReminder{Text: "soft"})
	assert.Len(t, compiledRoles(sm.GetMessagesForLLM()), 5, "a note already pinned this turn is not sent again")

	sm.AddMessage(ctx, Message{Role: "assistant", Content: "a1", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: "q2", Turn: 2})
	for _, line := range compiledRoles(sm.GetMessagesForLLM()) {
		assert.NotContains(t, line, "<system-reminder>", "a settled turn's reminders are not compiled")
	}
}

// TestPinTurnNotes_StatusReminderOncePerTurn (review F5): a status reminder
// whose text changes each call (the tool budget's count) is sent once per
// turn, so five calls in the reminder window compile one copy, not five
// contradictory ones. Event notes (repeat hints) are sent on every step that
// carries one. The next turn sends the status reminder again.
func TestPinTurnNotes_StatusReminderOncePerTurn(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})

	countReminders := func() (budget, hints int) {
		for _, line := range compiledRoles(sm.GetMessagesForLLM()) {
			budget += strings.Count(line, "You have executed many tool calls")
			hints += strings.Count(line, "repeat hint")
		}
		return budget, hints
	}
	for i := 0; i < 5; i++ {
		sm.PinTurnNotes(
			TurnReminder{Kind: ReminderToolBudget, Text: buildSoftReminder(75+i, 100)},
			TurnReminder{Text: fmt.Sprintf("repeat hint %d", i%2)},
		)
		id := fmt.Sprintf("c%d", i)
		sm.AddMessage(ctx, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "t"}}, Turn: 1})
		sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: id, Content: "r", Turn: 1})
	}
	budget, hints := countReminders()
	assert.Equal(t, 1, budget, "one tool-budget reminder per turn")
	assert.Equal(t, 5, hints, "an event note is sent on each step, even when its text repeats")
	compiled := strings.Join(compiledRoles(sm.GetMessagesForLLM()), "\n")
	assert.Contains(t, compiled, "(75 of 100 max)", "the first copy is the one kept")

	sm.SetTransientTail(TurnReminder{Kind: ReminderToolBudget, Text: buildSoftReminder(80, 100)})
	sm.mu.RLock()
	assert.Empty(t, sm.transientTail, "a kind sent this turn is not pending again")
	sm.mu.RUnlock()

	sm.AddMessage(ctx, Message{Role: "assistant", Content: "a1", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "user", Content: "q2", Turn: 2})
	sm.PinTurnNotes(TurnReminder{Kind: ReminderToolBudget, Text: buildSoftReminder(80, 100)})
	budget, _ = countReminders()
	assert.Equal(t, 1, budget)
	assert.Contains(t, strings.Join(compiledRoles(sm.GetMessagesForLLM()), "\n"), "(80 of 100 max)",
		"the next turn sends the kind again")
}

// TestPinTurnNotes_EventNoteRepeatsOnALaterStep (review F7): a tool-set
// delta whose text matches an earlier one of the turn (x added, removed by
// relief, added again) is a new event and is sent again, so the newest note
// the model sees matches the advertised tools. A resend of a refused call adds
// no row, so it doesn't send its notes twice, even when relief removed rows of
// the current turn in between (which a row-position key would miss).
func TestPinTurnNotes_EventNoteRepeatsOnALaterStep(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})

	notes := func() []string {
		var out []string
		for _, m := range sm.GetMessagesForLLM() {
			if IsSystemReminder(m) {
				out = append(out, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m.Content, "<system-reminder>\n"), "</system-reminder>")))
			}
		}
		return out
	}
	step := func(i int, note string) {
		sm.PinTurnNotes(TurnReminder{Text: note})
		id := fmt.Sprintf("c%d", i)
		sm.AddMessage(ctx, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "t"}}, Turn: 1})
		sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: id, Content: "r", Turn: 1})
	}
	step(0, "Tools added: x.")
	step(1, "Tools removed: x.")
	step(2, "Tools added: x.")
	assert.Equal(t, []string{"Tools added: x.", "Tools removed: x.", "Tools added: x."}, notes())

	// A refused send and its resend: no row between them.
	sm.PinTurnNotes(TurnReminder{Text: "Tools removed: x."})
	sm.PinTurnNotes(TurnReminder{Text: "Tools removed: x."})
	assert.Len(t, notes(), 4, "a resend doesn't send the refused call's event note again")

	// Relief drops current-turn rows before the resend; it is still the same step.
	sm.PinTurnNotes(TurnReminder{Text: "hint"})
	sm.mu.Lock()
	sm.contextMessages = sm.contextMessages[:3]
	sm.mu.Unlock()
	sm.PinTurnNotes(TurnReminder{Text: "hint"})
	got := notes()
	hints := 0
	for _, n := range got {
		if n == "hint" {
			hints++
		}
	}
	assert.Equal(t, 1, hints, "rows removed by relief don't make a resend a new step")
}

// TestPinTurnNotes_PendingTailSkipsPinnedNotes — the pre-send estimate counts
// only the notes not pinned yet; once pinned, they are compiled messages.
func TestPinTurnNotes_PendingTailSkipsPinnedNotes(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})

	sm.SetTransientTail(TurnReminder{Kind: ReminderGraphMemory, Text: "recall"}, TurnReminder{Text: "soft"})
	sm.mu.RLock()
	assert.Equal(t, systemReminderContent("recall", "soft"), sm.transientTail)
	sm.mu.RUnlock()

	sm.PinTurnNotes(TurnReminder{Kind: ReminderGraphMemory, Text: "recall"})
	sm.mu.RLock()
	assert.Empty(t, sm.transientTail, "pinning clears the pending tail")
	sm.mu.RUnlock()

	sm.SetTransientTail(TurnReminder{Kind: ReminderGraphMemory, Text: "recall"}, TurnReminder{Text: "soft"})
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

// reliefSeamAgent returns an agent whose session already holds sm, so the Chat
// turn starts from a known context state (the session keeps an existing
// SegmentedMem, see ensureSessionMemory).
func reliefSeamAgent(t *testing.T, llm interface {
	Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error)
	Name() string
	Model() string
}, sessionID string, sm *SegmentedMemory, opts ...Option) *Agent {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PatternConfig = DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	ag := NewAgent(&mockBackend{}, llm, append([]Option{WithConfig(cfg)}, opts...)...)
	session := ag.memory.GetOrCreateSession(context.Background(), sessionID)
	session.SegmentedMem = sm
	return ag
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
		sm.SetCompressor(&countingCompressor{})
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
		_, err := ag.Chat(ctx, "s-relief", "What about the walrus migration?")
		require.NoError(t, err)
		llm.mu.Lock()
		defer llm.mu.Unlock()
		require.NotEmpty(t, llm.main)
		// A fold is a main call that ends in the fold ask; sent is the first
		// call that is not one.
		for _, call := range llm.main {
			if strings.Contains(call[len(call)-1].Content, foldInstruction) {
				folds++
			} else if sent == nil {
				sent = call
			}
		}
		require.NotNil(t, sent)
		return folds, sent
	}

	t.Run("nothing to recall: under the start mark", func(t *testing.T) {
		folds, sent := run(t, false)
		require.Zero(t, folds, "fixture: without a recall note the context is under the start mark")
		for _, m := range sent {
			assert.NotContains(t, m.Content, graphMemoryContextMarker)
		}
	})
	t.Run("large recall note: a fold is asked for before the send", func(t *testing.T) {
		folds, sent := run(t, true)
		noted := false
		for _, m := range sent {
			if strings.Contains(m.Content, graphMemoryContextMarker) {
				noted = true
			}
		}
		require.True(t, noted, "fixture: the first send carries the recall note")
		assert.Positive(t, folds, "the fold trigger must count the pending recall note before the send")
	})
}

// budgetLoopLLM calls the probe tool (each call with new arguments, so no
// repeat hint) for its first toolCalls calls, then answers. It records the
// messages of every call.
type budgetLoopLLM struct {
	mu        sync.Mutex
	toolCalls int
	calls     [][]llmtypes.Message
}

func (l *budgetLoopLLM) Name() string  { return "mock" }
func (l *budgetLoopLLM) Model() string { return "mock-v1" }

func (l *budgetLoopLLM) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, append([]llmtypes.Message(nil), msgs...))
	n := len(l.calls)
	usage := llmtypes.Usage{InputTokens: 10, OutputTokens: 5}
	if n > l.toolCalls {
		return &llmtypes.LLMResponse{Content: "done", Usage: usage}, nil
	}
	return &llmtypes.LLMResponse{
		ToolCalls: []llmtypes.ToolCall{{ID: fmt.Sprintf("c%d", n), Name: "probe", Input: map[string]interface{}{"n": n}}},
		Usage:     usage,
	}, nil
}

// TestTurnNotes_ToolBudgetReminderOncePerTurnInLoop (review F5, agent
// level): with a budget of 20 tool executions the reminder window is 15–17.
// Three calls of one turn fall in it, and the turn's last request carries one
// tool-budget reminder, the count of the first call in the window.
func TestTurnNotes_ToolBudgetReminderOncePerTurnInLoop(t *testing.T) {
	llm := &budgetLoopLLM{toolCalls: 19}
	ag := reliefSeamAgent(t, llm, "s-budget", NewSegmentedMemory("ROM", 200000, 20000))
	ag.config.MaxToolExecutions = 20
	ag.config.MaxTurns = 100
	ag.RegisterTool(namedStubTool{name: "probe"})

	_, err := ag.Chat(context.Background(), "s-budget", "go")
	require.NoError(t, err)

	llm.mu.Lock()
	defer llm.mu.Unlock()
	require.Len(t, llm.calls, 20)
	reminders, rows := 0, 0
	for _, m := range llm.calls[len(llm.calls)-1] {
		if c := strings.Count(m.Content, "You have executed many tool calls"); c > 0 {
			reminders += c
			rows++
			assert.Contains(t, m.Content, "(15 of 20 max)")
		}
	}
	assert.Equal(t, 1, reminders, "one tool-budget reminder per turn")
	assert.Equal(t, 1, rows)
}
