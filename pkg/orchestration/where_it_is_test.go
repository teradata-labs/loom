// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// starts records where each child a run delegates begins.
type starts struct {
	stepRecords
	mu    sync.Mutex
	begin []string
	ids   []string
}

func (s *starts) ChildStarted(ctx context.Context, _, _ string, child *loomv1.WorkflowPattern) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.begin = append(s.begin, EntryOf(ctx, child.GetStateMachine()))
	s.ids = append(s.ids, child.GetWorkflowId())
}

// A child taken up again says where it begins: at the step that stopped it,
// not at its entry — so whoever keeps its record keeps where it really is.
func TestAChildTakenUpAgainSaysWhereItBegins(t *testing.T) {
	tools := &stopTools{told: map[string][]string{}}
	seen := &starts{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: tools, RunObserver: seen})
	child := wrap(&loomv1.StateMachinePattern{EntryState: "FIRST", States: map[string]*loomv1.WorkflowState{
		"FIRST": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "first"}},
			Route: map[string]*loomv1.Transition{"ok": to("SECOND")}},
		"SECOND": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "second", Params: map[string]string{"found": "{found}"}}},
			Route: map[string]*loomv1.Transition{"ok": to("DONE"), "stuck": to("STUCK")}},
		"DONE":  {Terminal: true},
		"STUCK": {Terminal: true},
	}})
	p := wrap(&loomv1.StateMachinePattern{EntryState: "WORK", States: map[string]*loomv1.WorkflowState{
		"WORK": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}},
			Route: map[string]*loomv1.Transition{"done": to("FIN"), "stuck": to("MANAGE")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "manage"}},
			Route: map[string]*loomv1.Transition{"work": to("WORK")}},
		"FIN": {Terminal: true},
	}})
	p.WorkflowId = "begins"

	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, []string{"FIRST", "SECOND"}, seen.begin)
	require.Len(t, seen.ids, 2)
	assert.Equal(t, seen.ids[0], seen.ids[1], "taken up again, the child is the same run")
}

// Every progress event says which loop and which step its seat is in — the
// step inside a child loop, not the parent's step that handed it the work.
func TestProgressSaysWhichLoopAndStepItComesFrom(t *testing.T) {
	store := machineStore(t)
	seen := &starts{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), RunObserver: seen})
	orch.RegisterAgent("planner", costedAgent(t, "planner", store, call{tool: "look", cost: 0.1}, call{say: "planned\nnext", cost: 0.1}))
	orch.RegisterAgent("builder", costedAgent(t, "builder", store, call{tool: "look", cost: 0.1}, call{say: "built\ndone", cost: 0.1}))
	child := wrap(&loomv1.StateMachinePattern{EntryState: "BUILD", States: map[string]*loomv1.WorkflowState{
		"BUILD": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "builder"}},
			Vocab: []string{"done"}, Route: map[string]*loomv1.Transition{"done": to("DONE")}},
		"DONE": {Terminal: true},
	}})
	p := wrap(&loomv1.StateMachinePattern{EntryState: "PLAN", States: map[string]*loomv1.WorkflowState{
		"PLAN": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "planner"}},
			Vocab: []string{"next"}, Route: map[string]*loomv1.Transition{"next": to("STAGE")}},
		"STAGE": {Body: &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}},
			Route: map[string]*loomv1.Transition{"done": to("FIN")}},
		"FIN": {Terminal: true},
	}})
	p.WorkflowId = "stamped"

	var mu sync.Mutex
	from := map[string]bool{}
	ctx := agent.ContextWithProgressCallback(context.Background(), func(ev agent.ProgressEvent) {
		if ev.IsToolStarted {
			mu.Lock()
			from[ev.RunID+"/"+ev.State] = true
			mu.Unlock()
		}
	})
	_, err := orch.ExecutePattern(ctx, p)
	require.NoError(t, err)
	require.Len(t, seen.ids, 1)
	assert.Equal(t, map[string]bool{"stamped/PLAN": true, seen.ids[0] + "/BUILD": true}, from)
}
