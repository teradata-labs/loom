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
package adapter

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
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// waitTransport is a minimal scripted stateless (2026-07-28) server: it
// answers server/discover and tools/list, serves scripted per-attempt
// tools/call results, and holds subscriptions/listen open so the test can
// inject the acknowledgment and resources/updated notifications.
type waitTransport struct {
	mu          sync.Mutex
	callResults []json.RawMessage // per tools/call attempt, in order
	callCount   int
	listenIDs   []json.RawMessage
	responses   chan []byte
}

func newWaitTransport(callResults ...json.RawMessage) *waitTransport {
	return &waitTransport{callResults: callResults, responses: make(chan []byte, 16)}
}

func (f *waitTransport) inject(data []byte) { f.responses <- data }

func (f *waitTransport) Send(_ context.Context, message []byte) error {
	var req protocol.Request
	if err := json.Unmarshal(message, &req); err != nil {
		return err
	}
	if req.ID == nil || req.Method == "" {
		return nil // notifications / client responses: unanswered
	}

	resp := protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID}
	switch req.Method {
	case "server/discover":
		res := &protocol.DiscoverResult{
			ResultType:        protocol.ResultTypeComplete,
			SupportedVersions: []string{protocol.Version20260728},
			CacheScope:        "public",
		}
		if err := res.SetServerInfo(protocol.Implementation{Name: "waitfake", Version: "1"}); err != nil {
			return err
		}
		resp.Result, _ = json.Marshal(res)
	case "tools/list":
		resp.Result, _ = json.Marshal(protocol.ToolListResult{Tools: []protocol.Tool{{
			Name:        "connect",
			Description: "test tool",
			InputSchema: map[string]interface{}{"type": "object"},
		}}})
	case "tools/call":
		f.mu.Lock()
		i := f.callCount
		f.callCount++
		f.mu.Unlock()
		if i >= len(f.callResults) {
			return fmt.Errorf("unscripted tools/call attempt %d", i)
		}
		resp.Result = f.callResults[i]
	case "subscriptions/listen":
		// Record the listen id and hold the stream open (no response).
		idRaw, _ := json.Marshal(req.ID)
		f.mu.Lock()
		f.listenIDs = append(f.listenIDs, idRaw)
		f.mu.Unlock()
		return nil
	default:
		resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
	}
	out, _ := json.Marshal(resp)
	f.responses <- out
	return nil
}

func (f *waitTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data := <-f.responses:
		return data, nil
	}
}

func (f *waitTransport) Close() error { return nil }

func (f *waitTransport) listenID(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.listenIDs)
		var id string
		if n > 0 {
			id = string(f.listenIDs[0])
		}
		f.mu.Unlock()
		if n > 0 {
			return id
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no subscriptions/listen arrived")
	return ""
}

func (f *waitTransport) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

func errorResultWithLink(msg, uri string) json.RawMessage {
	r, _ := json.Marshal(protocol.CallToolResult{IsError: true, Content: []protocol.Content{
		{Type: "text", Text: msg},
		{Type: "resource_link", URI: uri, Name: "availability"},
	}})
	return r
}

func errorResultPlain(msg string) json.RawMessage {
	r, _ := json.Marshal(protocol.CallToolResult{IsError: true, Content: []protocol.Content{
		{Type: "text", Text: msg},
	}})
	return r
}

func successResult(text string) json.RawMessage {
	r, _ := json.Marshal(protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: text}}})
	return r
}

func notifJSON(method, subID string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":{"_meta":{"%s":%s}}}`,
		method, protocol.MetaSubscriptionID, subID))
}

func waitAdapter(t *testing.T, ft *waitTransport) *MCPToolAdapter {
	t.Helper()
	c := client.NewClient(client.Config{Transport: ft})
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom", Version: "test"}))
	require.True(t, c.IsStateless())
	return NewMCPToolAdapter(c, protocol.Tool{Name: "connect", InputSchema: map[string]interface{}{"type": "object"}}, "waitfake")
}

// TestParkAndWake: a failed call that links a resource parks, is woken by
// notifications/resources/updated, retries, and succeeds — one Execute call,
// two wire attempts, zero agent-visible retries (issue #343).
func TestParkAndWake(t *testing.T) {
	uri := "test://slots"
	ft := newWaitTransport(
		errorResultWithLink("budget full", uri),
		successResult("connected"),
	)
	adapter := waitAdapter(t, ft)

	type outcome struct {
		res *shuttle.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := adapter.Execute(context.Background(), map[string]interface{}{})
		done <- outcome{res, err}
	}()

	subID := ft.listenID(t)
	ft.inject(notifJSON(protocol.NotificationSubscriptionAcknowledged, subID))
	ft.inject(notifJSON(protocol.NotificationResourceUpdated, subID))

	select {
	case o := <-done:
		require.NoError(t, o.err)
		require.True(t, o.res.Success, "parked call must succeed after wake: %+v", o.res)
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after resource update")
	}
	assert.Equal(t, 2, ft.calls(), "exactly one retry")
}

// TestNoLinkNoPark: a plain error (no linked resource) surfaces immediately
// with no subscription opened — pre-#343 behavior exactly.
func TestNoLinkNoPark(t *testing.T) {
	ft := newWaitTransport(errorResultPlain("hard failure"))
	adapter := waitAdapter(t, ft)

	start := time.Now()
	res, err := adapter.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, res.Success)
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "hard failure")
	assert.Less(t, time.Since(start), 2*time.Second, "must not park")
	ft.mu.Lock()
	defer ft.mu.Unlock()
	assert.Empty(t, ft.listenIDs, "no subscription for unlinked errors")
}

// TestParkTimeoutSurfacesError: a linked failure with no update within the
// context budget surfaces the original error.
func TestParkTimeoutSurfacesError(t *testing.T) {
	ft := newWaitTransport(errorResultWithLink("budget full", "test://slots"))
	adapter := waitAdapter(t, ft)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	res, err := adapter.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, res.Success)
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "budget full")
	assert.Equal(t, 1, ft.calls(), "no blind retries without an update")
}
