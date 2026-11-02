// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// A seat serves several states in one conversation. Each state's instructions
// arrive the first time the seat reaches that state, even though the
// conversation already exists: "first" belongs to the state, not to the
// conversation.
func TestASeatsNextStateBringsThatStatesInstructions(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	lead := newRecordingLLMProvider("did a\nnext", "did b\nfinish")
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, lead))

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "A",
		States: map[string]*loomv1.WorkflowState{
			"A":    {Body: agentState("devlead", "first-job"), Route: map[string]*loomv1.Transition{"next": to("B")}},
			"B":    {Body: agentState("devlead", "second-job"), Route: map[string]*loomv1.Transition{"finish": to("DONE")}},
			"DONE": {Terminal: true},
		},
	})
	pattern.WorkflowId = "next-state-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	prompts := lead.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[0], "Step A\n\nfirst-job")
	assert.Contains(t, prompts[1], "Step B\n\nsecond-job", "the seat's second state is a first entry into that state")
	assert.NotContains(t, prompts[1], "Back at step", "reaching a new state is not a return")
}

// A marker names one state exactly: a state whose name begins with another's
// is still a first entry.
func TestAStateWhoseNameExtendsAnothersIsStillAFirstEntry(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	lead := newRecordingLLMProvider("wrote\nnext", "reviewed\nfinish")
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, lead))

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState: "S_ARCH",
		States: map[string]*loomv1.WorkflowState{
			"S_ARCH":        {Body: agentState("devlead", "write-arch"), Route: map[string]*loomv1.Transition{"next": to("S_ARCH_REVIEW")}},
			"S_ARCH_REVIEW": {Body: agentState("devlead", "review-arch"), Route: map[string]*loomv1.Transition{"finish": to("DONE")}},
			"DONE":          {Terminal: true},
		},
	})
	pattern.WorkflowId = "prefix-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	prompts := lead.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "Step S_ARCH_REVIEW\n\nreview-arch")
}

// Every turn of a step carries its instructions: a return that brings nothing
// — the sender's reply was only its signal line — is the step's instructions
// alone.
func TestEveryTurnCarriesItsStepsInstructions(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	lead := newRecordingLLMProvider("plan v1\nplanned", "plan v1 stands\nplanned")
	reviewer := newRecordingLLMProvider("deltas", "converged")
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, lead))
	orch.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store, reviewer))

	pattern := wrap(planCycle(nil))
	pattern.WorkflowId = "nothing-new-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	prompts := lead.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "Step P_CREATE\n\nplan-create")
	assert.NotContains(t, prompts[1], "message in flight")
}

// A person's revision at a gate reaches the returning seat as feedback, after
// the step's instructions.
func TestAGateRevisionReachesTheReturningSeatAsFeedback(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	handler := &scriptedHITLHandler{decisions: []*loomv1.GateDecision{
		{Action: loomv1.GateAction_GATE_ACTION_REVISE, Feedback: "split task 3"},
		{Action: loomv1.GateAction_GATE_ACTION_APPROVE},
	}}
	lead := newRecordingLLMProvider("plan v1\nplanned", "plan v2\nplanned")
	reviewer := newRecordingLLMProvider("looks fine\nconverged", "looks fine\nconverged")
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(), HITLHandler: handler})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, lead))
	orch.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store, reviewer))

	sm := planCycle(&loomv1.HITLGate{PromptTemplate: "Review the plan", ReviseTargetStageId: "P_CREATE"})
	pattern := wrap(sm)
	pattern.WorkflowId = "gate-revise-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	prompts := lead.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "Step P_CREATE\n\nplan-create")
	assert.Contains(t, prompts[1], "split task 3")
}

// A dismissed seat's history stays in storage and comes back on its next
// entry, which carries the step's instructions and what came back.
func TestADismissedSeatReturnsToItsStoredConversation(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	lead := newRecordingLLMProvider("plan v1\nplanned", "plan v2\nplanned")
	reviewer := newRecordingLLMProvider("needs work\ndeltas", "looks good\nconverged")
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("devlead", machineAgent(t, "devlead", store, lead))
	orch.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store, reviewer))

	sm := planCycle(nil)
	sm.States["P_REVIEW"].Route["deltas"] = &loomv1.Transition{
		Target:  &loomv1.Transition_NextState{NextState: "P_CREATE"},
		Dismiss: []string{DismissAll},
	}
	pattern := wrap(sm)
	pattern.WorkflowId = "dismiss-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	prompts := lead.recordedPrompts()
	require.Len(t, prompts, 2)
	assert.Contains(t, prompts[1], "Step P_CREATE\n\nplan-create")
	assert.Contains(t, prompts[1], "needs work")
}

// When the message that carried a state's instructions has been folded out of
// the live conversation, the seat no longer holds them, so its next entry gets
// them again.
func TestFoldedInstructionsAreSentAgain(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	orch, lead, _ := planCycleOrchestrator(t, store, nil)

	sm := planCycle(&loomv1.HITLGate{PromptTemplate: "Review the plan"})
	pattern := wrap(sm)
	pattern.WorkflowId = "fold-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended)
	require.Len(t, lead.recordedPrompts(), 1)

	// Fold the lead's instructions message away.
	ctx := context.Background()
	msgs, err := store.LoadMessages(ctx, "fold-1-devlead")
	require.NoError(t, err)
	var seqs []int64
	for _, m := range msgs {
		if m.Role == "user" && strings.HasPrefix(m.Content, "Step P_CREATE\n\n") {
			id, err := strconv.ParseInt(m.ID, 10, 64)
			require.NoError(t, err)
			seqs = append(seqs, id)
		}
	}
	require.Len(t, seqs, 1, "the lead's first entry is stored with its marker")
	require.NoError(t, store.FoldMessages(ctx, "fold-1-devlead", seqs, 1, "summary of the plan turn"))

	orch2 := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	lead2 := newRecordingLLMProvider("plan v2\nplanned")
	orch2.RegisterAgent("devlead", machineAgent(t, "devlead", store, lead2))
	orch2.RegisterAgent("plan_reviewer", machineAgent(t, "plan_reviewer", store, newRecordingLLMProvider("looks good\nconverged")))

	raw, err := proto.Marshal(suspended.Checkpoint)
	require.NoError(t, err)
	revived := &loomv1.WorkflowCheckpoint{}
	require.NoError(t, proto.Unmarshal(raw, revived))

	_, _ = orch2.ResumeWorkflow(ctx, pattern, revived, &loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})

	prompts := lead2.recordedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "Step P_CREATE\n\nplan-create", "folded instructions are no longer in front of the seat")
}

// A spec written with the two-prompt keys is refused at load, by name, so a
// stale file never walks with its text silently missing.
func TestASpecWithTheOldPromptKeysIsRefused(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"mint_prompt", "resume_prompt"} {
		spec := []byte(`apiVersion: loom/v1
kind: Workflow
metadata:
  name: stale
spec:
  type: state-machine
  entry_state: A
  states:
    A:
      agent:
        agent_id: devlead
        ` + key + `: plan it
      route:
        planned: DONE
    DONE:
      terminal: true
`)
		_, err := LoadWorkflowFromYAMLBytes(spec)
		require.Error(t, err, key)
		assert.Contains(t, err.Error(), key)
		assert.Contains(t, err.Error(), "instructions")
	}
}
