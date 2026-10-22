// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
)

// A turn rebuilt from the session store replays its assistant tool_use rows
// without thinking blocks — they are turn-scoped replay material and never
// persist. The provider rejects that pairing with thinking on, so the request
// goes out with thinking suppressed and the resumed turn survives.
func TestRebuiltToolTurn_DetectsAStoreRebuild(t *testing.T) {
	withBlocks := []Message{
		{Role: "user", Turn: 3, Content: "do it"},
		{Role: "assistant", Turn: 3, ToolCalls: []ToolCall{{ID: "c1", Name: "shell_execute"}},
			ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "…", Signature: "sig"}}},
		{Role: "tool", Turn: 3, ToolUseID: "c1", Content: "ok"},
	}
	assert.False(t, rebuiltToolTurn(withBlocks),
		"a turn whose rows still carry their blocks needs no suppression")

	rebuilt := []Message{
		{Role: "user", Turn: 3, Content: "do it"},
		{Role: "assistant", Turn: 3, ToolCalls: []ToolCall{{ID: "c1", Name: "shell_execute"}}},
		{Role: "tool", Turn: 3, ToolUseID: "c1", Content: "ok"},
	}
	assert.True(t, rebuiltToolTurn(rebuilt),
		"an assistant tool_use row with no thinking blocks is the store-rebuild shape")

	// Settled turns render without their blocks by design, so a missing block
	// in an older turn is not this problem.
	settled := []Message{
		{Role: "assistant", Turn: 1, ToolCalls: []ToolCall{{ID: "old", Name: "shell_execute"}}},
		{Role: "tool", Turn: 1, ToolUseID: "old", Content: "ok"},
		{Role: "user", Turn: 4, Content: "next"},
	}
	assert.False(t, rebuiltToolTurn(settled),
		"only the newest turn is examined — settled turns are stripped on purpose")

	// A turn that has not called a tool yet has nothing to pair.
	assert.False(t, rebuiltToolTurn([]Message{{Role: "user", Turn: 5, Content: "hi"}}))
}

// Suppression needs BOTH signals: a resume, and rows that actually lost their
// blocks. Either alone leaves thinking on.
func TestProviderCtx_SuppressesOnlyOnAResumedRebuild(t *testing.T) {
	a := &Agent{}
	rebuilt := []Message{
		{Role: "assistant", Turn: 2, ToolCalls: []ToolCall{{ID: "c1", Name: "shell_execute"}}},
	}
	intact := []Message{
		{Role: "assistant", Turn: 2, ToolCalls: []ToolCall{{ID: "c1", Name: "shell_execute"}},
			ThinkingBlocks: []ThinkingBlock{{Type: "thinking", Thinking: "…"}}},
	}

	plain := &agentContext{Context: context.Background(), tracer: observability.NewNoOpTracer()}
	resumed := &agentContext{
		Context: contextWithResumedTurn(context.Background()),
		tracer:  observability.NewNoOpTracer(),
	}

	assert.False(t, llm.ThinkingSuppressed(a.providerCtx(plain, rebuilt)),
		"a rebuild outside a resume is not this problem")
	assert.False(t, llm.ThinkingSuppressed(a.providerCtx(resumed, intact)),
		"a resume whose rows kept their blocks needs no suppression")
	assert.True(t, llm.ThinkingSuppressed(a.providerCtx(resumed, rebuilt)),
		"a resumed turn rebuilt from the store must go out with thinking off")
}
