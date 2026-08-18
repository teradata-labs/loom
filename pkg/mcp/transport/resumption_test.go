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
// Tests for the frozen legacy StreamResumption surface (§9.2): the exported
// API stays source- and behavior-compatible through the deprecation window.
//
//nolint:staticcheck // frozen legacy surface retained through the 2026-07-28 deprecation window
package transport

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamResumption(t *testing.T) {
	t.Run("add and retrieve events", func(t *testing.T) {
		sr := NewStreamResumption(5)

		// Add events
		event1 := SSEEvent{ID: "event1", Data: []byte(`{"id":1}`)}
		event2 := SSEEvent{ID: "event2", Data: []byte(`{"id":2}`)}
		event3 := SSEEvent{ID: "event3", Data: []byte(`{"id":3}`)}

		sr.AddEvent(event1)
		sr.AddEvent(event2)
		sr.AddEvent(event3)

		assert.Equal(t, "event3", sr.GetLastEventID())

		// Get events after event1
		events := sr.GetEventsAfter("event1")
		assert.Len(t, events, 2)
		assert.Equal(t, "event2", events[0].ID)
		assert.Equal(t, "event3", events[1].ID)
	})

	t.Run("event not in buffer", func(t *testing.T) {
		sr := NewStreamResumption(5)
		sr.AddEvent(SSEEvent{ID: "event1", Data: []byte(`{"id":1}`)})

		events := sr.GetEventsAfter("nonexistent")
		assert.Nil(t, events)
	})

	t.Run("circular buffer overflow", func(t *testing.T) {
		sr := NewStreamResumption(3)

		// Add more events than buffer size
		for i := 1; i <= 5; i++ {
			sr.AddEvent(SSEEvent{
				ID:   string(rune('a' + i - 1)),
				Data: []byte(`{}`),
			})
		}

		// Should only have last 3 events
		assert.Equal(t, "e", sr.GetLastEventID())
	})

	t.Run("clear resumption", func(t *testing.T) {
		sr := NewStreamResumption(5)
		sr.AddEvent(SSEEvent{ID: "event1", Data: []byte(`{}`)})
		assert.Equal(t, "event1", sr.GetLastEventID())

		sr.Clear()
		assert.Equal(t, "", sr.GetLastEventID())
	})

	t.Run("update last event id", func(t *testing.T) {
		sr := NewStreamResumption(5)
		sr.UpdateLastEventID("manual")
		assert.Equal(t, "manual", sr.GetLastEventID())
	})
}
