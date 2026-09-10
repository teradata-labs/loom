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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/agent"
)

// A droppable event must never block the emitting goroutine. This is the
// property that keeps a HITL hold heartbeat from deepening the hang it exists
// to prevent: the hold emits from the turn goroutine while parked in its poll
// loop, so a blocking send on a wedged consumer would stall the poll that reads
// the human's decision — and the hold would then neither resolve nor expire.
func TestProgressSender_DroppableEventDoesNotBlockOnFullChannel(t *testing.T) {
	out := make(chan agent.ProgressEvent, 1)
	never := make(chan struct{}) // consumer alive, but wedged: never cancelled
	send := newProgressSender(out, never)

	send(agent.ProgressEvent{Message: "fills the buffer"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Several beats' worth, all onto a channel that is already full.
		for i := 0; i < 5; i++ {
			send(agent.ProgressEvent{Message: "heartbeat", Droppable: true})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a droppable event blocked on a full channel — a held turn would wedge here")
	}

	require.Len(t, out, 1, "the buffered state-bearing event is still the only one queued")
	require.Equal(t, "fills the buffer", (<-out).Message)
}

// A droppable event is still delivered when there is room — dropping is a
// backpressure release valve, not the normal path.
func TestProgressSender_DroppableEventDeliveredWhenThereIsRoom(t *testing.T) {
	out := make(chan agent.ProgressEvent, 2)
	send := newProgressSender(out, make(chan struct{}))

	send(agent.ProgressEvent{Message: "heartbeat", Droppable: true})

	require.Len(t, out, 1)
	got := <-out
	require.Equal(t, "heartbeat", got.Message)
	require.True(t, got.Droppable)
}

// A state-bearing event must NOT be dropped: a lost token chunk, HITL card, or
// tool lifecycle event is a hole the consumer cannot reconstruct, so the
// emitting turn waits for the sender to take it.
func TestProgressSender_StateBearingEventWaitsForTheSender(t *testing.T) {
	out := make(chan agent.ProgressEvent, 1)
	send := newProgressSender(out, make(chan struct{}))

	send(agent.ProgressEvent{Message: "first"})

	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		send(agent.ProgressEvent{Message: "second"})
	}()

	select {
	case <-blocked:
		t.Fatal("a state-bearing event was dropped instead of waiting")
	case <-time.After(50 * time.Millisecond):
	}

	require.Equal(t, "first", (<-out).Message) // drain, making room

	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("the state-bearing event never landed after room was made")
	}
	require.Equal(t, "second", (<-out).Message)
}

// A state-bearing event gives up when the stream's context is done, so a turn
// outliving its client cannot wedge on a consumer that will never read again.
func TestProgressSender_StateBearingEventGivesUpWhenDone(t *testing.T) {
	out := make(chan agent.ProgressEvent, 1)
	done := make(chan struct{})
	send := newProgressSender(out, done)

	send(agent.ProgressEvent{Message: "fills the buffer"})

	released := make(chan struct{})
	go func() {
		defer close(released)
		send(agent.ProgressEvent{Message: "abandoned"})
	}()

	close(done)

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("a state-bearing event did not give up after the stream was done")
	}
}
