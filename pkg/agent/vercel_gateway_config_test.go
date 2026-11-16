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

package agent

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
)

const gatewayAgentYAML = `
agent:
  name: gw-agent
  description: agent on the Vercel AI Gateway
  llm:
    provider: vercel
    model: anthropic/claude-sonnet-4.5
    rate_limit:
      requests_per_second: 20
    gateway_routing:
      order: [bedrock, anthropic]
      only: [bedrock, anthropic, vertex]
      sort: ttft
      fallback_models: [openai/gpt-5]
      zero_data_retention: true
      caching: auto
  judge_llm:
    provider: vercel-ai-gateway
    model: openai/gpt-5-mini
    gateway_routing:
      sort: cost
`

func TestLoadConfig_GatewayRoutingAndAlias(t *testing.T) {
	cfg, err := LoadConfigFromString(gatewayAgentYAML)
	require.NoError(t, err)
	require.NotNil(t, cfg.Llm)
	assert.Equal(t, vercelgateway.ProviderName, cfg.Llm.Provider, "alias normalised at load")
	assert.True(t, proto.Equal(&loomv1.LLMGatewayRouting{
		Order: []string{"bedrock", "anthropic"}, Only: []string{"bedrock", "anthropic", "vertex"}, Sort: "ttft",
		FallbackModels: []string{"openai/gpt-5"}, ZeroDataRetention: true, Caching: "auto",
	}, cfg.Llm.GatewayRouting), "got %v", cfg.Llm.GatewayRouting)
	require.NotNil(t, cfg.JudgeLlm)
	assert.Equal(t, "cost", cfg.JudgeLlm.GatewayRouting.GetSort())
	require.NoError(t, ValidateAgentConfig(cfg))

	// Round trip through YAML keeps routing and the main LLM's rate limit
	// (the latter was dropped for the main LLM before this change).
	y := protoToYAML(cfg)
	require.NotNil(t, y.Agent.LLM.GatewayRouting)
	assert.Equal(t, []string{"openai/gpt-5"}, y.Agent.LLM.GatewayRouting.FallbackModels)
	require.NotNil(t, y.Agent.LLM.RateLimit)
	assert.InDelta(t, 20.0, y.Agent.LLM.RateLimit.RequestsPerSecond, 1e-9)
	require.NotNil(t, y.Agent.JudgeLLM)
	assert.Equal(t, "cost", y.Agent.JudgeLLM.GatewayRouting.Sort)
}

func TestValidateAgentConfig_Gateway(t *testing.T) {
	tests := []struct {
		name    string
		llm     *loomv1.LLMConfig
		wantErr string
	}{
		{name: "valid", llm: &loomv1.LLMConfig{Provider: vercelgateway.ProviderName, Model: "openai/gpt-5", Temperature: 0.5}},
		{name: "alias", llm: &loomv1.LLMConfig{Provider: "vercel", Model: "openai/gpt-5"}},
		{name: "bare model", llm: &loomv1.LLMConfig{Provider: vercelgateway.ProviderName, Model: "gpt-5"}, wantErr: "creator/model"},
		{name: "bad routing", llm: &loomv1.LLMConfig{Provider: vercelgateway.ProviderName, Model: "openai/gpt-5",
			GatewayRouting: &loomv1.LLMGatewayRouting{Sort: "random"}}, wantErr: "gateway_routing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentConfig(&loomv1.AgentConfig{Name: "a", Llm: tt.llm})
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRegistry_CreateLLMProvider_VercelGateway(t *testing.T) {
	registry, _ := createTestRegistry(t)
	t.Setenv(vercelgateway.EnvLoomAPIKey, "")
	t.Setenv(vercelgateway.EnvAPIKey, "vck_test_not_a_real_key")
	t.Setenv("VERCEL_AI_GATEWAY_ENDPOINT", "")

	p, err := registry.createLLMProvider(&loomv1.LLMConfig{
		Provider:       "vercel",
		Model:          "anthropic/claude-sonnet-4.5",
		MaxTokens:      2048,
		GatewayRouting: &loomv1.LLMGatewayRouting{Only: []string{"bedrock"}},
	})
	require.NoError(t, err)
	client, ok := p.(*vercelgateway.Client)
	require.True(t, ok, "got %T", p)
	assert.Equal(t, vercelgateway.ProviderName, client.Name())
	assert.Equal(t, vercelgateway.SurfaceMessages, client.Surface())
	assert.Equal(t, vercelgateway.DefaultEndpoint, client.BaseURL())

	t.Setenv(vercelgateway.EnvAPIKey, "")
	_, err = registry.createLLMProvider(&loomv1.LLMConfig{Provider: vercelgateway.ProviderName, Model: "openai/gpt-5"})
	require.ErrorContains(t, err, "no credential")
}

func TestResolveContextLimits_GatewayFallback(t *testing.T) {
	// A model the catalog does not know falls back to the gateway default,
	// not the 8K generic floor.
	l := ResolveContextLimits(vercelgateway.ProviderName, "acme/unknown", 0, 0)
	assert.Equal(t, 128000, l.MaxContextTokens)
}

// looms serve hands the registry a builder so agents created or rebuilt at
// runtime get the server's credentials and gateway routing, not only the
// environment. Pool entries and the default still win where they apply.
func TestRegistry_LLMProviderBuilder(t *testing.T) {
	tmpDir := t.TempDir()
	var built []*loomv1.LLMConfig
	builderLLM := &mockLLMProvider{}
	r, err := NewRegistry(RegistryConfig{
		ConfigDir:   tmpDir,
		DBPath:      filepath.Join(tmpDir, "reg.db"),
		LLMProvider: &mockLLMProvider{},
		LLMProviderBuilder: func(c *loomv1.LLMConfig) (LLMProvider, error) {
			built = append(built, c)
			return builderLLM, nil
		},
		Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })

	p, err := r.createLLMProvider(&loomv1.LLMConfig{Provider: vercelgateway.ProviderName, Model: "openai/gpt-5"})
	require.NoError(t, err)
	assert.Same(t, builderLLM, p)
	require.Len(t, built, 1)
	assert.Equal(t, "openai/gpt-5", built[0].Model)

	pooled := &mockLLMProvider{}
	r.SetProviderPool(map[string]LLMProvider{"fast": pooled})
	p, err = r.createLLMProvider(&loomv1.LLMConfig{Provider: "fast"})
	require.NoError(t, err)
	assert.Same(t, pooled, p, "a pool entry wins over the builder")
	assert.Len(t, built, 1)
}

func TestValidateAgentConfig_GatewayRoutingNeedsTheGateway(t *testing.T) {
	routing := &loomv1.LLMGatewayRouting{ZeroDataRetention: true}
	tests := []struct {
		name    string
		cfg     *loomv1.AgentConfig
		wantErr string
	}{
		{name: "routing on another provider", cfg: &loomv1.AgentConfig{Name: "a",
			Llm: &loomv1.LLMConfig{Provider: "anthropic", Model: "claude-sonnet-4-5", GatewayRouting: routing}},
			wantErr: "llm.gateway_routing is honoured only by provider vercel-ai-gateway"},
		{name: "routing with no provider", cfg: &loomv1.AgentConfig{Name: "a",
			Llm: &loomv1.LLMConfig{GatewayRouting: routing}},
			wantErr: "llm.gateway_routing is honoured only"},
		{name: "routing on a role LLM of another provider", cfg: &loomv1.AgentConfig{Name: "a",
			JudgeLlm: &loomv1.LLMConfig{Provider: "openai", GatewayRouting: routing}},
			wantErr: "judge_llm.gateway_routing"},
		{name: "bad routing on a gateway role", cfg: &loomv1.AgentConfig{Name: "a",
			CompressorLlm: &loomv1.LLMConfig{Provider: "vercel", GatewayRouting: &loomv1.LLMGatewayRouting{Caching: "x"}}},
			wantErr: "compressor_llm.gateway_routing"},
		{name: "routing on the gateway", cfg: &loomv1.AgentConfig{Name: "a",
			Llm: &loomv1.LLMConfig{Provider: "vercel", Model: "openai/gpt-5", GatewayRouting: routing}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAgentConfig(tt.cfg)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
