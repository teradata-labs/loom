// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// machineStore opens a session store on disk. A shared, durable store is not
// incidental to these tests: first entry versus return is decided from the
// seat's stored conversation, so a per-agent in-memory store would make every
// re-entry look like a first entry and the assertions would pass for the wrong
// reason.
func machineStore(t *testing.T) *agent.SessionStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := agent.NewSessionStore(path, observability.NewNoOpTracer())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
		_ = os.Remove(path)
	})
	return store
}

// machineAgent builds a scripted agent backed by the shared store.
func machineAgent(t *testing.T, name string, store *agent.SessionStore, llm agent.LLMProvider) *agent.Agent {
	t.Helper()
	cfg := agent.DefaultConfig()
	cfg.PatternConfig = agent.DefaultPatternConfig()
	cfg.PatternConfig.UseLLMClassifier = false
	return agent.NewAgent(
		&mockBackend{},
		llm,
		agent.WithName(name),
		agent.WithConfig(cfg),
		agent.WithMemory(agent.NewMemoryWithStore(store)),
	)
}

// scriptedActions is a deterministic ActionHandler: it returns the signal for
// each action by name, records every call, and resolves advance hooks from a
// table the test controls.
type scriptedActions struct {
	mu sync.Mutex

	// action name -> signal it reports
	signals map[string]string

	// advance hook -> state it resolves to (consumed in order when a slice)
	advances map[string][]string

	actionCalls     []string
	transitionCalls []string
	advanceCalls    []string
}

func newScriptedActions() *scriptedActions {
	return &scriptedActions{
		signals:  map[string]string{},
		advances: map[string][]string{},
	}
}

// Execute is the tool door the walker goes through. A body reports the signal
// scripted for its tool; a computed destination reports a state name.
func (s *scriptedActions) Execute(_ context.Context, toolName string, params map[string]interface{}) (*shuttle.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if targets, ok := s.advances[toolName]; ok {
		s.advanceCalls = append(s.advanceCalls, toolName)
		if len(targets) == 0 {
			return nil, fmt.Errorf("unscripted advance %q", toolName)
		}
		next := targets[0]
		if len(targets) > 1 {
			s.advances[toolName] = targets[1:]
		}
		return &shuttle.Result{Success: true, Data: next}, nil
	}

	if sig, ok := s.signals[toolName]; ok {
		s.actionCalls = append(s.actionCalls, toolName)
		return &shuttle.Result{Success: true, Data: "output of " + toolName + "\n" + sig}, nil
	}

	// Anything else on an edge is a hook: it does work and reports nothing.
	s.transitionCalls = append(s.transitionCalls, toolName)
	return &shuttle.Result{Success: true, Data: ""}, nil
}

func agentState(id, instructions string) *loomv1.WorkflowState_Agent {
	return &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{
		AgentId:      id,
		Instructions: instructions,
	}}
}

func actionState(name string) *loomv1.WorkflowState_Tool {
	return &loomv1.WorkflowState_Tool{Tool: &loomv1.ToolState{Tool: name}}
}

// delegateTo is the single-child case: a set of one.
func delegateTo(child *loomv1.WorkflowPattern) *loomv1.WorkflowState_Delegate {
	return &loomv1.WorkflowState_Delegate{Delegate: &loomv1.DelegateState{Workflow: child}}
}

func to(state string) *loomv1.Transition {
	return &loomv1.Transition{Target: &loomv1.Transition_NextState{NextState: state}}
}

func wrap(sm *loomv1.StateMachinePattern) *loomv1.WorkflowPattern {
	return &loomv1.WorkflowPattern{Pattern: &loomv1.WorkflowPattern_StateMachine{StateMachine: sm}}
}

// planCycle is the DEVLEAD plan sub-cycle: create ⇄ review, then publish.
func planCycle(gate *loomv1.HITLGate) *loomv1.StateMachinePattern {
	return &loomv1.StateMachinePattern{
		EntryState: "P_CREATE",
		States: map[string]*loomv1.WorkflowState{
			"P_CREATE": {
				Body:  agentState("devlead", "plan-create"),
				Route: map[string]*loomv1.Transition{"planned": to("P_REVIEW")},
			},
			"P_REVIEW": {
				Body: agentState("plan_reviewer", "plan-review"),
				Route: map[string]*loomv1.Transition{
					"deltas":    to("P_CREATE"),
					"converged": to("PUBLISH"),
				},
				HitlGate: gate,
			},
			"PUBLISH": {Terminal: true},
		},
	}
}

func planCycleOrchestrator(t *testing.T, store *agent.SessionStore, handler HITLHandler) (*Orchestrator, *recordingLLMProvider, *recordingLLMProvider) {
	t.Helper()
	devleadLLM := newRecordingLLMProvider("plan v1\nplanned", "plan v2\nplanned")
	reviewerLLM := newRecordingLLMProvider("needs work\ndeltas", "looks good\nconverged")

	orch := NewOrchestrator(Config{
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
		HITLHandler: handler,
	})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, devleadLLM))
	orch.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store, reviewerLLM))
	return orch, devleadLLM, reviewerLLM
}

// ---------------------------------------------------------------------------
// H1 — a state is visited twice while the pattern never changes
// ---------------------------------------------------------------------------

func TestStateMachine_WalksACycle(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	orch, devleadLLM, reviewerLLM := planCycleOrchestrator(t, store, nil)

	pattern := wrap(planCycle(nil))
	pattern.WorkflowId = "cycle-1"

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	assert.Equal(t, "state_machine", result.PatternType)
	assert.Equal(t, "PUBLISH", result.Metadata["terminal_state"])
	// P_CREATE -> P_REVIEW -> P_CREATE -> P_REVIEW -> PUBLISH
	assert.Equal(t, "4", result.Metadata["transitions"])

	// The lead is one conversation across both visits, and every visit
	// carries the state's instructions and then what came to it. This is the
	// whole reason session identity is role-scoped rather than positional.
	prompts := devleadLLM.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[0], "Step P_CREATE\n\nplan-create")
	assert.Contains(t, prompts[1], "Step P_CREATE\n\nplan-create")
	assert.Contains(t, prompts[1], "needs work", "the reviewer's findings come with the return")

	// The reviewer's second visit is the same: its instructions, and the
	// revised plan.
	rprompts := reviewerLLM.recordedPrompts()
	require.Len(t, rprompts, 2)
	assert.Contains(t, rprompts[0], "Step P_REVIEW\n\nplan-review")
	assert.Contains(t, rprompts[1], "Step P_REVIEW\n\nplan-review")
	assert.Contains(t, rprompts[1], "plan v2")
}

// ---------------------------------------------------------------------------
// H2 — an all-action machine walks for free
// ---------------------------------------------------------------------------

func TestStateMachine_ActionOnlyWalkCostsNothing(t *testing.T) {
	t.Parallel()

	actions := newScriptedActions()
	actions.signals = map[string]string{
		"stage": "staged", "plan": "planned", "dev": "built",
	}

	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: actions,
	})

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "STAGE",
		States: map[string]*loomv1.WorkflowState{
			"STAGE": {Body: actionState("stage"), Route: map[string]*loomv1.Transition{"staged": to("PLAN")}},
			"PLAN":  {Body: actionState("plan"), Route: map[string]*loomv1.Transition{"planned": to("DEV")}},
			"DEV":   {Body: actionState("dev"), Route: map[string]*loomv1.Transition{"built": to("DONE")}},
			"DONE":  {Terminal: true},
		},
	})

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	assert.Equal(t, []string{"stage", "plan", "dev"}, actions.actionCalls)
	require.NotNil(t, result.Cost)
	assert.Zero(t, result.Cost.TotalCostUsd, "a walk of deterministic host work must cost nothing")
	assert.Zero(t, result.Cost.LlmCalls, "no model may be consulted on a healthy transition")
}

// ---------------------------------------------------------------------------
// H3 — park, discard everything, resume cold
// ---------------------------------------------------------------------------

func TestStateMachine_ParksAndResumesCold(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	// No HITL handler: gates suspend with a checkpoint, the durable default.
	orch, devleadLLM, _ := planCycleOrchestrator(t, store, nil)

	sm := planCycle(&loomv1.HITLGate{PromptTemplate: "Review the plan"})
	pattern := wrap(sm)
	pattern.WorkflowId = "park-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	ckpt := suspended.Checkpoint
	require.NotNil(t, ckpt)
	assert.Equal(t, []string{"P_REVIEW"}, ckpt.StatePath)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_GATE, ckpt.SuspendReason)
	assert.Equal(t, "state_machine", ckpt.PatternType)
	require.NotNil(t, ckpt.PendingGate)
	assert.Equal(t, "P_REVIEW", ckpt.PendingGate.StatePath)

	// Round-trip the checkpoint through bytes and rebuild the world: nothing
	// from the first half stays resident except the store.
	raw, err := proto.Marshal(ckpt)
	require.NoError(t, err)
	revived := &loomv1.WorkflowCheckpoint{}
	require.NoError(t, proto.Unmarshal(raw, revived))

	orch2 := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	devleadLLM2 := newRecordingLLMProvider("plan v2\nplanned")
	reviewerLLM2 := newRecordingLLMProvider("looks good\nconverged")
	orch2.RegisterAgent("devlead", machineAgent(t, "devlead", store, devleadLLM2))
	orch2.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store, reviewerLLM2))

	// The reviewer said "deltas", so approving routes back to P_CREATE — and
	// the lead must return to its existing conversation, not be given its
	// instructions as if new. A per-process in-memory store would silently
	// send the instructions here and still reach the terminal, which is why
	// the assertion is on the prompt.
	_, err = orch2.ResumeWorkflow(context.Background(), pattern, revived,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.Error(t, err)

	prompts := devleadLLM2.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "Step P_CREATE\n\nplan-create",
		"a cold resume gives the step its instructions, as every turn does")
	assert.Contains(t, prompts[0], "needs work", "a cold resume gives the step what came to it")

	// The walk came back round to P_REVIEW, which is gated, so it parked again.
	// A gate belongs to a state, not to a visit: every entry asks.
	var second *WorkflowSuspended
	require.ErrorAs(t, err, &second)
	assert.Equal(t, []string{"P_REVIEW"}, second.Checkpoint.StatePath)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_GATE, second.Checkpoint.SuspendReason)

	// This time the reviewer converged, so approving routes on to the terminal.
	result, err := orch2.ResumeWorkflow(context.Background(), pattern, second.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "PUBLISH", result.Metadata["terminal_state"])

	_ = devleadLLM
}

// ---------------------------------------------------------------------------
// H4b — the edge does work: hook, dismissal, computed destination
// ---------------------------------------------------------------------------

func TestStateMachine_EdgeFiresHookDismissesAndAdvances(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	actions := newScriptedActions()
	actions.signals = map[string]string{"deliver": "delivered"}
	actions.advances = map[string][]string{"advance:tasks": {"DONE"}}

	leadLLM := newRecordingLLMProvider("accepting\naccepted")
	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: actions,
	})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, leadLLM))

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "DELIVER",
		States: map[string]*loomv1.WorkflowState{
			"DELIVER": {
				Body: actionState("deliver"),
				Route: map[string]*loomv1.Transition{
					"delivered": {
						Target: &loomv1.Transition_NextState{NextState: "ACCEPT"},
						Hook:   &loomv1.ToolState{Tool: "task=first"},
					},
				},
			},
			"ACCEPT": {
				Body: agentState("devlead", "accept-task"),
				Route: map[string]*loomv1.Transition{
					"accepted": {
						Target:     &loomv1.Transition_NextTool{NextTool: &loomv1.ToolState{Tool: "advance:tasks"}},
						Dismiss:    []string{"devlead"},
						Checkpoint: true,
					},
				},
			},
			"DONE": {Terminal: true},
		},
	})
	pattern.WorkflowId = "edge-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	assert.Equal(t, []string{"task=first"}, actions.transitionCalls,
		"a checkpoint is the engine's own, not a tool call")
	assert.Equal(t, []string{"advance:tasks"}, actions.advanceCalls)

	// The dismissal took the lead's session out of memory and left its history
	// in storage, so a later entry resumes what the lead already worked out.
	ag, err := orch.GetAgent(context.Background(), "devlead")
	require.NoError(t, err)
	_, loaded := ag.GetSession("edge-1-devlead")
	assert.False(t, loaded, "a dismissed session must leave memory")
	exists, err := ag.SessionExists(context.Background(), "edge-1-devlead")
	require.NoError(t, err)
	assert.True(t, exists, "a dismissal must never delete a conversation's history")
}

func TestStateMachine_AdvanceToUnknownStateFails(t *testing.T) {
	t.Parallel()

	actions := newScriptedActions()
	actions.signals = map[string]string{"go": "went"}
	actions.advances = map[string][]string{"advance:nowhere": {"NO_SUCH_STATE"}}

	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: actions,
	})

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "GO",
		States: map[string]*loomv1.WorkflowState{
			"GO": {
				Body: actionState("go"),
				Route: map[string]*loomv1.Transition{
					"went": {Target: &loomv1.Transition_NextTool{NextTool: &loomv1.ToolState{Tool: "advance:nowhere"}}},
				},
			},
			"DONE": {Terminal: true},
		},
	})

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NO_SUCH_STATE",
		"host code is deterministic but is still not trusted to name a state that exists")
}

// ---------------------------------------------------------------------------
// H5 — an unroutable signal parks for a human instead of guessing
// ---------------------------------------------------------------------------

func TestStateMachine_UnroutableSignalSuspends(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	// "escalate" is in this state's vocabulary but has no route: the worker
	// judged its own situation and chose to escalate. That is deliberate
	// egress, and the engine must not argue with it.
	leadLLM := newRecordingLLMProvider("cannot proceed\nescalate")

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, leadLLM))

	sm := &loomv1.StateMachinePattern{
		EntryState: "P_CREATE",
		States: map[string]*loomv1.WorkflowState{
			"P_CREATE": {
				Body:  agentState("devlead", "plan-create"),
				Vocab: []string{"planned", "escalate"},
				Route: map[string]*loomv1.Transition{"planned": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	}
	pattern := wrap(sm)
	pattern.WorkflowId = "unroutable-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_UNROUTABLE, suspended.Checkpoint.SuspendReason)
	assert.Equal(t, []string{"P_CREATE"}, suspended.Checkpoint.StatePath)
	assert.Len(t, leadLLM.recordedPrompts(), 1,
		"an in-vocabulary signal the table does not route is a judgement already made; retrying it would loop that judgement")

	// Steering is the recovery: REVISE re-enters with the feedback in the prompt.
	leadLLM.responses = append(leadLLM.responses, "revised\nplanned")
	result, err := orch.ResumeWorkflow(context.Background(), pattern, suspended.Checkpoint,
		&loomv1.GateDecision{
			Action:   loomv1.GateAction_GATE_ACTION_REVISE,
			Feedback: "end with one of: planned",
		})
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])

	prompts := leadLLM.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "end with one of: planned")
}

// An off-vocabulary signal is a different thing entirely: the worker ended
// without a usable signal at all. Its answer goes back to it once, with the
// line and what is wrong with it.
func TestStateMachine_OffVocabularySignalIsRetriedOnce(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	leadLLM := newRecordingLLMProvider("rambling with no signal\nbanana", "sorry\nplanned")

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, leadLLM))

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "P_CREATE",
		States: map[string]*loomv1.WorkflowState{
			"P_CREATE": {
				Body:  agentState("devlead", "plan-create"),
				Vocab: []string{"planned", "escalate"},
				Route: map[string]*loomv1.Transition{"planned": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	pattern.WorkflowId = "offvocab-1"

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err, "one retry should recover a worker that simply failed to sign off")
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])

	prompts := leadLLM.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "Your response format or state is invalid.\nThe line: banana\n"+
		"What is wrong with it: it is not a word this step can end with\n"+
		"Your answer stands. Send it again in the expected format.")
}

// The answer goes back once. A worker that cannot sign off twice is not having
// a bad turn: that is its step's escalation, and with nothing to route it the
// run stops there.
func TestStateMachine_OffVocabularyRetryIsOfferedOnce(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	leadLLM := newRecordingLLMProvider("no\nbanana", "still no\nkumquat")

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, leadLLM))

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "P_CREATE",
		States: map[string]*loomv1.WorkflowState{
			"P_CREATE": {
				Body:  agentState("devlead", "plan-create"),
				Vocab: []string{"planned"},
				Route: map[string]*loomv1.Transition{"planned": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	pattern.WorkflowId = "offvocab-2"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	assert.Len(t, leadLLM.recordedPrompts(), 2, "exactly one send-back, then the escalation")
	_, escalated := escalationOf(suspended.Checkpoint)
	assert.True(t, escalated, "the stop is not the step's escalation")
}

// A signal's payload contract: a worker whose word came without what it
// carries has its answer sent back, with what is missing.
func TestStateMachine_SignalPayloadContractRetriesOnce(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	leadLLM := newRecordingLLMProvider(
		`{"signal": "planned"}`,
		`{"signal": "planned", "plan_path": "plan/v1.md"}`,
	)

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, leadLLM))

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "P_CREATE",
		SignalFields: map[string]*loomv1.SignalFields{
			"planned": {Fields: []string{"plan_path"}},
		},
		States: map[string]*loomv1.WorkflowState{
			"P_CREATE": {
				Body:  agentState("devlead", "plan-create"),
				Vocab: []string{"planned"},
				Route: map[string]*loomv1.Transition{"planned": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	pattern.WorkflowId = "fields-1"

	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])

	prompts := leadLLM.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], `What is wrong with it: "planned" came without plan_path`)
	assert.Contains(t, prompts[1], "Your answer stands. Send it again in the expected format.")
}

// A bubbling signal belongs to whoever started the run. Inside a nested body it
// parks unconditionally and rises; no inline handler is consulted, because the
// run is not entitled to decide it.
func TestStateMachine_BubblingSignalRisesToTheOwningScope(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	inner := wrap(&loomv1.StateMachinePattern{
		EntryState:    "WORK",
		BubbleSignals: []string{"hitl"},
		States: map[string]*loomv1.WorkflowState{
			"WORK": {
				Body:  agentState("coder", "do-task"),
				Vocab: []string{"done", "hitl"},
				Route: map[string]*loomv1.Transition{"done": to("TASK_DONE")},
			},
			"TASK_DONE": {Terminal: true},
		},
	})

	// An inline handler that would decide anything it is asked. It must never
	// be asked about a bubbling signal.
	handler := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{
		{Action: loomv1.GateAction_GATE_ACTION_APPROVE},
	}}

	orch := NewOrchestrator(Config{
		Logger:      zaptest.NewLogger(t),
		Tracer:      observability.NewNoOpTracer(),
		HITLHandler: handler,
	})
	orch.RegisterAgent("coder", machineAgent(t, "coder", store,
		newRecordingLLMProvider("parked for a human\nhitl")))
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, newRecordingLLMProvider()))

	outer := wrap(&loomv1.StateMachinePattern{
		EntryState: "DELIVER_TASK",
		States: map[string]*loomv1.WorkflowState{
			"DELIVER_TASK": {
				Body:  delegateTo(inner),
				Route: map[string]*loomv1.Transition{"task_done": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	outer.WorkflowId = "bubble-1"

	_, err := orch.ExecutePattern(context.Background(), outer)
	require.Error(t, err)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_NESTED, suspended.Checkpoint.SuspendReason)
	require.NotNil(t, suspended.Checkpoint.NestedCheckpoint)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_BUBBLED,
		suspended.Checkpoint.NestedCheckpoint.SuspendReason,
		"a bubbling signal parks as itself, so the owning scope can see why")
	assert.Zero(t, handler.calls,
		"the inline handler must never adjudicate a signal the machine declares as bubbling")
}

// ---------------------------------------------------------------------------
// H6 — the cost cap suspends, and approving it actually makes progress
// ---------------------------------------------------------------------------

func TestStateMachine_CostCapSuspendsAndGrantIsPersisted(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	// Each mock turn costs 0.001; a cap below that trips on the first state.
	leadLLM := newRecordingLLMProvider("a\ngo", "b\ngo", "c\nstop")

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, leadLLM))

	sm := &loomv1.StateMachinePattern{
		EntryState: "WORK",
		MaxCostUsd: 0.0005,
		States: map[string]*loomv1.WorkflowState{
			"WORK": {
				Body: agentState("devlead", "work"),
				Route: map[string]*loomv1.Transition{
					"go":   to("WORK"),
					"stop": to("DONE"),
				},
			},
			"DONE": {Terminal: true},
		},
	}
	pattern := wrap(sm)
	pattern.WorkflowId = "cap-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.Error(t, err)
	var first *WorkflowSuspended
	require.ErrorAs(t, err, &first)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_COST_CAP, first.Checkpoint.SuspendReason)

	fpBefore, err := WorkflowFingerprint(pattern)
	require.NoError(t, err)

	// Approving grants one further cap. If the grant were not persisted, the
	// resumed run would re-suspend at the same spend and never advance.
	_, err = orch.ResumeWorkflow(context.Background(), pattern, first.Checkpoint,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.Error(t, err)
	var second *WorkflowSuspended
	require.ErrorAs(t, err, &second)
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_COST_CAP, second.Checkpoint.SuspendReason)
	assert.Greater(t, len(second.Checkpoint.AllResults), len(first.Checkpoint.AllResults),
		"a granted cap must buy at least one more state, not re-suspend on the same spend")

	fpAfter, err := WorkflowFingerprint(pattern)
	require.NoError(t, err)
	assert.Equal(t, fpBefore, fpAfter,
		"granting budget is checkpoint state; editing the pattern would invalidate every checkpoint")
}

// ---------------------------------------------------------------------------
// validation — the graph is checked before a token is spent
// ---------------------------------------------------------------------------

func TestStateMachine_ValidationRejectsBrokenGraphs(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	llm := newRecordingLLMProvider("x\ngo")

	newOrch := func() *Orchestrator {
		o := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
		o.RegisterAgent("devlead", machineAgent(t, "devlead", store, llm))
		return o
	}

	tests := []struct {
		name    string
		machine *loomv1.StateMachinePattern
		wants   string
	}{
		{
			name: "entry state absent",
			machine: &loomv1.StateMachinePattern{
				EntryState: "NOPE",
				States:     map[string]*loomv1.WorkflowState{"DONE": {Terminal: true}},
			},
			wants: "entry_state",
		},
		{
			name: "route target absent",
			machine: &loomv1.StateMachinePattern{
				EntryState: "A",
				States: map[string]*loomv1.WorkflowState{
					"A":    {Body: agentState("devlead", "p"), Route: map[string]*loomv1.Transition{"go": to("GHOST")}},
					"DONE": {Terminal: true},
				},
			},
			wants: "GHOST",
		},
		{
			name: "no terminal is reachable",
			machine: &loomv1.StateMachinePattern{
				EntryState: "A",
				States: map[string]*loomv1.WorkflowState{
					"A": {Body: agentState("devlead", "p"), Route: map[string]*loomv1.Transition{"go": to("A")}},
				},
			},
			wants: "terminal",
		},
		{
			name: "non-terminal state with no body",
			machine: &loomv1.StateMachinePattern{
				EntryState: "A",
				States: map[string]*loomv1.WorkflowState{
					"A":    {Route: map[string]*loomv1.Transition{"go": to("DONE")}},
					"DONE": {Terminal: true},
				},
			},
			wants: "no body",
		},
		{
			name: "action declared with no handler registered",
			machine: &loomv1.StateMachinePattern{
				EntryState: "A",
				States: map[string]*loomv1.WorkflowState{
					"A":    {Body: actionState("publish"), Route: map[string]*loomv1.Transition{"done": to("DONE")}},
					"DONE": {Terminal: true},
				},
			},
			wants: "ToolExecutor",
		},
		{
			name: "gate revises to a state that does not exist",
			machine: &loomv1.StateMachinePattern{
				EntryState: "A",
				States: map[string]*loomv1.WorkflowState{
					"A": {
						Body:     agentState("devlead", "p"),
						Route:    map[string]*loomv1.Transition{"go": to("DONE")},
						HitlGate: &loomv1.HITLGate{ReviseTargetStageId: "GHOST"},
					},
					"DONE": {Terminal: true},
				},
			},
			wants: "GHOST",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newOrch().ExecutePattern(context.Background(), wrap(tc.machine))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wants)
		})
	}
}

// ---------------------------------------------------------------------------
// signal handling
// ---------------------------------------------------------------------------

func TestStateMachine_SignalIsTheLastLineNormalized(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, output, want string
	}{
		{"bare word", "planned", "planned"},
		{"prose above the signal", "here is my plan\n\nit is good\nplanned", "planned"},
		{"trailing punctuation", "Plan_Ready.", "plan_ready"},
		{"uppercase", "CONVERGED", "converged"},
		{"trailing blank lines", "planned\n\n\n", "planned"},
		{"empty output has no signal", "   \n\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeSignal(lastNonEmptyLine(tc.output)))
		})
	}
}

// ---------------------------------------------------------------------------
// the blast radius: sites that silently degrade without a state-machine arm
// ---------------------------------------------------------------------------

func TestStateMachine_ExtractAgentIDsSeesStates(t *testing.T) {
	t.Parallel()

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "A",
		States: map[string]*loomv1.WorkflowState{
			"A":    {Body: agentState("devlead", "p")},
			"B":    {Body: agentState("reviewer", "p")},
			"C":    {Body: actionState("publish")},
			"DONE": {Terminal: true},
		},
	})

	ids := ExtractAgentIDs(pattern)
	assert.ElementsMatch(t, []string{"devlead", "reviewer"}, ids,
		"a machine with no agent IDs would leave every caller that pre-warms agents blind")
}

func TestStateMachine_InterpolationReachesPromptsAndSuffix(t *testing.T) {
	t.Parallel()

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "A",
		States: map[string]*loomv1.WorkflowState{
			"A": {
				Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{
					AgentId:          "tester",
					Instructions:     "test {{unit}}",
					SessionKeySuffix: "{{suite}}",
				}},
				Route: map[string]*loomv1.Transition{
					"done": {Target: &loomv1.Transition_NextState{NextState: "DONE"}, Dismiss: []string{"tester-{{suite}}"}},
				},
			},
			"DONE": {Terminal: true},
		},
	})

	out := InterpolateVariables(pattern, map[string]string{"unit": "TER-419", "suite": "api"})
	st := out.GetStateMachine().GetStates()["A"]

	assert.Equal(t, "test TER-419", st.GetAgent().GetInstructions())
	assert.Equal(t, "api", st.GetAgent().GetSessionKeySuffix(),
		"an uninterpolated suffix would collapse every fanned surface onto one conversation")
	assert.Equal(t, []string{"tester-api"}, st.GetRoute()["done"].GetDismiss())
}

func TestStateMachine_GatesAreValidatedNotSilentlyAdmitted(t *testing.T) {
	t.Parallel()

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "A",
		States: map[string]*loomv1.WorkflowState{
			"A": {
				Body:     agentState("devlead", "p"),
				Route:    map[string]*loomv1.Transition{"go": to("DONE")},
				HitlGate: &loomv1.HITLGate{ReviseTargetStageId: "GHOST"},
			},
			"DONE": {Terminal: true},
		},
	})

	// hasHITLGate only ever scanned pipeline stages. If it cannot see a gate
	// hanging off a state, validateGatePlacement's default arm admits it and
	// the revise target is never checked.
	assert.True(t, hasHITLGate(pattern))
	require.Error(t, validateGatePlacement(pattern))
}

// sessionSuffix keeps one role fanned across surfaces as separate conversations.
func TestStateMachine_SessionSuffixSeparatesSuites(t *testing.T) {
	t.Parallel()

	e := &StateMachineExecutor{workflowID: "wf"}
	assert.Equal(t, "wf-tester", e.sessionID(&loomv1.AgentState{AgentId: "tester"}, nil))
	assert.Equal(t, "wf-tester-api", e.sessionID(&loomv1.AgentState{AgentId: "tester", SessionKeySuffix: "api"}, nil))
	assert.NotEqual(t,
		e.sessionID(&loomv1.AgentState{AgentId: "tester", SessionKeySuffix: "api"}, nil),
		e.sessionID(&loomv1.AgentState{AgentId: "tester", SessionKeySuffix: "ui"}, nil))
}

// ---------------------------------------------------------------------------
// H4 — a nested run parks, and resume reaches into it
// ---------------------------------------------------------------------------

// nestedTaskMachine is the sub-workflow a DELIVER_TASK state runs: one gated
// agent turn, then done.
func nestedTaskMachine() *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{
		EntryState: "WORK",
		States: map[string]*loomv1.WorkflowState{
			"WORK": {
				Body:     agentState("coder", "do-task"),
				Route:    map[string]*loomv1.Transition{"done": to("TASK_DONE")},
				HitlGate: &loomv1.HITLGate{PromptTemplate: "Review the task"},
			},
			"TASK_DONE": {Terminal: true},
		},
	})
}

func TestStateMachine_NestedRunParksAndResumesInward(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	coderLLM := newRecordingLLMProvider("task output\ndone")
	leadLLM := newRecordingLLMProvider("accepting\naccepted")

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("coder", machineAgent(t, "coder", store, coderLLM))
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, leadLLM))

	outer := wrap(&loomv1.StateMachinePattern{
		EntryState: "DELIVER_TASK",
		States: map[string]*loomv1.WorkflowState{
			"DELIVER_TASK": {
				Body:  delegateTo(nestedTaskMachine()),
				Route: map[string]*loomv1.Transition{"task_done": to("TASK_ACCEPT")},
			},
			"TASK_ACCEPT": {
				Body:  agentState("devlead", "accept-task"),
				Route: map[string]*loomv1.Transition{"accepted": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	outer.WorkflowId = "nested-1"

	// The inner machine's gate fires. The outer run must park knowing WHERE it
	// was, not merely that something failed.
	_, err := orch.ExecutePattern(context.Background(), outer)
	require.Error(t, err)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	ckpt := suspended.Checkpoint
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_NESTED, ckpt.SuspendReason)
	assert.Equal(t, []string{"DELIVER_TASK"}, ckpt.StatePath,
		"the outer position must survive an inner suspension")
	require.NotNil(t, ckpt.NestedCheckpoint, "the inner checkpoint is what makes the resume possible")
	assert.Equal(t, []string{"WORK"}, ckpt.NestedCheckpoint.StatePath)

	// Round-trip through bytes: a nested checkpoint has to survive persistence
	// like any other.
	raw, err := proto.Marshal(ckpt)
	require.NoError(t, err)
	revived := &loomv1.WorkflowCheckpoint{}
	require.NoError(t, proto.Unmarshal(raw, revived))

	// Approving delegates inward, finishes the sub-workflow, and then routes
	// the OUTER state on what the sub-workflow reported.
	result, err := orch.ResumeWorkflow(context.Background(), outer, revived,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])
	assert.Len(t, leadLLM.recordedPrompts(), 1,
		"the outer walk must continue past the nested state, not restart it")
}

// ---------------------------------------------------------------------------
// the machine is authorable as YAML, and it walks
// ---------------------------------------------------------------------------

const devleadSliceYAML = `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: devlead-plan-cycle
spec:
  type: state-machine
  entry_state: P_CREATE
  max_transitions: 50
  max_cost_usd: 400.0
  states:
    P_CREATE:
      agent:
        agent_id: devlead
        instructions: plan-create
      route:
        planned: P_REVIEW
    P_REVIEW:
      agent:
        agent_id: plan_reviewer
        instructions: plan-review
      route:
        deltas: P_CREATE
        converged:
          next_state: PUBLISH
          checkpoint: true
    PUBLISH:
      tool:
        name: publish
      route:
        published:
          next_state: DONE
          dismiss: ["*"]
    DONE:
      terminal: true
`

func TestStateMachine_LoadsFromYAMLAndWalks(t *testing.T) {
	t.Parallel()

	pattern, err := LoadWorkflowFromYAMLBytes([]byte(devleadSliceYAML))
	require.NoError(t, err)

	sm := pattern.GetStateMachine()
	require.NotNil(t, sm)
	assert.Equal(t, "P_CREATE", sm.EntryState)
	assert.Equal(t, int32(50), sm.MaxTransitions)
	assert.InDelta(t, 400.0, sm.MaxCostUsd, 0.001)

	// A bare string is the common edge; the object form carries the extras.
	assert.Equal(t, "P_REVIEW", sm.States["P_CREATE"].Route["planned"].GetNextState())
	assert.True(t, sm.States["P_REVIEW"].Route["converged"].GetCheckpoint())
	assert.Equal(t, []string{"*"}, sm.States["PUBLISH"].Route["published"].GetDismiss())
	assert.Equal(t, "publish", sm.States["PUBLISH"].GetTool().GetTool())
	assert.True(t, sm.States["DONE"].GetTerminal())

	// And it runs.
	store := machineStore(t)
	actions := newScriptedActions()
	actions.signals = map[string]string{"publish": "published"}

	orch := NewOrchestrator(Config{
		Logger:       zaptest.NewLogger(t),
		Tracer:       observability.NewNoOpTracer(),
		ToolExecutor: actions,
	})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store,
		newRecordingLLMProvider("plan v1\nplanned", "plan v2\nplanned")))
	orch.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store,
		newRecordingLLMProvider("needs work\ndeltas", "looks good\nconverged")))

	pattern.WorkflowId = "yaml-1"
	result, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])
	assert.Equal(t, []string{"publish"}, actions.actionCalls)
}

func TestStateMachine_YAMLGraphIsCheckedAtLoad(t *testing.T) {
	t.Parallel()

	const broken = `
apiVersion: loom/v1
kind: Workflow
metadata:
  name: broken
spec:
  type: state-machine
  entry_state: A
  states:
    A:
      agent:
        agent_id: devlead
        instructions: go
      route:
        done: GHOST
    DONE:
      terminal: true
`
	_, err := LoadWorkflowFromYAMLBytes([]byte(broken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GHOST",
		"a dangling route target is an authoring mistake and should cost nothing to find")
}

// The real process is machines calling machines, four deep: the cycle calls a
// phase, which calls the lead, which calls the task loop. One level working
// says nothing about four — a position lost at any depth loses the whole run.
func TestStateMachine_NestsFourDeepAndResumesFromTheBottom(t *testing.T) {
	t.Parallel()

	store := machineStore(t)

	// Level 4: the task, gated so it parks with three parents above it.
	task := wrap(&loomv1.StateMachinePattern{
		EntryState: "CODE",
		States: map[string]*loomv1.WorkflowState{
			"CODE": {
				Body:     agentState("coder", "write-code"),
				Route:    map[string]*loomv1.Transition{"code-ready": to("TASK_DONE")},
				HitlGate: &loomv1.HITLGate{PromptTemplate: "Review the code"},
			},
			"TASK_DONE": {Terminal: true},
		},
	})
	// Level 3: the lead delivers that task.
	lead := wrap(&loomv1.StateMachinePattern{
		EntryState: "DELIVER",
		States: map[string]*loomv1.WorkflowState{
			"DELIVER": {
				Body:  delegateTo(task),
				Route: map[string]*loomv1.Transition{"task_done": to("LEAD_DONE")},
			},
			"LEAD_DONE": {Terminal: true},
		},
	})
	// Level 2: the phase runs the lead.
	phase := wrap(&loomv1.StateMachinePattern{
		EntryState: "RUN_LEAD",
		States: map[string]*loomv1.WorkflowState{
			"RUN_LEAD": {
				Body:  delegateTo(lead),
				Route: map[string]*loomv1.Transition{"lead_done": to("PHASE_DONE")},
			},
			"PHASE_DONE": {Terminal: true},
		},
	})
	// Level 1: the cycle runs the phase, then closes the unit.
	cycle := wrap(&loomv1.StateMachinePattern{
		EntryState: "DEV",
		States: map[string]*loomv1.WorkflowState{
			"DEV": {
				Body:  delegateTo(phase),
				Route: map[string]*loomv1.Transition{"phase_done": to("RELEASE")},
			},
			"RELEASE": {
				Body:  agentState("releaser", "release"),
				Route: map[string]*loomv1.Transition{"released": to("DONE")},
			},
			"DONE": {Terminal: true},
		},
	})
	cycle.WorkflowId = "deep-1"

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("coder", machineAgent(t, "coder", store,
		newRecordingLLMProvider("the code\ncode-ready")))
	orch.RegisterAgent("releaser", machineAgent(t, "releaser", store,
		newRecordingLLMProvider("shipped\nreleased")))

	// The gate is four levels down. Every level above it must record where it
	// was, or the run cannot be picked up again.
	_, err := orch.ExecutePattern(context.Background(), cycle)
	require.Error(t, err)

	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)

	depth, ckpt := 0, suspended.Checkpoint
	var path []string
	for ckpt != nil {
		require.NotEmpty(t, ckpt.StatePath, "a checkpoint at depth %d lost its position", depth)
		path = append(path, ckpt.StatePath[0])
		if ckpt.SuspendReason != loomv1.SuspendReason_SUSPEND_REASON_NESTED {
			assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_GATE, ckpt.SuspendReason,
				"the innermost checkpoint is the one that actually parked")
			break
		}
		ckpt = ckpt.NestedCheckpoint
		depth++
	}
	assert.Equal(t, []string{"DEV", "RUN_LEAD", "DELIVER", "CODE"}, path,
		"every level must name the state it was in when the level below it parked")

	// Persistence has to survive the whole chain, not just the outermost frame.
	raw, err := proto.Marshal(suspended.Checkpoint)
	require.NoError(t, err)
	revived := &loomv1.WorkflowCheckpoint{}
	require.NoError(t, proto.Unmarshal(raw, revived))

	// One decision, taken at the top, has to reach the bottom, finish the task,
	// and then let all three parents carry on to their own terminals.
	result, err := orch.ResumeWorkflow(context.Background(), cycle, revived,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"],
		"the outermost machine must finish, not stop where its child did")
}
