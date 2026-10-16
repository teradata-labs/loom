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
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
)

// fanTools answers the item-listing tool and each child's work, and records the
// directory every child ran in.
type fanTools struct {
	mu      sync.Mutex
	items   string
	dirs    map[string]string
	budgets map[string]string
	parked  map[string]bool
	ran     []string
	told    map[string]string
}

func (f *fanTools) Execute(_ context.Context, name string, params map[string]interface{}) (*shuttle.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch name {
	case "list_repos":
		return &shuttle.Result{Success: true, Data: fmt.Sprintf(
			`{"signal": "listed", "repos": %q}`, f.items)}, nil

	case "review":
		dir, _ := params[ParamDir].(string)
		item := dir[strings.LastIndex(dir, "/")+1:]
		f.ran = append(f.ran, item)
		f.dirs[item] = dir
		if told, _ := params["told"].(string); told != "" {
			f.told[item] = told
		}
		if f.parked[item] {
			// This child needs a human this round.
			return &shuttle.Result{Success: true, Data: "needs a look\nhitl"}, nil
		}
		return &shuttle.Result{Success: true, Data: "reviewed\nclear"}, nil
	}
	return &shuttle.Result{Success: true, Data: ""}, nil
}

func newFanTools(items string, parked ...string) *fanTools {
	f := &fanTools{items: items, dirs: map[string]string{}, budgets: map[string]string{}, parked: map[string]bool{}, told: map[string]string{}}
	for _, p := range parked {
		f.parked[p] = true
	}
	return f
}

// repoMachine is the child: review this repo, then finish CLEAR or park.
func repoMachine() *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{
		EntryState: "REVIEW",
		States: map[string]*loomv1.WorkflowState{
			"REVIEW": {
				Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "review"}},
				Vocab: []string{"clear", "hitl"},
				Route: map[string]*loomv1.Transition{"clear": to("CLEAR")},
			},
			"CLEAR": {Terminal: true},
		},
	})
}

func fanPattern(items string) *loomv1.WorkflowPattern {
	p := wrap(&loomv1.StateMachinePattern{
		EntryState: "LIST",
		Dir:        "set",
		States: map[string]*loomv1.WorkflowState{
			"LIST": {
				Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "list_repos"}},
				Vocab: []string{"listed"},
				Route: map[string]*loomv1.Transition{"listed": to("FAN")},
			},
			"FAN": {
				Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{
					Workflow:    repoMachine(),
					ItemsKey:    "repos",
					DirTemplate: "repos/{item}",
				}},
				Vocab: []string{FannedSignal},
				Route: map[string]*loomv1.Transition{FannedSignal: to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	p.WorkflowId = "fan"
	return p
}

// One child or many is the same step, and each child runs somewhere of its own.
func TestDelegate_RunsOnePerItemInItsOwnDirectory(t *testing.T) {
	t.Parallel()

	tools := newFanTools("alpha\nbeta\ngamma")
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})

	result, err := orch.ExecutePattern(context.Background(), fanPattern("alpha\nbeta\ngamma"))
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])

	assert.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, tools.ran)
	assert.Equal(t, "set/repos/alpha", tools.dirs["alpha"],
		"a child works on its own part of the world, not its parent's")
	assert.Equal(t, "set/repos/gamma", tools.dirs["gamma"])
}

// A parked child parks the whole set — and the ones that finished stay
// finished, so resuming costs only what actually still has to run.
func TestDelegate_ParkedChildParksTheSetAndFinishedChildrenAreNotRerun(t *testing.T) {
	t.Parallel()

	tools := newFanTools("alpha\nbeta\ngamma", "beta")
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})
	pattern := fanPattern("alpha\nbeta\ngamma")

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, suspended.Checkpoint.SuspendReason)
	assert.Equal(t, []string{"FAN"}, suspended.Checkpoint.StatePath)

	// The two that finished are recorded, so the parent knows what is left.
	outcomes := suspended.Checkpoint.Vars["__outcomes:FAN"]
	assert.Contains(t, outcomes, "alpha=CLEAR")
	assert.Contains(t, outcomes, "gamma=CLEAR")
	assert.NotContains(t, outcomes, "beta=")

	// beta stops needing a human; the set is re-entered and only beta runs.
	tools.mu.Lock()
	tools.parked = map[string]bool{}
	tools.ran = nil
	tools.mu.Unlock()

	_, err = orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)

	assert.Equal(t, []string{"beta"}, tools.ran,
		"a second round costs only what moved; re-running finished children would pay twice")
}

// A child squeezed below what its work costs does not fail cleanly — it stops
// halfway having spent the money. The engine says so instead.
func TestDelegate_RefusesToStarveChildren(t *testing.T) {
	t.Parallel()

	tools := newFanTools("alpha\nbeta\ngamma")
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})

	pattern := fanPattern("alpha\nbeta\ngamma")
	sm := pattern.GetStateMachine()
	sm.MaxCostUsd = 3.0
	sm.States["FAN"].GetDelegate().MinBudgetUsd = 10.0

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "below the $10.00 a child needs")
	assert.Empty(t, tools.ran, "nothing should run when the set cannot be funded")
}

// The single-child case is the same step with a set of one, and it still
// answers its parent with the terminal it reached.
func TestDelegate_SingleChildAnswersWithItsTerminal(t *testing.T) {
	t.Parallel()

	tools := newFanTools("")
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "DELIVER",
		States: map[string]*loomv1.WorkflowState{
			"DELIVER": {
				Body:  &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: repoMachine()}},
				Vocab: []string{"clear"},
				Route: map[string]*loomv1.Transition{"clear": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	pattern.WorkflowId = "single"

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])
	assert.Len(t, tools.ran, 1)
}

// A child is capped at what its own work should cost, or at its share of what
// the parent has left, whichever is less. The share alone is wrong for a parent
// that delegates one child at a time: the first child's share is the whole
// balance.
func TestDelegate_AChildIsCappedAtTheLesserOfItsOwnCeilingAndItsShare(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		own, share, want float64
	}{
		{"its own ceiling is the smaller", 50, 300, 50},
		{"its share is the smaller", 50, 20, 20},
		{"it declares none, so the share is all there is", 0, 120, 120},
		{"the parent has no ceiling, so its own stands", 50, 0, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow := repoMachine()
			workflow.GetStateMachine().MaxCostUsd = tc.own
			e := &StateMachineExecutor{workflowID: "parent", dir: t.TempDir()}
			child, err := e.childPattern(&loomv1.DelegateState{Workflow: workflow}, workflow, "DELIVER", listEntry{Item: "D-1"}, tc.share)
			require.NoError(t, err)
			assert.Equal(t, tc.want, child.GetStateMachine().GetMaxCostUsd())
			assert.Equal(t, tc.own, workflow.GetStateMachine().GetMaxCostUsd(), "the template is not edited")
		})
	}
}

// A child is told what it is: its own item, and the constants the delegation
// carries — beside everything it inherits. Nothing in the child reads a file
// to find out which repo it is or which environment it proves.
func TestDelegate_AChildIsToldItsItemAndTheConstantsItWasGiven(t *testing.T) {
	t.Parallel()

	tools := newFanTools("alpha\nbeta")
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})
	pattern := fanPattern("alpha\nbeta")
	fan := pattern.GetStateMachine().States["FAN"].GetDelegate()
	fan.Vars = map[string]string{"scope": "dit"}
	fan.Workflow.GetStateMachine().States["REVIEW"].GetTool().Params = map[string]string{"told": "{item} proves {scope}"}

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "alpha proves dit", tools.told["alpha"])
	assert.Equal(t, "beta proves dit", tools.told["beta"])
}

// stopTools is a child that stops at its second step the first time and goes
// on the second, and a parent's manager that rules on it.
type stopTools struct {
	mu   sync.Mutex
	ran  []string
	told map[string][]string
	seen []string
}

func (s *stopTools) Execute(_ context.Context, name string, params map[string]interface{}) (*shuttle.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ran = append(s.ran, name)
	msg, _ := params[ParamMessage].(string)
	s.told[name] = append(s.told[name], msg)
	switch name {
	case "first":
		return &shuttle.Result{Success: true, Data: `{"signal":"ok","vars":{"found":"the schema"}}`}, nil
	case "second":
		got, _ := params["found"].(string)
		s.seen = append(s.seen, got)
		if len(s.told[name]) == 1 {
			return &shuttle.Result{Success: true, Data: "the pack has no stage command; `make up` is missing\nstuck"}, nil
		}
		return &shuttle.Result{Success: true, Data: "provisioned\nok"}, nil
	case "manage":
		return &shuttle.Result{Success: true, Data: "use `make dev` instead\nwork"}, nil
	}
	return &shuttle.Result{Success: true, Data: ""}, nil
}

// A child that stops hands its parent the whole of what the step that stopped
// said. Sent back into it, the child takes up at that step, holding what it had
// worked out, with the ruling in front of it — the steps before are not run again.
func TestDelegate_AStoppedChildIsHeardWholeAndTakenUpWhereItStopped(t *testing.T) {
	t.Parallel()

	tools := &stopTools{told: map[string][]string{}}
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})
	child := wrap(&loomv1.StateMachinePattern{
		EntryState: "FIRST",
		States: map[string]*loomv1.WorkflowState{
			"FIRST": {
				Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "first"}},
				Route: map[string]*loomv1.Transition{"ok": to("SECOND")},
			},
			"SECOND": {
				Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "second", Params: map[string]string{"found": "{found}"}}},
				Route: map[string]*loomv1.Transition{"ok": to("DONE"), "stuck": to("STUCK")},
			},
			"DONE":  {Terminal: true},
			"STUCK": {Terminal: true},
		},
	})
	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "WORK",
		States: map[string]*loomv1.WorkflowState{
			"WORK": {
				Body:  &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}},
				Route: map[string]*loomv1.Transition{"done": to("FIN"), "stuck": to("MANAGE")},
			},
			"MANAGE": {
				Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "manage"}},
				Route: map[string]*loomv1.Transition{"work": to("WORK")},
			},
			"FIN": {Terminal: true},
		},
	})
	pattern.WorkflowId = "stopped"

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "FIN", result.Metadata["terminal_state"])
	assert.Equal(t, []string{"first", "second", "manage", "second"}, tools.ran,
		"the child should take up at the step that stopped, not start over")
	require.Len(t, tools.told["manage"], 1)
	assert.Contains(t, tools.told["manage"][0], "STUCK")
	assert.Contains(t, tools.told["manage"][0], "`make up` is missing",
		"the manager should hear the whole of what the stopped step said")
	require.Len(t, tools.told["second"], 2)
	assert.Contains(t, tools.told["second"][1], "use `make dev` instead",
		"the step that stopped should be taken up with the ruling in front of it")
	assert.Equal(t, []string{"the schema", "the schema"}, tools.seen,
		"the child should hold what it had worked out when taken up again")
}
