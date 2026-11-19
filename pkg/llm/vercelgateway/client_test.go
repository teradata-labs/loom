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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

const fakeKey = "vck_test_not_a_real_key" // fake credential for tests

// Fixtures in the documented gateway shapes.
const (
	messagesSSE = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"gen_s1","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.5","content":[],"usage":{"input_tokens":12,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

	chatSSE = `data: {"id":"gen_cs","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"gen_cs","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[{"index":0,"delta":{"content":" there"},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"gen_cs","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[],"usage":{"prompt_tokens":40,"completion_tokens":4,"total_tokens":44},"providerMetadata":{"gateway":{"cost":"0.002","routing":{"finalProvider":"azure","modelAttemptCount":1,"totalProviderAttemptCount":1}}}}` + "\n\n" +
		"data: [DONE]\n\n"

	messagesToolUseSSE = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"gen_msg2","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4.5","content":[],"usage":{"input_tokens":50,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

	chatToolCallSSE = `data: {"id":"gen_c2","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
		`data: {"id":"gen_c2","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":8,"total_tokens":38}}` + "\n\n" +
		"data: [DONE]\n\n"
)

// gateway is a fake gateway that records every request.
type gateway struct {
	mu       sync.Mutex
	requests []recorded
	handler  func(w http.ResponseWriter, r *http.Request, body []byte)
}

type recorded struct {
	Path   string
	Header http.Header
	Body   map[string]any
}

func (g *gateway) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		g.mu.Lock()
		g.requests = append(g.requests, recorded{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		h := g.handler
		g.mu.Unlock()
		h(w, r, raw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (g *gateway) last(t *testing.T) recorded {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	require.NotEmpty(t, g.requests)
	return g.requests[len(g.requests)-1]
}

// respondBy returns the stream fixture for a surface. Every model call the
// client makes is streamed (Chat included), so there is no blocking fixture.
func respondBy(path string) string {
	if path == MessagesPath {
		return messagesSSE
	}
	return chatSSE
}

func okHandler(w http.ResponseWriter, r *http.Request, _ []byte) {
	if r.URL.Path == CreditsPath {
		_, _ = io.WriteString(w, `{"balance":"10.00","total_used":"1.25"}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, respondBy(r.URL.Path))
}

func newTestClient(t *testing.T, url, model string, mut ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{Endpoint: url, APIKey: fakeKey, Model: model}
	for _, m := range mut {
		m(&cfg)
	}
	c, err := NewClient(cfg)
	require.NoError(t, err)
	return c
}

var hi = []types.Message{{Role: "user", Content: "hi"}}

func TestNewClient_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "no model", cfg: Config{APIKey: fakeKey}, wantErr: "model is required"},
		{name: "bare model", cfg: Config{APIKey: fakeKey, Model: "gpt-5"}, wantErr: "creator/model"},
		{name: "no credential", cfg: Config{Model: "openai/gpt-5"}, wantErr: "no credential"},
		{name: "http endpoint", cfg: Config{APIKey: fakeKey, Model: "openai/gpt-5", Endpoint: "http://example.com"}, wantErr: "https"},
		{name: "bad routing", cfg: Config{APIKey: fakeKey, Model: "openai/gpt-5", Routing: &Routing{Sort: "x"}}, wantErr: "sort"},
		{name: "ok claude", cfg: Config{APIKey: fakeKey, Model: "anthropic/claude-sonnet-4.5"}},
		{name: "ok chat", cfg: Config{APIKey: fakeKey, Model: "openai/gpt-5", Routing: &Routing{Only: []string{"azure"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(tt.cfg)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, ProviderName, c.Name())
			assert.Equal(t, DefaultEndpoint, c.BaseURL())
		})
	}
}

func TestClient_RoutesByModelID(t *testing.T) {
	tests := []struct {
		model, surface, path string
	}{
		{"anthropic/claude-sonnet-4.5", SurfaceMessages, MessagesPath},
		{"anthropic/claude-opus-5.5", SurfaceMessages, MessagesPath},
		{"openai/gpt-5", SurfaceChat, ChatCompletionsPath},
		{"google/gemini-3.1-pro-preview", SurfaceChat, ChatCompletionsPath},
		{"vertex/claude-sonnet-4.5", SurfaceChat, ChatCompletionsPath},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			g := &gateway{handler: okHandler}
			srv := g.start(t)
			c := newTestClient(t, srv.URL, tt.model)
			assert.Equal(t, tt.surface, c.Surface())
			assert.Equal(t, srv.URL+tt.path, c.Endpoint())

			resp, err := c.Chat(context.Background(), hi, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.path, g.last(t).Path)
			assert.Equal(t, true, g.last(t).Body["stream"], "Chat is served over a stream")
			assert.Equal(t, tt.surface, resp.Metadata[AttrEndpointSurface])
			assert.Equal(t, tt.model, g.last(t).Body["model"], "the gateway id is sent unchanged")

			resp, err = c.ChatStream(context.Background(), hi, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.path, g.last(t).Path)
			assert.Equal(t, true, g.last(t).Body["stream"])
			assert.Equal(t, tt.surface, resp.Metadata[AttrEndpointSurface])
		})
	}
}

func TestClient_MessagesSurfaceHeaders(t *testing.T) {
	g := &gateway{handler: okHandler}
	c := newTestClient(t, g.start(t).URL, "anthropic/claude-sonnet-4.5")
	_, err := c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	h := g.last(t).Header
	assert.Equal(t, fakeKey, h.Get("x-api-key"))
	assert.Empty(t, h.Get("anthropic-beta"), "no beta header to the gateway")
	assert.Equal(t, "2023-06-01", h.Get("anthropic-version"))
}

func TestClient_ChatSurfaceHeaders(t *testing.T) {
	g := &gateway{handler: okHandler}
	c := newTestClient(t, g.start(t).URL, "openai/gpt-5")
	_, err := c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+fakeKey, g.last(t).Header.Get("Authorization"))
}

func TestClient_RoutingInBodyOnBothSurfaces(t *testing.T) {
	routing := &Routing{Order: []string{"bedrock", "anthropic"}, FallbackModels: []string{"openai/gpt-5"}, Caching: CachingAuto}
	want := map[string]any{"gateway": map[string]any{
		"order": []any{"bedrock", "anthropic"}, "models": []any{"openai/gpt-5"}, "caching": "auto"}}
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5"} {
		t.Run(model, func(t *testing.T) {
			g := &gateway{handler: okHandler}
			c := newTestClient(t, g.start(t).URL, model, func(cfg *Config) { cfg.Routing = routing })
			_, err := c.Chat(context.Background(), hi, nil)
			require.NoError(t, err)
			assert.Equal(t, want, g.last(t).Body["providerOptions"])
			_, err = c.ChatStream(context.Background(), hi, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, want, g.last(t).Body["providerOptions"])
		})
	}
}

func TestClient_NoRoutingLeavesBodyWithoutProviderOptions(t *testing.T) {
	g := &gateway{handler: okHandler}
	c := newTestClient(t, g.start(t).URL, "openai/gpt-5", func(cfg *Config) { cfg.Routing = &Routing{} })
	_, err := c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	_, has := g.last(t).Body["providerOptions"]
	assert.False(t, has)
}

func TestClient_GatewayCostAndRoutingMetadata(t *testing.T) {
	g := &gateway{handler: okHandler}
	c := newTestClient(t, g.start(t).URL, "openai/gpt-5")

	for name, call := range map[string]func() (*types.LLMResponse, error){
		"Chat":       func() (*types.LLMResponse, error) { return c.Chat(context.Background(), hi, nil) },
		"ChatStream": func() (*types.LLMResponse, error) { return c.ChatStream(context.Background(), hi, nil, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := call()
			require.NoError(t, err)
			assert.Equal(t, "hi there", resp.Content)
			assert.InDelta(t, 0.002, resp.Usage.CostUSD, 1e-12, "gateway-reported cost wins")
			assert.Equal(t, "azure", resp.Metadata[AttrFinalProvider])
			assert.Equal(t, "gen_cs", resp.Metadata[AttrGenerationID])
			assert.Equal(t, "false", resp.Metadata[AttrFallbackUsed])
			assert.Equal(t, "1", resp.Metadata[AttrAttemptCount])
		})
	}
}

func TestClient_ClaudeEstimateWhenGatewayReportsNoCost(t *testing.T) {
	g := &gateway{handler: okHandler}
	c := newTestClient(t, g.start(t).URL, "anthropic/claude-sonnet-4.5")
	resp, err := c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	// Sonnet 4.5 family card $3/$15: 12 in + 3 out (messagesSSE).
	assert.InDelta(t, (12*3.0+3*15.0)/1e6, resp.Usage.CostUSD, 1e-12)
	assert.Equal(t, "gen_s1", resp.Metadata[AttrGenerationID])
	_, unpriced := resp.Metadata[llm.MetadataCostUnpriced]
	assert.False(t, unpriced)
}

func TestClient_UnknownNonClaudeIsUnpriced(t *testing.T) {
	g := &gateway{handler: func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.ReplaceAll(chatToolCallSSE, "openai/gpt-5", "acme/unknown-model"))
	}}
	c := newTestClient(t, g.start(t).URL, "acme/unknown-model")
	resp, err := c.Chat(context.Background(), hi, nil)
	require.NoError(t, err)
	assert.Zero(t, resp.Usage.CostUSD)
	assert.Equal(t, true, resp.Metadata[llm.MetadataCostUnpriced])
}

func TestClient_ToolCallsOnBothSurfaces(t *testing.T) {
	tool := &shuttle.MockTool{MockName: "get_weather"}
	tests := []struct {
		model, fixture, wantID string
		wantInput              map[string]interface{}
	}{
		{"anthropic/claude-sonnet-4.5", messagesToolUseSSE, "toolu_1", map[string]interface{}{"city": "Paris"}},
		{"openai/gpt-5", chatToolCallSSE, "call_1", map[string]interface{}{"city": "Oslo"}},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			g := &gateway{handler: func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tt.fixture)
			}}
			c := newTestClient(t, g.start(t).URL, tt.model)
			resp, err := c.Chat(context.Background(), hi, []shuttle.Tool{tool})
			require.NoError(t, err)
			require.Len(t, resp.ToolCalls, 1)
			assert.Equal(t, tt.wantID, resp.ToolCalls[0].ID)
			assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
			assert.Equal(t, tt.wantInput, resp.ToolCalls[0].Input)
			tools, ok := g.last(t).Body["tools"].([]any)
			require.True(t, ok, "tools are sent")
			assert.Len(t, tools, 1)
		})
	}
}

func TestClient_ErrorClassification(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantCredits   bool
		wantFailure   llm.ProviderFailure
		deterministic bool
	}{
		{name: "402 credits", status: http.StatusPaymentRequired,
			body:        `{"error":{"message":"Insufficient credits","type":"insufficient_quota"}}`,
			wantCredits: true, deterministic: true},
		{name: "401 bad key", status: http.StatusUnauthorized,
			body: `{"error":{"message":"Invalid API key","type":"authentication_error"}}`, deterministic: true},
		{name: "404 unknown model", status: http.StatusNotFound,
			body: `{"error":{"message":"model not found","type":"invalid_request_error"}}`, deterministic: true},
		{name: "400 unsatisfiable only", status: http.StatusBadRequest,
			body: `{"error":{"message":"No providers available","type":"invalid_request_error"}}`, deterministic: true},
	}
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5"} {
		for _, tt := range tests {
			t.Run(model+"/"+tt.name, func(t *testing.T) {
				g := &gateway{handler: func(w http.ResponseWriter, _ *http.Request, _ []byte) {
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
				}}
				c := newTestClient(t, g.start(t).URL, model)
				for _, call := range []func() error{
					func() error { _, err := c.Chat(context.Background(), hi, nil); return err },
					func() error { _, err := c.ChatStream(context.Background(), hi, nil, nil); return err },
				} {
					err := call()
					require.Error(t, err)
					assert.Equal(t, tt.wantCredits, errors.Is(err, ErrCreditsExhausted), err.Error())
					assert.Equal(t, tt.deterministic, llm.IsDeterministicClientError(err), err.Error())
					if tt.wantCredits {
						assert.Contains(t, err.Error(), "credits")
						assert.NotContains(t, strings.ToLower(err.Error()), "authentication")
					}
				}
			})
		}
	}
}

// 5xx and 429 go through the inner clients' limiter retry, so a transient
// gateway failure is retried and then succeeds.
func TestClient_TransientRetriedOnBothSurfaces(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, model := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5"} {
			t.Run(model+"/"+http.StatusText(status), func(t *testing.T) {
				var mu sync.Mutex
				calls := 0
				g := &gateway{handler: func(w http.ResponseWriter, r *http.Request, raw []byte) {
					mu.Lock()
					calls++
					n := calls
					mu.Unlock()
					if n == 1 {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(status)
						_, _ = io.WriteString(w, `{"error":{"message":"busy","type":"rate_limit_error"}}`)
						return
					}
					okHandler(w, r, raw)
				}}
				c := newTestClient(t, g.start(t).URL, model, func(cfg *Config) {
					cfg.RateLimiterConfig = llm.RateLimiterConfig{Enabled: true, RequestsPerSecond: 100, BurstCapacity: 100,
						MinDelay: 1, RetryBackoff: 1, MaxRetries: 3}
				})
				resp, err := c.Chat(context.Background(), hi, nil)
				require.NoError(t, err)
				assert.NotEmpty(t, resp.Content)
				mu.Lock()
				assert.Equal(t, 2, calls)
				mu.Unlock()
			})
		}
	}
}

func TestMergeRateLimiter(t *testing.T) {
	d := DefaultRateLimiterConfig()
	got := mergeRateLimiter(llm.RateLimiterConfig{Enabled: true, RequestsPerSecond: 50})
	assert.Equal(t, 50.0, got.RequestsPerSecond, "explicit value kept")
	assert.Equal(t, d.TokensPerMinute, got.TokensPerMinute)
	assert.Equal(t, d.BurstCapacity, got.BurstCapacity)
	assert.Equal(t, d.MinDelay, got.MinDelay)
	assert.Equal(t, d.MaxRetries, got.MaxRetries)
	assert.Equal(t, d.RetryBackoff, got.RetryBackoff)
	assert.Equal(t, d.QueueTimeout, got.QueueTimeout)
	assert.NotNil(t, got.Logger)
	assert.True(t, got.Enabled)
}

func TestHealthCheck(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErr     string
		wantCredits bool
	}{
		{name: "ok", status: 200, body: `{"balance":"95.50","token_pass":null,"total_used":"4.50"}`},
		{name: "bad key", status: 401, body: `{"error":{"message":"Invalid API key"}}`, wantErr: "status 401"},
		{name: "402", status: 402, body: `{"error":{"message":"no credit"}}`, wantErr: "payment required", wantCredits: true},
		{name: "bad body", status: 200, body: `nope`, wantErr: "decode credits"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &gateway{handler: func(w http.ResponseWriter, r *http.Request, _ []byte) {
				assert.Equal(t, CreditsPath, r.URL.Path)
				assert.Equal(t, http.MethodGet, r.Method)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}}
			c := newTestClient(t, g.start(t).URL, "openai/gpt-5")
			err := c.HealthCheck(context.Background())
			assert.Equal(t, "Bearer "+fakeKey, g.last(t).Header.Get("Authorization"))
			if tt.wantErr == "" {
				require.NoError(t, err)
				cr, err := c.Credits(context.Background())
				require.NoError(t, err)
				assert.Equal(t, "95.50", cr.Balance)
				assert.Equal(t, "4.50", cr.TotalUsed)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Equal(t, tt.wantCredits, errors.Is(err, ErrCreditsExhausted))
			assert.NotContains(t, err.Error(), fakeKey, "the key never appears in an error")
		})
	}
}

func TestResolveAPIKey(t *testing.T) {
	t.Setenv(EnvLoomAPIKey, "")
	t.Setenv(EnvAPIKey, "")
	assert.Equal(t, "", ResolveAPIKey(""))
	t.Setenv(EnvAPIKey, "from-ai-gateway")
	assert.Equal(t, "from-ai-gateway", ResolveAPIKey(""))
	t.Setenv(EnvLoomAPIKey, "from-loom")
	assert.Equal(t, "from-loom", ResolveAPIKey(""))
	assert.Equal(t, "explicit", ResolveAPIKey(" explicit "))
}

// Many goroutines share one client (the server's provider pool does this).
func TestClient_ConcurrentUse(t *testing.T) {
	g := &gateway{handler: okHandler}
	srv := g.start(t)
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "openai/gpt-5"} {
		c := newTestClient(t, srv.URL, model, func(cfg *Config) { cfg.Routing = &Routing{Only: []string{"bedrock"}} })
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				var err error
				if i%2 == 0 {
					_, err = c.Chat(context.Background(), hi, nil)
				} else {
					_, err = c.ChatStream(context.Background(), hi, nil, nil)
				}
				assert.NoError(t, err)
			}(i)
		}
		wg.Wait()
	}
}
