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
// Phase 1 migration tests: revision pins, widened discover fallback, central
// result-envelope checking, and x-mcp-header tool handling.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// scriptedTransport simulates servers of different eras and records what the
// client sent.
type scriptedTransport struct {
	mu sync.Mutex

	// Behavior
	discoverErr       error                    // transport-level error for server/discover
	discoverRPCErr    *protocol.Error          // JSON-RPC error answer for server/discover (overrides discoverResult)
	discoverSilent    bool                     // swallow the probe entirely (legacy server that never answers)
	discoverResult    *protocol.DiscoverResult // nil → JSON-RPC MethodNotFound
	initializeVersion string                   // "" → 2024-11-05 (unless initializeEcho)
	initializeEcho    bool                     // echo the requested protocolVersion (dual-revision server)
	carriesHeaders    bool                     // pose as a Streamable HTTP transport (RequestHeaderCarrier)
	tools             []protocol.Tool
	callResult        json.RawMessage   // result payload for tools/call
	callResults       []json.RawMessage // per-attempt results; overrides callResult when set
	callStreamLost    int               // answer this many leading tools/call attempts with CodeStreamLost
	streamLostOn      map[string]int    // per-method leading CodeStreamLost answers (any method)

	// Recording
	sawDiscover       bool
	sawInitialize     bool
	requestedInitVer  string // protocolVersion the client sent in InitializeParams
	lastExtraHeaders  map[string]string
	sentNotifications []protocol.Request // client-sent notifications (no id)
	attempts          []string           // default-case request methods that executed
	sentResponseRaw   [][]byte           // client-sent responses (answers to server-initiated requests)
	callParams        []json.RawMessage  // raw params of every tools/call attempt
	listenSupported   bool               // answer subscriptions/listen by holding the stream open
	listenParams      []json.RawMessage  // raw params of every subscriptions/listen request
	listenIDs         []json.RawMessage  // raw JSON-RPC ids of listen requests
	toolsListCalls    int

	responses chan []byte
}

// inject delivers a raw server-to-client message (notification or response).
func (f *scriptedTransport) inject(data []byte) { f.responses <- data }

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{responses: make(chan []byte, 10)}
}

func (f *scriptedTransport) Send(ctx context.Context, message []byte) error {
	var req protocol.Request
	if err := json.Unmarshal(message, &req); err != nil {
		return err
	}

	f.mu.Lock()
	f.lastExtraHeaders = transport.ExtraHeadersFromContext(ctx)
	f.mu.Unlock()

	if req.ID == nil {
		f.mu.Lock()
		f.sentNotifications = append(f.sentNotifications, req)
		f.mu.Unlock()
		return nil // notifications are not answered
	}

	if req.Method == "" {
		// A JSON-RPC response from the client (answer to a server-initiated
		// request); record it and do not answer.
		f.mu.Lock()
		f.sentResponseRaw = append(f.sentResponseRaw, append([]byte(nil), message...))
		f.mu.Unlock()
		return nil
	}

	var resp protocol.Response
	resp.JSONRPC = protocol.JSONRPCVersion
	resp.ID = req.ID

	switch req.Method {
	case "server/discover":
		f.mu.Lock()
		f.sawDiscover = true
		derr, drpc, dres, silent := f.discoverErr, f.discoverRPCErr, f.discoverResult, f.discoverSilent
		f.mu.Unlock()
		if silent {
			return nil // never answers: the probe must give up on its own
		}
		if derr != nil {
			return derr
		}
		if drpc != nil {
			resp.Error = drpc
			break
		}
		if dres == nil {
			resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
			break
		}
		resp.Result, _ = json.Marshal(dres)
	case "initialize":
		var initParams protocol.InitializeParams
		_ = json.Unmarshal(req.Params, &initParams)
		f.mu.Lock()
		f.sawInitialize = true
		f.requestedInitVer = initParams.ProtocolVersion
		version := f.initializeVersion
		echo := f.initializeEcho
		f.mu.Unlock()
		if echo {
			version = initParams.ProtocolVersion
		} else if version == "" {
			version = protocol.Version20241105
		}
		result := protocol.InitializeResult{
			ProtocolVersion: version,
			ServerInfo:      protocol.Implementation{Name: "scripted", Version: "1.0"},
		}
		resp.Result, _ = json.Marshal(result)
	case "tools/list":
		f.mu.Lock()
		f.toolsListCalls++
		tools := f.tools
		f.mu.Unlock()
		resp.Result, _ = json.Marshal(protocol.ToolListResult{Tools: tools})
	case "subscriptions/listen":
		f.mu.Lock()
		supported := f.listenSupported
		if supported {
			f.listenParams = append(f.listenParams, append(json.RawMessage(nil), req.Params...))
			idJSON, _ := json.Marshal(req.ID)
			f.listenIDs = append(f.listenIDs, idJSON)
		}
		f.mu.Unlock()
		if supported {
			return nil // stream stays open; the test injects events
		}
		resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
	case "tools/call":
		f.mu.Lock()
		f.callParams = append(f.callParams, append(json.RawMessage(nil), req.Params...))
		attempt := len(f.callParams) - 1
		if attempt < f.callStreamLost {
			resp.Error = protocol.NewError(transport.CodeStreamLost, "response stream lost before completion; re-issue the request", nil)
		} else if len(f.callResults) > 0 {
			idx := attempt - f.callStreamLost
			if idx >= len(f.callResults) {
				idx = len(f.callResults) - 1
			}
			resp.Result = f.callResults[idx]
		} else {
			resp.Result = f.callResult
		}
		f.mu.Unlock()
	default:
		f.mu.Lock()
		if n := f.streamLostOn[req.Method]; n > 0 {
			f.streamLostOn[req.Method] = n - 1
			f.mu.Unlock()
			resp.Error = protocol.NewError(transport.CodeStreamLost, "response stream lost before completion; re-issue the request", nil)
			break
		}
		f.attempts = append(f.attempts, req.Method)
		f.mu.Unlock()
		resp.Result = json.RawMessage(`{"resultType":"complete","ok":true}`)
	}

	data, _ := json.Marshal(resp)
	f.responses <- data
	return nil
}

func (f *scriptedTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data := <-f.responses:
		return data, nil
	}
}

func (f *scriptedTransport) Close() error { return nil }

// CarriesRequestHeaders lets a test pose as a Streamable HTTP transport
// (transport.RequestHeaderCarrier); the zero value simulates stdio.
func (f *scriptedTransport) CarriesRequestHeaders() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.carriesHeaders
}

func (f *scriptedTransport) notificationsSent() []protocol.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.Request(nil), f.sentNotifications...)
}

func (f *scriptedTransport) sentResponses() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.sentResponseRaw...)
}

func (f *scriptedTransport) snapshot() (sawDiscover, sawInitialize bool, headers map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sawDiscover, f.sawInitialize, f.lastExtraHeaders
}

func statelessDiscoverResult() *protocol.DiscoverResult {
	return discoverResultOffering(protocol.Version20260728, protocol.Version20241105)
}

// discoverResultOffering builds an official-shape DiscoverResult advertising
// the given revisions, with the server identity in _meta as the schema
// requires.
func discoverResultOffering(versions ...string) *protocol.DiscoverResult {
	res := &protocol.DiscoverResult{
		ResultType:        protocol.ResultTypeComplete,
		SupportedVersions: versions,
		CacheScope:        "public",
	}
	if err := res.SetServerInfo(protocol.Implementation{Name: "scripted", Version: "1.0"}); err != nil {
		panic(err)
	}
	return res
}

func connectClient(t *testing.T, ft *scriptedTransport, cfg Config) *Client {
	t.Helper()
	cfg.Transport = ft
	c := NewClient(cfg)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestConnectFallsBackOnBareHTTPStatus(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented} {
		ft := newScriptedTransport()
		ft.discoverErr = &transport.HTTPStatusError{Code: code}
		c := connectClient(t, ft, Config{})

		require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}), "code %d", code)
		assert.False(t, c.IsStateless())
		_, sawInit, _ := ft.snapshot()
		assert.True(t, sawInit, "code %d must fall back to initialize", code)
	}
}

func TestConnectDoesNotFallBackOnAuthOrServerErrors(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		ft := newScriptedTransport()
		ft.discoverErr = &transport.HTTPStatusError{Code: code}
		c := connectClient(t, ft, Config{})

		require.Error(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}), "code %d", code)
		_, sawInit, _ := ft.snapshot()
		assert.False(t, sawInit, "code %d must not silently fall back", code)
	}
}

func TestConnectPinLegacySkipsProbe(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverErr = &transport.HTTPStatusError{Code: http.StatusInternalServerError} // probing would fail hard
	c := connectClient(t, ft, Config{ProtocolVersion: "legacy"})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	sawDiscover, sawInit, _ := ft.snapshot()
	assert.False(t, sawDiscover, "pinned legacy must not probe server/discover")
	assert.True(t, sawInit)
	assert.Equal(t, protocol.Version20241105, c.NegotiatedVersion())
}

func TestConnectPinStatelessOffered(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = statelessDiscoverResult()
	c := connectClient(t, ft, Config{ProtocolVersion: protocol.Version20260728})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	assert.True(t, c.IsStateless())
	assert.Equal(t, protocol.Version20260728, c.NegotiatedVersion())
}

func TestConnectPinStatelessNotOfferedFailsWithoutFallback(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = discoverResultOffering(protocol.Version20251125)
	c := connectClient(t, ft, Config{ProtocolVersion: protocol.Version20260728})

	require.Error(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	_, sawInit, _ := ft.snapshot()
	assert.False(t, sawInit, "an explicit pin must not silently downgrade")
}

func TestConnectPinLegacyFamilyVersion(t *testing.T) {
	ft := newScriptedTransport()
	c := connectClient(t, ft, Config{ProtocolVersion: protocol.Version20241105})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	sawDiscover, _, _ := ft.snapshot()
	assert.False(t, sawDiscover)

	// Mismatch: server negotiates a different handshake revision than pinned.
	ft2 := newScriptedTransport()
	ft2.initializeVersion = protocol.Version20250326
	c2 := connectClient(t, ft2, Config{ProtocolVersion: protocol.Version20241105})
	require.Error(t, c2.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
}

func TestConnectPinUnknownVersionRejected(t *testing.T) {
	ft := newScriptedTransport()
	c := connectClient(t, ft, Config{ProtocolVersion: "2031-01-01"})
	require.Error(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
}

func simpleTool(name string, schema map[string]interface{}) protocol.Tool {
	if schema == nil {
		schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	return protocol.Tool{Name: name, Description: "test tool", InputSchema: schema}
}

func TestStatelessCallToolRejectsInputRequired(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = statelessDiscoverResult()
	ft.tools = []protocol.Tool{simpleTool("do_thing", nil)}
	ft.callResult = json.RawMessage(`{"resultType":"input_required","inputRequests":{},"requestState":"abc"}`)
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	_, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.Error(t, err)
	var irErr *InputRequiredNotSupportedError
	require.True(t, errors.As(err, &irErr), "want *InputRequiredNotSupportedError, got %T: %v", err, err)
	assert.Equal(t, "tools/call", irErr.Method)
}

func TestLegacyResultWithoutEnvelopeIsComplete(t *testing.T) {
	ft := newScriptedTransport()
	ft.tools = []protocol.Tool{simpleTool("do_thing", nil)}
	ft.callResult = json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)
	c := connectClient(t, ft, Config{ProtocolVersion: "legacy"})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	result, err := c.CallTool(context.Background(), "do_thing", map[string]interface{}{})
	require.NoError(t, err)
	require.NotNil(t, result)
}

func TestListToolsFiltersInvalidHeaderAnnotations(t *testing.T) {
	valid := simpleTool("good", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"region": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
		},
	})
	invalid := simpleTool("bad", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"items_field": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string", "x-mcp-header": "Item"},
			},
		},
	})

	ft := newScriptedTransport()
	ft.carriesHeaders = true // Streamable HTTP: rejection is a MUST
	ft.discoverResult = statelessDiscoverResult()
	ft.tools = []protocol.Tool{valid, invalid}
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	tools, err := c.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "good", tools[0].Name)

	_, err = c.CallTool(context.Background(), "bad", map[string]interface{}{})
	require.Error(t, err, "rejected tool must not be callable")
}

// TestListToolsIgnoresHeaderAnnotationsOffStreamableHTTP (review finding 10,
// PR #327): the MUST-reject rule is scoped to Streamable HTTP clients;
// clients on other transports (stdio) MAY ignore x-mcp-header annotations
// entirely and must not hide tools over them. Same for legacy-negotiated
// connections, where the annotation does not exist.
func TestListToolsIgnoresHeaderAnnotationsOffStreamableHTTP(t *testing.T) {
	invalid := simpleTool("bad", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"items_field": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string", "x-mcp-header": "Item"},
			},
		},
	})

	t.Run("stdio stateless", func(t *testing.T) {
		ft := newScriptedTransport() // carriesHeaders unset → stdio-like
		ft.discoverResult = statelessDiscoverResult()
		ft.tools = []protocol.Tool{invalid}
		c := connectClient(t, ft, Config{})
		require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

		tools, err := c.ListTools(context.Background())
		require.NoError(t, err)
		require.Len(t, tools, 1, "stdio clients must not hide tools over header annotations")
		assert.Equal(t, "bad", tools[0].Name)
	})

	t.Run("legacy streamable http", func(t *testing.T) {
		ft := newScriptedTransport()
		ft.carriesHeaders = true // Streamable HTTP transport, but legacy revision
		ft.tools = []protocol.Tool{invalid}
		c := connectClient(t, ft, Config{ProtocolVersion: "legacy"})
		require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

		tools, err := c.ListTools(context.Background())
		require.NoError(t, err)
		require.Len(t, tools, 1, "the annotation does not exist under legacy revisions; tools must not be hidden")
	})
}

// TestCallToolDoesNotMirrorHeadersOffStreamableHTTP: with enforcement off,
// no Mcp-Param-* values may be mirrored even for tools whose schema carries
// valid annotations.
func TestCallToolDoesNotMirrorHeadersOffStreamableHTTP(t *testing.T) {
	ft := newScriptedTransport() // stdio-like
	ft.discoverResult = statelessDiscoverResult()
	ft.tools = []protocol.Tool{simpleTool("execute_sql", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"region": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
		},
	})}
	ft.callResult = json.RawMessage(`{"content":[],"resultType":"complete"}`)
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	_, err := c.CallTool(context.Background(), "execute_sql", map[string]interface{}{"region": "us-west1"})
	require.NoError(t, err)

	_, _, headers := ft.snapshot()
	_, mirrored := headers["Mcp-Param-Region"]
	assert.False(t, mirrored, "header mirroring is a Streamable HTTP behavior; stdio must not mirror")
}

func TestCallToolMirrorsHeaderParams(t *testing.T) {
	ft := newScriptedTransport()
	ft.carriesHeaders = true // mirroring is a Streamable HTTP behavior
	ft.discoverResult = statelessDiscoverResult()
	ft.tools = []protocol.Tool{simpleTool("execute_sql", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"region": map[string]interface{}{"type": "string", "x-mcp-header": "Region"},
			"query":  map[string]interface{}{"type": "string"},
		},
	})}
	ft.callResult = json.RawMessage(`{"content":[],"resultType":"complete"}`)
	c := connectClient(t, ft, Config{})
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))

	_, err := c.CallTool(context.Background(), "execute_sql", map[string]interface{}{
		"region": "us-west1",
		"query":  "SELECT 1",
	})
	require.NoError(t, err)

	_, _, headers := ft.snapshot()
	assert.Equal(t, "us-west1", headers["Mcp-Param-Region"])
	assert.Equal(t, protocol.Version20260728, headers["MCP-Protocol-Version"],
		"stateless requests must carry the negotiated version header")
}

func (f *scriptedTransport) requestedInitVersion() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requestedInitVer
}

// TestConnectPinLegacyRequestsPinnedRevision is review finding 4's scenario:
// a dual-revision server (echoes any requested handshake version) with an
// explicit 2025-03-26 pin. The handshake must request the pinned revision —
// requesting 2024-11-05 would negotiate 2024-11-05 and fail the pin check.
func TestConnectPinLegacyRequestsPinnedRevision(t *testing.T) {
	ft := newScriptedTransport()
	ft.initializeEcho = true
	c := connectClient(t, ft, Config{ProtocolVersion: protocol.Version20250326})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	assert.Equal(t, protocol.Version20250326, ft.requestedInitVersion(),
		"InitializeParams must carry the pinned revision")
	assert.Equal(t, protocol.Version20250326, c.NegotiatedVersion())
	assert.False(t, c.IsStateless())
}

// TestConnectFallbackRequestsLatestLegacy: with no pin and no discover
// support, the handshake asks for the newest legacy revision this client
// speaks, per legacy negotiation (client requests its latest; server answers
// with that or its own latest).
func TestConnectFallbackRequestsLatestLegacy(t *testing.T) {
	ft := newScriptedTransport()
	ft.initializeEcho = true
	c := connectClient(t, ft, Config{})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	assert.Equal(t, protocol.LatestLegacyVersion, ft.requestedInitVersion())
	assert.Equal(t, protocol.LatestLegacyVersion, c.NegotiatedVersion())
}

// TestConnectDiscoverNegotiatedLegacyRequestsThatRevision: when discover
// selects a pre-stateless mutual revision, the follow-up handshake requests
// exactly that revision rather than a hardcoded one.
func TestConnectDiscoverNegotiatedLegacyRequestsThatRevision(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverResult = discoverResultOffering(protocol.Version20250618)
	ft.initializeEcho = true
	c := connectClient(t, ft, Config{})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	assert.False(t, c.IsStateless())
	assert.Equal(t, protocol.Version20250618, ft.requestedInitVersion())
	assert.Equal(t, protocol.Version20250618, c.NegotiatedVersion())
}

// TestInitializedNotificationCarriesNegotiatedVersion: the notification that
// completes the legacy handshake is sent after negotiation, so it must carry
// the negotiated MCP-Protocol-Version header (required on every subsequent
// request by 2025-06-18+). The scripted transport records the extra headers
// of the last send, which for a legacy Connect is notifications/initialized.
func TestInitializedNotificationCarriesNegotiatedVersion(t *testing.T) {
	ft := newScriptedTransport()
	ft.initializeEcho = true
	c := connectClient(t, ft, Config{ProtocolVersion: protocol.Version20250618})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	_, _, headers := ft.snapshot()
	assert.Equal(t, protocol.Version20250618, headers["MCP-Protocol-Version"],
		"initialized notification must carry the negotiated protocol version header")
}

// TestConnectFallsBackOnLegacySessionRejection: strict legacy Streamable
// HTTP session servers (e.g. the official TypeScript SDK's 2025 server)
// reject any pre-initialize request with a bare 400 ("session required").
// Per the transport specification's backward-compatibility rule, a 400
// whose body is not a recognized modern JSON-RPC error identifies a legacy
// server and the client falls back to the handshake.
func TestConnectFallsBackOnLegacySessionRejection(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverErr = &transport.HTTPStatusError{
		Code: http.StatusBadRequest,
		Body: []byte("Bad Request: Mcp-Session-Id header is required"),
	}
	c := connectClient(t, ft, Config{})

	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	assert.False(t, c.IsStateless())
	_, sawInit, _ := ft.snapshot()
	assert.True(t, sawInit, "bare 400 without a modern error body must fall back to initialize")
}

// TestConnectDoesNotFallBackOnModernErrorBody: a 400 carrying a recognized
// modern JSON-RPC error (even id-less, which cannot be routed to the pending
// request) identifies a modern server; falling back to the legacy handshake
// over it would be wrong.
func TestConnectDoesNotFallBackOnModernErrorBody(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverErr = &transport.HTTPStatusError{
		Code: http.StatusBadRequest,
		Body: []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32022,"message":"Unsupported protocol version","data":{"supported":["2027-01-01"]}}}`),
	}
	c := connectClient(t, ft, Config{})

	require.Error(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	_, sawInit, _ := ft.snapshot()
	assert.False(t, sawInit, "a modern error body must not trigger legacy fallback")
}

// TestConnectFallsBackOnAnyNonModernRPCError (round-2 review blocker,
// PR #327): the stdio backward-compatibility rule is explicit that the
// fallback MUST NOT be keyed to one specific error code — legacy servers
// answer unknown pre-initialize requests with implementation-defined errors,
// commonly -32601 or -32602 ("initialize first"). Any JSON-RPC error other
// than the recognized modern codes must fall back to the handshake.
func TestConnectFallsBackOnAnyNonModernRPCError(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
	}{
		{"invalid params", protocol.InvalidParams, "initialize first"},
		{"method not found", protocol.MethodNotFound, "method not found"},
		{"implementation-defined server error", -32000, "unknown request before initialize"},
		{"internal error", protocol.InternalError, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newScriptedTransport()
			ft.discoverRPCErr = protocol.NewError(tc.code, tc.msg, nil)
			c := connectClient(t, ft, Config{})

			require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}),
				"code %d must fall back to initialize", tc.code)
			assert.False(t, c.IsStateless())
			_, sawInit, _ := ft.snapshot()
			assert.True(t, sawInit)
		})
	}
}

// TestConnectDoesNotFallBackOnModernRPCErrors: the recognized modern codes
// identify a modern server answering the probe; falling back to the legacy
// handshake over them would be wrong (the spec says to retry with an
// advertised version instead — with no mutual modern version, that is a
// hard error, never a silent downgrade).
func TestConnectDoesNotFallBackOnModernRPCErrors(t *testing.T) {
	for _, code := range []int{protocol.UnsupportedProtocolVersion, protocol.HeaderMismatch, protocol.MissingRequiredClientCapability} {
		ft := newScriptedTransport()
		ft.discoverRPCErr = protocol.NewError(code, "modern rejection", nil)
		c := connectClient(t, ft, Config{})

		require.Error(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}), "code %d", code)
		_, sawInit, _ := ft.snapshot()
		assert.False(t, sawInit, "code %d identifies a modern server; no fallback", code)
	}
}

// TestConnectFallsBackWhenSilentServerTimesOutOnStdio (round-2 review
// blocker, PR #327): a legacy stdio server may not answer the probe at all.
// The probe waits at most Config.RequestTimeout — previously defaulted but
// never applied — and classifies the silence as a legacy server, leaving
// the caller's context alive for the fallback handshake.
func TestConnectFallsBackWhenSilentServerTimesOutOnStdio(t *testing.T) {
	ft := newScriptedTransport() // carriesHeaders unset → stdio-like
	ft.discoverSilent = true
	c := connectClient(t, ft, Config{RequestTimeout: 150 * time.Millisecond})

	start := time.Now()
	require.NoError(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	assert.False(t, c.IsStateless())
	_, sawInit, _ := ft.snapshot()
	assert.True(t, sawInit, "silent probe must fall back to initialize")
	assert.Less(t, time.Since(start), 5*time.Second, "the probe wait must be bounded by RequestTimeout")
}

// TestConnectSilentProbeIsHardErrorOnStreamableHTTP: the timeout-as-legacy
// classification is the stdio rule; on Streamable HTTP the era signal is
// carried by status codes, and a hung POST must stay a hard error rather
// than masking an outage as a legacy server.
func TestConnectSilentProbeIsHardErrorOnStreamableHTTP(t *testing.T) {
	ft := newScriptedTransport()
	ft.carriesHeaders = true
	ft.discoverSilent = true
	c := connectClient(t, ft, Config{RequestTimeout: 150 * time.Millisecond})

	require.Error(t, c.Connect(context.Background(), protocol.Implementation{Name: "loom"}))
	_, sawInit, _ := ft.snapshot()
	assert.False(t, sawInit, "an HTTP probe timeout must not silently fall back")
}

// TestConnectExhaustedCallerContextDoesNotFallBack: when the caller's own
// context expires (not the probe's bounded window), there is no time left
// for a handshake and the cancellation is not a legacy signal.
func TestConnectExhaustedCallerContextDoesNotFallBack(t *testing.T) {
	ft := newScriptedTransport()
	ft.discoverSilent = true
	c := connectClient(t, ft, Config{RequestTimeout: 10 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, c.Connect(ctx, protocol.Implementation{Name: "loom"}))
	_, sawInit, _ := ft.snapshot()
	assert.False(t, sawInit, "caller-context expiry is not a legacy signal")
}
