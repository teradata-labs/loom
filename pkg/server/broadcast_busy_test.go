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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
)

// busyBroadcast is a server with a bus and a queue, an agent whose session
// another holder has leased for the whole test, and one broadcast from
// worker-2 on topic "progress" waiting in the agent's subscription.
func busyBroadcast(t *testing.T, agentID, sessionID string) (*MultiAgentServer, *agent.Agent, *injectionLLM, *communication.Subscription) {
	t.Helper()
	stores := newResumeStores(t)
	llm := &injectionLLM{}
	ag := stores.agent(llm, nil)
	srv := stores.server(t, ag)
	srv.injectBusy = injectBusyPolicy{initial: 10 * time.Millisecond, max: 20 * time.Millisecond, total: 150 * time.Millisecond}
	logger := zaptest.NewLogger(t)
	srv.messageBus = communication.NewMessageBus(nil, nil, nil, logger)
	q, err := communication.NewMessageQueue(":memory:", nil, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	srv.messageQueue = q

	release := stores.leaseSession(t, sessionID, "user-turn")
	t.Cleanup(release)

	ctx := context.Background()
	sub, err := srv.messageBus.Subscribe(ctx, agentID, "progress", nil, 4)
	require.NoError(t, err)
	_, _, err = srv.messageBus.Publish(ctx, "progress", &loomv1.BusMessage{
		Id: "m1", Topic: "progress", FromAgent: "worker-2",
		Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("half done")}},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(sub.Channel) == 1 }, 2*time.Second, 5*time.Millisecond,
		"the broadcast did not reach the subscription")
	return srv, ag, llm, sub
}

// queuedFor is the one message waiting in the queue for agentID.
func queuedFor(t *testing.T, srv *MultiAgentServer, agentID string) *communication.QueueMessage {
	t.Helper()
	m, err := srv.messageQueue.Dequeue(context.Background(), agentID)
	require.NoError(t, err)
	require.NotNil(t, m, "the broadcast was dropped, not queued")
	return m
}

// A broadcast for a coordinator whose session stays busy past the wait is put
// on the queue for it, with its sender, topic and words, and no turn is run.
func TestCoordinatorBroadcast_BusySessionQueuesTheMessage(t *testing.T) {
	srv, ag, llm, sub := busyBroadcast(t, "coord", "s-coord")
	srv.workflowSubAgentsMu.Lock()
	srv.workflowSubAgents["s-coord:coord"] = &workflowSubAgentContext{subscriptionIDs: []string{sub.ID}}
	srv.workflowSubAgentsMu.Unlock()

	srv.processCoordinatorBroadcastMessages(context.Background(), "s-coord:coord", ag, "s-coord", "coord")

	m := queuedFor(t, srv, "coord")
	text := string(m.Payload.GetValue())
	require.Equal(t, "worker-2", m.FromAgent)
	require.Equal(t, "progress", m.Metadata["topic"])
	require.True(t, strings.Contains(text, "half done"), "the queued broadcast lost its words: %q", text)
	require.Zero(t, llm.saw("half done"), "a refused broadcast ran a turn")
}

// The same for a sub-agent's broadcast.
func TestSubAgentBroadcast_BusySessionQueuesTheMessage(t *testing.T) {
	srv, ag, llm, sub := busyBroadcast(t, "wf:worker", "s-worker")

	srv.processSubAgentBroadcastMessages(context.Background(), "s-coord:wf:worker", ag, "s-worker", "wf:worker", sub.ID)

	m := queuedFor(t, srv, "wf:worker")
	text := string(m.Payload.GetValue())
	require.Equal(t, "worker-2", m.FromAgent)
	require.Equal(t, "progress", m.Metadata["topic"])
	require.True(t, strings.Contains(text, "half done"), "the queued broadcast lost its words: %q", text)
	require.Zero(t, llm.saw("half done"), "a refused broadcast ran a turn")
}
