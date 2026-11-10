// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
)

func TestStageRendersHistory(t *testing.T) {
	tests := []struct {
		name     string
		tmpl     string
		passFull bool
		prior    int
		want     bool
	}{
		{"history placeholder", "Compile: {{history}}", false, 2, true},
		{"history placeholder but first stage", "Compile: {{history}}", false, 0, false},
		{"previous only", "Next: {{previous}}", true, 2, false},
		{"no placeholder, pass_full_history appends", "Compile the report", true, 1, true},
		{"no placeholder, pass_full_history off", "Compile the report", false, 1, false},
		{"both placeholders", "{{previous}} {{history}}", false, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stageRendersHistory(&loomv1.PipelineStage{PromptTemplate: tt.tmpl}, tt.passFull, tt.prior)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHistoryJoinPolicy(t *testing.T) {
	def := historyJoinPolicy(nil)
	assert.Equal(t, int32(MaxStageOutputBytes), def.MaxBranchBytes, "nil policy: the {{previous}} cap")
	assert.Zero(t, def.MaxInputTokens)
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_UNSPECIFIED, def.OverflowStrategy)

	custom := historyJoinPolicy(&loomv1.HistoryPolicy{
		MaxEntryBytes:    100,
		MaxInputTokens:   50,
		OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE,
	})
	assert.Equal(t, int32(100), custom.MaxBranchBytes)
	assert.Equal(t, int32(50), custom.MaxInputTokens)
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE, custom.OverflowStrategy)

	zeroCap := historyJoinPolicy(&loomv1.HistoryPolicy{MaxInputTokens: 7})
	assert.Equal(t, int32(MaxStageOutputBytes), zeroCap.MaxBranchBytes, "0 keeps the default cap")
}

// historyPipeline is two chatty stages followed by a final stage whose
// prompt is finalTemplate; final records what it was asked.
func historyPipeline(t *testing.T, orch *Orchestrator, final *funcLLMProvider, finalTemplate string, passFull bool, policy *loomv1.HistoryPolicy) *loomv1.WorkflowPattern {
	t.Helper()
	orch.RegisterAgent("s1", createMockAgent(t, "s1", newMockLLMProvider(bigText("s1", 30_000))))
	orch.RegisterAgent("s2", createMockAgent(t, "s2", newMockLLMProvider(bigText("s2", 30_000))))
	orch.RegisterAgent("s3", createMockAgent(t, "s3", final))
	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
			InitialPrompt: "go",
			Stages: []*loomv1.PipelineStage{
				{AgentId: "s1", PromptTemplate: "Research A"},
				{AgentId: "s2", PromptTemplate: "Research B after: {{previous}}"},
				{AgentId: "s3", PromptTemplate: finalTemplate},
			},
			PassFullHistory: passFull,
			HistoryPolicy:   policy,
		}},
	}
}

func reportLLM() *funcLLMProvider {
	return newFuncLLMProvider(func(p string) (string, error) {
		if isReducePrompt(p) {
			return "card: figure 42, owner Dana, decision keep.", nil
		}
		return "REPORT", nil
	})
}

func TestPipeline_HistoryEntriesAreCappedLikePrevious(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	result, err := orch.ExecutePattern(context.Background(), historyPipeline(t, orch, final, "Compile: {{history}}", false, nil))
	require.NoError(t, err)
	assert.Equal(t, "REPORT", result.MergedOutput)

	prompts := final.recorded()
	require.Len(t, prompts, 1)
	p := prompts[0]
	assert.Contains(t, p, "## AVAILABLE CONTEXT (SharedMemory)")
	assert.Contains(t, p, "Stage 1 output:")
	assert.Contains(t, p, "Stage 2 output:")
	assert.Equal(t, 2, strings.Count(p, "[OUTPUT TRUNCATED - Full data stored in SharedMemory"), "each entry carries the {{previous}} notice once")
	assert.Regexp(t, `key="pipeline-[0-9a-f]+-stage-1-output"`, p, "keys are scoped by workflow id")
	assert.Regexp(t, `key="pipeline-[0-9a-f]+-stage-2-output"`, p)
	assert.NotContains(t, p, `key="stage-1-output"`, "no unscoped key")
	assert.Less(t, len(p), 2*MaxStageOutputBytes+4096, "two capped entries plus template and header")
	for _, k := range workflowMemoryKeys(t, orch.sharedMemory) {
		assert.Empty(t, managedKeyWorkflow(k), "the run's stash is cleaned up when the run ends; found %s", k)
	}

	assert.Equal(t, "1", result.Metadata["history.stages_rendered"])
	assert.Equal(t, "false", result.Metadata["history.budget_applied"])
	assert.Equal(t, "0", result.Metadata["history.stages_over_budget"])
	assert.Equal(t, "2", result.Metadata["history.entries_truncated"])
	assert.Equal(t, "0", result.Metadata["history.entries_reduced"])
	assert.Equal(t, "0", result.Metadata["history.reduce_llm_calls"])
	assert.Equal(t, int32(3), result.Cost.LlmCalls, "no policy: no extra LLM calls")
}

func TestPipeline_HistoryEntriesStayFullWithoutSharedMemory(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, false)
	final := reportLLM()
	result, err := orch.ExecutePattern(context.Background(), historyPipeline(t, orch, final, "Compile: {{history}}", false, nil))
	require.NoError(t, err)

	p := final.recorded()[0]
	assert.NotContains(t, p, "OUTPUT TRUNCATED", "nothing is cut that the stage could not fetch back")
	assert.Greater(t, len(p), 60_000, "both full outputs inline")
	assert.Equal(t, "0", result.Metadata["history.entries_truncated"])
}

func TestPipeline_PassFullHistoryAppendIsBounded(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	_, err := orch.ExecutePattern(context.Background(), historyPipeline(t, orch, final, "Compile the report", true, nil))
	require.NoError(t, err)

	p := final.recorded()[0]
	assert.Contains(t, p, "Previous stages:")
	assert.Equal(t, 2, strings.Count(p, "[OUTPUT TRUNCATED - Full data stored in SharedMemory"))
}

func TestPipeline_HistoryPolicy_TruncateFitsBudget(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	policy := &loomv1.HistoryPolicy{MaxInputTokens: 1200, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE}
	result, err := orch.ExecutePattern(context.Background(), historyPipeline(t, orch, final, "Compile: {{history}}", false, policy))
	require.NoError(t, err)

	p := final.recorded()[0]
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(p), 1200)
	assert.Equal(t, 2, strings.Count(p, "[OUTPUT TRUNCATED - Full data stored in SharedMemory"))
	assert.Equal(t, "true", result.Metadata["history.budget_applied"])
	assert.Equal(t, "1", result.Metadata["history.stages_over_budget"])
	assert.Equal(t, "2", result.Metadata["history.entries_truncated"])
	assert.Equal(t, "0", result.Metadata["history.reduce_llm_calls"])
	assert.Equal(t, int32(3), result.Cost.LlmCalls, "truncate makes no LLM calls")
	for _, rp := range final.recorded() {
		assert.False(t, isReducePrompt(rp))
	}
}

func TestPipeline_HistoryPolicy_PreReduceIsTheDefault(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	policy := &loomv1.HistoryPolicy{MaxInputTokens: 1200}
	result, err := orch.ExecutePattern(context.Background(), historyPipeline(t, orch, final, "Compile: {{history}}", false, policy))
	require.NoError(t, err)

	var reduces, stagePrompts []string
	for _, p := range final.recorded() {
		if isReducePrompt(p) {
			reduces = append(reduces, p)
		} else {
			stagePrompts = append(stagePrompts, p)
		}
	}
	require.Len(t, reduces, 2, "the stage agent's own LLM reduces both oversize entries (no compressor role configured)")
	require.Len(t, stagePrompts, 1)
	p := stagePrompts[0]
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(p), 1200)
	assert.Equal(t, 2, strings.Count(p, "[OUTPUT REDUCED from"))
	assert.Contains(t, p, "card: figure 42, owner Dana, decision keep.")
	assert.Equal(t, "2", result.Metadata["history.entries_reduced"])
	assert.Equal(t, "0", result.Metadata["history.entries_truncated"])
	assert.Equal(t, "2", result.Metadata["history.reduce_llm_calls"])
	assert.Equal(t, int32(5), result.Cost.LlmCalls, "three stages plus two reduce calls")
}

func TestPipeline_HistoryPolicy_FailStopsBeforeTheStageRuns(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	policy := &loomv1.HistoryPolicy{MaxInputTokens: 1200, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL}
	_, err := orch.ExecutePattern(context.Background(), historyPipeline(t, orch, final, "Compile: {{history}}", false, policy))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrJoinInputOverBudget), err.Error())
	assert.Contains(t, err.Error(), "stage 3 history")
	assert.Empty(t, final.recorded(), "the final stage never ran")
}

func TestPipeline_HistoryPolicy_BudgetDerivesFromTheStageAgent(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	for _, name := range []string{"r1", "r2", "r3"} {
		orch.RegisterAgent(name, createMockAgent(t, name, newMockLLMProvider(bigText(name, 30_000))))
	}
	final := reportLLM()
	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 10_000
	cfg.ReservedOutputTokens = 2_000
	orch.RegisterAgent("small", agent.NewAgent(&mockBackend{}, final, agent.WithName("small"), agent.WithConfig(cfg)))

	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
			InitialPrompt: "go",
			Stages: []*loomv1.PipelineStage{
				{AgentId: "r1", PromptTemplate: "A"},
				{AgentId: "r2", PromptTemplate: "B"},
				{AgentId: "r3", PromptTemplate: "C"},
				{AgentId: "small", PromptTemplate: "Compile: {{history}}"},
			},
			HistoryPolicy: &loomv1.HistoryPolicy{OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE},
		}},
	})
	require.NoError(t, err)
	var stagePrompt string
	for _, p := range final.recorded() {
		if !isReducePrompt(p) {
			stagePrompt = p
		}
	}
	require.NotEmpty(t, stagePrompt)
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(stagePrompt), 4_000, "half of the agent's usable window")
	assert.Equal(t, "true", result.Metadata["history.budget_applied"])
	assert.Equal(t, "1", result.Metadata["history.stages_over_budget"])
}

func TestPipeline_HistoryPolicy_NoHistoryInPromptMeansNoBudgetCheck(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	policy := &loomv1.HistoryPolicy{MaxInputTokens: 10, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL}
	result, err := orch.ExecutePattern(context.Background(), historyPipeline(t, orch, final, "Finish: {{previous}}", false, policy))
	require.NoError(t, err, "a tiny budget with fail does not fire: the stage carries no history")
	_, ok := result.Metadata["history.stages_rendered"]
	assert.False(t, ok, "no history.* metadata when no stage rendered history")
}

func TestPipelineBuilder_WithHistoryPolicy(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	a1 := createMockAgent(t, "b1", newMockLLMProvider(bigText("b1", 30_000)))
	a2 := createMockAgent(t, "b2", reportLLM())
	_, err := orch.Pipeline("go").
		WithStage(a1, "A").
		WithStage(a2, "Compile: {{history}}").
		WithHistoryPolicy(&loomv1.HistoryPolicy{MaxInputTokens: 100, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL}).
		Execute(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrJoinInputOverBudget), "the builder carried the policy to the executor")
}

func TestIterativePipeline_HistoryIsBoundedOnceFromFullOutputs(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	orch.RegisterAgent("i1", createMockAgent(t, "i1", newMockLLMProvider(bigText("i1", 30_000))))
	orch.RegisterAgent("i2", createMockAgent(t, "i2", final))

	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: &loomv1.IterativeWorkflowPattern{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "i1", PromptTemplate: "A"},
					{AgentId: "i2", PromptTemplate: "Compile: {{history}}"},
				},
			},
			MaxIterations: 1,
			RestartPolicy: &loomv1.RestartPolicy{Enabled: true},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "iterative_pipeline", result.PatternType, "the iterative executor ran, not the plain-pipeline fallback")

	p := final.recorded()[0]
	assert.Equal(t, 1, strings.Count(p, "[OUTPUT TRUNCATED - Full data stored in SharedMemory"), "cut once at render time, never twice")
	assert.Regexp(t, `key="iterative-[0-9a-f]+-stage-1-output"`, p)
	assert.Less(t, len(p), MaxStageOutputBytes+4096)
	assert.Equal(t, "1", result.Metadata["history.stages_rendered"])
	assert.Equal(t, "1", result.Metadata["history.entries_truncated"])
}

func TestIterativePipeline_HistoryPolicyTruncateFitsBudget(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	final := reportLLM()
	orch.RegisterAgent("i1", createMockAgent(t, "i1", newMockLLMProvider(bigText("i1", 30_000))))
	orch.RegisterAgent("i2", createMockAgent(t, "i2", newMockLLMProvider(bigText("i2", 30_000))))
	orch.RegisterAgent("i3", createMockAgent(t, "i3", final))

	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Iterative{Iterative: &loomv1.IterativeWorkflowPattern{
			Pipeline: &loomv1.PipelinePattern{
				InitialPrompt: "go",
				Stages: []*loomv1.PipelineStage{
					{AgentId: "i1", PromptTemplate: "A"},
					{AgentId: "i2", PromptTemplate: "B"},
					{AgentId: "i3", PromptTemplate: "Compile: {{history}}"},
				},
				HistoryPolicy: &loomv1.HistoryPolicy{MaxInputTokens: 1200, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE},
			},
			MaxIterations: 1,
			RestartPolicy: &loomv1.RestartPolicy{Enabled: true},
		}},
	})
	require.NoError(t, err)
	p := final.recorded()[0]
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(p), 1200)
	assert.Equal(t, "true", result.Metadata["history.budget_applied"])
	assert.Equal(t, "1", result.Metadata["history.stages_over_budget"])
}
