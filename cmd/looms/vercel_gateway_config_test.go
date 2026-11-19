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

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway/gatewaytest"
	"github.com/teradata-labs/loom/pkg/types"
)

const testGatewayKey = "vck_test_not_a_real_key" // fake credential for tests

// isolateGatewayEnv clears every variable the gateway key chain reads.
func isolateGatewayEnv(t *testing.T) {
	t.Helper()
	t.Setenv(vercelgateway.EnvLoomAPIKey, "")
	t.Setenv(vercelgateway.EnvAPIKey, "")
	t.Setenv(vercelgateway.EnvOIDCToken, "")
}

func TestVercelGatewayConfigFrom(t *testing.T) {
	isolateGatewayEnv(t)
	t.Setenv("GW_MODEL_FOR_TEST", "openai/gpt-5")
	gc := vercelGatewayConfigFrom(LLMConfig{
		VercelGatewayModel:    "${GW_MODEL_FOR_TEST}",
		VercelGatewayEndpoint: "https://eu.ai-gateway.vercel.sh",
		VercelGatewayAPIKey:   testGatewayKey,
		VercelGatewayRouting:  GatewayRoutingConfig{Order: []string{"azure"}, Sort: "cost"},
		MaxTokens:             2048,
		Temperature:           0.3,
		Timeout:               45,
	})
	assert.Equal(t, "openai/gpt-5", gc.Model, "placeholders expand")
	assert.Equal(t, "https://eu.ai-gateway.vercel.sh", gc.Endpoint)
	assert.Equal(t, testGatewayKey, gc.APIKey)
	assert.Equal(t, &vercelgateway.Routing{Order: []string{"azure"}, Sort: "cost"}, gc.Routing)
	assert.Equal(t, 2048, gc.MaxTokens)
	assert.InDelta(t, 0.3, gc.Temperature, 1e-9)
	assert.Equal(t, 45*time.Second, gc.Timeout)
	assert.Nil(t, gc.OIDC)

	// An empty routing block is no routing at all.
	assert.Nil(t, vercelGatewayConfigFrom(LLMConfig{}).Routing)

	// Key chain: config, then LOOM_LLM_VERCEL_GATEWAY_API_KEY, then AI_GATEWAY_API_KEY.
	t.Setenv(vercelgateway.EnvAPIKey, "from-ai-gateway")
	assert.Equal(t, "from-ai-gateway", vercelGatewayConfigFrom(LLMConfig{}).APIKey)
	t.Setenv(vercelgateway.EnvLoomAPIKey, "from-loom")
	assert.Equal(t, "from-loom", vercelGatewayConfigFrom(LLMConfig{}).APIKey)

	oidc := vercelGatewayConfigFrom(LLMConfig{VercelGatewayOIDC: true, VercelGatewayOIDCTokenFile: "/run/token"})
	require.NotNil(t, oidc.OIDC)
	assert.Equal(t, "/run/token", oidc.OIDC.TokenFile)
}

func TestValidate_VercelGateway(t *testing.T) {
	isolateGatewayEnv(t)
	base := func(provider string, llm LLMConfig) *Config {
		llm.Provider = provider
		return &Config{
			Server:  ServerConfig{Port: 60051},
			LLM:     llm,
			Storage: StorageBackendConfig{Backend: "sqlite", SQLite: SQLiteConfig{Path: "/tmp/test.db"}},
		}
	}
	tests := []struct {
		name     string
		provider string
		llm      LLMConfig
		wantErr  string
	}{
		{name: "valid", provider: "vercel-ai-gateway",
			llm: LLMConfig{VercelGatewayModel: "openai/gpt-5", VercelGatewayAPIKey: testGatewayKey}},
		{name: "alias valid", provider: "vercel",
			llm: LLMConfig{VercelGatewayModel: "anthropic/claude-sonnet-4.5", VercelGatewayAPIKey: testGatewayKey}},
		{name: "oidc instead of key", provider: "vercel-ai-gateway",
			llm: LLMConfig{VercelGatewayModel: "openai/gpt-5", VercelGatewayOIDC: true}},
		{name: "no model", provider: "vercel-ai-gateway",
			llm: LLMConfig{VercelGatewayAPIKey: testGatewayKey}, wantErr: "llm.vercel_gateway_model"},
		{name: "bare model", provider: "vercel-ai-gateway",
			llm: LLMConfig{VercelGatewayModel: "gpt-5", VercelGatewayAPIKey: testGatewayKey}, wantErr: "creator/model"},
		{name: "no credential", provider: "vercel-ai-gateway",
			llm: LLMConfig{VercelGatewayModel: "openai/gpt-5"}, wantErr: "looms config set-key vercel_gateway_api_key"},
		{name: "http endpoint", provider: "vercel-ai-gateway",
			llm:     LLMConfig{VercelGatewayModel: "openai/gpt-5", VercelGatewayAPIKey: testGatewayKey, VercelGatewayEndpoint: "http://gw.example.com"},
			wantErr: "llm.vercel_gateway_endpoint"},
		{name: "bad routing", provider: "vercel-ai-gateway",
			llm: LLMConfig{VercelGatewayModel: "openai/gpt-5", VercelGatewayAPIKey: testGatewayKey,
				VercelGatewayRouting: GatewayRoutingConfig{Sort: "fastest"}},
			wantErr: "llm.vercel_gateway_routing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := base(tt.provider, tt.llm).Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestGetSecretMappings_VercelGatewayAPIKey(t *testing.T) {
	mappings := GetSecretMappings()
	idx := -1
	for i, m := range mappings {
		if m.KeyringKey == "vercel_gateway_api_key" {
			idx = i
		}
	}
	require.NotEqual(t, -1, idx, "vercel_gateway_api_key must be a keyring secret")
	cfg := &Config{}
	assert.False(t, mappings[idx].IsSet(cfg))
	mappings[idx].Setter(cfg, testGatewayKey)
	assert.Equal(t, testGatewayKey, cfg.LLM.VercelGatewayAPIKey)
	assert.True(t, mappings[idx].IsSet(cfg))
}

func TestNormalizeProviderNames(t *testing.T) {
	c := &Config{LLM: LLMConfig{Provider: "Vercel"}, Providers: []ProviderPoolEntry{
		{Name: "a", LLM: LLMConfig{Provider: "vercel"}},
		{Name: "b", LLM: LLMConfig{Provider: "anthropic"}},
	}}
	normalizeProviderNames(c)
	assert.Equal(t, vercelgateway.ProviderName, c.LLM.Provider)
	assert.Equal(t, vercelgateway.ProviderName, c.Providers[0].LLM.Provider)
	assert.Equal(t, "anthropic", c.Providers[1].LLM.Provider)
}

func TestInheritCredentials_VercelGateway(t *testing.T) {
	src := LLMConfig{VercelGatewayAPIKey: testGatewayKey, VercelGatewayEndpoint: "https://eu.ai-gateway.vercel.sh"}
	got := inheritCredentials(LLMConfig{Provider: vercelgateway.ProviderName}, src)
	assert.Equal(t, testGatewayKey, got.VercelGatewayAPIKey)
	assert.Equal(t, "https://eu.ai-gateway.vercel.sh", got.VercelGatewayEndpoint)
	own := inheritCredentials(LLMConfig{VercelGatewayAPIKey: "own", VercelGatewayEndpoint: "https://us.ai-gateway.vercel.sh"}, src)
	assert.Equal(t, "own", own.VercelGatewayAPIKey, "an entry's own key wins")
	assert.Equal(t, "https://us.ai-gateway.vercel.sh", own.VercelGatewayEndpoint)
}

func TestGetDefaultModelForProvider_VercelGateway(t *testing.T) {
	assert.Equal(t, "openai/gpt-5", getDefaultModelForProvider(&Config{LLM: LLMConfig{Provider: vercelgateway.ProviderName, VercelGatewayModel: "openai/gpt-5"}}))
	assert.Empty(t, getDefaultModelForProvider(&Config{LLM: LLMConfig{Provider: vercelgateway.ProviderName}}), "no default model")
}

func TestCustomLLMFallbackAllowed_VercelGateway(t *testing.T) {
	assert.False(t, customLLMFallbackAllowed("vercel-ai-gateway"))
	assert.False(t, customLLMFallbackAllowed("vercel"))
}

// gatewayBodies is a fake gateway recording request bodies.
type gatewayBodies struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
}

func (g *gatewayBodies) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		g.mu.Lock()
		g.paths = append(g.paths, r.URL.Path)
		g.bodies = append(g.bodies, m)
		g.mu.Unlock()
		gatewaytest.WriteSSE(w, r, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (g *gatewayBodies) last(t *testing.T) (string, map[string]any) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	require.NotEmpty(t, g.bodies)
	return g.paths[len(g.paths)-1], g.bodies[len(g.bodies)-1]
}

func TestCreateProviderWithRateLimit_VercelGateway(t *testing.T) {
	isolateGatewayEnv(t)
	g := &gatewayBodies{}
	srv := g.server(t)
	p, err := createProviderWithRateLimit(LLMConfig{
		Provider:              vercelgateway.ProviderName,
		VercelGatewayModel:    "openai/gpt-5",
		VercelGatewayEndpoint: srv.URL,
		VercelGatewayAPIKey:   testGatewayKey,
		VercelGatewayRouting:  GatewayRoutingConfig{Only: []string{"azure"}},
	}, zap.NewNop())
	require.NoError(t, err)
	client, ok := p.(*vercelgateway.Client)
	require.True(t, ok, "expected *vercelgateway.Client, got %T", p)
	assert.Equal(t, "openai/gpt-5", client.Model())
	assert.Equal(t, vercelgateway.SurfaceChat, client.Surface())

	_, err = client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	path, body := g.last(t)
	assert.Equal(t, vercelgateway.ChatCompletionsPath, path)
	assert.Equal(t, map[string]any{"gateway": map[string]any{"only": []any{"azure"}}}, body["providerOptions"])

	_, err = createProviderWithRateLimit(LLMConfig{Provider: vercelgateway.ProviderName, VercelGatewayAPIKey: testGatewayKey}, zap.NewNop())
	require.ErrorContains(t, err, "model is required")
}

func TestCreateLLMProviderFromProtoConfig_VercelGateway(t *testing.T) {
	isolateGatewayEnv(t)
	g := &gatewayBodies{}
	srv := g.server(t)
	server := &Config{LLM: LLMConfig{
		Provider:              vercelgateway.ProviderName,
		VercelGatewayModel:    "openai/gpt-5",
		VercelGatewayEndpoint: srv.URL,
		VercelGatewayAPIKey:   testGatewayKey,
		VercelGatewayRouting:  GatewayRoutingConfig{Order: []string{"server-default"}},
		MaxTokens:             1024,
	}}

	t.Run("agent model and routing replace the server's", func(t *testing.T) {
		p, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{
			Provider:       "vercel",
			Model:          "anthropic/claude-sonnet-4.5",
			GatewayRouting: &loomv1.LLMGatewayRouting{Only: []string{"bedrock"}},
		}, server, zap.NewNop())
		require.NoError(t, err)
		client := p.(*vercelgateway.Client)
		assert.Equal(t, "anthropic/claude-sonnet-4.5", client.Model())
		assert.Equal(t, vercelgateway.SurfaceMessages, client.Surface())
		_, err = client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
		require.NoError(t, err)
		path, body := g.last(t)
		assert.Equal(t, vercelgateway.MessagesPath, path)
		assert.Equal(t, map[string]any{"gateway": map[string]any{"only": []any{"bedrock"}}}, body["providerOptions"])
	})
	t.Run("no agent model or routing uses the server's", func(t *testing.T) {
		p, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: vercelgateway.ProviderName}, server, zap.NewNop())
		require.NoError(t, err)
		client := p.(*vercelgateway.Client)
		assert.Equal(t, "openai/gpt-5", client.Model())
		_, err = client.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
		require.NoError(t, err)
		_, body := g.last(t)
		assert.Equal(t, map[string]any{"gateway": map[string]any{"order": []any{"server-default"}}}, body["providerOptions"])
	})
	t.Run("invalid agent routing is an error", func(t *testing.T) {
		_, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: vercelgateway.ProviderName,
			GatewayRouting: &loomv1.LLMGatewayRouting{Sort: "nope"}}, server, zap.NewNop())
		require.ErrorContains(t, err, "sort")
	})
}

// LoadConfig maps the looms.yaml block, normalises the alias, and the key
// arrives from AI_GATEWAY_API_KEY.
func TestLoadConfig_VercelGateway(t *testing.T) {
	isolateGatewayEnv(t)
	t.Setenv(vercelgateway.EnvAPIKey, testGatewayKey)
	viper.Reset()
	t.Cleanup(viper.Reset)
	yaml := `server:
  port: 60051
llm:
  provider: vercel
  vercel_gateway_model: anthropic/claude-sonnet-4.5
  vercel_gateway_routing:
    order: [bedrock, anthropic]
    fallback_models: [openai/gpt-5]
    zero_data_retention: true
    caching: auto
`
	path := filepath.Join(t.TempDir(), "looms.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, vercelgateway.ProviderName, cfg.LLM.Provider, "alias normalised at load")
	assert.Equal(t, GatewayRoutingConfig{Order: []string{"bedrock", "anthropic"}, FallbackModels: []string{"openai/gpt-5"},
		ZeroDataRetention: true, Caching: "auto"}, cfg.LLM.VercelGatewayRouting)
	gc := vercelGatewayConfigFrom(cfg.LLM)
	assert.Equal(t, testGatewayKey, gc.APIKey)
	require.NoError(t, gc.Routing.Validate())
}

// The new routing keys and vercel_gateway_api load from looms.yaml through
// mapstructure, and BYOK secrets resolve from the named variables.
func TestLoadConfig_VercelGatewayFullRoutingAndAPI(t *testing.T) {
	isolateGatewayEnv(t)
	t.Setenv(vercelgateway.EnvAPIKey, testGatewayKey)
	t.Setenv("TEST_BYOK_ANTHROPIC", "byok-test-value")
	viper.Reset()
	t.Cleanup(viper.Reset)
	yaml := `server:
  port: 60051
llm:
  provider: vercel-ai-gateway
  vercel_gateway_model: openai/gpt-5.5
  vercel_gateway_api: responses
  vercel_gateway_routing:
    byok:
      - provider: anthropic
        env: {apiKey: TEST_BYOK_ANTHROPIC}
    provider_timeouts_ms: {anthropic: 15000}
    session_affinity: false
    disallow_prompt_training: true
    inference_region: {scope: zone, geo_region: us}
    headquarters: [US]
    user: u1
    tags: [env:test]
`
	path := filepath.Join(t.TempDir(), "looms.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NoError(t, validateVercelGatewayConfig(cfg.LLM))
	gc := vercelGatewayConfigFrom(cfg.LLM)
	assert.Equal(t, vercelgateway.APIResponses, gc.API)
	require.NotNil(t, gc.Routing)
	require.NotNil(t, gc.Routing.SessionAffinity)
	assert.False(t, *gc.Routing.SessionAffinity)
	assert.Equal(t, map[string]int{"anthropic": 15000}, gc.Routing.ProviderTimeoutsMS)
	assert.Equal(t, &vercelgateway.InferenceRegion{Scope: "zone", GeoRegion: "us"}, gc.Routing.InferenceRegion)
	body, err := gc.Routing.ResolveExtraBody(nil)
	require.NoError(t, err)
	gw := body["providerOptions"].(map[string]any)["gateway"].(map[string]any)
	assert.Equal(t, map[string][]map[string]any{"anthropic": {{"apiKey": "byok-test-value"}}}, gw["byok"])

	// A bad API value fails at load.
	cfg.LLM.VercelGatewayAPI = "completions"
	require.ErrorContains(t, validateVercelGatewayConfig(cfg.LLM), "llm.vercel_gateway_api")
}

func TestNewVercelGatewayEmbedder(t *testing.T) {
	isolateGatewayEnv(t)
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2],"index":0}]}`)
	}))
	defer srv.Close()

	cfg := &Config{LLM: LLMConfig{VercelGatewayEndpoint: srv.URL, VercelGatewayAPIKey: testGatewayKey},
		Embedding: EmbeddingConfig{Enabled: true, Provider: "vercel", Dimensions: 2}}
	emb, err := newVercelGatewayEmbedder(cfg)
	require.NoError(t, err)
	assert.Equal(t, defaultGatewayEmbeddingModel, emb.Model())
	vec, err := emb.Embed(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, []float32{0.1, 0.2}, vec)
	assert.Equal(t, "/v1/embeddings", gotPath)
	assert.Equal(t, "Bearer "+testGatewayKey, gotAuth)
	assert.Equal(t, defaultGatewayEmbeddingModel, gotBody["model"])

	_, err = newVercelGatewayEmbedder(&Config{Embedding: EmbeddingConfig{Model: "text-embedding-3-small", APIKey: "k"}})
	require.ErrorContains(t, err, "creator/model")
	_, err = newVercelGatewayEmbedder(&Config{})
	require.ErrorContains(t, err, "no gateway API key")
}
