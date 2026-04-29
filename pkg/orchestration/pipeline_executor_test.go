// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// mockLLMWithThinking returns predictable responses with optional thinking content.
type mockLLMWithThinking struct {
	mu        sync.Mutex
	responses []mockThinkingResponse
	callCount int
}

type mockThinkingResponse struct {
	content  string
	thinking string
}

func newMockLLMWithThinking(responses ...mockThinkingResponse) *mockLLMWithThinking {
	return &mockLLMWithThinking{responses: responses}
}

func (m *mockLLMWithThinking) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var resp mockThinkingResponse
	if m.callCount < len(m.responses) {
		resp = m.responses[m.callCount]
	} else {
		resp = mockThinkingResponse{content: "fallback response"}
	}
	m.callCount++

	return &llmtypes.LLMResponse{
		Content:    resp.content,
		Thinking:   resp.thinking,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
			CostUSD:      0.001,
		},
	}, nil
}

func (m *mockLLMWithThinking) Name() string  { return "mock-thinking" }
func (m *mockLLMWithThinking) Model() string { return "mock-thinking-model" }

func TestPipeline_PartialResultsEmittedAfterEachStage(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var events []WorkflowProgressEvent

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
		ProgressCallback: func(event WorkflowProgressEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		},
	})

	stage1Agent := createMockAgent(t, "stage1", newMockLLMProvider("stage 1 output"))
	stage2Agent := createMockAgent(t, "stage2", newMockLLMProvider("stage 2 output"))
	stage3Agent := createMockAgent(t, "stage3", newMockLLMProvider("stage 3 output"))

	orch.RegisterAgent("stage1", stage1Agent)
	orch.RegisterAgent("stage2", stage2Agent)
	orch.RegisterAgent("stage3", stage3Agent)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "start",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "stage1", PromptTemplate: "{{previous}}"},
					{AgentId: "stage2", PromptTemplate: "{{previous}}"},
					{AgentId: "stage3", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, 3, len(result.AgentResults))

	mu.Lock()
	defer mu.Unlock()

	// Filter to only pipeline progress events (exclude the initial/final events
	// from ExecutePattern itself).
	var pipelineEvents []WorkflowProgressEvent
	for _, ev := range events {
		if ev.PatternType == "pipeline" && strings.Contains(ev.Message, "Stage") && strings.Contains(ev.Message, "completed") {
			pipelineEvents = append(pipelineEvents, ev)
		}
	}

	require.Equal(t, 3, len(pipelineEvents), "should emit one progress event per stage")

	// After stage 1: 1 partial result
	assert.Equal(t, 1, len(pipelineEvents[0].PartialResults))
	assert.Equal(t, "stage1", pipelineEvents[0].PartialResults[0].AgentId)
	assert.Equal(t, "stage 1 output", pipelineEvents[0].PartialResults[0].Output)

	// After stage 2: 2 partial results
	assert.Equal(t, 2, len(pipelineEvents[1].PartialResults))
	assert.Equal(t, "stage2", pipelineEvents[1].PartialResults[1].AgentId)

	// After stage 3: 3 partial results
	assert.Equal(t, 3, len(pipelineEvents[2].PartialResults))
	assert.Equal(t, "stage3", pipelineEvents[2].PartialResults[2].AgentId)
}

func TestPipeline_PartialResultsProgressPercentage(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var events []WorkflowProgressEvent

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
		ProgressCallback: func(event WorkflowProgressEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		},
	})

	stage1Agent := createMockAgent(t, "s1", newMockLLMProvider("output"))
	stage2Agent := createMockAgent(t, "s2", newMockLLMProvider("output"))

	orch.RegisterAgent("s1", stage1Agent)
	orch.RegisterAgent("s2", stage2Agent)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "s1", PromptTemplate: "{{previous}}"},
					{AgentId: "s2", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var pipelineEvents []WorkflowProgressEvent
	for _, ev := range events {
		if ev.PatternType == "pipeline" && strings.Contains(ev.Message, "Stage") && strings.Contains(ev.Message, "completed") {
			pipelineEvents = append(pipelineEvents, ev)
		}
	}

	require.Equal(t, 2, len(pipelineEvents))

	// Stage 1 of 2 = 50%
	assert.Equal(t, int32(50), pipelineEvents[0].Progress)
	// Stage 2 of 2 = 100%
	assert.Equal(t, int32(100), pipelineEvents[1].Progress)
}

func TestPipeline_PartialResultsNextAgentID(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var events []WorkflowProgressEvent

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
		ProgressCallback: func(event WorkflowProgressEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		},
	})

	stage1Agent := createMockAgent(t, "first", newMockLLMProvider("output"))
	stage2Agent := createMockAgent(t, "second", newMockLLMProvider("output"))

	orch.RegisterAgent("first", stage1Agent)
	orch.RegisterAgent("second", stage2Agent)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "first", PromptTemplate: "{{previous}}"},
					{AgentId: "second", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var pipelineEvents []WorkflowProgressEvent
	for _, ev := range events {
		if ev.PatternType == "pipeline" && strings.Contains(ev.Message, "Stage") && strings.Contains(ev.Message, "completed") {
			pipelineEvents = append(pipelineEvents, ev)
		}
	}

	require.Equal(t, 2, len(pipelineEvents))

	// After stage 1 completes, next agent is "second"
	assert.Equal(t, "second", pipelineEvents[0].CurrentAgentID)
	// After stage 2 (last), no next agent
	assert.Equal(t, "", pipelineEvents[1].CurrentAgentID)
}

func TestPipeline_ThinkingCapturedInMetadata(t *testing.T) {
	t.Parallel()

	thinkingLLM := newMockLLMWithThinking(
		mockThinkingResponse{content: "analysis result", thinking: "Let me reason about this step by step..."},
	)
	ag := createMockAgent(t, "thinker", thinkingLLM)

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})
	orch.RegisterAgent("thinker", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Analyze this",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "thinker", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	require.Equal(t, 1, len(result.AgentResults))

	meta := result.AgentResults[0].Metadata
	assert.Equal(t, "analysis result", result.AgentResults[0].Output)
	assert.Equal(t, "Let me reason about this step by step...", meta["thinking"])
}

func TestPipeline_EmptyThinkingOmittedFromMetadata(t *testing.T) {
	t.Parallel()

	noThinkingLLM := newMockLLMWithThinking(
		mockThinkingResponse{content: "plain response", thinking: ""},
	)
	ag := createMockAgent(t, "plain", noThinkingLLM)

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})
	orch.RegisterAgent("plain", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "Do something",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "plain", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	require.Equal(t, 1, len(result.AgentResults))

	meta := result.AgentResults[0].Metadata
	_, hasThinking := meta["thinking"]
	assert.False(t, hasThinking, "empty thinking should not be stored in metadata")
}

func TestPipeline_ChatWithProgressUsedWhenCallbackInContext(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var agentProgressEvents []agent.ProgressEvent

	progressCb := func(event agent.ProgressEvent) {
		mu.Lock()
		agentProgressEvents = append(agentProgressEvents, event)
		mu.Unlock()
	}

	ag := createMockAgent(t, "prog-agent", newMockLLMProvider("done"))

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})
	orch.RegisterAgent("prog-agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "prog-agent", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	// Inject progress callback into context — this is the path that triggers
	// ChatWithProgress instead of Chat.
	ctx := agent.ContextWithProgressCallback(context.Background(), progressCb)
	result, err := orch.ExecutePattern(ctx, pattern)
	require.NoError(t, err)
	assert.Equal(t, "done", result.MergedOutput)

	// The agent should have received the progress callback and emitted events.
	// Even with a simple mock LLM (no tool calls), the agent emits at least
	// start/complete events when ChatWithProgress is used.
	mu.Lock()
	defer mu.Unlock()
	assert.NotEmpty(t, agentProgressEvents, "agent should emit progress events when ChatWithProgress is used")
}

func TestPipeline_NoProgressCallbackUsesChat(t *testing.T) {
	t.Parallel()

	ag := createMockAgent(t, "simple-agent", newMockLLMProvider("simple output"))

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
	})
	orch.RegisterAgent("simple-agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "simple-agent", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	// No progress callback in context — plain Chat should be used.
	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "simple output", result.MergedOutput)
}

func TestPipeline_PartialResultsCostAndDuration(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var events []WorkflowProgressEvent

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
		ProgressCallback: func(event WorkflowProgressEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		},
	})

	ag := createMockAgent(t, "cost-agent", newMockLLMProvider("output"))
	orch.RegisterAgent("cost-agent", ag)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "analyze",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "cost-agent", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var pipelineEvents []WorkflowProgressEvent
	for _, ev := range events {
		if ev.PatternType == "pipeline" && strings.Contains(ev.Message, "Stage") && strings.Contains(ev.Message, "completed") {
			pipelineEvents = append(pipelineEvents, ev)
		}
	}

	require.Equal(t, 1, len(pipelineEvents))
	partial := pipelineEvents[0].PartialResults[0]
	assert.Greater(t, partial.DurationMs, int64(0), "partial result should have non-zero duration")
	assert.NotNil(t, partial.Cost, "partial result should have cost info")
	assert.Greater(t, partial.Cost.TotalTokens, int32(0), "partial result should have token counts")
}

func TestPipeline_PartialResultsMessageFormat(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var events []WorkflowProgressEvent

	orch := NewOrchestrator(Config{
		Logger: zaptest.NewLogger(t),
		Tracer: observability.NewNoOpTracer(),
		ProgressCallback: func(event WorkflowProgressEvent) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		},
	})

	ag1 := createMockAgent(t, "a", newMockLLMProvider("out"))
	ag2 := createMockAgent(t, "b", newMockLLMProvider("out"))
	orch.RegisterAgent("a", ag1)
	orch.RegisterAgent("b", ag2)

	pattern := &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "a", PromptTemplate: "{{previous}}"},
					{AgentId: "b", PromptTemplate: "{{previous}}"},
				},
			},
		},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	var pipelineEvents []WorkflowProgressEvent
	for _, ev := range events {
		if ev.PatternType == "pipeline" && strings.Contains(ev.Message, "Stage") && strings.Contains(ev.Message, "completed") {
			pipelineEvents = append(pipelineEvents, ev)
		}
	}

	require.Equal(t, 2, len(pipelineEvents))
	assert.Equal(t, "Stage 1 of 2 completed", pipelineEvents[0].Message)
	assert.Equal(t, "Stage 2 of 2 completed", pipelineEvents[1].Message)
}
