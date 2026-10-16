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
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// answers is a seat's model that answers each turn from the step it is at,
// how many turns that step has had, and what it was just told. Every prompt
// is kept under its step.
type answers struct {
	mu      sync.Mutex
	say     func(step string, turn int, told string) string
	prompts map[string][]string
	// fails makes a turn fail instead of answering, when it returns an error.
	fails func(step string, turn int) error
}

func (m *answers) Name() string  { return "answers" }
func (m *answers) Model() string { return "answers" }
func (m *answers) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	last := msgs[len(msgs)-1].Content
	step := ""
	if found := atStep.FindStringSubmatch(last); found != nil {
		step = found[1]
	}
	if m.prompts == nil {
		m.prompts = map[string][]string{}
	}
	turn := len(m.prompts[step])
	m.prompts[step] = append(m.prompts[step], last)
	if m.fails != nil {
		if err := m.fails(step, turn); err != nil {
			return nil, err
		}
	}
	return &llmtypes.LLMResponse{Content: m.say(step, turn, last), StopReason: "end_turn",
		Usage: llmtypes.Usage{TotalTokens: 10, CostUSD: turnCost}}, nil
}

// turnCost is what every turn of an answers seat costs.
const turnCost = 0.5

func (m *answers) told(step string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts[step]...)
}

// writes is a machine of two agent steps: PREP, then WRITE, which is given
// the words in vocab and routed on tests-ready alone.
func writes(instructions string, vocab ...string) *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: "PREP", States: map[string]*loomv1.WorkflowState{
		"PREP": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "prepare"}},
			Vocab: []string{"ready"}, Route: map[string]*loomv1.Transition{"ready": to("WRITE")}},
		"WRITE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: instructions}},
			Vocab: append([]string{"tests-ready"}, vocab...), Route: map[string]*loomv1.Transition{"tests-ready": to("DONE")}},
		"DONE": {Terminal: true},
	}})
}

// running is a machine with no manager that runs one child and ends.
func running(child *loomv1.WorkflowPattern) *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
		"RUN":  {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}}, Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE": {Terminal: true},
	}})
}

// overEach is a machine with no manager whose PLAN names the items and whose
// EACH runs the child once for each, one at a time.
func overEach(child *loomv1.WorkflowPattern) *loomv1.StateMachinePattern {
	return &loomv1.StateMachinePattern{EntryState: "PLAN", States: map[string]*loomv1.WorkflowState{
		"PLAN": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "plan it"}},
			Vocab: []string{"planned"}, Route: map[string]*loomv1.Transition{"planned": to("EACH")}},
		"EACH": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child, ItemsKey: "items", MaxParallel: 1}},
			Route: map[string]*loomv1.Transition{"fanned": to("DONE")}},
		"DONE": {Terminal: true},
	}}
}

// under is a machine whose manager is MANAGE and whose one step, PHASE, runs
// the child.
func under(id string, child *loomv1.WorkflowPattern) *loomv1.WorkflowPattern {
	p := wrap(&loomv1.StateMachinePattern{EntryState: "PHASE", LogicSeat: "MANAGE", States: map[string]*loomv1.WorkflowState{
		"PHASE":  {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}}, Route: map[string]*loomv1.Transition{"done": to("FIN")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}},
		"FIN":    {Terminal: true},
	}})
	p.WorkflowId = id
	return p
}

func orchestratorWith(t *testing.T, model *answers) *Orchestrator {
	t.Helper()
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("worker", machineAgent(t, "worker", machineStore(t), model))
	return orch
}

const (
	cannotWrite = "the code is not here. This step is WRITE.\n" + `{"signal":"escalate"}`
	whereItIs   = "the code is in the unit"
)

// codeMissing is a run in which WRITE cannot do its work until it is told
// where the code is, and every other step does its own. manage is what the
// manager says, turn by turn.
func codeMissing(manage ...string) *answers {
	return &answers{say: func(step string, turn int, told string) string {
		switch step {
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "PLAN":
			return "two of them\n" + `{"signal":"planned","items":"a,b"}`
		case "WRITE":
			if strings.Contains(told, whereItIs) {
				return "written\n" + `{"signal":"tests-ready"}`
			}
			return cannotWrite
		case "MANAGE":
			return manage[min(turn, len(manage)-1)]
		}
		return "?"
	}}
}

// A step deep in the machines a managed machine runs that cannot do its work
// reaches that machine's manager, inside the run: nothing parks. The manager
// is given what the step said; naming the step of its own machine that
// delegated, it sends the run back to the step that escalated — not to the
// start of the child it sits in — with the manager's words in front of it as
// what came back, and the run finishes. The children are the ones that were
// started, on the budgets they were given: none is started again, and what
// each turn cost is counted once.
func TestADeepStepsEscalationReachesTheManagerAboveAndTheStepIsSentBack(t *testing.T) {
	model := codeMissing(whereItIs + "\n" + `{"signal":"phase"}`)
	seen := &starts{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: seen})
	orch.RegisterAgent("worker", machineAgent(t, "worker", machineStore(t), model))
	p := under("deep", running(writes("write the tests")))
	// The child is promised the whole of a ceiling that, once the run has
	// spent anything, no longer leaves the least a child may be given.
	p.GetStateMachine().MaxCostUsd = 10
	p.GetStateMachine().States["PHASE"].GetDelegate().MinBudgetUsd = 10

	result, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the run parked, or did not finish")
	require.Equal(t, []string{"deep-PHASE", "deep-PHASE-RUN"}, seen.ids, "a child was started again")
	require.InDelta(t, 4*turnCost, result.GetCost().GetTotalCostUsd(), 1e-9, "four turns were not each counted once")

	manage, write := model.told("MANAGE"), model.told("WRITE")
	require.Len(t, manage, 1, "the manager did not take one turn")
	require.Contains(t, manage[0], "message in flight:\nescalated\n\n"+
		"From: PHASE › RUN › WRITE\n"+
		"This step can end with: tests-ready → DONE · escalate → you\n"+
		"Your word `phase` goes back to this step only. No other step runs until this one ends with its word.\n\n"+
		"It said:\nthe code is not here. This step is WRITE.",
		"the manager was not given what the step said")
	require.Contains(t, manage[0], "- `phase` → PHASE: a phase; sent back after it stopped, it takes up at the step that stopped")
	require.Len(t, model.told("PREP"), 1, "the child was started over")
	require.Len(t, write, 2, "the step that escalated did not run again")
	require.Contains(t, write[1], "Step WRITE\n\nwrite the tests",
		"the step that escalated was not given its instructions")
	require.Contains(t, write[1], "message in flight:\n"+whereItIs,
		"the step did not get the manager's words as what came back")
	require.NotContains(t, write[1], "REVISION FEEDBACK", "the manager's words arrived as a person's revision")
}

// The escalation passes through a set of children on its way up: the child
// that escalated is the one taken up again, at its step, and the children of
// the set that had finished are not run again.
func TestADeepEscalationPassesThroughASetOfChildren(t *testing.T) {
	model := codeMissing(whereItIs + "\n" + `{"signal":"phase"}`)
	model.say = forB(model.say)
	orch := orchestratorWith(t, model)

	_, err := orch.ExecutePattern(context.Background(), under("through", wrap(overEach(writes("write the tests for {item}")))))
	require.NoError(t, err, "the run parked, or did not finish")

	require.Len(t, model.told("MANAGE"), 1)
	require.Contains(t, model.told("MANAGE")[0], "From: PHASE › EACH (b) › WRITE\n",
		"the manager was not told which child of the set between, at which step, raised it")
	require.Len(t, model.told("PLAN"), 1, "the machine between was started over")
	require.Len(t, model.told("PREP"), 2, "a child of the set was started over")
	write := model.told("WRITE")
	require.Len(t, write, 3, "a's step, and b's twice")
	require.Contains(t, write[0], "for a")
	require.Contains(t, write[1], "for b")
	require.Contains(t, write[2], "message in flight:\n"+whereItIs)
}

// forB makes WRITE do its work for every item but b, which cannot until told.
func forB(say func(string, int, string) string) func(string, int, string) string {
	return func(step string, turn int, told string) string {
		if step == "WRITE" && !strings.Contains(told, "for b") && !strings.Contains(told, whereItIs) {
			return "written\n" + `{"signal":"tests-ready"}`
		}
		return say(step, turn, told)
	}
}

// A managed machine's own set of children is no different: the manager is
// told which child escalated and what it said, and the word for the step
// that runs the set takes that child up again and leaves the finished alone.
func TestAManagedMachinesSetOfChildrenEscalatesToItsManager(t *testing.T) {
	model := codeMissing(whereItIs + "\n" + `{"signal":"each"}`)
	model.say = forB(model.say)
	orch := orchestratorWith(t, model)
	sm := overEach(writes("write the tests for {item}"))
	sm.LogicSeat = "MANAGE"
	sm.States["MANAGE"] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}}
	p := wrap(sm)
	p.WorkflowId = "set"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the run parked, or did not finish")

	manage := model.told("MANAGE")
	require.Len(t, manage, 1)
	require.Contains(t, manage[0], "message in flight:\nb=escalated\n\nFrom: EACH (b) › WRITE\n", "the manager was not told which child escalated")
	require.Contains(t, manage[0], "\n\nIt said:\nthe code is not here.")
	require.Contains(t, manage[0], "- `each` → EACH: a phase; sent back after it stopped, it takes up at the step that stopped")
	require.Len(t, model.told("PREP"), 2, "a child of the set was started over")
	require.Len(t, model.told("WRITE"), 3, "a's step, and b's twice")
}

// The nearest manager above a step hears it. A child machine with a manager
// of its own settles what it can; what that manager escalates goes to the
// manager of the machine above, whose word sends the child's manager on.
func TestTheNearestManagerHearsAStepAndTheOneAboveHearsThatManager(t *testing.T) {
	model := &answers{say: func(step string, turn int, told string) string {
		switch step {
		case "DO":
			if turn == 0 {
				return "no branch named. This step is DO.\n" + `{"signal":"escalate"}`
			}
			return "did it\n" + `{"signal":"done"}`
		case "CHECK":
			return "fine\n" + `{"signal":"ok"}`
		case "MANAGE":
			if turn == 0 {
				return "the branch is not mine to choose\n" + `{"signal":"escalate"}`
			}
			return "use the branch you were given\n" + `{"signal":"do"}`
		case "BOSS":
			return "branch: main\n" + `{"signal":"phase"}`
		}
		return "?"
	}}
	orch := orchestratorWith(t, model)
	p := wrap(&loomv1.StateMachinePattern{EntryState: "PHASE", LogicSeat: "BOSS", States: map[string]*loomv1.WorkflowState{
		"PHASE": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: wrap(managed("MANAGE"))}}, Route: map[string]*loomv1.Transition{"fin": to("FIN")}},
		"BOSS":  {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}},
		"FIN":   {Terminal: true},
	}})
	p.WorkflowId = "two-managers"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the run parked, or did not finish")

	manage, boss := model.told("MANAGE"), model.told("BOSS")
	require.Len(t, boss, 1, "the manager above did not take one turn")
	require.Contains(t, boss[0], "the branch is not mine to choose", "the manager above was not given what the child's manager said")
	require.NotContains(t, boss[0], "no branch named", "the manager above was given the step's words, which the child's manager had heard")
	require.Len(t, manage, 2)
	require.Contains(t, manage[0], "no branch named")
	require.Contains(t, manage[1], "message in flight:\nbranch: main")
	require.Len(t, model.told("DO"), 2)
}

// A manager that cannot settle a deep escalation escalates in its turn, and
// the run parks at the manager as it does for a step of the manager's own
// machine. Each loop below stands where it stopped on its own record; the run
// notes only which of its children it reported. Stored, read back and resumed
// the way a host resumes a run — given its variables again — the manager is
// told the person's answer, names the step, and the child goes on from the
// step that escalated.
func TestAManagerThatEscalatesParksTheRunWithTheChildKept(t *testing.T) {
	model := codeMissing("only the person knows where the code is\n"+`{"signal":"escalate"}`, whereItIs+"\n"+`{"signal":"phase"}`)
	orch := orchestratorWith(t, model)
	// The machine between names its delegating state as the one above does.
	spec := func() *loomv1.WorkflowPattern {
		mid := running(writes("write the tests"))
		sm := mid.GetStateMachine()
		sm.States["PHASE"], sm.EntryState = sm.States["RUN"], "PHASE"
		delete(sm.States, "RUN")
		return under("parks-deep", mid)
	}

	_, err := orch.ExecutePattern(context.Background(), spec())
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked, "the manager's escalation did not park the run")
	require.Equal(t, []string{"MANAGE"}, parked.Checkpoint.GetStatePath())
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, parked.Checkpoint.GetSuspendReason())
	require.Nil(t, parked.Checkpoint.GetNestedCheckpoint())
	require.Equal(t, "item:", parked.Checkpoint.GetVars()[askedKey("PHASE")], "the run does not note that its phase's child stopped and was heard")
	for id, at := range map[string]string{"parks-deep-PHASE": "PHASE", "parks-deep-PHASE-PHASE": "WRITE"} {
		place, ok := orch.places.PlaceOf(context.Background(), id)
		require.True(t, ok, "%s has no record", id)
		require.Equal(t, at, place.At(), "%s's own record is not where it stopped", id)
		require.False(t, place.Ended, id)
	}

	stored, err := proto.Marshal(parked.Checkpoint)
	require.NoError(t, err)
	ckpt := &loomv1.WorkflowCheckpoint{}
	require.NoError(t, proto.Unmarshal(stored, ckpt))

	_, err = orch.ResumeWorkflow(WithRunVars(context.Background(), ckpt.GetVars()), spec(), ckpt,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "it is in the unit"})
	require.NoError(t, err, "resumed with the person's answer, the run did not finish")
	require.Len(t, model.told("MANAGE"), 2)
	require.Len(t, model.told("PREP"), 1, "the child was started over")
	require.Len(t, model.told("WRITE"), 2)
}

// Only a stop on a word is the manager's. A child that stops for a person —
// here a word its loop says goes straight to the scope that owns the run —
// parks the whole run where it stopped, and the manager takes no turn.
func TestAChildThatStopsOnSomethingElseParksTheRun(t *testing.T) {
	model := &answers{say: func(step string, _ int, _ string) string {
		if step == "PREP" {
			return "prepared\n" + `{"signal":"ready"}`
		}
		return "the suite is red\n" + `{"signal":"blocked"}`
	}}
	orch := orchestratorWith(t, model)

	_, err := orch.ExecutePattern(context.Background(), under("not-escalation", running(forThePerson(writes("write the tests", "blocked"), "blocked"))))
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_NESTED, parked.Checkpoint.GetSuspendReason())
	require.Equal(t, []string{"PHASE"}, parked.Checkpoint.GetStatePath())
	require.Equal(t, []string{"WRITE"}, innermost(parked.Checkpoint).GetStatePath())
	require.Empty(t, model.told("MANAGE"), "the manager took a turn on a stop that is a person's")
}

// An answer to a child that cannot be taken up — its loop no longer has the
// step it stands at — is written on the child's record and not lost. The run
// waits at the state that runs it, on the error; once the child's loop has
// the step again, resuming the run takes the child up with the answer.
func TestAnAnswerToAChildThatCannotBeTakenUpWaitsOnItsRecord(t *testing.T) {
	model := codeMissing("unused")
	orch := orchestratorWith(t, model)
	spec := func(whole bool) *loomv1.WorkflowPattern {
		child := writes("write the tests")
		if !whole {
			delete(child.GetStateMachine().States, "WRITE")
			child.GetStateMachine().States["PREP"].Route["ready"] = to("DONE")
		}
		p := running(child)
		p.WorkflowId = "refused"
		return p
	}

	_, err := orch.ExecutePattern(context.Background(), spec(true))
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, parked.Checkpoint.GetSuspendReason())
	require.Equal(t, []string{"RUN"}, parked.Checkpoint.GetStatePath())

	_, err = orch.ResumeWorkflow(context.Background(), spec(false), parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: whereItIs})
	var waits *WorkflowSuspended
	require.ErrorAs(t, err, &waits, "the run did not wait where the child could not be taken up")
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, waits.Checkpoint.GetSuspendReason())
	require.Contains(t, waits.Checkpoint.GetError(), "cannot be taken up where it stands")
	require.Len(t, model.told("WRITE"), 1)
	place, ok := orch.places.PlaceOf(context.Background(), "refused-RUN")
	require.True(t, ok)
	require.Equal(t, "WRITE", place.At())
	require.Equal(t, whereItIs, place.Message, "the answer is not on the child's record")

	_, err = orch.ResumeWorkflow(context.Background(), spec(true), waits.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	write := model.told("WRITE")
	require.Len(t, write, 2)
	require.Contains(t, write[1], "message in flight:\n"+whereItIs, "the answer was lost")
	require.Len(t, model.told("PREP"), 1, "the child was started over")
}

// What the engine keeps for a machine's own walk is not its children's to
// inherit. A host resumes a run by giving it its variables again; a child
// started after that, whose entry state has the name of a state its parent
// has finished, still runs its own child.
func TestAMachinesOwnBookkeepingIsNotItsChildrensToInherit(t *testing.T) {
	model := &answers{say: func(step string, turn int, _ string) string {
		switch step {
		case "ASK":
			if turn == 0 {
				return "which way? This step is ASK.\n" + `{"signal":"escalate"}`
			}
			return "that way\n" + `{"signal":"ok"}`
		case "FIRST", "SECOND":
			return "did it\n" + `{"signal":"ok"}`
		}
		return "?"
	}}
	orch := orchestratorWith(t, model)
	does := func(step string) *loomv1.WorkflowPattern {
		return wrap(&loomv1.StateMachinePattern{EntryState: step, States: map[string]*loomv1.WorkflowState{
			step:   {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "do it"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("DONE")}},
			"DONE": {Terminal: true},
		}})
	}
	spec := func() *loomv1.WorkflowPattern {
		p := wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
			"RUN":  {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: does("FIRST")}}, Route: map[string]*loomv1.Transition{"done": to("ASK")}},
			"ASK":  {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "ask"}}, Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("NEXT")}},
			"NEXT": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: running(does("SECOND"))}}, Route: map[string]*loomv1.Transition{"done": to("DONE")}},
			"DONE": {Terminal: true},
		}})
		p.WorkflowId = "own"
		return p
	}

	_, err := orch.ExecutePattern(context.Background(), spec())
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)
	require.Equal(t, []string{"ASK"}, parked.Checkpoint.GetStatePath())

	_, err = orch.ResumeWorkflow(WithRunVars(context.Background(), parked.Checkpoint.GetVars()), spec(), parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "that way"})
	require.NoError(t, err)
	require.Len(t, model.told("SECOND"), 1, "a child read its parent's finished walk as its own and ran nothing")
}

// A child taken up by its manager's word that then ends, not done, is the
// manager's again, as any child that ends so is; sent back once more, it is
// entered at the step that ended it. The kept child was spent by taking it up.
func TestAChildTakenUpThatThenEndsIsSentBackToTheStepThatEndedIt(t *testing.T) {
	model := &answers{say: func(step string, turn int, _ string) string {
		switch step {
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "WRITE":
			return []string{cannotWrite, "still cannot\n" + `{"signal":"give-up"}`, "written\n" + `{"signal":"tests-ready"}`}[min(turn, 2)]
		}
		return "try again\n" + `{"signal":"phase"}`
	}}
	orch := orchestratorWith(t, model)
	child := writes("write the tests", "give-up")
	child.GetStateMachine().States["WRITE"].Route["give-up"] = to("BLOCKED")
	child.GetStateMachine().States["BLOCKED"] = &loomv1.WorkflowState{Terminal: true}

	_, err := orch.ExecutePattern(context.Background(), under("twice", child))
	require.NoError(t, err, "the run parked, or did not finish")

	manage, write := model.told("MANAGE"), model.told("WRITE")
	require.Len(t, manage, 2)
	require.Contains(t, manage[1], "message in flight:\nBLOCKED\n\nstill cannot")
	require.Len(t, write, 3)
	require.Contains(t, write[2], "Step WRITE\n")
	require.Contains(t, write[2], "message in flight:\ntry again")
	require.Len(t, model.told("PREP"), 1, "the child was started over")
}

// A new list is a fresh walk: a manager that answers a set's escalation with
// other items has the set run those. The child that had escalated is not on
// it: it stands where it stopped, on its own record, and is not run.
func TestAStoppedChildNotOnANewListIsNotRun(t *testing.T) {
	model := codeMissing("leave b; do c\n" + `{"signal":"each","items":"c"}`)
	model.say = forB(model.say)
	orch := orchestratorWith(t, model)
	sm := overEach(writes("write the tests for {item}"))
	sm.LogicSeat = "MANAGE"
	sm.States["MANAGE"] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}}
	p := wrap(sm)
	p.WorkflowId = "new-list"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)

	write := model.told("WRITE")
	require.Len(t, write, 3, "a, b and c")
	require.Contains(t, write[2], "Step WRITE\n", "c was not started as itself")
	require.Contains(t, write[2], "for c")
}

// A step that escalates again after its manager sent it back reaches the
// manager again, and is sent back again: each escalation is kept in place of
// the last, and what the child spent is counted once however often it stops.
func TestAStepThatEscalatesAgainReachesTheManagerAgain(t *testing.T) {
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
	p := under("again", running(writes("write the tests")))
	p.GetStateMachine().MaxCostUsd = 10

	result, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the run parked, or did not finish")

	manage, write := model.told("MANAGE"), model.told("WRITE")
	require.Len(t, manage, 2)
	require.Contains(t, manage[1], "nor there.")
	require.Len(t, write, 3)
	require.Contains(t, write[1], "message in flight:\nlook in the unit")
	require.Contains(t, write[2], "message in flight:\nlook in the pool")
	require.Len(t, model.told("PREP"), 1, "the child was started over")
	require.InDelta(t, 6*turnCost, result.GetCost().GetTotalCostUsd(), 1e-9, "six turns were not each counted once")
}

// A person's answer is for the park it answers and no other. A run parked
// inside a child for a person is resumed with their answer in front of the
// step that stopped; when that step then escalates and its manager sends it
// back, it is the manager's words it gets, not the person's answer again.
func TestAPersonsAnswerIsForTheParkItAnswers(t *testing.T) {
	model := &answers{say: func(step string, turn int, _ string) string {
		switch step {
		case "PREP":
			return "prepared\n" + `{"signal":"ready"}`
		case "WRITE":
			return []string{"the suite is red\n" + `{"signal":"blocked"}`, cannotWrite, "written\n" + `{"signal":"tests-ready"}`}[min(turn, 2)]
		}
		return whereItIs + "\n" + `{"signal":"phase"}`
	}}
	orch := orchestratorWith(t, model)
	spec := func() *loomv1.WorkflowPattern {
		return under("answered", running(forThePerson(writes("write the tests", "blocked"), "blocked")))
	}

	_, err := orch.ExecutePattern(context.Background(), spec())
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked)

	_, err = orch.ResumeWorkflow(context.Background(), spec(), parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "the suite is red on main too"})
	require.NoError(t, err, "the run parked again, or did not finish")

	write := model.told("WRITE")
	require.Len(t, write, 3)
	require.Contains(t, write[1], "REVISION FEEDBACK (from human reviewer)")
	require.Contains(t, write[1], "the suite is red on main too")
	require.Contains(t, write[2], "message in flight:\n"+whereItIs)
	require.NotContains(t, write[2], "the suite is red on main too", "the person's answer was given a second time")
	require.Len(t, model.told("MANAGE"), 1)
}

// forThePerson makes a child loop's words stops for the person: they park the
// whole run, for the scope that owns it.
func forThePerson(p *loomv1.WorkflowPattern, words ...string) *loomv1.WorkflowPattern {
	p.GetStateMachine().BubbleSignals = append(p.GetStateMachine().GetBubbleSignals(), words...)
	return p
}
