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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestHeaderFields(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		wantMethod string
		wantName   string
	}{
		{
			name:       "tools/call carries params.name",
			message:    `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_weather","arguments":{}}}`,
			wantMethod: "tools/call",
			wantName:   "get_weather",
		},
		{
			name:       "prompts/get carries params.name",
			message:    `{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"greeting"}}`,
			wantMethod: "prompts/get",
			wantName:   "greeting",
		},
		{
			name:       "resources/read carries params.uri",
			message:    `{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"file:///a.json"}}`,
			wantMethod: "resources/read",
			wantName:   "file:///a.json",
		},
		{
			name:       "other methods omit Mcp-Name",
			message:    `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`,
			wantMethod: "tools/list",
			wantName:   "",
		},
		{
			name:       "non-ASCII name is sentinel-encoded",
			message:    `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"天気"}}`,
			wantMethod: "tools/call",
			wantName:   "=?base64?5aSp5rCX?=",
		},
		{
			name:       "malformed body yields nothing",
			message:    `{not json`,
			wantMethod: "",
			wantName:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, name := requestHeaderFields([]byte(tt.message))
			assert.Equal(t, tt.wantMethod, method)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestWithExtraHeadersMerge(t *testing.T) {
	ctx := WithExtraHeaders(context.Background(), map[string]string{"A": "1", "B": "2"})
	ctx = WithExtraHeaders(ctx, map[string]string{"B": "3", "C": "4"})
	assert.Equal(t, map[string]string{"A": "1", "B": "3", "C": "4"}, ExtraHeadersFromContext(ctx))
	assert.Nil(t, ExtraHeadersFromContext(context.Background()))
}

func TestIsJSONRPCErrorResponse(t *testing.T) {
	assert.True(t, isJSONRPCErrorResponse([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`)))
	assert.False(t, isJSONRPCErrorResponse([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)))
	assert.False(t, isJSONRPCErrorResponse([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700}}`)))
	assert.False(t, isJSONRPCErrorResponse([]byte(`not json`)))
	assert.False(t, isJSONRPCErrorResponse(nil))
}

// newTestTransport builds a transport against an httptest server.
func newTestTransport(t *testing.T, handler http.HandlerFunc) (*StreamableHTTPTransport, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	trans, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: srv.URL})
	require.NoError(t, err)
	t.Cleanup(func() { _ = trans.Close() })
	return trans, srv
}

func TestSendSetsRequestMetadataHeaders(t *testing.T) {
	var got http.Header
	trans, _ := newTestTransport(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})

	ctx := WithExtraHeaders(context.Background(), map[string]string{
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Param-Region":     "us-west1",
	})
	err := trans.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_sql","arguments":{"region":"us-west1"}}}`))
	require.NoError(t, err)

	assert.Equal(t, "tools/call", got.Get("Mcp-Method"))
	assert.Equal(t, "execute_sql", got.Get("Mcp-Name"))
	assert.Equal(t, "2026-07-28", got.Get("MCP-Protocol-Version"))
	assert.Equal(t, "us-west1", got.Get("Mcp-Param-Region"))
}

func TestHTTPErrorWithJSONRPCBodyIsDeliveredAsMessage(t *testing.T) {
	// A 2026-07-28 server answers an unknown method with HTTP 404 carrying a
	// JSON-RPC -32601 error; the transport must deliver it as a protocol
	// message so the pending request receives the typed error.
	body := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`
	trans, _ := newTestTransport(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	})

	err := trans.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msg, err := trans.Receive(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(msg))
}

func TestBareHTTPErrorYieldsTypedStatusError(t *testing.T) {
	trans, _ := newTestTransport(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such endpoint", http.StatusNotFound)
	})

	err := trans.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`))
	require.Error(t, err)

	var statusErr *HTTPStatusError
	require.True(t, errors.As(err, &statusErr), "want *HTTPStatusError, got %T: %v", err, err)
	assert.Equal(t, http.StatusNotFound, statusErr.Code)
}

func TestLegacySessionExpiryStillSignalled(t *testing.T) {
	trans, _ := newTestTransport(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	require.NoError(t, trans.SetSessionID("legacy-session"))

	err := trans.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{}}`))
	require.ErrorIs(t, err, ErrSessionExpired)
	assert.Empty(t, trans.GetSessionID(), "session must be cleared after expiry")
}
