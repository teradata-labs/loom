// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
	"go.uber.org/zap/zaptest"
)

// scriptedLLM returns its responses in order, each with its own usage.
type scriptedLLM struct {
	mu        sync.Mutex
	responses []*types.LLMResponse
	idx       int
}

func (m *scriptedLLM) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return &types.LLMResponse{Content: "done", StopReason: "end_turn"}, nil
	}
	r := *m.responses[m.idx]
	m.idx++
	return &r, nil
}
func (m *scriptedLLM) Name() string  { return "scripted" }
func (m *scriptedLLM) Model() string { return "scripted-model" }

func usage(tokens int, cost float64) types.Usage {
	return types.Usage{InputTokens: tokens - 10, OutputTokens: 10, TotalTokens: tokens, CostUSD: cost}
}

// toolTurnAgent makes two LLM calls in one turn: a tool call, then the answer.
func toolTurnAgent(t *testing.T, name string) *agent.Agent {
	t.Helper()
	llm := &scriptedLLM{responses: []*types.LLMResponse{
		{ToolCalls: []types.ToolCall{{ID: "c1", Name: "echo_tool", Input: map[string]interface{}{"n": 1}}}, StopReason: "tool_use", Usage: usage(1000, 0.10)},
		{Content: name + " answer", StopReason: "end_turn", Usage: usage(500, 0.05)},
	}}
	ag := createMockAgent(t, name, llm)
	ag.RegisterTool(&shuttle.MockTool{
		MockName:        "echo_tool",
		MockDescription: "Echoes its input",
		MockExecute: func(_ context.Context, params map[string]interface{}) (*shuttle.Result, error) {
			return &shuttle.Result{Success: true, Data: params}, nil
		},
	})
	return ag
}

// oneCallAgent answers in a single LLM call.
func oneCallAgent(t *testing.T, name string) *agent.Agent {
	t.Helper()
	return createMockAgent(t, name, &scriptedLLM{responses: []*types.LLMResponse{
		{Content: name + " answer", StopReason: "end_turn", Usage: usage(200, 0.02)},
	}})
}

// Workflow costs must cover every LLM call: each agent's whole turn (not only
// its final call) and the calls the executor makes itself (the merge).
func TestForkJoinCost_CountsWholeTurnsAndTheMerge(t *testing.T) {
	merge := &scriptedLLM{responses: []*types.LLMResponse{{Content: "merged", StopReason: "end_turn", Usage: usage(300, 0.03)}}}
	o := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), LLMProvider: merge})

	result, err := o.Fork("analyze").
		WithAgents(toolTurnAgent(t, "tooler"), oneCallAgent(t, "plain")).
		Join(loomv1.MergeStrategy_SUMMARY).
		Execute(context.Background())
	require.NoError(t, err)
	require.Len(t, result.AgentResults, 2)

	// Look results up by output, not position: this test is about cost, not
	// about result order.
	costOf := map[string]*loomv1.AgentExecutionCost{}
	for _, r := range result.AgentResults {
		costOf[r.Output] = r.Cost
	}
	tooler, plain := costOf["tooler answer"], costOf["plain answer"]
	require.NotNil(t, tooler)
	require.NotNil(t, plain)
	assert.Equal(t, int32(1500), tooler.TotalTokens, "the tool-calling agent's cost covers both of its calls")
	assert.InDelta(t, 0.15, tooler.CostUsd, 1e-9)
	assert.Equal(t, int32(2), tooler.LlmCalls)
	assert.Equal(t, int32(200), plain.TotalTokens)
	assert.Equal(t, int32(1), plain.LlmCalls)

	assert.Equal(t, int32(2+1+1), result.Cost.LlmCalls, "2 + 1 agent calls + 1 merge call")
	assert.Equal(t, int32(1500+200+300), result.Cost.TotalTokens)
	assert.InDelta(t, 0.15+0.02+0.03, result.Cost.TotalCostUsd, 1e-9)
}

func TestParallelCost_CountsWholeTurnsAndTheMerge(t *testing.T) {
	merge := &scriptedLLM{responses: []*types.LLMResponse{{Content: "merged", StopReason: "end_turn", Usage: usage(300, 0.03)}}}
	o := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), LLMProvider: merge})

	result, err := o.Parallel().
		WithTask(toolTurnAgent(t, "tooler"), "task a").
		WithTask(oneCallAgent(t, "plain"), "task b").
		WithMergeStrategy(loomv1.MergeStrategy_SUMMARY).
		Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, int32(4), result.Cost.LlmCalls)
	assert.Equal(t, int32(2000), result.Cost.TotalTokens)
	assert.InDelta(t, 0.20, result.Cost.TotalCostUsd, 1e-9)
}

func TestPipelineCost_CountsWholeTurnsAndValidation(t *testing.T) {
	validator := &scriptedLLM{responses: []*types.LLMResponse{{Content: "valid", StopReason: "end_turn", Usage: usage(100, 0.01)}}}
	o := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), LLMProvider: validator})
	tooler := toolTurnAgent(t, "tooler")
	o.RegisterAgent("tooler", tooler)

	result, err := o.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
			InitialPrompt: "go",
			Stages:        []*loomv1.PipelineStage{{AgentId: "tooler", PromptTemplate: "{{previous}}", ValidationPrompt: "Is this valid? {{output}}"}},
		}},
	})
	require.NoError(t, err)

	assert.Equal(t, int32(2), result.AgentResults[0].Cost.LlmCalls)
	assert.Equal(t, int32(2+1), result.Cost.LlmCalls, "2 agent calls + 1 validation call")
	assert.Equal(t, int32(1500+100), result.Cost.TotalTokens)
	assert.InDelta(t, 0.16, result.Cost.TotalCostUsd, 1e-9)
}

func TestResultLLMCalls(t *testing.T) {
	tests := []struct {
		name string
		cost *loomv1.AgentExecutionCost
		want int32
	}{
		{"set", &loomv1.AgentExecutionCost{LlmCalls: 4}, 4},
		{"producer predates llm_calls", &loomv1.AgentExecutionCost{TotalTokens: 10}, 1},
		{"nil cost", nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resultLLMCalls(tt.cost))
		})
	}
}

func TestAuxUsage_ConcurrentAdds(t *testing.T) {
	var a auxUsage
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.addCall(usage(10, 0.001)) }()
		go func() {
			defer wg.Done()
			a.addTurn(&agent.Response{TurnUsage: usage(20, 0.002), TurnLLMCalls: 2})
		}()
	}
	wg.Wait()
	c := &loomv1.WorkflowCost{TotalTokens: 5, LlmCalls: 1, TotalCostUsd: 0.5}
	a.applyTo(c)
	assert.Equal(t, int32(1+50+100), c.LlmCalls)
	assert.Equal(t, int32(5+500+1000), c.TotalTokens)
	assert.InDelta(t, 0.5+0.05+0.1, c.TotalCostUsd, 1e-9)
}

func TestAuxUsage_AbsorbMovesCalls(t *testing.T) {
	var outer, inner auxUsage
	inner.addCall(usage(100, 0.01))
	inner.addCall(usage(50, 0.005))
	outer.addCall(usage(10, 0.001))

	outer.absorb(&inner)
	outer.absorb(&inner) // a second absorb must not double-count

	c := &loomv1.WorkflowCost{}
	outer.applyTo(c)
	assert.Equal(t, int32(3), c.LlmCalls)
	assert.Equal(t, int32(160), c.TotalTokens)
	assert.InDelta(t, 0.016, c.TotalCostUsd, 1e-9)

	empty := &loomv1.WorkflowCost{}
	inner.applyTo(empty)
	assert.Zero(t, empty.LlmCalls, "absorb empties the inner collector")
}
