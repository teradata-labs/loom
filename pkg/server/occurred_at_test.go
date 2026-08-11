// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestWeave_OccurredAt_DisabledByDefault pins the security default: a request
// carrying occurred_at must be rejected unless the operator explicitly opted
// in via server.allow_time_override — client-supplied arrival times can poison
// temporal grounding (compiled-view stamps, extraction anchoring).
func TestWeave_OccurredAt_DisabledByDefault(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:      "hello",
		OccurredAt: timestamppb.New(time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)),
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Equal(t, int64(0), llm.calls.Load(), "the agent must not run when occurred_at is rejected")
}

// TestWeave_OccurredAt_RejectsFutureTimestamp verifies future-dated overrides
// are rejected even when the override gate is enabled.
func TestWeave_OccurredAt_RejectsFutureTimestamp(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowTimeOverride(true)

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:      "hello",
		OccurredAt: timestamppb.New(time.Now().Add(2 * time.Hour)),
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestWeave_OccurredAt_AnchorsPersistedRows verifies the accepted path: every
// row persisted during the call — the user turn and the assistant reply —
// carries the override instead of the ingestion wall clock, so replayed or
// imported conversations anchor temporal grounding at the conversation's
// historical time.
func TestWeave_OccurredAt_AnchorsPersistedRows(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowTimeOverride(true)

	occurred := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	const sessionID = "occurred-at-session"

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:      "what did i deploy?",
		SessionId:  sessionID,
		OccurredAt: timestamppb.New(occurred),
	})
	require.NoError(t, err)

	session, ok := ag.GetSession(sessionID)
	require.True(t, ok, "session should exist after Weave")
	messages := session.GetMessages()
	require.NotEmpty(t, messages)

	var stamped int
	for _, m := range messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		assert.True(t, m.Timestamp.Equal(occurred),
			"%s row Timestamp = %s, want the occurred_at override %s", m.Role, m.Timestamp, occurred)
		stamped++
	}
	assert.GreaterOrEqual(t, stamped, 2, "both the user turn and the assistant reply should be anchored")
}

// TestWeave_OccurredAt_UnsetLeavesWallClock verifies the default path is
// untouched: without occurred_at, rows keep their ingestion wall-clock
// timestamps even when the override gate is enabled.
func TestWeave_OccurredAt_UnsetLeavesWallClock(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowTimeOverride(true)

	const sessionID = "wall-clock-session"
	before := time.Now().Add(-time.Minute)

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:     "hello",
		SessionId: sessionID,
	})
	require.NoError(t, err)

	session, ok := ag.GetSession(sessionID)
	require.True(t, ok)
	for _, m := range session.GetMessages() {
		if m.Role == "user" {
			assert.True(t, m.Timestamp.After(before),
				"user row keeps its wall-clock arrival time when no override is supplied")
		}
	}
}

// TestServer_Weave_OccurredAt_DisabledByDefault mirrors the multi-agent gating
// test for the single-agent Server.
func TestServer_Weave_OccurredAt_DisabledByDefault(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewServer(ag, nil)

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:      "hello",
		OccurredAt: timestamppb.New(time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)),
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}

// TestServer_Weave_OccurredAt_AnchorsPersistedRows mirrors the accepted-path
// test for the single-agent Server.
func TestServer_Weave_OccurredAt_AnchorsPersistedRows(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewServer(ag, nil)
	srv.SetAllowTimeOverride(true)

	occurred := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	const sessionID = "single-occurred-at-session"

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:      "what did i deploy?",
		SessionId:  sessionID,
		OccurredAt: timestamppb.New(occurred),
	})
	require.NoError(t, err)

	session, ok := ag.GetSession(sessionID)
	require.True(t, ok)
	for _, m := range session.GetMessages() {
		if m.Role == "user" || m.Role == "assistant" {
			assert.True(t, m.Timestamp.Equal(occurred),
				"%s row Timestamp = %s, want %s", m.Role, m.Timestamp, occurred)
		}
	}
}
