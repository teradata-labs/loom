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
	"github.com/teradata-labs/loom/pkg/agent"
)

// newProgressSender returns the ProgressCallback a streaming RPC installs on a
// turn: it hands each event to the sending goroutine over out, and gives up
// when done closes. Shared by every streaming entry point so the two cannot
// drift on backpressure semantics.
//
// A state-bearing event blocks until the sender takes it — a dropped token
// chunk, HITL card, or tool lifecycle event is a hole the consumer cannot
// reconstruct, so the turn waits.
//
// A DROPPABLE event is discarded instead when out is full. This is what keeps a
// HITL hold heartbeat from deepening the hang it exists to prevent. A hold
// emits from the turn goroutine while blocked in its poll loop, and the sending
// goroutine can be wedged indefinitely in stream.Send against a client that is
// gone but whose context has not been canceled — an intermediary dropping the
// connection half-open, which no gRPC keepalive is configured to detect. Once
// out fills, a blocking send would stall the poll loop that reads the human's
// decision, and the hold would never resolve OR expire. Losing a heartbeat
// costs nothing: the next one is 30s away, and the only job of any of them is
// to produce bytes.
func newProgressSender(out chan<- agent.ProgressEvent, done <-chan struct{}) agent.ProgressCallback {
	return func(event agent.ProgressEvent) {
		if event.Droppable {
			select {
			case out <- event:
			default:
				// Consumer is behind; this event carries no state to lose.
			}
			return
		}
		select {
		case out <- event:
		case <-done:
			// Context cancelled, stop sending
		}
	}
}
