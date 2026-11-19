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

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/agent"
	loomconfig "github.com/teradata-labs/loom/pkg/config"
	"github.com/teradata-labs/loom/pkg/embedding"
	"github.com/teradata-labs/loom/pkg/llm/vercelgateway"
)

// defaultGatewayEmbeddingModel mirrors the openai embedding provider's
// default (text-embedding-3-small, 1536 dimensions), addressed through the
// gateway.
const defaultGatewayEmbeddingModel = "openai/text-embedding-3-small"

// newVercelGatewayEmbedder builds the memory embedder for embedding.provider:
// vercel-ai-gateway. The gateway's POST /v1/embeddings takes the OpenAI
// request shape, so the OpenAI embedder is pointed at the gateway base URL.
// Key: embedding.api_key, else the LLM gateway key chain. OIDC is not
// supported for embeddings (the embedder sends a static bearer key).
func newVercelGatewayEmbedder(c *Config) (*embedding.OpenAIEmbedder, error) {
	key := loomconfig.ExpandEnvPlaceholders(c.Embedding.APIKey)
	if key == "" {
		key = vercelgateway.ResolveAPIKey(loomconfig.ExpandEnvPlaceholders(c.LLM.VercelGatewayAPIKey))
	}
	if key == "" {
		return nil, fmt.Errorf("no gateway API key (set embedding.api_key, the vercel_gateway_api_key keyring entry, %s or %s)", vercelgateway.EnvLoomAPIKey, vercelgateway.EnvAPIKey)
	}
	model := c.Embedding.Model
	if model == "" {
		model = defaultGatewayEmbeddingModel
	}
	if err := vercelgateway.ValidateModelID(model); err != nil {
		return nil, fmt.Errorf("embedding.model: %w", err)
	}
	base, err := vercelgateway.NormalizeEndpoint(loomconfig.ExpandEnvPlaceholders(c.LLM.VercelGatewayEndpoint))
	if err != nil {
		return nil, err
	}
	return embedding.NewOpenAIEmbedder(embedding.OpenAIConfig{
		APIKey:     key,
		Model:      model,
		Dimensions: c.Embedding.Dimensions,
		BaseURL:    base + "/v1",
	}), nil
}

// GatewayRoutingConfig is llm.vercel_gateway_routing in looms.yaml. It is
// the agent YAML block (proto LLMGatewayRouting), so both files take the same
// keys.
type GatewayRoutingConfig = agent.LLMGatewayRoutingYAML

// gatewayRouting converts the looms.yaml block; nil when nothing is set.
func gatewayRouting(g GatewayRoutingConfig) *vercelgateway.Routing {
	r := vercelgateway.RoutingFromProto(g.ToProto())
	if r.IsZero() {
		return nil
	}
	return r
}

// vercelGatewayConfigFrom builds the vercelgateway.Config shared by every
// looms path that constructs a gateway client from server configuration:
// model, endpoint, the API key (config/keyring, then
// LOOM_LLM_VERCEL_GATEWAY_API_KEY, then AI_GATEWAY_API_KEY), OIDC, routing,
// and the common generation settings. ${ENV_VAR} placeholders are expanded.
func vercelGatewayConfigFrom(cfg LLMConfig) vercelgateway.Config {
	gc := vercelgateway.Config{
		Endpoint:    loomconfig.ExpandEnvPlaceholders(cfg.VercelGatewayEndpoint),
		APIKey:      vercelgateway.ResolveAPIKey(loomconfig.ExpandEnvPlaceholders(cfg.VercelGatewayAPIKey)),
		Model:       loomconfig.ExpandEnvPlaceholders(cfg.VercelGatewayModel),
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		Routing:     gatewayRouting(cfg.VercelGatewayRouting),
		API:         cfg.VercelGatewayAPI,
	}
	if cfg.Timeout > 0 {
		gc.Timeout = time.Duration(cfg.Timeout) * time.Second
	}
	if cfg.VercelGatewayStreamFirstByteTimeout > 0 {
		gc.StreamFirstByteTimeout = time.Duration(cfg.VercelGatewayStreamFirstByteTimeout) * time.Second
	}
	if cfg.VercelGatewayStreamIdleTimeout > 0 {
		gc.StreamIdleTimeout = time.Duration(cfg.VercelGatewayStreamIdleTimeout) * time.Second
	}
	if cfg.VercelGatewayOIDC {
		gc.OIDC = &vercelgateway.OIDCConfig{TokenFile: loomconfig.ExpandEnvPlaceholders(cfg.VercelGatewayOIDCTokenFile)}
	}
	return gc
}

// applyProtoGatewayOverrides layers an agent's LLMConfig over the server's
// gateway config: the agent's model and, when set, its gateway_routing and
// gateway_api replace the server's.
func applyProtoGatewayOverrides(gc vercelgateway.Config, p *loomv1.LLMConfig) vercelgateway.Config {
	if p == nil {
		return gc
	}
	if p.GetModel() != "" {
		gc.Model = p.GetModel()
	}
	if r := vercelgateway.RoutingFromProto(p.GetGatewayRouting()); !r.IsZero() {
		gc.Routing = r
	}
	if api := p.GetGatewayApi(); api != "" {
		gc.API = api
	}
	return gc
}

// validateVercelGatewayConfig is the looms.yaml check for the provider: a
// model, a valid endpoint and routing block, and a credential.
func validateVercelGatewayConfig(cfg LLMConfig) error {
	gc := vercelGatewayConfigFrom(cfg)
	if err := vercelgateway.ValidateModelID(gc.Model); err != nil {
		return fmt.Errorf("%w (set llm.vercel_gateway_model)", err)
	}
	if _, err := vercelgateway.NormalizeEndpoint(gc.Endpoint); err != nil {
		return fmt.Errorf("%w (llm.vercel_gateway_endpoint)", err)
	}
	if err := gc.Routing.Validate(); err != nil {
		return fmt.Errorf("%w (llm.vercel_gateway_routing)", err)
	}
	if err := vercelgateway.ValidateAPI(gc.API); err != nil {
		return fmt.Errorf("%w (llm.vercel_gateway_api)", err)
	}
	if gc.APIKey == "" && gc.OIDC == nil {
		return fmt.Errorf("vercel-ai-gateway API key is required (save to keyring with 'looms config set-key vercel_gateway_api_key', or set %s / %s), or set llm.vercel_gateway_oidc: true", vercelgateway.EnvLoomAPIKey, vercelgateway.EnvAPIKey)
	}
	return nil
}

// normalizeProviderNames maps the "vercel" alias to the canonical provider
// name on the main LLM and every pool entry, so limiter keys, the catalog
// namespace and span attributes only ever see vercel-ai-gateway.
func normalizeProviderNames(c *Config) {
	c.LLM.Provider = vercelgateway.NormalizeProviderName(c.LLM.Provider)
	for i := range c.Providers {
		c.Providers[i].LLM.Provider = vercelgateway.NormalizeProviderName(c.Providers[i].LLM.Provider)
	}
}

// gatewayCatalogs records the gateway endpoints whose catalog this process
// has registered, so each is fetched and refreshed once however many clients
// are built for it.
var gatewayCatalogs = struct {
	sync.Mutex
	started map[string]bool
}{started: map[string]bool{}}

// ensureVercelGatewayCatalog registers the gateway catalog (GET /v1/models,
// refreshed hourly) for endpoint the first time a gateway client is built in
// this process. It runs on every looms path that builds one (the server's
// main LLM, a pool entry, an agent's own llm block, a reload, looms
// workflow), so an agent that uses the gateway only through its own YAML
// gets catalog pricing, reasoning detection and output caps too. The fetch
// does not block the caller; a failed first fetch is logged, and the
// background refresh keeps retrying.
func ensureVercelGatewayCatalog(endpoint string, logger *zap.Logger) {
	base, err := vercelgateway.NormalizeEndpoint(endpoint)
	if err != nil {
		return // NewClient has already rejected this endpoint
	}
	gatewayCatalogs.Lock()
	if gatewayCatalogs.started[base] {
		gatewayCatalogs.Unlock()
		return
	}
	gatewayCatalogs.started[base] = true
	gatewayCatalogs.Unlock()
	if logger == nil {
		logger = zap.L()
	}
	// The first fetch (up to 30 s) runs in the background, so building a
	// client on an RPC or reload path never waits for it. Calls made before it
	// lands price Claude ids from the family table and report other ids
	// unpriced.
	go func() {
		if _, err := vercelgateway.RegisterCatalog(context.Background(), base, 0, logger); err != nil {
			logger.Warn("Vercel AI Gateway catalog fetch failed; will retry in the background", zap.Error(err))
		}
	}()
}
