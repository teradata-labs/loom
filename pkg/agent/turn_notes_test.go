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

	"github.com/teradata-labs/loom/pkg/llm"
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

// TestPinTurnNotes_TailOncePerValuePerTurn (review round 3, F6): the caller
// tail (WithTransientTail) is live state sampled per call, but the request
// only grows within a turn, so a tail is sent once per distinct value: a call
// whose tail equals the turn's latest pinned tail adds no note, a changed
// value pins a new one, and a value the turn pinned earlier but replaced pins
// again — the latest tail note must always carry the current value, so the
// model never acts on stale state.
func TestPinTurnNotes_TailOncePerValuePerTurn(t *testing.T) {
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
	pin := func(text string) { sm.PinTurnNotes(TurnReminder{Tail: true, Text: text}) }
	step := func() { sm.AddMessage(ctx, Message{Role: "assistant", Content: "a", Turn: 1}) }

	pin("A")
	assert.Equal(t, []string{"A"}, notes())
	step()
	pin("A")
	assert.Equal(t, []string{"A"}, notes(), "unchanged state is not sent again")
	step()
	pin("B")
	assert.Equal(t, []string{"A", "B"}, notes(), "a changed value pins a new note")
	step()
	pin("A")
	assert.Equal(t, []string{"A", "B", "A"}, notes(),
		"a value the turn replaced pins again: the latest tail note must carry the current value")

	// A resend of the same call (no new row since) sends nothing again.
	pin("A")
	assert.Len(t, notes(), 3)

	// The pending relief estimate skips a tail equal to the latest pinned one.
	sm.SetTransientTail(TurnReminder{Tail: true, Text: "A"})
	sm.mu.RLock()
	assert.Empty(t, sm.transientTail, "the latest pinned tail is not pending again")
	sm.mu.RUnlock()
	sm.SetTransientTail(TurnReminder{Tail: true, Text: "C"})
	sm.mu.RLock()
	assert.Equal(t, systemReminderContent("C"), sm.transientTail, "a changed value is pending until pinned")
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

// TestTransientTail_CountedInReliefEstimate (review Q1, agent level): the
// caller tail is sampled once per provider call and made pending for the
// relief pass before the send, and pinning at the send clears it — the tail
// counts once as pending, then as a compiled message. Since fn now runs once
// per provider call, before SetTransientTail, it observes an empty pending
// tail on every call: nothing is left pending across sends. The
// estimate-counts-the-pending-tail property itself is pinned at unit level
// (TestPinTurnNotes_PendingTailSkipsPinnedNotes,
// TestPinTurnNotes_TailOncePerValuePerTurn) and for the shared SetTransientTail
// seam by TestTurnNotes_ReliefCountsRecallNoteBeforeFirstSend.
func TestTransientTail_CountedInReliefEstimate(t *testing.T) {
	sid := "tail_relief"
	var seg *SegmentedMemory
	var pendingAtFn []string
	var ag *Agent
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "stub", Input: map[string]interface{}{}}}},
			{content: "done"},
		},
	}}
	ag = NewAgent(&mockBackend{}, llm,
		WithConfig(&Config{MaxTurns: 10, MaxToolExecutions: 10, MaxIterations: 10}),
		WithTransientTail(func(context.Context) string {
			if s, ok := ag.GetSession(sid); ok {
				seg, _ = s.SegmentedMem.(*SegmentedMemory)
			}
			if seg != nil {
				seg.mu.RLock()
				pendingAtFn = append(pendingAtFn, seg.transientTail)
				seg.mu.RUnlock()
			}
			return "live-state"
		}))
	ag.RegisterTool(namedStubTool{name: "stub"})

	_, err := ag.Chat(context.Background(), sid, "go")
	require.NoError(t, err)

	require.NotNil(t, seg)
	assert.Equal(t, []string{"", ""}, pendingAtFn,
		"fn runs once per provider call and nothing is left pending across sends")
	seg.mu.RLock()
	assert.Empty(t, seg.transientTail, "pinning cleared the pending tail at the send")
	seg.mu.RUnlock()
}

// tailReliefFixture builds a small-window memory filled well under the relief
// marks and calibrates a caller tail whose PENDING estimate pushes the total
// past `cross` (an absolute estimate). The fill sits at 60% of the penalized
// start mark, so without the tail no mark is crossed. Returns the memory,
// its counting compressor and the tail text.
func tailReliefFixture(t *testing.T, cross func(start, synth int) int) (*SegmentedMemory, *countingCompressor, string) {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 16000, 1000)
	sm.SetThreshold(16384)
	comp := &countingCompressor{}
	sm.SetCompressor(comp)
	sm.mu.Lock()
	start := sm.startMarkLocked(0)
	synth := sm.startMarkLocked(pressureRecoveryPenalty)
	sm.mu.Unlock()
	for turn := int64(1); ; turn++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: denseText(fmt.Sprintf("q%d", turn), 10), Turn: turn})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: denseText(fmt.Sprintf("a%d", turn), 10), Turn: turn})
		sm.mu.Lock()
		est := sm.estimateLocked()
		sm.mu.Unlock()
		if est >= synth*60/100 {
			break
		}
	}
	for reps := 1; reps <= 1<<20; reps *= 2 {
		tail := denseText("tail", reps)
		sm.SetTransientTail(TurnReminder{Tail: true, Text: tail})
		sm.mu.Lock()
		est := sm.estimateLocked()
		sm.mu.Unlock()
		sm.SetTransientTail()
		if est >= cross(start, synth) {
			return sm, comp, tail
		}
	}
	t.Fatal("could not calibrate a caller tail that crosses the mark")
	return nil, nil, ""
}

// tailReliefLLM serves the fixture turn: the fold ask is answered with a
// summary JSON, the synthesis ask ("You must provide your final answer NOW")
// with a refusal when refuseOnce is set and a text answer otherwise, and the
// first toolCalls loop asks with a probe tool call ("done" after).
type tailReliefLLM struct {
	mu         sync.Mutex
	calls      [][]llmtypes.Message
	toolCalls  int
	refuseOnce bool
	refused    bool
}

// promptHas reports whether any message of the request carries s — the
// synthesis request's last message is the pinned tail note, not the prompt.
func promptHas(msgs []llmtypes.Message, s string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, s) {
			return true
		}
	}
	return false
}

func (l *tailReliefLLM) Name() string  { return "mock" }
func (l *tailReliefLLM) Model() string { return "mock-v1" }

func (l *tailReliefLLM) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, append([]llmtypes.Message(nil), msgs...))
	usage := llmtypes.Usage{InputTokens: 10, OutputTokens: 5}
	if promptHas(msgs, "Context maintenance: summarise the work so far") {
		return &llmtypes.LLMResponse{Content: `{"summary": "state carried forward"}`, Usage: usage}, nil
	}
	if promptHas(msgs, "You must provide your final answer NOW") {
		if l.refuseOnce && !l.refused {
			l.refused = true
			return nil, fmt.Errorf("API error (status 400): prompt is too long: %w", llm.ErrContextTooLong)
		}
		return &llmtypes.LLMResponse{Content: "final answer", Usage: usage}, nil
	}
	if len(l.calls) <= l.toolCalls {
		return &llmtypes.LLMResponse{
			ToolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "probe", Input: map[string]interface{}{}}},
			Usage:     usage,
		}, nil
	}
	return &llmtypes.LLMResponse{Content: "done", Usage: usage}, nil
}

// TestTransientTail_TailPushesReliefOverTheMark (review F9): only the caller
// tail pushes the estimate over the threshold, on both paths — the loop's
// fold trigger and the synthesis refusal's relief pass. Dropping the tail
// from either SetTransientTail call fails it: without the tail the estimate
// stays under the mark, so no fold is asked and the refusal relief no-ops.
func TestTransientTail_TailPushesReliefOverTheMark(t *testing.T) {
	t.Run("loop: the fold trigger counts the tail", func(t *testing.T) {
		sm, _, tail := tailReliefFixture(t, func(start, synth int) int { return start })
		llm := &tailReliefLLM{}
		ag := reliefSeamAgent(t, llm, "s-tail-relief", sm,
			WithTransientTail(func(context.Context) string { return tail }))
		ag.RegisterTool(namedStubTool{name: "probe"})

		_, err := ag.Chat(context.Background(), "s-tail-relief", "What about the walrus migration?")
		require.NoError(t, err)

		llm.mu.Lock()
		defer llm.mu.Unlock()
		folds := 0
		for _, call := range llm.calls {
			if promptHas(call, foldInstruction) {
				folds++
			}
		}
		assert.Positive(t, folds, "the fold trigger must count the pending caller tail before the send")
	})

	t.Run("synthesis: the refusal relief counts the tail", func(t *testing.T) {
		sm, comp, tail := tailReliefFixture(t, func(start, synth int) int { return synth })
		// fn is empty during the loop's single call and returns the tail from
		// the synthesis call on: the loop never pins it, so the pending tail
		// set at synthesis is the only thing that can cross the mark.
		fnCalls := 0
		llm := &tailReliefLLM{toolCalls: 1, refuseOnce: true}
		ag := reliefSeamAgent(t, llm, "s-tail-synth", sm,
			WithTransientTail(func(context.Context) string {
				fnCalls++
				if fnCalls == 1 {
					return ""
				}
				return tail
			}))
		ag.config.MaxTurns = 1
		ag.RegisterTool(namedStubTool{name: "probe"})

		_, err := ag.Chat(context.Background(), "s-tail-synth", "go")
		require.NoError(t, err)

		llm.mu.Lock()
		defer llm.mu.Unlock()
		var asks [][]llmtypes.Message
		for _, call := range llm.calls {
			if promptHas(call, "You must provide your final answer NOW") {
				asks = append(asks, call)
			}
		}
		require.Len(t, asks, 2, "the refused synthesis send is followed by one relief resend")
		bytes := func(msgs []llmtypes.Message) int {
			n := 0
			for _, m := range msgs {
				n += len(m.Content)
			}
			return n
		}
		assert.Positive(t, comp.calls, "the relief pass ran on the refusal")
		assert.Less(t, bytes(asks[1]), bytes(asks[0]),
			"the resend is smaller: relief shed because the pending tail crossed the mark")
	})
}

// TestReview589R7_NoLadderBeforeSynthesis (review round 7, F12): the merge
// re-added the pressure ladder before the synthesis send. The ladder answers
// a provider refusal only (main 6f7611f4): a turn that exhausts MaxTurns with
// the estimate at or above the start mark and NO provider refusal must not
// run the compressor at synthesis. fn is empty during the loop's single call
// and returns the tail from the synthesis call on, so the loop's estimate
// stays under the start mark (no in-loop fold) and the synthesis send
// crosses it with no refusal.
func TestReview589R7_NoLadderBeforeSynthesis(t *testing.T) {
	sm, comp, tail := tailReliefFixture(t, func(start, synth int) int { return start })
	fnCalls := 0
	llm := &tailReliefLLM{toolCalls: 1}
	ag := reliefSeamAgent(t, llm, "s-tail-noladder", sm,
		WithTransientTail(func(context.Context) string {
			fnCalls++
			if fnCalls == 1 {
				return ""
			}
			return tail
		}))
	ag.config.MaxTurns = 1
	ag.RegisterTool(namedStubTool{name: "probe"})

	resp, err := ag.Chat(context.Background(), "s-tail-noladder", "go")
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Content)
	assert.Zero(t, comp.calls, "the pressure ladder must not run before the synthesis send")
}

// TestPinTurnNotes_ResendWithChangedTailPinsNewNote (review Q5): a resend
// whose caller tail changed pins a second note — one per distinct text, in
// send order, each after the rows so far — so the request only grows and the
// first note stays in front of the replayed thinking. A resend with unchanged
// text sends nothing again.
func TestPinTurnNotes_ResendWithChangedTailPinsNewNote(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 200000, 20000)
	sm.AddMessage(ctx, Message{Role: "user", Content: "q1", Turn: 1})
	sm.AddMessage(ctx, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "t"}}, Turn: 1})
	sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: "c1", Content: "r1", Turn: 1})

	notes := func() []string {
		var out []string
		for _, m := range sm.GetMessagesForLLM() {
			if IsSystemReminder(m) {
				out = append(out, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m.Content, "<system-reminder>\n"), "</system-reminder>")))
			}
		}
		return out
	}

	sm.PinTurnNotes(TurnReminder{Text: "tail one"})
	// The resend: no new row since, changed text.
	sm.PinTurnNotes(TurnReminder{Text: "tail two"})
	assert.Equal(t, []string{"tail one", "tail two"}, notes(),
		"one note per distinct text, in send order")

	// Another resend with unchanged text sends nothing again.
	sm.PinTurnNotes(TurnReminder{Text: "tail two"})
	assert.Len(t, notes(), 2, "an unchanged resend is deduped by text")
}
