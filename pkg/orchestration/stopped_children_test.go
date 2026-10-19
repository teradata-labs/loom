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

// managedSet is a managed machine whose PLAN names the items and whose EACH
// runs a child for each, as many at once as given.
func managedSet(id string, atOnce int32) *loomv1.WorkflowPattern {
	sm := overEach(writes("write the tests for {item}"))
	sm.States["EACH"].GetDelegate().MaxParallel = atOnce
	sm.LogicSeat = "MANAGE"
	sm.States["MANAGE"] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}}
	p := wrap(sm)
	p.WorkflowId = id
	return p
}

// itemOfPrompt is the item a WRITE step was started for, read from its words.
func itemOfPrompt(told string) string {
	for _, item := range []string{"a", "b"} {
		if strings.Contains(told, "for "+item) {
			return item
		}
	}
	return ""
}

// Two children of a set that run at once and both escalate both stand where
// they stopped, each on its own record. Their manager hears them one at a
// time — each with what that child said. The one answered is taken up with
// the answer; the other is taken up with its own words, asks again, and is
// heard next. Each reads only the words meant for it, and neither is started
// again.
func TestChildrenThatStopTogetherAreHeardOneAtATime(t *testing.T) {
	model := &answers{}
	model.say = func(step string, turn int, told string) string {
		switch step {
		case "PLAN":
			return "two of them\n" + `{"signal":"planned","items":"a,b"}`
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "WRITE":
			if strings.Contains(told, "the fixture for") {
				return "written with " + told[strings.Index(told, "the fixture for"):strings.Index(told, "the fixture for")+17] + "\n" + `{"signal":"tests-ready"}`
			}
			return "no fixture for " + itemOfPrompt(told) + ". This step is WRITE.\n" + `{"signal":"escalate"}`
		case "MANAGE":
			for _, item := range []string{"a", "b"} {
				if strings.Contains(told, item+"=escalated") {
					return "use the fixture for " + item + "\n" + `{"signal":"each"}`
				}
			}
		}
		return "?"
	}
	orch := orchestratorWith(t, model)

	_, err := orch.ExecutePattern(context.Background(), managedSet("together", 2))
	require.NoError(t, err, "the run parked, or did not finish")

	manage := model.told("MANAGE")
	require.Len(t, manage, 2, "each child's escalation is one turn of the manager")
	require.Contains(t, manage[0], "message in flight:\na=escalated\n\nFrom: EACH (a) › WRITE\n")
	require.Contains(t, manage[0], "\n\nIt said:\nno fixture for a.")
	require.Contains(t, manage[1], "message in flight:\nb=escalated\n\nFrom: EACH (b) › WRITE\n")
	require.Contains(t, manage[1], "\n\nIt said:\nno fixture for b.")
	require.Len(t, model.told("PREP"), 2, "a child that stopped was started over")
	write := model.told("WRITE")
	require.Len(t, write, 5, "a: stopped, answered; b: stopped, asked again, answered")
	var forA, forB int
	for _, p := range write {
		if strings.Contains(p, "message in flight:\nuse the fixture for a") {
			forA++
		}
		if strings.Contains(p, "message in flight:\nuse the fixture for b") {
			forB++
		}
	}
	require.Equal(t, []int{1, 1}, []int{forA, forB}, "a child was taken up with words meant for another")
}

// The same holds across a park: with no manager above, the first child's stop
// stops the run at the state that runs the set, and the other stands on its
// own record. Answered, the first goes on; the other, taken up with its own
// words, asks again, and the run stops on it; a second answer takes it up.
func TestChildrenThatStopTogetherParkTheRunOneAtATime(t *testing.T) {
	model := &answers{}
	model.say = func(step string, turn int, told string) string {
		switch step {
		case "PLAN":
			return "two of them\n" + `{"signal":"planned","items":"a,b"}`
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "WRITE":
			if strings.Contains(told, "use the fixture") {
				return "written\n" + `{"signal":"tests-ready"}`
			}
			return "no fixture for " + itemOfPrompt(told) + ". This step is WRITE.\n" + `{"signal":"escalate"}`
		}
		return "?"
	}
	orch := orchestratorWith(t, model)
	spec := func() *loomv1.WorkflowPattern {
		sm := overEach(writes("write the tests for {item}"))
		sm.States["EACH"].GetDelegate().MaxParallel = 2
		p := wrap(sm)
		p.WorkflowId = "together-parked"
		return p
	}
	answer := &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "use the fixture"}

	_, err := orch.ExecutePattern(context.Background(), spec())
	var first *WorkflowSuspended
	require.ErrorAs(t, err, &first)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, first.Checkpoint.GetSuspendReason())
	require.Equal(t, []string{"EACH"}, first.Checkpoint.GetStatePath())
	word, said := stopOf(first.Checkpoint)
	require.Equal(t, EscalatedSignal, word)
	require.Contains(t, said, "a=escalated\n\nFrom: EACH (a) › WRITE\n")
	place, ok := orch.places.PlaceOf(context.Background(), "together-parked-EACH-b")
	require.True(t, ok)
	require.Equal(t, "WRITE", place.At(), "the other stopped child does not stand where it stopped")

	_, err = orch.ResumeWorkflow(context.Background(), spec(), first.Checkpoint, answer)
	var second *WorkflowSuspended
	require.ErrorAs(t, err, &second, "the second child's stop was not put to anyone")
	_, said = stopOf(second.Checkpoint)
	require.Contains(t, said, "b=escalated\n\nFrom: EACH (b) › WRITE\n")
	require.Len(t, model.told("WRITE"), 4, "a answered; b asked again")

	_, err = orch.ResumeWorkflow(context.Background(), spec(), second.Checkpoint, answer)
	require.NoError(t, err)
	require.Len(t, model.told("PREP"), 2, "a child that stopped was started over")
	require.Len(t, model.told("WRITE"), 5)
}

// A new list is a fresh walk: children that stopped and are not on it stand
// where they stopped, on their own records, and none of them is raised or
// taken up by it.
func TestStoppedChildrenNotOnANewListAreNotRaised(t *testing.T) {
	model := &answers{}
	model.say = func(step string, turn int, told string) string {
		switch step {
		case "PLAN":
			return "two of them\n" + `{"signal":"planned","items":"a,b"}`
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "WRITE":
			if strings.Contains(told, "for c") {
				return "written\n" + `{"signal":"tests-ready"}`
			}
			return "no fixture. This step is WRITE.\n" + `{"signal":"escalate"}`
		}
		return "leave them; do c\n" + `{"signal":"each","items":"c"}`
	}
	orch := orchestratorWith(t, model)

	_, err := orch.ExecutePattern(context.Background(), managedSet("let-go", 2))
	require.NoError(t, err)
	require.Len(t, model.told("MANAGE"), 1, "a child of the list before was raised after the new list")
	require.Len(t, model.told("WRITE"), 3, "a, b and c")
}

// A stop caught by a ceiling is not lost. A run that reaches its ceiling as
// a child's stop comes back is parked at the state that runs the child, the
// child standing where it stopped on its own record. Granted more, the state
// takes the child up with its own words; it asks again, or goes on.
func TestAStopCaughtByACeilingIsAskedAgain(t *testing.T) {
	model := &answers{say: func(step string, turn int, _ string) string {
		switch step {
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "WRITE":
			return []string{cannotWrite, "nor there. This step is WRITE.\n" + `{"signal":"escalate"}`, "written\n" + `{"signal":"tests-ready"}`}[min(turn, 2)]
		}
		return []string{"look in the unit", "look in the pool"}[min(turn, 1)] + "\n" + `{"signal":"phase"}`
	}}
	orch := orchestratorWith(t, model)
	spec := func() *loomv1.WorkflowPattern {
		p := under("ceiling", writes("write the tests"))
		p.GetStateMachine().MaxCostUsd = 3 * turnCost
		return p
	}

	_, err := orch.ExecutePattern(context.Background(), spec())
	var capped *WorkflowSuspended
	require.ErrorAs(t, err, &capped)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_COST_CAP, capped.Checkpoint.GetSuspendReason())
	require.Equal(t, []string{"PHASE"}, capped.Checkpoint.GetStatePath())
	require.Len(t, model.told("MANAGE"), 1)
	place, ok := orch.places.PlaceOf(context.Background(), "ceiling-PHASE")
	require.True(t, ok)
	require.Equal(t, "WRITE", place.At(), "the child does not stand where it stopped")

	_, err = orch.ResumeWorkflow(context.Background(), spec(), capped.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	write := model.told("WRITE")
	require.Len(t, write, 3, "the child was not taken up again")
	require.Contains(t, write[2], "message in flight:\nlook in the unit", "the child was not taken up with its own words")
	require.Len(t, model.told("PREP"), 1, "the child was started over")
}

// A manager that sends a step back with its word alone sends it back with
// nothing: the step is told nothing new came — not what a machine further up
// once handed the machine in between.
func TestAWordlessSendBackCarriesNothingFromFurtherUp(t *testing.T) {
	model := &answers{say: func(step string, turn int, _ string) string {
		switch step {
		case "FIRST":
			return "TOP-HANDOFF\n" + `{"signal":"ok"}`
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "WRITE":
			if turn == 0 {
				return cannotWrite
			}
			return "written\n" + `{"signal":"tests-ready"}`
		}
		return `{"signal":"phase"}`
	}}
	orch := orchestratorWith(t, model)
	p := wrap(&loomv1.StateMachinePattern{EntryState: "FIRST", States: map[string]*loomv1.WorkflowState{
		"FIRST": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "start"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("RUN")}},
		"RUN":   {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: under("", writes("write the tests"))}}, Route: map[string]*loomv1.Transition{"fin": to("DONE")}},
		"DONE":  {Terminal: true},
	}})
	p.WorkflowId = "wordless"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	write := model.told("WRITE")
	require.Len(t, write, 2)
	require.NotContains(t, write[1], "TOP-HANDOFF", "the step was handed words from two machines up")
	require.NotContains(t, write[1], "message in flight", "the step was handed words its manager did not send")
}

// Stopping a run while a kept child is being taken up, below a machine that
// holds no child of its own, ends the walk with the stop — it does not bring
// the process down.
func TestStoppingARunDuringATakeUpEndsTheWalk(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	model := codeMissing(whereItIs + "\n" + `{"signal":"phase"}`)
	model.fails = func(step string, turn int) error {
		if step == "WRITE" && turn == 1 {
			stop()
			return context.Canceled
		}
		return nil
	}
	orch := orchestratorWith(t, model)
	p := running(under("", writes("write the tests")))
	p.WorkflowId = "stopped-mid-take-up"

	var err error
	require.NotPanics(t, func() { _, err = orch.ExecutePattern(ctx, p) })
	require.Error(t, err)
}

// A gated step that says it cannot do the work has said a word its table does
// not route. Approving what it said does not fail the resume: the run stops at
// the step, as it would have with no gate, for whoever settles it.
func TestApprovingAGatedStepThatEscalatedStopsTheRunAtTheStep(t *testing.T) {
	model := &answers{say: func(step string, turn int, _ string) string {
		if turn == 0 {
			return "which way? This step is ASK.\n" + `{"signal":"escalate"}`
		}
		return "that way\n" + `{"signal":"ok"}`
	}}
	orch := orchestratorWith(t, model)
	spec := func() *loomv1.WorkflowPattern {
		p := oneStep("ASK")
		p.GetStateMachine().States["ASK"].HitlGate = &loomv1.HITLGate{PromptTemplate: "Approve it"}
		p.WorkflowId = "gated"
		return p
	}

	_, err := orch.ExecutePattern(context.Background(), spec())
	var gated *WorkflowSuspended
	require.ErrorAs(t, err, &gated)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_GATE, gated.Checkpoint.GetSuspendReason())

	_, err = orch.ResumeWorkflow(context.Background(), spec(), gated.Checkpoint, &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped, "approving the gate failed the resume")
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, stopped.Checkpoint.GetSuspendReason())
	require.Equal(t, []string{"ASK"}, stopped.Checkpoint.GetStatePath())
}

// A child taken up again is as much a child as one started: a word it is to
// hand to the scope that started it is handed there after a take-up too.
func TestAChildTakenUpStillHandsItsOwnersWordUp(t *testing.T) {
	model := &answers{say: func(step string, turn int, _ string) string {
		switch {
		case step == "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case turn == 0:
			return "the suite is red\n" + `{"signal":"blocked"}`
		}
		return "only the owner can say\n" + `{"signal":"bounce"}`
	}}
	orch := orchestratorWith(t, model)
	spec := func() *loomv1.WorkflowPattern {
		child := writes("write the tests", "blocked", "bounce")
		child.GetStateMachine().BubbleSignals = []string{"bounce"}
		p := running(child)
		p.WorkflowId = "bubbles"
		return p
	}
	_, err := orch.ExecutePattern(context.Background(), spec())
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)

	_, err = orch.ResumeWorkflow(context.Background(), spec(), parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "go on"})
	var again *WorkflowSuspended
	require.ErrorAs(t, err, &again)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_BUBBLED, innermost(again.Checkpoint).GetSuspendReason(),
		"a child taken up again was not treated as a child")
}

// A run that holds a copy of a stopped child in its variables — as an earlier
// engine kept them, readable or not — lets the copy go when it is taken up:
// the child is taken up from its own record, where it stands.
func TestAnEarlierEnginesCopyOfAStoppedChildIsLetGo(t *testing.T) {
	model := codeMissing("only the person knows where the code is\n"+`{"signal":"escalate"}`, whereItIs+"\n"+`{"signal":"phase"}`)
	orch := orchestratorWith(t, model)
	spec := func() *loomv1.WorkflowPattern { return under("unread", running(writes("write the tests"))) }
	_, err := orch.ExecutePattern(context.Background(), spec())
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)
	parked.Checkpoint.Vars[keptKey("PHASE")] = "not a record"
	parked.Checkpoint.Vars["__waiting:PHASE"] = "not a record either"

	_, err = orch.ResumeWorkflow(context.Background(), spec(), parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "it is in the unit"})
	require.NoError(t, err, "the copy was used in place of the child's own record")
	require.Len(t, model.told("PREP"), 1, "the child was started over")
	write := model.told("WRITE")
	require.Len(t, write, 2)
	require.Contains(t, write[1], "message in flight:\n"+whereItIs)
}

// A host can ask whether a machine can be walked without walking it, and is
// told why not: nothing of it runs.
func TestAHostIsToldWhyAMachineCannotBeWalked(t *testing.T) {
	model := &answers{say: func(string, int, string) string { return "?" }}
	orch := orchestratorWith(t, model)
	good := oneStep("ASK")
	require.NoError(t, orch.CanWalk(context.Background(), good))

	unseated := oneStep("ASK")
	unseated.GetStateMachine().States["ASK"].GetAgent().AgentId = "ghost"
	require.ErrorContains(t, orch.CanWalk(context.Background(), unseated), "ghost")
	require.Empty(t, model.told("ASK"))
}

// Words sent back to a child that stopped are for that child. The children of
// the set that start after it open with what the child before them ended with
// — never with what was said to the one that stopped.
func TestWordsSentBackToAKeptChildDoNotReachTheRestOfTheSet(t *testing.T) {
	const forA = "use the fixture for a, and only a"
	model := &answers{say: func(step string, _ int, told string) string {
		switch step {
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "PLAN":
			return "two of them, in order\n" + `{"signal":"planned","items":"a,b"}`
		case "WRITE":
			if strings.Contains(told, "for a") && !strings.Contains(told, forA) {
				return "no fixture for a. This step is WRITE.\n" + `{"signal":"escalate"}`
			}
			return "written\n" + `{"signal":"tests-ready"}`
		case "MANAGE":
			return forA + "\n" + `{"signal":"each"}`
		}
		return "?"
	}}
	p := wrap(overEach(writes("write the tests for {item}")))
	p.GetStateMachine().LogicSeat = "MANAGE"
	p.GetStateMachine().States["MANAGE"] = &loomv1.WorkflowState{
		Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}}
	p.WorkflowId = "kept-words"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	require.NoError(t, err)

	var a, b []string
	for _, prompt := range append(model.told("PREP"), model.told("WRITE")...) {
		if strings.Contains(prompt, "for b") || strings.Contains(prompt, "kept-words-EACH-b") {
			b = append(b, prompt)
		}
		if strings.Contains(prompt, forA) {
			a = append(a, prompt)
		}
	}
	require.NotEmpty(t, a, "the kept child was not given the words it was sent back with")
	prep := model.told("PREP")
	require.Len(t, prep, 2, "each child takes its first step once")
	require.Contains(t, prep[1], "message in flight:\nwritten",
		"the child that started after did not open with what the child before it ended with")
	for _, prompt := range append(prep[1:], b...) {
		require.NotContains(t, prompt, forA, "words sent back to the child that stopped reached another child of the set")
	}
}
