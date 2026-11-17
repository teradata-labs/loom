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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// proseMemory builds turns 1..n of user/assistant prose, each message ~3 dense
// rows. Prose has no tool result to evict, so only a fold or the fallback view
// can bring it down.
func proseMemory(t *testing.T, maxTokens, reserved, turns int) *SegmentedMemory {
	t.Helper()
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", maxTokens, reserved)
	sm.SetThreshold(16384)
	for turn := 1; turn <= turns; turn++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: denseResult(turn) + denseResult(turn+100), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: denseResult(turn+200) + denseResult(turn+300), Turn: int64(turn)})
	}
	return sm
}

func sentTurns(sm *SegmentedMemory) map[int64]bool {
	return turnsOf(sm.GetMessagesForSend())
}

func turnsOf(msgs []Message) map[int64]bool {
	turns := map[int64]bool{}
	for _, m := range msgs {
		if m.Role != "system" {
			turns[m.Turn] = true
		}
	}
	return turns
}

func TestEnsureFits_UnderUsableIsANoOp(t *testing.T) {
	sm := proseMemory(t, 100000, 1000, 2)

	fit := sm.EnsureFits(context.Background())

	require.True(t, fit.Fits)
	assert.False(t, fit.Fallback)
	assert.False(t, fit.Shed)
	assert.Len(t, sentTurns(sm), 2, "nothing is dropped when the request already fits")
}

func TestEnsureFits_NoWindowConfiguredDisablesTheGuard(t *testing.T) {
	sm := proseMemory(t, 0, 0, 3)

	assert.True(t, sm.EnsureFits(context.Background()).Fits)
}

func TestEnsureFits_FallbackKeepsNewestTurnsAndFits(t *testing.T) {
	// usable 11000; 12 turns of ~3K tokens each is far over it.
	sm := proseMemory(t, 12000, 1000, 12)
	// A pass already in flight makes ReleasePressure shed nothing, as when the
	// compressor fails and every region is already flagged.
	sm.reliefInFlight = true

	fit := sm.EnsureFits(context.Background())

	require.True(t, fit.Fits, "the fallback view always fits when the newest turns do")
	require.True(t, fit.Fallback)
	require.LessOrEqual(t, fit.Estimate, fit.Usable)
	turns := sentTurns(sm)
	assert.True(t, turns[12], "the current turn is always sent")
	assert.False(t, turns[1], "old turns are dropped from the send view")
	assert.Len(t, turns, fit.TurnsKept)

	// Send-side only: the store still holds every turn for the next fold.
	stored := map[int64]bool{}
	for _, m := range sm.contextMessages {
		stored[m.Turn] = true
	}
	assert.Len(t, stored, 12)

	// The view holds for the rest of the turn, even with room again: widening
	// it would change what precedes the turn's own thinking blocks.
	sm.tokenBudget.MaxTokens = 1000000
	same := sm.EnsureFits(context.Background())
	assert.True(t, same.Fallback)
	assert.Equal(t, fit.TurnsKept, same.TurnsKept)

	// The next turn re-evaluates from scratch: with room, nothing is dropped.
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "next", Turn: 13})
	next := sm.EnsureFits(context.Background())
	assert.True(t, next.Fits)
	assert.False(t, next.Fallback)
	assert.Len(t, sentTurns(sm), 13)
}

func TestEnsureFits_CurrentTurnAloneTooBigReportsNotFitting(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 3000, 500)
	sm.SetThreshold(1 << 20)
	huge := ""
	for i := 0; i < 20; i++ {
		huge += denseResult(i)
	}
	sm.AddMessage(ctx, Message{Role: "user", Content: huge, Turn: 1})

	fit := sm.EnsureFits(ctx)

	assert.False(t, fit.Fits)
	assert.Greater(t, fit.Estimate, fit.Usable)
	assert.False(t, fit.Fallback)
}

func TestRowsFromTurn_KeepsSkillBodiesAndRowsWithoutATurn(t *testing.T) {
	rows := []Message{
		{Role: "user", Content: "old", Turn: 1},
		{Role: roleSkillBody, Content: "skill", Turn: 1},
		{Role: "assistant", Turn: 2, ToolCalls: []ToolCall{{ID: "a"}}},
		{Role: "tool", ToolUseID: "a", Content: "r"}, // no Turn: belongs to turn 2
		{Role: "user", Content: "now", Turn: 3},
	}

	var got []string
	for _, m := range rowsFromTurn(rows, 2) {
		got = append(got, fmt.Sprintf("%s:%s", m.Role, m.Content))
	}
	assert.Equal(t, []string{"skill_body:skill", "assistant:", "tool:r", "user:now"}, got)
}

// F2: the fallback view is for the provider only. History readers
// (GetConversationHistory, the lesson miner) still see every turn.
func TestEnsureFits_FallbackIsProviderSendOnly(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 12)
	sm.reliefInFlight = true

	fit := sm.EnsureFits(context.Background())

	require.True(t, fit.Fallback)
	assert.Len(t, sentTurns(sm), fit.TurnsKept)
	assert.Len(t, turnsOf(sm.GetMessagesForLLM()), 12, "history keeps every turn after a fallback send")
}

// F1: a settled turn's thinking blocks are replayed only after the context
// they were written after; the fallback view is a different one.
func TestEnsureFits_FallbackDropsSettledThinking(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	sm.SetThreshold(16384)
	for turn := 1; turn <= 12; turn++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: denseResult(turn) + denseResult(turn+100), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: denseResult(turn + 200), Turn: int64(turn),
			ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: fmt.Sprintf("t%d", turn), Signature: fmt.Sprintf("s%d", turn)}}})
	}
	sm.AddMessage(ctx, Message{Role: "user", Content: "now", Turn: 13})
	sm.reliefInFlight = true

	settledThinking := func(msgs []Message) []int64 {
		var turns []int64
		for _, m := range msgs {
			if m.Role == "assistant" && len(m.ThinkingBlocks) > 0 {
				turns = append(turns, m.Turn)
			}
		}
		return turns
	}
	require.NotEmpty(t, settledThinking(sm.GetMessagesForLLM()), "unchanged full context replays settled thinking")

	fit := sm.EnsureFits(ctx)

	require.True(t, fit.Fallback)
	assert.Greater(t, fit.TurnsKept, 1, "a settled turn is in the view")
	assert.Empty(t, settledThinking(sm.GetMessagesForSend()), "no settled assistant message in the fallback view carries thinking")
}

// Q3: the fallback view keeps the bodies of skills whose tools stay advertised.
func TestEnsureFits_FallbackKeepsSkillBodies(t *testing.T) {
	ctx := context.Background()
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	sm.SetThreshold(16384)
	sm.AddMessage(ctx, Message{Role: "user", Content: "load it", Turn: 1})
	sm.AddMessage(ctx, Message{Role: roleSkillBody, Content: "SKILL BODY", Turn: 1})
	for turn := 2; turn <= 12; turn++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: denseResult(turn) + denseResult(turn+100), Turn: int64(turn)})
		sm.AddMessage(ctx, Message{Role: "assistant", Content: denseResult(turn+200) + denseResult(turn+300), Turn: int64(turn)})
	}
	sm.reliefInFlight = true

	fit := sm.EnsureFits(ctx)

	require.True(t, fit.Fallback)
	var bodies int
	for _, m := range sm.GetMessagesForSend() {
		if m.Role == roleSkillBody {
			bodies++
		}
	}
	assert.Equal(t, 1, bodies)
	assert.False(t, sentTurns(sm)[2], "older prose is still dropped")
}

// D1: the guard is opt-in; without it an oversized request goes to the
// provider as before.
func TestChat_PreflightIsOffByDefault(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	ag := reliefSeamAgent(t, llm, "preflight_off", sm)
	require.False(t, ag.config.PreflightFit)

	_, err := ag.Chat(context.Background(), "preflight_off", strings.Repeat("lorem ipsum ", 6000))

	require.NoError(t, err)
	assert.Equal(t, 1, llm.calls)
}

// calibratingRefuseLLM refuses its first call and, like a provider that counts
// far more tokens than loom estimated, leaves loom's estimate scaled up.
type calibratingRefuseLLM struct {
	refuseOnceLLM
	sm *SegmentedMemory
}

func (l *calibratingRefuseLLM) Chat(ctx context.Context, msgs []Message, tools []shuttle.Tool) (*LLMResponse, error) {
	l.sm.mu.Lock()
	l.sm.estimateCalibration = calibrationMax
	l.sm.mu.Unlock()
	return l.refuseOnceLLM.Chat(ctx, msgs, tools)
}

// F3: the synthesis resend after a refusal is guarded too, so a request relief
// cannot fit is not sent (and billed) a second time.
func TestSynthesis_ResendAfterRefusalIsGuarded(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: strings.Repeat("lorem ipsum ", 3500), Turn: 1})
	l := &calibratingRefuseLLM{sm: sm}
	ag := reliefSeamAgent(t, l, "synth_guard", sm)
	ag.config.PreflightFit = true
	session, ok := ag.GetSession("synth_guard")
	require.True(t, ok)

	resp, err := ag.synthesizeFinalResponse(newCtxDumpContext("synth_guard", observability.NewNoOpTracer(), nil), nil,
		session, "", 1, 0, nil, Usage{}, 0)

	require.NoError(t, err, "the guidance message answers")
	assert.Len(t, l.calls, 1, "exactly one provider call")
	assert.Contains(t, resp.Metadata["synthesis_error"], "conversation too long")
}

// F4: a fallback send and a local refusal are visible on the turn's span.
func TestPreflightFit_SpanEvents(t *testing.T) {
	t.Run("fallback", func(t *testing.T) {
		llm := &capturingLLM{response: "done"}
		tracer := &spanKeepingTracer{}
		sm := proseMemory(t, 12000, 1000, 12)
		sm.reliefInFlight = true
		ag := reliefSeamAgent(t, llm, "pf_span_fallback", sm, WithTracer(tracer))
		ag.config.PreflightFit = true

		_, err := ag.Chat(context.Background(), "pf_span_fallback", "hi")

		require.NoError(t, err)
		evs := tracer.eventsNamed("context.preflight_fallback")
		require.NotEmpty(t, evs)
		assert.Positive(t, evs[0].Attributes["turns_kept"])
	})

	t.Run("refused", func(t *testing.T) {
		llm := &capturingLLM{response: "done"}
		tracer := &spanKeepingTracer{}
		sm := NewSegmentedMemory("ROM", 12000, 1000)
		ag := reliefSeamAgent(t, llm, "pf_span_refused", sm, WithTracer(tracer))
		ag.config.PreflightFit = true

		_, err := ag.Chat(context.Background(), "pf_span_refused", strings.Repeat("lorem ipsum ", 6000))

		require.Error(t, err)
		assert.NotEmpty(t, tracer.eventsNamed("context.preflight_refused"))
	})
}

func TestChat_PreflightRefusesOversizedRequestWithoutCallingTheProvider(t *testing.T) {
	llm := &capturingLLM{response: "done"}
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	ag := reliefSeamAgent(t, llm, "preflight_too_long", sm)
	ag.config.PreflightFit = true

	// Over the 11000 usable window by itself: nothing relief or the fallback can drop.
	_, err := ag.Chat(context.Background(), "preflight_too_long", strings.Repeat("lorem ipsum ", 6000))

	require.Error(t, err)
	var re *RecoverableError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "context_exhausted", re.ErrorType)
	assert.Contains(t, err.Error(), "compact the conversation or start a new session")
	assert.Equal(t, 0, llm.calls, "an oversized request is never sent")
}

// thinkingTurn adds turn t's assistant tool call with thinking blocks and its
// result: the turn has sent reasoning the provider checks against what precedes it.
func thinkingTurn(sm *SegmentedMemory, t int64) {
	ctx := context.Background()
	sm.AddMessage(ctx, Message{Role: "assistant", Turn: t,
		ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "plan", Signature: "sig"}},
		ToolCalls:      []ToolCall{{ID: "tc", Name: "run_query"}}})
	sm.AddMessage(ctx, Message{Role: "tool", ToolUseID: "tc", Content: "ok", Turn: t})
}

// Q1: once a turn has sent thinking under the full view, the guard does not
// switch to a fallback in front of it; the request is refused instead.
func TestEnsureFits_TurnWithThinkingKeepsItsView(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 12)
	thinkingTurn(sm, 12)
	sm.reliefInFlight = true

	fit := sm.EnsureFits(context.Background())

	assert.False(t, fit.Fits)
	assert.True(t, fit.ThinkingPinned)
	assert.False(t, fit.Fallback)
	assert.Len(t, sentTurns(sm), 12, "the view in front of the turn's thinking is unchanged")
}

// Q1: a fallback chosen before the turn's thinking holds, but does not narrow
// once the thinking was sent after it.
func TestEnsureFits_HeldFallbackDoesNotNarrowAfterThinking(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 12)
	sm.reliefInFlight = true
	first := sm.EnsureFits(context.Background())
	require.True(t, first.Fallback)
	require.Greater(t, first.TurnsKept, 1, "a narrower view exists to fall back to")
	thinkingTurn(sm, 12)

	sm.tokenBudget.MaxTokens = first.Estimate // the held view no longer fits
	fit := sm.EnsureFits(context.Background())

	assert.False(t, fit.Fits)
	assert.True(t, fit.ThinkingPinned)
	assert.Equal(t, first.TurnsKept, fit.TurnsKept)
	assert.Len(t, sentTurns(sm), first.TurnsKept, "the view is not narrowed")
}

// The refusal says why the conversation could not be trimmed.
func TestPreflightFit_RefusalNamesThePinnedReasoning(t *testing.T) {
	sm := proseMemory(t, 12000, 1000, 12)
	thinkingTurn(sm, 12)
	sm.reliefInFlight = true
	a := &Agent{config: &Config{PreflightFit: true}}

	core, logs := observer.New(zapcore.ErrorLevel)
	defer zap.ReplaceGlobals(zap.New(core))()
	span := &observability.Span{}

	_, _, err := a.preflightFit(newCtxDumpContext("pinned", observability.NewNoOpTracer(), nil), span,
		&Session{ID: "pinned", SegmentedMem: sm}, sm, nil, nil, nil)

	var re *RecoverableError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "context_exhausted", re.ErrorType)
	assert.Contains(t, err.Error(), "this turn's reasoning cannot be resent")
	assert.Equal(t, true, re.RecoveryPayload["thinking_pinned"])
	require.Len(t, span.Events, 1)
	assert.Equal(t, true, span.Events[0].Attributes["thinking_pinned"])
	entries := logs.FilterMessage("preflight: request cannot fit the usable window; not sending").All()
	require.Len(t, entries, 1)
	assert.Equal(t, true, entries[0].ContextMap()["thinking_pinned"])
}

// #672 F9: a pinned refusal on the synthesis path is told apart from a real
// overflow in the trace, the log and the error.
func TestSynthesisTooLong_RecordsThePin(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	defer zap.ReplaceGlobals(zap.New(core))()
	sm := proseMemory(t, 12000, 1000, 12)
	thinkingTurn(sm, 12)
	sm.reliefInFlight = true
	fit := sm.EnsureFits(context.Background())
	require.False(t, fit.Fits)
	require.True(t, fit.ThinkingPinned)
	span := &observability.Span{}

	err := synthesisTooLong(span, "pinned-synth", fit)

	assert.Contains(t, err.Error(), "this turn's reasoning cannot be resent")
	require.Len(t, span.Events, 1)
	assert.Equal(t, true, span.Events[0].Attributes["synthesis"])
	assert.Equal(t, true, span.Events[0].Attributes["thinking_pinned"])
	entries := logs.FilterMessage("preflight: synthesis request cannot fit the usable window; not sending").All()
	require.Len(t, entries, 1)
	assert.Equal(t, true, entries[0].ContextMap()["thinking_pinned"])

	// An ordinary overflow says so too.
	plain := synthesisTooLong(nil, "plain-synth", FitResult{Estimate: 20000, Usable: 11000})
	assert.NotContains(t, plain.Error(), "reasoning")
	assert.Equal(t, false, logs.FilterMessage("preflight: synthesis request cannot fit the usable window; not sending").All()[1].ContextMap()["thinking_pinned"])
}

// #674: when the current turn alone is over the window, the refusal names the
// turn's size, not the thinking pin, even though the pin is in force.
func TestEnsureFits_OverflowingTurnIsNotBlamedOnThePin(t *testing.T) {
	sm := NewSegmentedMemory("ROM", 12000, 1000)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: strings.Repeat("lorem ipsum ", 6000), Turn: 1})
	thinkingTurn(sm, 1)

	fit := sm.EnsureFits(context.Background())

	require.False(t, fit.Fits)
	assert.False(t, fit.ThinkingPinned)
	assert.Greater(t, fit.Estimate, fit.Usable)

	a := &Agent{config: &Config{PreflightFit: true}}
	_, _, err := a.preflightFit(newCtxDumpContext("overflow", observability.NewNoOpTracer(), nil), nil,
		&Session{ID: "overflow", SegmentedMem: sm}, sm, nil, nil, nil)
	var re *RecoverableError
	require.ErrorAs(t, err, &re)
	assert.Contains(t, err.Error(), "even with only the current turn kept")
	assert.NotContains(t, err.Error(), "reasoning")
	assert.Equal(t, false, re.RecoveryPayload["thinking_pinned"])
	assert.NotContains(t, synthesisTooLong(nil, "overflow", fit).Error(), "reasoning")
}
