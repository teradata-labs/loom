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
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
)

// TestIterativeRestartSubscriber_ExitsWhenBusClosesChannel covers the restart
// listener when the bus closes its channel while the run's context is still
// live (bus shutdown, or the subscription removed from under it). The listener
// used to read the closed channel's nil message and panic dereferencing its
// payload; it must exit instead.
func TestIterativeRestartSubscriber_ExitsWhenBusClosesChannel(t *testing.T) {
	tracer, logger := observability.NewNoOpTracer(), zaptest.NewLogger(t)
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)
	orch := NewOrchestrator(Config{Logger: logger, Tracer: tracer, MessageBus: bus})
	pattern := &loomv1.IterativeWorkflowPattern{
		Pipeline:      &loomv1.PipelinePattern{Stages: []*loomv1.PipelineStage{{AgentId: "s1"}}},
		RestartPolicy: &loomv1.RestartPolicy{Enabled: true},
	}
	exec := NewIterativePipelineExecutor(orch, pattern, bus, "wf-close")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec.subscribeToRestarts(ctx, communication.RestartTopicFor("wf-close"))

	require.NoError(t, bus.Close())

	done := make(chan struct{})
	go func() {
		exec.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart listener did not exit after the bus closed its channel")
	}
}
