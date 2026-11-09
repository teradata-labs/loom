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

// scriptedAgent answers each turn with the next scripted reply; every reply is
// one LLM call of the given size.
func scriptedAgent(t *testing.T, name string, tokens int, replies ...string) *agent.Agent {
	t.Helper()
	responses := make([]*types.LLMResponse, 0, len(replies))
	for _, r := range replies {
		responses = append(responses, &types.LLMResponse{Content: r, StopReason: "end_turn", Usage: usage(tokens, float64(tokens)/10000)})
	}
	return createMockAgent(t, name, &scriptedLLM{responses: responses})
}

// singleStage is a one-stage pipeline on agentID.
func singleStage(agentID string) *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
		InitialPrompt: "go",
		Stages:        []*loomv1.PipelineStage{{AgentId: agentID, PromptTemplate: "{{previous}}"}},
	}}}
}

// Workflow costs must also count the calls a run makes and then throws away:
// the conditional classifier's turn and its retries, a pipeline stage attempt
// that a retry replaced, and an iterative stage attempt that failed structure
// validation. Each case reproduces one row of the reviewer's ledger probe on
// PR #571: the want columns are the calls the mock providers actually served.
func TestWorkflowCost_CountsClassifierAndDiscardedAttempts(t *testing.T) {
	const validStageJSON = `{"stage_id": "worker", "outputs": {"answer": 42}}`

	tests := []struct {
		name string
		// validator is the orchestrator LLM (stage validation); nil = none.
		validator  []*types.LLMResponse
		agents     map[string]*agent.Agent
		pattern    *loomv1.WorkflowPattern
		wantCalls  int32
		wantTokens int32
	}{
		{
			name: "conditional classifier and branch",
			agents: func() map[string]*agent.Agent {
				return map[string]*agent.Agent{
					"classifier": scriptedAgent(t, "classifier", 100, "alpha"),
					"worker":     scriptedAgent(t, "worker", 200, "worked"),
				}
			}(),
			pattern: &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Conditional{Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "alpha or beta?",
				Branches:         map[string]*loomv1.WorkflowPattern{"alpha": singleStage("worker"), "beta": singleStage("worker")},
			}}},
			wantCalls:  2,   // classifier 1 + branch 1
			wantTokens: 300, // 100 + 200
		},
		{
			name: "conditional classifier retry",
			agents: func() map[string]*agent.Agent {
				return map[string]*agent.Agent{
					"classifier": scriptedAgent(t, "classifier", 100, "unknown", "alpha"),
					"worker":     scriptedAgent(t, "worker", 200, "worked"),
				}
			}(),
			pattern: &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Conditional{Conditional: &loomv1.ConditionalPattern{
				ConditionAgentId: "classifier",
				ConditionPrompt:  "alpha or beta?",
				Branches:         map[string]*loomv1.WorkflowPattern{"alpha": singleStage("worker"), "beta": singleStage("worker")},
				RetryPolicy:      &loomv1.OutputRetryPolicy{MaxRetries: 1},
			}}},
			wantCalls:  3,   // classifier 1 + classifier retry 1 + branch 1
			wantTokens: 400, // 100 + 100 + 200
		},
		{
			name: "pipeline stage retry_policy with one retry",
			validator: []*types.LLMResponse{
				{Content: "no", StopReason: "end_turn", Usage: usage(100, 0.01)},
				{Content: "valid", StopReason: "end_turn", Usage: usage(100, 0.01)},
			},
			agents: func() map[string]*agent.Agent {
				return map[string]*agent.Agent{"worker": scriptedAgent(t, "worker", 200, "first try", "second try")}
			}(),
			pattern: &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{{
					AgentId:          "worker",
					PromptTemplate:   "{{previous}}",
					ValidationPrompt: "Is this valid? {{output}}",
					RetryPolicy:      &loomv1.OutputRetryPolicy{MaxRetries: 1},
				}},
			}}},
			wantCalls:  4,   // attempt 1 + validation + retry + validation
			wantTokens: 600, // 200 + 100 + 200 + 100
		},
		{
			name: "pipeline stage retries exhausted",
			validator: []*types.LLMResponse{
				{Content: "no", StopReason: "end_turn", Usage: usage(100, 0.01)},
				{Content: "no", StopReason: "end_turn", Usage: usage(100, 0.01)},
			},
			agents: func() map[string]*agent.Agent {
				return map[string]*agent.Agent{"worker": scriptedAgent(t, "worker", 200, "first try", "second try")}
			}(),
			pattern: &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{{
					AgentId:          "worker",
					PromptTemplate:   "{{previous}}",
					ValidationPrompt: "Is this valid? {{output}}",
					RetryPolicy:      &loomv1.OutputRetryPolicy{MaxRetries: 1},
				}},
			}}},
			wantCalls:  4,   // the kept first attempt, the dropped retry, two validations
			wantTokens: 600, // 200 + 100 + 200 + 100
		},
		{
			name: "iterative structure-validation retry",
			agents: func() map[string]*agent.Agent {
				return map[string]*agent.Agent{"worker": scriptedAgent(t, "worker", 200, "not structured", validStageJSON)}
			}(),
			pattern: &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: &loomv1.IterativeWorkflowPattern{
				Pipeline: &loomv1.PipelinePattern{
					InitialPrompt: "go",
					Stages:        []*loomv1.PipelineStage{{AgentId: "worker", PromptTemplate: "{{previous}}"}},
				},
				MaxIterations: 1,
				RestartPolicy: &loomv1.RestartPolicy{Enabled: true, MaxValidationRetries: 1},
			}}},
			wantCalls:  2,   // the rejected attempt + the accepted retry
			wantTokens: 400, // 200 + 200
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := NewOrchestrator(Config{
				Logger:      zaptest.NewLogger(t),
				Tracer:      observability.NewNoOpTracer(),
				LLMProvider: &scriptedLLM{responses: tt.validator},
			})
			for id, ag := range tt.agents {
				o.RegisterAgent(id, ag)
			}

			result, err := o.ExecutePattern(context.Background(), tt.pattern)
			require.NoError(t, err, "the workflow must run to completion")
			require.NotNil(t, result.Cost, "a completed workflow reports a cost")

			assert.Equal(t, tt.wantCalls, result.Cost.LlmCalls, "llm_calls must equal the calls the providers served")
			assert.Equal(t, tt.wantTokens, result.Cost.TotalTokens, "total_tokens must equal the tokens the providers served")
			assert.InDelta(t, float64(tt.wantTokens)/10000, result.Cost.TotalCostUsd, 1e-9, "total_cost_usd must equal the cost the providers served")
		})
	}
}

func TestAuxUsage_AddResultCost(t *testing.T) {
	tests := []struct {
		name       string
		cost       *loomv1.AgentExecutionCost
		wantCalls  int32
		wantTokens int32
		wantUSD    float64
	}{
		{"whole turn", &loomv1.AgentExecutionCost{TotalTokens: 1500, CostUsd: 0.15, LlmCalls: 2}, 2, 1500, 0.15},
		{"producer predates llm_calls", &loomv1.AgentExecutionCost{TotalTokens: 200, CostUsd: 0.02}, 1, 200, 0.02},
		{"nil cost carries nothing", nil, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a auxUsage
			a.addResultCost(tt.cost)
			c := &loomv1.WorkflowCost{}
			a.applyTo(c)
			assert.Equal(t, tt.wantCalls, c.LlmCalls, "llm_calls of the discarded attempt")
			assert.Equal(t, tt.wantTokens, c.TotalTokens, "tokens of the discarded attempt")
			assert.InDelta(t, tt.wantUSD, c.TotalCostUsd, 1e-9, "USD of the discarded attempt")
		})
	}
}

// A swarm tie calls the judge agent, and an unmatched judge answer calls it
// again under the retry policy. Both judge turns are calls of the run.
func TestSwarmCost_CountsJudgeAndJudgeRetries(t *testing.T) {
	tests := []struct {
		name         string
		judgeReplies []string
		wantDecision string
		wantCalls    int32
		wantTokens   int32
	}{
		{
			name:         "judge breaks the tie",
			judgeReplies: []string{"PostgreSQL"},
			wantDecision: "PostgreSQL",
			wantCalls:    3,   // 2 votes + 1 judge turn
			wantTokens:   500, // 200 + 200 + 100
		},
		{
			name:         "judge retried once",
			judgeReplies: []string{"I think SQLite would be better", "PostgreSQL"},
			wantDecision: "PostgreSQL",
			wantCalls:    4,   // 2 votes + judge + judge retry
			wantTokens:   600, // 200 + 200 + 100 + 100
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
			o.RegisterAgent("voter1", scriptedAgent(t, "voter1", 200, "VOTE: PostgreSQL\nCONFIDENCE: 0.9\nREASONING: Best for OLTP"))
			o.RegisterAgent("voter2", scriptedAgent(t, "voter2", 200, "VOTE: MySQL\nCONFIDENCE: 0.9\nREASONING: Simpler to manage"))
			o.RegisterAgent("judge", scriptedAgent(t, "judge", 100, tt.judgeReplies...))

			result, err := o.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
				Pattern: &loomv1.WorkflowPattern_Swarm{Swarm: &loomv1.SwarmPattern{
					Question:            "Which database?",
					AgentIds:            []string{"voter1", "voter2"},
					Strategy:            loomv1.VotingStrategy_MAJORITY,
					ConfidenceThreshold: 0.5,
					JudgeAgentId:        "judge",
					RetryPolicy:         &loomv1.OutputRetryPolicy{MaxRetries: 1},
				}},
			})
			require.NoError(t, err, "the swarm must reach a decision")
			require.Equal(t, tt.wantDecision, result.MergedOutput, "precondition: the judge decided the tie")

			assert.Equal(t, tt.wantCalls, result.Cost.LlmCalls, "llm_calls must include every judge turn")
			assert.Equal(t, tt.wantTokens, result.Cost.TotalTokens, "total_tokens must include every judge turn")
			assert.InDelta(t, float64(tt.wantTokens)/10000, result.Cost.TotalCostUsd, 1e-9, "total_cost_usd must include every judge turn")
		})
	}
}
