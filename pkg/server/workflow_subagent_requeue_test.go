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

	_, err := queue.Send(context.Background(), "wf", agentID, "task",
		&loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("do the thing")}}, nil)
	require.NoError(t, err)

	// First delivery: Chat fails, the message must go back to pending.
	notifyChan <- struct{}{}
	require.Eventually(t, func() bool {
		calls, _ := llm.snapshot()
		return calls >= 1 && queue.GetQueueDepth(agentID) == 1
	}, 5*time.Second, 10*time.Millisecond, "message should be requeued after a failed Chat")

	failedCalls, _ := llm.snapshot()

	// Second delivery once the LLM recovers: the same message is redelivered
	// and acknowledged.
	llm.heal()
	notifyChan <- struct{}{}
	require.Eventually(t, func() bool {
		calls, _ := llm.snapshot()
		return calls > failedCalls && queue.GetQueueDepth(agentID) == 0
	}, 5*time.Second, 10*time.Millisecond, "requeued message should be redelivered")

	_, seen := llm.snapshot()
	assert.Contains(t, seen[len(seen)-1], "do the thing", "the retry should carry the original message")
}
