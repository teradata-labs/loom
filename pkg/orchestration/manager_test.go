// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"regexp"
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

var atStep = regexp.MustCompile(`(?:Step|Back at step) ([A-Z_]+)`)

// byStep answers each turn from what its step is scripted to say, in order;
// the last answer of a step repeats. Every prompt is kept under its step.
type byStep struct {
	mu      sync.Mutex
	says    map[string][]string
	prompts map[string][]string
}

func (m *byStep) Name() string  { return "by-step" }
func (m *byStep) Model() string { return "by-step" }
func (m *byStep) Chat(_ context.Context, msgs []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
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
	n := len(m.prompts[step])
	m.prompts[step] = append(m.prompts[step], last)
	script := m.says[step]
	return &llmtypes.LLMResponse{Content: script[min(n, len(script)-1)], StopReason: "end_turn"}, nil
}

func (m *byStep) told(step string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts[step]...)
}

// managed is a machine with a manager: DO, which CHECK passes, and MANAGE.
func managed(seat string) *loomv1.StateMachinePattern {
	return &loomv1.StateMachinePattern{EntryState: "DO", LogicSeat: seat, States: map[string]*loomv1.WorkflowState{
		"DO": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "do it"}},
			Route: map[string]*loomv1.Transition{"done": to("CHECK")}},
		"CHECK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "check it"}},
			Vocab: []string{"ok"}, Route: map[string]*loomv1.Transition{"ok": to("FIN")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker", Instructions: "settle it"}}},
		"FIN":    {Terminal: true},
	}}
}

// A step of a managed machine that cannot go on goes to the manager, inside
// the run; the manager settles it and names the step, which runs again with
// the manager's words in front of it, and the machine walks on. The step is
// told its own name; the manager is given the map of the machine, and is not
// told it is a step — how a manager works is the host's to tell it, not the
// engine's. None of it is in the spec:
// the engine works it out when it takes the machine up, and the spec is left
// as its author wrote it.
func TestAManagedMachinesStepEscalatesToItsManagerAndIsSentBack(t *testing.T) {
	store := machineStore(t)
	model := &byStep{says: map[string][]string{
		"DO":     {"no branch named. This step is DO.\n" + `{"signal":"escalate"}`, "did it on main\n" + `{"signal":"done"}`},
		"CHECK":  {"fine\n" + `{"signal":"ok"}`},
		"MANAGE": {"branch: main\n" + `{"signal":"do"}`},
	}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("worker", machineAgent(t, "worker", store, model))
	spec := managed("MANAGE")
	p := wrap(spec)
	p.WorkflowId = "managed"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)

	do, manage := model.told("DO"), model.told("MANAGE")
	require.Len(t, do, 2, "the step did not run again")
	require.Equal(t, 1, strings.Count(do[0], "This step is DO."), "the step was not told its name once")
	require.Contains(t, do[1], "branch: main", "the step did not get the manager's words")
	require.Len(t, manage, 1)
	require.Contains(t, manage[0], "no branch named", "the manager was not given what the step said")
	require.NotContains(t, manage[0], "HOW A MANAGER WORKS", "the engine told the manager how to work: that is the host's to say")
	require.Contains(t, manage[0], "- `do` → DO: do it")
	require.Contains(t, manage[0], "- `check` → CHECK: check it")
	require.NotContains(t, manage[0], "This step is MANAGE", "the manager was told it is a step")
	require.Contains(t, manage[0], `"check", "do", "escalate"`, "the manager's words are not the steps' names and the way out")

	require.Nil(t, spec.GetStates()["DO"].GetRoute()["escalate"], "the spec was written into")
	require.Equal(t, "settle it", spec.GetStates()["MANAGE"].GetAgent().GetInstructions(), "the spec was written into")
}

// A machine run as a child is taken up like any machine: its manager is
// seated though the machine that runs it has none.
func TestAChildMachinesManagerIsSeatedWhereverTheChildRuns(t *testing.T) {
	store := machineStore(t)
	model := &byStep{says: map[string][]string{
		"DO":     {"no branch. This step is DO.\n" + `{"signal":"escalate"}`, "done on main\n" + `{"signal":"done"}`},
		"CHECK":  {"fine\n" + `{"signal":"ok"}`},
		"MANAGE": {"branch: main\n" + `{"signal":"do"}`},
	}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("worker", machineAgent(t, "worker", store, model))
	runs := func(child *loomv1.WorkflowPattern) *loomv1.WorkflowPattern {
		return wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
			"RUN": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}},
				Route: map[string]*loomv1.Transition{"fin": to("END"), "end": to("END")}},
			"END": {Terminal: true},
		}})
	}
	p := runs(runs(wrap(managed("MANAGE"))))
	p.WorkflowId = "outer"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "the child's step did not reach the child's manager")
	require.Len(t, model.told("MANAGE"), 1)
	require.Len(t, model.told("DO"), 2)
}

// A manager that is not a state of its machine, or not an agent's, stops the
// machine before it runs anything.
func TestAMachineWhoseManagerCannotBeSeatedDoesNotRun(t *testing.T) {
	for seat, why := range map[string]string{"NOPE": "is not a state of this loop", "FIN": "is not an agent state"} {
		store := machineStore(t)
		model := &byStep{says: map[string][]string{"DO": {"x\n" + `{"signal":"done"}`}}}
		orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
		orch.RegisterAgent("worker", machineAgent(t, "worker", store, model))
		p := wrap(managed(seat))
		p.WorkflowId = "bad-" + seat
		_, err := orch.ExecutePattern(context.Background(), p)
		require.ErrorContains(t, err, why, seat)
		require.Empty(t, model.told("DO"), "%s: the machine ran", seat)
	}
}

// A managed machine that parks is taken up again on its spec as written: the
// manager's seating is not part of what identifies the run.
func TestAParkedManagedMachineIsTakenUpOnItsSpec(t *testing.T) {
	store := machineStore(t)
	model := &byStep{says: map[string][]string{
		"DO":     {"no branch. This step is DO.\n" + `{"signal":"escalate"}`, "done on main\n" + `{"signal":"done"}`},
		"CHECK":  {"fine\n" + `{"signal":"ok"}`},
		"MANAGE": {"only the person knows the branch\n" + `{"signal":"escalate"}`, "branch: main\n" + `{"signal":"do"}`},
	}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("worker", machineAgent(t, "worker", store, model))
	p := wrap(managed("MANAGE"))
	p.WorkflowId = "parks"

	_, err := orch.ExecutePattern(context.Background(), p)
	var parked *WorkflowSuspended
	require.ErrorAs(t, err, &parked, "the manager's own escalation did not park the machine")
	require.Equal(t, []string{"MANAGE"}, parked.Checkpoint.GetStatePath())

	again := wrap(managed("MANAGE"))
	again.WorkflowId = "parks"
	_, err = orch.ResumeWorkflow(context.Background(), again, parked.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "main"})
	require.NoError(t, err)
	manage, do := model.told("MANAGE"), model.told("DO")
	require.Len(t, manage, 2, "the manager did not take the person's answer")
	require.Contains(t, manage[1], "main")
	require.Len(t, do, 2, "the step was not sent back by the manager's word")
	require.Contains(t, do[1], "branch: main")
	require.Len(t, model.told("CHECK"), 1, "the machine did not walk on to its end")
}

// A manager's own words stay its words. A manager whose spec routes a word of
// its own and declares no vocabulary is offered that word beside the steps'
// names and the way out.
func TestAManagersOwnRoutedWordsStayInItsVocabulary(t *testing.T) {
	spec := managed("MANAGE")
	spec.States["MANAGE"].Route = map[string]*loomv1.Transition{"abandon": to("FIN")}
	seated, err := Seated(spec, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"abandon", "check", "do", "escalate"}, stateVocab(seated.GetStates()["MANAGE"]))
	require.Empty(t, spec.GetStates()["MANAGE"].GetVocab(), "the spec was written into")
}
