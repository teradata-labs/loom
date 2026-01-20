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
package transport

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStreamableHTTPTransport(t *testing.T) {
	tests := []struct {
		name      string
		config    StreamableHTTPConfig
		expectErr bool
	}{
		{
			name: "valid config",
			config: StreamableHTTPConfig{
				Endpoint:         "http://localhost:8080/mcp",
				EnableSessions:   true,
				EnableResumption: true,
			},
			expectErr: false,
		},
		{
			name: "missing endpoint",
			config: StreamableHTTPConfig{
				EnableSessions: true,
			},
			expectErr: true,
		},
		{
			name: "sessions disabled",
			config: StreamableHTTPConfig{
				Endpoint:       "http://localhost:8080/mcp",
				EnableSessions: false,
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, err := NewStreamableHTTPTransport(tt.config)
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, transport)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, transport)
				if transport != nil {
					assert.NoError(t, transport.Close())
				}
			}
		})
	}
}

func TestSessionManager(t *testing.T) {
	t.Run("set and get session ID", func(t *testing.T) {
		sm := NewSessionManager()
		assert.False(t, sm.HasSession())

		err := sm.SetSessionID("test-session-123")
		assert.NoError(t, err)
		assert.True(t, sm.HasSession())
		assert.Equal(t, "test-session-123", sm.GetSessionID())
	})

	t.Run("clear session", func(t *testing.T) {
		sm := NewSessionManager()
		require.NoError(t, sm.SetSessionID("test-session"))
		assert.True(t, sm.HasSession())

		sm.ClearSession()
		assert.False(t, sm.HasSession())
		assert.Equal(t, "", sm.GetSessionID())
	})

	t.Run("invalid session ID with non-ASCII", func(t *testing.T) {
		sm := NewSessionManager()
		err := sm.SetSessionID("test\x00session")
		assert.Error(t, err)
	})

	t.Run("valid ASCII characters", func(t *testing.T) {
		sm := NewSessionManager()
		// Test visible ASCII range (0x21 to 0x7E)
		validIDs := []string{
			"abc123",
			"ABC-123_xyz",
			"session!@#$%",
		}
		for _, id := range validIDs {
			err := sm.SetSessionID(id)
			assert.NoError(t, err, "should accept valid ID: %s", id)
		}
	})
}

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
}

func TestSSEParser(t *testing.T) {
	t.Run("parse single event", func(t *testing.T) {
		data := "id: event1\nevent: message\ndata: {\"jsonrpc\":\"2.0\"}\n\n"
		parser := NewSSEParser(bytes.NewReader([]byte(data)))

		event, err := parser.ParseEvent()
		assert.NoError(t, err)
		assert.NotNil(t, event)
		assert.Equal(t, "event1", event.ID)
		assert.Equal(t, `{"jsonrpc":"2.0"}`, string(event.Data))
	})

	t.Run("parse multi-line data", func(t *testing.T) {
		data := "id: event2\ndata: line1\ndata: line2\ndata: line3\n\n"
		parser := NewSSEParser(bytes.NewReader([]byte(data)))

		event, err := parser.ParseEvent()
		assert.NoError(t, err)
		assert.Equal(t, "event2", event.ID)
		assert.Equal(t, "line1\nline2\nline3", string(event.Data))
	})

	t.Run("skip comments", func(t *testing.T) {
		data := ": this is a comment\nid: event3\ndata: test\n\n"
		parser := NewSSEParser(bytes.NewReader([]byte(data)))

		event, err := parser.ParseEvent()
		assert.NoError(t, err)
		assert.Equal(t, "event3", event.ID)
	})

	t.Run("parse all events", func(t *testing.T) {
		data := "id: e1\ndata: data1\n\nid: e2\ndata: data2\n\n"
		parser := NewSSEParser(bytes.NewReader([]byte(data)))

		events, err := parser.ParseAll()
		assert.NoError(t, err)
		assert.Len(t, events, 2)
		assert.Equal(t, "e1", events[0].ID)
		assert.Equal(t, "e2", events[1].ID)
	})
}

func TestStreamableHTTPConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		config  StreamableHTTPConfig
		wantErr bool
	}{
		{
			name: "sessions and resumption enabled",
			config: StreamableHTTPConfig{
				Endpoint:         "http://example.com/mcp",
				EnableSessions:   true,
				EnableResumption: true,
			},
			wantErr: false,
		},
		{
			name: "only sessions enabled",
			config: StreamableHTTPConfig{
				Endpoint:       "http://example.com/mcp",
				EnableSessions: true,
			},
			wantErr: false,
		},
		{
			name: "no features enabled",
			config: StreamableHTTPConfig{
				Endpoint: "http://example.com/mcp",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trans, err := NewStreamableHTTPTransport(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, trans)
				assert.NoError(t, trans.Close())
			}
		})
	}
}
