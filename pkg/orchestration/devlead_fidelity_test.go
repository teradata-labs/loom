// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// The DEVLEAD spec, transcribed from
// tera-sdlc-loop/tera_sdlc_loop/agents/developer/devlead.py.
//
// Whether the port is faithful is a property of the translation, not of a run:
// STATE_TABLE, VOCAB and ROUTE are static data on both sides, so the honest
// test compares tables rather than replaying one path through them. A walk
// exercises whichever edges it happens to touch; this covers all of them.

// devleadRoute is ROUTE: (state, signal) -> (destination, dismiss, hook).
// An "advance:" destination is a host-computed target, not a state name.
var devleadRoute = map[[2]string]struct {
	target  string
	dismiss []string
	hook    string
}{
	{"P_CREATE", "planned"}:             {target: "P_REVIEW"},
	{"P_REVIEW", "deltas"}:              {target: "P_CREATE"},
	{"P_REVIEW", "converged"}:           {target: "PUBLISH"},
	{"PUBLISH", "published"}:            {target: "DELIVER_TASK", hook: "task=first"},
	{"DELIVER_TASK", "delivered"}:       {target: "TASK_ACCEPT", hook: "checkpoint"},
	{"DELIVER_TASK", "skipped"}:         {target: "advance:tasks"},
	{"DELIVER_TASK", "failed"}:          {target: "EXCEPTION"},
	{"TASK_ACCEPT", "accepted"}:         {target: "advance:tasks"},
	{"TASK_ACCEPT", "task-deltas"}:      {target: "DELIVER_TASK", hook: "task=fix"},
	{"TASK_ACCEPT", "refined"}:          {target: "P_REVIEW"},
	{"TASK_ACCEPT", "replan"}:           {target: "P_CREATE"},
	{"EXCEPTION", "resolved"}:           {target: "DELIVER_TASK"},
	{"EXCEPTION", "replan"}:             {target: "P_CREATE"},
	{"VALIDATE", "validated"}:           {target: "PLAN_EPIC_TESTS"},
	{"PLAN_EPIC_TESTS", "planned"}:      {target: "WRITE_EPIC_TESTS"},
	{"PLAN_EPIC_TESTS", "no-change"}:    {target: "VERIFY"},
	{"WRITE_EPIC_TESTS", "tests-ready"}: {target: "CHECK_EPIC_TESTS"},
	{"CHECK_EPIC_TESTS", "deltas"}:      {target: "WRITE_EPIC_TESTS"},
	{"CHECK_EPIC_TESTS", "converged"}:   {target: "VERIFY", hook: "epic-tests=commit"},
	{"VERIFY", "pass"}:                  {target: "DONE", dismiss: []string{"*"}, hook: "checkpoint"},
	{"VERIFY", "fix"}:                   {target: "DELIVER_TASK", hook: "task=first"},
}

// devleadUnrouted is VOCAB minus ROUTE: signals a worker may legitimately emit
// that the table deliberately does not resolve. Each one must park for the DM.
// DELIVER_TASK's "hitl" is the sharpest: the task parked for a human, and no
// machine layer is allowed to re-judge that.
var devleadUnrouted = [][2]string{
	{"P_CREATE", "bounce"},
	{"P_REVIEW", "bounce"},
	{"TASK_ACCEPT", "bounce"},
	{"DELIVER_TASK", "hitl"},
	{"EXCEPTION", "redesign"},
	{"EXCEPTION", "escalate"},
	{"VALIDATE", "env"},
	{"VERIFY", "env"},
	{"VERIFY", "bubble"},
}

// devleadBodies is STATE_TABLE: which worker owns each state, and with which
// prompts. A None worker in the loop is deterministic host work here.
var devleadBodies = map[string]struct {
	kind         string // "agent" | "action" | "nested"
	agent        string
	instructions string
	action       string
}{
	"P_CREATE":         {kind: "agent", agent: "devlead", instructions: "plan-create"},
	"P_REVIEW":         {kind: "agent", agent: "plan_reviewer", instructions: "plan-review"},
	"PUBLISH":          {kind: "action", action: "publish"},
	"DELIVER_TASK":     {kind: "nested"},
	"TASK_ACCEPT":      {kind: "agent", agent: "devlead", instructions: "accept-task"},
	"EXCEPTION":        {kind: "agent", agent: "devlead", instructions: "handle-exception"},
	"VALIDATE":         {kind: "action", action: "validate"},
	"PLAN_EPIC_TESTS":  {kind: "agent", agent: "test_lead", instructions: "plan-epic-tests"},
	"WRITE_EPIC_TESTS": {kind: "agent", agent: "feature_tester", instructions: "write-epic-tests"},
	"CHECK_EPIC_TESTS": {kind: "agent", agent: "test_lead", instructions: "check-epic-tests"},
	"VERIFY":           {kind: "nested"},
}

var devleadTerminals = []string{"DONE", "BOUNCED", "ESCALATED"}

func loadDevlead(t *testing.T) *loomv1.StateMachinePattern {
	t.Helper()
	pattern, err := LoadWorkflowFromYAML("testdata/devlead.yaml")
	require.NoError(t, err)
	sm := pattern.GetStateMachine()
	require.NotNil(t, sm, "devlead.yaml must load as a state machine")
	return sm
}

// TestDevlead_EveryEdgeMatchesTheLoopSpec is the fidelity proof: every entry in
// ROUTE, every deliberately unrouted signal, every body in STATE_TABLE.
func TestDevlead_EveryEdgeMatchesTheLoopSpec(t *testing.T) {
	t.Parallel()
	sm := loadDevlead(t)

	t.Run("entry and terminals", func(t *testing.T) {
		assert.Equal(t, "P_CREATE", sm.EntryState)
		for _, name := range devleadTerminals {
			st, ok := sm.States[name]
			require.True(t, ok, "terminal %s is missing", name)
			assert.True(t, st.GetTerminal(), "%s must be terminal", name)
		}
		assert.InDelta(t, 400.0, sm.MaxCostUsd, 0.001, "the loop caps a unit at $400")
	})

	t.Run("every routed edge", func(t *testing.T) {
		for key, want := range devleadRoute {
			state, signal := key[0], key[1]
			t.Run(fmt.Sprintf("%s/%s", state, signal), func(t *testing.T) {
				st, ok := sm.States[state]
				require.True(t, ok, "state %s is missing", state)
				tr, ok := st.GetRoute()[signal]
				require.True(t, ok, "signal %q is not routed from %s", signal, state)

				if len(want.target) > 8 && want.target[:8] == "advance:" {
					require.NotNil(t, tr.GetNextTool(),
						"a computed destination must stay computed, not be frozen to one state")
					assert.Equal(t, want.target, tr.GetNextTool().GetTool())
				} else {
					assert.Equal(t, want.target, tr.GetNextState())
				}
				switch want.hook {
				case "":
					assert.Nil(t, tr.GetHook())
					assert.False(t, tr.GetCheckpoint())
				case HookCheckpoint:
					assert.True(t, tr.GetCheckpoint(), "a checkpoint is the engine's own, not a tool")
				default:
					require.NotNil(t, tr.GetHook())
					assert.Equal(t, want.hook, tr.GetHook().GetTool())
				}
				assert.Equal(t, want.dismiss, nilIfEmpty(tr.GetDismiss()))
			})
		}
	})

	t.Run("no edge exists that the loop does not have", func(t *testing.T) {
		for name, st := range sm.States {
			for signal := range st.GetRoute() {
				_, ok := devleadRoute[[2]string{name, signal}]
				assert.True(t, ok, "%s/%s is routed here but not in the loop spec", name, signal)
			}
		}
	})

	t.Run("signals the loop leaves unrouted stay unrouted", func(t *testing.T) {
		for _, key := range devleadUnrouted {
			state, signal := key[0], key[1]
			st, ok := sm.States[state]
			require.True(t, ok)
			_, routed := st.GetRoute()[signal]
			assert.False(t, routed,
				"%s/%s is unrouted by design — routing it would silently take a decision that belongs to a human",
				state, signal)
		}
	})

	t.Run("bodies", func(t *testing.T) {
		for name, want := range devleadBodies {
			st, ok := sm.States[name]
			require.True(t, ok, "state %s is missing", name)
			switch want.kind {
			case "agent":
				b := st.GetAgent()
				require.NotNil(t, b, "%s must be an agent state", name)
				assert.Equal(t, want.agent, b.GetAgentId())
				assert.Equal(t, want.instructions, b.GetInstructions())
			case "action":
				b := st.GetTool()
				require.NotNil(t, b, "%s is worker-less in the loop, so it must cost nothing here", name)
				assert.Equal(t, want.action, b.GetTool())
			case "nested":
				require.NotNil(t, st.GetDelegate(), "%s hands its work to another workflow", name)
			}
		}
	})

	t.Run("only the states the loop declares", func(t *testing.T) {
		for name := range sm.States {
			_, isBody := devleadBodies[name]
			isTerminal := false
			for _, term := range devleadTerminals {
				if term == name {
					isTerminal = true
				}
			}
			assert.True(t, isBody || isTerminal, "%s exists here but not in the loop spec", name)
		}
	})
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// TestDevlead_WalksTheSpine drives the ported machine with the signals a real
// unit produces and asserts where it goes. The table test above proves the
// translation; this proves the engine honours it.
func TestDevlead_WalksTheSpine(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	actions := newScriptedActions()
	actions.signals = map[string]string{
		"publish":  "published",
		"run-task": "done",
		"validate": "validated",
		"triage":   "clean",
	}
	// The lead accepts the task; the host says there are no tasks left, so the
	// walk advances to the completeness gate. This is the DAG walk the pattern
	// deliberately cannot express on its own.
	actions.advances = map[string][]string{"advance:tasks": {"VALIDATE"}}

	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: actions,
	})
	devleadLLM := newRecordingLLMProvider(
		"plan v1\nplanned", // P_CREATE (first entry: instructions)
		"plan v2\nplanned", // P_CREATE (return, after deltas)
		"digest\naccepted", // TASK_ACCEPT
	)
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, devleadLLM))
	orch.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store,
		newRecordingLLMProvider("needs work\ndeltas", "good\nconverged")))
	orch.RegisterAgent("test_lead", machineAgent(t, "test_lead", store,
		newRecordingLLMProvider("classes\nplanned", "certified\nconverged")))
	orch.RegisterAgent("feature_tester", machineAgent(t, "feature_tester", store,
		newRecordingLLMProvider("written\ntests-ready")))

	pattern, err := LoadWorkflowFromYAML("testdata/devlead.yaml")
	require.NoError(t, err)
	pattern.WorkflowId = "devlead-spine"

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	// P_CREATE -> P_REVIEW -> P_CREATE -> P_REVIEW -> PUBLISH -> DELIVER_TASK
	//   -> TASK_ACCEPT -> (advance:tasks) VALIDATE -> PLAN_EPIC_TESTS
	//   -> WRITE_EPIC_TESTS -> CHECK_EPIC_TESTS -> VERIFY -> DONE
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])
	assert.Equal(t, "12", result.Metadata["transitions"])

	// Every deterministic state ran as host work, in order, costing nothing.
	assert.Equal(t, []string{"publish", "run-task", "validate", "triage"}, actions.actionCalls)
	assert.Equal(t, []string{"advance:tasks"}, actions.advanceCalls)
	// checkpoint is the engine's own hook; the rest reach the host.
	assert.Equal(t, []string{"task=first", "epic-tests=commit"}, actions.transitionCalls)

	// The lead is one conversation across P_CREATE, P_CREATE again and
	// TASK_ACCEPT, and every visit carries its step's instructions.
	prompts := devleadLLM.recordedPrompts()
	require.Len(t, prompts, 3)
	assert.Contains(t, prompts[0], "plan-create")
	assert.Contains(t, prompts[1], "Step P_CREATE\n\nplan-create", "a return carries its instructions and what came back")
	assert.Contains(t, prompts[2], "accept-task")

	// VERIFY/pass dismisses every session on the way to DONE.
	for _, role := range []string{"devlead", "plan_reviewer", "test_lead", "feature_tester"} {
		ag, err := orch.GetAgent(context.Background(), role)
		require.NoError(t, err)
		for _, s := range ag.ListSessions() {
			assert.NotContains(t, s.ID, "devlead-spine",
				"the terminal edge dismisses every worker; %s survived", s.ID)
		}
	}
}

// TestDevlead_UnroutedSignalParksForTheDM proves the design's most important
// negative: a worker saying "hitl" is not re-judged by any machine layer.
func TestDevlead_UnroutedSignalParksForTheDM(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	actions := newScriptedActions()
	actions.signals = map[string]string{"publish": "published", "run-task": "hitl"}

	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: actions,
	})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store,
		newRecordingLLMProvider("plan\nplanned")))
	orch.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store,
		newRecordingLLMProvider("good\nconverged")))
	orch.RegisterAgent("test_lead", machineAgent(t, "test_lead", store, newRecordingLLMProvider()))
	orch.RegisterAgent("feature_tester", machineAgent(t, "feature_tester", store, newRecordingLLMProvider()))

	pattern, err := LoadWorkflowFromYAML("testdata/devlead.yaml")
	require.NoError(t, err)
	pattern.WorkflowId = "devlead-hitl"

	// The nested task machine has no route for "hitl", so it stops where it
	// stands; the dev lead's state that ran it says so in its own loop, whose
	// table routes nothing for it either, and so the run stops there.
	_, err = orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, suspended.Checkpoint.SuspendReason,
		"a task stopped for a human must surface as unroutable, not be resolved by a default branch")
	assert.Equal(t, []string{"DELIVER_TASK"}, suspended.Checkpoint.StatePath)
	word, said := stopOf(suspended.Checkpoint)
	assert.Equal(t, EscalatedSignal, word)
	assert.Contains(t, said, "hitl", "the dev lead's stop does not carry what the task stopped on")
}
