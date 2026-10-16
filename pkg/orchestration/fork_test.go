// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// hearingLLM answers from a script and keeps everything it was shown on each
// call — the whole conversation, not only the newest message — because what a
// fork is for is what the seat can see behind its first prompt.
type hearingLLM struct {
	mu      sync.Mutex
	replies []string
	heard   []string
}

func (h *hearingLLM) Chat(_ context.Context, messages []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var all []string
	for _, m := range messages {
		all = append(all, m.Role+": "+m.Content)
	}
	h.heard = append(h.heard, strings.Join(all, "\n"))
	reply := fmt.Sprintf("reply %d\ndone", len(h.heard))
	if len(h.heard) <= len(h.replies) {
		reply = h.replies[len(h.heard)-1]
	}
	return &llmtypes.LLMResponse{Content: reply, StopReason: "stop",
		Usage: llmtypes.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CostUSD: 0.5}}, nil
}
func (h *hearingLLM) Name() string  { return "hearing" }
func (h *hearingLLM) Model() string { return "hearing-model" }

// namesTheParent is the deterministic step that says which conversation the
// next seat starts from. It can, because the engine tells it which run it is in.
type namesTheParent struct{ absent bool }

func (n namesTheParent) Execute(_ context.Context, _ string, params map[string]interface{}) (*shuttle.Result, error) {
	parent := fmt.Sprintf("%v-designer", params[ParamRun])
	if n.absent {
		parent = "a-run-that-never-happened-designer"
	}
	return &shuttle.Result{Success: true, Data: fmt.Sprintf(
		`{"signal":"named","vars":{"fork:planner":%q}}`, parent)}, nil
}

func forkPattern(id string) *loomv1.WorkflowPattern {
	p := wrap(&loomv1.StateMachinePattern{
		EntryState:     "DESIGN",
		LoopbackStates: []string{"PLAN"},
		States: map[string]*loomv1.WorkflowState{
			"DESIGN": {
				Body:  agentState("designer", "design the greeter"),
				Route: map[string]*loomv1.Transition{"designed": to("HANDOVER")},
			},
			"HANDOVER": {
				Body:  actionState("name-parent"),
				Route: map[string]*loomv1.Transition{"named": to("PLAN")},
			},
			"PLAN": {
				Body: agentState("planner", "plan the work"),
				Route: map[string]*loomv1.Transition{
					"again":   to("PLAN"),
					"planned": to("DONE"),
				},
			},
			"DONE": {Terminal: true},
		},
	})
	p.WorkflowId = id
	return p
}

// A seat the run names a parent for starts with the parent's memory, once; the
// parent's conversation is left exactly as it was, and what the parent's turns
// cost is not billed again to the fork.
func TestASeatStartsFromTheConversationTheRunNamesForIt(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	designer := &hearingLLM{replies: []string{"DK-1: the greeter takes a name, because the ticket says so\ndesigned"}}
	planner := &hearingLLM{replies: []string{"first cut\nagain", "second cut\nplanned"}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: namesTheParent{}})
	orch.RegisterAgent("designer", machineAgent(t, "designer", store, designer))
	orch.RegisterAgent("planner", machineAgent(t, "planner", store, planner))

	_, err := orch.ExecutePattern(context.Background(), forkPattern("fork-1"))
	require.NoError(t, err)

	require.Len(t, planner.heard, 2)
	assert.Contains(t, planner.heard[0], "DK-1: the greeter takes a name",
		"the planner's first turn did not carry the designer's conversation")
	assert.Contains(t, planner.heard[0], "plan the work", "a forked seat's first turn is still its full brief")
	assert.Contains(t, planner.heard[1], "first cut", "its second turn returns to its own conversation")
	assert.Equal(t, 1, strings.Count(planner.heard[1], "DK-1: the greeter takes a name"),
		"the parent's history was copied a second time")

	ctx := context.Background()
	parent, err := store.LoadMessages(ctx, "fork-1-designer")
	require.NoError(t, err)
	for _, m := range parent {
		assert.NotContains(t, m.Content, "plan the work", "the fork wrote into its parent")
	}
	fork, err := store.LoadMessages(ctx, "fork-1-planner")
	require.NoError(t, err)
	require.Greater(t, len(fork), len(parent))
	var inherited float64
	for _, m := range fork[:len(parent)] {
		inherited += m.CostUSD
	}
	assert.Zero(t, inherited, "the parent's spend was billed to the fork as well")
}

// A parent that was named and never existed is a cold start: the seat gets its
// brief and nothing else, and the run carries on.
func TestASeatWhoseNamedParentIsAbsentStartsCold(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	designer := &hearingLLM{replies: []string{"DK-1: a decision\ndesigned"}}
	planner := &hearingLLM{replies: []string{"cold cut\nplanned"}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: namesTheParent{absent: true}})
	orch.RegisterAgent("designer", machineAgent(t, "designer", store, designer))
	orch.RegisterAgent("planner", machineAgent(t, "planner", store, planner))

	_, err := orch.ExecutePattern(context.Background(), forkPattern("fork-2"))
	require.NoError(t, err)
	require.Len(t, planner.heard, 1)
	assert.NotContains(t, planner.heard[0], "DK-1")
	assert.Contains(t, planner.heard[0], "plan the work")
}

// gateNamesTheParent is a gated deterministic step that ends on a signal object,
// as grooming's design gate does: the word that routes, and what the run should
// remember — here, which conversation the next seat continues.
type gateNamesTheParent struct{}

func (gateNamesTheParent) Execute(_ context.Context, _ string, params map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true, Data: fmt.Sprintf(
		"the design is written\n"+`{"signal":"ready","vars":{"fork:planner":"%v-designer"}}`, params[ParamRun])}, nil
}

// A gate stops the walk before anything is done with what its state said. When
// the gate is approved, all of it still has to happen: the vars are kept, the
// signal is read as a signal and not as a line of JSON, and the prose is handed
// on. A live grooming run crossed its design gate and died there — "emitted
// {"signal":"ready",…}, legal signals are [ready]" — and had it not, the seats
// after the gate would have started cold, their parent never recorded.
func TestWhatAGatedStateSaidSurvivesTheGate(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	designer := &hearingLLM{replies: []string{"DK-1: the greeter takes a name\ndesigned"}}
	planner := &hearingLLM{replies: []string{"the plan\nplanned"}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: gateNamesTheParent{}})
	orch.RegisterAgent("designer", machineAgent(t, "designer", store, designer))
	orch.RegisterAgent("planner", machineAgent(t, "planner", store, planner))

	pattern := forkPattern("gate-1")
	pattern.GetStateMachine().States["HANDOVER"] = &loomv1.WorkflowState{
		Body:     actionState("gate"),
		Route:    map[string]*loomv1.Transition{"ready": to("PLAN")},
		HitlGate: &loomv1.HITLGate{PromptTemplate: "Approve the design"},
	}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_GATE, suspended.Checkpoint.SuspendReason)

	result, err := orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])

	require.Len(t, planner.heard, 1)
	assert.Contains(t, planner.heard[0], "DK-1: the greeter takes a name",
		"the gated state's vars were lost, so the planner started cold")
	assert.Contains(t, planner.heard[0], "the design is written",
		"what the gated state said was not handed on")
}
