// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package collaboration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// scriptedLLM answers every prompt with content and records the prompts.
// A prompt containing failOn (when set) fails instead.
type scriptedLLM struct {
	mu      sync.Mutex
	content string
	failOn  string
	prompts []string
}

func (m *scriptedLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prompt := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			prompt = messages[i].Content
			m.prompts = append(m.prompts, prompt)
			break
		}
	}
	if m.failOn != "" && strings.Contains(prompt, m.failOn) {
		return nil, errors.New("scripted failure")
	}
	return &llmtypes.LLMResponse{Content: m.content, StopReason: "stop", Usage: llmtypes.Usage{TotalTokens: 30, CostUSD: 0.001}}, nil
}
func (m *scriptedLLM) Name() string  { return "scripted" }
func (m *scriptedLLM) Model() string { return "scripted-model" }
func (m *scriptedLLM) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// fakeBounder cuts every entry over cap bytes and records each request.
// With fail set it returns ErrHandoffOverBudget.
type fakeBounder struct {
	mu      sync.Mutex
	cap     int
	fail    bool
	failPut map[string]bool // keys whose store Put "failed": reported as not stored
	calls   int32
	reqs    []*HandoffRequest
}

func (f *fakeBounder) BoundHandoff(_ context.Context, req *HandoffRequest) (*HandoffResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.fail {
		return nil, fmt.Errorf("%w: fake", ErrHandoffOverBudget)
	}
	pieces := make([]HandoffPiece, len(req.Entries))
	res := &HandoffResult{Overflow: "none", LLMCalls: f.calls, Usage: types.Usage{TotalTokens: int(f.calls) * 10, CostUSD: float64(f.calls) * 0.01}}
	for i, e := range req.Entries {
		p := HandoffPiece{Index: e.Index, AgentID: e.AgentID, Inline: e.Text, Key: e.Key, FullSize: len(e.Text)}
		if e.Key != "" && !f.failPut[e.Key] {
			res.Stored++
			res.StoredKeys = append(res.StoredKeys, e.Key)
		}
		if f.cap > 0 && len(e.Text) > f.cap {
			p.Inline = e.Text[:f.cap]
			p.Notice = fmt.Sprintf("[cut to %d bytes; key=%s]", f.cap, e.Key)
			res.Truncated++
		}
		pieces[i] = p
	}
	res.Prompt = req.Render(pieces)
	return res, nil
}

func (f *fakeBounder) requests() []*HandoffRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*HandoffRequest(nil), f.reqs...)
}

// boundingProvider is an AgentProvider that also bounds hand-offs.
type boundingProvider struct {
	agents  map[string]*agent.Agent
	bounder *fakeBounder
}

func (p *boundingProvider) GetAgent(_ context.Context, id string) (*agent.Agent, error) {
	ag, ok := p.agents[id]
	if !ok {
		return nil, fmt.Errorf("no agent %s", id)
	}
	return ag, nil
}

func (p *boundingProvider) BoundHandoff(ctx context.Context, req *HandoffRequest) (*HandoffResult, error) {
	return p.bounder.BoundHandoff(ctx, req)
}

// plainProvider cannot bound hand-offs.
type plainProvider struct{ agents map[string]*agent.Agent }

func (p *plainProvider) GetAgent(_ context.Context, id string) (*agent.Agent, error) {
	ag, ok := p.agents[id]
	if !ok {
		return nil, fmt.Errorf("no agent %s", id)
	}
	return ag, nil
}

func scriptedAgent(name, reply string) (*agent.Agent, *scriptedLLM) {
	llm := &scriptedLLM{content: reply}
	return agent.NewAgent(nil, llm, agent.WithName(name)), llm
}

func position(agentID, text string) *loomv1.AgentPosition {
	return &loomv1.AgentPosition{AgentId: agentID, Position: text, Arguments: []string{"arg one", "arg two"}, Confidence: 0.7}
}

func TestDebate_HandoffPolicy_RequiresBounder(t *testing.T) {
	policy := &loomv1.JoinPolicy{MaxBranchBytes: 10}
	plain := NewDebateOrchestrator(&plainProvider{})
	assert.Nil(t, plain.handoffPolicy(&loomv1.DebatePattern{JoinPolicy: policy}), "no bounder: the policy is inert")

	bounding := NewDebateOrchestrator(&boundingProvider{bounder: &fakeBounder{}})
	assert.Same(t, policy, bounding.handoffPolicy(&loomv1.DebatePattern{JoinPolicy: policy}))
	assert.Nil(t, bounding.handoffPolicy(&loomv1.DebatePattern{}), "no policy: nothing to apply")
}

func TestDebate_GetAgentResponses_BoundedAndInConfiguredOrder(t *testing.T) {
	b, bLLM := scriptedAgent("b", "I concede nothing.")
	bounder := &fakeBounder{cap: 60, calls: 1}
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"b": b}, bounder: bounder})
	config := &loomv1.DebatePattern{Topic: "topic", AgentIds: []string{"a", "b", "c"}, JoinPolicy: &loomv1.JoinPolicy{MaxBranchBytes: 20}}
	positions := map[string]*loomv1.AgentPosition{
		"a": position("a", strings.Repeat("alpha ", 30)),
		"b": position("b", "mine"),
		"c": position("c", "short"),
	}
	cost := &loomv1.WorkflowCost{}

	responses, fit, err := d.getAgentResponses(context.Background(), "wf", "b", config, positions, 2, cost)
	require.NoError(t, err)
	assert.Equal(t, "I concede nothing.", responses["all"])
	require.NotNil(t, fit)
	assert.Equal(t, 1, fit.Truncated)

	reqs := bounder.requests()
	require.Len(t, reqs, 1)
	req := reqs[0]
	assert.Equal(t, "wf", req.WorkflowID)
	assert.Equal(t, "debate", req.Pattern)
	assert.Equal(t, "round-2-response-b", req.Scope)
	assert.Same(t, b, req.Receiver)
	assert.Equal(t, "topic", req.Task)
	require.Len(t, req.Entries, 2, "the responder's own position is not an entry")
	assert.Equal(t, "a", req.Entries[0].AgentID)
	assert.Equal(t, 1, req.Entries[0].Index)
	assert.Equal(t, "wf-round-2-position-a", req.Entries[0].Key)
	assert.Equal(t, "c", req.Entries[1].AgentID)
	assert.Equal(t, 3, req.Entries[1].Index, "index is the agent's place in agent_ids")
	assert.Contains(t, req.Entries[0].Text, "Position: alpha")
	assert.Contains(t, req.Entries[0].Text, "Arguments:\narg one\narg two")

	prompts := bLLM.recorded()
	require.Len(t, prompts, 1)
	p := prompts[0]
	assert.Contains(t, p, "## AVAILABLE CONTEXT (SharedMemory)", "stored positions are announced")
	assert.Contains(t, p, "`wf-round-2-position-a`")
	assert.Contains(t, p, "You previously took this position:\nmine")
	assert.Less(t, strings.Index(p, "Agent a:"), strings.Index(p, "Agent c:"))
	assert.Contains(t, p, "[cut to 60 bytes; key=wf-round-2-position-a]")
	assert.NotContains(t, p, "Agent b:")

	assert.Equal(t, int32(2), cost.LlmCalls, "one reduce call plus the response turn")
	assert.InDelta(t, 0.011, cost.AgentCostsUsd["b"], 1e-9)
}

func TestDebate_GetAgentResponses_WithoutPolicyIsUnboundedButOrdered(t *testing.T) {
	b, bLLM := scriptedAgent("b", "reply")
	bounder := &fakeBounder{cap: 5}
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"b": b}, bounder: bounder})
	config := &loomv1.DebatePattern{Topic: "topic", AgentIds: []string{"c", "b", "a"}}
	positions := map[string]*loomv1.AgentPosition{
		"a": position("a", strings.Repeat("alpha ", 30)),
		"b": position("b", "mine"),
		"c": position("c", "charlie"),
	}
	_, fit, err := d.getAgentResponses(context.Background(), "wf", "b", config, positions, 2, &loomv1.WorkflowCost{})
	require.NoError(t, err)
	assert.Nil(t, fit)
	assert.Empty(t, bounder.requests(), "no policy: the bounder is not consulted")
	p := bLLM.recorded()[0]
	assert.NotContains(t, p, "AVAILABLE CONTEXT")
	assert.Less(t, strings.Index(p, "Agent c:"), strings.Index(p, "Agent a:"), "agent_ids order, not map order")
	assert.Contains(t, p, strings.Repeat("alpha ", 30), "full position inline")
}

func TestDebate_GetAgentResponses_NoOthersMeansNoCall(t *testing.T) {
	b, bLLM := scriptedAgent("b", "reply")
	d := NewDebateOrchestrator(&plainProvider{agents: map[string]*agent.Agent{"b": b}})
	config := &loomv1.DebatePattern{AgentIds: []string{"b"}}
	responses, fit, err := d.getAgentResponses(context.Background(), "wf", "b", config, map[string]*loomv1.AgentPosition{"b": position("b", "mine")}, 2, &loomv1.WorkflowCost{})
	require.NoError(t, err)
	assert.Empty(t, responses)
	assert.Nil(t, fit)
	assert.Empty(t, bLLM.recorded())
}

func TestDebate_SynthesizeWithModerator_BoundedRoundsAndTemplate(t *testing.T) {
	mod, modLLM := scriptedAgent("mod", "SYNTHESIS")
	bounder := &fakeBounder{cap: 60, calls: 2}
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"mod": mod}, bounder: bounder})
	config := &loomv1.DebatePattern{
		Topic:            "topic",
		AgentIds:         []string{"a", "b"},
		ModeratorAgentId: "mod",
		JoinPolicy:       &loomv1.JoinPolicy{PromptTemplate: "Decide on {{prompt}} from {{branch_count}} positions:\n{{branches}}\nOne line."},
	}
	result := &loomv1.DebateResult{Rounds: []*loomv1.DebateRound{
		{RoundNumber: 1, Positions: []*loomv1.AgentPosition{position("a", strings.Repeat("a1 ", 40)), position("b", "b1")}},
		{RoundNumber: 2, Positions: []*loomv1.AgentPosition{position("a", "a2"), position("b", strings.Repeat("b2 ", 40))}},
	}}
	cost := &loomv1.WorkflowCost{}

	out, fit, err := d.synthesizeWithModerator(context.Background(), "wf", config, result, cost)
	require.NoError(t, err)
	assert.Equal(t, "SYNTHESIS", out)
	require.NotNil(t, fit)
	assert.Equal(t, 4, fit.Stored)
	assert.Equal(t, 2, fit.Truncated)

	req := bounder.requests()[0]
	assert.Equal(t, "moderator-synthesis", req.Scope)
	assert.Same(t, mod, req.Receiver)
	require.Len(t, req.Entries, 4)
	assert.Equal(t, []string{"wf-round-1-position-a", "wf-round-1-position-b", "wf-round-2-position-a", "wf-round-2-position-b"},
		[]string{req.Entries[0].Key, req.Entries[1].Key, req.Entries[2].Key, req.Entries[3].Key})
	assert.Contains(t, req.Entries[0].Text, "Arguments: arg one; arg two")

	p := modLLM.recorded()[0]
	assert.Contains(t, p, "## AVAILABLE CONTEXT (SharedMemory)")
	assert.Contains(t, p, "Decide on topic from 4 positions:")
	assert.Contains(t, p, "## Round 1\n**a** (70% confidence):")
	assert.Contains(t, p, "## Round 2\n")
	assert.Contains(t, p, "[cut to 60 bytes; key=wf-round-1-position-a]")
	assert.Contains(t, p, "One line.")
	assert.NotContains(t, p, "Synthesize this debate")
	assert.Equal(t, int32(3), cost.LlmCalls, "two reduce calls plus the synthesis turn")
	assert.InDelta(t, 0.021, cost.AgentCostsUsd["mod"], 1e-9)
}

func TestDebate_SynthesizeWithModerator_DefaultWordingWithoutPolicy(t *testing.T) {
	mod, modLLM := scriptedAgent("mod", "S")
	d := NewDebateOrchestrator(&plainProvider{agents: map[string]*agent.Agent{"mod": mod}})
	config := &loomv1.DebatePattern{Topic: "topic", ModeratorAgentId: "mod"}
	result := &loomv1.DebateResult{Rounds: []*loomv1.DebateRound{{RoundNumber: 1, Positions: []*loomv1.AgentPosition{position("a", "pa"), position("b", "pb")}}}}
	_, fit, err := d.synthesizeWithModerator(context.Background(), "wf", config, result, &loomv1.WorkflowCost{})
	require.NoError(t, err)
	assert.Nil(t, fit)
	p := modLLM.recorded()[0]
	assert.Contains(t, p, "# Debate Topic\ntopic\n\n# All Debate Rounds\n\n## Round 1\n")
	assert.Contains(t, p, "**a** (70% confidence):\npa\n\nArguments: arg one; arg two\n")
	assert.Contains(t, p, debateSynthesisInstruction)
}

func TestDebate_ExecuteRound_OverBudgetIsFatal(t *testing.T) {
	a, _ := scriptedAgent("a", "pos a")
	b, _ := scriptedAgent("b", "pos b")
	bounder := &fakeBounder{fail: true}
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"a": a, "b": b}, bounder: bounder})
	config := &loomv1.DebatePattern{Topic: "t", AgentIds: []string{"a", "b"}, JoinPolicy: &loomv1.JoinPolicy{MaxInputTokens: 1}}

	_, _, err := d.executeRound(context.Background(), "wf", 2, config, nil, a, &loomv1.WorkflowCost{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHandoffOverBudget))
	assert.Contains(t, err.Error(), "round 2 response prompt for a")

	round, stats, err := d.executeRound(context.Background(), "wf", 1, config, nil, a, &loomv1.WorkflowCost{})
	require.NoError(t, err, "round 1 has no response pass")
	assert.Len(t, round.Positions, 2)
	assert.Zero(t, stats.bounded)
}

// A response turn that fails for an ordinary reason (the provider errors on
// the round 2 "respond to the others" prompt) is not fatal: the position
// stands, the round completes, and only that agent's responses are missing.
func TestDebate_ExecuteRound_ChatFailureStaysNonFatal(t *testing.T) {
	aLLM := &scriptedLLM{content: "pos a", failOn: "Other agents have presented these positions"}
	a := agent.NewAgent(nil, aLLM, agent.WithName("a"))
	b, bLLM := scriptedAgent("b", "pos b")
	d := NewDebateOrchestrator(&plainProvider{agents: map[string]*agent.Agent{"a": a, "b": b}})
	config := &loomv1.DebatePattern{Topic: "t", AgentIds: []string{"a", "b"}}

	round, stats, err := d.executeRound(context.Background(), "wf", 2, config, nil, a, &loomv1.WorkflowCost{})
	require.NoError(t, err, "an ordinary response failure is not fatal")
	require.Len(t, round.Positions, 2)
	assert.Empty(t, round.Positions[0].Responses, "a's response turn failed, so it has none")
	assert.Equal(t, "pos b", round.Positions[1].Responses["all"], "b responded")
	assert.Zero(t, stats.bounded, "no policy: nothing bounded")
	assert.Len(t, bLLM.recorded(), 2, "b: position then response")
	aPrompts := aLLM.recorded()
	require.GreaterOrEqual(t, len(aPrompts), 2, "a: position, then the failed response attempt (the agent retries a failed call)")
	assert.Contains(t, aPrompts[len(aPrompts)-1], "Other agents have presented these positions", "the failures were the response turn, not the position")
	assert.Contains(t, aPrompts[0], "POSITION:", "the position turn came first and succeeded")

	// A missing debater still fails the position pass.
	ghost := &loomv1.DebatePattern{Topic: "t", AgentIds: []string{"a", "ghost"}}
	r, _, err := d.executeRound(context.Background(), "wf", 1, ghost, nil, a, &loomv1.WorkflowCost{})
	require.Error(t, err)
	assert.Nil(t, r)
}

func TestDebate_Execute_RejectsJoinPolicyAgentID(t *testing.T) {
	a, _ := scriptedAgent("a", "pos a")
	b, _ := scriptedAgent("b", "pos b")
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"a": a, "b": b}, bounder: &fakeBounder{}})
	_, err := d.Execute(context.Background(), &loomv1.DebatePattern{
		Topic: "t", AgentIds: []string{"a", "b"}, Rounds: 1,
		JoinPolicy: &loomv1.JoinPolicy{AgentId: "someone"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `join_policy.agent_id "someone" is not used`)
	assert.Contains(t, err.Error(), "moderator_agent_id is the join agent")
	assert.Empty(t, (&fakeBounder{}).requests())
}

// A position carried into several prompts of one run is stored once: later
// hand-offs mark it Stored so the bounder references the key without
// writing again, and the bookkeeping is dropped when the run ends.
func TestDebate_PositionsAreStoredOncePerRun(t *testing.T) {
	a, _ := scriptedAgent("a", "pos a")
	b, _ := scriptedAgent("b", "pos b")
	c, _ := scriptedAgent("c", "pos c")
	bounder := &fakeBounder{}
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"a": a, "b": b, "c": c}, bounder: bounder})
	config := &loomv1.DebatePattern{Topic: "t", AgentIds: []string{"a", "b", "c"}, JoinPolicy: &loomv1.JoinPolicy{MaxBranchBytes: 1}}

	_, _, err := d.executeRound(context.Background(), "wf", 2, config, nil, a, &loomv1.WorkflowCost{})
	require.NoError(t, err)
	reqs := bounder.requests()
	require.Len(t, reqs, 3, "one response prompt per debater")
	// a's prompt carries b and c (first time: not yet stored); b's prompt
	// carries a (new) and c (stored by a's hand-off); c's carries a and b
	// (both stored).
	stored := func(req *HandoffRequest) []bool {
		out := make([]bool, len(req.Entries))
		for i, e := range req.Entries {
			out[i] = e.Stored
		}
		return out
	}
	assert.Equal(t, []bool{false, false}, stored(reqs[0]))
	assert.Equal(t, []bool{false, true}, stored(reqs[1]))
	assert.Equal(t, []bool{true, true}, stored(reqs[2]))
	assert.True(t, d.positionStored("wf", positionKey("wf", 2, "a")))

	d.forgetStoredPositions("wf")
	assert.False(t, d.positionStored("wf", positionKey("wf", 2, "a")), "bookkeeping is per run")
}

func TestHandoffResult_Metadata(t *testing.T) {
	r := &HandoffResult{InputTokens: 12, Budget: 100, Overflow: "truncate", Stored: 3, Truncated: 2, Reduced: 1}
	assert.Equal(t, map[string]string{
		"join.input_tokens":        "12",
		"join.budget_tokens":       "100",
		"join.overflow":            "truncate",
		"join.positions_stored":    "3",
		"join.positions_truncated": "2",
		"join.positions_reduced":   "1",
	}, r.Metadata("join", "positions"))
}

func TestSpliceTemplate(t *testing.T) {
	got := SpliceTemplate("On {{prompt}} ({{branch_count}}):\n{{branches}}", "topic", "section with {{prompt}} inside", 2)
	assert.Equal(t, "On topic (2):\nsection with {{prompt}} inside", got, "{{branches}} is spliced last so section text is never re-scanned")
	assert.Equal(t, "no placeholder\n\nS", SpliceTemplate("no placeholder", "t", "S", 1), "sections are appended when the template has no {{branches}}")
}

func TestSharedMemoryHeader(t *testing.T) {
	assert.Empty(t, SharedMemoryHeader([]HandoffPiece{{Index: 1, AgentID: "a", Inline: "x"}}, "positions", "wf"), "nothing stored, no header")
	h := SharedMemoryHeader([]HandoffPiece{
		{Index: 1, AgentID: "a", Inline: "x", Key: "wf-round-1-position-a", FullSize: 9000},
		{Index: 2, AgentID: "b", Inline: "y"},
	}, "positions", "wf")
	assert.True(t, strings.HasPrefix(h, "## AVAILABLE CONTEXT (SharedMemory)\n"))
	assert.Contains(t, h, "Full text of 1 of 2 positions is stored")
	assert.Contains(t, h, "- `wf-round-1-position-a`: agent a, 9000 bytes")
	assert.Contains(t, h, "should start with `wf-`")
	assert.True(t, strings.HasSuffix(h, "\n---\n\n"))
}

func TestPairProgrammingAndTeacherStudent_NotImplemented(t *testing.T) {
	provider := &plainProvider{}
	_, err := NewPairProgrammingOrchestrator(provider).Execute(context.Background(), &loomv1.PairProgrammingPattern{Task: "write tests"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPatternNotImplemented))
	assert.Contains(t, err.Error(), "pair_programming")
	assert.Contains(t, err.Error(), "write tests")

	_, err = NewTeacherStudentOrchestrator(provider).Execute(context.Background(), &loomv1.TeacherStudentPattern{Objective: "learn SQL"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPatternNotImplemented))
	assert.Contains(t, err.Error(), "teacher_student")

	engine := NewEngine(provider)
	_, err = engine.Execute(context.Background(), &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_PairProgramming{PairProgramming: &loomv1.PairProgrammingPattern{}}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPatternNotImplemented), "the engine wraps with %%w")
	_, err = engine.Execute(context.Background(), &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_TeacherStudent{TeacherStudent: &loomv1.TeacherStudentPattern{}}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPatternNotImplemented))
}

// A configured moderator that does not exist fails the debate before any
// round is paid for, instead of after every round at the synthesis.
func TestDebate_Execute_MissingModeratorFailsBeforeAnyRound(t *testing.T) {
	a, llmA := scriptedAgent("a", "pos a")
	b, llmB := scriptedAgent("b", "pos b")
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"a": a, "b": b}, bounder: &fakeBounder{}})
	_, err := d.Execute(context.Background(), &loomv1.DebatePattern{
		Topic: "t", AgentIds: []string{"a", "b"}, Rounds: 2, ModeratorAgentId: "moderator",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `moderator_agent_id "moderator"`)
	assert.Contains(t, err.Error(), "no agent moderator")
	assert.Empty(t, llmA.recorded(), "no debater was asked for a position")
	assert.Empty(t, llmB.recorded())
}

// A position whose store Put failed is not marked stored: the next prompt
// that carries it asks the bounder to store it again instead of pointing at
// a key that is not in the store.
func TestDebate_FailedPutIsNotMarkedStored(t *testing.T) {
	a, _ := scriptedAgent("a", "pos a")
	b, _ := scriptedAgent("b", "pos b")
	c, _ := scriptedAgent("c", "pos c")
	bounder := &fakeBounder{failPut: map[string]bool{"wf-round-2-position-c": true}}
	d := NewDebateOrchestrator(&boundingProvider{agents: map[string]*agent.Agent{"a": a, "b": b, "c": c}, bounder: bounder})
	config := &loomv1.DebatePattern{Topic: "t", AgentIds: []string{"a", "b", "c"}, JoinPolicy: &loomv1.JoinPolicy{MaxBranchBytes: 1}}

	_, _, err := d.executeRound(context.Background(), "wf", 2, config, nil, a, &loomv1.WorkflowCost{})
	require.NoError(t, err)
	reqs := bounder.requests()
	require.Len(t, reqs, 3)
	storedIn := func(req *HandoffRequest) map[string]bool {
		m := map[string]bool{}
		for _, e := range req.Entries {
			m[e.Key] = e.Stored
		}
		return m
	}
	// a's hand-off carried b and c; b's Put succeeded, c's failed. b's prompt
	// carries a (new) and c: c is offered again, not referenced.
	second := storedIn(reqs[1])
	assert.False(t, second["wf-round-2-position-a"], "a appears for the first time")
	assert.False(t, second["wf-round-2-position-c"], "c's Put failed in the first hand-off: not marked stored")
	// c's prompt carries a (stored by b's hand-off) and b (stored by a's).
	third := storedIn(reqs[2])
	assert.True(t, third["wf-round-2-position-a"])
	assert.True(t, third["wf-round-2-position-b"])
	assert.False(t, d.positionStored("wf", "wf-round-2-position-c"), "still not stored: every Put of c failed")
	assert.True(t, d.positionStored("wf", "wf-round-2-position-b"))
}
