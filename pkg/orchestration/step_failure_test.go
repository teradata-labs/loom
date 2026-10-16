// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// failsUntil is a command that fails — the box it reaches is down — for its
// first runs, and says done after.
type failsUntil struct {
	mu    sync.Mutex
	fails int
	runs  int
}

func (f *failsUntil) Execute(context.Context, string, map[string]interface{}) (*shuttle.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs++
	if f.runs <= f.fails {
		return &shuttle.Result{Success: false, Error: &shuttle.Error{Code: "EXIT_3", Message: "the box is unreachable"}}, nil
	}
	return &shuttle.Result{Success: true, Data: `{"signal":"done"}`}, nil
}

// commanded is a machine whose one step runs a command, with a manager.
func commanded(manager string) *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: "RUN", LogicSeat: "MANAGE", States: map[string]*loomv1.WorkflowState{
		"RUN": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "bench"}},
			Route: map[string]*loomv1.Transition{"done": to("FIN")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: manager, Instructions: "settle it"}}},
		"FIN":    {Terminal: true},
	}})
}

// A step that could not run is a step that cannot go on: in a machine with a
// manager it goes to the manager, inside the run, with what stopped it. The
// manager may send the machine back into the step, which runs again.
func TestAStepThatCouldNotRunGoesToTheManager(t *testing.T) {
	store := machineStore(t)
	model := &byStep{says: map[string][]string{
		"MANAGE": {"the box is back\n" + `{"signal":"run"}`},
	}}
	bench := &failsUntil{fails: 1}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), ToolExecutor: bench})
	orch.RegisterAgent("boss", machineAgent(t, "boss", store, model))
	p := commanded("boss")
	p.WorkflowId = "bench-1"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "a step that could not run stopped the run instead of reaching the manager")
	require.Equal(t, 2, bench.runs, "the manager's word did not run the step again")
	told := model.told("MANAGE")
	require.Len(t, told, 1)
	require.Contains(t, told[0], "From: RUN")
	require.Contains(t, told[0], "RUN could not run")
	require.Contains(t, told[0], "the box is unreachable", "the manager was not told what stopped the step")
	require.Contains(t, told[0], "- `run` → RUN: a fixed step the loop runs itself; sent back, it runs again", "the manager has no word for the command step")
}

// The manager is the last one inside the run. When its own step cannot run,
// the run stops there for a person, as any step of a machine without one does.
func TestAManagerThatCouldNotRunStopsTheRunForAPerson(t *testing.T) {
	store := machineStore(t)
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: &failsUntil{fails: 99}})
	orch.RegisterAgent("boss", machineAgent(t, "boss", store, &unreachableOnce{}))
	p := commanded("boss")
	p.WorkflowId = "bench-2"

	_, err := orch.ExecutePattern(context.Background(), p)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	ckpt := suspended.Checkpoint
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, ckpt.SuspendReason)
	require.Equal(t, "MANAGE", ckpt.StatePath[len(ckpt.StatePath)-1], "the run did not stop at the manager")
	require.Contains(t, ckpt.Error, "refresh cached SSO token failed")
}

// byTool runs each tool by name.
type byTool map[string]*failsUntil

func (b byTool) Execute(ctx context.Context, name string, params map[string]interface{}) (*shuttle.Result, error) {
	return b[name].Execute(ctx, name, params)
}

// What runs on the way out of a step — its commit — failing is the step
// failing: it goes to the manager like any other, and the manager's word runs
// the step and its commit again.
func TestAStepsCommitThatFailsGoesToTheManager(t *testing.T) {
	model := &byStep{says: map[string][]string{"MANAGE": {"the box is back\n" + `{"signal":"run"}`}}}
	tools := byTool{"bench": {}, "commit": {fails: 1}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), ToolExecutor: tools})
	orch.RegisterAgent("boss", machineAgent(t, "boss", machineStore(t), model))
	p := commanded("boss")
	p.GetStateMachine().States["RUN"].Route["done"].Hook = &loomv1.ToolState{Tool: "commit"}
	p.WorkflowId = "commit-1"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "a failed commit stopped the run instead of reaching the manager")
	require.Equal(t, 2, tools["commit"].runs, "the manager's word did not run the commit again")
	told := model.told("MANAGE")
	require.Len(t, told, 1)
	require.Contains(t, told[0], "RUN could not run")
	require.Contains(t, told[0], "the box is unreachable", "the manager was not told what stopped the commit")
}

// deepUnder is a machine that runs mid, which runs inner, whose one step runs a
// command: two loops without a manager under the top one, which has a manager
// when manager is named.
func deepUnder(manager string, kept *shelf) *loomv1.WorkflowPattern {
	kept.put("inner", wrap(&loomv1.StateMachinePattern{EntryState: "RUN", States: map[string]*loomv1.WorkflowState{
		"RUN":  {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "bench"}}, Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE": {Terminal: true},
	}}))
	kept.put("mid", wrap(&loomv1.StateMachinePattern{EntryState: "TASK", States: map[string]*loomv1.WorkflowState{
		"TASK": {Body: named("inner"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE": {Terminal: true},
	}}))
	sm := &loomv1.StateMachinePattern{EntryState: "PHASE", States: map[string]*loomv1.WorkflowState{
		"PHASE": {Body: named("mid"), Route: map[string]*loomv1.Transition{"done": to("FIN")}},
		"FIN":   {Terminal: true},
	}}
	if manager != "" {
		sm.LogicSeat = "MANAGE"
		sm.States["MANAGE"] = &loomv1.WorkflowState{Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: manager, Instructions: "settle it"}}}
	}
	return wrap(sm)
}

// A step that could not run in a loop with no manager of its own goes up to the
// nearest manager above, however many loops between, with what stopped it; the
// manager's word sends the work back in and the step runs again.
func TestAStepThatCouldNotRunDeepBelowGoesToTheNearestManager(t *testing.T) {
	kept := &shelf{}
	model := &byStep{says: map[string][]string{"MANAGE": {"the box is back\n" + `{"signal":"phase"}`}}}
	tools := byTool{"bench": {fails: 1}}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: tools, WorkflowResolver: kept.find})
	orch.RegisterAgent("boss", machineAgent(t, "boss", machineStore(t), model))
	p := deepUnder("boss", kept)
	p.WorkflowId = "deep-1"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err, "a step deep below stopped the run instead of reaching the manager")
	require.Equal(t, 2, tools["bench"].runs, "the manager's word did not run the step again")
	told := model.told("MANAGE")
	require.Len(t, told, 1)
	require.Contains(t, told[0], "RUN could not run")
	require.Contains(t, told[0], "the box is unreachable", "the manager was not told what stopped the step")
}

// With no manager anywhere above, a step that could not run stops the run for
// a person, on what stopped it.
func TestAStepThatCouldNotRunWithNoManagerAnywhereStopsTheRun(t *testing.T) {
	kept := &shelf{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: byTool{"bench": {fails: 99}}, WorkflowResolver: kept.find})
	p := deepUnder("", kept)
	p.WorkflowId = "deep-2"

	_, err := orch.ExecutePattern(context.Background(), p)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	at := innermost(suspended.Checkpoint)
	require.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, at.GetSuspendReason())
	require.Contains(t, at.GetError(), "the box is unreachable")
}
