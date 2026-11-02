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

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// blockedThenTold is a seat that cannot do its work until it is told
// something. Its first turn says so without a signal, as a seat with no word
// for it does; its answer sent back, it takes the way out it was offered. Told
// what it needed, it does the work.
type blockedThenTold struct {
	mu      sync.Mutex
	prompts []string
}

func (m *blockedThenTold) Name() string  { return "blocked" }
func (m *blockedThenTold) Model() string { return "blocked" }
func (m *blockedThenTold) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	last := msgs[len(msgs)-1].Content
	m.prompts = append(m.prompts, last)
	say := func(text string) (*llmtypes.LLMResponse, error) {
		return &llmtypes.LLMResponse{Content: text, StopReason: "end_turn"}, nil
	}
	switch {
	case strings.Contains(last, "the code is in the unit"):
		return say("written\n" + `{"signal":"tests-ready"}`)
	case strings.Contains(last, "Your response format or state is invalid."):
		return say("the code is not here. This step is WRITE.\n" + `{"signal":"escalate"}`)
	}
	return say("I cannot write these tests: the code is not here.")
}

func (m *blockedThenTold) told() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// threeDeep is a machine that runs a machine that runs a machine whose one
// agent step is given a single success word by its spec.
func threeDeep() *loomv1.WorkflowPattern {
	inner := wrap(&loomv1.StateMachinePattern{EntryState: "WRITE", States: map[string]*loomv1.WorkflowState{
		"WRITE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "tester", Instructions: "write the tests"}},
			Vocab: []string{"tests-ready"}, Route: map[string]*loomv1.Transition{"tests-ready": to("DONE")}},
		"DONE": {Terminal: true},
	}})
	runs := func(child *loomv1.WorkflowPattern) *loomv1.WorkflowPattern {
		return wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
			"RUN":  {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}}, Route: map[string]*loomv1.Transition{"done": to("DONE")}},
			"DONE": {Terminal: true},
		}})
	}
	p := runs(runs(inner))
	p.WorkflowId = "deep"
	return p
}

// innermost follows a parked run down to the machine that actually stopped.
func innermost(ckpt *loomv1.WorkflowCheckpoint) *loomv1.WorkflowCheckpoint {
	for ckpt.GetNestedCheckpoint() != nil {
		ckpt = ckpt.GetNestedCheckpoint()
	}
	return ckpt
}

// A seat that cannot do its work can say so, however deep the machine it sits
// in and whatever words its spec gave it. The engine offers the way out with
// the state's own words and tells the step how to take it; the run stops at
// that step with what the seat said, instead of being answered with the
// nearest success and going round. Taken up again with what the seat needed,
// that step runs again and the run finishes.
func TestASeatAtAnyDepthCanSayItCannotDoTheWork(t *testing.T) {
	store := machineStore(t)
	model := &blockedThenTold{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("tester", machineAgent(t, "tester", store, model))
	p := threeDeep()

	_, err := orch.ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped, "the run did not stop")
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, stopped.Checkpoint.GetSuspendReason())
	word, said := stopOf(stopped.Checkpoint)
	require.Equal(t, EscalatedSignal, word, "the run does not say the work below cannot go on")
	require.Contains(t, said, "From: RUN › RUN › WRITE\n", "the run does not say which step could not go on")
	place, ok := orch.places.PlaceOf(context.Background(), "deep-RUN-RUN")
	require.True(t, ok)
	require.Equal(t, "WRITE", place.At(), "the step that could not go on does not stand where it stopped")
	require.Equal(t, EscalateSignal, place.StopWord)

	told := model.told()
	require.Len(t, told, 2, "the seat was sent round")
	require.Contains(t, told[0], `This step is WRITE. If you cannot do the work, say so, name this step, and end with {"signal":"escalate"}.`)
	require.Contains(t, told[0], `["escalate", "tests-ready"]`, "the way out was not among the words the step was offered")
	require.Contains(t, told[1], "The line: I cannot write these tests: the code is not here.\n"+
		"What is wrong with it: it is not a word this step can end with", "the seat's answer was not sent back to it")

	revise := &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "the code is in the unit"}
	_, err = orch.ResumeWorkflow(context.Background(), threeDeep(), stopped.Checkpoint, revise)
	require.NoError(t, err, "taken up again with what it needed, the run did not finish")
}

// A step is told the way out once, and a state that is not an agent's is
// offered nothing.
func TestTheWayOutIsSaidOnceAndOnlyToAgents(t *testing.T) {
	agent := &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "a"}},
		Vocab: []string{"done"}}
	require.Equal(t, []string{"done", "escalate"}, stateVocab(agent))
	told := withTheWayOut(agent, "do it")
	require.Contains(t, told, "This step is {loom_state}.")
	require.Equal(t, told, withTheWayOut(agent, told), "the way out was said twice")

	tool := &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "shell_execute"}},
		Vocab: []string{"done"}}
	require.Equal(t, []string{"done"}, stateVocab(tool))
}
