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
	discoverResult    *protocol.DiscoverResult // nil → JSON-RPC MethodNotFound
	initializeVersion string                   // "" → 2024-11-05
	tools             []protocol.Tool
	callResult        json.RawMessage   // result payload for tools/call
	callResults       []json.RawMessage // per-attempt results; overrides callResult when set
	callStreamLost    int               // answer this many leading tools/call attempts with CodeStreamLost

	// Recording
	sawDiscover      bool
	sawInitialize    bool
	lastExtraHeaders map[string]string
	callParams       []json.RawMessage // raw params of every tools/call attempt
	listenSupported  bool              // answer subscriptions/listen by holding the stream open
	listenParams     []json.RawMessage // raw params of every subscriptions/listen request
	listenIDs        []json.RawMessage // raw JSON-RPC ids of listen requests
	toolsListCalls   int

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
		return nil // notifications/initialized
	}

	var resp protocol.Response
	resp.JSONRPC = protocol.JSONRPCVersion
	resp.ID = req.ID

	switch req.Method {
	case "server/discover":
		f.mu.Lock()
		f.sawDiscover = true
		derr, dres := f.discoverErr, f.discoverResult
		f.mu.Unlock()
		if derr != nil {
			return derr
		}
		if dres == nil {
			resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
			break
		}
		resp.Result, _ = json.Marshal(dres)
	case "initialize":
		f.mu.Lock()
		f.sawInitialize = true
		version := f.initializeVersion
		f.mu.Unlock()
		if version == "" {
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
		resp.Error = protocol.NewError(protocol.MethodNotFound, "method not found", nil)
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

func (f *scriptedTransport) snapshot() (sawDiscover, sawInitialize bool, headers map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sawDiscover, f.sawInitialize, f.lastExtraHeaders
}

func statelessDiscoverResult() *protocol.DiscoverResult {
	return &protocol.DiscoverResult{
		ProtocolVersions: []string{protocol.Version20260728, protocol.Version20241105},
		ServerInfo:       protocol.Implementation{Name: "scripted", Version: "1.0"},
	}
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
	ft.discoverResult = &protocol.DiscoverResult{
		ProtocolVersions: []string{protocol.Version20251125},
		ServerInfo:       protocol.Implementation{Name: "scripted", Version: "1.0"},
	}
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

func TestCallToolMirrorsHeaderParams(t *testing.T) {
	ft := newScriptedTransport()
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
