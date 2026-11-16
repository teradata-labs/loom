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

package openai

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

// Options added for the Vercel AI Gateway: ExtraBody (request-body members),
// ResponseInspector (gateway cost and routing facts), ReportUnpriced, and
// mid-stream error payloads.

const plainChatResponse = `{"id":"gen_01","object":"chat.completion","created":1,"model":"vendor/model-x","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100}}`

type capturedBodies struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *capturedBodies) add(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, b)
}

func (c *capturedBodies) last(t *testing.T) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.bodies)
	var m map[string]any
	require.NoError(t, json.Unmarshal(c.bodies[len(c.bodies)-1], &m))
	return m
}

func newBodyServer(t *testing.T, cap *capturedBodies, respond func(w http.ResponseWriter, stream bool)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.add(b)
		respond(w, bytes.Contains(b, []byte(`"stream":true`)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

var gatewayExtra = llm.ExtraBody{"providerOptions": map[string]any{
	"gateway": map[string]any{"order": []string{"bedrock", "anthropic"}, "models": []string{"openai/gpt-5"}},
}}

func TestExtraBody_AddedToBlockingAndStreamingBodies(t *testing.T) {
	var cap capturedBodies
	srv := newBodyServer(t, &cap, func(w http.ResponseWriter, stream bool) {
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, gatewayStreamNoDone)
			return
		}
		_, _ = io.WriteString(w, plainChatResponse)
	})
	c := NewClient(Config{APIKey: "k", Model: "vendor/model-x", Endpoint: srv.URL, ExtraBody: gatewayExtra})
	msgs := []types.Message{{Role: "user", Content: "hi"}}

	_, err := c.Chat(context.Background(), msgs, nil)
	require.NoError(t, err)
	body := cap.last(t)
	assert.Equal(t, "vendor/model-x", body["model"], "client fields are kept")
	want := map[string]any{"gateway": map[string]any{
		"order": []any{"bedrock", "anthropic"}, "models": []any{"openai/gpt-5"}}}
	assert.Equal(t, want, body["providerOptions"])

	_, err = c.ChatStream(context.Background(), msgs, nil, nil)
	require.NoError(t, err)
	body = cap.last(t)
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, want, body["providerOptions"])
}

func TestExtraBody_AbsentLeavesBodyByteIdentical(t *testing.T) {
	var cap capturedBodies
	srv := newBodyServer(t, &cap, func(w http.ResponseWriter, _ bool) { _, _ = io.WriteString(w, plainChatResponse) })
	msgs := []types.Message{{Role: "user", Content: "hi"}}

	_, err := NewClient(Config{APIKey: "k", Model: "m", Endpoint: srv.URL}).Chat(context.Background(), msgs, nil)
	require.NoError(t, err)
	_, err = NewClient(Config{APIKey: "k", Model: "m", Endpoint: srv.URL, ExtraBody: llm.ExtraBody{}}).Chat(context.Background(), msgs, nil)
	require.NoError(t, err)
	cap.mu.Lock()
	defer cap.mu.Unlock()
	require.Len(t, cap.bodies, 2)
	assert.Equal(t, string(cap.bodies[0]), string(cap.bodies[1]))
}

func TestExtraBody_CollisionFailsEveryCall(t *testing.T) {
	bad := llm.ExtraBody{"model": "other", "tools": []any{}}
	require.Error(t, ValidateExtraBody(bad))
	require.NoError(t, ValidateExtraBody(gatewayExtra))

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true }))
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "m", Endpoint: srv.URL, ExtraBody: bad})
	msgs := []types.Message{{Role: "user", Content: "hi"}}
	_, err := c.Chat(context.Background(), msgs, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model, tools")
	_, err = c.ChatStream(context.Background(), msgs, nil, nil)
	require.Error(t, err)
	assert.False(t, called, "no request is sent with a bad ExtraBody")
}

// costInspector reports a fixed cost and routing attribute when the payload
// mentions the marker, the way the gateway inspector reads providerMetadata.
func costInspector(marker string, cost float64) llm.ResponseInspector {
	return func(_ http.Header, payload []byte) llm.ResponseFacts {
		if !bytes.Contains(payload, []byte(marker)) {
			return llm.ResponseFacts{}
		}
		return llm.ResponseFacts{CostUSD: cost, Attributes: map[string]string{"gateway.final_provider": "bedrock"}}
	}
}

func TestResponseInspector_BlockingCostWinsOverHeaderAndEstimate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(providerCostHeader, "9.99")
		_, _ = io.WriteString(w, plainChatResponse)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "vendor/model-x", Endpoint: srv.URL,
		ResponseInspector: costInspector(`"gen_01"`, 0.0123)})
	resp, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.InDelta(t, 0.0123, resp.Usage.CostUSD, 1e-12)
	assert.Equal(t, "bedrock", resp.Metadata["gateway.final_provider"])
}

func TestResponseInspector_HeaderUsedWhenInspectorSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(providerCostHeader, "0.5")
		_, _ = io.WriteString(w, plainChatResponse)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "vendor/model-x", Endpoint: srv.URL,
		ResponseInspector: costInspector("never-present", 1)})
	resp, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, resp.Usage.CostUSD, 1e-12)
	_, has := resp.Metadata["gateway.final_provider"]
	assert.False(t, has)
}

func TestResponseInspector_StreamCostFromFinalChunk(t *testing.T) {
	stream := gatewayStreamNoDone +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"providerMetadata":{"gateway":{"cost":"0.0042"}}}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "vendor/model-x", Endpoint: srv.URL,
		ResponseInspector: costInspector("providerMetadata", 0.0042)})
	resp, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "OK", resp.Content)
	assert.InDelta(t, 0.0042, resp.Usage.CostUSD, 1e-12)
	assert.Equal(t, "bedrock", resp.Metadata["gateway.final_provider"])
}

func TestStream_MidStreamErrorPayloadIsAnError(t *testing.T) {
	stream := `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"par"},"finish_reason":null}]}` + "\n\n" +
		`data: {"error":{"message":"upstream overloaded","type":"overloaded_error"}}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "m", Endpoint: srv.URL})
	_, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream overloaded")
	assert.Equal(t, llm.ProviderFailureUnavailable, llm.ClassifyProviderFailure(err))
}

func TestReportUnpriced(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		report       bool
		header       string
		wantUnpriced bool
		wantZero     bool
	}{
		{name: "unknown model, report on", model: "vendor/unknown-model", report: true, wantUnpriced: true, wantZero: true},
		{name: "unknown model, report off uses gpt-4o guess", model: "vendor/unknown-model", report: false},
		{name: "claude family is priced", model: "anthropic/claude-sonnet-4.5", report: true},
		{name: "gateway cost reported", model: "vendor/unknown-model", report: true, header: "0.25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := strings.Replace(plainChatResponse, "vendor/model-x", tt.model, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.header != "" {
					w.Header().Set(providerCostHeader, tt.header)
				}
				_, _ = io.WriteString(w, resp)
			}))
			defer srv.Close()
			c := NewClient(Config{APIKey: "k", Model: tt.model, Endpoint: srv.URL,
				CatalogProvider: "test-gateway-ns", ReportUnpriced: tt.report})
			out, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
			require.NoError(t, err)
			_, flagged := out.Metadata[llm.MetadataCostUnpriced]
			assert.Equal(t, tt.wantUnpriced, flagged)
			if tt.wantZero {
				assert.Zero(t, out.Usage.CostUSD)
			} else {
				assert.Positive(t, out.Usage.CostUSD)
			}
		})
	}
}

// A chunk whose "error" member is not an object (a string, null) is not a
// failure and its content is kept.
func TestStream_NonObjectErrorMemberIsNotAFailure(t *testing.T) {
	stream := `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","error":null,"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","error":"","choices":[{"index":0,"delta":{"content":"b"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","error":{},"choices":[{"index":0,"delta":{"content":"c"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "k", Model: "m", Endpoint: srv.URL})
	resp, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "abc", resp.Content)
}
