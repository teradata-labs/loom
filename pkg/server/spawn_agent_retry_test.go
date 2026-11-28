// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// recoveringLLM fails every Chat call until healed. Failing until healed
// (rather than a fixed number of times) keeps the agent's own LLM handling
// from masking the failure.
type recoveringLLM struct {
	mu     sync.Mutex
	calls  int
	healed bool
}

func (m *recoveringLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if !m.healed {
		return nil, errors.New("LLM unavailable")
	}
	return &llmtypes.LLMResponse{Content: "processed"}, nil
}

func (m *recoveringLLM) Name() string  { return "recovering-llm" }
func (m *recoveringLLM) Model() string { return "recovering-model" }

func (m *recoveringLLM) heal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healed = true
}

func (m *recoveringLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// spawnedAgentOnTopic subscribes a spawned agent and a sender to topic and
// publishes one message from the sender. It returns the spawned agent context
// and the sender's subscription channel.
func spawnedAgentOnTopic(t *testing.T, srv *MultiAgentServer, ag *agent.Agent, topic string) (*spawnedAgentContext, <-chan *loomv1.BusMessage) {
	t.Helper()
	ctx := context.Background()

	const workerID = "spawned-worker"
	_, err := srv.messageBus.Subscribe(ctx, workerID, topic, nil, 10)
	require.NoError(t, err)
	senderSub, err := srv.messageBus.Subscribe(ctx, "sender", topic, nil, 10)
	require.NoError(t, err)

	_, _, err = srv.messageBus.Publish(ctx, topic, &loomv1.BusMessage{
		Id:        "msg-1",
		Topic:     topic,
		FromAgent: "sender",
		Payload:   &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("please handle this")}},
		Timestamp: time.Now().UnixMilli(),
	})
	require.NoError(t, err)

	return &spawnedAgentContext{
		subAgentID:   workerID,
		subSessionID: "spawned-worker-session",
		agent:        ag,
	}, senderSub.Channel
}

// replyTo returns the first message on ch that replies to msgID, or nil.
func replyTo(ch <-chan *loomv1.BusMessage, msgID string) *loomv1.BusMessage {
	for {
		select {
		case m := <-ch:
			if m.GetMetadata()["in_reply_to"] == msgID {
				return m
			}
		default:
			return nil
		}
	}
}

func userRowCount(t *testing.T, ag *agent.Agent, sessionID string) int {
	t.Helper()
	sess, ok := ag.GetSession(sessionID)
	require.True(t, ok, "the spawned session should exist")
	n := 0
	for _, m := range sess.GetMessages() {
		if m.Role == "user" {
			n++
		}
	}
	return n
}

// A Chat that fails after the agent's own LLM retries is not run again (that
// would store the prompt twice and could re-run tools). The sender gets an
// error reply instead of silence (#532), and the loss is logged once at ERROR
// with the message ID.
func TestProcessSpawnedAgentMessages_FailedChatSendsErrorReply(t *testing.T) {
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	core, logs := observer.New(zap.ErrorLevel)
	srv.logger = zap.New(core)

	// Measure what one failing turn costs in LLM calls (the agent's own
	// retries), so the test can prove exactly one turn ran.
	probe := &recoveringLLM{}
	_, err := agent.NewAgent(&mockBackend{}, probe).Chat(context.Background(), "probe", "x")
	require.Error(t, err)
	callsPerTurn := probe.callCount()
	require.Positive(t, callsPerTurn)

	llm := &recoveringLLM{} // never healed
	ag := agent.NewAgent(&mockBackend{}, llm)
	spawned, senderCh := spawnedAgentOnTopic(t, srv, ag, "fail-topic")

	srv.processSpawnedAgentMessages(context.Background(), spawned)

	assert.Equal(t, callsPerTurn, llm.callCount(), "exactly one turn must run")
	assert.Equal(t, 1, userRowCount(t, ag, spawned.subSessionID), "the prompt must be stored once")

	reply := replyTo(senderCh, "msg-1")
	require.NotNil(t, reply, "the sender must be told the message was not processed")
	assert.Equal(t, spawnedOutcomeFailed, reply.GetMetadata()["error"])
	assert.Equal(t, "spawned-worker", reply.GetFromAgent())
	assert.Contains(t, string(reply.GetPayload().GetValue()), "msg-1")

	errs := logs.FilterMessage("Spawned agent could not process message; sending an error reply").All()
	require.Len(t, errs, 1)
	assert.Equal(t, "msg-1", errs[0].ContextMap()["message_id"])
	assert.Len(t, logs.All(), 1, "no other ERROR")
}

// A busy session refuses the prompt before the turn touches the conversation.
// That refusal is the one failure the agent's retry layers cannot absorb, so
// it is waited out: the turn then runs once and the reply is published.
func TestProcessSpawnedAgentMessages_WaitsOutABusySession(t *testing.T) {
	stores := newResumeStores(t)
	llm := &injectionLLM{}
	ag := stores.agent(llm, nil)
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	srv.injectBusy = injectBusyPolicy{initial: 10 * time.Millisecond, max: 20 * time.Millisecond, total: 10 * time.Second}
	spawned, senderCh := spawnedAgentOnTopic(t, srv, ag, "busy-topic")

	release := stores.leaseSession(t, spawned.subSessionID, "user-turn")
	time.AfterFunc(200*time.Millisecond, release)

	srv.processSpawnedAgentMessages(context.Background(), spawned)

	assert.Equal(t, 1, llm.saw("please handle this"), "the message must reach the session exactly once")
	assert.Equal(t, 1, userRowCount(t, ag, spawned.subSessionID))
	reply := replyTo(senderCh, "msg-1")
	require.NotNil(t, reply)
	assert.Equal(t, "ok", string(reply.GetPayload().GetValue()))
	assert.Empty(t, reply.GetMetadata()["error"])
}

// A stopping agent ends the batch: no Chat, no error reply, no ERROR (#540).
func TestProcessSpawnedAgentMessages_StopsWhenCancelled(t *testing.T) {
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	core, logs := observer.New(zap.ErrorLevel)
	srv.logger = zap.New(core)
	llm := &recoveringLLM{}
	spawned, senderCh := spawnedAgentOnTopic(t, srv, agent.NewAgent(&mockBackend{}, llm), "cancel-topic")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv.processSpawnedAgentMessages(ctx, spawned)

	assert.Zero(t, llm.callCount())
	assert.Nil(t, replyTo(senderCh, "msg-1"))
	assert.Empty(t, logs.All())
}

func TestSpawnedChatOutcome(t *testing.T) {
	live := context.Background()
	stopped, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"success", live, nil, spawnedOutcomeReplied},
		// A parked turn finishes through ResumeChat: not a failure, no reply now.
		{"turn parked", live, fmt.Errorf("chat: %w", &agent.TurnParkedError{RequestID: "hr-1"}), spawnedOutcomeParked},
		{"agent stopping", stopped, context.Canceled, spawnedOutcomeCancelled},
		{"session stayed busy", live, fmt.Errorf("%w: %w", errInjectNotDelivered, agent.ErrSessionBusy), spawnedOutcomeNotDelivered},
		{"session parked", live, &agent.SessionParkedError{RequestID: "hr-1"}, spawnedOutcomeFailed},
		{"turn timed out", live, context.DeadlineExceeded, spawnedOutcomeFailed},
		{"llm error", live, errors.New("LLM unavailable"), spawnedOutcomeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, spawnedChatOutcome(tc.ctx, tc.err))
		})
	}
}
