// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// A state that runs a child answers with how the child ended, whether it runs
// one child, or the entries of a list one at a time. A child that ran to done
// went on; a child that ended any other way did not, and the state's word is
// that end: the spec routes it, and an end the spec does not route is a word
// nobody expected — the state's escalation.

const stuckBecause = "the gateway sends every call to the login page"

// works is a child machine whose one step runs to DONE, or says it is stuck
// and ends ESCALATED.
func works() *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: "WORK", States: map[string]*loomv1.WorkflowState{
		"WORK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "do {item}"}},
			Vocab: []string{"done", "stuck"},
			Route: map[string]*loomv1.Transition{"done": to("DONE"), "stuck": to("ESCALATED")}},
		"DONE":      {Terminal: true},
		"ESCALATED": {Terminal: true},
	}})
}

// oneAtATime is a machine whose EACH runs works for each item of a list, one
// at a time: JUDGE reads an entry that was stepped and sends the walk back for
// the next; routes are what EACH routes besides.
func oneAtATime(seat string, routes map[string]*loomv1.Transition) *loomv1.WorkflowPattern {
	each := map[string]*loomv1.Transition{SteppedSignal: to("JUDGE"), FannedSignal: to("FIN")}
	for word, tr := range routes {
		each[word] = tr
	}
	sm := &loomv1.StateMachinePattern{EntryState: "PLAN", LogicSeat: seat, States: map[string]*loomv1.WorkflowState{
		"PLAN": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "plan it"}},
			Vocab: []string{"planned"}, Route: map[string]*loomv1.Transition{"planned": to("EACH")}},
		"EACH": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{
			Workflow: works(), ItemsKey: "items", MaxParallel: 1, Step: true}}, Route: each},
		"JUDGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "judge what was delivered"}},
			Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("EACH")}},
		"HANDLE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "handle what stopped"}},
			Vocab: []string{"handled"}, Route: map[string]*loomv1.Transition{"handled": to("FIN")}},
		"FIN": {Terminal: true},
	}}
	if seat != "" {
		sm.States[seat] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}}
	}
	return wrap(sm)
}

// bIsStuck is a run over the items a and b in which a is done and b is stuck.
func bIsStuck(manage string) *answers {
	return &answers{say: func(step string, _ int, told string) string {
		switch step {
		case "PLAN":
			return "two of them\n" + `{"signal":"planned","items":"a,b"}`
		case "WORK":
			if strings.Contains(told, "do b") {
				return stuckBecause + "\n" + `{"signal":"stuck"}`
			}
			return "did it\n" + `{"signal":"done"}`
		case "JUDGE":
			return "fine\n" + `{"signal":"ok"}`
		case "HANDLE":
			return "seen\n" + `{"signal":"handled"}`
		case "MANAGE":
			return manage
		}
		return "?"
	}}
}

// An entry that ran to done is stepped, and judged. An entry that ended any
// other way is not handed to the judge as though it had delivered: the state
// answers with that end, the spec's route for it is taken, and the step it
// leads to is given the whole of what the step that ended the child said.
func TestAnEntryThatDidNotRunToDoneIsRoutedByHowItEnded(t *testing.T) {
	model := bIsStuck("")
	p := oneAtATime("", map[string]*loomv1.Transition{"escalated": to("HANDLE")})
	p.WorkflowId = "stopped-entry"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	require.NoError(t, err)

	judged := model.told("JUDGE")
	require.Len(t, judged, 1, "the judge of delivered entries was given an entry that did not deliver")
	require.Contains(t, judged[0], "message in flight:\na=DONE")
	handled := model.told("HANDLE")
	require.Len(t, handled, 1, "the entry that stopped was not routed by how it ended")
	require.Contains(t, handled[0], "message in flight:\nb=ESCALATED\n\n"+stuckBecause,
		"the step it was routed to was not given what the step that ended it said")
}

// An end the state does not route is its escalation. In a machine with a
// manager the manager hears it, with what the step that ended the child said.
func TestAnEntrysEndTheStateDoesNotRouteReachesTheManager(t *testing.T) {
	model := bIsStuck("leave it\n" + `{"signal":"escalate"}`)
	p := oneAtATime("MANAGE", nil)
	p.WorkflowId = "unrouted-entry"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped, "the manager's own escalation did not stop the run")

	require.Len(t, model.told("JUDGE"), 1, "the entry that stopped was judged as delivered")
	manage := model.told("MANAGE")
	require.Len(t, manage, 1, "the manager did not hear the entry's end")
	require.Contains(t, manage[0], "b=ESCALATED\n\n"+stuckBecause)
}

// With no manager in its machine, the state stops the run as an escalation:
// the nearest manager above hears it, and with none above it is the person's.
func TestAnEndNoStateRoutesStopsTheRunAsAnEscalation(t *testing.T) {
	for name, p := range map[string]*loomv1.WorkflowPattern{
		"an entry of a list":  oneAtATime("", nil),
		"a state's one child": running(works()),
	} {
		model := bIsStuck("")
		if name == "a state's one child" {
			model.say = func(string, int, string) string { return stuckBecause + "\n" + `{"signal":"stuck"}` }
		}
		p.WorkflowId = "unrouted-" + strings.ReplaceAll(name, " ", "-")
		_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
		var stopped *WorkflowSuspended
		require.ErrorAs(t, err, &stopped, name)
		require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, stopped.Checkpoint.GetSuspendReason(), name)
		word, said := stopOf(stopped.Checkpoint)
		require.Equal(t, EscalatedSignal, word, "%s: the stop does not say the work below cannot go on", name)
		require.Contains(t, said, "ESCALATED\n\n"+stuckBecause, name)
	}
}

// Above a machine with no manager, the manager of the machine that runs it
// hears the end its child's state did not route.
func TestAChildsUnroutedEndReachesTheManagerAbove(t *testing.T) {
	model := &answers{say: func(step string, _ int, _ string) string {
		if step == "MANAGE" {
			return "leave it\n" + `{"signal":"escalate"}`
		}
		return stuckBecause + "\n" + `{"signal":"stuck"}`
	}}
	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), under("above", running(works())))
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	manage := model.told("MANAGE")
	require.Len(t, manage, 1, "the manager above did not hear it")
	require.Contains(t, manage[0], "ESCALATED\n\n"+stuckBecause)
}

// stopOf is the word a stopped walk stopped on and what its step said.
func stopOf(ckpt *loomv1.WorkflowCheckpoint) (string, string) {
	results := ckpt.GetAllResults()
	if len(results) == 0 {
		return "", ""
	}
	last := results[len(results)-1]
	return normalizeSignal(last.GetMetadata()["signal"]), strings.TrimSpace(last.GetOutput())
}

// escalationOf is what a stopped walk's step said, when it stopped on a word
// its loop does not route: the step's own escalation, or a step that runs a
// child saying the work below cannot go on.
func escalationOf(ckpt *loomv1.WorkflowCheckpoint) (string, bool) {
	leaf := ckpt
	for leaf.GetSuspendReason() == loomv1.SuspendReason_SUSPEND_REASON_NESTED && leaf.GetNestedCheckpoint() != nil {
		leaf = leaf.GetNestedCheckpoint()
	}
	if leaf.GetSuspendReason() != loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE {
		return "", false
	}
	word, said := stopOf(leaf)
	return said, word == EscalateSignal || word == EscalatedSignal
}
