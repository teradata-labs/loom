// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// A seat is told which signals its state may end on at the end of every turn —
// the first, and every one after. The set is the spec's, and the way out the
// engine gives every seat: a prompt that leaves it out, or a resumed turn whose
// prompt is one line, still ends on a legal word.
func TestEveryAgentTurnEndsWithItsStatesLegalSignals(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	llm := newRecordingLLMProvider(
		"first look\n"+`{"signal":"deltas"}`,
		"second look\n"+`{"signal":"bounce","problem":"the plan names no repo"}`,
	)
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer()})
	orch.RegisterAgent("reviewer", machineAgent(t, "reviewer", store, llm))

	pattern := wrap(&loomv1.StateMachinePattern{
		EntryState:     "REVIEW",
		LoopbackStates: []string{"REVIEW"},
		SignalFields: map[string]*loomv1.SignalFields{
			"bounce": {Fields: []string{"problem"}},
		},
		States: map[string]*loomv1.WorkflowState{
			"REVIEW": {
				Body:  agentState("reviewer", "review the plan"),
				Vocab: []string{"converged", "deltas", "bounce"},
				Route: map[string]*loomv1.Transition{
					"converged": {Target: &loomv1.Transition_NextState{NextState: "DONE"}},
					"deltas":    {Target: &loomv1.Transition_NextState{NextState: "REVIEW"}},
					"bounce":    {Target: &loomv1.Transition_NextState{NextState: "DONE"}},
				},
			},
			"DONE": {Terminal: true},
		},
	})
	pattern.WorkflowId = "contract-1"

	_, err := orch.ExecutePattern(context.Background(), pattern)
	require.NoError(t, err)

	prompts := llm.recordedPrompts()
	require.Len(t, prompts, 2)
	for i, p := range prompts {
		tail := p[strings.LastIndex(p, "== SIGNAL CONTRACT =="):]
		assert.Contains(t, tail, `["bounce", "converged", "deltas", "escalate"]`, "turn %d", i+1)
		assert.Contains(t, tail, `With "bounce" the same object also carries: ["problem"].`, "turn %d", i+1)
		assert.True(t, strings.HasSuffix(p, tail), "the contract is the last thing turn %d reads", i+1)
	}
	assert.Contains(t, prompts[1], "Step REVIEW\n\nreview the plan", "every turn carries the step's instructions")
}
