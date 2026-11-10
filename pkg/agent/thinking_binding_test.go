// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// A thinking block is signed over what preceded it when the model made it —
// the system prompt, the tool list, every message before it — and a provider
// that checks the binding refuses a request replaying it on anything else.
// These tests hold the two places a block is let go: a committed fold, which
// rewrites the messages, and the provider's own refusal, which is the only
// witness to every other change.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// The refusal as each client reports it. The first is the Bedrock SDK's
// stream error, word for word as claude-fable-5-2 returned it after a fold.
var thinkingRefusals = []string{
	"stream error: POST \"https://bedrock-runtime.us-west-2.amazonaws.com/v1/messages\": 400 Bad Request " +
		"{\"message\":\"messages.3.content.0: Invalid `signature` in `thinking` block. The block is bound to a " +
		"different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to " +
		"\\\"drop_block\\\". That setting requires the `thinking-binding-controls-2026-08-01` value in the " +
		"`anthropic-beta` header. Content that preceded this block when it was created is missing from this " +
		"request, starting at `messages.1`.\"}",
	"API error (status 400): {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":" +
		"\"messages.1.content.0: Invalid `signature` in `thinking` block. The block is bound to a different " +
		"conversation. Remove the block. The `tools` list differs from when this block was created.\"}}",
	// A gateway that passes the wording through under its own envelope, with
	// nothing in it the retry policy reads as a refusal.
	"litellm.BadRequestError: messages.1.content.0: Invalid `signature` in `thinking` block. The block is " +
		"bound to a different conversation. Remove the block.",
}

// bindingLLM stands in for a provider that checks the binding: a request that
// replays a thinking block is refused with refusal, any other is answered.
type bindingLLM struct {
	refusal  string
	replayed []int // thinking blocks carried by each request, in order
}

func (b *bindingLLM) Chat(_ context.Context, msgs []Message, _ []shuttle.Tool) (*LLMResponse, error) {
	n := 0
	for i := range msgs {
		n += len(msgs[i].ThinkingBlocks)
	}
	b.replayed = append(b.replayed, n)
	if n > 0 {
		return nil, errors.New(b.refusal)
	}
	return &LLMResponse{Content: "ok"}, nil
}
func (b *bindingLLM) Name() string  { return "binding" }
func (b *bindingLLM) Model() string { return "binding-model" }

func thinkingCount(msgs []Message) int {
	n := 0
	for i := range msgs {
		n += len(msgs[i].ThinkingBlocks)
	}
	return n
}

func hasToolCall(msgs []Message, id string) bool {
	for i := range msgs {
		for _, c := range msgs[i].ToolCalls {
			if c.ID == id {
				return true
			}
		}
	}
	return false
}

// giveThinkingToTheLastAssistantRow puts a signed block on the pending row,
// as the response that made it did.
func giveThinkingToTheLastAssistantRow(t *testing.T, sm *SegmentedMemory) {
	t.Helper()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for i := len(sm.contextMessages) - 1; i >= 0; i-- {
		if sm.contextMessages[i].Role == "assistant" {
			sm.contextMessages[i].ThinkingBlocks = []ThinkingBlock{{Type: "thinking", Thinking: "why this call", Signature: "sig"}}
			return
		}
	}
	t.Fatal("no assistant row to carry a thinking block")
}

// midTurnMemory is a turn one step in: the ask, the model's tool call with the
// thinking that led to it, and the result.
func midTurnMemory(t *testing.T) *SegmentedMemory {
	t.Helper()
	sm := newCompileMemory(t)
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "do it", Turn: 1})
	sm.AddMessage(context.Background(), Message{Role: "assistant", Content: "reading", Turn: 1,
		ToolCalls:      []ToolCall{{ID: "c1", Name: "shell_execute"}},
		ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "why this call", Signature: "sig"}}})
	sm.AddMessage(context.Background(), Message{Role: "tool", ToolUseID: "c1", Content: "ok", Turn: 1})
	require.Equal(t, 1, thinkingCount(sm.GetMessagesForLLM()), "the current turn's block is replayed until something lets it go")
	return sm
}

func sessionContext(sm *SegmentedMemory) *agentContext {
	return &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer(),
		session: &Session{SegmentedMem: sm}}
}

// The in-loop fold keeps the pending pair whole on top of a past that is now a
// summary. The pair's thinking block was made on the past that is gone, so the
// fold lets it go; the call and its result stay.
func TestThinkingBinding_AnInloopFoldDropsTheBlocksOfWhatItKeeps(t *testing.T) {
	sm := newCompileMemory(t)
	sm.SetProtectedRecentTurns(2)
	seedInloopSession(sm)
	giveThinkingToTheLastAssistantRow(t, sm)
	require.Equal(t, 1, thinkingCount(sm.GetMessagesForLLM()), "before the fold the pending row replays its block")

	applied, _ := sm.ApplyFoldReply(context.Background(), "the work so far")
	require.True(t, applied)

	out := sm.GetMessagesForLLM()
	assert.Zero(t, thinkingCount(out), "a block made on the folded past must not be replayed on the summary")
	assert.True(t, hasToolCall(out, "c9"), "the pending call itself survives the fold")
}

// The pressure fold keeps the pending pair past a folded region the same way.
func TestThinkingBinding_APressureFoldDropsTheBlocksOfWhatItKeeps(t *testing.T) {
	sm := reliefSingleTurnFixture(t)
	sm.SetCompressor(&foldCompressor{out: "covers msg:1-99\nsummary of the analysis prose"})
	giveThinkingToTheLastAssistantRow(t, sm)
	require.Equal(t, 1, thinkingCount(sm.GetMessagesForLLM()), "before the fold the pending row replays its block")

	shed, _, _ := sm.ReleasePressure(context.Background(), 0)
	require.True(t, shed, "the fixture must fold")

	out := sm.GetMessagesForLLM()
	assert.Zero(t, thinkingCount(out), "a block made on the folded region must not be replayed past it")
	assert.True(t, hasToolCall(out, "cLast"), "the pending call itself survives the fold")
}

// Whatever else changed what a block was made on — the tool list, the system
// prompt — only the provider knows. Its refusal is answered the way it asks:
// the blocks go, from the request and from the context, and the request is
// sent once more. The turn goes on.
func TestThinkingBinding_ARefusedBlockIsDroppedAndTheRequestResentOnce(t *testing.T) {
	for _, refusal := range thinkingRefusals {
		sm := midTurnMemory(t)
		provider := &bindingLLM{refusal: refusal}
		a := &Agent{id: "binding-test", llm: provider, config: &Config{}}

		resp, err := a.chatWithRetry(sessionContext(sm), sm.GetMessagesForLLM(), nil)

		require.NoError(t, err, "the refusal must not end the turn: %s", refusal)
		assert.Equal(t, "ok", resp.Content)
		assert.Equal(t, []int{1, 0}, provider.replayed,
			"one refused send carrying the block, one resend without it, and no retry of the refused request")

		after := sm.GetMessagesForLLM()
		assert.Zero(t, thinkingCount(after), "the context lets the block go too, or every later call of the turn is refused")
		assert.True(t, hasToolCall(after, "c1"), "the row keeps its call")
	}
}

// A call with no session behind it — a one-off, a test double — has only its
// request to strip, and is resent all the same.
func TestThinkingBinding_ACallWithNoSessionIsStillResent(t *testing.T) {
	provider := &bindingLLM{refusal: thinkingRefusals[0]}
	a := &Agent{id: "binding-test", llm: provider, config: &Config{}}
	msgs := []Message{
		{Role: "user", Content: "do it", Turn: 1},
		{Role: "assistant", Turn: 1, ToolCalls: []ToolCall{{ID: "c1", Name: "shell_execute"}},
			ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "…", Signature: "sig"}}},
		{Role: "tool", Turn: 1, ToolUseID: "c1", Content: "ok"},
	}

	_, err := a.chatWithRetry(retryTestContext(), msgs, nil)

	require.NoError(t, err)
	assert.Equal(t, []int{1, 0}, provider.replayed)
	assert.Len(t, msgs[1].ThinkingBlocks, 1, "the caller's slice is not rewritten under it: the resend goes out on a copy")
}

// The resend exists to remove something. A refusal that arrives with no block
// in the request has nothing to remove, and is returned as it came.
func TestThinkingBinding_ARefusalWithNothingToRemoveIsReturned(t *testing.T) {
	provider := &flakyLLM{failures: 1000, err: errors.New(thinkingRefusals[0])}
	a := &Agent{id: "binding-test", llm: provider, config: &Config{}}

	_, err := a.chatWithRetry(retryTestContext(), []Message{{Role: "user", Content: "q", Turn: 1}}, nil)

	require.Error(t, err)
	assert.Equal(t, 1, provider.calls, "nothing to remove means nothing to resend")
}

// Only this refusal lets the blocks go. Any other failure leaves them where
// they are: they are still what the provider expects to read back.
func TestThinkingBinding_AnyOtherRefusalKeepsTheBlocks(t *testing.T) {
	sm := midTurnMemory(t)
	provider := &flakyLLM{failures: 1000, err: errors.New("API error (status 400): messages: field required")}
	a := &Agent{id: "binding-test", llm: provider, config: &Config{}}

	_, err := a.chatWithRetry(sessionContext(sm), sm.GetMessagesForLLM(), nil)

	require.Error(t, err)
	assert.Equal(t, 1, provider.calls, "a different 400 is not this problem and is not resent")
	assert.Equal(t, 1, thinkingCount(sm.GetMessagesForLLM()), "the block is untouched")
}

// The window estimate reads the compile, so a dropped block stops counting
// toward the window the moment it is dropped.
func TestThinkingBinding_ADroppedBlockNoLongerCountsTowardTheWindow(t *testing.T) {
	sm := midTurnMemory(t)
	estimate := func() int {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		return sm.estimateLocked()
	}
	before := estimate()

	sm.DropThinking()

	assert.Less(t, estimate(), before)
}
