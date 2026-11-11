// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

// A thinking block is signed over what preceded it when the model made it —
// the system prompt, the tool list, every message before it — and a provider
// that checks the binding refuses a request replaying it on anything else.
// A committed fold rewrites what precedes the rows it keeps, so it lets their
// blocks go.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	t.Fatal("no assistant row to give a thinking block to")
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
