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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/collaboration"
)

func TestOrchestrator_BoundHandoff_StoresCapsAndRenders(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	var seen []collaboration.HandoffPiece
	res, err := orch.BoundHandoff(context.Background(), &collaboration.HandoffRequest{
		WorkflowID: "debate-abc",
		Pattern:    "debate",
		Scope:      "round-2-response-b",
		Policy:     &loomv1.JoinPolicy{MaxBranchBytes: 2048},
		Entries: []collaboration.HandoffEntry{
			{Index: 1, AgentID: "a", Text: bigText("a", 30_000), Key: "debate-abc-round-2-position-a"},
			{Index: 3, AgentID: "c", Text: "short position", Key: ""},
		},
		Render: func(pieces []collaboration.HandoffPiece) string {
			seen = pieces
			var b strings.Builder
			for _, p := range pieces {
				b.WriteString(fmt.Sprintf("Agent %s:\n%s\n%s\n", p.AgentID, p.Inline, p.Notice))
			}
			return b.String()
		},
	})
	require.NoError(t, err)
	require.Len(t, seen, 2)

	assert.Equal(t, 1, seen[0].Index)
	assert.Equal(t, "a", seen[0].AgentID)
	assert.LessOrEqual(t, len(seen[0].Inline), 2048)
	assert.Contains(t, seen[0].Notice, "Branch output truncated")
	assert.Contains(t, seen[0].Notice, "debate-abc-round-2-position-a")
	assert.Equal(t, "debate-abc-round-2-position-a", seen[0].Key)
	assert.Equal(t, len(bigText("a", 30_000)), seen[0].FullSize, "FullSize is the stored text's size")

	assert.Equal(t, "short position", seen[1].Inline)
	assert.Empty(t, seen[1].Notice)
	assert.Empty(t, seen[1].Key, "an entry without Key is not stored")

	assert.Equal(t, 1, res.Stored)
	assert.Equal(t, 1, res.Truncated)
	assert.Equal(t, 0, res.Reduced)
	assert.Equal(t, "none", res.Overflow)
	assert.Equal(t, 0, res.Budget, "no receiver and no explicit budget: no check")
	assert.Contains(t, res.Prompt, "Agent a:")
	assert.Contains(t, workflowMemoryKeys(t, orch.sharedMemory), "debate-abc-round-2-position-a")
}

func TestOrchestrator_BoundHandoff_UnstoredEntriesAreStillCapped(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, false)
	res, err := orch.BoundHandoff(context.Background(), &collaboration.HandoffRequest{
		WorkflowID: "wf", Pattern: "debate", Scope: "s",
		Policy:  &loomv1.JoinPolicy{MaxBranchBytes: 1024},
		Entries: []collaboration.HandoffEntry{{Index: 1, AgentID: "a", Text: bigText("a", 10_000), Key: "k"}},
		Render: func(pieces []collaboration.HandoffPiece) string {
			return pieces[0].Inline + pieces[0].Notice
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Stored, "no SharedMemory store")
	assert.Equal(t, 1, res.Truncated, "debate hand-offs cut even unstored entries (the join rule)")
	assert.Contains(t, res.Prompt, "SharedMemory is not configured")
}

func TestOrchestrator_BoundHandoff_OverBudgetFailMapsToCollaborationSentinel(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	receiver := createMockAgent(t, "mod", newMockLLMProvider("x"))
	_, err := orch.BoundHandoff(context.Background(), &collaboration.HandoffRequest{
		WorkflowID: "wf", Pattern: "debate", Scope: "moderator-synthesis", Receiver: receiver,
		Policy:  &loomv1.JoinPolicy{MaxInputTokens: 100, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL},
		Entries: []collaboration.HandoffEntry{{Index: 1, AgentID: "a", Text: bigText("a", 10_000)}},
		Render:  func(pieces []collaboration.HandoffPiece) string { return pieces[0].Inline },
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, collaboration.ErrHandoffOverBudget), err.Error())
	assert.True(t, errors.Is(err, ErrJoinInputOverBudget), "the orchestration sentinel is kept in the chain")
}

func TestOrchestrator_BoundHandoff_RequiresPolicyAndRender(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, false)
	_, err := orch.BoundHandoff(context.Background(), nil)
	require.Error(t, err)
	_, err = orch.BoundHandoff(context.Background(), &collaboration.HandoffRequest{Policy: &loomv1.JoinPolicy{}})
	require.Error(t, err)
	_, err = orch.BoundHandoff(context.Background(), &collaboration.HandoffRequest{Render: func([]collaboration.HandoffPiece) string { return "" }})
	require.Error(t, err)
}

// debateAgents registers two chatty debaters and a moderator whose LLM
// records what it is asked. Debaters answer with unformatted text, so the
// whole reply is the position (the parser's fallback).
func debateAgents(t *testing.T, orch *Orchestrator) *funcLLMProvider {
	t.Helper()
	orch.RegisterAgent("advocate", createMockAgent(t, "advocate", newFuncLLMProvider(func(string) (string, error) {
		return bigText("advocate", 20_000), nil
	})))
	orch.RegisterAgent("skeptic", createMockAgent(t, "skeptic", newFuncLLMProvider(func(string) (string, error) {
		return bigText("skeptic", 20_000), nil
	})))
	moderator := newFuncLLMProvider(func(p string) (string, error) {
		switch {
		case strings.Contains(p, "# All Debate Rounds"), strings.Contains(p, "Decide on"):
			return "SYNTHESIS", nil
		case isReducePrompt(p):
			return "card", nil
		default:
			return "summary", nil
		}
	})
	orch.RegisterAgent("moderator", createMockAgent(t, "moderator", moderator))
	return moderator
}

func debatePattern(rounds int32, policy *loomv1.JoinPolicy) *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Debate{Debate: &loomv1.DebatePattern{
			Topic:            "Monolith or microservices?",
			AgentIds:         []string{"advocate", "skeptic"},
			Rounds:           rounds,
			ModeratorAgentId: "moderator",
			JoinPolicy:       policy,
		}},
	}
}

func synthesisPrompt(t *testing.T, moderator *funcLLMProvider) string {
	t.Helper()
	for _, p := range moderator.recorded() {
		if strings.Contains(p, "# All Debate Rounds") || strings.Contains(p, "Decide on") {
			return p
		}
	}
	t.Fatal("no synthesis prompt recorded")
	return ""
}

func TestDebate_JoinPolicy_EndToEnd(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	moderator := debateAgents(t, orch)

	result, err := orch.ExecutePattern(context.Background(), debatePattern(2, &loomv1.JoinPolicy{MaxBranchBytes: 1024}))
	require.NoError(t, err)
	assert.Equal(t, "SYNTHESIS", result.MergedOutput)

	assert.Equal(t, "moderator", result.Metadata["join.mode"])
	assert.Equal(t, "moderator", result.Metadata["join.agent_id"])
	assert.Equal(t, "4", result.Metadata["join.positions_stored"], "two rounds, two agents")
	assert.Equal(t, "4", result.Metadata["join.positions_truncated"])
	assert.Equal(t, "0", result.Metadata["join.positions_reduced"])
	assert.Equal(t, "none", result.Metadata["join.overflow"])
	assert.Equal(t, "2", result.Metadata["debate.response_prompts_bounded"], "round 2: one response prompt per debater")
	assert.Equal(t, "0", result.Metadata["debate.response_prompts_over_budget"])
	assert.Equal(t, "2", result.Metadata["debate.response_positions_truncated"], "each response prompt carried one other position")

	sp := synthesisPrompt(t, moderator)
	assert.Contains(t, sp, "## AVAILABLE CONTEXT (SharedMemory)")
	assert.Contains(t, sp, "## Round 1")
	assert.Contains(t, sp, "## Round 2")
	assert.Equal(t, 4, strings.Count(sp, "[Branch output truncated"))
	assert.Contains(t, sp, "Synthesize this debate into a clear consensus")
	assert.Less(t, len(sp), 4*1024+6000, "four capped positions plus header and instruction")

	keys := workflowMemoryKeys(t, orch.sharedMemory)
	for _, want := range []string{"round-1-position-advocate", "round-1-position-skeptic", "round-2-position-advocate", "round-2-position-skeptic"} {
		found := false
		for _, k := range keys {
			if strings.HasSuffix(k, want) {
				found = true
			}
		}
		assert.True(t, found, "missing stored position %s in %v", want, keys)
	}
}

func TestDebate_JoinPolicy_ResponsePromptIsBoundedAndOrdered(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	debateAgents(t, orch)
	third := newFuncLLMProvider(func(string) (string, error) { return bigText("third", 20_000), nil })
	orch.RegisterAgent("third", createMockAgent(t, "third", third))

	_, err := orch.ExecutePattern(context.Background(), &loomv1.WorkflowPattern{
		Pattern: &loomv1.WorkflowPattern_Debate{Debate: &loomv1.DebatePattern{
			Topic:      "t",
			AgentIds:   []string{"advocate", "third", "skeptic"},
			Rounds:     2,
			JoinPolicy: &loomv1.JoinPolicy{MaxBranchBytes: 1024},
		}},
	})
	require.NoError(t, err)

	var response string
	for _, p := range third.recorded() {
		if strings.Contains(p, "Other agents have presented these positions:") {
			response = p
		}
	}
	require.NotEmpty(t, response, "round 2 asked third to respond")
	assert.Contains(t, response, "## AVAILABLE CONTEXT (SharedMemory)")
	a := strings.Index(response, "Agent advocate:")
	s := strings.Index(response, "Agent skeptic:")
	require.True(t, a > 0 && s > 0)
	assert.Less(t, a, s, "other positions follow agent_ids order")
	assert.NotContains(t, response, "Agent third:", "an agent is not shown its own position as another's")
	assert.Equal(t, 2, strings.Count(response, "[Branch output truncated"))
}

func TestDebate_JoinPolicy_FailOverflowIsFatalOnTheResponsePrompt(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	moderator := debateAgents(t, orch)
	policy := &loomv1.JoinPolicy{MaxInputTokens: 100, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL}
	_, err := orch.ExecutePattern(context.Background(), debatePattern(2, policy))
	require.Error(t, err)
	assert.True(t, errors.Is(err, collaboration.ErrHandoffOverBudget), err.Error())
	assert.Contains(t, err.Error(), "round 2 response prompt for advocate")
	for _, p := range moderator.recorded() {
		assert.NotContains(t, p, "# All Debate Rounds", "the synthesis was never paid for")
	}
}

func TestDebate_JoinPolicy_FailOverflowIsFatalOnTheSynthesis(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	moderator := debateAgents(t, orch)
	policy := &loomv1.JoinPolicy{MaxInputTokens: 100, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL}
	_, err := orch.ExecutePattern(context.Background(), debatePattern(1, policy))
	require.Error(t, err)
	assert.True(t, errors.Is(err, collaboration.ErrHandoffOverBudget), err.Error())
	assert.Contains(t, err.Error(), "moderator synthesis failed")
	for _, p := range moderator.recorded() {
		assert.NotContains(t, p, "# All Debate Rounds")
	}
}

func TestDebate_JoinPolicy_PreReduceFitsTheSynthesisToTheBudget(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	moderator := debateAgents(t, orch)
	policy := &loomv1.JoinPolicy{MaxInputTokens: 900}
	result, err := orch.ExecutePattern(context.Background(), debatePattern(1, policy))
	require.NoError(t, err)

	sp := synthesisPrompt(t, moderator)
	assert.LessOrEqual(t, agentTokens(sp), 900)
	assert.Equal(t, "pre_reduce", result.Metadata["join.overflow"])
	assert.Equal(t, "2", result.Metadata["join.positions_reduced"])
	assert.Equal(t, 2, strings.Count(sp, "[Branch output reduced from"))
	reduces := 0
	for _, p := range moderator.recorded() {
		if isReducePrompt(p) {
			reduces++
		}
	}
	assert.Equal(t, 2, reduces, "the moderator's own LLM reduced both positions")
	assert.Greater(t, result.Cost.AgentCostsUsd["moderator"], 0.0)
}

func TestDebate_JoinPolicy_PromptTemplate(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	moderator := debateAgents(t, orch)
	policy := &loomv1.JoinPolicy{
		MaxBranchBytes: 512,
		PromptTemplate: "Decide on {{prompt}} from {{branch_count}} positions:\n{{branches}}\nAnswer in one line.",
	}
	_, err := orch.ExecutePattern(context.Background(), debatePattern(1, policy))
	require.NoError(t, err)
	sp := synthesisPrompt(t, moderator)
	assert.Contains(t, sp, "Decide on Monolith or microservices? from 2 positions:")
	assert.Contains(t, sp, "## Round 1")
	assert.Contains(t, sp, "Answer in one line.")
	assert.NotContains(t, sp, "Synthesize this debate", "the template replaces the default wording")
}

func TestDebate_WithoutJoinPolicy_IsUnchanged(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	moderator := debateAgents(t, orch)
	result, err := orch.ExecutePattern(context.Background(), debatePattern(2, nil))
	require.NoError(t, err)
	sp := synthesisPrompt(t, moderator)
	assert.NotContains(t, sp, "Branch output truncated")
	assert.NotContains(t, sp, "AVAILABLE CONTEXT")
	assert.Greater(t, len(sp), 4*20_000, "every position inline in full")
	_, ok := result.Metadata["join.mode"]
	assert.False(t, ok)
	_, ok = result.Metadata["debate.response_prompts_bounded"]
	assert.False(t, ok)
	for _, k := range workflowMemoryKeys(t, orch.sharedMemory) {
		assert.NotContains(t, k, "-position-", "nothing stored without a policy")
	}
}

func TestDebateBuilder_WithJoinPolicy(t *testing.T) {
	orch := newJoinTestOrchestrator(t, nil, true)
	a := createMockAgent(t, "a", newMockLLMProvider(bigText("a", 20_000)))
	b := createMockAgent(t, "b", newMockLLMProvider(bigText("b", 20_000)))
	_, err := orch.Debate("t").WithAgents(a, b).WithRounds(2).
		WithJoinPolicy(&loomv1.JoinPolicy{MaxInputTokens: 50, OverflowStrategy: loomv1.JoinOverflowStrategy_JOIN_OVERFLOW_STRATEGY_FAIL}).
		Execute(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, collaboration.ErrHandoffOverBudget), "the builder carried the policy to the engine")

	_, err = orch.Debate("t").WithAgents(a, b).WithRounds(1).
		WithJoinPolicy(&loomv1.JoinPolicy{AgentId: "someone"}).
		Execute(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not set agent_id")
}

func TestExtractAgentIDs_IncludesJoinAgents(t *testing.T) {
	fork := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{
		AgentIds:   []string{"a", "b"},
		JoinPolicy: &loomv1.JoinPolicy{AgentId: "presenter"},
	}}}
	assert.ElementsMatch(t, []string{"a", "b", "presenter"}, ExtractAgentIDs(fork))

	parallel := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Parallel{Parallel: &loomv1.ParallelPattern{
		Tasks:      []*loomv1.AgentTask{{AgentId: "x"}, {AgentId: "y"}},
		JoinPolicy: &loomv1.JoinPolicy{AgentId: "editor"},
	}}}
	assert.ElementsMatch(t, []string{"x", "y", "editor"}, ExtractAgentIDs(parallel))

	noPolicy := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_ForkJoin{ForkJoin: &loomv1.ForkJoinPattern{AgentIds: []string{"a"}}}}
	assert.Equal(t, []string{"a"}, ExtractAgentIDs(noPolicy))

	debate := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Debate{Debate: &loomv1.DebatePattern{
		AgentIds: []string{"d1", "d2"}, ModeratorAgentId: "mod", JoinPolicy: &loomv1.JoinPolicy{MaxBranchBytes: 1},
	}}}
	assert.ElementsMatch(t, []string{"d1", "d2", "mod"}, ExtractAgentIDs(debate))
}
