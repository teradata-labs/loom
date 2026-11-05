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

// Package bedrockmantle implements an LLM provider for the Amazon Bedrock
// "bedrock-mantle" endpoint (https://bedrock-mantle.{region}.api.aws).
//
// Mantle speaks the Anthropic Messages API for Claude models and the OpenAI
// Chat Completions API for everything else, so this package does not ship a
// third wire protocol. It builds the existing anthropic or openai HTTP client
// pointed at the right Mantle path and adds what Mantle needs on top:
//
//   - authentication with a Bedrock API key (sent as-is by the inner client)
//     or, when no key is set, AWS SigV4 signing of every request from the AWS
//     credential chain (static keys, named profile, SSO, IAM role);
//   - no anthropic-beta header, which Mantle rejects with 400;
//   - the per-vendor Chat Completions path prefix (/v1 or /openai/v1) with a
//     one-shot fallback to the other prefix on Mantle's route error;
//   - a health check against GET /v1/models.
//
// Model ids are Mantle's own ("anthropic.claude-haiku-4-5",
// "openai.gpt-oss-120b"): no "us." or "global." inference-profile prefixes,
// which Mantle rejects with 404. The endpoint lists models the account is not
// subscribed to; those fail at inference time with 401/403 and a marketplace
// subscription message, which is a configuration error, not a transient one.
//
// Everything this package asserts about the endpoint was observed live on
// 2026-11-04; see docs/research/bedrock-mantle-spike.md.
package bedrockmantle

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

const (
	// ProviderName is the provider string used in configuration
	// (llm.provider: bedrock-mantle) and returned by Client.Name.
	ProviderName = "bedrock-mantle"

	// CatalogProvider is the pkg/llm/catalog namespace Mantle model ids are
	// registered and priced under.
	CatalogProvider = ProviderName

	// DefaultModel is the Claude model that was visible in every region probed
	// during the spike (us-west-2, us-east-1, us-east-2, eu-west-1). Larger
	// Claude models are region- and subscription-dependent.
	DefaultModel = "anthropic.claude-haiku-4-5"

	// DefaultTimeout bounds a single blocking request.
	DefaultTimeout = 120 * time.Second
	// DefaultMaxTokens is the per-request output cap when none is configured.
	DefaultMaxTokens = 4096
	// DefaultTemperature matches the other Loom providers.
	DefaultTemperature = 1.0

	healthBodyLimit = 4096
)

// EndpointForRegion returns the public bedrock-mantle base URL for a region.
func EndpointForRegion(region string) string {
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws", region)
}

// Config holds configuration for a Bedrock Mantle client.
type Config struct {
	// Region selects the regional endpoint and scopes SigV4 signatures.
	// Required unless Endpoint is set and APIKey is used.
	Region string

	// Endpoint overrides the base URL (PrivateLink DNS names, tests). A
	// trailing "/" or "/v1" is stripped. When empty, EndpointForRegion(Region).
	Endpoint string

	// APIKey is a Bedrock API key. When set, requests carry it as a bearer
	// credential and no SigV4 signing happens. Short-term keys are bound to a
	// region; a mismatch with Region is rejected at construction. When empty,
	// every request is signed with SigV4 from the AWS credential chain.
	APIKey string

	// Static AWS credentials for SigV4. Optional; when empty the named Profile
	// or the default credential chain is used.
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// Profile names a shared-config profile for SigV4. Optional.
	Profile string

	// Model is the Mantle model id. Default: DefaultModel.
	Model string

	MaxTokens   int           // Default: DefaultMaxTokens
	Temperature float64       // Default: DefaultTemperature
	Timeout     time.Duration // Default: DefaultTimeout

	// ThinkingLevel requests extended thinking ("", "none" = off). Passed
	// through to the inner client; Claude on Mantle honors it.
	ThinkingLevel string

	// StrictToolSchemas is passed through to the inner client.
	StrictToolSchemas bool

	RateLimiterConfig llm.RateLimiterConfig
}

// Client implements types.StreamingLLMProvider and types.HealthChecker for
// one Mantle model by delegating to an anthropic.Client (Claude) or an
// openai.Client (every other vendor).
type Client struct {
	model   string
	baseURL string
	apiKey  string
	health  *http.Client

	// mu guards inner, route and moved: the Chat Completions route can be
	// switched once, from any goroutine, after Mantle's route error.
	mu     sync.RWMutex
	inner  types.StreamingLLMProvider
	route  string        // Chat Completions prefix in use; "" for Claude
	moved  bool          // the one-shot route fallback has been applied
	oaiCfg openai.Config // template for rebuilding the openai client on the other route
}

// NewClientForModel builds the client for cfg.Model. It resolves the base URL,
// chooses bearer or SigV4 auth, and constructs the inner client on the path
// Mantle serves that model on.
func NewClientForModel(cfg Config) (*Client, error) {
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = DefaultTemperature
	}

	baseURL, err := resolveBaseURL(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		cfg.Region = regionFromEndpoint(baseURL)
	}

	var wrap func(http.RoundTripper) http.RoundTripper
	if cfg.APIKey != "" {
		// The key's region is decoded from the key itself, so it stays out of
		// the error text: callers log this error, and a malformed key could
		// carry any part of the key in that field.
		if keyRegion, ok := ShortTermKeyRegion(cfg.APIKey); ok && cfg.Region != "" && keyRegion != cfg.Region {
			return nil, fmt.Errorf("bedrock-mantle: short-term API key is scoped to a different region than the endpoint region %q; mint the key for %q or change the region", cfg.Region, cfg.Region)
		}
	} else {
		if cfg.Region == "" {
			return nil, fmt.Errorf("bedrock-mantle: region is required for SigV4 signing when no API key is set")
		}
		awsCfg, err := loadAWSConfig(context.Background(), cfg)
		if err != nil {
			return nil, err
		}
		creds, region := awsCfg.Credentials, cfg.Region
		wrap = func(base http.RoundTripper) http.RoundTripper {
			return newSigV4Transport(base, creds, region)
		}
	}

	c := &Client{
		model:   cfg.Model,
		baseURL: baseURL,
		apiKey:  cfg.APIKey,
	}

	if IsAnthropicModel(cfg.Model) {
		c.inner = anthropic.NewClient(anthropic.Config{
			StrictToolSchemas: cfg.StrictToolSchemas,
			APIKey:            cfg.APIKey,
			Model:             cfg.Model,
			Endpoint:          baseURL + MessagesPath,
			Timeout:           cfg.Timeout,
			MaxTokens:         cfg.MaxTokens,
			Temperature:       cfg.Temperature,
			ThinkingLevel:     cfg.ThinkingLevel,
			RateLimiterConfig: cfg.RateLimiterConfig,
			NoBetaHeaders:     true,
			CatalogProvider:   CatalogProvider,
			WrapTransport:     wrap,
		})
	} else {
		c.route = RoutePrefix(cfg.Model)
		c.oaiCfg = openai.Config{
			StrictToolSchemas: cfg.StrictToolSchemas,
			APIKey:            cfg.APIKey,
			Model:             cfg.Model,
			Timeout:           cfg.Timeout,
			MaxTokens:         cfg.MaxTokens,
			Temperature:       cfg.Temperature,
			ThinkingLevel:     cfg.ThinkingLevel,
			RateLimiterConfig: cfg.RateLimiterConfig,
			CatalogProvider:   CatalogProvider,
			WrapTransport:     wrap,
		}
		c.inner = c.newOpenAIClient(c.route)
	}

	healthTransport := http.RoundTripper(http.DefaultTransport)
	if wrap != nil {
		healthTransport = wrap(healthTransport)
	}
	c.health = &http.Client{Timeout: cfg.Timeout, Transport: healthTransport}

	return c, nil
}

// resolveBaseURL applies the Endpoint override or derives the regional URL.
func resolveBaseURL(cfg Config) (string, error) {
	if cfg.Endpoint != "" {
		base := strings.TrimRight(cfg.Endpoint, "/")
		base = strings.TrimSuffix(base, RouteV1)
		if _, err := url.ParseRequestURI(base); err != nil {
			return "", fmt.Errorf("bedrock-mantle: invalid endpoint %q: %w", cfg.Endpoint, err)
		}
		return base, nil
	}
	if cfg.Region == "" {
		return "", fmt.Errorf("bedrock-mantle: region is required (or set an explicit endpoint)")
	}
	return EndpointForRegion(cfg.Region), nil
}

// regionFromEndpoint recovers the region from a public Mantle hostname
// (bedrock-mantle.<region>.api.aws). Returns "" for any other host.
func regionFromEndpoint(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) >= 4 && parts[0] == "bedrock-mantle" && parts[len(parts)-2] == "api" && parts[len(parts)-1] == "aws" {
		return parts[1]
	}
	return ""
}

func (c *Client) newOpenAIClient(route string) *openai.Client {
	cfg := c.oaiCfg
	cfg.Endpoint = chatCompletionsURL(c.baseURL, route)
	return openai.NewClient(cfg)
}

// Name returns the provider identifier.
func (c *Client) Name() string { return ProviderName }

// Model returns the configured Mantle model id.
func (c *Client) Model() string { return c.model }

// BaseURL returns the endpoint base URL in use.
func (c *Client) BaseURL() string { return c.baseURL }

// Route returns the Chat Completions prefix in use, or "" for a Claude model.
func (c *Client) Route() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.route
}

// Endpoint returns the full request URL the inner client posts to.
func (c *Client) Endpoint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.route == "" {
		return c.baseURL + MessagesPath
	}
	return chatCompletionsURL(c.baseURL, c.route)
}

func (c *Client) current() (types.StreamingLLMProvider, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.inner, c.route
}

// Chat sends a conversation to Mantle and returns the response.
func (c *Client) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	p, route := c.current()
	resp, err := p.Chat(ctx, messages, tools)
	if alt, ok := c.fallOver(route, err); ok {
		return alt.Chat(ctx, messages, tools)
	}
	return resp, err
}

// ChatStream streams tokens from Mantle as they are generated.
func (c *Client) ChatStream(ctx context.Context, messages []types.Message, tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*types.LLMResponse, error) {
	p, route := c.current()
	resp, err := p.ChatStream(ctx, messages, tools, tokenCallback)
	if alt, ok := c.fallOver(route, err); ok {
		return alt.ChatStream(ctx, messages, tools, tokenCallback)
	}
	return resp, err
}

// fallOver applies the one-shot route fallback. It returns the provider to
// retry on and true when err is Mantle's route error for a Chat Completions
// request and a different route is available; otherwise nil and false.
//
// The switch is sticky for the life of the client. A request that fails with
// the route error on the route the client has already moved to is returned as
// an error, so two bad routes can never ping-pong.
func (c *Client) fallOver(usedRoute string, err error) (types.StreamingLLMProvider, bool) {
	if usedRoute == "" || !IsRouteError(err) {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.moved {
		if c.route != usedRoute {
			// Another goroutine moved the route after this request started.
			return c.inner, true
		}
		return nil, false
	}
	c.route = otherRoute(usedRoute)
	c.inner = c.newOpenAIClient(c.route)
	c.moved = true
	zap.L().Info("bedrock-mantle: model is not served on the expected route; switched",
		zap.String("model", c.model),
		zap.String("from", usedRoute),
		zap.String("to", c.route))
	return c.inner, true
}

// HealthCheck verifies the endpoint and the credential with GET /v1/models,
// which needs no model subscription and no inference quota.
func (c *Client) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+ModelsPath, nil)
	if err != nil {
		return fmt.Errorf("bedrock-mantle: create health request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.health.Do(req)
	if err != nil {
		return fmt.Errorf("bedrock-mantle: health check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, healthBodyLimit))
		return fmt.Errorf("bedrock-mantle: health check failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
