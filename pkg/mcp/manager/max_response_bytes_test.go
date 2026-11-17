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

package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/mcp/transport"
)

// oversizedJSONServer answers every POST with a valid JSON-RPC response whose
// result is `size` bytes of payload.
func oversizedJSONServer(t *testing.T, size int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"blob": strings.Repeat("x", size)},
		})
		_, _ = w.Write(payload)
	}))
}

// The point of the setting is that it changes what the TRANSPORT does. A field
// that exists but never reaches StreamableHTTPConfig is the defect this test
// exists to catch, so it asserts on behaviour and not on the struct.
func TestServerConfig_MaxResponseBytesReachesTheTransport(t *testing.T) {
	srv := oversizedJSONServer(t, 64<<10) // 64 KiB of result
	defer srv.Close()

	newManager := func(t *testing.T) *Manager {
		t.Helper()
		m, err := NewManager(Config{}, zap.NewNop())
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		return m
	}

	cfg := func(maxResponseBytes int64) ServerConfig {
		return ServerConfig{
			Enabled:          true,
			Transport:        "streamable-http",
			URL:              srv.URL,
			MaxResponseBytes: maxResponseBytes,
			// Bound startServer so a handshake that cannot complete against
			// this stub server fails fast instead of waiting out the 15s
			// default.
			Timeout: "2s",
		}
	}

	t.Run("a bound below the response refuses it", func(t *testing.T) {
		m := newManager(t)
		defer func() { _ = m.Stop() }()

		err := m.startServer(context.Background(), "stub", cfg(1024))
		if err == nil {
			t.Fatal("a 64 KiB response under a 1 KiB bound must fail")
		}
		if !errors.Is(err, transport.ErrResponseTooLarge) {
			t.Fatalf("the configured bound did not reach the transport: want ErrResponseTooLarge, got %v", err)
		}
	})

	t.Run("a bound above the response does not refuse it", func(t *testing.T) {
		m := newManager(t)
		defer func() { _ = m.Stop() }()

		// The stub is not a real MCP server, so the handshake still fails —
		// but it must NOT fail on size. That is what proves the configured
		// value, and not the 16 MiB default, is the one in force.
		err := m.startServer(context.Background(), "stub", cfg(1<<20))
		if errors.Is(err, transport.ErrResponseTooLarge) {
			t.Fatalf("a 1 MiB bound must not refuse a 64 KiB response: %v", err)
		}
	})

	t.Run("zero selects the default, which admits this response", func(t *testing.T) {
		m := newManager(t)
		defer func() { _ = m.Stop() }()

		err := m.startServer(context.Background(), "stub", cfg(0))
		if errors.Is(err, transport.ErrResponseTooLarge) {
			t.Fatalf("zero must select the %d byte default, which admits 64 KiB: %v",
				transport.DefaultMaxResponseBytes, err)
		}
	})
}
