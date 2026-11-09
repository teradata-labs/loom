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

package bedrockmantle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/types"
)

// Wire fixtures shaped like the live responses captured in the spike.
const (
	messagesOK = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":14,"output_tokens":4,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`

	messagesSSE = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[],"stop_reason":null,"usage":{"input_tokens":14,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"O"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"K"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	// chatSSENoDone mirrors Mantle's Chat Completions stream: a reasoning delta
	// in "reasoning", a usage chunk with empty choices, and no "data: [DONE]".
	chatSSENoDone = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{"reasoning":"think"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"openai.gpt-oss-120b","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n"
)

func chatOK(model, content, reasoning string) string {
	return fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%q,"reasoning":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, model, content, reasoning)
}

func routeError(model string) string {
	return fmt.Sprintf(`{"error":{"code":"invalid_request_error","message":"model `+"`%s`"+` isn't supported on this route","param":null,"type":"invalid_request_error"}}`, model)
}

// recorder is an httptest handler that records every request and answers
// from a per-path table.
type recorder struct {
	mu       sync.Mutex
	paths    []string
	headers  []http.Header
	bodies   [][]byte
	respond  func(path string) (status int, contentType, body string)
	hitCount map[string]int
}

func newRecorder(respond func(path string) (int, string, string)) *recorder {
	return &recorder{respond: respond, hitCount: map[string]int{}}
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.headers = append(r.headers, req.Header.Clone())
	r.bodies = append(r.bodies, body)
	r.hitCount[req.URL.Path]++
	r.mu.Unlock()

	status, ct, resp := r.respond(req.URL.Path)
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, resp)
}

func (r *recorder) hits(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hitCount[path]
}

func (r *recorder) lastHeader() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headers[len(r.headers)-1]
}

func (r *recorder) lastBody() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[len(r.bodies)-1]
}

func jsonResponder(status int, body string) func(string) (int, string, string) {
	return func(string) (int, string, string) { return status, "application/json", body }
}

func userMessage(text string) []types.Message {
	return []types.Message{{Role: "user", Content: text}}
}

func TestClient_ImplementsInterfaces(t *testing.T) {
	var _ types.StreamingLLMProvider = (*Client)(nil)
	var _ types.HealthChecker = (*Client)(nil)
}

func TestNewClientForModel_ClaudeUsesMessagesPathWithoutBetaHeader(t *testing.T) {
	rec := newRecorder(jsonResponder(http.StatusOK, messagesOK))
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "ABSKtest", Model: "anthropic.claude-haiku-4-5"})
	require.NoError(t, err)
	assert.Equal(t, ProviderName, c.Name())
	assert.Equal(t, "anthropic.claude-haiku-4-5", c.Model())
	assert.Equal(t, "", c.Route())
	assert.Equal(t, srv.URL+MessagesPath, c.Endpoint())

	resp, err := c.Chat(context.Background(), userMessage("Reply OK"), nil)
	require.NoError(t, err)
	assert.Equal(t, "OK", resp.Content)

	assert.Equal(t, 1, rec.hits(MessagesPath))
	h := rec.lastHeader()
	assert.Equal(t, "ABSKtest", h.Get("x-api-key"), "Bedrock API key travels as x-api-key, which Mantle accepts")
	assert.Equal(t, "2023-06-01", h.Get("anthropic-version"), "Mantle requires anthropic-version")
	_, hasBeta := h["Anthropic-Beta"]
	assert.False(t, hasBeta, "Mantle rejects every anthropic-beta value with 400")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.lastBody(), &body))
	assert.Equal(t, "anthropic.claude-haiku-4-5", body["model"], "Mantle id is sent verbatim, no us. prefix")
}

func TestNewClientForModel_ClaudeStreams(t *testing.T) {
	rec := newRecorder(func(string) (int, string, string) { return http.StatusOK, "text/event-stream", messagesSSE })
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "ABSKtest", Model: "anthropic.claude-haiku-4-5"})
	require.NoError(t, err)

	var tokens []string
	resp, err := c.ChatStream(context.Background(), userMessage("Reply OK"), nil, func(tok string) { tokens = append(tokens, tok) })
	require.NoError(t, err)
	assert.Equal(t, "OK", resp.Content)
	assert.Equal(t, []string{"O", "K"}, tokens)
	_, hasBeta := rec.lastHeader()["Anthropic-Beta"]
	assert.False(t, hasBeta)
}

func TestNewClientForModel_OpenAICompatibleRoutes(t *testing.T) {
	tests := []struct {
		model    string
		wantPath string
	}{
		{"openai.gpt-oss-120b", RouteV1 + chatCompletionsSuffix},
		{"mistral.devstral-2-123b", RouteV1 + chatCompletionsSuffix},
		{"google.gemma-4-e2b", RouteOpenAIV1 + chatCompletionsSuffix},
		{"xai.grok-4.6", RouteOpenAIV1 + chatCompletionsSuffix},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			rec := newRecorder(jsonResponder(http.StatusOK, chatOK(tt.model, "OK", "")))
			srv := httptest.NewServer(rec)
			defer srv.Close()

			c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "ABSKtest", Model: tt.model})
			require.NoError(t, err)
			assert.Equal(t, srv.URL+tt.wantPath, c.Endpoint())

			resp, err := c.Chat(context.Background(), userMessage("Reply OK"), nil)
			require.NoError(t, err)
			assert.Equal(t, "OK", resp.Content)
			assert.Equal(t, 1, rec.hits(tt.wantPath))
			assert.Equal(t, "Bearer ABSKtest", rec.lastHeader().Get("Authorization"))
		})
	}
}

func TestClient_ReasoningFieldBecomesThinking(t *testing.T) {
	rec := newRecorder(jsonResponder(http.StatusOK, chatOK("openai.gpt-oss-120b", "OK", "chain of thought")))
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "k", Model: "openai.gpt-oss-120b"})
	require.NoError(t, err)
	resp, err := c.Chat(context.Background(), userMessage("Reply OK"), nil)
	require.NoError(t, err)
	assert.Equal(t, "OK", resp.Content)
	assert.Equal(t, "chain of thought", resp.Thinking, "gpt-oss on Mantle reports the trace in \"reasoning\"")
}

func TestClient_ChatStreamWithoutDoneSentinel(t *testing.T) {
	rec := newRecorder(func(string) (int, string, string) { return http.StatusOK, "text/event-stream", chatSSENoDone })
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "k", Model: "openai.gpt-oss-120b"})
	require.NoError(t, err)

	var got strings.Builder
	resp, err := c.ChatStream(context.Background(), userMessage("Reply OK"), nil, func(tok string) { got.WriteString(tok) })
	require.NoError(t, err)
	assert.Equal(t, "OK", got.String())
	assert.Equal(t, "OK", resp.Content)
	assert.Equal(t, "think", resp.Thinking)
	assert.Equal(t, 10, resp.Usage.InputTokens, "usage chunk with empty choices is honored")
	assert.Equal(t, 5, resp.Usage.OutputTokens)
}

func TestClient_RouteFallbackIsSticky(t *testing.T) {
	const model = "vendor.unknown-model"
	rec := newRecorder(func(path string) (int, string, string) {
		if strings.HasPrefix(path, RouteOpenAIV1) {
			return http.StatusOK, "application/json", chatOK(model, "OK", "")
		}
		return http.StatusBadRequest, "application/json", routeError(model)
	})
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "k", Model: model})
	require.NoError(t, err)
	assert.Equal(t, RouteV1, c.Route(), "unknown vendors start on /v1")

	resp, err := c.Chat(context.Background(), userMessage("hi"), nil)
	require.NoError(t, err)
	assert.Equal(t, "OK", resp.Content)
	assert.Equal(t, RouteOpenAIV1, c.Route())
	assert.Equal(t, 1, rec.hits(RouteV1+chatCompletionsSuffix))
	assert.Equal(t, 1, rec.hits(RouteOpenAIV1+chatCompletionsSuffix))

	// The switch sticks: the second call goes straight to /openai/v1.
	_, err = c.Chat(context.Background(), userMessage("hi again"), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, rec.hits(RouteV1+chatCompletionsSuffix))
	assert.Equal(t, 2, rec.hits(RouteOpenAIV1+chatCompletionsSuffix))
}

func TestClient_RouteFallbackDoesNotPingPong(t *testing.T) {
	const model = "vendor.nowhere"
	rec := newRecorder(jsonResponder(http.StatusBadRequest, routeError(model)))
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "k", Model: model})
	require.NoError(t, err)

	_, err = c.Chat(context.Background(), userMessage("hi"), nil)
	require.Error(t, err)
	assert.True(t, IsRouteError(err))
	assert.Equal(t, 1, rec.hits(RouteV1+chatCompletionsSuffix))
	assert.Equal(t, 1, rec.hits(RouteOpenAIV1+chatCompletionsSuffix))

	// A later call is not retried on /v1 again: the client stays on the moved route.
	_, err = c.Chat(context.Background(), userMessage("hi"), nil)
	require.Error(t, err)
	assert.Equal(t, 1, rec.hits(RouteV1+chatCompletionsSuffix))
	assert.Equal(t, 2, rec.hits(RouteOpenAIV1+chatCompletionsSuffix))
}

func TestClient_RouteFallbackConcurrent(t *testing.T) {
	const model = "vendor.unknown-model"
	rec := newRecorder(func(path string) (int, string, string) {
		if strings.HasPrefix(path, RouteOpenAIV1) {
			return http.StatusOK, "application/json", chatOK(model, "OK", "")
		}
		return http.StatusBadRequest, "application/json", routeError(model)
	})
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "k", Model: model})
	require.NoError(t, err)

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Chat(context.Background(), userMessage("hi"), nil)
			if err != nil {
				errs <- err
				return
			}
			if resp.Content != "OK" {
				errs <- fmt.Errorf("unexpected content %q", resp.Content)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	assert.Equal(t, RouteOpenAIV1, c.Route())
	assert.LessOrEqual(t, rec.hits(RouteV1+chatCompletionsSuffix), n)
	assert.GreaterOrEqual(t, rec.hits(RouteOpenAIV1+chatCompletionsSuffix), n)
}

func TestClient_ClaudeNeverFallsOver(t *testing.T) {
	// Claude is only served on the Messages path; a route-shaped error there
	// must surface as-is rather than trigger a Chat Completions retry.
	rec := newRecorder(jsonResponder(http.StatusBadRequest, routeError("anthropic.claude-haiku-4-5")))
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "k", Model: "anthropic.claude-haiku-4-5"})
	require.NoError(t, err)
	_, err = c.Chat(context.Background(), userMessage("hi"), nil)
	require.Error(t, err)
	assert.Equal(t, 1, rec.hits(MessagesPath))
	assert.Equal(t, 0, rec.hits(RouteV1+chatCompletionsSuffix))
	assert.Equal(t, 0, rec.hits(RouteOpenAIV1+chatCompletionsSuffix))
}

func TestClient_HealthCheck(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		rec := newRecorder(jsonResponder(http.StatusOK, `{"data":[{"id":"anthropic.claude-haiku-4-5"}]}`))
		srv := httptest.NewServer(rec)
		defer srv.Close()
		c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "ABSKtest"})
		require.NoError(t, err)
		require.NoError(t, c.HealthCheck(context.Background()))
		assert.Equal(t, 1, rec.hits(ModelsPath))
		assert.Equal(t, "Bearer ABSKtest", rec.lastHeader().Get("Authorization"))
	})
	t.Run("rejected credential", func(t *testing.T) {
		rec := newRecorder(jsonResponder(http.StatusUnauthorized, `{"error":{"code":"invalid_api_key","message":"Invalid bearer token"}}`))
		srv := httptest.NewServer(rec)
		defer srv.Close()
		c, err := NewClientForModel(Config{Endpoint: srv.URL, Region: "us-west-2", APIKey: "ABSKtest"})
		require.NoError(t, err)
		err = c.HealthCheck(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
		assert.Contains(t, err.Error(), "Invalid bearer token")
	})
}

func TestNewClientForModel_SigV4SignsEveryRequest(t *testing.T) {
	// Static credentials exercise the SigV4 path end to end without touching
	// the ambient AWS environment.
	cfg := Config{
		Region:          "us-west-2",
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "secret",
		SessionToken:    "session",
	}

	t.Run("messages path", func(t *testing.T) {
		rec := newRecorder(jsonResponder(http.StatusOK, messagesOK))
		srv := httptest.NewServer(rec)
		defer srv.Close()
		cfg := cfg
		cfg.Endpoint, cfg.Model = srv.URL, "anthropic.claude-haiku-4-5"

		c, err := NewClientForModel(cfg)
		require.NoError(t, err)
		_, err = c.Chat(context.Background(), userMessage("hi"), nil)
		require.NoError(t, err)

		h := rec.lastHeader()
		assert.True(t, strings.HasPrefix(h.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/"), "got %q", h.Get("Authorization"))
		assert.Contains(t, h.Get("Authorization"), "/us-west-2/bedrock-mantle/aws4_request")
		assert.Equal(t, "session", h.Get("X-Amz-Security-Token"))
		_, hasAPIKey := h["X-Api-Key"]
		assert.False(t, hasAPIKey, "no empty x-api-key reaches the gateway")
		assert.Equal(t, "2023-06-01", h.Get("anthropic-version"))
	})

	t.Run("chat completions path", func(t *testing.T) {
		rec := newRecorder(jsonResponder(http.StatusOK, chatOK("openai.gpt-oss-120b", "OK", "")))
		srv := httptest.NewServer(rec)
		defer srv.Close()
		cfg := cfg
		cfg.Endpoint, cfg.Model = srv.URL, "openai.gpt-oss-120b"

		c, err := NewClientForModel(cfg)
		require.NoError(t, err)
		_, err = c.Chat(context.Background(), userMessage("hi"), nil)
		require.NoError(t, err)
		auth := rec.lastHeader().Get("Authorization")
		assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 "), "Bearer header must be replaced by the signature, got %q", auth)
	})

	t.Run("health check", func(t *testing.T) {
		rec := newRecorder(jsonResponder(http.StatusOK, `{"data":[]}`))
		srv := httptest.NewServer(rec)
		defer srv.Close()
		cfg := cfg
		cfg.Endpoint = srv.URL

		c, err := NewClientForModel(cfg)
		require.NoError(t, err)
		require.NoError(t, c.HealthCheck(context.Background()))
		assert.True(t, strings.HasPrefix(rec.lastHeader().Get("Authorization"), "AWS4-HMAC-SHA256 "))
	})
}

func TestNewClientForModel_Validation(t *testing.T) {
	t.Run("region or endpoint required", func(t *testing.T) {
		_, err := NewClientForModel(Config{APIKey: "k"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "region is required")
	})
	t.Run("sigv4 needs a region even with an endpoint override", func(t *testing.T) {
		_, err := NewClientForModel(Config{Endpoint: "http://127.0.0.1:1", AccessKeyID: "a", SecretAccessKey: "b"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SigV4")
	})
	t.Run("short-term key region must match", func(t *testing.T) {
		_, err := NewClientForModel(Config{Region: "us-west-2", APIKey: fakeShortTermKey("us-east-1", false)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `different region than the endpoint region "us-west-2"`)
		assert.NotContains(t, err.Error(), "us-east-1", "a value decoded from the key must not reach the error text")
	})
	t.Run("key content never reaches the error", func(t *testing.T) {
		// The credential scope comes from the key itself, so a malformed or
		// hostile key could carry any text there. The error is logged by
		// every caller; it must not echo it.
		const leaked = "SECRETPART"
		_, err := NewClientForModel(Config{Region: "us-west-2", APIKey: fakeShortTermKey(leaked, false)})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), leaked)
	})
	t.Run("short-term key region matches", func(t *testing.T) {
		c, err := NewClientForModel(Config{Region: "us-east-1", APIKey: fakeShortTermKey("us-east-1", false)})
		require.NoError(t, err)
		assert.Equal(t, "https://bedrock-mantle.us-east-1.api.aws"+MessagesPath, c.Endpoint())
	})
	t.Run("long-term key is not region checked", func(t *testing.T) {
		c, err := NewClientForModel(Config{Region: "eu-west-1", APIKey: "ABSKopaque"})
		require.NoError(t, err)
		assert.Equal(t, DefaultModel, c.Model())
	})
	t.Run("invalid endpoint", func(t *testing.T) {
		_, err := NewClientForModel(Config{Endpoint: "not a url", APIKey: "k"})
		require.Error(t, err)
	})
}

func TestResolveBaseURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"region", Config{Region: "us-west-2"}, "https://bedrock-mantle.us-west-2.api.aws"},
		{"endpoint wins", Config{Region: "us-west-2", Endpoint: "https://vpce-123.bedrock-mantle.us-west-2.vpce.amazonaws.com"}, "https://vpce-123.bedrock-mantle.us-west-2.vpce.amazonaws.com"},
		{"trailing slash", Config{Endpoint: "https://bedrock-mantle.us-east-1.api.aws/"}, "https://bedrock-mantle.us-east-1.api.aws"},
		{"trailing /v1", Config{Endpoint: "https://bedrock-mantle.us-east-1.api.aws/v1"}, "https://bedrock-mantle.us-east-1.api.aws"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBaseURL(tt.cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegionFromEndpoint(t *testing.T) {
	assert.Equal(t, "us-west-2", regionFromEndpoint("https://bedrock-mantle.us-west-2.api.aws"))
	assert.Equal(t, "us-gov-west-1", regionFromEndpoint("https://bedrock-mantle.us-gov-west-1.api.aws"))
	assert.Equal(t, "", regionFromEndpoint("http://127.0.0.1:8080"))
	assert.Equal(t, "", regionFromEndpoint("https://vpce-123.bedrock-mantle.us-west-2.vpce.amazonaws.com"))
	assert.Equal(t, "", regionFromEndpoint("::bad::"))
}

func TestNewClientForModel_RegionDerivedFromPublicEndpoint(t *testing.T) {
	// A public Mantle URL carries its region, so a short-term key can still be
	// checked against it when only Endpoint is set.
	_, err := NewClientForModel(Config{Endpoint: "https://bedrock-mantle.us-west-2.api.aws", APIKey: fakeShortTermKey("us-east-1", false)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `endpoint region "us-west-2"`)
	assert.NotContains(t, err.Error(), "us-east-1")
}
