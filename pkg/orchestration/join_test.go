// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// funcLLMProvider answers each prompt through fn and records every prompt it
// saw. Safe for concurrent use: the pre-reduce path calls it from several
// goroutines at once.
type funcLLMProvider struct {
	mu      sync.Mutex
	prompts []string
	fn      func(prompt string) (string, error)
}

func newFuncLLMProvider(fn func(prompt string) (string, error)) *funcLLMProvider {
	return &funcLLMProvider{fn: fn}
}

func (m *funcLLMProvider) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	prompt := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			prompt = messages[i].Content
			break
		}
	}
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	m.mu.Unlock()

	content, err := m.fn(prompt)
	if err != nil {
		return nil, err
	}
	return &llmtypes.LLMResponse{
		Content:    content,
		StopReason: "stop",
		Usage:      llmtypes.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CostUSD: 0.001},
	}, nil
}

func (m *funcLLMProvider) Name() string  { return "func-mock" }
func (m *funcLLMProvider) Model() string { return "func-mock-model" }

func (m *funcLLMProvider) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.prompts))
	copy(out, m.prompts)
	return out
}

// isReducePrompt tells a pre-reduce call from the join call.
func isReducePrompt(p string) bool {
	return strings.HasPrefix(p, "Reduce the following output from agent")
}

// bigText is a multi-line body of roughly n bytes.
func bigText(label string, n int) string {
	var b strings.Builder
	line := fmt.Sprintf("%s: finding with figure 42, owner Dana, decision keep.\n", label)
	for b.Len() < n {
		b.WriteString(line)
	}
	return b.String()
}

func newJoinTestOrchestrator(t *testing.T, llm agent.LLMProvider, withMemory bool) *Orchestrator {
	t.Helper()
	cfg := Config{
		LLMProvider: llm,
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
	}
	if withMemory {
		sm, err := communication.NewSharedMemoryStore(observability.NewNoOpTracer(), zaptest.NewLogger(t))
		require.NoError(t, err)
		cfg.SharedMemory = sm
	}
	return NewOrchestrator(cfg)
}

func branchResults(sizes ...int) []*loomv1.AgentResult {
	out := make([]*loomv1.AgentResult, 0, len(sizes))
	for i, n := range sizes {
		out = append(out, &loomv1.AgentResult{
			AgentId:  fmt.Sprintf("a%d", i+1),
			Output:   bigText(fmt.Sprintf("a%d", i+1), n),
			Metadata: map[string]string{"agent_name": fmt.Sprintf("a%d", i+1)},
		})
	}
	return out
}

func workflowMemoryKeys(t *testing.T, sm *communication.SharedMemoryStore) []string {
	t.Helper()
	resp, err := sm.List(context.Background(), &loomv1.ListSharedMemoryKeysRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		AgentId:   "test",
	})
	require.NoError(t, err)
	return resp.Keys
}

func TestJoinPolicyActive(t *testing.T) {
	tests := []struct {
		name     string
		policy   *loomv1.JoinPolicy
		strategy loomv1.MergeStrategy
		want     bool
	}{
		{"nil policy", nil, loomv1.MergeStrategy_SUMMARY, false},
		{"agent on concatenate", &loomv1.JoinPolicy{AgentId: "j"}, loomv1.MergeStrategy_CONCATENATE, true},
		{"agent on first", &loomv1.JoinPolicy{AgentId: "j"}, loomv1.MergeStrategy_FIRST, true},
		{"agent on summary", &loomv1.JoinPolicy{AgentId: "j"}, loomv1.MergeStrategy_SUMMARY, true},
		{"no agent on summary", &loomv1.JoinPolicy{MaxInputTokens: 10}, loomv1.MergeStrategy_SUMMARY, true},
		{"no agent on consensus", &loomv1.JoinPolicy{MaxInputTokens: 10}, loomv1.MergeStrategy_CONSENSUS, true},
		{"no agent on voting", &loomv1.JoinPolicy{MaxInputTokens: 10}, loomv1.MergeStrategy_VOTING, true},
		{"no agent on best", &loomv1.JoinPolicy{MaxInputTokens: 10}, loomv1.MergeStrategy_BEST, true},
		{"no agent on concatenate", &loomv1.JoinPolicy{MaxInputTokens: 10}, loomv1.MergeStrategy_CONCATENATE, false},
		{"no agent on first", &loomv1.JoinPolicy{MaxInputTokens: 10}, loomv1.MergeStrategy_FIRST, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, joinPolicyActive(tt.policy, tt.strategy))
		})
	}
}

func TestJoiner_CollectBranches_CapsInlineAndStoresFull(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	j := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "j", MaxBranchBytes: 1024}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf-1", "p")

	results := branchResults(200, 20_000)
	results = append(results, &loomv1.AgentResult{
		AgentId:  "a3",
		Output:   "Error: boom",
		Metadata: map[string]string{"error": "boom"},
	})

	branches := j.collectBranches(context.Background(), results)
	require.Len(t, branches, 3)

	small, big, failed := branches[0], branches[1], branches[2]

	assert.Equal(t, joinBranchFull, small.action)
	assert.Equal(t, small.full, small.inline)
	assert.Equal(t, "wf-1-branch-1-output", small.memKey, "small outputs are stored too")

	assert.Equal(t, joinBranchTruncated, big.action)
	assert.LessOrEqual(t, len(big.inline), 1024)
	assert.True(t, strings.HasPrefix(big.full, big.inline), "inline is a prefix of the full output")
	assert.Equal(t, "wf-1-branch-2-output", big.memKey)

	assert.True(t, failed.failed)
	assert.Empty(t, failed.memKey, "failed branches are not stored")
	assert.Equal(t, "Error: boom", failed.inline)

	got, err := orch.sharedMemory.Get(context.Background(), &loomv1.GetSharedMemoryRequest{
		Namespace: loomv1.SharedMemoryNamespace_SHARED_MEMORY_NAMESPACE_WORKFLOW,
		Key:       "wf-1-branch-2-output",
		AgentId:   "reader",
	})
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, big.full, string(got.Value.Value))
}

func TestJoiner_CollectBranches_DefaultCapAndNoMemory(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, false)
	j := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "j"}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf-1", "p")

	branches := j.collectBranches(context.Background(), branchResults(DefaultJoinBranchBytes+5000, 100))
	require.Len(t, branches, 2)
	assert.Equal(t, joinBranchTruncated, branches[0].action)
	assert.LessOrEqual(t, len(branches[0].inline), DefaultJoinBranchBytes)
	assert.Empty(t, branches[0].memKey)
	assert.Equal(t, joinBranchFull, branches[1].action)

	rendered := renderJoinBranches(branches)
	assert.Contains(t, rendered, "SharedMemory is not configured")
	assert.NotContains(t, rendered, "shared_memory_read")
}

func TestJoiner_Render_PlaceholdersHeaderAndSplicing(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	j := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "j", MaxBranchBytes: 512}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf-9", "THE PROMPT")

	results := branchResults(100, 5000)
	results[0].Output = "literal {{prompt}} and {{branch_count}} stay as written inside a branch"
	branches := j.collectBranches(context.Background(), results)

	tmpl := "Task: {{prompt}} over {{branch_count}} branches.\n{{branches}}\nEnd."
	out := j.render(tmpl, branches, true)

	assert.True(t, strings.HasPrefix(out, "## AVAILABLE CONTEXT (SharedMemory)"), "join agent gets the key header")
	assert.Contains(t, out, "- `wf-9-branch-1-output`: branch 1, agent a1")
	assert.Contains(t, out, "- `wf-9-branch-2-output`: branch 2, agent a2")
	assert.Contains(t, out, "Task: THE PROMPT over 2 branches.")
	assert.Contains(t, out, "### Branch 1: agent a1\nliteral {{prompt}} and {{branch_count}} stay as written inside a branch")
	assert.Contains(t, out, "### Branch 2: agent a2\n")
	assert.Contains(t, out, `shared_memory_read(namespace="workflow", key="wf-9-branch-2-output")`)
	assert.True(t, strings.HasSuffix(out, "\nEnd."))

	noHeader := j.render(tmpl, branches, false)
	assert.False(t, strings.HasPrefix(noHeader, "## AVAILABLE CONTEXT"), "raw merge LLM has no tools; no header")

	appended := j.render("No placeholder here.", branches, false)
	assert.True(t, strings.HasPrefix(appended, "No placeholder here.\n\n### Branch 1: agent a1"), "sections are appended when the template forgets {{branches}}")
}

func TestJoiner_DefaultTemplatesCoverEveryStrategy(t *testing.T) {
	for _, s := range []loomv1.MergeStrategy{
		loomv1.MergeStrategy_MERGE_STRATEGY_UNSPECIFIED, loomv1.MergeStrategy_CONSENSUS, loomv1.MergeStrategy_VOTING,
		loomv1.MergeStrategy_CONCATENATE, loomv1.MergeStrategy_FIRST, loomv1.MergeStrategy_BEST, loomv1.MergeStrategy_SUMMARY,
	} {
		withPrompt := defaultJoinTemplate(s, true)
		assert.Contains(t, withPrompt, joinPlaceholderBranches, s.String())
		assert.Contains(t, withPrompt, joinPlaceholderPrompt, s.String())
		without := defaultJoinTemplate(s, false)
		assert.Contains(t, without, joinPlaceholderBranches, s.String())
		assert.NotContains(t, without, joinPlaceholderPrompt, s.String())
	}
}

func TestJoiner_Budget(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, false)

	explicit := newJoiner(orch, &loomv1.JoinPolicy{MaxInputTokens: 1234}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "")
	assert.Equal(t, 1234, explicit.budget(nil), "explicit budget wins without an agent")
	assert.Equal(t, 1234, explicit.budget(createMockAgent(t, "j", newMockLLMProvider("x"))), "explicit budget wins with an agent")

	derived := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "j"}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "")
	assert.Equal(t, 0, derived.budget(nil), "no agent, no explicit budget: no check")

	defaults := agent.DefaultTokenBudgetConfig()
	wantDefault := (defaults.MaxContextTokens - defaults.ReservedOutputTokens) * DefaultJoinInputBudgetPercent / 100
	assert.Equal(t, wantDefault, derived.budget(createMockAgent(t, "j", newMockLLMProvider("x"))))

	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	cfg.MaxContextTokens = 10_000
	cfg.ReservedOutputTokens = 2_000
	small := agent.NewAgent(&mockBackend{}, newMockLLMProvider("x"), agent.WithName("small"), agent.WithConfig(cfg))
	assert.Equal(t, 4_000, derived.budget(small), "half of the agent's usable window")
}

func TestJoiner_Run_FailStrategyStopsBeforeTheJoinCall(t *testing.T) {
	joinLLM := newFuncLLMProvider(func(string) (string, error) { return "never", nil })
	orch := newJoinTestOrchestrator(t, joinLLM, true)
	policy := &loomv1.JoinPolicy{
		MaxInputTokens:   200,
		OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL,
	}
	j := newJoiner(orch, policy, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "p")

	_, err := j.run(context.Background(), branchResults(4000, 4000, 4000))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrJoinInputOverBudget))
	assert.Contains(t, err.Error(), "overflow_strategy is fail")
	assert.Empty(t, joinLLM.recorded(), "no LLM call is paid for")
}

func TestJoiner_Run_TruncateFitsTheBudget(t *testing.T) {
	joinLLM := newFuncLLMProvider(func(string) (string, error) { return "MERGED", nil })
	orch := newJoinTestOrchestrator(t, joinLLM, true)
	budget := 900
	policy := &loomv1.JoinPolicy{
		MaxInputTokens:   int32(budget),
		OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE,
	}
	j := newJoiner(orch, policy, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "p")

	outcome, err := j.run(context.Background(), branchResults(6000, 6000, 6000))
	require.NoError(t, err)
	assert.Equal(t, "MERGED", outcome.Output)
	assert.Equal(t, "merge_llm", outcome.Metadata["join.mode"])
	assert.Equal(t, "truncate", outcome.Metadata["join.overflow"])
	assert.Equal(t, "3", outcome.Metadata["join.branches_truncated"])
	assert.Equal(t, "0", outcome.Metadata["join.branches_reduced"])
	assert.Equal(t, int32(1), outcome.LLMCalls)

	prompts := joinLLM.recorded()
	require.Len(t, prompts, 1, "one join call, no reduce calls")
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(prompts[0]), budget)
	assert.Contains(t, prompts[0], "Branch output truncated")
	assert.False(t, strings.HasPrefix(prompts[0], "## AVAILABLE CONTEXT"), "raw merge LLM has no tools; no header")
}

func TestJoiner_Run_PreReduceIsTheDefaultAndReducesOnlyOversizeBranches(t *testing.T) {
	var reduceCalls int32
	var mu sync.Mutex
	joinLLM := newFuncLLMProvider(func(p string) (string, error) {
		if isReducePrompt(p) {
			mu.Lock()
			reduceCalls++
			mu.Unlock()
			return "CARD: figure 42, owner Dana, decision keep.", nil
		}
		return "DECK", nil
	})
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("presenter", createMockAgent(t, "presenter", joinLLM))

	budget := 1200
	policy := &loomv1.JoinPolicy{AgentId: "presenter", MaxInputTokens: int32(budget)} // overflow_strategy left UNSPECIFIED
	j := newJoiner(orch, policy, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "p")

	outcome, err := j.run(context.Background(), branchResults(8000, 8000, 60))
	require.NoError(t, err)
	assert.Equal(t, "DECK", outcome.Output)
	assert.Equal(t, "agent", outcome.Metadata["join.mode"])
	assert.Equal(t, "presenter", outcome.Metadata["join.agent_id"])
	assert.Equal(t, "pre_reduce", outcome.Metadata["join.overflow"])
	assert.Equal(t, "2", outcome.Metadata["join.branches_reduced"])
	assert.Equal(t, "3", outcome.Metadata["join.branches_stored"])
	assert.Equal(t, int32(2), reduceCalls, "the small branch is not reduced")
	assert.Equal(t, int32(3), outcome.LLMCalls, "two reduce calls plus the join turn")
	assert.Equal(t, "presenter", outcome.AgentID)

	var joinPrompt string
	for _, p := range joinLLM.recorded() {
		if !isReducePrompt(p) {
			joinPrompt = p
		}
	}
	require.NotEmpty(t, joinPrompt)
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(joinPrompt), budget)
	assert.Contains(t, joinPrompt, "## AVAILABLE CONTEXT (SharedMemory)")
	assert.Contains(t, joinPrompt, "CARD: figure 42")
	assert.Contains(t, joinPrompt, "Branch output reduced from")
	assert.Contains(t, joinPrompt, `key="wf-branch-1-output"`)
	assert.Contains(t, joinPrompt, "### Branch 3: agent a3\na3: finding", "the small branch stays verbatim")
}

func TestJoiner_Run_PreReduce_LongReductionIsTruncatedToShare(t *testing.T) {
	joinLLM := newFuncLLMProvider(func(p string) (string, error) {
		if isReducePrompt(p) {
			return bigText("REDUCED-BUT-LONG", 20_000), nil
		}
		return "DECK", nil
	})
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("presenter", createMockAgent(t, "presenter", joinLLM))

	budget := 1000
	j := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "presenter", MaxInputTokens: int32(budget)}, loomv1.MergeStrategy_CONSENSUS, "fork-join", "wf", "p")

	outcome, err := j.run(context.Background(), branchResults(8000, 8000))
	require.NoError(t, err)
	assert.Equal(t, "2", outcome.Metadata["join.branches_reduced"])

	var joinPrompt string
	for _, p := range joinLLM.recorded() {
		if !isReducePrompt(p) {
			joinPrompt = p
		}
	}
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(joinPrompt), budget, "a non-compliant reduction is cut to its share")
	assert.Contains(t, joinPrompt, "REDUCED-BUT-LONG")
}

func TestJoiner_Run_PreReduce_ReduceErrorFallsBackToTruncation(t *testing.T) {
	joinLLM := newFuncLLMProvider(func(p string) (string, error) {
		if isReducePrompt(p) {
			return "", errors.New("compressor down")
		}
		return "DECK", nil
	})
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("presenter", createMockAgent(t, "presenter", joinLLM))

	budget := 1000
	j := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "presenter", MaxInputTokens: int32(budget)}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "p")

	outcome, err := j.run(context.Background(), branchResults(8000, 8000))
	require.NoError(t, err)
	assert.Equal(t, "DECK", outcome.Output)
	assert.Equal(t, "pre_reduce", outcome.Metadata["join.overflow"])
	assert.Equal(t, "0", outcome.Metadata["join.branches_reduced"])
	assert.Equal(t, "2", outcome.Metadata["join.branches_truncated"])
	assert.Equal(t, int32(1), outcome.LLMCalls, "failed reduce calls are not counted as usage")

	var joinPrompt string
	for _, p := range joinLLM.recorded() {
		if !isReducePrompt(p) {
			joinPrompt = p
		}
	}
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(joinPrompt), budget)
	assert.Contains(t, joinPrompt, "Branch output truncated")
}

func TestJoiner_Run_PreReduce_NoReduceLLMTruncates(t *testing.T) {
	// No orchestrator LLM and no join agent: nothing can reduce, so the
	// budget is met by truncation and the join itself then fails for want
	// of a merge LLM, with the legacy error text.
	orch := newJoinTestOrchestrator(t, nil, true)
	j := newJoiner(orch, &loomv1.JoinPolicy{MaxInputTokens: 1000}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "p")

	_, err := j.run(context.Background(), branchResults(8000, 8000))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM provider required for SUMMARY merge strategy")
}

func TestJoiner_Run_BudgetTooSmallForBranchCount(t *testing.T) {
	joinLLM := newFuncLLMProvider(func(string) (string, error) { return "never", nil })
	orch := newJoinTestOrchestrator(t, joinLLM, true)
	j := newJoiner(orch, &loomv1.JoinPolicy{
		MaxInputTokens:   150,
		OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE,
	}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "p")

	_, err := j.run(context.Background(), branchResults(4000, 4000, 4000))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrJoinInputOverBudget))
	assert.Contains(t, err.Error(), fmt.Sprintf("at least %d is required", MinJoinBranchShareTokens))
	assert.Empty(t, joinLLM.recorded())
}

func TestJoiner_Run_UnderBudgetLeavesBranchesAlone(t *testing.T) {
	joinLLM := newFuncLLMProvider(func(string) (string, error) { return "DECK", nil })
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("presenter", createMockAgent(t, "presenter", joinLLM))
	j := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "presenter", MaxInputTokens: 50_000}, loomv1.MergeStrategy_BEST, "fork-join", "wf", "p")

	outcome, err := j.run(context.Background(), branchResults(500, 500))
	require.NoError(t, err)
	assert.Equal(t, "none", outcome.Metadata["join.overflow"])
	assert.Equal(t, "0", outcome.Metadata["join.branches_truncated"])
	assert.Equal(t, "0", outcome.Metadata["join.branches_reduced"])
	assert.Equal(t, int32(1), outcome.LLMCalls)
	require.Len(t, joinLLM.recorded(), 1)
	assert.Contains(t, joinLLM.recorded()[0], "Evaluate these 2 responses")
}

func TestJoiner_PreReduce_ManyBranchesConcurrentUnderSemaphore(t *testing.T) {
	const branches = 12
	var inFlight, peak int32
	var mu sync.Mutex
	joinLLM := newFuncLLMProvider(func(p string) (string, error) {
		if !isReducePrompt(p) {
			return "DECK", nil
		}
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
		return "CARD", nil
	})
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.llmSemaphore = make(chan struct{}, 3)
	orch.RegisterAgent("presenter", createMockAgent(t, "presenter", joinLLM))

	sizes := make([]int, branches)
	for i := range sizes {
		sizes[i] = 6000
	}
	j := newJoiner(orch, &loomv1.JoinPolicy{AgentId: "presenter", MaxInputTokens: 6000}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "p")

	outcome, err := j.run(context.Background(), branchResults(sizes...))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%d", branches), outcome.Metadata["join.branches_reduced"])
	assert.Equal(t, int32(branches+1), outcome.LLMCalls)
	assert.LessOrEqual(t, peak, int32(3), "reduce calls respect the orchestrator LLM semaphore")
}

func TestTruncateToTokens(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, false)
	j := newJoiner(orch, &loomv1.JoinPolicy{}, loomv1.MergeStrategy_SUMMARY, "fork-join", "wf", "")
	counter := agent.GetTokenCounter()

	inputs := []string{
		bigText("plain", 10_000),
		strings.Repeat("héllo wörld ünïcode 日本語 — ", 400),
		strings.Repeat("x", 5000),
	}
	for _, in := range inputs {
		for _, max := range []int{16, 100, 500} {
			out := j.truncateToTokens(in, max)
			assert.LessOrEqual(t, counter.CountTokens(out), max)
			assert.True(t, strings.HasPrefix(in, out), "output is a prefix")
			assert.True(t, utf8.ValidString(out), "never splits a rune")
			assert.NotEmpty(t, out)
		}
	}
	assert.Equal(t, "short", j.truncateToTokens("short", 100), "under the bound is untouched")
	assert.Equal(t, "", j.truncateToTokens("anything", 0))
}

func TestCutAtLineBoundary(t *testing.T) {
	assert.Equal(t, "abc", cutAtLineBoundary("abc", 10))
	assert.Equal(t, "line one\nline two", cutAtLineBoundary("line one\nline two\nline three", 20))
	assert.Equal(t, "", cutAtLineBoundary("abc", 0))
	cut := cutAtLineBoundary("日本語日本語", 4)
	assert.True(t, utf8.ValidString(cut))
	assert.Equal(t, "日", cut)
}

// --- executor integration ---

func TestForkJoin_JoinAgent_EndToEnd(t *testing.T) {
	joinLLM := newRecordingLLMProvider("DECK")
	orch := newJoinTestOrchestrator(t, nil, true)
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("analyst-%d", i)
		orch.RegisterAgent(name, createMockAgent(t, name, newMockLLMProvider(bigText(name, 30_000))))
	}
	orch.RegisterAgent("presenter", createMockAgent(t, "presenter", joinLLM))

	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
			Prompt:        "Analyze Q3",
			AgentIds:      []string{"analyst-1", "analyst-2", "analyst-3"},
			MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			JoinPolicy: &loomv1.JoinPolicy{
				AgentId:        "presenter",
				PromptTemplate: "Build a deck for: {{prompt}}\n\n{{branches}}",
				MaxBranchBytes: 2048,
			},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "DECK", result.MergedOutput)
	assert.Len(t, result.AgentResults, 3, "the join agent is not a branch result")
	assert.Equal(t, "agent", result.Metadata["join.mode"])
	assert.Equal(t, "presenter", result.Metadata["join.agent_id"])
	assert.Equal(t, "3", result.Metadata["join.branches_truncated"])
	assert.Equal(t, "3", result.Metadata["join.branches_stored"])
	assert.Equal(t, "none", result.Metadata["join.overflow"])
	assert.Equal(t, int32(4), result.Cost.LlmCalls, "three branches plus the join turn")
	assert.Greater(t, result.Cost.AgentCostsUsd["presenter"], 0.0)
	assert.Equal(t, "recording-mock-model", result.ModelsUsed["presenter"])

	prompts := joinLLM.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "## AVAILABLE CONTEXT (SharedMemory)")
	assert.Contains(t, prompts[0], "Build a deck for: Analyze Q3")
	assert.Contains(t, prompts[0], "### Branch 1: agent analyst-1")
	assert.Contains(t, prompts[0], "### Branch 3: agent analyst-3")
	assert.Contains(t, prompts[0], "Branch output truncated: ")

	keys := workflowMemoryKeys(t, orch.sharedMemory)
	stored := 0
	for _, k := range keys {
		if strings.HasSuffix(k, "-branch-1-output") || strings.HasSuffix(k, "-branch-2-output") || strings.HasSuffix(k, "-branch-3-output") {
			stored++
		}
	}
	assert.Equal(t, 3, stored, "every branch's full output is in SharedMemory")
}

func TestForkJoin_JoinAgentMissing_FailsBeforeAnyBranchRuns(t *testing.T) {
	branchLLM := newRecordingLLMProvider("never")
	orch := newJoinTestOrchestrator(t, nil, false)
	orch.RegisterAgent("analyst", createMockAgent(t, "analyst", branchLLM))

	_, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
			Prompt:     "p",
			AgentIds:   []string{"analyst"},
			JoinPolicy: &loomv1.JoinPolicy{AgentId: "ghost"},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "join agent not found: ghost")
	assert.Empty(t, branchLLM.recordedPrompts(), "no branch was paid for")
}

func TestForkJoin_JoinPolicyWithoutAgent_LeavesConcatenateUnchanged(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("a", createMockAgent(t, "a", newMockLLMProvider("alpha")))
	orch.RegisterAgent("b", createMockAgent(t, "b", newMockLLMProvider("beta")))

	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
			Prompt:        "p",
			AgentIds:      []string{"a", "b"},
			MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			JoinPolicy:    &loomv1.JoinPolicy{MaxInputTokens: 1},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "=== Agent a ===\nalpha\n\n=== Agent b ===\nbeta", result.MergedOutput)
	_, hasJoinMeta := result.Metadata["join.mode"]
	assert.False(t, hasJoinMeta, "the policy was inert")
}

func TestForkJoin_JoinPolicyWithoutAgent_BoundsTheMergeLLM(t *testing.T) {
	mergeLLM := newRecordingLLMProvider("SUMMARY")
	orch := newJoinTestOrchestrator(t, mergeLLM, true)
	orch.RegisterAgent("a", createMockAgent(t, "a", newMockLLMProvider(bigText("a", 20_000))))
	orch.RegisterAgent("b", createMockAgent(t, "b", newMockLLMProvider(bigText("b", 20_000))))

	budget := 800
	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
			Prompt:        "p",
			AgentIds:      []string{"a", "b"},
			MergeStrategy: loomv1.MergeStrategy_SUMMARY,
			JoinPolicy: &loomv1.JoinPolicy{
				MaxInputTokens:   int32(budget),
				OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE,
			},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "SUMMARY", result.MergedOutput)
	assert.Equal(t, "merge_llm", result.Metadata["join.mode"])
	assert.Equal(t, "truncate", result.Metadata["join.overflow"])
	assert.Equal(t, int32(3), result.Cost.LlmCalls)
	prompts := mergeLLM.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.LessOrEqual(t, agent.GetTokenCounter().CountTokens(prompts[0]), budget)
	assert.Contains(t, prompts[0], "Original prompt: p")
	assert.Contains(t, prompts[0], "Summarize these 2 agent responses concisely")
}

func TestParallel_JoinAgent_EndToEnd(t *testing.T) {
	joinLLM := newRecordingLLMProvider("REPORT")
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("docs", createMockAgent(t, "docs", newMockLLMProvider(bigText("docs", 12_000))))
	orch.RegisterAgent("api", createMockAgent(t, "api", newMockLLMProvider("short api notes")))
	orch.RegisterAgent("editor", createMockAgent(t, "editor", joinLLM))

	result, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{Parallel: &loomv1.ParallelPattern{
			Tasks: []*loomv1.AgentTask{
				{AgentId: "docs", Prompt: "write the guide"},
				{AgentId: "api", Prompt: "write the api notes"},
			},
			MergeStrategy: loomv1.MergeStrategy_CONCATENATE,
			JoinPolicy:    &loomv1.JoinPolicy{AgentId: "editor", MaxBranchBytes: 4096},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "REPORT", result.MergedOutput)
	assert.Equal(t, "agent", result.Metadata["join.mode"])
	assert.Equal(t, "editor", result.Metadata["join.agent_id"])
	assert.Equal(t, "1", result.Metadata["join.branches_truncated"])
	assert.Equal(t, "2", result.Metadata["join.branches_stored"])
	assert.Equal(t, int32(3), result.Cost.LlmCalls)

	prompts := joinLLM.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "Combine the following 2 task outputs into one coherent result")
	assert.Contains(t, prompts[0], "### Branch 1: agent docs")
	assert.Contains(t, prompts[0], "### Branch 2: agent api\nshort api notes")
	assert.NotContains(t, prompts[0], "Original prompt:", "parallel has no fan-out prompt")
}

func TestParallel_JoinAgentMissing_FailsBeforeAnyTaskRuns(t *testing.T) {
	taskLLM := newRecordingLLMProvider("never")
	orch := newJoinTestOrchestrator(t, nil, false)
	orch.RegisterAgent("docs", createMockAgent(t, "docs", taskLLM))

	_, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Parallel{Parallel: &loomv1.ParallelPattern{
			Tasks:      []*loomv1.AgentTask{{AgentId: "docs", Prompt: "p"}},
			JoinPolicy: &loomv1.JoinPolicy{AgentId: "ghost"},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "join agent not found: ghost")
	assert.Empty(t, taskLLM.recordedPrompts())
}

// --- builders ---

func TestForkJoinBuilder_WithJoinPolicy(t *testing.T) {
	joinLLM := newRecordingLLMProvider("DECK")
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("presenter", createMockAgent(t, "presenter", joinLLM))

	result, err := orch.Fork("Analyze").
		WithAgents(createMockAgent(t, "a", newMockLLMProvider("alpha")), createMockAgent(t, "b", newMockLLMProvider("beta"))).
		Join(loomv1.MergeStrategy_CONCATENATE).
		WithJoinPolicy(&loomv1.JoinPolicy{AgentId: "presenter"}).
		Execute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "DECK", result.MergedOutput)
	assert.Equal(t, "presenter", result.Metadata["join.agent_id"])
}

func TestParallelBuilder_WithJoinPolicy(t *testing.T) {
	joinLLM := newRecordingLLMProvider("REPORT")
	orch := newJoinTestOrchestrator(t, nil, true)
	orch.RegisterAgent("editor", createMockAgent(t, "editor", joinLLM))

	result, err := orch.Parallel().
		WithTask(createMockAgent(t, "a", newMockLLMProvider("alpha")), "do a").
		WithTask(createMockAgent(t, "b", newMockLLMProvider("beta")), "do b").
		WithJoinPolicy(&loomv1.JoinPolicy{AgentId: "editor"}).
		Execute(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "REPORT", result.MergedOutput)
	assert.Equal(t, "editor", result.Metadata["join.agent_id"])
}

// --- YAML ---

func TestLoadWorkflowFromYAML_ForkJoinJoinBlock(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: quarterly-deck
spec:
  type: fork-join
  prompt: "Analyze Q3"
  agent_ids: [finance, sales, ops]
  merge_strategy: concatenate
  join:
    agent_id: presenter
    prompt_template: "Build a 10-slide deck for {{prompt}}.\n\n{{branches}}"
    max_branch_bytes: 4096
    max_input_tokens: 60000
    overflow_strategy: pre_reduce
`
	pattern, err := LoadWorkflowFromYAML(createTempYAMLFile(t, yaml))
	require.NoError(t, err)
	fj := pattern.GetForkJoin()
	require.NotNil(t, fj)
	require.NotNil(t, fj.JoinPolicy)
	assert.Equal(t, "presenter", fj.JoinPolicy.AgentId)
	assert.Contains(t, fj.JoinPolicy.PromptTemplate, "{{branches}}")
	assert.Equal(t, int32(4096), fj.JoinPolicy.MaxBranchBytes)
	assert.Equal(t, int32(60000), fj.JoinPolicy.MaxInputTokens)
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE, fj.JoinPolicy.OverflowStrategy)
}

func TestLoadWorkflowFromYAML_ParallelJoinBlock(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: docs
spec:
  type: parallel
  tasks:
    - agent_id: writer
      prompt: "write"
    - agent_id: reviewer
      prompt: "review"
  merge_strategy: summary
  join:
    max_input_tokens: 20000
    overflow_strategy: truncate
`
	pattern, err := LoadWorkflowFromYAML(createTempYAMLFile(t, yaml))
	require.NoError(t, err)
	par := pattern.GetParallel()
	require.NotNil(t, par)
	require.NotNil(t, par.JoinPolicy)
	assert.Empty(t, par.JoinPolicy.AgentId)
	assert.Equal(t, int32(20000), par.JoinPolicy.MaxInputTokens)
	assert.Equal(t, loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE, par.JoinPolicy.OverflowStrategy)
}

func TestLoadWorkflowFromYAML_NoJoinBlockLeavesPolicyNil(t *testing.T) {
	yaml := `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: plain
spec:
  type: fork-join
  prompt: "p"
  agent_ids: [a]
`
	pattern, err := LoadWorkflowFromYAML(createTempYAMLFile(t, yaml))
	require.NoError(t, err)
	assert.Nil(t, pattern.GetForkJoin().JoinPolicy)
}

func TestParseJoinPolicy_Errors(t *testing.T) {
	base := func(join string) string {
		return `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: bad
spec:
  type: fork-join
  prompt: "p"
  agent_ids: [a]
  merge_strategy: %s
  join:
` + join
	}
	tests := []struct {
		name     string
		strategy string
		join     string
		wantErr  string
	}{
		{"negative max_branch_bytes", "summary", "    max_branch_bytes: -1\n", "max_branch_bytes must be >= 0"},
		{"negative max_input_tokens", "summary", "    max_input_tokens: -5\n", "max_input_tokens must be >= 0"},
		{"unknown overflow strategy", "summary", "    max_input_tokens: 10\n    overflow_strategy: explode\n", `unknown value "explode"`},
		{"overflow strategy that can never fire", "summary", "    overflow_strategy: fail\n", "can never fire"},
		{"unknown field", "summary", "    agent_id: j\n    max_tokens: 10\n", "max_tokens is not a join field"},
		{"empty block", "summary", "    {}\n", "sets nothing"},
		{"no agent on concatenate", "concatenate", "    max_input_tokens: 10\n", "has no effect on merge_strategy concatenate"},
		{"no agent on first", "first", "    max_input_tokens: 10\n", "has no effect on merge_strategy first"},
		{"not an object", "summary", "    - agent_id\n", "join must be an object"},
		{"fractional tokens", "summary", "    max_input_tokens: 10.5\n", "must be a whole number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := fmt.Sprintf(base(tt.join), tt.strategy)
			_, err := LoadWorkflowFromYAML(createTempYAMLFile(t, yaml))
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidWorkflow), err.Error())
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestParseJoinOverflowStrategy_Spellings(t *testing.T) {
	for raw, want := range map[string]loomv1.JoinOverflowStrategy{
		"pre_reduce": loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE,
		"pre-reduce": loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE,
		"reduce":     loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_PRE_REDUCE,
		"TRUNCATE":   loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_TRUNCATE,
		" fail ":     loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL,
	} {
		got, err := parseJoinOverflowStrategy(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
}
