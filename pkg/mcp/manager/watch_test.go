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

func startWatcher(t *testing.T, ft *watchFakeTransport) (*Manager, *client.Client, chan struct{}) {
	t.Helper()
	cl := client.NewClient(client.Config{Transport: ft})
	t.Cleanup(func() { _ = cl.Close() })
	require.NoError(t, cl.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	require.True(t, cl.IsStateless())

	m, err := NewManager(Config{}, nil)
	require.NoError(t, err)

	stopCh := make(chan struct{})
	m.watchWG.Add(1)
	go m.watchToolLists("fake", cl, stopCh)
	return m, cl, stopCh
}

func TestWatchSubscribesThenReconcilesOnAck(t *testing.T) {
	ft := newWatchFakeTransport(true)
	m, _, stopCh := startWatcher(t, ft)

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

	close(stopCh)
	waitDone(t, &m.watchWG)
}

func TestWatchExitsWhenListenUnsupported(t *testing.T) {
	ft := newWatchFakeTransport(false)
	m, _, stopCh := startWatcher(t, ft)

	// The watcher must exit on its own (MethodNotFound), without stopCh.
	waitDone(t, &m.watchWG)
	close(stopCh)
	assert.Zero(t, ft.listCalls(), "no fetches against a server without subscriptions support")
}

func waitDone(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
}
