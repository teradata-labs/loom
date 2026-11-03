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

// A broadcast waits out a session busy with another turn, and delivers once
// it is free; it gives up only after its wait.
func TestABroadcastWaitsOutABusySession(t *testing.T) {
	was := busyWait
	busyWait = 5 * time.Second
	t.Cleanup(func() { busyWait = was })

	var tries atomic.Int32
	ran, err := chatWhenFree(context.Background(), func() error {
		if tries.Add(1) < 3 {
			return agent.ErrSessionBusy
		}
		return nil
	})
	if !ran || err != nil || tries.Load() != 3 {
		t.Fatalf("the broadcast was not delivered once the session freed: ran=%v err=%v tries=%d", ran, err, tries.Load())
	}

	busyWait = 0
	ran, err = chatWhenFree(context.Background(), func() error { return agent.ErrSessionBusy })
	if ran || !errors.Is(err, agent.ErrSessionBusy) {
		t.Fatalf("a session busy past the wait reports busy, got ran=%v err=%v", ran, err)
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
