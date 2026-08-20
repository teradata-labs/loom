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
		"Mcp-Method":           "tools/list",
		"MCP-Protocol-Version": protocol.Version20260728,
		"Mcp-Session-Id":       "stale-or-unknown-session",
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

func TestStatelessMissingHeadersRejected(t *testing.T) {
	// Review finding 1 (PR #328): the required standard headers are exactly
	// that — a stateless request without MCP-Protocol-Version and Mcp-Method
	// is rejected 400 with HeaderMismatch, never executed.
	_, ts, calls := newStatelessTestServer(t)

	resp := postJSON(t, ts.URL, statelessBody(5, "tools/list"), nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, 0, *calls, "a request failing header validation must not reach the handler")
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

func statelessHeaders(method string) map[string]string {
	return map[string]string{
		"Mcp-Method":           method,
		"MCP-Protocol-Version": protocol.Version20260728,
	}
}

// TestStatelessAdmissionIsExactVersion (review finding 1, PR #328): admission
// must not be an open-ended date comparison — a future revision this server
// never implemented is rejected with UnsupportedProtocolVersion listing the
// supported set, so the client can retry with a mutual revision.
func TestStatelessAdmissionIsExactVersion(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 7, "method": "tools/list",
		"params": map[string]interface{}{
			"_meta": map[string]interface{}{protocol.MetaProtocolVersion: "2027-01-01"},
		},
	})
	resp := postJSON(t, ts.URL, body, map[string]string{
		"Mcp-Method":           "tools/list",
		"MCP-Protocol-Version": "2027-01-01",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, 0, *calls)

	var rpc struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Supported []string `json:"supported"`
				Requested string   `json:"requested"`
			} `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpc))
	assert.Equal(t, protocol.UnsupportedProtocolVersion, rpc.Error.Code)
	assert.Equal(t, []string{protocol.Version20260728}, rpc.Error.Data.Supported)
	assert.Equal(t, "2027-01-01", rpc.Error.Data.Requested)
}

// TestLegacyBodyWithModernHeaderRejected: a legacy-shaped body under a modern
// MCP-Protocol-Version header must not slip into legacy dispatch — it is a
// header/body disagreement.
func TestLegacyBodyWithModernHeaderRejected(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	legacyBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 8, "method": "tools/list", "params": map[string]interface{}{},
	})
	resp := postJSON(t, ts.URL, legacyBody, map[string]string{
		"MCP-Protocol-Version": protocol.Version20260728,
		"Mcp-Method":           "tools/list",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, 0, *calls)
}

// TestMcpNameRequiredAndDecoded: tools/call, prompts/get, and resources/read
// require an Mcp-Name header matching the body value, after Base64-sentinel
// decoding.
func TestMcpNameRequiredAndDecoded(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	callBody := func(id int) []byte {
		body, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]interface{}{
				"_meta": map[string]interface{}{protocol.MetaProtocolVersion: protocol.Version20260728},
				"name":  "höllo tool", // not header-safe: must travel sentinel-encoded
			},
		})
		return body
	}

	// Missing Mcp-Name → rejected.
	h := statelessHeaders("tools/call")
	resp := postJSON(t, ts.URL, callBody(9), h)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Correctly encoded Mcp-Name → decoded, matched, executed.
	h["Mcp-Name"] = protocol.EncodeHeaderValue("höllo tool")
	resp = postJSON(t, ts.URL, callBody(10), h)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Mismatched Mcp-Name → rejected.
	h["Mcp-Name"] = "other-tool"
	before := *calls
	resp = postJSON(t, ts.URL, callBody(11), h)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, before, *calls)
}

// TestMalformedMcpParamRejected: an Mcp-Param header with an invalid Base64
// sentinel payload contains invalid characters per the specification and is
// rejected; well-formed unrecognized params are ignored (no tool served here
// declares x-mcp-header annotations).
func TestMalformedMcpParamRejected(t *testing.T) {
	_, ts, _ := newStatelessTestServer(t)

	h := statelessHeaders("tools/list")
	h["Mcp-Param-Region"] = "=?base64?!!!not-base64!!!?="
	resp := postJSON(t, ts.URL, statelessBody(12, "tools/list"), h)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	h["Mcp-Param-Region"] = "us-west1" // well-formed, unrecognized → ignored
	resp = postJSON(t, ts.URL, statelessBody(13, "tools/list"), h)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestUnknownMethodIs404 (review finding 14, PR #328): a modern request for a
// method this server does not implement answers HTTP 404 with the JSON-RPC
// MethodNotFound body, distinguishing it from a legacy server without the
// modern endpoint.
func TestUnknownMethodIs404(t *testing.T) {
	srv, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler: func(ctx context.Context, msg []byte) ([]byte, error) {
			var req protocol.Request
			_ = json.Unmarshal(msg, &req)
			resp, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]interface{}{"code": protocol.MethodNotFound, "message": "method not found"},
			})
			return resp, nil
		},
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp := postJSON(t, ts.URL, statelessBody(14, "no/such/method"), statelessHeaders("no/such/method"))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	var rpc struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpc))
	assert.Equal(t, protocol.MethodNotFound, rpc.Error.Code)
}

// TestOriginValidation (review finding 2, PR #328): a present-but-invalid
// Origin is rejected 403 before any body or session processing; absent
// Origins (non-browser clients) and loopback origins pass by default, and a
// configured allowlist replaces the default policy.
func TestOriginValidation(t *testing.T) {
	_, ts, calls := newStatelessTestServer(t)

	h := statelessHeaders("tools/list")
	h["Origin"] = "https://evil.example.com"
	resp := postJSON(t, ts.URL, statelessBody(15, "tools/list"), h)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, 0, *calls, "disallowed origins must not reach the handler")

	h["Origin"] = "http://localhost:3000"
	resp = postJSON(t, ts.URL, statelessBody(16, "tools/list"), h)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	srvAllow, err := NewStreamableHTTPServer(StreamableHTTPServerConfig{
		Handler:        func(ctx context.Context, msg []byte) ([]byte, error) { return nil, nil },
		AllowedOrigins: []string{"https://app.example.com"},
	})
	require.NoError(t, err)
	tsAllow := httptest.NewServer(srvAllow)
	t.Cleanup(tsAllow.Close)

	h2 := statelessHeaders("tools/list")
	h2["Origin"] = "https://app.example.com"
	resp = postJSON(t, tsAllow.URL, statelessBody(17, "tools/list"), h2)
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode)

	h2["Origin"] = "http://localhost:3000" // not in the explicit allowlist
	resp = postJSON(t, tsAllow.URL, statelessBody(18, "tools/list"), h2)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
