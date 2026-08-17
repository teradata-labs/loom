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
// End-to-end test of the 2026-07-28 stateless revision: Loom's own client
// against Loom's own server over Streamable HTTP — discover negotiation,
// _meta stamping, dual-mode admission, and subscriptions/listen, with no
// fakes on either side.
package mcp_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

func TestStatelessEndToEnd(t *testing.T) {
	mcpServer := server.NewMCPServer("loom-mcp-e2e", "1.4.0", nil)

	httpServer, err := transport.NewStreamableHTTPServer(transport.StreamableHTTPServerConfig{
		Handler:       mcpServer.HandleMessage,
		StreamHandler: mcpServer,
	})
	require.NoError(t, err)
	ts := httptest.NewServer(httpServer)
	defer ts.Close()

	trans, err := transport.NewStreamableHTTPTransport(transport.StreamableHTTPConfig{Endpoint: ts.URL})
	require.NoError(t, err)
	c := client.NewClient(client.Config{Transport: trans})
	defer func() { _ = c.Close() }()

	// Negotiation lands on the stateless revision via a real server/discover.
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom", Version: "1.4.0"}))
	require.True(t, c.IsStateless(), "own client and server must negotiate 2026-07-28")
	assert.Equal(t, protocol.Version20260728, c.NegotiatedVersion())
	assert.Equal(t, "loom-mcp-e2e", c.ServerInfo().Name)

	// Stateless ping must answer MethodNotFound end to end.
	//nolint:staticcheck // frozen legacy surface exercised deliberately
	err = c.Ping(context.Background())
	require.Error(t, err)

	// subscriptions/listen: subscribe, receive the ack, then a live
	// tools-list change published by the server.
	sub, err := c.Subscribe(context.Background(), protocol.NotificationFilter{ToolsListChanged: true})
	require.NoError(t, err)

	waitNotif := func() client.Notification {
		select {
		case n, ok := <-sub.Notifications:
			require.True(t, ok, "subscription ended unexpectedly: %v", sub.Err())
			return n
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for notification")
			return client.Notification{}
		}
	}

	ack := waitNotif()
	assert.Equal(t, protocol.NotificationSubscriptionAcknowledged, ack.Method)

	mcpServer.NotifyToolsListChanged()
	change := waitNotif()
	assert.Equal(t, protocol.NotificationToolsListChanged, change.Method)
}
