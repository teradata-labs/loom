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
// Package transport implements stream resumption for streamable-http transport.
package transport

import (
	"container/ring"
	"sync"
)

// SSEEvent represents a Server-Sent Event with ID for resumption.
type SSEEvent struct {
	ID   string // Event ID for resumption
	Data []byte // JSON-RPC message
}

// StreamResumption manages event buffering for stream resumption.
// Per MCP spec, event IDs are globally unique across all streams in a session,
// and servers MAY replay messages after a given event ID on the same stream.
type StreamResumption struct {
	lastEventID string
	eventBuffer *ring.Ring // Circular buffer of recent events
	bufferSize  int
	mu          sync.RWMutex
}

// NewStreamResumption creates a new stream resumption manager.
func NewStreamResumption(bufferSize int) *StreamResumption {
	if bufferSize <= 0 {
		bufferSize = 100 // Default buffer size
	}
	return &StreamResumption{
		eventBuffer: ring.New(bufferSize),
		bufferSize:  bufferSize,
	}
}

// UpdateLastEventID updates the last received event ID.
func (s *StreamResumption) UpdateLastEventID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastEventID = id
}

// GetLastEventID returns the last received event ID.
func (s *StreamResumption) GetLastEventID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastEventID
}

// AddEvent adds an event to the buffer for potential replay.
func (s *StreamResumption) AddEvent(event SSEEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.eventBuffer.Value = event
	s.eventBuffer = s.eventBuffer.Next()
	s.lastEventID = event.ID
}

// GetEventsAfter retrieves events after a given event ID.
// Returns nil if no events found or if event ID is not in buffer.
func (s *StreamResumption) GetEventsAfter(afterEventID string) []SSEEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if afterEventID == "" {
		return nil
	}

	var events []SSEEvent
	found := false

	// Scan buffer for the afterEventID
	s.eventBuffer.Do(func(v interface{}) {
		if v == nil {
			return
		}
		event, ok := v.(SSEEvent)
		if !ok {
			return
		}

		// Start collecting after we find the target event ID
		if found {
			events = append(events, event)
		} else if event.ID == afterEventID {
			found = true
		}
	})

	if !found {
		return nil // Event ID not found in buffer
	}

	return events
}

// Clear clears the event buffer and last event ID.
func (s *StreamResumption) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastEventID = ""
	s.eventBuffer = ring.New(s.bufferSize)
}
