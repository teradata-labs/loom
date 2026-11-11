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
// message list the provider saw plus the stored session messages.
func tailTurn(t *testing.T, sid string, opts ...Option) ([][]llmtypes.Message, []llmtypes.Message) {
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
	return llm.seen, session.GetMessages()
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

func TestTransientTail_SentEveryCallNeverStored(t *testing.T) {
	calls := 0
	seen, stored := tailTurn(t, "tail_every", WithTransientTail(func(context.Context) string {
		calls++
		return fmt.Sprintf("  live-state-%d \n", calls)
	}))

	require.Len(t, seen, 2)
	for i, msgs := range seen {
		last := msgs[len(msgs)-1]
		assert.Equal(t, "user", last.Role, "call %d", i)
		assert.Equal(t, fmt.Sprintf("<system-reminder>\nlive-state-%d\n</system-reminder>", i+1), last.Content, "call %d", i)
	}
	assert.Equal(t, 2, calls, "fn runs once per provider call")
	for _, m := range stored {
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

func TestTransientTail_ChangesDoNotAlterPrefix(t *testing.T) {
	base, _ := tailTurn(t, "tail_prefix")
	n := 0
	with, _ := tailTurn(t, "tail_prefix", WithTransientTail(func(context.Context) string {
		n++
		return strings.Repeat("x", n*7)
	}))

	wb, ww := wire(base), wire(with)
	require.Len(t, ww, len(wb))
	for i := range wb {
		require.Len(t, ww[i], len(wb[i])+1, "call %d: exactly one trailing message added", i)
		assert.Equal(t, wb[i], ww[i][:len(wb[i])], "call %d: prefix byte-identical", i)
	}
	assert.NotEqual(t, with[0][len(with[0])-1].Content, with[1][len(with[1])-1].Content)
}

// The budget-exhausted synthesis call writes the final answer, so it carries
// the tail too: MaxTurns is reached after one loop call, and the synthesis
// call that follows must ride the same trailing reminder.
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
	last := llm.seen[1][len(llm.seen[1])-1]
	assert.Equal(t, "user", last.Role)
	assert.Equal(t, "<system-reminder>\nlive-state-synthesis\n</system-reminder>", last.Content)
	assert.Equal(t, 2, calls, "fn runs once per provider call, synthesis included")
}

// Each provider call that carries a tail emits transient_tail.added with the
// tail's byte length — never its text.
func TestTransientTail_SpanEventCarriesByteLength(t *testing.T) {
	tracer := &spanKeepingTracer{}
	_, _ = tailTurn(t, "tail_event", WithTracer(tracer),
		WithTransientTail(func(context.Context) string { return "12345" }))

	evs := tracer.eventsNamed("transient_tail.added")
	require.Len(t, evs, 2, "one event per provider call")
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
