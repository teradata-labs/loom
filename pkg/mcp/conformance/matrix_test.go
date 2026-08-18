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
// Cross-cutting dual-revision matrix scenarios (Phase 8): the behaviors no
// single phase owns — both revision families served by one process at the
// same time, over the real Streamable HTTP transport.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	loomserver "github.com/teradata-labs/loom/pkg/mcp/server"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// matrixToolProvider is a minimal deterministic tool provider.
type matrixToolProvider struct{}

func (p *matrixToolProvider) ListTools(_ context.Context) ([]protocol.Tool, error) {
	return []protocol.Tool{{
		Name:        "noop",
		Description: "does nothing",
		InputSchema: map[string]interface{}{"type": "object"},
	}}, nil
}

func (p *matrixToolProvider) CallTool(_ context.Context, _ string, _ map[string]interface{}) (*protocol.CallToolResult, error) {
	return &protocol.CallToolResult{Content: []protocol.Content{{Type: "text", Text: "ok"}}}, nil
}

// newMatrixServer serves one MCPServer over real Streamable HTTP.
func newMatrixServer(t *testing.T, sessionTTL time.Duration) *httptest.Server {
	t.Helper()
	mcpServer := loomserver.NewMCPServer("loom-matrix", "1.4.0", nil,
		loomserver.WithToolProvider(&matrixToolProvider{}))
	httpSrv, err := transport.NewStreamableHTTPServer(transport.StreamableHTTPServerConfig{
		Handler:       mcpServer.HandleMessage,
		StreamHandler: mcpServer,
		SessionTTL:    sessionTTL,
	})
	require.NoError(t, err)
	ts := httptest.NewServer(httpSrv)
	t.Cleanup(ts.Close)
	t.Cleanup(httpSrv.Close)
	return ts
}

type wireResponse struct {
	status  int
	header  http.Header
	body    map[string]json.RawMessage // top-level JSON-RPC response fields
	rawBody []byte
}

// post sends one raw JSON-RPC request and decodes the response envelope.
func post(t *testing.T, url, body string, headers map[string]string) wireResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	out := wireResponse{status: resp.StatusCode, header: resp.Header, rawBody: raw}
	if len(raw) > 0 {
		out.body = map[string]json.RawMessage{}
		_ = json.Unmarshal(raw, &out.body)
	}
	return out
}

func legacyBody(id int, method string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"%s","params":{}}`, id, method)
}

func statelessMatrixBody(id int, method string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"%s","params":{"_meta":{"%s":"%s"}}}`,
		id, method, protocol.MetaProtocolVersion, protocol.Version20260728)
}

// X1: a legacy sessionful client and a stateless client interleave many
// requests against one server; neither mode bleeds into the other.
func TestMatrixMixedModeInterleave(t *testing.T) {
	ts := newMatrixServer(t, 0)

	// Legacy client initializes and gets a session.
	init := post(t, ts.URL, legacyBody(1, "initialize"), nil)
	require.Equal(t, http.StatusOK, init.status)
	sessionID := init.header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "legacy initialize mints a session")

	for i := 0; i < 50; i++ {
		// Legacy request rides the session; result must NOT carry the
		// stateless envelope.
		legacy := post(t, ts.URL, legacyBody(100+i, "tools/list"), map[string]string{"Mcp-Session-Id": sessionID})
		require.Equal(t, http.StatusOK, legacy.status, "legacy round %d", i)
		assert.NotContains(t, string(legacy.body["result"]), `"resultType"`, "legacy round %d", i)

		// Stateless request bypasses sessions; result carries the envelope
		// and never a session header.
		stateless := post(t, ts.URL, statelessMatrixBody(200+i, "tools/list"), nil)
		require.Equal(t, http.StatusOK, stateless.status, "stateless round %d", i)
		assert.Contains(t, string(stateless.body["result"]), `"resultType":"complete"`, "stateless round %d", i)
		assert.Empty(t, stateless.header.Get("Mcp-Session-Id"), "stateless round %d", i)
	}
}

// X2: the session janitor expires a legacy session mid-sequence; stateless
// traffic is unaffected and the legacy client recovers by re-initializing.
func TestMatrixJanitorExpiryRecovery(t *testing.T) {
	ts := newMatrixServer(t, 60*time.Millisecond)

	init := post(t, ts.URL, legacyBody(1, "initialize"), nil)
	sessionID := init.header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	// Wait past TTL + cleanup interval (TTL/2) for the janitor to sweep.
	// Every probe carrying the session header refreshes lastActivity, so the
	// poll interval must exceed the TTL or the probing itself keeps the
	// session alive forever.
	require.Eventually(t, func() bool {
		r := post(t, ts.URL, legacyBody(2, "tools/list"), map[string]string{"Mcp-Session-Id": sessionID})
		return r.status == http.StatusNotFound
	}, 5*time.Second, 250*time.Millisecond, "janitor must expire the idle session")

	// Stateless traffic never noticed.
	stateless := post(t, ts.URL, statelessMatrixBody(3, "tools/list"), nil)
	assert.Equal(t, http.StatusOK, stateless.status)

	// Legacy recovery: re-initialize mints a fresh session.
	reinit := post(t, ts.URL, legacyBody(4, "initialize"), nil)
	newSession := reinit.header.Get("Mcp-Session-Id")
	require.NotEmpty(t, newSession)
	assert.NotEqual(t, sessionID, newSession)
	ok := post(t, ts.URL, legacyBody(5, "tools/list"), map[string]string{"Mcp-Session-Id": newSession})
	assert.Equal(t, http.StatusOK, ok.status)
}

// X3: methods removed by the stateless revision split correctly per mode on
// the wire: answered in legacy, MethodNotFound under stateless _meta.
func TestMatrixDeprecatedMethodSplit(t *testing.T) {
	ts := newMatrixServer(t, 0)

	legacyPing := post(t, ts.URL, legacyBody(1, "ping"), nil)
	require.Equal(t, http.StatusOK, legacyPing.status)
	assert.NotContains(t, legacyPing.body, "error")

	statelessPing := post(t, ts.URL, statelessMatrixBody(2, "ping"), nil)
	require.Equal(t, http.StatusOK, statelessPing.status)
	var rpcErr struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(statelessPing.body["error"], &rpcErr))
	assert.Equal(t, protocol.MethodNotFound, rpcErr.Code)

	// resources/subscribe was never a server method here; MethodNotFound in
	// both modes (its replacement is subscriptions/listen).
	for _, body := range []string{legacyBody(3, "resources/subscribe"), statelessMatrixBody(4, "resources/subscribe")} {
		r := post(t, ts.URL, body, nil)
		require.NoError(t, json.Unmarshal(r.body["error"], &rpcErr))
		assert.Equal(t, protocol.MethodNotFound, rpcErr.Code)
	}
}

// X4: server/discover is idempotent and byte-identical across repeated calls
// in both stamped and unstamped forms (the result content must not vary; the
// stateless envelope stamp is mode-dependent by design, so compare the
// mode-stable core fields).
func TestMatrixDiscoverIdempotent(t *testing.T) {
	ts := newMatrixServer(t, 0)

	var unstampedFirst, stampedFirst string
	for i := 0; i < 10; i++ {
		un := post(t, ts.URL, legacyBody(10+i, "server/discover"), nil)
		require.Equal(t, http.StatusOK, un.status)
		if unstampedFirst == "" {
			unstampedFirst = string(un.body["result"])
			assert.Contains(t, unstampedFirst, `"supportedVersions"`)
		} else {
			assert.Equal(t, unstampedFirst, string(un.body["result"]), "unstamped discover call %d", i)
		}

		st := post(t, ts.URL, statelessMatrixBody(30+i, "server/discover"), nil)
		require.Equal(t, http.StatusOK, st.status)
		if stampedFirst == "" {
			stampedFirst = string(st.body["result"])
		} else {
			assert.Equal(t, stampedFirst, string(st.body["result"]), "stamped discover call %d", i)
		}
	}
}

// X8: malformed inputs are rejected in well-formed ways — never a 5xx, never
// a hang, never mode confusion.
func TestMatrixMalformedInputs(t *testing.T) {
	ts := newMatrixServer(t, 0)

	t.Run("truncated JSON is a ParseError", func(t *testing.T) {
		r := post(t, ts.URL, `{"jsonrpc":"2.0","id":1,"met`, nil)
		require.Equal(t, http.StatusOK, r.status)
		assert.Contains(t, string(r.rawBody), `"error"`)
	})

	t.Run("_meta with wrong-typed protocolVersion falls back to legacy handling", func(t *testing.T) {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"%s":123}}}`,
			protocol.MetaProtocolVersion)
		r := post(t, ts.URL, body, nil)
		require.Equal(t, http.StatusOK, r.status)
		assert.NotContains(t, string(r.body["result"]), `"resultType"`, "non-string version must not select stateless mode")
	})

	t.Run("oversized body is rejected without a 5xx", func(t *testing.T) {
		// The transport caps request bodies at 10MB; an 11MB body must fail
		// as a client error, not crash or hang.
		huge := `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"pad":"` +
			strings.Repeat("x", 11*1024*1024) + `"}}`
		req, err := http.NewRequest(http.MethodPost, ts.URL, bytes.NewReader([]byte(huge)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Less(t, resp.StatusCode, 500, "oversized body must be a client-side failure")
	})

	t.Run("wrong content type is rejected", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader("method=ping"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
	})
}
