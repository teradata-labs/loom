// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// toolInputStreamingProvider streams nothing through the token callback and
// instead reports `notifies` tool-input deltas — the shape of a model emitting
// a large tool argument.
type toolInputStreamingProvider struct {
	notifies int
	tokens   []string
}

func (p *toolInputStreamingProvider) Name() string  { return "tool-input-stream" }
func (p *toolInputStreamingProvider) Model() string { return "test-model" }

func (p *toolInputStreamingProvider) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: "chat"}, nil
}

func (p *toolInputStreamingProvider) ChatStream(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool, cb llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {
	for _, tok := range p.tokens {
		cb(tok)
	}
	for i := 0; i < p.notifies; i++ {
		llmtypes.NotifyStreamActivity(ctx)
	}
	return &llmtypes.LLMResponse{StopReason: "tool_use"}, nil
}

func TestChatWithStreaming_ToolInputDeltasBecomeThrottledActivityEvents(t *testing.T) {
	var events []ProgressEvent
	ctx := newTestContextWithProgress(func(e ProgressEvent) { events = append(events, e) })
	a := &Agent{llm: &toolInputStreamingProvider{notifies: 50}}

	resp, err := a.chatWithStreaming(ctx, []Message{{Role: "user", Content: "build it"}}, nil, func(e ProgressEvent) { events = append(events, e) })
	require.NoError(t, err)
	assert.Equal(t, "tool_use", resp.StopReason)

	var toolInput []ProgressEvent
	for _, e := range events {
		if e.IsToolInputStream {
			toolInput = append(toolInput, e)
		}
	}
	// 50 deltas arrive within microseconds: exactly one pulse per
	// toolInputActivityInterval, not one event per JSON fragment.
	require.Len(t, toolInput, 1, "tool-input activity must be throttled to one pulse per interval")
	e := toolInput[0]
	assert.True(t, e.Droppable, "a liveness pulse must be droppable under backpressure")
	assert.False(t, e.IsTokenStream)
	assert.Empty(t, e.PartialContent, "tool-input bytes must never leak into the visible partial content")
	assert.Equal(t, StageLLMGeneration, e.Stage)
}

func TestChatWithStreaming_NoToolInputDeltasNoActivityEvents(t *testing.T) {
	var events []ProgressEvent
	ctx := newTestContextWithProgress(nil)
	a := &Agent{llm: &toolInputStreamingProvider{tokens: []string{"hello", " world"}}}

	_, err := a.chatWithStreaming(ctx, []Message{{Role: "user", Content: "hi"}}, nil, func(e ProgressEvent) { events = append(events, e) })
	require.NoError(t, err)

	for _, e := range events {
		assert.False(t, e.IsToolInputStream, "a text-only stream must not emit tool-input activity")
	}
	require.NotEmpty(t, events, "text tokens still produce progress events")
}
