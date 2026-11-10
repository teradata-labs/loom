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

package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/mcp/protocol"
	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// A size refusal must look the same to a caller whichever path carried it. The
// two paths are structurally different — the JSON read happens inside Send and
// can fail it, while an SSE refusal happens in the stream goroutine long after
// Send returned, so it can only arrive as a synthesized JSON-RPC error — but
// that is an implementation detail a caller should not have to know. One
// predicate has to hold for both, or a caller cannot tell "too large" from any
// other failure without matching on two unrelated error shapes.
func TestResponseTooLarge_SameShapeOnBothPaths(t *testing.T) {
	const limit = 1024

	// oversizedServer answers resources/read with a result far past the limit,
	// delivered either as plain JSON or as one SSE message.
	oversizedServer := func(t *testing.T, sse bool) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)

			// Notifications carry no id and take no response body.
			if len(req.ID) == 0 {
				w.WriteHeader(http.StatusAccepted)
				return
			}

			switch req.Method {
			case "initialize":
				// Well under the limit, so the handshake is unaffected.
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) +
					`,"result":{"protocolVersion":"2024-11-05",` +
					`"serverInfo":{"name":"stub","version":"1.0"},"capabilities":{}}}`))
			case "resources/read":
				body := `{"jsonrpc":"2.0","id":` + string(req.ID) +
					`,"result":{"contents":[{"uri":"file:///big","blob":"` +
					strings.Repeat("A", limit*4) + `"}]}}`
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: " + body + "\n\n"))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			default:
				// server/discover and anything else: fall back to initialize.
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) +
					`,"error":{"code":-32601,"message":"method not found"}}`))
			}
		}))
	}

	for _, tc := range []struct {
		name string
		sse  bool
	}{
		{"json", false},
		{"sse", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := oversizedServer(t, tc.sse)
			defer srv.Close()

			tr, err := transport.NewStreamableHTTPTransport(transport.StreamableHTTPConfig{
				Endpoint:         srv.URL,
				MaxResponseBytes: limit,
				Logger:           zap.NewNop(),
			})
			if err != nil {
				t.Fatalf("new transport: %v", err)
			}
			defer func() { _ = tr.Close() }()

			c := NewClient(Config{Transport: tr, Logger: zap.NewNop()})
			ctx := context.Background()
			if err := c.Connect(ctx, protocol.Implementation{Name: "test", Version: "0.1.0"}); err != nil {
				t.Fatalf("connect: %v", err)
			}

			_, err = c.ReadResource(ctx, "file:///big")
			if err == nil {
				t.Fatal("a result four times the limit must not be delivered")
			}

			// THE one predicate. It must hold on both paths.
			if !errors.Is(err, transport.ErrResponseTooLarge) {
				t.Fatalf("errors.Is(err, transport.ErrResponseTooLarge) must be true on the %s path; got %T: %v",
					tc.name, err, err)
			}

			// The adapted form must stay unwrappable to the JSON-RPC error it
			// wraps, which is what the CHANGELOG promises a caller who wants
			// the code. Only the SSE path has one to reach: the JSON path
			// fails inside Send and never produces a *protocol.Error.
			if tc.sse {
				var rpcErr *protocol.Error
				if !errors.As(err, &rpcErr) {
					t.Fatalf("an adapted refusal must still unwrap to its *protocol.Error; got %T: %v", err, err)
				}
				if rpcErr.Code != transport.CodeResponseTooLarge {
					t.Fatalf("want the wrapped code %d, got %d", transport.CodeResponseTooLarge, rpcErr.Code)
				}
			}
		})
	}
}

// The adaptation must be narrow: only the locally synthesized size refusal is
// re-shaped. An ordinary JSON-RPC error has to arrive exactly as the server
// sent it, or every unrelated failure would start reporting itself as a size
// refusal. A mutant that adapts every error passes the test above, so this is
// what pins the code check.
func TestOrdinaryJSONRPCErrorIsNotAdaptedToResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) +
				`,"result":{"protocolVersion":"2024-11-05",` +
				`"serverInfo":{"name":"stub","version":"1.0"},"capabilities":{}}}`))
			return
		}
		// Everything else, including resources/read: a plain method-not-found.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) +
			`,"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer srv.Close()

	tr, err := transport.NewStreamableHTTPTransport(transport.StreamableHTTPConfig{
		Endpoint: srv.URL,
		Logger:   zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	defer func() { _ = tr.Close() }()

	c := NewClient(Config{Transport: tr, Logger: zap.NewNop()})
	ctx := context.Background()
	if err := c.Connect(ctx, protocol.Implementation{Name: "test", Version: "0.1.0"}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	_, err = c.ReadResource(ctx, "file:///missing")
	if err == nil {
		t.Fatal("a method-not-found answer must surface as an error")
	}
	if errors.Is(err, transport.ErrResponseTooLarge) {
		t.Fatalf("a -32601 error must not be reported as a size refusal: %v", err)
	}

	// And it must still be reachable as the JSON-RPC error it is.
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("want a *protocol.Error, got %T: %v", err, err)
	}
	if rpcErr.Code != protocol.MethodNotFound {
		t.Fatalf("want code %d, got %d", protocol.MethodNotFound, rpcErr.Code)
	}
}
