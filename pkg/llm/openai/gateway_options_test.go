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

package openai

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

// Options and wire shapes added for OpenAI-compatible gateways (Amazon
// Bedrock Mantle): WrapTransport on both transports, the "reasoning" field
// gpt-oss uses for its trace, and streams that end without "data: [DONE]".

const gatewayChatReasoning = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"message":{"role":"assistant","content":"OK","reasoning":"the trace"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`

const gatewayStreamNoDone = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{"reasoning":"the "},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{"reasoning":"trace"},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
	`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n"

type gatewayMarker struct {
	mu    sync.Mutex
	calls int
	base  http.RoundTripper
}

func (m *gatewayMarker) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Wrapped", "yes")
	return m.base.RoundTrip(r2)
}

func TestClient_WrapTransport_BlockingAndStreaming(t *testing.T) {
	var mu sync.Mutex
	var wrapped []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		wrapped = append(wrapped, r.Header.Get("X-Wrapped"))
		mu.Unlock()
		if string(body) != "" && containsStreamTrue(body) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, gatewayStreamNoDone)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, gatewayChatReasoning)
	}))
	defer srv.Close()

	var markers []*gatewayMarker
	client := NewClient(Config{
		APIKey:   "k",
		Model:    "openai.gpt-oss-120b",
		Endpoint: srv.URL + "/v1/chat/completions",
		WrapTransport: func(base http.RoundTripper) http.RoundTripper {
			m := &gatewayMarker{base: base}
			markers = append(markers, m)
			return m
		},
	})
	require.Len(t, markers, 2, "both the blocking and the streaming transport are wrapped")

	_, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	_, err = client.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, func(string) {})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"yes", "yes"}, wrapped)
}

func containsStreamTrue(body []byte) bool {
	return len(body) > 0 && (indexOf(body, `"stream":true`) >= 0)
}

func indexOf(hay []byte, needle string) int {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(hay); i++ {
		if string(hay[i:i+len(n)]) == needle {
			return i
		}
	}
	return -1
}

func TestClient_ReasoningAlias_NonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, gatewayChatReasoning)
	}))
	defer srv.Close()

	client := NewClient(Config{APIKey: "k", Model: "openai.gpt-oss-120b", Endpoint: srv.URL + "/v1/chat/completions"})
	resp, err := client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "OK", resp.Content)
	assert.Equal(t, "the trace", resp.Thinking, `"reasoning" is read when "reasoning_content" is absent`)
}

func TestClient_ReasoningAlias_StreamWithoutDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, gatewayStreamNoDone)
	}))
	defer srv.Close()

	client := NewClient(Config{APIKey: "k", Model: "openai.gpt-oss-120b", Endpoint: srv.URL + "/v1/chat/completions"})
	var tokens string
	resp, err := client.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, func(tok string) { tokens += tok })
	require.NoError(t, err)
	assert.Equal(t, "OK", tokens, "reasoning deltas are never forwarded to the token callback")
	assert.Equal(t, "OK", resp.Content)
	assert.Equal(t, "the trace", resp.Thinking)
	assert.Equal(t, 10, resp.Usage.InputTokens, "the trailing usage chunk is read even without [DONE]")
	assert.Equal(t, 5, resp.Usage.OutputTokens)
}

func TestChatMessage_ReasoningOmittedFromRequests(t *testing.T) {
	// Reasoning is read-only: an assistant message replayed to the model must
	// not carry it.
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, gatewayChatReasoning)
	}))
	defer srv.Close()

	client := NewClient(Config{APIKey: "k", Model: "openai.gpt-oss-120b", Endpoint: srv.URL + "/v1/chat/completions"})
	_, err := client.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "earlier answer"},
		{Role: "user", Content: "again"},
	}, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(sent), `"reasoning"`)
}
