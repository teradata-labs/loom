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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// fakeStreamHandler is a StreamingMCPHandler used to exercise the transport's
// POST-response SSE path without pulling in the MCP server / gRPC bridge.
type fakeStreamHandler struct {
	progressEvents int    // number of notifications/progress events to emit
	finalText      string // text payload of the final tools/call result
	called         bool   // set true when HandleMessageStream is invoked
}

func (f *fakeStreamHandler) HandleMessageStream(_ context.Context, msg []byte, w SSEWriter) ([]byte, error) {
	f.called = true
	var req struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
	}
	_ = json.Unmarshal(msg, &req)

	for i := 1; i <= f.progressEvents; i++ {
		notif := fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"tok","progress":%d,"total":100}}`, i*50)
		if err := w.WriteEvent([]byte(notif)); err != nil {
			return nil, err
		}
	}

	id := "null"
	if req.ID != nil {
		id = string(*req.ID)
	}
	final := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":%q}]}}`, id, f.finalText)
	return []byte(final), nil
}

// sseEvent is a parsed Server-Sent Event (data payload only).
type sseEvent struct {
	data string
}

// parseSSE collects the data payloads from an SSE response body, in order.
func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var data string
		sawData := false
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data:") {
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				sawData = true
			}
		}
		if sawData {
			events = append(events, sseEvent{data: data})
		}
	}
	return events
}

func newSSEServer(t *testing.T, sh StreamingMCPHandler) *StreamableHTTPServer {
	t.Helper()
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(msg []byte) ([]byte, error) {
			// Synchronous fallback path: echo a trivial JSON-RPC result.
			var req struct {
				ID     *json.RawMessage `json:"id"`
				Method string           `json:"method"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				return nil, err
			}
			if req.ID == nil {
				return nil, nil
			}
			return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"sync":true}}`, string(*req.ID))), nil
		},
		StreamHandler: sh,
		Logger:        zaptest.NewLogger(t),
	})
	require.NoError(t, err)
	return srv
}

func postSSE(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestStreamableHTTPServer_GET_Returns405(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, "POST, DELETE", resp.Header.Get("Allow"))
}

func TestStreamableHTTPServer_POST_SSE_HappyPath(t *testing.T) {
	sh := &fakeStreamHandler{progressEvents: 2, finalText: "the answer"}
	srv := newSSEServer(t, sh)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := postSSE(t, ts.URL, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"loom_weave","arguments":{"query":"hi"},"_meta":{"progressToken":"tok"}}}`)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.True(t, sh.called, "stream handler should be invoked")

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	events := parseSSE(t, string(bodyBytes))

	require.Len(t, events, 3, "expected 2 progress events + 1 final result")

	// First two events are progress notifications.
	for i := 0; i < 2; i++ {
		var notif struct {
			Method string `json:"method"`
			Params struct {
				Progress float64 `json:"progress"`
			} `json:"params"`
		}
		require.NoError(t, json.Unmarshal([]byte(events[i].data), &notif))
		assert.Equal(t, "notifications/progress", notif.Method)
		assert.Equal(t, float64((i+1)*50), notif.Params.Progress)
	}

	// Last event is the final tools/call result echoing the request id.
	var final struct {
		ID     int64 `json:"id"`
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(events[2].data), &final))
	assert.Equal(t, int64(7), final.ID)
	require.Len(t, final.Result.Content, 1)
	assert.Equal(t, "the answer", final.Result.Content[0].Text)
}

func TestStreamableHTTPServer_POST_NoAcceptSSE_UsesJSON(t *testing.T) {
	sh := &fakeStreamHandler{progressEvents: 2, finalText: "x"}
	srv := newSSEServer(t, sh)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// No Accept: text/event-stream -> synchronous JSON path, stream handler untouched.
	resp, err := http.Post(ts.URL, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"loom_weave"}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.False(t, sh.called, "stream handler must not be used without Accept: text/event-stream")

	bodyBytes, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(bodyBytes), `"sync":true`)
}

func TestStreamableHTTPServer_POST_SSE_NoStreamHandler_FallsBackToJSON(t *testing.T) {
	// newTestServer configures no StreamHandler; an SSE-accepting POST must still
	// get a normal JSON response.
	srv := newTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := postSSE(t, ts.URL, `{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestStreamableHTTPServer_POST_SSE_InitializeUsesJSON(t *testing.T) {
	// Even with Accept: text/event-stream, initialize must use the JSON path so
	// the session is created and the Mcp-Session-Id header is returned.
	sh := &fakeStreamHandler{progressEvents: 1, finalText: "x"}
	srv := newSSEServer(t, sh)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := postSSE(t, ts.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.NotEmpty(t, resp.Header.Get("Mcp-Session-Id"), "initialize must create a session")
	assert.False(t, sh.called, "initialize must not take the streaming path")
}
