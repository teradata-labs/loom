// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestWeave_ReplayAssistant_DisabledByDefault pins the security default:
// generation-free replay lets a caller write the assistant's side of the
// conversation, so a request carrying replay_assistant_message must be rejected
// unless the operator opted in via server.allow_assistant_override — and the
// agent must not run.
func TestWeave_ReplayAssistant_DisabledByDefault(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:                  "make me a schedule",
		ReplayAssistantMessage: "Sure — Admon works 8am-4pm on Sundays.",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Equal(t, int64(0), llm.calls.Load(), "the agent must not run when replay is rejected")
}

// TestWeave_ReplayAssistant_UsesScriptedTurn_NoLLMCall verifies the accepted
// path: with the gate open and replay_assistant_message set, the agent records
// the scripted text verbatim as the assistant turn and never calls the LLM. The
// mock LLM would return "pong" if invoked, so an assistant row holding the
// scripted text with zero LLM calls proves generation was skipped.
func TestWeave_ReplayAssistant_UsesScriptedTurn_NoLLMCall(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowAssistantOverride(true)

	const (
		sessionID = "replay-session"
		scripted  = "Admon is assigned to the 8am-4pm Day Shift on Sundays."
		userTurn  = "Can you set up the Sunday shift rotation?"
	)

	resp, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:                  userTurn,
		SessionId:              sessionID,
		ReplayAssistantMessage: scripted,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, int64(0), llm.calls.Load(),
		"the LLM must not be called for a generation-free replay turn")

	session, ok := ag.GetSession(sessionID)
	require.True(t, ok, "session should exist after replay")
	messages := session.GetMessages()

	// The user turn carries a temporal arrival-stamp prefix in the compiled
	// view (e.g. "[Tue 2026-08-18 ...] <text>"), so match on containment. The
	// assistant reply is rendered verbatim, so it must equal the scripted text
	// exactly — the mock would have written "pong" had generation run.
	var sawUser, sawScripted bool
	for _, m := range messages {
		switch m.Role {
		case "user":
			if strings.Contains(m.Content, userTurn) {
				sawUser = true
			}
		case "assistant":
			assert.Equal(t, scripted, m.Content,
				"assistant turn must be the scripted text verbatim, not the mock's generated \"pong\"")
			if m.Content == scripted {
				sawScripted = true
			}
		}
	}
	assert.True(t, sawUser, "the user turn should be persisted")
	assert.True(t, sawScripted, "the scripted assistant turn should be persisted")
}

// TestWeave_ReplayAssistant_AnchorsBothRows verifies replay composes with
// occurred_at: when both are supplied, the recorded user turn and the scripted
// assistant turn both anchor at the historical time, with no LLM call.
func TestWeave_ReplayAssistant_AnchorsBothRows(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowTimeOverride(true)
	srv.SetAllowAssistantOverride(true)

	occurred := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	const sessionID = "replay-anchor-session"

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:                  "what's my plan?",
		SessionId:              sessionID,
		ReplayAssistantMessage: "Your plan is to ship on Friday.",
		OccurredAt:             timestamppb.New(occurred),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), llm.calls.Load())

	session, ok := ag.GetSession(sessionID)
	require.True(t, ok)
	var anchored int
	for _, m := range session.GetMessages() {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		assert.True(t, m.Timestamp.Equal(occurred),
			"%s row Timestamp = %s, want the occurred_at override %s", m.Role, m.Timestamp, occurred)
		anchored++
	}
	assert.GreaterOrEqual(t, anchored, 2, "both the user turn and the scripted assistant turn should be anchored")
}

// TestWeave_ReplayAssistant_EmptyFallsThroughToGeneration verifies an empty
// override is a no-op: the request generates normally (the mock LLM is called
// and returns "pong"), so live traffic that never sets the field is unaffected.
func TestWeave_ReplayAssistant_EmptyFallsThroughToGeneration(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowAssistantOverride(true)

	const sessionID = "replay-empty-session"
	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:                  "hello",
		SessionId:              sessionID,
		ReplayAssistantMessage: "",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), llm.calls.Load(),
		"an empty replay override must fall through to a normal generated turn")
}

// TestServer_Weave_ReplayAssistant_DisabledByDefault mirrors the gating default
// for the single-agent Server.
func TestServer_Weave_ReplayAssistant_DisabledByDefault(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewServer(ag, nil)

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:                  "hello",
		ReplayAssistantMessage: "scripted",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Equal(t, int64(0), llm.calls.Load())
}

// TestWeave_ReplayAssistant_TimeOverrideDoesNotUnlockReplay pins the flag
// separation: enabling server.allow_time_override (timestamp anchoring) must
// NOT implicitly accept caller-supplied assistant content — that requires its
// own server.allow_assistant_override opt-in.
func TestWeave_ReplayAssistant_TimeOverrideDoesNotUnlockReplay(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowTimeOverride(true) // timestamp anchoring only

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:                  "make me a schedule",
		ReplayAssistantMessage: "scripted",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Equal(t, int64(0), llm.calls.Load(), "the agent must not run when replay is rejected")
}

// TestWeave_ReplayAssistant_WhitespaceOnlyRejected pins the gate/loop
// alignment: the conversation loop substitutes only non-blank scripted text,
// so a whitespace-only replay_assistant_message on an enabled server must be
// rejected up front (INVALID_ARGUMENT) rather than silently generating a real
// turn where the caller expected verbatim replay.
func TestWeave_ReplayAssistant_WhitespaceOnlyRejected(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowAssistantOverride(true)

	_, err := srv.Weave(context.Background(), &loomv1.WeaveRequest{
		Query:                  "hello",
		ReplayAssistantMessage: " \n\t ",
	})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Equal(t, int64(0), llm.calls.Load(),
		"a whitespace-only replay must be rejected, never silently generated")
}

// mockStreamWeaveStream implements loomv1.LoomService_StreamWeaveServer for
// gate tests (modeled on mockABTestStream in judge_server_test.go).
type mockStreamWeaveStream struct {
	ctx      context.Context
	mu       sync.Mutex
	progress []*loomv1.WeaveProgress
}

func (m *mockStreamWeaveStream) Send(p *loomv1.WeaveProgress) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.progress = append(m.progress, p)
	return nil
}
func (m *mockStreamWeaveStream) Context() context.Context        { return m.ctx }
func (m *mockStreamWeaveStream) SendMsg(msg interface{}) error   { return nil }
func (m *mockStreamWeaveStream) RecvMsg(msg interface{}) error   { return nil }
func (m *mockStreamWeaveStream) SetHeader(md metadata.MD) error  { return nil }
func (m *mockStreamWeaveStream) SendHeader(md metadata.MD) error { return nil }
func (m *mockStreamWeaveStream) SetTrailer(md metadata.MD)       {}

// TestMultiAgentStreamWeave_ReplayAssistant_DisabledByDefault mirrors
// TestWeave_ReplayAssistant_DisabledByDefault for the streaming entry point:
// MultiAgentServer.StreamWeave must reject a replay_assistant_message when the
// gate is off — the field must never be silently dropped (which would persist
// a generated turn where the scripted one belongs).
func TestMultiAgentStreamWeave_ReplayAssistant_DisabledByDefault(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)

	stream := &mockStreamWeaveStream{ctx: context.Background()}
	err := srv.StreamWeave(&loomv1.WeaveRequest{
		Query:                  "make me a schedule",
		ReplayAssistantMessage: "Sure — Admon works 8am-4pm on Sundays.",
	}, stream)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Equal(t, int64(0), llm.calls.Load(), "the agent must not run when replay is rejected")
}

// TestMultiAgentStreamWeave_ReplayAssistant_UsesScriptedTurn verifies the
// accepted streaming path end to end: with the gate open, StreamWeave records
// the scripted text verbatim with zero LLM calls — proving the streaming entry
// point applies the substitution rather than generating.
func TestMultiAgentStreamWeave_ReplayAssistant_UsesScriptedTurn(t *testing.T) {
	llm := &countingLLM{name: "mock", model: "mock-model"}
	ag := agent.NewAgent(&mockBackend{}, llm)
	srv := NewMultiAgentServer(map[string]*agent.Agent{"agent1": ag}, nil)
	srv.SetAllowAssistantOverride(true)

	const (
		sessionID = "replay-stream-session"
		scripted  = "Admon is assigned to the 8am-4pm Day Shift on Sundays."
	)
	stream := &mockStreamWeaveStream{ctx: context.Background()}
	err := srv.StreamWeave(&loomv1.WeaveRequest{
		Query:                  "Can you set up the Sunday shift rotation?",
		SessionId:              sessionID,
		ReplayAssistantMessage: scripted,
	}, stream)
	require.NoError(t, err)
	assert.Equal(t, int64(0), llm.calls.Load(),
		"the LLM must not be called for a generation-free streaming replay turn")

	session, ok := ag.GetSession(sessionID)
	require.True(t, ok, "session should exist after streaming replay")
	var sawScripted bool
	for _, m := range session.GetMessages() {
		if m.Role == "assistant" {
			assert.Equal(t, scripted, m.Content,
				"assistant turn must be the scripted text verbatim, not the mock's generated \"pong\"")
			if m.Content == scripted {
				sawScripted = true
			}
		}
	}
	assert.True(t, sawScripted, "the scripted assistant turn should be persisted")
}
