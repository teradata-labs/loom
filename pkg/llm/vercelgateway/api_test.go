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

package vercelgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// responsesSSE is a trimmed Responses API stream in the shape captured live on
// 2026-11-19: text deltas, then response.completed carrying usage and the
// gateway's cost and routing under response.provider_metadata.gateway, and a
// dated model echo.
const responsesSSE = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"gen_r1","object":"response","status":"in_progress","model":"gpt-5.5-2026-04-23","output":[]}}` + "\n\n" +
	"event: response.output_item.added\n" +
	`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hi "}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"there"}` + "\n\n" +
	"event: response.output_item.done\n" +
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi there","annotations":[]}]}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"gen_r1","object":"response","status":"completed","model":"gpt-5.5-2026-04-23",` +
	`"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi there","annotations":[]}]}],` +
	`"usage":{"input_tokens":40,"output_tokens":4,"total_tokens":44,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}},` +
	`"provider_metadata":{"gateway":{"routing":{"finalProvider":"openai","modelAttemptCount":1,"totalProviderAttemptCount":1},"cost":"0.0031","generationId":"gen_r1"}}}}` + "\n\n"

func responsesHandler(w http.ResponseWriter, r *http.Request, raw []byte) {
	if r.URL.Path == ResponsesPath {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesSSE)
		return
	}
	okHandler(w, r, raw)
}

func TestClient_APISelectsSurface(t *testing.T) {
	tests := []struct {
		api, model, surface, path string
	}{
		{"", "anthropic/claude-sonnet-4.5", SurfaceMessages, MessagesPath},
		{APIAuto, "openai/gpt-5", SurfaceChat, ChatCompletionsPath},
		{APIChat, "anthropic/claude-sonnet-4.5", SurfaceChat, ChatCompletionsPath},
		{APIMessages, "openai/gpt-5", SurfaceMessages, MessagesPath},
		{APIResponses, "openai/gpt-5", SurfaceResponses, ResponsesPath},
		{APIResponses, "anthropic/claude-sonnet-4.5", SurfaceResponses, ResponsesPath},
	}
	for _, tt := range tests {
		t.Run(tt.api+"/"+tt.model, func(t *testing.T) {
			g := &gateway{handler: responsesHandler}
			srv := g.start(t)
			c := newTestClient(t, srv.URL, tt.model, func(cfg *Config) { cfg.API = tt.api })
			assert.Equal(t, tt.surface, c.Surface())
			assert.Equal(t, srv.URL+tt.path, c.Endpoint())
			resp, err := c.Chat(context.Background(), hi, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.path, g.last(t).Path)
			assert.Equal(t, tt.model, g.last(t).Body["model"])
			assert.Equal(t, tt.surface, resp.Metadata[AttrEndpointSurface])
		})
	}
}

func TestNewClient_RejectsUnknownAPI(t *testing.T) {
	_, err := NewClient(Config{APIKey: fakeKey, Model: "openai/gpt-5", API: "completions"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway_api")
}

func TestClient_ResponsesSurfaceCostAndMetadata(t *testing.T) {
	g := &gateway{handler: responsesHandler}
	c := newTestClient(t, g.start(t).URL, "openai/gpt-5.5", func(cfg *Config) {
		cfg.API = APIResponses
		cfg.Routing = &Routing{Only: []string{"openai"}}
	})
	var streamed string
	resp, err := c.ChatStream(context.Background(), hi, []shuttle.Tool{&shuttle.MockTool{MockName: "get_weather"}},
		func(tok string) { streamed += tok })
	require.NoError(t, err)
	assert.Equal(t, "hi there", resp.Content)
	assert.Equal(t, "hi there", streamed)
	assert.InDelta(t, 0.0031, resp.Usage.CostUSD, 1e-12, "gateway cost from response.completed")
	assert.Equal(t, "openai", resp.Metadata[AttrFinalProvider])
	assert.Equal(t, "gen_r1", resp.Metadata[AttrGenerationID])
	assert.Equal(t, "false", resp.Metadata[AttrFallbackUsed], "a dated snapshot of the requested id is not a fallback")

	body := g.last(t).Body
	assert.Equal(t, false, body["store"])
	assert.Equal(t, map[string]any{"gateway": map[string]any{"only": []any{"openai"}}}, body["providerOptions"])
	_, hasPrev := body["previous_response_id"]
	assert.False(t, hasPrev, "gateway gen_ ids are not provider response ids")
}

func TestClient_SessionIDHeader(t *testing.T) {
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5"} {
		t.Run(model, func(t *testing.T) {
			g := &gateway{handler: okHandler}
			c := newTestClient(t, g.start(t).URL, model)

			_, err := c.Chat(context.Background(), hi, nil)
			require.NoError(t, err)
			assert.Empty(t, g.last(t).Header.Get(HeaderSessionID), "no loom session, no header")

			ctx := session.WithSessionID(context.Background(), "sess-abc")
			_, err = c.Chat(ctx, hi, nil)
			require.NoError(t, err)
			got := g.last(t).Header.Get(HeaderSessionID)
			assert.Len(t, got, 32)
			assert.NotContains(t, got, "sess-abc", "the loom id is not sent")
			assert.Equal(t, gatewaySessionID("sess-abc"), got)

			_, err = c.Chat(session.WithSessionID(context.Background(), "sess-other"), hi, nil)
			require.NoError(t, err)
			assert.NotEqual(t, got, g.last(t).Header.Get(HeaderSessionID), "distinct sessions, distinct ids")
		})
	}
}

func TestSameModel(t *testing.T) {
	tests := []struct {
		served, requested string
		want              bool
	}{
		{"openai/gpt-5.5", "openai/gpt-5.5", true},
		{"gpt-5.5", "openai/gpt-5.5", true},
		{"gpt-5.5-2026-04-23", "openai/gpt-5.5", true},
		{"openai/gpt-5.5-2026-04-23", "openai/gpt-5.5", true},
		{"gpt-5.5-mini", "openai/gpt-5.5", false},
		{"gpt-5.5-2026-04", "openai/gpt-5.5", false},
		{"gpt-5.5-xxxx-04-23", "openai/gpt-5.5", false},
		{"claude-sonnet-4.5", "anthropic/claude-sonnet-4.5", true},
		{"openai/gpt-5", "openai/gpt-5.5", false},
		{"claude-sonnet-5-5", "anthropic/claude-sonnet-5.5", true},
		{"claude-sonnet-5", "anthropic/claude-sonnet-5.5", false},
		{"claude-sonnet-5-5-20261001", "anthropic/claude-sonnet-5.5", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, sameModel(tt.served, tt.requested), "%s vs %s", tt.served, tt.requested)
	}
}

func TestClient_NoSessionHeaderWhenAffinityIsOff(t *testing.T) {
	off := false
	g := &gateway{handler: okHandler}
	c := newTestClient(t, g.start(t).URL, "openai/gpt-5", func(cfg *Config) { cfg.Routing = &Routing{SessionAffinity: &off} })
	_, err := c.Chat(session.WithSessionID(context.Background(), "sess-abc"), hi, nil)
	require.NoError(t, err)
	assert.Empty(t, g.last(t).Header.Get(HeaderSessionID))
}

func TestGatewaySessionIDIsSalted(t *testing.T) {
	unsalted := sha256.Sum256([]byte("loom-session:sess-abc"))
	plain := sha256.Sum256([]byte("sess-abc"))
	got := gatewaySessionID("sess-abc")
	assert.NotEqual(t, hex.EncodeToString(unsalted[:16]), got)
	assert.NotEqual(t, hex.EncodeToString(plain[:16]), got)
	assert.Equal(t, got, gatewaySessionID("sess-abc"), "stable within the process")
}

// An error body that echoes the request (a gateway or upstream 400 quoting
// its input) must not carry a BYOK key into logs.
func TestClient_BYOKSecretIsRedactedFromErrors(t *testing.T) {
	const secret = "sk-ant-api03-fake-byok-value-for-redaction-test" // fake, test only
	t.Setenv("LOOM_BYOK_REDACT_TEST", secret)
	g := &gateway{handler: func(w http.ResponseWriter, _ *http.Request, raw []byte) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(append([]byte(`{"error":{"type":"invalid_request_error","message":"bad request: `), append(bytes.ReplaceAll(raw, []byte(`"`), []byte(`'`)), []byte(`"}}`)...)...))
	}}
	c := newTestClient(t, g.start(t).URL, "openai/gpt-5", func(cfg *Config) {
		cfg.Routing = &Routing{BYOK: []BYOKCredential{{Provider: "openai", Env: map[string]string{"apiKey": "LOOM_BYOK_REDACT_TEST"}}}}
	})
	_, err := c.Chat(context.Background(), hi, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.Contains(t, err.Error(), "[REDACTED]", "the echo is still visible, without the key")
	var sc llm.HTTPStatusCoder
	require.ErrorAs(t, err, &sc, "the status survives redaction")
	assert.Equal(t, http.StatusBadRequest, sc.HTTPStatusCode())
}
