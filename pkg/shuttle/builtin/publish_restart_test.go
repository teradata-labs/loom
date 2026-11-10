// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/communication"
	"github.com/teradata-labs/loom/pkg/observability"
	"go.uber.org/zap/zaptest"
)

// The tool publishes to the running pipeline's own topic, taken from the
// turn's context, so on a bus shared by many runs a request reaches the run
// that made it and no other.
func TestPublishRestartTool_PublishesToTheRunsTopicFromContext(t *testing.T) {
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	ctx := context.Background()
	own, err := bus.Subscribe(ctx, "run-a", communication.RestartTopicFor("run-a"), nil, 4)
	require.NoError(t, err)
	other, err := bus.Subscribe(ctx, "run-b", communication.RestartTopicFor("run-b"), nil, 4)
	require.NoError(t, err)
	legacy, err := bus.Subscribe(ctx, "legacy", "workflow.restart", nil, 4)
	require.NoError(t, err)

	tool := NewPublishRestartTool(bus, "analyst")
	res, err := tool.Execute(communication.WithRestartTopic(ctx, communication.RestartTopicFor("run-a")), map[string]interface{}{
		"target_stage_id": "discover",
		"reason":          "missing event",
	})
	require.NoError(t, err)
	require.True(t, res.Success, "%+v", res.Error)
	data := res.Data.(map[string]interface{})
	assert.Equal(t, "workflow.restart.run-a", data["topic"])
	assert.Equal(t, 1, data["published_count"])

	select {
	case msg := <-own.Channel:
		assert.Equal(t, "workflow.restart.run-a", msg.Topic)
		var req loomv1.RestartRequest
		require.NoError(t, json.Unmarshal(msg.Payload.GetValue(), &req))
		assert.Equal(t, "analyst", req.RequesterStageId)
		assert.Equal(t, "discover", req.TargetStageId)
	case <-time.After(2 * time.Second):
		t.Fatal("the run's own subscription received nothing")
	}
	select {
	case msg := <-other.Channel:
		t.Fatalf("another run received the request: %s", msg.Topic)
	case msg := <-legacy.Channel:
		t.Fatalf("the unscoped topic received the request: %s", msg.Topic)
	case <-time.After(100 * time.Millisecond):
	}
}

// Outside an iterative run with restarts enabled there is no topic on the
// context; the tool says so instead of publishing into the void.
func TestPublishRestartTool_RefusesOutsideARun(t *testing.T) {
	bus := communication.NewMessageBus(communication.NewMemoryStore(0), nil, observability.NewNoOpTracer(), zaptest.NewLogger(t))
	legacy, err := bus.Subscribe(context.Background(), "legacy", "workflow.restart", nil, 4)
	require.NoError(t, err)

	res, err := NewPublishRestartTool(bus, "analyst").Execute(context.Background(), map[string]interface{}{
		"target_stage_id": "discover",
		"reason":          "missing event",
	})
	require.NoError(t, err)
	require.False(t, res.Success)
	require.NotNil(t, res.Error)
	assert.Equal(t, "RESTART_NOT_ACTIVE", res.Error.Code)
	select {
	case msg := <-legacy.Channel:
		t.Fatalf("published without a run: %s", msg.Topic)
	case <-time.After(100 * time.Millisecond):
	}
}
