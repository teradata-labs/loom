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

package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/types"
)

// Options added for the Vercel AI Gateway: ExtraBody, ResponseInspector, and
// the "error" stream event.

var gatewayExtra = llm.ExtraBody{"providerOptions": map[string]any{
	"gateway": map[string]any{"only": []string{"bedrock"}},
}}

type bodyCapture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (b *bodyCapture) server(t *testing.T, respond func(w http.ResponseWriter, stream bool)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.bodies = append(b.bodies, raw)
		b.mu.Unlock()
		respond(w, bytes.Contains(raw, []byte(`"stream":true`)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (b *bodyCapture) last(t *testing.T) map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	require.NotEmpty(t, b.bodies)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b.bodies[len(b.bodies)-1], &m))
	return m
}

func respondOK(w http.ResponseWriter, stream bool) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, gatewaySSE)
		return
	}
	_, _ = io.WriteString(w, gatewayMessagesOK)
}

func TestExtraBody_ChatAndStream(t *testing.T) {
	var cap bodyCapture
	srv := cap.server(t, respondOK)
	c := NewClient(Config{APIKey: "k", Model: "anthropic/claude-haiku-4.5", Endpoint: srv.URL, ExtraBody: gatewayExtra})
	msgs := []types.Message{{Role: "user", Content: "hi"}}
	want := map[string]any{"gateway": map[string]any{"only": []any{"bedrock"}}}

	_, err := c.Chat(context.Background(), msgs, nil)
	require.NoError(t, err)
	body := cap.last(t)
	assert.Equal(t, "anthropic/claude-haiku-4.5", body["model"])
	assert.Equal(t, want, body["providerOptions"])

	_, err = c.ChatStream(context.Background(), msgs, nil, nil)
	require.NoError(t, err)
	body = cap.last(t)
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, want, body["providerOptions"])
}

func TestExtraBody_CollisionRejected(t *testing.T) {
	bad := llm.ExtraBody{"max_tokens": 1}
	require.Error(t, ValidateExtraBody(bad))
	require.NoError(t, ValidateExtraBody(gatewayExtra))

	var cap bodyCapture
	srv := cap.server(t, respondOK)
	c := NewClient(Config{APIKey: "k", Model: "m", Endpoint: srv.URL, ExtraBody: bad})
	msgs := []types.Message{{Role: "user", Content: "hi"}}
	_, err := c.Chat(context.Background(), msgs, nil)
	require.ErrorContains(t, err, "max_tokens")
	_, err = c.ChatStream(context.Background(), msgs, nil, nil)
	require.ErrorContains(t, err, "max_tokens")
	cap.mu.Lock()
	defer cap.mu.Unlock()
	assert.Empty(t, cap.bodies, "no request is sent with a bad ExtraBody")
}

func TestResponseInspector_CostAndAttributes(t *testing.T) {
	inspector := func(_ http.Header, payload []byte) llm.ResponseFacts {
		if !bytes.Contains(payload, []byte(`"msg_1"`)) && !bytes.Contains(payload, []byte(`message_stop`)) {
			return llm.ResponseFacts{}
		}
		return llm.ResponseFacts{CostUSD: 0.031, Attributes: map[string]string{"gateway.final_provider": "vertex"}}
	}
	var cap bodyCapture
	srv := cap.server(t, respondOK)
	c := NewClient(Config{APIKey: "k", Model: "anthropic/claude-haiku-4.5", Endpoint: srv.URL, ResponseInspector: inspector})
	msgs := []types.Message{{Role: "user", Content: "hi"}}

	resp, err := c.Chat(context.Background(), msgs, nil)
	require.NoError(t, err)
	assert.InDelta(t, 0.031, resp.Usage.CostUSD, 1e-12)
	assert.Equal(t, "vertex", resp.Metadata["gateway.final_provider"])

	resp, err = c.ChatStream(context.Background(), msgs, nil, nil)
	require.NoError(t, err)
	assert.InDelta(t, 0.031, resp.Usage.CostUSD, 1e-12)
	assert.Equal(t, "vertex", resp.Metadata["gateway.final_provider"])
	assert.Equal(t, true, resp.Metadata["streaming"])
}

func TestResponseInspector_SilentKeepsEstimate(t *testing.T) {
	var cap bodyCapture
	srv := cap.server(t, respondOK)
	c := NewClient(Config{APIKey: "k", Model: "anthropic/claude-haiku-4.5", Endpoint: srv.URL,
		ResponseInspector: func(http.Header, []byte) llm.ResponseFacts { return llm.ResponseFacts{} }})
	resp, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	// Haiku 4.5 at $1/$5 per 1M: 10 in + 5 out.
	assert.InDelta(t, (10*1.0+5*5.0)/1e6, resp.Usage.CostUSD, 1e-12)
}

// Anthropic (and a gateway in front of it) reports a failure after the stream
// has started as an "error" event. It used to be ignored, so an overloaded
// mid-stream turn returned a truncated answer as a success.
func TestStream_ErrorEventIsAnError(t *testing.T) {
	stream := strings.Split(gatewaySSE, "event: content_block_stop\n")[0] +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "claude-haiku-4-5", Endpoint: srv.URL})
	_, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Overloaded")
	assert.Equal(t, llm.ProviderFailureUnavailable, llm.ClassifyProviderFailure(err))
}
