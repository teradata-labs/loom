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
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

// tailTurn runs one turn (tool call, then final answer) and returns every
// message list the provider saw plus the session.
func tailTurn(t *testing.T, sid string, opts ...Option) ([][]llmtypes.Message, *Session) {
	t.Helper()
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "stub", Input: map[string]interface{}{}}}},
			{content: "done"},
		},
	}}
	opts = append([]Option{WithConfig(&Config{MaxTurns: 10, MaxToolExecutions: 10, MaxIterations: 10})}, opts...)
	ag := NewAgent(&mockBackend{}, llm, opts...)
	ag.RegisterTool(namedStubTool{name: "stub"})

	_, err := ag.Chat(context.Background(), sid, "go")
	require.NoError(t, err)

	llm.recMu.Lock()
	defer llm.recMu.Unlock()
	session, ok := ag.GetSession(sid)
	require.True(t, ok)
	return llm.seen, session
}

// rawRows returns the session's stored rows — no compiled reminders — what a
// persist would write.
func rawRows(t *testing.T, session *Session) []llmtypes.Message {
	t.Helper()
	seg, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok)
	return seg.GetMessages()
}

var stampRE = regexp.MustCompile(`^\[[^\]]*\] `)

// wire reduces each call's messages to what the provider reads (role, text,
// tool calls), dropping per-run ids, timestamps and the minute stamp on the
// user turn.
func wire(calls [][]llmtypes.Message) [][]string {
	out := make([][]string, len(calls))
	for i, msgs := range calls {
		for _, m := range msgs {
			out[i] = append(out[i], fmt.Sprintf("%s|%s|%v", m.Role, stampRE.ReplaceAllString(m.Content, ""), m.ToolCalls))
		}
	}
	return out
}

// With segmented memory the tail is pinned with the turn's built-in notes
// (PinTurnNotes): fn runs once per relief pass and once per provider call,
// each call's compiled view ends with that call's tail, and the tail never
// becomes a stored row.
func TestTransientTail_SentEveryCallNeverStored(t *testing.T) {
	calls := 0
	seen, session := tailTurn(t, "tail_every", WithTransientTail(func(context.Context) string {
		calls++
		return fmt.Sprintf("  live-state-%d \n", calls)
	}))

	require.Len(t, seen, 2)
	assert.Equal(t, 4, calls, "fn runs once per relief pass and once per provider call")
	for i, msgs := range seen {
		last := msgs[len(msgs)-1]
		assert.Equal(t, "user", last.Role, "call %d", i)
		assert.True(t, strings.HasPrefix(last.Content, "<system-reminder>\nlive-state-"), "call %d: %q", i, last.Content)
		assert.True(t, strings.HasSuffix(last.Content, "\n</system-reminder>"), "call %d: %q", i, last.Content)
	}
	rows := rawRows(t, session)
	require.NotEmpty(t, rows)
	for _, m := range rows {
		assert.NotContains(t, m.Content, "live-state", "the tail must never be persisted")
	}
}

func TestTransientTail_EmptyAddsNothing(t *testing.T) {
	base, _ := tailTurn(t, "tail_empty")
	blank, _ := tailTurn(t, "tail_empty", WithTransientTail(func(context.Context) string { return "  \n" }))
	nilFn, _ := tailTurn(t, "tail_empty", WithTransientTail(nil))
	assert.Equal(t, wire(base), wire(blank))
	assert.Equal(t, wire(base), wire(nilFn))
}

// With segmented memory a changing tail is pinned in place with the turn's
// built-in notes (PinTurnNotes), so the request only grows within the turn:
// every call's request is a byte-identical prefix of the next one's, and an
// earlier tail is never dropped in front of replayed content.
func TestTransientTail_ChangesDoNotAlterPrefix(t *testing.T) {
	n := 0
	seen, _ := tailTurn(t, "tail_prefix", WithTransientTail(func(context.Context) string {
		n++
		return strings.Repeat("x", n*7)
	}))

	ww := wire(seen)
	require.Len(t, ww, 2)
	require.Greater(t, len(ww[1]), len(ww[0]), "the request grows within the turn")
	for i, m := range ww[0] {
		assert.Equal(t, m, ww[1][i], "message %d: the earlier request is a byte-identical prefix", i)
	}
}

// The budget-exhausted synthesis call writes the final answer, so it carries
// the tail too: MaxTurns is reached after one loop call, and the synthesis
// call's compiled view still holds the note the loop call pinned — the tail
// stays in place for the rest of the turn.
func TestTransientTail_SynthesisCallCarriesTail(t *testing.T) {
	calls := 0
	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "stub", Input: map[string]interface{}{}}}},
		},
	}}
	ag := NewAgent(&mockBackend{}, llm,
		WithConfig(&Config{MaxTurns: 1, MaxToolExecutions: 10, MaxIterations: 10}),
		WithTransientTail(func(context.Context) string {
			calls++
			return "live-state-synthesis"
		}))
	ag.RegisterTool(namedStubTool{name: "stub"})

	_, err := ag.Chat(context.Background(), "tail_synthesis", "go")
	require.NoError(t, err)

	llm.recMu.Lock()
	defer llm.recMu.Unlock()
	require.Len(t, llm.seen, 2, "one loop call, one synthesis call")
	found := false
	for _, m := range llm.seen[1] {
		if m.Content == "<system-reminder>\nlive-state-synthesis\n</system-reminder>" {
			found = true
		}
	}
	assert.True(t, found, "the synthesis call carries the tail pinned at the loop call")
	assert.Equal(t, 4, calls, "fn runs once per relief pass and once per provider call, synthesis included")
}

// Each provider call that carries a tail emits transient_tail.added with the
// tail's byte length — never its text. The synthesis call emits it too
// (MaxTurns=1: one loop call, one synthesis call).
func TestTransientTail_SpanEventCarriesByteLength(t *testing.T) {
	tracer := &spanKeepingTracer{}
	_, _ = tailTurn(t, "tail_event", WithTracer(tracer),
		WithTransientTail(func(context.Context) string { return "12345" }))

	evs := tracer.eventsNamed("transient_tail.added")
	require.Len(t, evs, 2, "one event per provider call")
	for i, ev := range evs {
		assert.Equal(t, 5, ev.Attributes["bytes"], "event %d", i)
	}

	llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
		responses: []mockLLMResponse{
			{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "stub", Input: map[string]interface{}{}}}},
			{content: "done"},
		},
	}}
	synthTracer := &spanKeepingTracer{}
	ag := NewAgent(&mockBackend{}, llm,
		WithConfig(&Config{MaxTurns: 1, MaxToolExecutions: 10, MaxIterations: 10}),
		WithTracer(synthTracer),
		WithTransientTail(func(context.Context) string { return "12345" }))
	ag.RegisterTool(namedStubTool{name: "stub"})
	_, err := ag.Chat(context.Background(), "tail_event_synth", "go")
	require.NoError(t, err)

	evs = synthTracer.eventsNamed("transient_tail.added")
	require.Len(t, evs, 2, "one event per provider call, synthesis included")
	for i, ev := range evs {
		assert.Equal(t, 5, ev.Attributes["bytes"], "event %d", i)
	}
}

// An empty tail adds nothing and emits no event.
func TestTransientTail_SpanEvent_EmptyTailNoEvent(t *testing.T) {
	tracer := &spanKeepingTracer{}
	_, _ = tailTurn(t, "tail_event_empty", WithTracer(tracer),
		WithTransientTail(func(context.Context) string { return "  \n" }))

	assert.Empty(t, tracer.eventsNamed("transient_tail.added"))
}

// Without segmented memory the built-in notes and the caller tail ride ONE
// trailing <system-reminder> message, joined (withSystemReminder) — on the
// loop send and on the synthesis call alike. This harness has no built-in
// notes, so the single message carries the tail.
func TestTransientTail_NonSegmented_JoinedReminder(t *testing.T) {
	for _, tc := range []struct {
		name         string
		maxTurns     int
		observedCall int
	}{
		{"loop send", 10, 1},
		{"synthesis call", 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
				responses: []mockLLMResponse{
					{content: "warmup"},
					{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "stub", Input: map[string]interface{}{}}}},
					{content: "done"},
				},
			}}
			ag := NewAgent(&mockBackend{}, llm,
				WithConfig(&Config{MaxTurns: tc.maxTurns, MaxToolExecutions: 10, MaxIterations: 10}),
				WithTransientTail(func(context.Context) string { return "live-state" }))
			ag.RegisterTool(namedStubTool{name: "stub"})

			_, err := ag.Chat(context.Background(), "tail_nonseg", "warm up")
			require.NoError(t, err)
			session, ok := ag.GetSession("tail_nonseg")
			require.True(t, ok)
			session.SegmentedMem = nil

			_, err = ag.Chat(context.Background(), "tail_nonseg", "go")
			require.NoError(t, err)

			llm.recMu.Lock()
			defer llm.recMu.Unlock()
			require.Greater(t, len(llm.seen), tc.observedCall)
			msgs := llm.seen[tc.observedCall]
			require.NotEmpty(t, msgs)
			last := msgs[len(msgs)-1]
			assert.Equal(t, "user", last.Role)
			assert.Equal(t, "<system-reminder>\nlive-state\n</system-reminder>", last.Content,
				"one trailing message with the notes and the tail joined")
			require.Greater(t, len(msgs), 1)
			assert.False(t, IsSystemReminder(msgs[len(msgs)-2]), "exactly one trailing reminder message")
		})
	}
}
