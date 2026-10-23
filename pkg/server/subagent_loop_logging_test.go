// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
)

// TestRunWorkflowSubAgent_EmptyWakeUpLogsNoChat is the BUG-13 logging case: a
// wake-up that finds no message used to log "Sub-agent Chat() completed" and
// the semaphore lines at info, which made an idle loop look like LLM traffic.
func TestRunWorkflowSubAgent_EmptyWakeUpLogsNoChat(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	q, err := communication.NewMessageQueue(filepath.Join(t.TempDir(), "queue.db"), nil, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	ag := agent.NewAgent(&mockBackend{}, &mockLLMForMultiAgent{})
	srv := NewMultiAgentServer(map[string]*agent.Agent{"default": ag}, nil)
	srv.messageQueue = q
	srv.SetLogger(zap.New(core))

	const key, agentID = "sess-1:wf:agent-a", "wf:agent-a"
	notify := make(chan struct{}, 1)
	srv.workflowSubAgentsMu.Lock()
	srv.workflowSubAgents[key] = &workflowSubAgentContext{agent: ag, sessionID: "sess-1", notifyChan: notify}
	srv.workflowSubAgentsMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.runWorkflowSubAgent(ctx, ag, agentID, key, "sess-1", "wf", notify)
		close(done)
	}()
	notify <- struct{}{}

	require.Eventually(t, func() bool {
		return logs.FilterMessage("Sub-agent released LLM semaphore").Len() > 0
	}, 5*time.Second, 10*time.Millisecond, "the wake-up must finish")
	cancel()
	<-done

	assert.Zero(t, logs.FilterMessage("Sub-agent Chat() completed").Len(), "no Chat() ran, so none may be logged")
	for _, msg := range []string{"Sub-agent acquiring LLM semaphore", "Sub-agent acquired LLM semaphore", "Sub-agent released LLM semaphore"} {
		for _, e := range logs.FilterMessage(msg).All() {
			assert.Equal(t, zapcore.DebugLevel, e.Level, "%q is not LLM traffic and logs at debug", msg)
		}
	}
}
