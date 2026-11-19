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
	"github.com/teradata-labs/loom/pkg/communication"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// flakyLLM fails every Chat call until healed, recording the last message it
// was given on every call. Failing until healed (rather than once) keeps the
// agent's own LLM retries from masking the failure.
type flakyLLM struct {
	mu     sync.Mutex
	calls  int
	healed bool
	seen   []string
}

func (m *flakyLLM) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if len(messages) > 0 {
		m.seen = append(m.seen, messages[len(messages)-1].Content)
	}
	if !m.healed {
		return nil, errors.New("LLM unavailable")
	}
	return &llmtypes.LLMResponse{Content: "done"}, nil
}

func (m *flakyLLM) Name() string  { return "flaky-llm" }
func (m *flakyLLM) Model() string { return "flaky-model" }

func (m *flakyLLM) heal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healed = true
}

func (m *flakyLLM) snapshot() (int, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, append([]string{}, m.seen...)
}

// TestRunWorkflowSubAgent_RequeuesMessagesOnChatError guards against messages
// being stranded in-flight when the sub-agent's Chat fails: they must return to
// pending so a later notification redelivers them.
func TestRunWorkflowSubAgent_RequeuesMessagesOnChatError(t *testing.T) {
	llm := &flakyLLM{}
	subAgent := agent.NewAgent(&mockBackend{}, llm)

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	queue := srv.messageQueue
	require.NotNil(t, queue)

	const agentID = "wf:worker"
	ctx, cancel := context.WithCancel(context.Background())
	notifyChan := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runWorkflowSubAgent(ctx, subAgent, agentID, "coord-session:"+agentID, "worker-session", "wf", notifyChan)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	msgID, err := queue.Send(context.Background(), "wf", agentID, "task",
		&loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("do the thing")}}, nil)
	require.NoError(t, err)

	// First delivery: Chat fails, the message must go back to pending.
	notifyChan <- struct{}{}
	require.Eventually(t, func() bool {
		calls, _ := llm.snapshot()
		return calls >= 1 && queue.GetQueueDepth(agentID) == 1
	}, 5*time.Second, 10*time.Millisecond, "message should be requeued after a failed Chat")

	failedCalls, _ := llm.snapshot()
	// After the failed turn the session already holds the user row Chat
	// persisted before the LLM error. Count it so the healed retry can prove
	// it did not append a second copy.
	session, ok := subAgent.GetSession("worker-session")
	require.True(t, ok, "failed Chat should have created the worker session")
	userRowsBeforeRetry := 0
	for _, m := range session.GetMessages() {
		if m.Role == "user" {
			userRowsBeforeRetry++
		}
	}
	require.Equal(t, 1, userRowsBeforeRetry, "failed Chat should leave exactly one user row")

	// Second delivery once the LLM recovers: the same message is redelivered
	// and acknowledged without duplicating the user row.
	llm.heal()
	notifyChan <- struct{}{}
	require.Eventually(t, func() bool {
		calls, _ := llm.snapshot()
		return calls > failedCalls && queue.GetQueueDepth(agentID) == 0
	}, 5*time.Second, 10*time.Millisecond, "requeued message should be redelivered")

	_, seen := llm.snapshot()
	assert.Contains(t, seen[len(seen)-1], "do the thing", "the retry should carry the original message")

	session, ok = subAgent.GetSession("worker-session")
	require.True(t, ok)
	userRows := 0
	for _, m := range session.GetMessages() {
		if m.Role == "user" {
			userRows++
		}
	}
	assert.Equal(t, 1, userRows, "healed retry must not duplicate the user message in the session")

	// Pending count 0 also holds for a message stuck in flight or marked
	// failed; only the stored status proves the success path acknowledged it.
	require.Eventually(t, func() bool {
		status, _, err := queue.MessageStatus(context.Background(), msgID)
		return err == nil && status == communication.QueueMessageStatusAcked
	}, 5*time.Second, 10*time.Millisecond, "healed message must be acknowledged")
}

// TestRunWorkflowSubAgent_DropsMessageAfterMaxRetries checks the MaxRetries
// cap: a message whose Chat keeps failing ends up failed, and the drop is
// logged at ERROR with the message ID.
func TestRunWorkflowSubAgent_DropsMessageAfterMaxRetries(t *testing.T) {
	llm := &flakyLLM{} // never healed
	subAgent := agent.NewAgent(&mockBackend{}, llm)

	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	core, logs := observer.New(zap.ErrorLevel)
	srv.logger = zap.New(core)
	queue := srv.messageQueue

	const agentID = "wf:worker"
	ctx, cancel := context.WithCancel(context.Background())
	notifyChan := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runWorkflowSubAgent(ctx, subAgent, agentID, "coord-session:"+agentID, "worker-session", "wf", notifyChan)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	msgID, err := queue.Send(context.Background(), "wf", agentID, "task",
		&loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("always fails")}}, nil)
	require.NoError(t, err)

	// Keep offering the message (the worker backs off 1s, then 2s, between
	// failed attempts) until it is marked failed.
	require.Eventually(t, func() bool {
		select {
		case notifyChan <- struct{}{}:
		default:
		}
		status, _, err := queue.MessageStatus(context.Background(), msgID)
		return err == nil && status == communication.QueueMessageStatusFailed
	}, 20*time.Second, 50*time.Millisecond, "a message that keeps failing must end up failed")

	_, count, err := queue.MessageStatus(context.Background(), msgID)
	require.NoError(t, err)
	assert.Equal(t, int32(communication.DefaultMaxRetries), count)

	dropped := logs.FilterMessage("Message dropped after reaching max retries").All()
	require.Len(t, dropped, 1)
	assert.Equal(t, msgID, dropped[0].ContextMap()["message_id"])
}

// settleFixture enqueues one message for agentID and dequeues it, leaving it
// in flight as runWorkflowSubAgent does before Chat.
func settleFixture(t *testing.T) (*MultiAgentServer, *observer.ObservedLogs, string) {
	t.Helper()
	srv := setupBroadcastTestServer(t, map[string]*agent.Agent{}, nil)
	core, logs := observer.New(zap.InfoLevel)
	srv.logger = zap.New(core)
	ctx := context.Background()
	msgID, err := srv.messageQueue.Send(ctx, "wf", "wf:worker", "task",
		&loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("x")}}, nil)
	require.NoError(t, err)
	msg, err := srv.messageQueue.Dequeue(ctx, "wf:worker")
	require.NoError(t, err)
	require.NotNil(t, msg)
	require.Equal(t, msgID, msg.ID)
	return srv, logs, msgID
}

func assertStatus(t *testing.T, srv *MultiAgentServer, msgID string, want communication.QueueMessageStatus, wantCount int32) {
	t.Helper()
	status, count, err := srv.messageQueue.MessageStatus(context.Background(), msgID)
	require.NoError(t, err)
	assert.Equal(t, want, status)
	assert.Equal(t, wantCount, count, "dequeue count")
}

func TestSettleSubAgentBatch(t *testing.T) {
	t.Run("success acknowledges", func(t *testing.T) {
		srv, _, id := settleFixture(t)
		srv.settleSubAgentBatch(context.Background(), "wf:worker", []string{id}, nil)
		assertStatus(t, srv, id, communication.QueueMessageStatusAcked, 1)
	})

	// The parked turn consumed the batch and finishes through ResumeChat;
	// redelivering would inject it twice (#533 review Q2).
	t.Run("parked turn acknowledges", func(t *testing.T) {
		srv, _, id := settleFixture(t)
		err := fmt.Errorf("chat: %w", &agent.TurnParkedError{RequestID: "hr-1"})
		srv.settleSubAgentBatch(context.Background(), "wf:worker", []string{id}, err)
		assertStatus(t, srv, id, communication.QueueMessageStatusAcked, 1)
	})

	// A parked turn owns the session, so the batch was refused. It must stay
	// deliverable however many times that happens (#533 review Q2).
	t.Run("session parked releases without spending a retry", func(t *testing.T) {
		srv, _, id := settleFixture(t)
		srv.settleSubAgentBatch(context.Background(), "wf:worker", []string{id}, &agent.SessionParkedError{RequestID: "hr-1"})
		assertStatus(t, srv, id, communication.QueueMessageStatusPending, 0)
	})

	// The worker stopped mid-turn (shutdown or reconfiguration); that is not
	// the message's fault (#533 review Q1, Q6).
	t.Run("cancelled worker releases without spending a retry", func(t *testing.T) {
		srv, _, id := settleFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		srv.settleSubAgentBatch(ctx, "wf:worker", []string{id}, context.Canceled)
		assertStatus(t, srv, id, communication.QueueMessageStatusPending, 0)
	})

	// Requeued once, with no false requeue-failure log (#533 review F4).
	t.Run("not delivered requeues once", func(t *testing.T) {
		srv, logs, id := settleFixture(t)
		err := fmt.Errorf("%w: %w", errInjectNotDelivered, agent.ErrSessionBusy)
		srv.settleSubAgentBatch(context.Background(), "wf:worker", []string{id}, err)
		assertStatus(t, srv, id, communication.QueueMessageStatusPending, 1)
		assert.Empty(t, logs.FilterLevelExact(zap.ErrorLevel).All())
		assert.Len(t, logs.FilterMessage("Sub-agent session unavailable; left messages for redelivery").All(), 1)
	})

	t.Run("chat error requeues", func(t *testing.T) {
		srv, logs, id := settleFixture(t)
		srv.settleSubAgentBatch(context.Background(), "wf:worker", []string{id}, errors.New("LLM unavailable"))
		assertStatus(t, srv, id, communication.QueueMessageStatusPending, 1)
		assert.Empty(t, logs.FilterLevelExact(zap.ErrorLevel).All())
	})
}
