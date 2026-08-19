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
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// captureSSEWriter collects events for assertions.
type captureSSEWriter struct {
	mu     sync.Mutex
	events [][]byte
}

func (c *captureSSEWriter) WriteEvent(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, append([]byte(nil), data...))
	return nil
}

func (c *captureSSEWriter) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.events))
	for i, e := range c.events {
		out[i] = string(e)
	}
	return out
}

func listenRequest(id int, filter string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":` + jsonInt(id) + `,"method":"subscriptions/listen","params":{
		"_meta":{"` + protocol.MetaProtocolVersion + `":"` + protocol.Version20260728 + `"},
		"notifications":` + filter + `}}`)
}

func jsonInt(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestSubscriptionsListenStream(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	w := &captureSSEWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		final, err := s.HandleMessageStream(ctx, listenRequest(7, `{"toolsListChanged":true}`), w)
		assert.NoError(t, err)
		assert.Nil(t, final, "client-cancelled stream carries no final response")
	}()

	// Acknowledgment arrives first, tagged with the subscription id.
	require.Eventually(t, func() bool { return len(w.snapshot()) >= 1 }, 2*time.Second, 5*time.Millisecond)
	ack := w.snapshot()[0]
	assert.Contains(t, ack, protocol.NotificationSubscriptionAcknowledged)
	assert.Contains(t, ack, protocol.MetaSubscriptionID)

	// A subscribed change is delivered; an unsubscribed one is not.
	s.NotifyToolsListChanged()
	s.NotifyPromptsListChanged()
	require.Eventually(t, func() bool { return len(w.snapshot()) >= 2 }, 2*time.Second, 5*time.Millisecond)
	events := w.snapshot()
	assert.Contains(t, events[1], protocol.NotificationToolsListChanged)
	for _, e := range events {
		assert.NotContains(t, e, protocol.NotificationPromptsListChanged, "unsubscribed types must not be delivered")
	}

	// Client cancellation unregisters the subscription.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("listen stream did not exit on cancellation")
	}
	s.subsMu.RLock()
	remaining := len(s.subscriptions)
	s.subsMu.RUnlock()
	assert.Zero(t, remaining, "registry must be cleaned up")

	// Publishing after close is a silent no-op (the old notifyCh bug).
	s.NotifyToolsListChanged()
}

func TestResourceUpdatedFilteredByURI(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	w := &captureSSEWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_, _ = s.HandleMessageStream(ctx, listenRequest(8, `{"resourceSubscriptions":["file:///a.json"]}`), w)
	}()
	require.Eventually(t, func() bool { return len(w.snapshot()) >= 1 }, 2*time.Second, 5*time.Millisecond)

	s.NotifyResourceUpdated("file:///other.json")
	s.NotifyResourceUpdated("file:///a.json")

	require.Eventually(t, func() bool { return len(w.snapshot()) >= 2 }, 2*time.Second, 5*time.Millisecond)
	events := w.snapshot()
	assert.Contains(t, events[1], "file:///a.json")
	for _, e := range events {
		assert.False(t, strings.Contains(e, "other.json"), "unsubscribed URI must not be delivered")
	}
}

func TestSubscriptionsListenSynchronousPathAnswersMethodNotFound(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)

	raw, err := s.HandleMessage(context.Background(), listenRequest(9, `{"toolsListChanged":true}`))
	require.NoError(t, err)
	var resp struct {
		Error *protocol.Error `json:"error"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, protocol.MethodNotFound, resp.Error.Code)
}

// TestSubscriptionIDsDoNotCollideAcrossClients (review finding 10, PR #328):
// JSON-RPC ids are client-scoped — two independent clients both opening a
// subscription with id 1 are concurrent subscriptions, not a duplicate.
func TestSubscriptionIDsDoNotCollideAcrossClients(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)

	openOne := func() (*captureSSEWriter, context.CancelFunc, chan struct{}) {
		w := &captureSSEWriter{}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = s.HandleMessageStream(ctx, listenRequest(1, `{"toolsListChanged":true}`), w)
		}()
		return w, cancel, done
	}

	w1, cancel1, done1 := openOne()
	w2, cancel2, done2 := openOne()
	defer func() { cancel1(); cancel2(); <-done1; <-done2 }()

	// Both streams must be acknowledged — the second must NOT be rejected as
	// a duplicate of the first client's id.
	require.Eventually(t, func() bool { return len(w1.snapshot()) >= 1 }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return len(w2.snapshot()) >= 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Contains(t, w1.snapshot()[0], protocol.NotificationSubscriptionAcknowledged)
	assert.Contains(t, w2.snapshot()[0], protocol.NotificationSubscriptionAcknowledged)

	// A published change reaches both independent subscriptions.
	s.NotifyToolsListChanged()
	require.Eventually(t, func() bool { return len(w1.snapshot()) >= 2 }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return len(w2.snapshot()) >= 2 }, 2*time.Second, 5*time.Millisecond)
}
