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
// (PinTurnNotes): fn runs once per provider call — one value for the relief
// estimate and the send — and a changing tail pins a note per distinct value,
// so each call's compiled view ends with that call's tail; the tail never
// becomes a stored row.
func TestTransientTail_SentEveryCallNeverStored(t *testing.T) {
	calls := 0
	seen, session := tailTurn(t, "tail_every", WithTransientTail(func(context.Context) string {
		calls++
		return fmt.Sprintf("  live-state-%d \n", calls)
	}))

	require.Len(t, seen, 2)
	assert.Equal(t, 2, calls, "fn runs once per provider call")
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

// tailNotes returns the tail notes a request carries, as bare texts, in send
// order.
func tailNotes(msgs []llmtypes.Message, marker string) []string {
	var out []string
	for _, m := range msgs {
		if IsSystemReminder(m) && strings.Contains(m.Content, marker) {
			out = append(out, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m.Content, "<system-reminder>\n"), "</system-reminder>")))
		}
	}
	return out
}

// TestReview589R3_TailPinnedOncePerValue (review round 3, F6): the tail is
// live state, but the request only grows within a turn (PinTurnNotes), so a
// call whose tail equals the turn's latest pinned tail must not stack another
// copy — a constant tail appears exactly once in the final request of a
// 4-call turn, and a changing tail appears once per distinct value. A note
// per call would leave every earlier value of the "live state" at harness
// authority next to the current one.
func TestReview589R3_TailPinnedOncePerValue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tail      func(n int) string
		wantTails []string
	}{
		{"constant", func(int) string { return "status: ok" }, []string{"status: ok"}},
		{"changing", func(n int) string { return fmt.Sprintf("queue depth: %d", n) },
			[]string{"queue depth: 1", "queue depth: 2", "queue depth: 3", "queue depth: 4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			marker := "queue depth"
			if tc.name == "constant" {
				marker = "status:"
			}
			llm := &recordingToolLLM{mockToolCallingLLM: &mockToolCallingLLM{
				responses: []mockLLMResponse{
					{toolCalls: []llmtypes.ToolCall{{ID: "c1", Name: "stub", Input: map[string]interface{}{"n": 1}}}},
					{toolCalls: []llmtypes.ToolCall{{ID: "c2", Name: "stub", Input: map[string]interface{}{"n": 2}}}},
					{toolCalls: []llmtypes.ToolCall{{ID: "c3", Name: "stub", Input: map[string]interface{}{"n": 3}}}},
					{content: "done"},
				},
			}}
			ag := NewAgent(&mockBackend{}, llm,
				WithConfig(&Config{MaxTurns: 10, MaxToolExecutions: 10, MaxIterations: 10}),
				WithTransientTail(func(context.Context) string {
					n++
					return tc.tail(n)
				}))
			ag.RegisterTool(namedStubTool{name: "stub"})

			_, err := ag.Chat(context.Background(), "tail_once_per_value", "go")
			require.NoError(t, err)

			llm.recMu.Lock()
			defer llm.recMu.Unlock()
			require.Len(t, llm.seen, 4, "3 tool calls, one answer")
			assert.Equal(t, tc.wantTails, tailNotes(llm.seen[len(llm.seen)-1], marker),
				"the final request carries one tail note per distinct value, in send order")
		})
	}
}

// TestReview589R3_TailFnOncePerProviderCall (review round 3, F7): fn runs
// once per provider call — the relief estimate counts the same value the send
// pins, so the request's tail notes carry the fn sequence itself, not the
// even-numbered samples of a fn run twice per call.
func TestReview589R3_TailFnOncePerProviderCall(t *testing.T) {
	calls := 0
	seen, _ := tailTurn(t, "tail_fn_once", WithTransientTail(func(context.Context) string {
		calls++
		return fmt.Sprintf("v%d", calls)
	}))

	require.Len(t, seen, 2, "one tool call, one answer")
	assert.Equal(t, 2, calls, "fn runs once per provider call")
	assert.Equal(t, []string{"v1"}, tailNotes(seen[0], "v"),
		"the first request carries the value the relief estimate counted")
	assert.Equal(t, []string{"v1", "v2"}, tailNotes(seen[1], "v"),
		"the second request carries the values in fn order, one note each")
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
	assert.Equal(t, 2, calls, "fn runs once per provider call, synthesis included")
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

// TestTransientTail_ResetMemory_TailPinnedOnce runs a second Chat on a
// session whose SegmentedMemory was reset between chats. Chat rebuilds the
// memory when it is nil (ensureSessionMemory), so this exercises the pinned
// path with a fresh memory: the loop send pins the tail after the turn's
// rows, and the budget-exhausted synthesis call with an UNCHANGED tail adds
// no second note (TurnReminder.Tail) — the loop-pinned note stays where it
// was sent, before the synthesis prompt, and the request carries exactly one
// copy of the tail.
func TestTransientTail_ResetMemory_TailPinnedOnce(t *testing.T) {
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

			_, err := ag.Chat(context.Background(), "tail_reset_mem", "warm up")
			require.NoError(t, err)
			session, ok := ag.GetSession("tail_reset_mem")
			require.True(t, ok)
			session.SegmentedMem = nil

			_, err = ag.Chat(context.Background(), "tail_reset_mem", "go")
			require.NoError(t, err)

			llm.recMu.Lock()
			defer llm.recMu.Unlock()
			require.Greater(t, len(llm.seen), tc.observedCall)
			msgs := llm.seen[tc.observedCall]
			require.NotEmpty(t, msgs)
			assert.Equal(t, []string{"live-state"}, tailNotes(msgs, "live-state"),
				"exactly one tail note, wherever the turn pinned it")
			last := msgs[len(msgs)-1]
			if tc.name == "loop send" {
				assert.Equal(t, "<system-reminder>\nlive-state\n</system-reminder>", last.Content,
					"the loop send's tail note is the request's last message")
			} else {
				assert.Contains(t, last.Content, "You must provide your final answer",
					"the synthesis prompt is the last row: an unchanged tail adds no second note")
				assert.False(t, IsSystemReminder(last))
			}
		})
	}
}
