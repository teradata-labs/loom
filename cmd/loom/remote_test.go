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

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEdge mimics the loom-mcp Streamable-HTTP endpoint: bearer-gated
// initialize, then SSE tools/call responses with progress notifications and a
// final result whose second content block is a protojson WeaveResponse.
func fakeEdge(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "mcp-sess-1")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"serverInfo":{"name":"fake"}}}`, req.ID)
		case "tools/call":
			calls = append(calls, req.Params.Arguments)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"t\",\"progress\":1,\"total\":3,\"message\":\"planning\"}}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"t\",\"progress\":2,\"total\":3}}\n\n")
			final := map[string]any{
				"jsonrpc": "2.0", "id": json.RawMessage(req.ID),
				"result": map[string]any{
					"isError": false,
					"content": []map[string]any{
						{"type": "text", "text": "the answer"},
						{"type": "text", "text": `{"sessionId":"weave-sess-42","agentId":"a1"}`},
					},
				},
			}
			data, _ := json.Marshal(final)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
		}
	}))
	return srv, &calls
}

func TestRemoteClient_WeaveStreamsProgressAndThreadsSession(t *testing.T) {
	srv, calls := fakeEdge(t)
	defer srv.Close()

	rc := &remoteClient{baseURL: srv.URL + "/", token: "test-token", hc: srv.Client()}
	require.NoError(t, rc.initialize())
	assert.Equal(t, "mcp-sess-1", rc.mcpSession)

	var notes []string
	answer, err := rc.weave("first question", func(n string) { notes = append(notes, n) })
	require.NoError(t, err)
	assert.Equal(t, "the answer", answer)
	assert.Equal(t, []string{"planning", "progress 2/3"}, notes,
		"message-bearing and bare progress events should both render")
	assert.Equal(t, "weave-sess-42", rc.weaveSession, "sessionId from the WeaveResponse block must be captured")

	_, err = rc.weave("second question", nil)
	require.NoError(t, err)
	require.Len(t, *calls, 2)
	assert.Nil(t, (*calls)[0]["session_id"], "first turn starts a fresh session")
	assert.Equal(t, "weave-sess-42", (*calls)[1]["session_id"], "second turn must continue the session")
}

func TestRemoteClient_Unauthorized(t *testing.T) {
	srv, _ := fakeEdge(t)
	defer srv.Close()

	rc := &remoteClient{baseURL: srv.URL + "/", token: "wrong", hc: srv.Client()}
	err := rc.initialize()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loom login")
}

func TestRemoteClient_ToolErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"isError":true,"content":[{"type":"text","text":"weave failed: boom"}]}}`, req.ID)
	}))
	defer srv.Close()

	rc := &remoteClient{baseURL: srv.URL + "/", token: "t", hc: srv.Client()}
	require.NoError(t, rc.initialize())
	_, err := rc.weave("q", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "weave failed: boom")
}
