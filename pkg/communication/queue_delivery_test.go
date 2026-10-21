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
package communication

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
	"github.com/teradata-labs/loom/pkg/observability"
)

func newDeliveryTestQueue(t *testing.T, tracer observability.Tracer) (*MessageQueue, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.WarnLevel)
	q, err := NewMessageQueue(filepath.Join(t.TempDir(), "queue.db"), tracer, zap.New(core))
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close() })
	return q, logs
}

func enqueueTo(t *testing.T, q *MessageQueue, id, to string) {
	t.Helper()
	require.NoError(t, q.Enqueue(context.Background(), &QueueMessage{
		ID: id, ToAgent: to, FromAgent: "sender-guid", MessageType: "workflow.start",
		Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("go")}},
	}))
}

// TestDequeue_TracesOnlyWhenAMessageIsReturned: workers poll Dequeue until the
// queue is empty, so an empty poll must not create a span (BUG-5).
func TestDequeue_TracesOnlyWhenAMessageIsReturned(t *testing.T) {
	tracer := observability.NewMockTracer()
	q, _ := newDeliveryTestQueue(t, tracer)
	ctx := context.Background()

	msg, err := q.Dequeue(ctx, "nobody")
	require.NoError(t, err)
	assert.Nil(t, msg)

	enqueueTo(t, q, "m-1", "wf:agent-a")
	msg, err = q.Dequeue(ctx, "wf:agent-a")
	require.NoError(t, err)
	require.NotNil(t, msg)
	msg, err = q.Dequeue(ctx, "wf:agent-a") // drained: the poll that ends a worker's loop
	require.NoError(t, err)
	assert.Nil(t, msg)

	spans := tracer.GetSpansByName(SpanQueueDequeue)
	require.Len(t, spans, 1, "only the dequeue that returned a message is traced")
	assert.Equal(t, "m-1", spans[0].Attributes["message_id"])
	assert.Equal(t, "wf:agent-a", spans[0].Attributes["agent_id"])
	assert.Equal(t, int32(1), spans[0].Attributes["dequeue_count"])
}

// TestFailUndeliverable marks only messages older than the cutoff, logs one
// warning each, persists the status, and stops reporting them as pending.
func TestFailUndeliverable(t *testing.T) {
	q, logs := newDeliveryTestQueue(t, nil)
	ctx := context.Background()
	enqueueTo(t, q, "short-1", "agent-a")
	enqueueTo(t, q, "other-1", "wf:agent-a")

	assert.Empty(t, q.FailUndeliverable(ctx, "agent-a", time.Now().Add(-time.Hour), "too new"),
		"a message younger than the grace period is kept")
	assert.Contains(t, q.GetAgentsWithPendingMessages(ctx), "agent-a")

	failed := q.FailUndeliverable(ctx, "agent-a", time.Now().Add(time.Second), "no listener for this agent ID")
	assert.Equal(t, []string{"short-1"}, failed)
	assert.NotContains(t, q.GetAgentsWithPendingMessages(ctx), "agent-a")
	assert.Contains(t, q.GetAgentsWithPendingMessages(ctx), "wf:agent-a", "other agents are untouched")
	msg, err := q.Dequeue(ctx, "agent-a")
	require.NoError(t, err)
	assert.Nil(t, msg, "a failed message is never dequeued")

	var status int
	require.NoError(t, q.db.QueryRowContext(ctx, `SELECT status FROM message_queue WHERE id = ?`, "short-1").Scan(&status))
	assert.Equal(t, int(QueueMessageStatusFailed), status, "the status is persisted, so a restart does not reload it")

	entries := logs.FilterMessage("Message undeliverable; marked failed").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	assert.Equal(t, "short-1", fields["message_id"])
	assert.Equal(t, "agent-a", fields["to_agent"])
	assert.Equal(t, "no listener for this agent ID", fields["reason"])

	assert.Empty(t, q.FailUndeliverable(ctx, "agent-a", time.Now().Add(time.Hour), "again"), "failing is done once")
}

// TestUnregisterNotificationChannelIf removes a channel only while it is still
// the registered one.
func TestUnregisterNotificationChannelIf(t *testing.T) {
	q, _ := newDeliveryTestQueue(t, nil)
	old, replacement := make(chan struct{}, 1), make(chan struct{}, 1)
	q.RegisterNotificationChannel("wf:agent-a", old)
	q.RegisterNotificationChannel("wf:agent-a", replacement)

	assert.False(t, q.UnregisterNotificationChannelIf("wf:agent-a", old), "the replaced worker must not remove its replacement")
	got, ok := q.GetNotificationChannel("wf:agent-a")
	require.True(t, ok)
	assert.Equal(t, replacement, got)

	assert.True(t, q.UnregisterNotificationChannelIf("wf:agent-a", replacement))
	_, ok = q.GetNotificationChannel("wf:agent-a")
	assert.False(t, ok)
}
