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
// Tests for the manager's tool-list watch loop (subscriptions/listen).
package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/client"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

// watchFakeTransport simulates a stateless 2026-07-28 server with optional
// subscriptions/listen support.
type watchFakeTransport struct {
	mu             sync.Mutex
	listenOK       bool
	toolsListCalls int
	listenIDs      []string
	events         []string // request-order trace: "tools/list", "listen"
	responses      chan []byte
}

func newWatchFakeTransport(listenOK bool) *watchFakeTransport {
	return &watchFakeTransport{listenOK: listenOK, responses: make(chan []byte, 20)}
}

func (f *watchFakeTransport) Send(_ context.Context, message []byte) error {
	var req protocol.Request
	if err := json.Unmarshal(message, &req); err != nil {
		return err
	}
	if req.ID == nil {
		return nil
	}
	var resp protocol.Response
	resp.JSONRPC = protocol.JSONRPCVersion
	resp.ID = req.ID

	switch req.Method {
	case "server/discover":
		result := protocol.DiscoverResult{
			ResultType:        protocol.ResultTypeComplete,
			SupportedVersions: []string{protocol.Version20260728},
			CacheScope:        "public",
		}
		if err := result.SetServerInfo(protocol.Implementation{Name: "watch-fake", Version: "1.0"}); err != nil {
			return err
		}
		resp.Result, _ = json.Marshal(result)
	case "tools/list":
		f.mu.Lock()
		f.toolsListCalls++
		f.events = append(f.events, "tools/list")
		f.mu.Unlock()
		resp.Result, _ = json.Marshal(protocol.ToolListResult{})
	case protocol.MethodSubscriptionsListen:
		f.mu.Lock()
		ok := f.listenOK
		f.events = append(f.events, "listen")
		if ok {
			idJSON, _ := json.Marshal(req.ID)
			f.listenIDs = append(f.listenIDs, string(idJSON))
		}
		f.mu.Unlock()
		if ok {
			return nil // stream stays open
		}
		resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
	default:
		resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
	}

	data, _ := json.Marshal(resp)
	f.responses <- data
	return nil
}

func (f *watchFakeTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data := <-f.responses:
		return data, nil
	}
}

func (f *watchFakeTransport) Close() error { return nil }

func (f *watchFakeTransport) listCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.toolsListCalls
}

func startWatcher(t *testing.T, ft *watchFakeTransport) (*Manager, *client.Client, *watcherHandle) {
	t.Helper()
	cl := client.NewClient(client.Config{Transport: ft})
	t.Cleanup(func() { _ = cl.Close() })
	require.NoError(t, cl.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	require.True(t, cl.IsStateless())

	m, err := NewManager(Config{}, nil)
	require.NoError(t, err)

	h := &watcherHandle{stop: make(chan struct{}), done: make(chan struct{})}
	go m.watchToolLists("fake", cl, h.stop, h.done)
	return m, cl, h
}

func TestWatchSubscribesThenReconcilesOnAck(t *testing.T) {
	ft := newWatchFakeTransport(true)
	_, _, h := startWatcher(t, ft)

	// The watcher subscribes first — no fetch may precede the subscription,
	// or a change landing between fetch and subscription establishment would
	// be lost (review finding 6, PR #327).
	require.Eventually(t, func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		return len(ft.listenIDs) == 1
	}, 3*time.Second, 10*time.Millisecond, "watcher must subscribe")
	assert.Zero(t, ft.listCalls(), "no fetch before the subscription is acknowledged")

	ft.mu.Lock()
	subID := ft.listenIDs[0]
	ft.mu.Unlock()

	// The acknowledgment is the race-free reconciliation point: the server
	// sends nothing before it and notifies every change after it.
	ft.responses <- []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":{"_meta":{"%s":%s}}}`,
		protocol.NotificationSubscriptionAcknowledged, protocol.MetaSubscriptionID, subID))
	require.Eventually(t, func() bool {
		return ft.listCalls() == 1
	}, 3*time.Second, 10*time.Millisecond, "acknowledgment must trigger the reconciliation fetch")

	// A change notification → one more refetch.
	ft.responses <- []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":{"_meta":{"%s":%s}}}`,
		protocol.NotificationToolsListChanged, protocol.MetaSubscriptionID, subID))
	require.Eventually(t, func() bool {
		return ft.listCalls() == 2
	}, 3*time.Second, 10*time.Millisecond, "change notification must trigger a refetch")

	ft.mu.Lock()
	events := append([]string(nil), ft.events...)
	ft.mu.Unlock()
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "listen", events[0], "subscription must be opened before any fetch")

	close(h.stop)
	waitWatcher(t, h)
}

func TestWatchExitsWhenListenUnsupported(t *testing.T) {
	ft := newWatchFakeTransport(false)
	_, _, h := startWatcher(t, ft)

	// The watcher must exit on its own (MethodNotFound), without stop.
	waitWatcher(t, h)
	close(h.stop)
	assert.Zero(t, ft.listCalls(), "no fetches against a server without subscriptions support")
}

func waitWatcher(t *testing.T, h *watcherHandle) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

// registerFakeServer wires a connected fake-backed client and its watcher
// into the manager the way startServer does, so per-server lifecycle can be
// exercised without a real transport config.
func registerFakeServer(t *testing.T, m *Manager, name string, ft *watchFakeTransport) *watcherHandle {
	t.Helper()
	cl := client.NewClient(client.Config{Transport: ft})
	require.NoError(t, cl.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	h := &watcherHandle{stop: make(chan struct{}), done: make(chan struct{})}
	m.mu.Lock()
	m.clients[name] = cl
	m.watchers[name] = h
	m.mu.Unlock()
	go m.watchToolLists(name, cl, h.stop, h.done)
	return h
}

// TestStopServerStopsWatcher (review finding 9, PR #327): stopping one
// server must stop that server's watcher — previously it kept retrying
// subscriptions against the closed client until the whole manager stopped.
func TestStopServerStopsWatcher(t *testing.T) {
	ft := newWatchFakeTransport(true)
	m, err := NewManager(Config{}, nil)
	require.NoError(t, err)
	h := registerFakeServer(t, m, "fake", ft)

	require.Eventually(t, func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		return len(ft.listenIDs) == 1
	}, 3*time.Second, 10*time.Millisecond, "watcher must subscribe before the stop")

	require.NoError(t, m.StopServer("fake"))
	waitWatcher(t, h)

	m.mu.RLock()
	_, watcherLeft := m.watchers["fake"]
	_, clientLeft := m.clients["fake"]
	m.mu.RUnlock()
	assert.False(t, watcherLeft, "watcher registration must be removed")
	assert.False(t, clientLeft, "client must be removed")
}

// TestRemoveServerStopsWatcher mirrors TestStopServerStopsWatcher for
// RemoveServer, which also drops the config entry.
func TestRemoveServerStopsWatcher(t *testing.T) {
	ft := newWatchFakeTransport(true)
	m, err := NewManager(Config{Servers: map[string]ServerConfig{"fake": {}}}, nil)
	require.NoError(t, err)
	h := registerFakeServer(t, m, "fake", ft)

	require.Eventually(t, func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		return len(ft.listenIDs) == 1
	}, 3*time.Second, 10*time.Millisecond, "watcher must subscribe before the removal")

	require.NoError(t, m.RemoveServer("fake"))
	waitWatcher(t, h)

	m.mu.RLock()
	_, watcherLeft := m.watchers["fake"]
	m.mu.RUnlock()
	assert.False(t, watcherLeft, "watcher registration must be removed")
	_, err = m.GetServerConfig("fake")
	assert.Error(t, err, "config entry must be removed")
}

// TestManagerStopStopsAllWatchers: manager Stop must stop every per-server
// watcher before closing clients.
func TestManagerStopStopsAllWatchers(t *testing.T) {
	ftA, ftB := newWatchFakeTransport(true), newWatchFakeTransport(true)
	m, err := NewManager(Config{}, nil)
	require.NoError(t, err)
	m.started = true
	hA := registerFakeServer(t, m, "a", ftA)
	hB := registerFakeServer(t, m, "b", ftB)

	require.NoError(t, m.Stop())
	waitWatcher(t, hA)
	waitWatcher(t, hB)

	m.mu.RLock()
	left := len(m.watchers)
	m.mu.RUnlock()
	assert.Zero(t, left, "no watcher registrations may survive Stop")
}
