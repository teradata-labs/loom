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
// stdio subscriptions/listen tests (review finding 11, PR #328): the stdio
// binding requires acknowledged, tagged subscriptions with cancellation via
// notifications/cancelled, and forbids unrequested notification types under
// the modern era.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// stdioHarness runs Serve over in-memory pipes, exactly the wire a stdio
// client sees. A dedicated reader goroutine consumes complete
// newline-terminated lines (the transport writes message and newline
// separately; a value-only decoder would strand the newline write on the
// synchronous pipe).
type stdioHarness struct {
	t      *testing.T
	inW    io.WriteCloser // client → server stdin
	lines  chan []byte    // server stdout, one message per entry
	done   chan error
	cancel context.CancelFunc
}

func newStdioHarness(t *testing.T, s *MCPServer) *stdioHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	tr := transport.NewStdioServerTransport(inR, outW)

	lines := make(chan []byte, 32)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, tr) }()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not stop")
		}
		_ = outR.Close() // unblock any residual writes; ends the reader
	})
	return &stdioHarness{t: t, inW: inW, lines: lines, done: done, cancel: cancel}
}

func (h *stdioHarness) send(line string) {
	h.t.Helper()
	_, err := h.inW.Write([]byte(line + "\n"))
	require.NoError(h.t, err)
}

// next reads one server message with a deadline.
func (h *stdioHarness) next() map[string]json.RawMessage {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		require.True(h.t, ok, "server output closed unexpectedly")
		var m map[string]json.RawMessage
		require.NoError(h.t, json.Unmarshal(line, &m))
		return m
	case <-time.After(2 * time.Second):
		h.t.Fatal("timed out waiting for a server message")
		return nil
	}
}

func stdioListenLine(id int) string {
	return `{"jsonrpc":"2.0","id":` + jsonInt(id) + `,"method":"subscriptions/listen","params":{` +
		`"_meta":{"` + protocol.MetaProtocolVersion + `":"` + protocol.Version20260728 + `"},` +
		`"notifications":{"toolsListChanged":true}}}`
}

func TestStdioSubscriptionLifecycle(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	h := newStdioHarness(t, s)

	// Open a subscription; the acknowledgment arrives first, tagged.
	h.send(stdioListenLine(1))
	ack := h.next()
	assert.Contains(t, string(ack["method"]), protocol.NotificationSubscriptionAcknowledged)
	assert.Contains(t, string(ack["params"]), protocol.MetaSubscriptionID)

	// A published change arrives on the shared channel, tagged with the
	// subscription id.
	s.NotifyToolsListChanged()
	change := h.next()
	assert.Contains(t, string(change["method"]), protocol.NotificationToolsListChanged)
	assert.Contains(t, string(change["params"]), protocol.MetaSubscriptionID)

	// Duplicate listen id on the same connection is rejected.
	h.send(stdioListenLine(1))
	dup := h.next()
	assert.Contains(t, string(dup["error"]), "duplicate")

	// notifications/cancelled referencing the listen id ends the
	// subscription (no response); later changes are not delivered.
	h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	require.Eventually(t, func() bool {
		s.subsMu.RLock()
		defer s.subsMu.RUnlock()
		return len(s.subscriptions) == 0
	}, 2*time.Second, 5*time.Millisecond, "cancellation must unregister the subscription")

	// The id is reusable after cancellation, proving connection-scoped
	// bookkeeping was cleaned up.
	h.send(stdioListenLine(1))
	reack := h.next()
	assert.Contains(t, string(reack["method"]), protocol.NotificationSubscriptionAcknowledged)
}

// TestStdioModernEraSuppressesUntaggedNotifications: without a legacy
// initialize handshake, untagged legacy notifications must not reach the
// wire — the modern era only delivers what a subscription requested.
func TestStdioModernEraSuppressesUntaggedNotifications(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	h := newStdioHarness(t, s)

	// No subscription, no handshake: a published change must produce nothing.
	s.NotifyToolsListChanged()

	// Follow with a request; the FIRST server message must be its response,
	// not a stray untagged notification.
	h.send(`{"jsonrpc":"2.0","id":5,"method":"server/discover","params":{"_meta":{"` +
		protocol.MetaProtocolVersion + `":"` + protocol.Version20260728 + `"}}}`)
	first := h.next()
	assert.Contains(t, string(first["id"]), "5", "response expected, got: %v", first)
	assert.NotContains(t, string(first["method"]), "list_changed")
}

// TestStdioLegacyEraStillDeliversUntagged: a connection that performed the
// initialize handshake keeps the legacy notification behavior it expects.
func TestStdioLegacyEraStillDeliversUntagged(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	h := newStdioHarness(t, s)

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"legacy","version":"1"}}}`)
	initResp := h.next()
	assert.Contains(t, string(initResp["id"]), "1")

	s.NotifyToolsListChanged()
	notif := h.next()
	assert.Contains(t, string(notif["method"]), protocol.NotificationToolsListChanged)
	assert.NotContains(t, string(notif["params"]), protocol.MetaSubscriptionID,
		"legacy delivery is untagged")
}

// Round-2 finding 5, stdio binding: the connection outlives the subscription,
// so the gap signal is a server-initiated notifications/cancelled for the
// listen id — after which the id is reusable for the refetch re-subscribe.
func TestStdioSubscriptionOverflowSendsCancelled(t *testing.T) {
	s := NewMCPServer("loom-mcp", "1.4.0", nil)
	h := newStdioHarness(t, s)

	h.send(stdioListenLine(5))
	ack := h.next()
	require.Contains(t, string(ack["method"]), protocol.NotificationSubscriptionAcknowledged)

	// Flood without draining: the pipe (unbuffered) and the harness line
	// buffer park the pump mid-send, the subscription buffer fills, and the
	// next publish overflows.
	for i := 0; i < subscriptionBuffer+50; i++ {
		s.NotifyToolsListChanged()
	}

	// Drain until the cancellation surfaces.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-h.lines:
			require.True(t, ok, "server output closed before cancellation")
			if strings.Contains(string(line), protocol.NotificationCancelled) {
				assert.Contains(t, string(line), "overflowed")
				goto resubscribe
			}
		case <-deadline:
			t.Fatal("no notifications/cancelled after overflow")
		}
	}

resubscribe:
	// notifications/cancelled is the client's cue to re-subscribe, so the
	// listen id must be reusable the moment the cue arrives: re-subscribe
	// immediately, with no wait on server internals in between.
	h.send(stdioListenLine(5))
	ack2 := h.next()
	assert.Contains(t, string(ack2["method"]), protocol.NotificationSubscriptionAcknowledged)

	// The overflowed subscription was unregistered before the cue was sent,
	// so exactly the replacement subscription remains.
	s.subsMu.RLock()
	remaining := len(s.subscriptions)
	s.subsMu.RUnlock()
	assert.Equal(t, 1, remaining, "only the replacement subscription should be registered")
}
