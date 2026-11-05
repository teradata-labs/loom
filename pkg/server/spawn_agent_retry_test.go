// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func setSpawnedChatRetryDelay(t *testing.T, delay func(attempt int) time.Duration) {
	t.Helper()
	orig := spawnedChatRetryDelay
	spawnedChatRetryDelay = delay
	t.Cleanup(func() { spawnedChatRetryDelay = orig })
}

// spawnedAgentOnTopic subscribes a spawned agent and a sender to topic and
// publishes one message from the sender. It returns the spawned agent context
// and the sender's subscription channel.
func spawnedAgentOnTopic(t *testing.T, srv *MultiAgentServer, llm *recoveringLLM, topic string) (*spawnedAgentContext, <-chan *loomv1.BusMessage) {
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
		agent:        agent.NewAgent(&mockBackend{}, llm),
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

// TestProcessSpawnedAgentMessages_RetriesFailedChat guards against a bus
// message being dropped when the spawned agent's Chat fails once: the message
// must be retried and its reply published to the sender.
func TestProcessSpawnedAgentMessages_RetriesFailedChat(t *testing.T) {
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	llm := &recoveringLLM{}
	spawned, senderCh := spawnedAgentOnTopic(t, srv, llm, "retry-topic")

	var retries []int
	setSpawnedChatRetryDelay(t, func(attempt int) time.Duration {
		retries = append(retries, attempt)
		llm.heal() // the LLM recovers before the retry
		return time.Millisecond
	})

	srv.processSpawnedAgentMessages(context.Background(), spawned)

	assert.Equal(t, []int{1}, retries, "the failed Chat should be retried once")
	reply := replyTo(senderCh, "msg-1")
	require.NotNil(t, reply, "the retried message should get a reply")
	assert.Equal(t, "spawned-worker", reply.GetFromAgent())
	assert.Equal(t, "processed", string(reply.GetPayload().GetValue()))
}

// TestProcessSpawnedAgentMessages_GivesUpAfterMaxAttempts checks the retry is
// bounded: a Chat that keeps failing is tried spawnedChatMaxAttempts times and
// no reply is published.
func TestProcessSpawnedAgentMessages_GivesUpAfterMaxAttempts(t *testing.T) {
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	llm := &recoveringLLM{}
	spawned, senderCh := spawnedAgentOnTopic(t, srv, llm, "giveup-topic")

	var retries []int
	setSpawnedChatRetryDelay(t, func(attempt int) time.Duration {
		retries = append(retries, attempt)
		return time.Millisecond
	})

	srv.processSpawnedAgentMessages(context.Background(), spawned)

	assert.Equal(t, []int{1, 2}, retries, "Chat should be tried spawnedChatMaxAttempts times")
	assert.Nil(t, replyTo(senderCh, "msg-1"), "no reply should be published when every attempt fails")
	assert.Positive(t, llm.callCount())
}

// TestProcessSpawnedAgentMessages_StopsRetryingWhenCancelled checks a cancelled
// context ends the retry loop instead of waiting out the backoff.
func TestProcessSpawnedAgentMessages_StopsRetryingWhenCancelled(t *testing.T) {
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	llm := &recoveringLLM{}
	spawned, senderCh := spawnedAgentOnTopic(t, srv, llm, "cancel-topic")

	ctx, cancel := context.WithCancel(context.Background())
	setSpawnedChatRetryDelay(t, func(int) time.Duration {
		cancel()
		return time.Hour
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.processSpawnedAgentMessages(ctx, spawned)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("processSpawnedAgentMessages kept waiting after the context was cancelled")
	}
	assert.Nil(t, replyTo(senderCh, "msg-1"))
}
