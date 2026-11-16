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
	server := &Config{LLM: LLMConfig{VercelGatewayAPIKey: testGatewayKey}}

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
			p, label, err := resolveLLMProvider()
			require.Error(t, err)
			assert.Nil(t, p)
			assert.Empty(t, label)
			assert.Contains(t, err.Error(), "Not falling back")
		})
	}

	t.Run("a buildable gateway is used", func(t *testing.T) {
		config = &Config{LLM: LLMConfig{Provider: vercelgateway.ProviderName, VercelGatewayModel: "openai/gpt-5",
			VercelGatewayAPIKey: testGatewayKey, VercelGatewayEndpoint: "http://127.0.0.1:1"}}
		p, label, err := resolveLLMProvider()
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
	assert.Equal(t, int32(1), hits.Load(), "one fetch per endpoint")
	in, out, ok := catalog.LookupPricing(vercelgateway.ProviderName, "acme/only-in-this-test")
	require.True(t, ok, "the agent-only gateway model is priced from the catalog")
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
