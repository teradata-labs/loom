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
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	"github.com/teradata-labs/loom/pkg/communication"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A turn refused because its session is busy reaches a gRPC client as
// Unavailable — retry shortly — and is never cached as an outcome.
func TestABusySessionIsUnavailableToAClient(t *testing.T) {
	busy := fmt.Errorf("turn: %w", &agent.SessionBusyError{SessionID: "s", Holder: "h"})
	err := wrapAgentError(busy)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("a busy session reached the client as %v, want Unavailable", status.Code(err))
	}
	if !isTransientOutcome(err) {
		t.Fatal("a busy session's refusal must not be cached as an outcome")
	}
}

// A broadcast waits out a session busy with another turn for as long as it
// stays busy, holding no model-call slot while it waits, and delivers once the
// session is free; it stops only when its context ends.
func TestABroadcastWaitsOutABusySession(t *testing.T) {
	was := busyRetryEvery
	t.Cleanup(func() { busyRetryEvery = was })
	s := &MultiAgentServer{llmSemaphore: make(chan struct{}, 1)}

	// Many short waits: delivered once free, however long that takes.
	busyRetryEvery = time.Millisecond
	var tries atomic.Int32
	err := s.injectWhenFree(context.Background(), func() error {
		if tries.Add(1) < 50 {
			return agent.ErrSessionBusy
		}
		return nil
	})
	if err != nil || tries.Load() != 50 {
		t.Fatalf("the broadcast was not delivered once the session freed: err=%v tries=%d", err, tries.Load())
	}

	// While it waits, another turn can take the only slot.
	busyRetryEvery = 200 * time.Millisecond
	waiting := make(chan struct{})
	got := make(chan bool, 1)
	go func() {
		<-waiting
		select {
		case s.llmSemaphore <- struct{}{}:
			<-s.llmSemaphore
			got <- true
		case <-time.After(100 * time.Millisecond):
			got <- false
		}
	}()
	var once atomic.Bool
	err = s.injectWhenFree(context.Background(), func() error {
		if once.CompareAndSwap(false, true) {
			close(waiting)
			return agent.ErrSessionBusy
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !<-got {
		t.Fatal("the broadcast held the model-call slot while it waited for the session")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	busyRetryEvery = time.Millisecond
	err = s.injectWhenFree(ctx, func() error { return agent.ErrSessionBusy })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a broadcast to a session that stays busy stops with its context, got %v", err)
	}
}

// A queued message handed to a busy session is not lost: it goes back to the
// queue and is delivered again. One the session heard is settled.
func TestAQueuedMessageForABusySessionIsDeliveredAgain(t *testing.T) {
	q, err := communication.NewMessageQueue(":memory:", nil, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	ctx := context.Background()
	send := func() {
		if err := q.Enqueue(ctx, &communication.QueueMessage{ToAgent: "coord", FromAgent: "sub", MessageType: "result",
			Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("done")}}, MaxRetries: 3}); err != nil {
			t.Fatal(err)
		}
	}

	send()
	first, err := q.Dequeue(ctx, "coord")
	if err != nil || first == nil {
		t.Fatalf("dequeue: %v %v", first, err)
	}
	settleQueued(ctx, q, zaptest.NewLogger(t), first.ID, agent.ErrSessionBusy)
	again, err := q.Dequeue(ctx, "coord")
	if err != nil || again == nil || again.ID != first.ID {
		t.Fatalf("the message for a busy session was lost: %v %v", again, err)
	}

	settleQueued(ctx, q, zaptest.NewLogger(t), again.ID, nil)
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
		settleQueued(ctx, q, zaptest.NewLogger(t), m.ID, agent.ErrSessionBusy)
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

// A sub-agent's queued messages are acknowledged when its turn heard them,
// deferred when its session was busy (delivered again, retries untouched),
// and left in flight on any other failure, as before.
func TestASubAgentTurnSettlesItsMessages(t *testing.T) {
	ctx := context.Background()
	q, err := communication.NewMessageQueue(":memory:", nil, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()
	send := func() string {
		if err := q.Enqueue(ctx, &communication.QueueMessage{ToAgent: "sub", FromAgent: "coord", MessageType: "task",
			Payload: &loomv1.MessagePayload{Data: &loomv1.MessagePayload_Value{Value: []byte("go")}}, MaxRetries: 3}); err != nil {
			t.Fatal(err)
		}
		m, err := q.Dequeue(ctx, "sub")
		if err != nil || m == nil {
			t.Fatalf("dequeue: %v %v", m, err)
		}
		return m.ID
	}
	log := zaptest.NewLogger(t)

	busy := send()
	for i := 0; i < 5; i++ {
		settleSubAgentTurn(ctx, q, log, "sub", []string{busy}, agent.ErrSessionBusy)
		m, err := q.Dequeue(ctx, "sub")
		if err != nil || m == nil || m.ID != busy {
			t.Fatalf("refusal %d: a message for a busy sub-agent was not delivered again: %v %v", i+1, m, err)
		}
		if m.DequeueCount != 1 {
			t.Fatalf("a busy refusal counted against the message: %d deliveries", m.DequeueCount)
		}
	}
	settleSubAgentTurn(ctx, q, log, "sub", []string{busy}, nil)
	if m, _ := q.Dequeue(ctx, "sub"); m != nil {
		t.Fatalf("a heard message was delivered again: %v", m)
	}

	failed := send()
	settleSubAgentTurn(ctx, q, log, "sub", []string{failed}, errors.New("model down"))
	if m, _ := q.Dequeue(ctx, "sub"); m != nil {
		t.Fatalf("a message of a failed turn changed state: %v", m)
	}
}
