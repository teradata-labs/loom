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
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
)

// A queued message whose session was busy is offered again, and one the
// session heard is not.
func TestAQueuedMessageForABusySessionIsDeliveredAgain(t *testing.T) {
	q, err := communication.NewMessageQueue(":memory:", nil, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	ctx := context.Background()
	if err := q.Enqueue(ctx, &communication.QueueMessage{ToAgent: "coord", FromAgent: "sub", MessageType: "result",
		Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("done")}}, MaxRetries: 3}); err != nil {
		t.Fatal(err)
	}

	first, err := q.Dequeue(ctx, "coord")
	if err != nil || first == nil {
		t.Fatalf("dequeue: %v %v", first, err)
	}
	returnUndelivered(ctx, q, zaptest.NewLogger(t), first.ID, agent.ErrSessionBusy)
	again, err := q.Dequeue(ctx, "coord")
	if err != nil || again == nil || again.ID != first.ID {
		t.Fatalf("the message for a busy session was lost: %v %v", again, err)
	}
	if err := q.Acknowledge(ctx, again.ID); err != nil {
		t.Fatal(err)
	}
	if next, _ := q.Dequeue(ctx, "coord"); next != nil {
		t.Fatalf("a message the session heard was delivered again: %v", next)
	}
}

// A message refused as busy more times than its retries is still delivered:
// a busy session is not a failed attempt. Its count survives a restart.
func TestABusySessionNeverUsesUpAMessagesRetries(t *testing.T) {
	path := t.TempDir() + "/queue.db"
	q, err := communication.NewMessageQueue(path, nil, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.Enqueue(ctx, &communication.QueueMessage{ToAgent: "coord", FromAgent: "sub", MessageType: "result",
		Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("done")}}, MaxRetries: 3}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		m, err := q.Dequeue(ctx, "coord")
		if err != nil || m == nil {
			t.Fatalf("delivery %d: the message is gone after %d busy refusals (%v)", i+1, i, err)
		}
		returnUndelivered(ctx, q, zaptest.NewLogger(t), m.ID, agent.ErrSessionBusy)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	q, err = communication.NewMessageQueue(path, nil, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	m, err := q.Dequeue(ctx, "coord")
	if err != nil || m == nil {
		t.Fatalf("after a restart the deferred message is gone: %v", err)
	}
	if m.DequeueCount != 1 {
		t.Fatalf("after a restart the message counts %d deliveries, want 1 (busy refusals are not counted)", m.DequeueCount)
	}
}

// A busy refusal reaches the queue the way chatInjected reports it, wrapped
// under "not delivered": it is still deferred, not charged a delivery.
func TestABusyRefusalThroughInjectionIsDeferred(t *testing.T) {
	q, err := communication.NewMessageQueue(":memory:", nil, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	ctx := context.Background()
	if err := q.Enqueue(ctx, &communication.QueueMessage{ToAgent: "coord", FromAgent: "sub", MessageType: "result",
		Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("done")}}, MaxRetries: 3}); err != nil {
		t.Fatal(err)
	}
	first, err := q.Dequeue(ctx, "coord")
	if err != nil || first == nil {
		t.Fatalf("dequeue: %v %v", first, err)
	}
	asInjected := fmt.Errorf("%w: %w", errInjectNotDelivered, fmt.Errorf("session coord: %w", agent.ErrSessionBusy))
	if !injectNotDelivered(asInjected) {
		t.Fatal("the test's error is not what chatInjected reports")
	}
	returnUndelivered(ctx, q, zaptest.NewLogger(t), first.ID, asInjected)
	again, err := q.Dequeue(ctx, "coord")
	if err != nil || again == nil {
		t.Fatalf("the message was lost: %v %v", again, err)
	}
	if again.DequeueCount != 1 {
		t.Fatalf("a busy refusal was charged as a delivery: count %d, want 1", again.DequeueCount)
	}
}

// A broadcast a busy session could not take within the wait is queued for it
// and comes out of the queue for that agent with its topic and its sender,
// instead of being dropped.
func TestABroadcastThatOutlastsTheWaitIsQueued(t *testing.T) {
	q, err := communication.NewMessageQueue(":memory:", nil, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	ctx := context.Background()
	if err := queueBroadcast(ctx, q, "coord", "worker-2", "progress", "half done"); err != nil {
		t.Fatal(err)
	}
	m, err := q.Dequeue(ctx, "coord")
	if err != nil || m == nil {
		t.Fatalf("the queued broadcast did not come out for its agent: %v %v", m, err)
	}
	text := string(m.Payload.GetValue())
	if m.FromAgent != "worker-2" || m.Metadata["topic"] != "progress" || !strings.Contains(text, "progress") || !strings.Contains(text, "half done") {
		t.Fatalf("the broadcast lost its sender, topic or words: from=%q meta=%v text=%q", m.FromAgent, m.Metadata, text)
	}
}
