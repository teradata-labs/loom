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

// GatewayRoutingConfig is llm.vercel_gateway_routing in looms.yaml: the typed
// routing options (proto LLMGatewayRouting) sent as providerOptions.gateway.
type GatewayRoutingConfig struct {
	Order             []string `mapstructure:"order" yaml:"order,omitempty"`
	Only              []string `mapstructure:"only" yaml:"only,omitempty"`
	Sort              string   `mapstructure:"sort" yaml:"sort,omitempty"`
	FallbackModels    []string `mapstructure:"fallback_models" yaml:"fallback_models,omitempty"`
	ZeroDataRetention bool     `mapstructure:"zero_data_retention" yaml:"zero_data_retention,omitempty"`
	Caching           string   `mapstructure:"caching" yaml:"caching,omitempty"`
}

// routing converts the looms.yaml block; nil when nothing is set.
func (g GatewayRoutingConfig) routing() *vercelgateway.Routing {
	r := &vercelgateway.Routing{
		Order:             append([]string(nil), g.Order...),
		Only:              append([]string(nil), g.Only...),
		Sort:              g.Sort,
		FallbackModels:    append([]string(nil), g.FallbackModels...),
		ZeroDataRetention: g.ZeroDataRetention,
		Caching:           g.Caching,
	}
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
		Routing:     cfg.VercelGatewayRouting.routing(),
	}
	if cfg.Timeout > 0 {
		gc.Timeout = time.Duration(cfg.Timeout) * time.Second
	}
	if cfg.VercelGatewayOIDC {
		gc.OIDC = &vercelgateway.OIDCConfig{TokenFile: loomconfig.ExpandEnvPlaceholders(cfg.VercelGatewayOIDCTokenFile)}
	}
	return gc
}

// applyProtoGatewayOverrides layers an agent's LLMConfig over the server's
// gateway config: the agent's model and, when set, its gateway_routing
// replace the server's.
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

// gatewayCatalogFirstFetchWait bounds how long building a gateway client
// waits for the first catalog fetch of its endpoint. The agent built next
// reads the model's context window from the catalog (agent.NewAgent), so the
// wait lets the window reach the first agents at startup. The fetch itself may
// take up to 30 s; past this bound the build goes on without the catalog.
const gatewayCatalogFirstFetchWait = 10 * time.Second

// gatewayCatalogFetch is the first catalog fetch of one gateway endpoint.
type gatewayCatalogFetch struct {
	// done is closed when the first fetch has finished, with or without an
	// error.
	done chan struct{}
	// deadline is the latest time any build waits for done. It is set once,
	// when the fetch starts, so all builds together wait at most one bound
	// per endpoint.
	deadline time.Time
}

// gatewayCatalogs records the first catalog fetch of each gateway endpoint
// whose catalog this process has registered. Each endpoint is fetched and
// refreshed once however many clients are built for it.
var gatewayCatalogs = struct {
	sync.Mutex
	started map[string]*gatewayCatalogFetch
}{started: map[string]*gatewayCatalogFetch{}}

// ensureVercelGatewayCatalog registers the gateway catalog (GET /v1/models,
// refreshed hourly) for endpoint the first time a gateway client is built in
// this process. It runs on every looms path that builds one (the server's
// main LLM, a pool entry, an agent's own llm block, a reload, looms
// workflow), so an agent that uses the gateway only through its own YAML
// gets catalog pricing, reasoning detection, output caps and its context
// window too.
//
// It waits for the endpoint's first fetch to finish, so the agent built from
// the client sees the catalog, but never past gatewayCatalogFirstFetchWait
// after that fetch started. Builds made while the fetch runs share that one
// deadline; once the fetch has finished or the deadline has passed, calls
// return at once. A failed or slow first
// fetch is logged and never fails the caller; the background refresh keeps
// retrying.
func ensureVercelGatewayCatalog(endpoint string, logger *zap.Logger) {
	ensureVercelGatewayCatalogWithin(endpoint, logger, gatewayCatalogFirstFetchWait)
}

// ensureVercelGatewayCatalogWithin is ensureVercelGatewayCatalog with the
// wait as a parameter, so tests can use a short one.
func ensureVercelGatewayCatalogWithin(endpoint string, logger *zap.Logger, wait time.Duration) {
	base, err := vercelgateway.NormalizeEndpoint(endpoint)
	if err != nil {
		return // NewClient has already rejected this endpoint
	}
	if logger == nil {
		logger = zap.L()
	}
	gatewayCatalogs.Lock()
	fetch, ok := gatewayCatalogs.started[base]
	if !ok {
		fetch = &gatewayCatalogFetch{done: make(chan struct{}), deadline: time.Now().Add(wait)}
		gatewayCatalogs.started[base] = fetch
		done := fetch.done
		// The fetch runs in the background so the wait below stays bounded.
		// Calls made before it lands price Claude ids from the family table,
		// report other ids unpriced, and budget against the default window.
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Vercel AI Gateway catalog fetch panicked; continuing without the catalog",
						zap.String("endpoint", base), zap.Any("panic", r))
				}
			}()
			if _, err := vercelgateway.RegisterCatalog(context.Background(), base, 0, logger); err != nil {
				logger.Warn("Vercel AI Gateway catalog fetch failed; will retry in the background", zap.Error(err))
			}
		}()
	}
	gatewayCatalogs.Unlock()

	select {
	case <-fetch.done:
		return
	default:
	}
	remaining := time.Until(fetch.deadline)
	if remaining <= 0 {
		return // the shared deadline has passed; a build already logged it
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-fetch.done:
	case <-timer.C:
		logger.Warn("Vercel AI Gateway catalog: the first fetch has not finished; continuing without it",
			zap.String("endpoint", base), zap.Duration("waited", remaining))
	}
}
