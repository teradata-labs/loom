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
// Tests for 2026-07-28 dual-mode admission on the Streamable HTTP server.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
)

func newStatelessTestServer(t *testing.T) (*StreamableHTTPServer, *httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(ctx context.Context, msg []byte) ([]byte, error) {
			calls++
			var req protocol.Request
			if err := json.Unmarshal(msg, &req); err != nil {
				return nil, err
			}
			if req.ID == nil {
				return nil, nil // notification
			}
			resp, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID, "result": map[string]interface{}{"resultType": "complete"},
			})
			return resp, nil
		},
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts, &calls
}

func statelessBody(id int, method string) []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": method,
		"params": map[string]interface{}{
			"_meta": map[string]interface{}{
				protocol.MetaProtocolVersion: protocol.Version20260728,
			},
		},
	})
	return body
}

func postJSON(t *testing.T, url string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestStatelessRequestAdmittedWithoutSession(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	resp := postJSON(t, ts.URL, statelessBody(1, "tools/list"), map[string]string{
		"Mcp-Method":           "tools/list",
		"MCP-Protocol-Version": protocol.Version20260728,
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Mcp-Session-Id"), "stateless responses never carry a session header")
	assert.Equal(t, 1, *calls)
}

func TestStatelessRequestIgnoresStaleSessionHeader(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	resp := postJSON(t, ts.URL, statelessBody(2, "tools/list"), map[string]string{
		"Mcp-Session-Id": "stale-or-unknown-session",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode, "stateless requests must not 404 on unknown sessions")
	assert.Equal(t, 1, *calls)
}

func TestHeaderMismatchRejected(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	resp := postJSON(t, ts.URL, statelessBody(3, "tools/call"), map[string]string{
		"Mcp-Method": "tools/list", // disagrees with body
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var rpcResp struct {
		ID    int `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpcResp))
	assert.Equal(t, protocol.HeaderMismatch, rpcResp.Error.Code)
	assert.Equal(t, 3, rpcResp.ID, "error must be routable to the request")
	assert.Zero(t, *calls, "mismatched requests must not reach the handler")
}

func TestProtocolVersionHeaderMismatchRejected(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	resp := postJSON(t, ts.URL, statelessBody(4, "tools/list"), map[string]string{
		"MCP-Protocol-Version": protocol.Version20251125, // disagrees with _meta
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var rpcResp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpcResp))
	assert.Equal(t, protocol.HeaderMismatch, rpcResp.Error.Code)
	assert.Zero(t, *calls)
}

func TestStatelessMissingHeadersTolerated(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	resp := postJSON(t, ts.URL, statelessBody(5, "tools/list"), nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "missing headers tolerated during the window")
	assert.Equal(t, 1, *calls)
}

func TestStatelessNotificationAccepted(t *testing.T) {
	_, ts, _ := newStatelessTestServer(t)

	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "method": "notifications/whatever",
		"params": map[string]interface{}{
			"_meta": map[string]interface{}{protocol.MetaProtocolVersion: protocol.Version20260728},
		},
	})
	resp := postJSON(t, ts.URL, body, nil)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestLegacyPathsUnchangedByDualMode(t *testing.T) {
	_, ts, _ := newStatelessTestServer(t)

	// Legacy request with an unknown session still 404s.
	legacyBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 9, "method": "tools/list", "params": map[string]interface{}{},
	})
	resp := postJSON(t, ts.URL, legacyBody, map[string]string{"Mcp-Session-Id": "unknown"})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Legacy initialize still mints a session.
	initBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 10, "method": "initialize", "params": map[string]interface{}{},
	})
	resp = postJSON(t, ts.URL, initBody, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Mcp-Session-Id"), "legacy initialize mints a session")
}
