// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/types"
)

// Options added for Anthropic-compatible gateways (Amazon Bedrock Mantle):
// NoBetaHeaders, CatalogProvider, WrapTransport, and an omitted x-api-key
// when the credential comes from the transport.

const gatewayMessagesOK = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5}}`

const gatewaySSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

type headerCapture struct {
	mu      sync.Mutex
	headers []http.Header
}

func (h *headerCapture) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.headers = append(h.headers, r.Header.Clone())
}

func (h *headerCapture) all() []http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]http.Header(nil), h.headers...)
}

func gatewayServer(t *testing.T, cap *headerCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		_, _ = io.ReadAll(r.Body)
		if r.Header.Get("Accept") == "text/event-stream" || r.URL.Query().Get("stream") == "1" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, gatewaySSE)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, gatewayMessagesOK)
	}))
}

func TestClient_NoBetaHeaders_Chat(t *testing.T) {
	cap := &headerCapture{}
	srv := gatewayServer(t, cap)
	defer srv.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: srv.URL, NoBetaHeaders: true})
	_, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)

	h := cap.all()
	require.Len(t, h, 1)
	_, hasBeta := h[0]["Anthropic-Beta"]
	assert.False(t, hasBeta, "NoBetaHeaders must drop anthropic-beta entirely")
	assert.Equal(t, "test-key", h[0].Get("x-api-key"))
	assert.Equal(t, "2023-06-01", h[0].Get("anthropic-version"))
}

func TestClient_NoBetaHeaders_ChatStream(t *testing.T) {
	cap := &headerCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, gatewaySSE)
	}))
	defer srv.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: srv.URL, NoBetaHeaders: true})
	resp, err := client.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, func(string) {})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)

	h := cap.all()
	require.Len(t, h, 1)
	_, hasBeta := h[0]["Anthropic-Beta"]
	assert.False(t, hasBeta)
}

func TestClient_DefaultKeepsBetaHeader(t *testing.T) {
	cap := &headerCapture{}
	srv := gatewayServer(t, cap)
	defer srv.Close()

	client := NewClient(Config{APIKey: "test-key", Endpoint: srv.URL})
	_, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "prompt-caching-2024-07-31", cap.all()[0].Get("anthropic-beta"), "api.anthropic.com behavior is unchanged")
}

func TestClient_EmptyAPIKeyOmitsHeader(t *testing.T) {
	cap := &headerCapture{}
	srv := gatewayServer(t, cap)
	defer srv.Close()

	client := NewClient(Config{APIKey: "", Endpoint: srv.URL})
	_, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	_, hasKey := cap.all()[0]["X-Api-Key"]
	assert.False(t, hasKey, "an empty x-api-key must not be sent; a signing transport supplies the credential")
}

type markerTransport struct {
	base  http.RoundTripper
	calls int
	mu    sync.Mutex
}

func (m *markerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Wrapped", "yes")
	return m.base.RoundTrip(r2)
}

func TestClient_WrapTransport(t *testing.T) {
	cap := &headerCapture{}
	srv := gatewayServer(t, cap)
	defer srv.Close()

	var mt *markerTransport
	client := NewClient(Config{APIKey: "k", Endpoint: srv.URL, WrapTransport: func(base http.RoundTripper) http.RoundTripper {
		mt = &markerTransport{base: base}
		return mt
	}})
	require.NotNil(t, mt, "WrapTransport must be invoked at construction")
	assert.Same(t, http.DefaultTransport, mt.base, "the wrapper receives the default transport")

	_, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "yes", cap.all()[0].Get("X-Wrapped"))
	mt.mu.Lock()
	assert.Equal(t, 1, mt.calls)
	mt.mu.Unlock()
}

func TestCalculateCost_GatewayCatalogProvider(t *testing.T) {
	// Mantle ids live in the "bedrock-mantle" catalog namespace. Pricing must
	// resolve there instead of through the substring fallback, which would
	// return the first-party (global) card. A bare Mantle id is a regional
	// endpoint, so the namespace carries the 10 percent regional premium.
	client := NewClient(Config{APIKey: "k", Model: "anthropic.claude-haiku-4-5", CatalogProvider: "bedrock-mantle"})
	cost := client.calculateCost(1_000_000, 1_000_000, 0, 0)
	assert.InDelta(t, 1.10+5.50, cost, 1e-9, "Haiku 4.5 on Mantle: $1.10/M in, $5.50/M out (regional rate)")

	defaultNS := NewClient(Config{APIKey: "k", Model: "claude-haiku-4-5-20251001"})
	assert.Equal(t, DefaultCatalogProvider, defaultNS.catalogProvider)
}
