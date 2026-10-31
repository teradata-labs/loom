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

func agentStep(instructions string, vocab ...string) *loomv1.WorkflowState_Agent {
	return &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: instructions}}
}

// proving is a task that writes its code and proves it over a set of suites.
func proving() *loomv1.WorkflowPattern {
	suite := wrap(&loomv1.StateMachinePattern{EntryState: "JUDGE", States: map[string]*loomv1.WorkflowState{
		"JUDGE": {Body: agentStep("judge suite {item}"), Vocab: []string{"green"}, Route: map[string]*loomv1.Transition{"green": to("GREEN")}},
		"GREEN": {Terminal: true},
	}})
	return wrap(&loomv1.StateMachinePattern{EntryState: "CODE", States: map[string]*loomv1.WorkflowState{
		"CODE": {Body: agentStep("write the code"), Vocab: []string{"code-ready"}, Route: map[string]*loomv1.Transition{"code-ready": to("PROVE")}},
		"PROVE": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: suite, ItemsKey: "suites", MaxParallel: 1}},
			Route: map[string]*loomv1.Transition{"green": to("DONE")}},
		"DONE": {Terminal: true},
	}})
}

// delivering walks tasks one at a time; ACCEPT judges each, and its finding
// sends a delivered task back in at the step the route names.
func delivering(enter string) *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: "PLAN", States: map[string]*loomv1.WorkflowState{
		"PLAN": {Body: agentStep("plan it"), Vocab: []string{"planned"}, Route: map[string]*loomv1.Transition{"planned": to("EACH")}},
		"EACH": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: proving(), ItemsKey: "items", MaxParallel: 1, Step: true}},
			Route: map[string]*loomv1.Transition{SteppedSignal: to("ACCEPT"), FannedSignal: to("FIN"), "done": to("FIN")}},
		"ACCEPT": {Body: agentStep("accept what was delivered"), Vocab: []string{"accepted", "deltas"},
			Route: map[string]*loomv1.Transition{"accepted": to("EACH"),
				"deltas": {Target: &loomv1.Transition_NextState{NextState: "EACH"}, Enter: enter}}},
		"FIN": {Terminal: true},
	}})
}

func rejectsOnce(model *answers) func(string, int, string) string {
	rejected := false
	return func(step string, turn int, told string) string {
		switch step {
		case "PLAN":
			return "two of them\n" + `{"signal":"planned","items":"a,b"}`
		case "CODE":
			return "coded\n" + `{"signal":"code-ready","suites":"unit,e2e"}`
		case "JUDGE":
			if strings.Contains(told, "THE-FINDING") {
				return "fixed and green\n" + `{"signal":"green"}`
			}
			return "green\n" + `{"signal":"green"}`
		case "ACCEPT":
			if !rejected && strings.Contains(told, "a=DONE") {
				rejected = true
				return "THE-FINDING: the handler is missing\n" + `{"signal":"deltas","items":"a,b"}`
			}
			return "fine\n" + `{"signal":"accepted"}`
		}
		return "?"
	}
}

// A delivered child sent back by a route that names a step is taken up at that
// step: the step runs its last child again, which reads the finding. Nothing
// before it runs again.
func TestADeliveredChildSentBackIsTakenUpAtTheStepTheRouteNames(t *testing.T) {
	model := &answers{}
	model.say = rejectsOnce(model)
	p := delivering("PROVE")
	p.WorkflowId = "sent-back"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	require.NoError(t, err)

	require.Len(t, model.told("CODE"), 2, "a was coded again: the route's step was not where it took up")
	var readers []string
	for _, told := range model.told("JUDGE") {
		if strings.Contains(told, "THE-FINDING") {
			readers = append(readers, told[strings.Index(told, "judge suite "):strings.Index(told, "judge suite ")+16])
		}
	}
	require.Equal(t, []string{"judge suite e2e\n"}, readers, "the finding was not read by the last suite's judge alone")
	require.Len(t, model.told("JUDGE"), 5, "a: unit, e2e, e2e again; b: unit, e2e")
}

// A route that names a step only sends back a child that ran to done before:
// a child that never ran starts at its beginning.
func TestAChildThatNeverRanStartsAtItsBeginning(t *testing.T) {
	model := &answers{}
	inner := rejectsOnce(model)
	sent := false
	model.say = func(step string, turn int, told string) string {
		if step == "ACCEPT" && strings.Contains(told, "a=DONE") && !sent {
			sent = true
			return "THE-FINDING: b first\n" + `{"signal":"deltas","items":"b"}`
		}
		return inner(step, turn, told)
	}
	p := delivering("PROVE")
	p.WorkflowId = "never-ran"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, model.told("CODE"), 2, "b was not started at its beginning, or a was coded again")
}

// A route can say where a child takes up only when it enters a state that
// runs children.
func TestARouteThatNamesWhereAChildTakesUpMustEnterAStateThatRunsChildren(t *testing.T) {
	p := delivering("PROVE")
	p.GetStateMachine().States["ACCEPT"].Route["deltas"].Target = &loomv1.Transition_NextState{NextState: "PLAN"}
	_, err := orchestratorWith(t, &answers{say: func(string, int, string) string { return "?" }}).ExecutePattern(context.Background(), p)
	var unwalkable *NotWalkable
	require.ErrorAs(t, err, &unwalkable)
	require.Contains(t, err.Error(), `says where a child takes up, and "PLAN" runs no child`)
}

// A route that names a step the child's loop does not have stops the state
// that runs it, naming the step; the walk waits there.
func TestARouteNamingAStepTheChildLacksStopsTheState(t *testing.T) {
	model := &answers{}
	model.say = rejectsOnce(model)
	p := delivering("NOWHERE")
	p.WorkflowId = "lacks"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	var waits *WorkflowSuspended
	require.ErrorAs(t, err, &waits)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, waits.Checkpoint.GetSuspendReason())
	require.Equal(t, []string{"EACH"}, waits.Checkpoint.GetStatePath())
	require.Contains(t, waits.Checkpoint.GetError(), `sends a child back in at "NOWHERE", which is not a step of its loop that runs children`)
	require.Len(t, model.told("CODE"), 1, "a was coded again")
}

// A child taken up at the step that ended it starts there; the loops it runs
// begin where they begin, not at a step of the same name.
func TestAReEntryIsForTheChildItNames(t *testing.T) {
	grand := wrap(&loomv1.StateMachinePattern{EntryState: "X", States: map[string]*loomv1.WorkflowState{
		"X":    {Body: agentStep("grandchild first step"), Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("B")}},
		"B":    {Body: agentStep("grandchild second step"), Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("DONE")}},
		"DONE": {Terminal: true},
	}})
	child := wrap(&loomv1.StateMachinePattern{EntryState: "A", States: map[string]*loomv1.WorkflowState{
		"A":     {Body: agentStep("child first step"), Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("B")}},
		"B":     {Body: agentStep("child second step"), Vocab: []string{"ok", "stuck"}, Route: map[string]*loomv1.Transition{"ok": to("RUN"), "stuck": to("STUCK")}},
		"RUN":   {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: grand}}, Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE":  {Terminal: true},
		"STUCK": {Terminal: true},
	}})
	stuck := false
	model := &answers{say: func(step string, turn int, told string) string {
		switch {
		case step == "B" && strings.Contains(told, "child second step") && !stuck:
			stuck = true
			return "cannot\n" + `{"signal":"stuck"}`
		case step == "MANAGE":
			return "go on\n" + `{"signal":"phase"}`
		}
		return "fine\n" + `{"signal":"ok"}`
	}}
	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), under("leak", child))
	require.NoError(t, err)
	var grandFirst int
	for _, told := range model.told("X") {
		if strings.Contains(told, "grandchild first step") {
			grandFirst++
		}
	}
	require.Equal(t, 1, grandFirst, "the grandchild was not begun at its own first step")
}
