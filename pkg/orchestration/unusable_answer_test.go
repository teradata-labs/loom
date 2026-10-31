// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// An answer the loop cannot use goes back to the agent that gave it, once for
// that answer, with the line as it was received and what is wrong with it.
// Still unusable, it is the step's escalation: it goes where the step's own
// escalation goes — the nearest manager, or a stop for whoever started the run.

const (
	finding = "the route check is missing"
	// brokenDeltas is a reviewer's answer whose signal object is never closed.
	brokenDeltas = finding + "\n" + `{"signal":"deltas","deltas":[{"ac":"ac-13","problem":"no check"}]`
	goodDeltas   = finding + "\n" + `{"signal":"deltas","deltas":[{"ac":"ac-13","problem":"no check"}]}`
	sentBackFor  = "Your response format or state is invalid.\n" +
		`The line: {"signal":"deltas","deltas":[{"ac":"ac-13","problem":"no check"}]` + "\n" +
		"What is wrong with it: it is not valid JSON: unexpected end of JSON input\n" +
		"Your answer stands. Send it again in the expected format."
	twiceUnread = "CHECK ended twice with a response the loop could not read.\n" +
		`The line: {"signal":"deltas","deltas":[{"ac":"ac-13","problem":"no check"}]` + "\n" +
		"What is wrong with it: it is not valid JSON: unexpected end of JSON input\n" +
		"What it said: " + finding
)

// reviewed is a machine with no manager: WRITE, then CHECK, which sends the
// work back to WRITE on deltas and ends the machine on converged.
func reviewed() *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: "WRITE", States: map[string]*loomv1.WorkflowState{
		"WRITE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "write the tests"}},
			Vocab: []string{"tests-ready"}, Route: map[string]*loomv1.Transition{"tests-ready": to("CHECK")}},
		"CHECK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "check the tests"}},
			Vocab: []string{"converged", "deltas"},
			Route: map[string]*loomv1.Transition{"converged": to("DONE"), "deltas": to("WRITE")}},
		"DONE": {Terminal: true},
	}})
}

// reviewing is a run of reviewed. check is what CHECK answers on each entry,
// and resent what the agent answers each time an answer is sent back to it:
// a turn that names no step, because it is told only what was wrong.
func reviewing(check, resent []string, manage ...string) *answers {
	return &answers{say: func(step string, turn int, told string) string {
		switch step {
		case "WRITE":
			return "written\n" + `{"signal":"tests-ready"}`
		case "CHECK":
			return check[min(turn, len(check)-1)]
		case "MANAGE":
			return manage[min(turn, len(manage)-1)]
		case "":
			return resent[min(turn, len(resent)-1)]
		}
		return "?"
	}}
}

const converged = "all asserted\n" + `{"signal":"converged"}`

// turnStamp is the time an agent puts in front of every turn it is given.
var turnStamp = regexp.MustCompile(`^\[[^\]]+\] `)

// sentBack is every turn in which an answer was sent back, as the agent was
// given it.
func sentBack(model *answers) []string {
	var out []string
	for _, turn := range model.told("") {
		out = append(out, turnStamp.ReplaceAllString(turn, ""))
	}
	return out
}

// The answer goes back once, as exactly the line and what is wrong with it —
// nothing of the step's instructions, of what came to it, or of its words —
// and the answer sent again is the one the loop goes on with.
func TestAnAnswerTheLoopCannotUseGoesBackToItsAgentOnce(t *testing.T) {
	model := reviewing([]string{brokenDeltas, converged}, []string{goodDeltas})
	p := reviewed()
	p.WorkflowId = "sent-back"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the run stopped on an answer its agent could put right")

	require.Equal(t, []string{sentBackFor}, sentBack(model), "the agent was not told the line and what is wrong with it, once")
	write := model.told("WRITE")
	require.Len(t, write, 2, "the answer sent again did not send the work back")
	require.Contains(t, write[1], finding, "the answer sent again is not what the next step was handed")
	require.Contains(t, write[1], "no check", "what the answer's signal carried was not handed on")
}

// Each answer has its own send-back: a step whose answer was sent back on one
// visit is sent its answer back again on a later one.
func TestEveryAnswerGetsItsOwnSendBack(t *testing.T) {
	model := reviewing([]string{brokenDeltas, brokenDeltas, converged}, []string{goodDeltas})
	p := reviewed()
	p.WorkflowId = "each-answer"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	require.NoError(t, err, "a later answer was not sent back to its agent")
	require.Equal(t, []string{sentBackFor, sentBackFor}, sentBack(model))
	require.Len(t, model.told("CHECK"), 3)
}

// Still unusable after it was sent back, the answer is the step's escalation.
// With no manager above, the run stops at the step as an escalation, saying
// the step, the line, what is wrong with it and what the step said; the
// agent's own answer stays in the record as it gave it.
func TestAnAnswerStillUnusableIsTheStepsEscalation(t *testing.T) {
	model := reviewing([]string{brokenDeltas}, []string{brokenDeltas})
	p := reviewed()
	p.WorkflowId = "still-unusable"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped, "the run did not stop")
	at := innermost(stopped.Checkpoint)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, at.GetSuspendReason())
	require.Equal(t, []string{"CHECK"}, at.GetStatePath())
	require.Len(t, model.told(""), 1, "the answer was sent back more than once")

	said, escalated := escalationOf(stopped.Checkpoint)
	require.True(t, escalated, "the stop is not an escalation")
	require.Equal(t, twiceUnread+"\n"+`{"signal":"escalate"}`, said)
	results := at.GetAllResults()
	require.Equal(t, brokenDeltas, results[len(results)-2].GetOutput(), "the agent's answer was not kept as it gave it")
	for _, r := range results[len(results)-3:] {
		require.Equal(t, "escalate", r.GetMetadata()["signal"], "the visit is not recorded as an escalation")
	}
}

// Under a manager, the escalation reaches it inside the run: nothing parks.
// The manager hears the step, the line, what is wrong with it and what the
// step said; sent back, the step is taken up with the manager's words, and an
// answer the loop cannot use there is sent back to its agent like any other.
func TestAnAnswerStillUnusableReachesTheManagerAbove(t *testing.T) {
	model := reviewing(
		[]string{brokenDeltas, brokenDeltas},
		[]string{brokenDeltas, converged},
		"close the object\n"+`{"signal":"phase"}`)

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), under("to-manager", running(reviewed())))
	require.NoError(t, err, "the run parked, or did not finish")

	manage := model.told("MANAGE")
	require.Len(t, manage, 1, "the manager did not take one turn")
	require.Contains(t, manage[0], "message in flight:\nescalated\n\n"+
		"From: PHASE › RUN › CHECK\n"+
		"This step can end with: converged → DONE · deltas → WRITE · escalate → you\n"+
		"Your word `phase` goes back to this step only. No other step runs until this one ends with its word.\n\n"+
		"It said:\n"+twiceUnread, "the manager was not given where the stop came from and what stopped the step")
	check := model.told("CHECK")
	require.Len(t, check, 2, "the step was not taken up again")
	require.Contains(t, check[1], "message in flight:\nclose the object")
	require.Len(t, model.told(""), 2, "taken up again, the step's answer was not sent back to it")
	require.Len(t, model.told("WRITE"), 1, "the child was started over")
}

// A step of the manager's own machine is no different: its unusable answer,
// sent back once, goes to the manager as the step's escalation does.
func TestAManagedMachinesOwnStepEscalatesAnUnusableAnswer(t *testing.T) {
	model := &answers{say: func(step string, turn int, told string) string {
		switch step {
		case "CHECK":
			if strings.Contains(told, "say converged") {
				return converged
			}
			return "looks fine\nbanana"
		case "MANAGE":
			return "say converged\n" + `{"signal":"check"}`
		}
		return "looks fine\nkumquat"
	}}
	p := wrap(&loomv1.StateMachinePattern{EntryState: "CHECK", LogicSeat: "MANAGE", States: map[string]*loomv1.WorkflowState{
		"CHECK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "check the tests"}},
			Vocab: []string{"converged"}, Route: map[string]*loomv1.Transition{"converged": to("DONE")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}},
		"DONE":   {Terminal: true},
	}})
	p.WorkflowId = "own-step"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the run parked, or did not finish")

	require.Equal(t, []string{"Your response format or state is invalid.\nThe line: banana\n" +
		"What is wrong with it: it is not a word this step can end with\n" +
		"Your answer stands. Send it again in the expected format."}, sentBack(model))
	manage := model.told("MANAGE")
	require.Len(t, manage, 1)
	require.Contains(t, manage[0], "message in flight:\nFrom: CHECK\n"+
		"This step can end with: converged → DONE · escalate → you\n"+
		"Your word `check` sends the loop back to this step.\n\n"+
		"It said:\nCHECK ended twice with a response the loop could not read.\n"+
		"The line: kumquat\nWhat is wrong with it: it is not a word this step can end with\nWhat it said: looks fine")
}

// A word that comes without what the loop's spec says it carries is an answer
// the loop cannot use: sent back once, and then the step's escalation — the
// loop does not go on with a word whose content is missing.
func TestAWordWithoutWhatItCarriesIsSentBackThenEscalated(t *testing.T) {
	model := &answers{say: func(string, int, string) string { return "planned it\n" + `{"signal":"planned"}` }}
	p := wrap(&loomv1.StateMachinePattern{EntryState: "PLAN",
		SignalFields: map[string]*loomv1.SignalFields{"planned": {Fields: []string{"plan_path"}}},
		States: map[string]*loomv1.WorkflowState{
			"PLAN": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "plan it"}},
				Vocab: []string{"planned"}, Route: map[string]*loomv1.Transition{"planned": to("DONE")}},
			"DONE": {Terminal: true},
		}})
	p.WorkflowId = "no-content"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped, "the loop went on with a word whose content was missing")
	require.Equal(t, []string{"Your response format or state is invalid.\n" + `The line: {"signal":"planned"}` + "\n" +
		`What is wrong with it: "planned" came without plan_path` + "\n" +
		"Your answer stands. Send it again in the expected format."}, sentBack(model))
	_, escalated := escalationOf(stopped.Checkpoint)
	require.True(t, escalated, "the stop is not an escalation")
}

// What is wrong with a last line is said as it is: a JSON object that names no
// signal, and a signal that is not one of the step's words.
func TestWhatIsWrongWithALastLine(t *testing.T) {
	require.Equal(t, `it has no "signal"`, whyUnread("done\n"+`{"status":"ok"}`))
	require.Equal(t, `"banana" is not a word this step can end with`, whyUnread("done\n"+`{"signal":"banana"}`))
	require.Equal(t, "the answer is empty", whyUnread("\n"))
}

// A step's gate comes after: the person is asked about an answer the loop can
// use, once, and not about the one that was sent back.
func TestAGatedStepsAnswerIsSentBackBeforeThePersonIsAsked(t *testing.T) {
	gate := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{{Action: loomv1.GateAction_GATE_ACTION_APPROVE}}}
	model := newRecordingLLMProvider("fine\nbanana", "fine\nconverged")
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), HITLHandler: gate})
	orch.RegisterAgent("worker", machineAgent(t, "worker", machineStore(t), model))
	p := gatedCheck("")
	p.WorkflowId = "gated-inline"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, gate.requests, 1, "the person was asked about an answer the loop could not use")
	require.Equal(t, "fine\nconverged", gate.requests[0].GetStageOutput())
	prompts := model.recordedPrompts()
	require.Len(t, prompts, 2)
	require.Contains(t, prompts[1], "The line: banana\n")
}

// gatedCheck is a machine whose one step, CHECK, has a gate; seat names its
// manager, when it has one.
func gatedCheck(seat string) *loomv1.WorkflowPattern {
	sm := &loomv1.StateMachinePattern{EntryState: "CHECK", LogicSeat: seat, States: map[string]*loomv1.WorkflowState{
		"CHECK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "check the tests"}},
			Vocab: []string{"converged"}, Route: map[string]*loomv1.Transition{"converged": to("DONE")},
			HitlGate: &loomv1.HITLGate{PromptTemplate: "Review the check"}},
		"DONE": {Terminal: true},
	}}
	if seat != "" {
		sm.States[seat] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}}
	}
	return wrap(sm)
}

// A gate that waits for its person does not let an unusable answer past the
// rule: the answer is sent back before the run waits, and still unusable it is
// the escalation the person is asked about and the manager then hears — never
// a stop on a word nobody can read.
func TestAGateThatWaitsDoesNotLetAnUnusableAnswerPast(t *testing.T) {
	model := &answers{say: func(step string, turn int, told string) string {
		switch step {
		case "CHECK":
			if strings.Contains(told, "say converged") {
				return converged
			}
			return "looks fine\nbanana"
		case "MANAGE":
			return "say converged\n" + `{"signal":"check"}`
		}
		return "kumquat"
	}}
	orch := orchestratorWith(t, model)
	p := gatedCheck("MANAGE")
	p.WorkflowId = "gated-waits"
	approve := &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE}

	_, err := orch.ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_GATE, stopped.Checkpoint.GetSuspendReason())
	require.Len(t, model.told(""), 1, "the answer was not sent back before the run waited at the gate")
	require.Contains(t, stopped.Checkpoint.GetPendingGate().GetStageOutput(),
		"CHECK ended twice with a response the loop could not read.\nThe line: kumquat\n"+
			"What is wrong with it: it is not a word this step can end with\nWhat it said: looks fine",
		"the person was not asked about the escalation, with what the step said when its answer was sent back")

	_, err = orch.ResumeWorkflow(context.Background(), p, stopped.Checkpoint, approve)
	require.ErrorAs(t, err, &stopped, "the step's answer, put right, did not come to its gate")
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_GATE, stopped.Checkpoint.GetSuspendReason())
	require.Len(t, model.told("MANAGE"), 1, "the manager did not hear the escalation")
	require.Equal(t, converged, stopped.Checkpoint.GetPendingGate().GetStageOutput())

	_, err = orch.ResumeWorkflow(context.Background(), p, stopped.Checkpoint, approve)
	require.NoError(t, err)
}

// What the step said is the step's own: an agent that said nothing above its
// line either time is not given the words of the step before it.
func TestAStepThatSaidNothingIsNotGivenAnotherStepsWords(t *testing.T) {
	model := reviewing([]string{"banana"}, []string{"kumquat"})
	p := reviewed()
	p.WorkflowId = "said-nothing"

	_, err := orchestratorWith(t, model).ExecutePattern(context.Background(), p)
	var stopped *WorkflowSuspended
	require.ErrorAs(t, err, &stopped)
	said, escalated := escalationOf(stopped.Checkpoint)
	require.True(t, escalated)
	require.Equal(t, "CHECK ended twice with a response the loop could not read.\nThe line: kumquat\n"+
		"What is wrong with it: it is not a word this step can end with\n"+`{"signal":"escalate"}`, said)
}
