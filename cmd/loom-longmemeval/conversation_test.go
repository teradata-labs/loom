// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// recordingClient is a LoomServiceClient stub that records Weave requests and
// hands back a fixed response. Embedding the interface satisfies every method
// (unused ones would panic if called) while we override only what
// runConversationWith touches: CreateSession and Weave.
type recordingClient struct {
	loomv1.LoomServiceClient
	weaves []*loomv1.WeaveRequest
}

func (c *recordingClient) CreateSession(_ context.Context, _ *loomv1.CreateSessionRequest, _ ...grpc.CallOption) (*loomv1.Session, error) {
	return &loomv1.Session{Id: "conv-session"}, nil
}

func (c *recordingClient) Weave(_ context.Context, in *loomv1.WeaveRequest, _ ...grpc.CallOption) (*loomv1.WeaveResponse, error) {
	c.weaves = append(c.weaves, in)
	return &loomv1.WeaveResponse{Text: "ok"}, nil
}

// TestRunConversationWith_ReplaysPairsGenerationFree verifies that conversation
// mode walks a session's alternating turns as (user, assistant) pairs and
// replays each generation-free — the user text as the query and the original
// assistant text as replay_assistant_message — then asks the question as a
// normal generating turn (no scripted response).
func TestRunConversationWith_ReplaysPairsGenerationFree(t *testing.T) {
	client := &recordingClient{}
	r := &Runner{
		config: RunConfig{Mode: ModeConversation},
		logger: zap.NewNop(),
		client: client,
	}
	entry := Entry{
		QuestionID:   "q1",
		Question:     "what did I say?",
		QuestionDate: "2023/05/01 (Mon) 10:00",
	}
	sessions := []SessionWithDate{{
		Date: "2023/04/01",
		Turns: []Turn{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: "a1"},
			{Role: "user", Content: "u2"},
			{Role: "assistant", Content: "a2"},
		},
	}}

	res := r.runConversationWith(context.Background(), entry, sessions, EntryResult{}, "", time.Time{})

	require.Empty(t, res.Error)
	assert.Equal(t, "ok", res.Hypothesis)
	require.Len(t, client.weaves, 3, "two replay turns + one question turn")

	assert.Equal(t, "u1", client.weaves[0].Query)
	assert.Equal(t, "a1", client.weaves[0].ReplayAssistantMessage)
	assert.Equal(t, "u2", client.weaves[1].Query)
	assert.Equal(t, "a2", client.weaves[1].ReplayAssistantMessage)

	// The final turn generates the answer: no scripted response, and it carries
	// the question text.
	assert.Empty(t, client.weaves[2].ReplayAssistantMessage,
		"the question turn must generate, not replay")
	assert.Contains(t, client.weaves[2].Query, "what did I say?")
}

// TestRunConversationWith_HandlesTurnAnomalies verifies robustness on
// non-strictly-alternating sessions: a leading assistant turn (no preceding
// user) is skipped, and a trailing user turn with no assistant reply is sent as
// a normal generating turn rather than replayed with invented assistant text.
func TestRunConversationWith_HandlesTurnAnomalies(t *testing.T) {
	client := &recordingClient{}
	r := &Runner{
		config: RunConfig{Mode: ModeConversation},
		logger: zap.NewNop(),
		client: client,
	}
	entry := Entry{QuestionID: "q2", Question: "q?", QuestionDate: "2023/05/01 (Mon) 10:00"}
	sessions := []SessionWithDate{{
		Turns: []Turn{
			{Role: "assistant", Content: "orphan"}, // leading assistant → skipped
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: "a1"}, // paired → replayed
			{Role: "user", Content: "u2"},      // trailing user → generated
		},
	}}

	res := r.runConversationWith(context.Background(), entry, sessions, EntryResult{}, "", time.Time{})
	require.Empty(t, res.Error)

	require.Len(t, client.weaves, 3, "one replayed pair + one generated trailing user + one question")

	// The orphan assistant turn never reaches the wire.
	for _, w := range client.weaves {
		assert.NotEqual(t, "orphan", w.ReplayAssistantMessage)
	}
	assert.Equal(t, "u1", client.weaves[0].Query)
	assert.Equal(t, "a1", client.weaves[0].ReplayAssistantMessage)

	// Trailing user turn generates (no invented assistant content).
	assert.Equal(t, "u2", client.weaves[1].Query)
	assert.Empty(t, client.weaves[1].ReplayAssistantMessage)

	// Question turn.
	assert.Empty(t, client.weaves[2].ReplayAssistantMessage)
	assert.Contains(t, client.weaves[2].Query, "q?")
}
