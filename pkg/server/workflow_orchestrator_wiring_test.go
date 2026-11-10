// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap"
)

// The ExecuteWorkflow orchestrator must get the server's workflow
// communication store and bus: that store is where the bounded hand-offs
// stash full outputs and where the agents' shared_memory_read tool reads.
// Before this, the gRPC path ran every workflow with no store, so stage,
// branch and position outputs were never retrievable.
func TestCreateOrchestratorWithProgress_WiresWorkflowCommunication(t *testing.T) {
	tracer := observability.NewNoOpTracer()
	logger := zap.NewNop()
	store, err := communication.NewSharedMemoryStore(tracer, logger)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, tracer, logger)

	orch := createOrchestratorWithProgress(nil, nil, nil, nil, nil, nil, store, bus)
	assert.Same(t, store, orch.SharedMemory())
	assert.Same(t, bus, orch.MessageBus())

	bare := createOrchestratorWithProgress(nil, nil, nil, nil, nil, nil, nil, nil)
	assert.Nil(t, bare.SharedMemory(), "no store configured: inline-only hand-offs, as before")
}
