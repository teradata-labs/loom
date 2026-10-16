// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// listTools is the fan's tools with two more seats: a lister whose list is
// whatever the test says, and a judge that keeps every message it was handed.
type listTools struct {
	*fanTools
	mu     sync.Mutex
	lists  []string // what each visit to the lister says, in order
	visits int
	judged []string // the message the judge opened with, per visit
	opened map[string]string
}

func newListTools(lists ...string) *listTools {
	return &listTools{fanTools: newFanTools(""), lists: lists, opened: map[string]string{}}
}

func (l *listTools) Execute(ctx context.Context, name string, params map[string]interface{}) (*shuttle.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch name {
	case "list_repos":
		say := l.lists[len(l.lists)-1]
		if l.visits < len(l.lists) {
			say = l.lists[l.visits]
		}
		l.visits++
		return &shuttle.Result{Success: true, Data: "the list follows\n" + say}, nil
	case "judge":
		msg, _ := params[ParamMessage].(string)
		l.judged = append(l.judged, msg)
		return &shuttle.Result{Success: true, Data: "judged\nok"}, nil
	case "review":
		dir, _ := params[ParamDir].(string)
		item := dir[strings.LastIndex(dir, "/")+1:]
		msg, _ := params[ParamMessage].(string)
		l.opened[item] = msg
	}
	l.mu.Unlock()
	defer l.mu.Lock()
	return l.fanTools.Execute(ctx, name, params)
}

// listPattern is the fan with a judge after every stepped entry and a way to
// list again after the set is done.
func listPattern(step bool) *loomv1.WorkflowPattern {
	p := fanPattern("")
	sm := p.GetStateMachine()
	sm.States["FAN"].GetDelegate().Step = step
	sm.States["FAN"].GetDelegate().MaxParallel = 1
	sm.States["FAN"].Vocab = []string{FannedSignal, SteppedSignal, "clear"}
	sm.States["FAN"].Route = map[string]*loomv1.Transition{FannedSignal: to("AFTER"), "clear": to("AFTER"), SteppedSignal: to("JUDGE")}
	sm.States["JUDGE"] = &loomv1.WorkflowState{
		Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "judge"}},
		Vocab: []string{"ok"},
		Route: map[string]*loomv1.Transition{"ok": to("FAN")},
	}
	sm.States["AFTER"] = &loomv1.WorkflowState{
		Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "judge"}},
		Vocab: []string{"ok"},
		Route: map[string]*loomv1.Transition{"ok": to("DONE")},
	}
	return p
}

func runList(t *testing.T, tools *listTools, p *loomv1.WorkflowPattern) {
	t.Helper()
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})
	_, err := orch.ExecutePattern(context.Background(), p)
	require.NoError(t, err)
}

// A list rides on the signal line under the field the state names, and how
// the walk over it ended is the message the next state opens with. The list
// itself is the engine's: the children do not read it, and neither does the
// state after.
func TestListState_TheListIsTheEnginesAndTheOutcomeIsTheNextMessage(t *testing.T) {
	t.Parallel()
	tools := newListTools(`{"signal": "listed", "repos": "alpha,beta"}`)
	runList(t, tools, listPattern(false))

	assert.Equal(t, []string{"alpha", "beta"}, tools.ran)
	require.Len(t, tools.judged, 1)
	assert.Equal(t, "alpha=CLEAR\nbeta=CLEAR", tools.judged[0], "the state after the list opens with how each entry ended")
	assert.Equal(t, "the list follows", tools.opened["alpha"], "a child opens with the handover, not with the list")
}

// A stepping list ends after every entry: the entry's outcome is the message,
// the state after judges it, and re-entering runs the next. When nothing is
// left the state ends fanned with the whole record.
func TestListState_SteppingJudgesEveryEntryBeforeTheNext(t *testing.T) {
	t.Parallel()
	tools := newListTools(`{"signal": "listed", "repos": "alpha,beta"}`)
	p := listPattern(true)
	// An entry is stepped when it ran to done: the children of this walk do.
	child := p.GetStateMachine().States["FAN"].GetDelegate().GetWorkflow().GetStateMachine()
	child.States["REVIEW"].Route["clear"] = to("DONE")
	delete(child.States, "CLEAR")
	child.States["DONE"] = &loomv1.WorkflowState{Terminal: true}
	runList(t, tools, p)

	assert.Equal(t, []string{"alpha", "beta"}, tools.ran, "each entry once, in order")
	assert.Equal(t, []string{"alpha=DONE", "beta=DONE", "alpha=DONE\nbeta=DONE"}, tools.judged,
		"judged after each entry with that entry's outcome, then the whole record when the list is done")
}

// A new list arriving with a transition into the state is a fresh walk: what
// an earlier list finished as is forgotten, so an entry named again runs
// again. Re-entering without a list continues the walk in progress.
func TestListState_ANewListIsAFreshWalk(t *testing.T) {
	t.Parallel()
	tools := newListTools(`{"signal": "listed", "repos": "alpha,beta"}`, `{"signal": "listed", "repos": "alpha"}`)
	p := listPattern(false)
	// After the first walk the lister speaks again with a list of one.
	sm := p.GetStateMachine()
	sm.States["AFTER"].Route = map[string]*loomv1.Transition{"ok": to("RELIST")}
	sm.States["RELIST"] = &loomv1.WorkflowState{
		Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "list_repos"}},
		Vocab: []string{"listed"},
		Route: map[string]*loomv1.Transition{"listed": to("FAN2")},
	}
	fan2 := &loomv1.WorkflowState{
		Body:  sm.States["FAN"].Body,
		Vocab: []string{FannedSignal, "clear"},
		Route: map[string]*loomv1.Transition{FannedSignal: to("LAST"), "clear": to("LAST")},
	}
	sm.States["FAN2"] = fan2
	sm.States["LAST"] = &loomv1.WorkflowState{
		Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "judge"}},
		Vocab: []string{"ok"},
		Route: map[string]*loomv1.Transition{"ok": to("DONE")},
	}
	runList(t, tools, p)

	assert.Equal(t, []string{"alpha", "beta", "alpha"}, tools.ran, "alpha runs again because the new list names it; beta does not")
	assert.Equal(t, "alpha=CLEAR", tools.judged[len(tools.judged)-1], "the record of the new walk is the new list's alone")
}

// A list may say where each child works. A relative place is under the
// parent's; the child is told nothing else.
func TestListState_AnEntryMayPlaceItsChild(t *testing.T) {
	t.Parallel()
	tools := newListTools(`{"signal": "listed", "repos": [{"item": "alpha", "dir": "work/alpha"}, {"item": "beta"}]}`)
	runList(t, tools, listPattern(false))

	assert.Equal(t, "set/work/alpha", tools.dirs["alpha"], "placed where the list said")
	assert.Equal(t, "set/repos/beta", tools.dirs["beta"], "placed by the state's template when the list did not say")
}

// A set of one is a fresh walk each time a transition enters it, so a phase
// asked for again runs again; and the constants a delegation hands its child
// may be spelled from the run's own placing.
func TestListState_ASetOfOneRunsAgainWhenEnteredAgainAndItsConstantsAreSpelledFromThePlacing(t *testing.T) {
	t.Parallel()
	tools := newListTools(`{"signal": "listed"}`)
	p := listPattern(false)
	sm := p.GetStateMachine()
	fan := sm.States["FAN"].GetDelegate()
	fan.ItemsKey = ""
	fan.DirTemplate = "one"
	fan.Vars = map[string]string{"unit": "{ws}/verify"}
	fan.Workflow.GetStateMachine().States["REVIEW"].GetTool().Params = map[string]string{"told": "unit is {unit}"}
	sm.States["FAN"].Vocab = []string{"clear"}
	sm.States["FAN"].Route = map[string]*loomv1.Transition{"clear": to("AGAIN")}
	sm.States["AGAIN"] = &loomv1.WorkflowState{
		Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "judge"}},
		Vocab: []string{"ok"},
		Route: map[string]*loomv1.Transition{"ok": to("FAN2")},
	}
	sm.States["FAN2"] = &loomv1.WorkflowState{
		Body:  sm.States["FAN"].Body,
		Vocab: []string{"clear"},
		Route: map[string]*loomv1.Transition{"clear": to("DONE")},
	}
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})
	_, err := orch.ExecutePattern(WithRunVars(context.Background(), map[string]string{"ws": "/w"}), p)
	var suspended *WorkflowSuspended
	if errors.As(err, &suspended) {
		t.Fatalf("suspended %s at %v; ran %v", suspended.Checkpoint.SuspendReason, suspended.Checkpoint.StatePath, tools.ran)
	}
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "one"}, tools.ran, "the set of one ran once per entry")
	assert.Equal(t, "unit is /w/verify", tools.told["one"])
}

// What a list says about an entry beyond its item and place fills the
// constants the spec declared for the child; a field the spec did not declare
// is not the child's. And a constant is spelled from the parent's own vars.
func TestListState_AnEntryFillsTheConstantsTheSpecDeclared(t *testing.T) {
	t.Parallel()
	tools := newListTools(`{"signal": "listed", "repos": [{"item": "alpha", "repo": "loom", "secret": "x"}]}`)
	p := listPattern(false)
	fan := p.GetStateMachine().States["FAN"].GetDelegate()
	fan.Vars = map[string]string{"repo": "", "parent_item": "{item}", "run": "{loom_run}"}
	fan.Workflow.GetStateMachine().States["REVIEW"].GetTool().Params = map[string]string{"told": "{item} in {repo} of {parent_item} run {run} {secret}"}
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: tools,
	})
	_, err := orch.ExecutePattern(WithRunVars(context.Background(), map[string]string{"item": "D-8"}), p)
	require.NoError(t, err)
	assert.Equal(t, "alpha in loom of D-8 run fan {secret}", tools.told["alpha"])
}

// A set whose entries all ended the same way answers with that word; a mixed
// set is fanned, for the state after to read.
func TestListState_AUniformSetAnswersWithItsWord(t *testing.T) {
	t.Parallel()
	tools := newListTools(`{"signal": "listed", "repos": "alpha,beta"}`)
	tools.parked["beta"] = true // beta ends needing a human: a mixed set
	p := listPattern(false)
	sm := p.GetStateMachine()
	sm.States["FAN"].GetDelegate().GetWorkflow().GetStateMachine().States["REVIEW"].Route["hitl"] = to("HELD")
	sm.States["FAN"].GetDelegate().GetWorkflow().GetStateMachine().States["HELD"] = &loomv1.WorkflowState{Terminal: true}
	sm.States["FAN"].Route[FannedSignal] = to("MIXED")
	sm.States["MIXED"] = &loomv1.WorkflowState{
		Body:  &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: "judge"}},
		Vocab: []string{"ok"},
		Route: map[string]*loomv1.Transition{"ok": to("DONE")},
	}
	runList(t, tools, p)
	assert.Equal(t, []string{"alpha=CLEAR\nbeta=HELD"}, tools.judged, "a mixed set is fanned and its record read by the state after")
}
