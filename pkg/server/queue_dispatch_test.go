// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
)

func newDispatchTestServer(t *testing.T) (*MultiAgentServer, *communication.MessageQueue, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.WarnLevel)
	q, err := communication.NewMessageQueue(filepath.Join(t.TempDir(), "queue.db"), nil, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	ag := agent.NewAgent(&mockBackend{}, &mockLLMForMultiAgent{})
	srv := NewMultiAgentServer(map[string]*agent.Agent{"default": ag}, nil)
	srv.messageQueue = q
	srv.SetLogger(zap.New(core))
	return srv, q, logs
}

func enqueueFor(t *testing.T, q *communication.MessageQueue, id, to string) {
	t.Helper()
	require.NoError(t, q.Enqueue(context.Background(), &communication.QueueMessage{
		ID: id, ToAgent: to, FromAgent: "coordinator-guid", MessageType: "workflow.start",
		Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("start")}},
	}))
}

func notified(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func registerWorker(srv *MultiAgentServer, key, agentID string) chan struct{} {
	ch := make(chan struct{}, 1)
	srv.workflowSubAgentsMu.Lock()
	srv.workflowSubAgents[key] = &workflowSubAgentContext{agentID: agentID, notifyChan: ch}
	srv.workflowSubAgentsMu.Unlock()
	return ch
}

// TestDispatchPendingMessages_NotifiesExactListenerOnly is the BUG-5 case: a
// message addressed to a short name ("conf-intel-discovery") used to wake the
// worker registered as "<session>:<workflow>:conf-intel-discovery" every second
// by key suffix, although that worker dequeues under its full name and can
// never take it.
func TestDispatchPendingMessages_NotifiesExactListenerOnly(t *testing.T) {
	srv, q, _ := newDispatchTestServer(t)
	ctx := context.Background()
	ch := registerWorker(srv, "sess-1:wf:agent-a", "wf:agent-a")

	enqueueFor(t, q, "short-1", "agent-a")
	srv.dispatchPendingMessages(ctx, time.Now())
	assert.False(t, notified(ch), "a short-name message must not wake the workflow-qualified worker")

	enqueueFor(t, q, "full-1", "wf:agent-a")
	srv.dispatchPendingMessages(ctx, time.Now())
	assert.True(t, notified(ch), "a message addressed to the worker's own ID wakes it")
}

// TestDispatchPendingMessages_FailsUndeliverableAfterGrace keeps a message with
// no listener during the grace period, then marks it failed with a warning that
// names the worker it was probably meant for; deliverable messages are kept.
func TestDispatchPendingMessages_FailsUndeliverableAfterGrace(t *testing.T) {
	srv, q, logs := newDispatchTestServer(t)
	ctx := context.Background()
	registerWorker(srv, "sess-1:wf:agent-a", "wf:agent-a")
	enqueueFor(t, q, "short-1", "agent-a")
	enqueueFor(t, q, "full-1", "wf:agent-a")
	direct := make(chan struct{}, 1)
	q.RegisterNotificationChannel("direct-listener", direct)
	enqueueFor(t, q, "direct-1", "direct-listener")

	srv.dispatchPendingMessages(ctx, time.Now())
	assert.Contains(t, q.GetAgentsWithPendingMessages(ctx), "agent-a", "kept during the grace period")

	srv.dispatchPendingMessages(ctx, time.Now().Add(defaultUndeliverableMessageGrace+time.Second))
	pending := q.GetAgentsWithPendingMessages(ctx)
	assert.NotContains(t, pending, "agent-a", "failed after the grace period")
	assert.Contains(t, pending, "wf:agent-a", "a message with an exact listener is never failed")
	assert.Contains(t, pending, "direct-listener", "a message with a listener on the queue is never failed")

	entries := logs.FilterMessage("Message undeliverable; marked failed").All()
	require.Len(t, entries, 1)
	assert.Equal(t, "short-1", entries[0].ContextMap()["message_id"])
	assert.Contains(t, entries[0].ContextMap()["reason"], "registered as wf:agent-a")
}

// TestDispatchPendingMessages_FailsWhenAutoSpawnFails covers a workflow-shaped
// ID that cannot be spawned (for example "<guid>:agent" from the old routing):
// it is retried during the grace period, then failed instead of retried every
// second for the message's whole TTL.
func TestDispatchPendingMessages_FailsWhenAutoSpawnFails(t *testing.T) {
	srv, q, logs := newDispatchTestServer(t)
	ctx := context.Background()
	enqueueFor(t, q, "ghost-1", "9722a860-guid:ghost-agent")

	srv.dispatchPendingMessages(ctx, time.Now())
	assert.Contains(t, q.GetAgentsWithPendingMessages(ctx), "9722a860-guid:ghost-agent")

	srv.undeliverableMessageGrace = time.Minute
	srv.dispatchPendingMessages(ctx, time.Now().Add(2*time.Minute))
	assert.NotContains(t, q.GetAgentsWithPendingMessages(ctx), "9722a860-guid:ghost-agent")
	entries := logs.FilterMessage("Message undeliverable; marked failed").All()
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].ContextMap()["reason"], "auto-spawn failed")
}

// TestRunWorkflowSubAgent_StoppingWorkerKeepsItsReplacement: a workflow that
// re-registers a sub-agent cancels the old worker before the new one starts;
// the old worker's cleanup runs after the new registration and must not remove
// it (or its queue channel), or exact matching would never wake the new worker.
func TestRunWorkflowSubAgent_StoppingWorkerKeepsItsReplacement(t *testing.T) {
	srv, q, _ := newDispatchTestServer(t)
	ag := agent.NewAgent(&mockBackend{}, &mockLLMForMultiAgent{})
	const key, agentID = "sess-1:wf:agent-a", "wf:agent-a"

	oldCh := registerWorker(srv, key, agentID)
	q.RegisterNotificationChannel(agentID, oldCh)
	oldCtx, cancelOld := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.runWorkflowSubAgent(oldCtx, ag, agentID, key, "sess-old", "wf", oldCh)
		close(done)
	}()

	newCh := registerWorker(srv, key, agentID) // the replacement registers first...
	q.RegisterNotificationChannel(agentID, newCh)
	cancelOld() // ...and the old worker stops afterwards
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("old worker did not stop")
	}

	srv.workflowSubAgentsMu.RLock()
	current, ok := srv.workflowSubAgents[key]
	srv.workflowSubAgentsMu.RUnlock()
	require.True(t, ok, "the replacement's registration must survive the old worker's cleanup")
	assert.Equal(t, newCh, current.notifyChan)
	got, ok := q.GetNotificationChannel(agentID)
	require.True(t, ok, "the replacement's queue channel must survive")
	assert.Equal(t, newCh, got)

	// Without a replacement, a stopping worker still cleans up after itself.
	lastCtx, cancelLast := context.WithCancel(context.Background())
	lastDone := make(chan struct{})
	go func() {
		srv.runWorkflowSubAgent(lastCtx, ag, agentID, key, "sess-new", "wf", newCh)
		close(lastDone)
	}()
	cancelLast()
	<-lastDone
	srv.workflowSubAgentsMu.RLock()
	_, ok = srv.workflowSubAgents[key]
	srv.workflowSubAgentsMu.RUnlock()
	assert.False(t, ok)
	_, ok = q.GetNotificationChannel(agentID)
	assert.False(t, ok)
}
