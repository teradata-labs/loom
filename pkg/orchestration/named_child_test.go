// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// shelf is where a host keeps its workflows by name. What a name means can
// change between one asking and the next, and every asking is kept.
type shelf struct {
	mu     sync.Mutex
	byName map[string]*loomv1.WorkflowPattern
	asked  []string
}

func (s *shelf) find(_ context.Context, name string) (*loomv1.WorkflowPattern, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, name)
	found, ok := s.byName[name]
	if !ok {
		return nil, fmt.Errorf("no workflow %q on the shelf", name)
	}
	return found, nil
}

func (s *shelf) put(name string, p *loomv1.WorkflowPattern) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byName == nil {
		s.byName = map[string]*loomv1.WorkflowPattern{}
	}
	s.byName[name] = p
}

func (s *shelf) askedFor() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

// named is a delegating body that gives its child by name.
func named(name string) *loomv1.WorkflowState_Delegate {
	return &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{WorkflowName: name}}
}

// oneStep is a machine of a single agent step that ends when the step says ok.
func oneStep(step string) *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: step, States: map[string]*loomv1.WorkflowState{
		step:   {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "do it"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("DONE")}},
		"DONE": {Terminal: true},
	}})
}

func orchestratorOn(t *testing.T, model *answers, kept *shelf) *Orchestrator {
	t.Helper()
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), WorkflowResolver: kept.find})
	orch.RegisterAgent("worker", machineAgent(t, "worker", machineStore(t), model))
	return orch
}

// A state that delegates by name runs the workflow of that name as it is
// defined when the state is entered: nothing is asked for before the walk
// reaches the state, and a state entered again after the definition changed
// runs the new one.
func TestANamedChildIsTheOneDefinedWhenItsStateIsEntered(t *testing.T) {
	kept := &shelf{}
	kept.put("child", oneStep("OLD"))
	var askedBeforeRun []string
	model := &answers{say: func(step string, turn int, _ string) string {
		switch step {
		case "FIRST":
			askedBeforeRun = kept.askedFor()
			return "started\n" + `{"signal":"ok"}`
		case "CHECK":
			if turn == 0 {
				kept.put("child", oneStep("NEW"))
				return "once more\n" + `{"signal":"again"}`
			}
		}
		return "fine\n" + `{"signal":"ok"}`
	}}
	orch := orchestratorOn(t, model, kept)
	p := wrap(&loomv1.StateMachinePattern{EntryState: "FIRST", States: map[string]*loomv1.WorkflowState{
		"FIRST": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "start"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("RUN")}},
		"RUN":   {Body: named("child"), Route: map[string]*loomv1.Transition{"done": to("CHECK")}},
		"CHECK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "check"}}, Vocab: []string{"ok", "again"},
			Route: map[string]*loomv1.Transition{"ok": to("DONE"), "again": to("RUN")}},
		"DONE": {Terminal: true},
	}})
	p.WorkflowId = "by-name"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	require.Empty(t, askedBeforeRun, "the child was read before the walk reached the state that runs it")
	require.Equal(t, []string{"child", "child"}, kept.askedFor(), "the child was not read each time its state was entered")
	require.Len(t, model.told("OLD"), 1)
	require.Len(t, model.told("NEW"), 1, "the state entered again did not run the child as it is now defined")
}

// A managed machine reads its named child no sooner: taking the machine up
// asks for nothing, and how the child can end is read off the child as it is
// when its state runs it — so a child that is only there by then, and ends
// not done, still reaches the manager.
func TestAManagedMachineReadsItsNamedChildWhenItsStateRuns(t *testing.T) {
	kept := &shelf{}
	ends := oneStep("TRY")
	ends.GetStateMachine().States["TRY"].Vocab = []string{"ok", "stuck"}
	ends.GetStateMachine().States["TRY"].Route["stuck"] = to("BLOCKED")
	ends.GetStateMachine().States["BLOCKED"] = &loomv1.WorkflowState{Terminal: true}
	var askedBeforeRun []string
	model := &answers{say: func(step string, turn int, _ string) string {
		switch {
		case step == "FIRST":
			askedBeforeRun = kept.askedFor()
			kept.put("late", ends)
		case step == "TRY" && turn == 0:
			return "cannot\n" + `{"signal":"stuck"}`
		case step == "MANAGE":
			return "try the other way\n" + `{"signal":"phase"}`
		}
		return "fine\n" + `{"signal":"ok"}`
	}}
	orch := orchestratorOn(t, model, kept)
	p := wrap(&loomv1.StateMachinePattern{EntryState: "FIRST", LogicSeat: "MANAGE", States: map[string]*loomv1.WorkflowState{
		"FIRST":  {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "start"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("PHASE")}},
		"PHASE":  {Body: named("late"), Route: map[string]*loomv1.Transition{"done": to("FIN")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}},
		"FIN":    {Terminal: true},
	}})
	p.WorkflowId = "late-child"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the end of a child that was only there when its state ran did not reach the manager")
	require.Empty(t, askedBeforeRun, "taking the managed machine up read its named child")
	require.Len(t, model.told("MANAGE"), 1)
	require.Contains(t, model.told("MANAGE")[0], "message in flight:\nBLOCKED\n\ncannot")
}

// A workflow run under its own name that delegates to that name is refused at
// once: the chain closes on the first step, before anything is read.
func TestAWorkflowRunUnderItsNameThatDelegatesToItselfIsRefusedAtOnce(t *testing.T) {
	kept := &shelf{}
	self := wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
		"RUN":  {Body: named("self"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE": {Terminal: true},
	}})
	self.WorkflowId = "self-run"
	kept.put("self", self)
	orch := orchestratorOn(t, &answers{say: func(string, int, string) string { return "?" }}, kept)

	_, err := orch.ExecutePattern(WithWorkflowName(context.Background(), "self"), self)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	require.Nil(t, stopped.Checkpoint.GetNestedCheckpoint(), "the workflow ran a level inside itself")
	require.Contains(t, stopped.Checkpoint.GetError(), "delegates to itself through self -> self")
	require.Empty(t, kept.askedFor())
}

// A spec gives a child as a name or writes it in; both are read.
func TestADelegatedWorkflowIsReadByNameOrWrittenIn(t *testing.T) {
	parse := func(workflow string) (*loomv1.DelegateState, error) {
		p, err := LoadWorkflowFromYAMLBytes([]byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: outer
spec:
  type: state-machine
  entry_state: A
  states:
    A:
      delegate:
        workflow: ` + workflow + `
      route:
        done: Z
    Z:
      terminal: true
`))
		return p.GetStateMachine().GetStates()["A"].GetDelegate(), err
	}
	byName, err := parse("task")
	require.NoError(t, err)
	require.Equal(t, "task", byName.GetWorkflowName())
	require.Nil(t, byName.GetWorkflow())

	written, err := parse("{type: state-machine, entry_state: B, states: {B: {terminal: true}}}")
	require.NoError(t, err)
	require.Empty(t, written.GetWorkflowName())
	require.Equal(t, "B", written.GetWorkflow().GetStateMachine().GetEntryState())

	for _, nothing := range []string{`""`, "7"} {
		_, err = parse(nothing)
		require.ErrorContains(t, err, "delegates but names no workflow", nothing)
	}
}

// A named child that cannot be found stops the run at the state that
// delegates to it, saying which; nothing before that state is lost, and once
// the child can be found, resuming the run enters the state and runs it.
func TestANamedChildThatCannotBeFoundStopsTheRunAtItsState(t *testing.T) {
	for name, withShelf := range map[string]bool{"the host does not have it": true, "the host finds nothing by name": false} {
		kept := &shelf{}
		model := &answers{say: func(string, int, string) string { return "fine\n" + `{"signal":"ok"}` }}
		orch := orchestratorOn(t, model, kept)
		if !withShelf {
			orch.SetWorkflowResolver(nil)
		}
		spec := func() *loomv1.WorkflowPattern {
			p := wrap(&loomv1.StateMachinePattern{EntryState: "FIRST", States: map[string]*loomv1.WorkflowState{
				"FIRST": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "start"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("RUN")}},
				"RUN":   {Body: named("child"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
				"DONE":  {Terminal: true},
			}})
			p.WorkflowId = "missing"
			return p
		}

		_, err := orch.ExecutePattern(context.Background(), spec())
		var stopped *WorkflowSuspended
		require.ErrorAs(t, err, &stopped, name)
		require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, stopped.Checkpoint.GetSuspendReason(), name)
		require.Equal(t, []string{"RUN"}, stopped.Checkpoint.GetStatePath(), name)
		require.Contains(t, stopped.Checkpoint.GetError(), `state "RUN" delegates to "child"`, name)

		kept.put("child", oneStep("LATE"))
		orch.SetWorkflowResolver(kept.find)
		_, err = orch.ResumeWorkflow(context.Background(), spec(), stopped.Checkpoint, &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
		require.NoError(t, err, name)
		require.Len(t, model.told("FIRST"), 1, name)
		require.Len(t, model.told("LATE"), 1, name)
	}
}

// A workflow that reaches itself again through the workflows it delegates to
// would never end. It is refused where the chain closes, with the chain named.
func TestAWorkflowThatDelegatesToItselfIsRefusedWhereTheChainCloses(t *testing.T) {
	kept := &shelf{}
	runs := func(child string) *loomv1.WorkflowPattern {
		return wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
			"RUN":  {Body: named(child), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
			"DONE": {Terminal: true},
		}})
	}
	kept.put("a", runs("b"))
	kept.put("b", runs("a"))
	orch := orchestratorOn(t, &answers{say: func(string, int, string) string { return "?" }}, kept)
	p := runs("a")
	p.WorkflowId = "round"

	_, err := orch.ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	at := innermost(stopped.Checkpoint)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, at.GetSuspendReason())
	require.Contains(t, at.GetError(), `delegates to "a", which delegates to itself through a -> b -> a`)
	require.Equal(t, []string{"a", "b"}, kept.askedFor(), "the chain was followed past where it closes")
}

// A managed machine's named child is no different from one written in: how it
// can end is read when the machine is taken up, an end that is not done goes
// to the manager, and a step deep inside named children that escalates
// reaches the manager and is sent back to its step — from a park too.
func TestAManagedMachinesNamedChildrenAreItsChildren(t *testing.T) {
	kept := &shelf{}
	ends := oneStep("TRY")
	ends.GetStateMachine().States["TRY"].Vocab = []string{"ok", "stuck"}
	ends.GetStateMachine().States["TRY"].Route["stuck"] = to("BLOCKED")
	ends.GetStateMachine().States["BLOCKED"] = &loomv1.WorkflowState{Terminal: true}
	kept.put("ends", ends)
	kept.put("inner", writes("write the tests"))
	kept.put("mid", wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
		"RUN":  {Body: named("inner"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE": {Terminal: true},
	}}))
	managedOver := func(id, child string) *loomv1.WorkflowPattern {
		p := wrap(&loomv1.StateMachinePattern{EntryState: "PHASE", LogicSeat: "MANAGE", States: map[string]*loomv1.WorkflowState{
			"PHASE":  {Body: named(child), Route: map[string]*loomv1.Transition{"done": to("FIN")}},
			"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}},
			"FIN":    {Terminal: true},
		}})
		p.WorkflowId = id
		return p
	}

	t.Run("an end that is not done goes to the manager", func(t *testing.T) {
		model := &answers{say: func(step string, turn int, _ string) string {
			if step == "TRY" && turn == 0 {
				return "cannot\n" + `{"signal":"stuck"}`
			}
			if step == "MANAGE" {
				return "try the other way\n" + `{"signal":"phase"}`
			}
			return "fine\n" + `{"signal":"ok"}`
		}}
		_, err := orchestratorOn(t, model, kept).ExecutePattern(context.Background(), managedOver("named-end", "ends"))
		require.NoError(t, err, "the named child's end was not routed to the manager")
		require.Len(t, model.told("MANAGE"), 1)
		require.Contains(t, model.told("MANAGE")[0], "message in flight:\nBLOCKED\n\ncannot")
		require.Len(t, model.told("TRY"), 2)
	})

	t.Run("a deep escalation reaches the manager, and its word takes the child up from a park", func(t *testing.T) {
		model := codeMissing("only the person knows where the code is\n"+`{"signal":"escalate"}`, whereItIs+"\n"+`{"signal":"phase"}`)
		orch := orchestratorOn(t, model, kept)
		_, err := orch.ExecutePattern(context.Background(), managedOver("named-deep", "mid"))
		var parked *WorkflowSuspended
		require.ErrorAs(t, err, &parked)
		require.Equal(t, []string{"MANAGE"}, parked.Checkpoint.GetStatePath())

		_, err = orch.ResumeWorkflow(context.Background(), managedOver("named-deep", "mid"), parked.Checkpoint,
			&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "it is in the unit"})
		require.NoError(t, err)
		require.Len(t, model.told("PREP"), 1, "the child was started over")
		require.Len(t, model.told("WRITE"), 2)
	})
}

// A set of children given by name is one definition, read once for the entry
// that runs them.
func TestASetOfNamedChildrenIsReadOnceForTheEntry(t *testing.T) {
	kept := &shelf{}
	kept.put("inner", writes("write the tests for {item}"))
	model := codeMissing("unused")
	model.say = func(step string, _ int, _ string) string {
		switch step {
		case "PLAN":
			return "two\n" + `{"signal":"planned","items":"a,b"}`
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		}
		return "written\n" + `{"signal":"tests-ready"}`
	}
	sm := overEach(nil)
	sm.States["EACH"].Body = &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{WorkflowName: "inner", ItemsKey: "items", MaxParallel: 1}}
	p := wrap(sm)
	p.WorkflowId = "named-set"

	_, err := orchestratorOn(t, model, kept).ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, model.told("WRITE"), 2)
	require.Equal(t, []string{"inner"}, kept.askedFor())
}

// A workflow's parameters fill a delegating state's directory template
// whether its child is written in or given by name.
func TestANamedChildsDirectoryTemplateTakesTheWorkflowsParameters(t *testing.T) {
	p := wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
		"RUN": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{WorkflowName: "child", DirTemplate: "{{where}}/{item}"}},
			Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE": {Terminal: true},
	}})
	got := InterpolateVariables(p, map[string]string{"where": "units"})
	require.Equal(t, "units/{item}", got.GetStateMachine().GetStates()["RUN"].GetDelegate().GetDirTemplate())
}

// A person's answer to a run stopped on a named child, when the child's loop
// cannot be found, is not spent: the run waits at the state, on the error,
// and the same answer reaches the child once its loop is there.
func TestAnAnswerForANamedChildThatCannotBeFoundWaits(t *testing.T) {
	kept := &shelf{}
	kept.put("inner", writes("write the tests", "blocked"))
	model := &answers{say: func(step string, turn int, told string) string {
		switch {
		case step == "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case turn == 0:
			return "the suite is red\n" + `{"signal":"blocked"}`
		}
		return "written\n" + `{"signal":"tests-ready"}`
	}}
	orch := orchestratorOn(t, model, kept)
	spec := func() *loomv1.WorkflowPattern {
		p := wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
			"RUN":  {Body: named("inner"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
			"DONE": {Terminal: true},
		}})
		p.WorkflowId = "answer-waits"
		return p
	}
	_, err := orch.ExecutePattern(context.Background(), spec())
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)
	answer := &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "red on main too; go on"}

	inner := kept.byName["inner"]
	delete(kept.byName, "inner")
	_, err = orch.ResumeWorkflow(context.Background(), spec(), parked.Checkpoint, answer)
	var waits *WorkflowSuspended
	require.ErrorAs(t, err, &waits, "the run did not wait where its child could not be found")
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, waits.Checkpoint.GetSuspendReason())
	require.Contains(t, waits.Checkpoint.GetError(), `state "RUN" delegates to "inner"`)

	kept.put("inner", inner)
	_, err = orch.ResumeWorkflow(context.Background(), spec(), waits.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	write := model.told("WRITE")
	require.Len(t, write, 2)
	require.Contains(t, write[1], "red on main too; go on", "the answer was spent by the resume that was refused")
}

// A run is taken up on its machine as the machine is defined then. A machine
// changed while a run was parked — a step's words, a step added on the way
// out — is the machine the run goes on in, from the state it stopped at.
func TestAParkedRunGoesOnInTheMachineAsItIsNowDefined(t *testing.T) {
	model := &answers{say: func(step string, turn int, told string) string {
		switch {
		case step == "ASK" && turn == 0:
			return "which way? This step is ASK.\n" + `{"signal":"escalate"}`
		case step == "ASK":
			return "that way\n" + `{"signal":"ok"}`
		}
		return "did it\n" + `{"signal":"ok"}`
	}}
	orch := orchestratorWith(t, model)
	machine := func(ask string, withProof bool) *loomv1.WorkflowPattern {
		states := map[string]*loomv1.WorkflowState{
			"ASK":  {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: ask}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("DONE")}},
			"DONE": {Terminal: true},
		}
		if withProof {
			states["ASK"].Route["ok"] = to("PROVE")
			states["PROVE"] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "prove it"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("DONE")}}
		}
		p := wrap(&loomv1.StateMachinePattern{EntryState: "ASK", States: states})
		p.WorkflowId = "redefined"
		return p
	}

	_, err := orch.ExecutePattern(context.Background(), machine("ask the old way", false))
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)
	require.Empty(t, parked.Checkpoint.GetConfigFingerprint(), "the park holds a mark of the machine it was taken in")

	_, err = orch.ResumeWorkflow(context.Background(), machine("ask the new way", true), parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "that way"})
	require.NoError(t, err, "the run was refused the machine as it is now defined")
	require.Len(t, model.told("ASK"), 2, "the run did not go on from the state it stopped at")
	require.Len(t, model.told("PROVE"), 1, "the run did not walk the machine as it is now defined")
}

// The one thing a changed machine can refuse a parked run is its position: a
// run standing in a state the machine no longer has is not resumed, and the
// refusal names the state.
func TestARunStandingInAStateTheMachineNoLongerHasIsRefused(t *testing.T) {
	model := &answers{say: func(string, int, string) string {
		return "which way? This step is ASK.\n" + `{"signal":"escalate"}`
	}}
	orch := orchestratorWith(t, model)
	p := oneStep("ASK")
	p.WorkflowId = "moved"
	_, err := orch.ExecutePattern(context.Background(), p)
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)

	renamed := oneStep("ENQUIRE")
	renamed.WorkflowId = "moved"
	_, err = orch.ResumeWorkflow(context.Background(), renamed, parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.ErrorContains(t, err, `the run stands at state "ASK", which this definition does not have`)
	var again *WorkflowSuspended
	require.False(t, errors.As(err, &again), "the refusal parked the run anew")
	require.Empty(t, model.told("ENQUIRE"), "the run was entered somewhere it was not standing")
}

// A child taken up again is taken up in its loop as that loop is defined
// then: a named child changed while the run was parked inside it goes on from
// its step, in the changed loop.
func TestAParkedChildGoesOnInItsLoopAsItIsNowDefined(t *testing.T) {
	kept := &shelf{}
	kept.put("inner", writes("write the tests", "blocked"))
	model := &answers{say: func(step string, turn int, _ string) string {
		switch {
		case step == "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case step == "WRITE" && turn == 0:
			return "the suite is red\n" + `{"signal":"blocked"}`
		case step == "WRITE":
			return "written\n" + `{"signal":"tests-ready"}`
		}
		return "looked\n" + `{"signal":"ok"}`
	}}
	orch := orchestratorOn(t, model, kept)
	spec := func() *loomv1.WorkflowPattern {
		p := wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
			"RUN":  {Body: named("inner"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
			"DONE": {Terminal: true},
		}})
		p.WorkflowId = "child-redefined"
		return p
	}
	_, err := orch.ExecutePattern(context.Background(), spec())
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)

	changed := writes("write the tests", "blocked")
	changed.GetStateMachine().States["WRITE"].Route["tests-ready"] = to("REVIEW")
	changed.GetStateMachine().States["REVIEW"] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "review them"}},
		Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("DONE")}}
	kept.put("inner", changed)

	_, err = orch.ResumeWorkflow(context.Background(), spec(), parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "red on main too; go on"})
	require.NoError(t, err, "the child was refused its loop as it is now defined")
	require.Len(t, model.told("PREP"), 1, "the child was started over")
	require.Len(t, model.told("REVIEW"), 1, "the child did not walk its loop as it is now defined")
}

// A child whose machine cannot be walked — here one whose manager cannot be
// seated — has not failed at its work: it never began. The run stops at the
// state that delegates to it, saying why, and nothing is recorded as that
// child's outcome: once the child's definition is right, resuming the run
// enters the state and runs it.
func TestAChildThatCannotBeWalkedStopsItsStateAndIsRunOnceItCan(t *testing.T) {
	kept := &shelf{}
	bad := oneStep("LATER")
	bad.GetStateMachine().LogicSeat = "NOPE"
	kept.put("child", bad)
	model := &answers{say: func(string, int, string) string { return "fine\n" + `{"signal":"ok"}` }}
	orch := orchestratorOn(t, model, kept)
	spec := func() *loomv1.WorkflowPattern {
		p := wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
			"RUN":  {Body: named("child"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
			"DONE": {Terminal: true},
		}})
		p.WorkflowId = "not-walkable"
		return p
	}

	_, err := orch.ExecutePattern(context.Background(), spec())
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, stopped.Checkpoint.GetSuspendReason())
	require.Equal(t, []string{"RUN"}, stopped.Checkpoint.GetStatePath())
	require.Contains(t, stopped.Checkpoint.GetError(), `logic_seat "NOPE" is not a state of this loop`, "the stop does not say why the child could not be walked")
	require.Empty(t, model.told("LATER"))

	kept.put("child", oneStep("LATER"))
	_, err = orch.ResumeWorkflow(context.Background(), spec(), stopped.Checkpoint, &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err, "with the child's definition right, the run did not go on")
	require.Len(t, model.told("LATER"), 1)
}

// A child that began and failed outright is an outcome its parent routes on,
// and the state says why it failed: the reason goes with FAILED in what the
// state hands on.
func TestAChildThatFailsOutrightSaysWhy(t *testing.T) {
	model := &answers{say: func(string, int, string) string { return "fine\n" + `{"signal":"ok"}` }}
	orch := orchestratorWith(t, model)
	// A pipeline whose one stage names an agent nobody seats.
	child := &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_Pipeline{Pipeline: &loomv1.PipelinePattern{
		InitialPrompt: "go", Stages: []*loomv1.PipelineStage{{AgentId: "nobody", PromptTemplate: "{{previous}}"}}}}}
	p := wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
		"RUN":   {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}}, Route: map[string]*loomv1.Transition{"failed": to("AFTER")}},
		"AFTER": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "look at it"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("DONE")}},
		"DONE":  {Terminal: true},
	}})
	p.WorkflowId = "fails-outright"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	after := model.told("AFTER")
	require.Len(t, after, 1)
	require.Contains(t, after[0], "message in flight:\nFAILED\n\n")
	require.Contains(t, after[0], "nobody", "the state does not say why its child failed")
}
