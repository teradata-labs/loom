// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// escalatedToManager is a worker that cannot go on, and the manager it
// escalates to, which settles it.
func escalatedToManager() *loomv1.WorkflowPattern {
	return wrap(&loomv1.StateMachinePattern{EntryState: "WORK", States: map[string]*loomv1.WorkflowState{
		"WORK": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "worker"}},
			Vocab: []string{"escalate"}, Route: map[string]*loomv1.Transition{"escalate": to("MANAGE")}},
		"MANAGE": {Body: &loomv1.WorkflowState_Agent{Agent: &loomv1.AgentState{AgentId: "manager"}},
			Vocab: []string{"close"}, Route: map[string]*loomv1.Transition{"close": to("END")}},
		"END": {Terminal: true},
	}})
}

// A seat the run was given a conversation for takes its turns in that
// conversation — the one that already happened, with everything said in it —
// and in no conversation of its own.
func TestASeatGivenAConversationTakesItsTurnsThere(t *testing.T) {
	store := machineStore(t)
	manager := &hearingLLM{replies: []string{"planned", "settled\nclose"}}
	managerAgent := machineAgent(t, "manager", store, manager)
	ctx := context.Background()
	_, err := managerAgent.Chat(ctx, "planning", "plan the greeter")
	require.NoError(t, err)

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("worker", costedAgent(t, "worker", store, call{say: "stuck\nescalate"}))
	orch.RegisterAgent("manager", managerAgent)
	p := escalatedToManager()
	p.WorkflowId = "seated"

	_, err = orch.ExecutePattern(WithRunVars(ctx, map[string]string{SessionVarPrefix + "manager": "planning"}), p)
	require.NoError(t, err)
	require.Len(t, manager.heard, 2)
	require.Contains(t, manager.heard[1], "plan the greeter", "the manager's step did not see the conversation it was seated in")
	own, err := managerAgent.SessionExists(ctx, "seated-manager")
	require.NoError(t, err)
	require.False(t, own, "the manager took its step in a conversation of its own")
}

// A step whose seat's conversation is taking a turn elsewhere — the person
// talking with the manager — waits for it and then goes on. It does not stop
// the run.
func TestAStepWaitsForItsSeatsConversation(t *testing.T) {
	was := seatBusyRetry
	seatBusyRetry = 10 * time.Millisecond
	t.Cleanup(func() { seatBusyRetry = was })

	store := machineStore(t)
	manager := &hearingLLM{replies: []string{"planned", "settled\nclose"}}
	managerAgent := machineAgent(t, "manager", store, manager)
	ctx := context.Background()
	_, err := managerAgent.Chat(ctx, "planning", "plan the greeter")
	require.NoError(t, err)
	claim, err := store.TakeTurn(ctx, "planning", "1@elsewhere#1", "", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, claim.Taken)

	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("worker", costedAgent(t, "worker", store, call{say: "stuck\nescalate"}))
	orch.RegisterAgent("manager", managerAgent)
	p := escalatedToManager()
	p.WorkflowId = "waits"

	done := make(chan error, 1)
	go func() {
		_, err := orch.ExecutePattern(WithRunVars(ctx, map[string]string{SessionVarPrefix + "manager": "planning"}), p)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the step went ahead while its seat's conversation was busy: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	require.NoError(t, store.EndTurn(ctx, "planning", "1@elsewhere#1"))
	select {
	case err := <-done:
		require.NoError(t, err, "the run stopped rather than waiting its turn")
	case <-time.After(5 * time.Second):
		t.Fatal("the step never took its turn once the conversation was free")
	}
	require.Len(t, manager.heard, 2)
}
