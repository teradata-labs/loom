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
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// stubLLM is a named provider used as the server default in these tests.
type stubLLM struct{ name string }

func (s stubLLM) Chat(_ context.Context, _ []types.Message, _ []shuttle.Tool) (*types.LLMResponse, error) {
	return &types.LLMResponse{Content: s.name}, nil
}
func (s stubLLM) Name() string  { return s.name }
func (s stubLLM) Model() string { return s.name }

// The cold-start loop and the hot-reload callback both resolve an agent's
// LLM through resolveAgentRoleLLM. Hot reload used to ignore the agent's llm
// block and rebuild every agent on the server default.
func TestResolveAgentRoleLLM(t *testing.T) {
	isolateGatewayEnv(t)
	def := stubLLM{name: "server-default"}
	// A loopback endpoint: building a gateway client starts its catalog fetch,
	// and a test must never dial the real gateway.
	server := &Config{LLM: LLMConfig{VercelGatewayAPIKey: testGatewayKey, VercelGatewayEndpoint: "http://127.0.0.1:1"}}

	t.Run("no llm block uses the default", func(t *testing.T) {
		p, err := resolveAgentRoleLLM(nil, def, server, zap.NewNop())
		require.NoError(t, err)
		assert.Equal(t, def, p)
		p, err = resolveAgentRoleLLM(&loomv1.LLMConfig{}, def, server, zap.NewNop())
		require.NoError(t, err)
		assert.Equal(t, def, p)
	})
	t.Run("the agent's own provider is built", func(t *testing.T) {
		p, err := resolveAgentRoleLLM(&loomv1.LLMConfig{Provider: "vercel", Model: "openai/gpt-5"}, def, server, zap.NewNop())
		require.NoError(t, err)
		c, ok := p.(*vercelgateway.Client)
		require.True(t, ok, "got %T", p)
		assert.Equal(t, "openai/gpt-5", c.Model())
	})
	t.Run("an unbuildable gateway refuses the fallback", func(t *testing.T) {
		_, err := resolveAgentRoleLLM(&loomv1.LLMConfig{Provider: vercelgateway.ProviderName, Model: "not-a-gateway-id"}, def, server, zap.NewNop())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not allow a fallback LLM")
	})
	t.Run("an unbuildable provider that allows a fallback gets the default", func(t *testing.T) {
		p, err := resolveAgentRoleLLM(&loomv1.LLMConfig{Provider: "no-such-provider"}, def, server, zap.NewNop())
		require.NoError(t, err)
		assert.Equal(t, def, p)
	})
}

// looms workflow and looms eval used to fall through to ANTHROPIC_API_KEY when
// the configured gateway (or bedrock-mantle) client could not be built.
func TestResolveLLMProvider_NoFallbackForGatewayAndMantle(t *testing.T) {
	isolateGatewayEnv(t)
	isolateAWSEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-not-a-real-key")
	saved := config
	t.Cleanup(func() { config = saved })

	for _, llm := range []LLMConfig{
		{Provider: vercelgateway.ProviderName, VercelGatewayModel: "openai/gpt-5", VercelGatewayOIDC: true, // no token anywhere
			VercelGatewayRouting: GatewayRoutingConfig{ZeroDataRetention: true}},
		{Provider: "vercel", VercelGatewayModel: "gpt-5", VercelGatewayAPIKey: testGatewayKey}, // malformed id
		{Provider: "bedrock-mantle"}, // no region anywhere
	} {
		t.Run(llm.Provider, func(t *testing.T) {
			t.Setenv("BEDROCK_MANTLE_REGION", "")
			t.Setenv("AWS_REGION", "")
			config = &Config{LLM: llm}
			p, label, err := resolveLLMProvider(zap.NewNop())
			require.Error(t, err)
			assert.Nil(t, p)
			assert.Empty(t, label)
			assert.Contains(t, err.Error(), "Not falling back")
		})
	}

	t.Run("a buildable gateway is used", func(t *testing.T) {
		config = &Config{LLM: LLMConfig{Provider: vercelgateway.ProviderName, VercelGatewayModel: "openai/gpt-5",
			VercelGatewayAPIKey: testGatewayKey, VercelGatewayEndpoint: "http://127.0.0.1:1"}}
		p, label, err := resolveLLMProvider(zap.NewNop())
		require.NoError(t, err)
		assert.Equal(t, "Vercel AI Gateway (config)", label)
		assert.IsType(t, &vercelgateway.Client{}, p)
	})
}

func TestInheritCredentials_VercelGatewayOIDC(t *testing.T) {
	src := LLMConfig{VercelGatewayOIDC: true, VercelGatewayOIDCTokenFile: "/run/oidc"}
	got := inheritCredentials(LLMConfig{Provider: vercelgateway.ProviderName}, src)
	assert.True(t, got.VercelGatewayOIDC)
	assert.Equal(t, "/run/oidc", got.VercelGatewayOIDCTokenFile)

	own := inheritCredentials(LLMConfig{VercelGatewayAPIKey: "own-key"}, src)
	assert.False(t, own.VercelGatewayOIDC, "an entry with its own key does not take the main LLM's OIDC")

	ownFile := inheritCredentials(LLMConfig{VercelGatewayOIDC: true, VercelGatewayOIDCTokenFile: "/mine"}, src)
	assert.Equal(t, "/mine", ownFile.VercelGatewayOIDCTokenFile)
}

// The catalog is fetched once per endpoint, on the first client built for
// it, whichever path builds it (agent YAML only, here).
func TestEnsureVercelGatewayCatalog_OncePerEndpoint(t *testing.T) {
	t.Cleanup(func() { catalog.Register(nil) })
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == vercelgateway.ModelsPath {
			hits.Add(1)
			_, _ = io.WriteString(w, `{"data":[{"id":"acme/only-in-this-test","type":"language","context_window":1000,"max_tokens":100,"pricing":{"input":"0.000001","output":"0.000002"}}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	isolateGatewayEnv(t)
	server := &Config{LLM: LLMConfig{VercelGatewayEndpoint: srv.URL, VercelGatewayAPIKey: testGatewayKey}}
	for i := 0; i < 3; i++ {
		_, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: vercelgateway.ProviderName, Model: "acme/only-in-this-test"}, server, zap.NewNop())
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		_, _, ok := catalog.LookupPricing(vercelgateway.ProviderName, "acme/only-in-this-test")
		return ok
	}, 5*time.Second, 10*time.Millisecond, "the agent-only gateway model is priced from the catalog")
	assert.Equal(t, int32(1), hits.Load(), "one fetch per endpoint")
	in, out, ok := catalog.LookupPricing(vercelgateway.ProviderName, "acme/only-in-this-test")
	require.True(t, ok)
	assert.InDelta(t, 1.0, in, 1e-9)
	assert.InDelta(t, 2.0, out, 1e-9)
}

// The serve builder now passes an agent's seed to ollama (it used to be
// honoured only on the agent-registry path).
func TestCreateLLMProviderFromProtoConfig_OllamaSeed(t *testing.T) {
	var chatBody atomic.Value
	chatBody.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/chat":
			chatBody.Store(string(raw))
			_, _ = io.WriteString(w, `{"model":"llama3.1","message":{"role":"assistant","content":"ok"},"done":true}`)
		case "/api/show":
			_, _ = io.WriteString(w, `{"capabilities":["completion","tools"]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	seed := int64(42)
	p, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: "ollama", Model: "llama3.1", Seed: &seed},
		&Config{LLM: LLMConfig{OllamaEndpoint: srv.URL}}, zap.NewNop())
	require.NoError(t, err)
	_, err = p.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Contains(t, chatBody.Load(), `"seed":42`)
}

// resolveAgentLLMs is what both the cold-start loop and the hot-reload
// callback run on: the agent's own llm and all four role LLMs, with one
// fallback rule. judge_llm and orchestrator_llm were not built under serve.
func TestResolveAgentLLMs(t *testing.T) {
	isolateGatewayEnv(t)
	isolateAWSEnv(t)
	t.Setenv("BEDROCK_MANTLE_REGION", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("BEDROCK_MANTLE_ENDPOINT", "")
	def := stubLLM{name: "server-default"}
	server := &Config{LLM: LLMConfig{VercelGatewayAPIKey: testGatewayKey, VercelGatewayEndpoint: "http://127.0.0.1:1"}}

	set, err := resolveAgentLLMs(&loomv1.AgentConfig{
		Llm:             &loomv1.LLMConfig{Provider: "vercel", Model: "openai/gpt-5"},
		CompressorLlm:   &loomv1.LLMConfig{Provider: "vercel", Model: "openai/gpt-5-mini"},
		JudgeLlm:        &loomv1.LLMConfig{Provider: "vercel", Model: "anthropic/claude-haiku-4.5"},
		OrchestratorLlm: &loomv1.LLMConfig{Provider: "vercel", Model: "google/gemini-3-flash"},
	}, def, server, zap.NewNop())
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5", set.primary.Model(), "the agent's own llm, not the server default")
	require.NotNil(t, set.compressor)
	assert.Equal(t, "openai/gpt-5-mini", set.compressor.Model())
	require.NotNil(t, set.judge)
	assert.Equal(t, "anthropic/claude-haiku-4.5", set.judge.Model())
	require.NotNil(t, set.orchestrator)
	assert.Equal(t, "google/gemini-3-flash", set.orchestrator.Model())
	assert.Nil(t, set.classifier)

	primary, classifier, opts := set.instrumented(nil)
	assert.Equal(t, set.primary, primary)
	assert.Nil(t, classifier)
	assert.Len(t, opts, 3, "judge, orchestrator and compressor options")

	set, err = resolveAgentLLMs(&loomv1.AgentConfig{}, def, server, zap.NewNop())
	require.NoError(t, err)
	assert.Equal(t, def, set.primary)

	for _, cfg := range []*loomv1.AgentConfig{
		{Llm: &loomv1.LLMConfig{Provider: "vercel", Model: "bad-id"}},
		{CompressorLlm: &loomv1.LLMConfig{Provider: "vercel", Model: "bad-id"}},
		{ClassifierLlm: &loomv1.LLMConfig{Provider: "bedrock-mantle", Model: "x"}},
		{JudgeLlm: &loomv1.LLMConfig{Provider: "vercel", Model: "bad-id"}},
		{OrchestratorLlm: &loomv1.LLMConfig{Provider: "vercel", Model: "bad-id"}},
	} {
		_, err := resolveAgentLLMs(cfg, def, server, zap.NewNop())
		require.Error(t, err, "%v", cfg)
		assert.Contains(t, err.Error(), "does not allow a fallback LLM")
	}

	// A provider that allows a fallback degrades to the primary.
	set, err = resolveAgentLLMs(&loomv1.AgentConfig{ClassifierLlm: &loomv1.LLMConfig{Provider: "no-such-provider"}}, def, server, zap.NewNop())
	require.NoError(t, err)
	assert.Nil(t, set.classifier)
}

func TestConfigOrEnv(t *testing.T) {
	t.Setenv("CFG_OR_ENV_A", "")
	t.Setenv("CFG_OR_ENV_B", "from-b")
	t.Setenv("CFG_OR_ENV_PH", "from-placeholder")
	assert.Equal(t, "explicit", configOrEnv("explicit", "CFG_OR_ENV_B"))
	assert.Equal(t, "from-placeholder", configOrEnv("${CFG_OR_ENV_PH}", "CFG_OR_ENV_B"))
	assert.Equal(t, "from-b", configOrEnv("", "CFG_OR_ENV_A", "CFG_OR_ENV_B"))
	assert.Equal(t, "", configOrEnv("", "CFG_OR_ENV_A"))
}

// Runtime-created agents keep env-first location settings (owner's decision).
func TestRuntimeAgentLLMConfig_EnvFirst(t *testing.T) {
	base := &Config{LLM: LLMConfig{OllamaEndpoint: "http://cfg:11434", BedrockRegion: "us-west-2", BedrockProfile: "cfg"}}
	t.Setenv("OLLAMA_ENDPOINT", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_PROFILE", "")
	got := runtimeAgentLLMConfig(base)
	assert.Equal(t, "http://cfg:11434", got.LLM.OllamaEndpoint)
	assert.Equal(t, "us-west-2", got.LLM.BedrockRegion)

	t.Setenv("OLLAMA_ENDPOINT", "http://env:11434")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_PROFILE", "envprof")
	got = runtimeAgentLLMConfig(base)
	assert.Equal(t, "http://env:11434", got.LLM.OllamaEndpoint)
	assert.Equal(t, "eu-west-1", got.LLM.BedrockRegion)
	assert.Equal(t, "envprof", got.LLM.BedrockProfile)
	assert.Equal(t, "http://cfg:11434", base.LLM.OllamaEndpoint, "the server config is not modified")
}

func TestInheritCredentials_OIDCEntryDoesNotTakeMainKey(t *testing.T) {
	got := inheritCredentials(LLMConfig{VercelGatewayOIDC: true}, LLMConfig{VercelGatewayAPIKey: testGatewayKey})
	assert.Empty(t, got.VercelGatewayAPIKey, "a key would win over the entry's OIDC token")
}

// A provider with no credential is a build error, never a keyless client
// that fails on its first call: runtime-created agents then fail to create,
// and a role LLM falls back to the main LLM, as on the registry's own path.
func TestCreateLLMProviderFromProtoConfig_MissingKeyIsAnError(t *testing.T) {
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "MISTRAL_API_KEY", "GEMINI_API_KEY", "HUGGINGFACE_API_KEY", "HUGGINGFACE_TOKEN"} {
		t.Setenv(env, "")
	}
	for _, p := range []string{"anthropic", "openai", "mistral", "gemini", "huggingface"} {
		_, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: p, Model: "m"}, &Config{}, zap.NewNop())
		require.Error(t, err, p)
		assert.Contains(t, err.Error(), "API key not configured", p)
	}
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	_, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: "gemini", Model: "m"}, &Config{}, zap.NewNop())
	require.NoError(t, err, "an env-only key is enough")
}

// The env fallbacks reach the built client, not only configOrEnv: an env-only
// key builds (a missing one is an error, so reverting a fallback fails here),
// and an env-only OLLAMA_ENDPOINT is where the request goes.
func TestCreateLLMProviderFromProtoConfig_EnvFallbacksReachTheClient(t *testing.T) {
	for _, env := range []string{"MISTRAL_API_KEY", "GEMINI_API_KEY", "HUGGINGFACE_API_KEY", "HUGGINGFACE_TOKEN", "OLLAMA_ENDPOINT"} {
		t.Setenv(env, "")
	}
	for p, env := range map[string]string{"mistral": "MISTRAL_API_KEY", "gemini": "GEMINI_API_KEY", "huggingface": "HUGGINGFACE_API_KEY"} {
		_, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: p, Model: "m"}, &Config{}, zap.NewNop())
		require.Error(t, err, "%s without a key", p)
		t.Setenv(env, "test-not-a-real-key")
		_, err = createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: p, Model: "m"}, &Config{}, zap.NewNop())
		require.NoError(t, err, "%s with only %s set", p, env)
	}
	t.Setenv("HUGGINGFACE_API_KEY", "")
	t.Setenv("HUGGINGFACE_TOKEN", "test-not-a-real-key")
	_, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: "huggingface", Model: "m"}, &Config{}, zap.NewNop())
	require.NoError(t, err, "HUGGINGFACE_TOKEN is the backward-compatible name")

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/api/chat" {
			_, _ = io.WriteString(w, `{"model":"m","message":{"role":"assistant","content":"ok"},"done":true}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_ENDPOINT", srv.URL)
	p, err := createLLMProviderFromProtoConfig(&loomv1.LLMConfig{Provider: "ollama", Model: "m"}, &Config{}, zap.NewNop())
	require.NoError(t, err)
	_, err = p.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	require.NoError(t, err)
	assert.Positive(t, hits.Load(), "an env-only OLLAMA_ENDPOINT is used")
}
