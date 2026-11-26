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

func TestResponseIDMatches(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		request string
		want    bool
	}{
		{"numeric ids equal", `{"jsonrpc":"2.0","id":1,"error":{"code":-32601}}`, `{"jsonrpc":"2.0","id":1,"method":"x"}`, true},
		{"string ids equal", `{"jsonrpc":"2.0","id":"a","error":{}}`, `{"jsonrpc":"2.0","id":"a","method":"x"}`, true},
		{"numeric vs string are distinct", `{"jsonrpc":"2.0","id":"1","error":{}}`, `{"jsonrpc":"2.0","id":1,"method":"x"}`, false},
		{"synthetic server-error id", `{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600}}`, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`, false},
		{"response without id", `{"jsonrpc":"2.0","error":{"code":-32600}}`, `{"jsonrpc":"2.0","id":1,"method":"x"}`, false},
		{"request without id (notification)", `{"jsonrpc":"2.0","id":1,"error":{}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, false},
		{"request with null id", `{"jsonrpc":"2.0","id":null,"error":{}}`, `{"jsonrpc":"2.0","id":null,"method":"x"}`, false},
		{"unparseable body", `not json`, `{"jsonrpc":"2.0","id":1,"method":"x"}`, false},
		{"unparseable request", `{"jsonrpc":"2.0","id":1,"error":{}}`, `nope`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, responseIDMatches([]byte(tt.body), []byte(tt.request)))
		})
	}
}

// A server that refuses the request at the HTTP layer may answer with a
// well-formed JSON-RPC error carrying a synthetic id — teradata-mcp-server
// rejects an unsupported MCP-Protocol-Version with HTTP 400 and
// id "server-error". Routing that body as a protocol message leaves the real
// request waiting until its deadline (the client's router drops responses for
// unknown ids), so it must surface as an HTTPStatusError instead, where
// client.Connect classifies the 400 as a legacy signal and falls back to the
// initialize handshake.
func TestStreamableHTTPTransport_MismatchedErrorIDIsAnHTTPError(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Bad Request: Unsupported protocol version: 2026-07-28"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: srv.URL})
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	err = tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`))
	var httpErr *HTTPStatusError
	require.True(t, errors.As(err, &httpErr), "mismatched-id error body must surface as HTTPStatusError, got %v", err)
	assert.Equal(t, http.StatusBadRequest, httpErr.Code)
	assert.JSONEq(t, body, string(httpErr.Body), "the body must travel with the error so the client can classify it")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = tr.Receive(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "nothing may be routed as a message for a response that answers no pending request")
}

// The routable case is unchanged: a 4xx whose JSON-RPC error carries the
// request's own id is still delivered as a protocol message so the pending
// request receives the typed error.
func TestStreamableHTTPTransport_MatchingErrorIDIsRoutedAsMessage(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"Method not found"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	tr, err := NewStreamableHTTPTransport(StreamableHTTPConfig{Endpoint: srv.URL})
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	require.NoError(t, tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":7,"method":"server/discover","params":{}}`)))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := tr.Receive(ctx)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(got))
}
