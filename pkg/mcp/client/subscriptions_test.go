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
// Tests for the subscriptions/listen client (2026-07-28).
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

func subscribedClient(t *testing.T, ft *scriptedTransport) (*Client, *Subscription) {
	t.Helper()
	ft.discoverResult = statelessDiscoverResult()
	ft.listenSupported = true
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	sub, err := c.Subscribe(context.Background(), protocol.NotificationFilter{ToolsListChanged: true})
	require.NoError(t, err)
	return c, sub
}

func notifJSON(method, subID string, extra string) []byte {
	params := fmt.Sprintf(`{"_meta":{"%s":%s}%s}`, protocol.MetaSubscriptionID, subID, extra)
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":%s}`, method, params))
}

func receiveNotif(t *testing.T, sub *Subscription) Notification {
	t.Helper()
	select {
	case n, ok := <-sub.Notifications:
		require.True(t, ok, "notifications channel closed unexpectedly")
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notification")
		return Notification{}
	}
}

func TestSubscribeRequiresStateless(t *testing.T) {
	ft := newScriptedTransport()
	c := connectClient(t, ft, Config{ProtocolVersion: "legacy"})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	_, err := c.Subscribe(context.Background(), protocol.NotificationFilter{ToolsListChanged: true})
	require.Error(t, err)
}

func TestSubscribeDeliversAckAndFilteredNotifications(t *testing.T) {
	ft := newScriptedTransport()
	_, sub := subscribedClient(t, ft)

	ft.mu.Lock()
	require.Len(t, ft.listenParams, 1)
	var params struct {
		Notifications protocol.NotificationFilter `json:"notifications"`
	}
	require.NoError(t, json.Unmarshal(ft.listenParams[0], &params))
	subID := string(ft.listenIDs[0])
	ft.mu.Unlock()
	assert.True(t, params.Notifications.ToolsListChanged, "filter must reach the wire")

	// Acknowledgment arrives first, tagged with the subscription ID.
	ft.inject(notifJSON(protocol.NotificationSubscriptionAcknowledged, subID, `,"notifications":{"toolsListChanged":true}`))
	ack := receiveNotif(t, sub)
	assert.Equal(t, protocol.NotificationSubscriptionAcknowledged, ack.Method)

	// A change notification for this subscription is delivered.
	ft.inject(notifJSON(protocol.NotificationToolsListChanged, subID, ""))
	change := receiveNotif(t, sub)
	assert.Equal(t, protocol.NotificationToolsListChanged, change.Method)

	// A notification for a different subscription must NOT be delivered here.
	ft.inject(notifJSON(protocol.NotificationToolsListChanged, "9999", ""))
	select {
	case n := <-sub.Notifications:
		t.Fatalf("received notification for foreign subscription: %+v", n)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestSubscriptionGracefulClosure(t *testing.T) {
	ft := newScriptedTransport()
	_, sub := subscribedClient(t, ft)

	ft.mu.Lock()
	subID := string(ft.listenIDs[0])
	ft.mu.Unlock()

	// The server closes gracefully by answering the listen request.
	closure := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","_meta":{"%s":%s}}}`,
		subID, protocol.MetaSubscriptionID, subID)
	ft.inject([]byte(closure))

	select {
	case <-sub.Done():
		assert.NoError(t, sub.Err(), "graceful closure carries no error")
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not close")
	}
	_, open := <-sub.Notifications
	assert.False(t, open, "notifications channel must be closed after closure")
}

func TestSubscriptionStreamLossSurfacesError(t *testing.T) {
	ft := newScriptedTransport()
	_, sub := subscribedClient(t, ft)

	ft.mu.Lock()
	subID := string(ft.listenIDs[0])
	ft.mu.Unlock()

	// Simulate what the transport synthesizes when the listen stream drops.
	lost := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":"response stream lost before completion"}}`,
		subID, transport.CodeStreamLost)
	ft.inject([]byte(lost))

	select {
	case <-sub.Done():
		require.Error(t, sub.Err())
		var rpcErr *protocol.Error
		require.ErrorAs(t, sub.Err(), &rpcErr)
		assert.Equal(t, transport.CodeStreamLost, rpcErr.Code)
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not end on stream loss")
	}
}

func TestSubscribeCancelledByCaller(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = statelessDiscoverResult()
	ft.listenSupported = true
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	ctx, cancel := context.WithCancel(context.Background())
	sub, err := c.Subscribe(ctx, protocol.NotificationFilter{ToolsListChanged: true})
	require.NoError(t, err)

	cancel()
	select {
	case <-sub.Done():
		assert.ErrorIs(t, sub.Err(), context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not end on caller cancellation")
	}
}

// TestSubscriptionErrorPublishedBeforeChannelClose (review finding 7,
// PR #327): a consumer that observes the closed Notifications channel must
// never read Err() == nil for an abnormal end — the terminal error is set
// before anything externally visible closes.
func TestSubscriptionErrorPublishedBeforeChannelClose(t *testing.T) {
	ft := newScriptedTransport()
	_, sub := subscribedClient(t, ft)

	ft.mu.Lock()
	subID := string(ft.listenIDs[0])
	ft.mu.Unlock()

	// Abnormal end: MethodNotFound answering the listen request (a server
	// without subscriptions support).
	ft.inject([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":"method not found"}}`,
		subID, protocol.MethodNotFound)))

	// Drain until closure, then read the error immediately: this is the
	// consumer-side ordering the fix guarantees.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-sub.Notifications:
			if !ok {
				require.Error(t, sub.Err(),
					"Err() must be non-nil the moment the Notifications channel closes abnormally")
				var rpcErr *protocol.Error
				require.ErrorAs(t, sub.Err(), &rpcErr)
				assert.Equal(t, protocol.MethodNotFound, rpcErr.Code)
				return
			}
		case <-deadline:
			t.Fatal("notifications channel never closed")
		}
	}
}

// TestSubscribeCancelSendsCancelledOnStdio (review finding 8, PR #327): on
// transports where closing the response stream is not the cancellation
// signal (stdio), cancelling a subscription must send notifications/cancelled
// referencing the listen request's ID — otherwise the server keeps the
// subscription alive.
func TestSubscribeCancelSendsCancelledOnStdio(t *testing.T) {
	ft := newScriptedTransport() // carriesHeaders unset → stdio-like transport
	ft.discoverResult = statelessDiscoverResult()
	ft.listenSupported = true
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	ctx, cancel := context.WithCancel(context.Background())
	sub, err := c.Subscribe(ctx, protocol.NotificationFilter{ToolsListChanged: true})
	require.NoError(t, err)

	ft.mu.Lock()
	listenID := string(ft.listenIDs[0])
	ft.mu.Unlock()

	cancel()
	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not end on caller cancellation")
	}

	var cancelled *protocol.Request
	for _, n := range ft.notificationsSent() {
		if n.Method == protocol.NotificationCancelled {
			nCopy := n
			cancelled = &nCopy
		}
	}
	require.NotNil(t, cancelled, "cancellation must be propagated to the server on stdio")
	var params protocol.CancelledParams
	require.NoError(t, json.Unmarshal(cancelled.Params, &params))
	require.NotNil(t, params.RequestID)
	assert.Equal(t, listenID, params.RequestID.String(),
		"notifications/cancelled must reference the listen request's ID")
}

// TestSubscribeCancelSendsNothingOnStreamableHTTP: on Streamable HTTP the
// cancellation signal is closing the SSE stream (driven by the request
// context); no notifications/cancelled message is defined for that
// transport and none may be sent.
func TestSubscribeCancelSendsNothingOnStreamableHTTP(t *testing.T) {
	ft := newScriptedTransport()
	ft.carriesHeaders = true // pose as Streamable HTTP
	ft.discoverResult = statelessDiscoverResult()
	ft.listenSupported = true
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	ctx, cancel := context.WithCancel(context.Background())
	sub, err := c.Subscribe(ctx, protocol.NotificationFilter{ToolsListChanged: true})
	require.NoError(t, err)

	cancel()
	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not end on caller cancellation")
	}

	for _, n := range ft.notificationsSent() {
		assert.NotEqual(t, protocol.NotificationCancelled, n.Method,
			"no notifications/cancelled on Streamable HTTP: closing the stream is the signal")
	}
}
