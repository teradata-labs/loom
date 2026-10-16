// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	llmtypes "github.com/teradata-labs/loom/pkg/types"
)

// unreachableOnce is a model that cannot be reached until someone fixes what was
// wrong — an expired credential, say — and answers from then on.
type unreachableOnce struct {
	mu    sync.Mutex
	fixed bool
	calls int
}

func (u *unreachableOnce) Chat(_ context.Context, _ []llmtypes.Message, _ []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if !u.fixed {
		return nil, errors.New("refresh cached SSO token failed")
	}
	return &llmtypes.LLMResponse{Content: "the plan\nplanned", StopReason: "stop",
		Usage: llmtypes.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
}
func (u *unreachableOnce) Name() string  { return "unreachable-once" }
func (u *unreachableOnce) Model() string { return "unreachable-once-model" }

// No run ends because a step broke. A state that cannot run is one more place a
// person takes over: the walk stops AT it, says what happened, keeps everything
// before it, and approving re-enters that state — not the run from its first.
func TestAStateThatCannotRunIsWhereTheRunWaits(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	designer := &hearingLLM{replies: []string{"the design\ndesigned"}}
	planner := &unreachableOnce{}
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: namesTheParent{absent: true}})
	orch.RegisterAgent("designer", machineAgent(t, "designer", store, designer))
	orch.RegisterAgent("planner", machineAgent(t, "planner", store, planner))
	pattern := forkPattern("error-1")

	_, err := orch.ExecutePattern(context.Background(), pattern)
	var suspended *WorkflowSuspended
	require.ErrorAs(t, err, &suspended, "a state that could not run ended the run instead of parking it")
	ckpt := suspended.Checkpoint
	assert.Equal(t, loomv1.SuspendReason_SUSPEND_REASON_ERROR, ckpt.SuspendReason)
	assert.Equal(t, []string{"PLAN"}, ckpt.StatePath, "the run waits at the state that broke")
	assert.Contains(t, ckpt.Error, "refresh cached SSO token failed")

	// A checkpoint is stored and read back by another process, like any other.
	raw, err := proto.Marshal(ckpt)
	require.NoError(t, err)
	revived := &loomv1.WorkflowCheckpoint{}
	require.NoError(t, proto.Unmarshal(raw, revived))

	planner.mu.Lock()
	planner.fixed = true
	planner.mu.Unlock()

	result, err := orch.ResumeWorkflow(context.Background(), pattern, revived,
		&loomv1.GateDecision{Action: loomv1.GateAction_GATE_ACTION_APPROVE})
	require.NoError(t, err)
	assert.Equal(t, "DONE", result.Metadata["terminal_state"])
	assert.Len(t, designer.heard, 1, "the state before the break was paid for again")
}

// A walk someone stopped is not a broken step: it is reported as stopped.
func TestAStoppedWalkIsNotParkedAsAnError(t *testing.T) {
	t.Parallel()

	store := machineStore(t)
	orch := NewOrchestrator(Config{Logger: zaptest.NewLogger(t), Tracer: observability.NewNoOpTracer(),
		ToolExecutor: namesTheParent{}})
	orch.RegisterAgent("designer", machineAgent(t, "designer", store, &unreachableOnce{}))
	orch.RegisterAgent("planner", machineAgent(t, "planner", store, &unreachableOnce{}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := orch.ExecutePattern(ctx, forkPattern("stopped-1"))
	require.Error(t, err)
	var suspended *WorkflowSuspended
	assert.False(t, errors.As(err, &suspended), "a stopped walk was parked as if it could be resumed into")
}
