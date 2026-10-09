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

// toolInputProgressProvider reports per-call tool-input progress the way a
// provider that knows the streaming call's identity does, and also fires the
// bare activity hook for every delta as providers do.
type toolInputProgressProvider struct {
	steps []llmtypes.ToolInputProgress
}

func (p *toolInputProgressProvider) Name() string  { return "tool-input-progress" }
func (p *toolInputProgressProvider) Model() string { return "test-model" }

func (p *toolInputProgressProvider) Chat(context.Context, []llmtypes.Message, []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	return &llmtypes.LLMResponse{Content: "chat"}, nil
}

func (p *toolInputProgressProvider) ChatStream(ctx context.Context, _ []llmtypes.Message, _ []shuttle.Tool, _ llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {
	for _, step := range p.steps {
		llmtypes.NotifyToolInputProgress(ctx, step)
		llmtypes.NotifyStreamActivity(ctx)
	}
	return &llmtypes.LLMResponse{StopReason: "tool_use"}, nil
}

func TestChatWithStreaming_ToolInputProgressNamesTheCallImmediately(t *testing.T) {
	var events []ProgressEvent
	ctx := newTestContextWithProgress(nil)
	a := &Agent{llm: &toolInputProgressProvider{steps: []llmtypes.ToolInputProgress{
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 10},
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 2000},
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 4000},
		{Index: 1, ToolCallID: "call_2", ToolName: "read_notebook", Bytes: 5},
	}}}

	_, err := a.chatWithStreaming(ctx, []Message{{Role: "user", Content: "build it"}}, nil, func(e ProgressEvent) { events = append(events, e) })
	require.NoError(t, err)

	var inputEvents []ProgressEvent
	for _, e := range events {
		if e.IsToolInputStream {
			inputEvents = append(inputEvents, e)
		}
	}
	// The first delta of each call is emitted at once; the rest of call 0's
	// deltas fall inside the throttle window, and no bare pulse is added.
	require.Len(t, inputEvents, 2)
	assert.Equal(t, "files", inputEvents[0].ToolName)
	assert.Equal(t, "call_1", inputEvents[0].ToolCallID)
	assert.Equal(t, int64(10), inputEvents[0].ToolInputBytes)
	assert.Equal(t, "read_notebook", inputEvents[1].ToolName)
	assert.Equal(t, "call_2", inputEvents[1].ToolCallID)
	for _, e := range inputEvents {
		assert.True(t, e.Droppable)
		assert.Empty(t, e.PartialContent, "tool-input bytes must never leak into the visible partial content")
	}
}

func TestChatWithStreaming_ToolInputProgressEmitsWhenNameArrivesLate(t *testing.T) {
	var events []ProgressEvent
	ctx := newTestContextWithProgress(nil)
	a := &Agent{llm: &toolInputProgressProvider{steps: []llmtypes.ToolInputProgress{
		{Index: 0, Bytes: 4},
		{Index: 0, ToolCallID: "call_1", ToolName: "files", Bytes: 9},
	}}}

	_, err := a.chatWithStreaming(ctx, []Message{{Role: "user", Content: "go"}}, nil, func(e ProgressEvent) { events = append(events, e) })
	require.NoError(t, err)

	var inputEvents []ProgressEvent
	for _, e := range events {
		if e.IsToolInputStream {
			inputEvents = append(inputEvents, e)
		}
	}
	require.Len(t, inputEvents, 2, "the delta that first supplies the name must not wait for the throttle")
	assert.Empty(t, inputEvents[0].ToolName)
	assert.Equal(t, "files", inputEvents[1].ToolName)
	assert.Equal(t, int64(9), inputEvents[1].ToolInputBytes)
}
