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

// Package vercelgateway implements an LLM provider for the Vercel AI Gateway
// (https://ai-gateway.vercel.sh), one host that fronts many upstream
// providers under creator-scoped model ids ("anthropic/claude-sonnet-4.5",
// "openai/gpt-5").
//
// The gateway speaks the Anthropic Messages API, the OpenAI Chat Completions
// API and the OpenAI Responses API, so this package ships no wire protocol of
// its own. By default (Config.API "" or "auto") it routes by model id:
//
//   - anthropic/* ids go to an anthropic.Client on /v1/messages, so thinking
//     signatures, cache_control breakpoints and cache-token usage travel in
//     the native shape loom already round-trips;
//   - every other id goes to an openai.Client on /v1/chat/completions.
//
// Config.API "messages", "chat" or "responses" sends every id to that API
// (Responses through pkg/llm/openairesponses).
//
// On top of the inner clients it adds typed routing options (sent as
// providerOptions.gateway in the request body), a response inspector that
// reads the gateway's cost and routing metadata, a 402 error that names
// credits rather than auth, unpriced-cost reporting for ids with no known
// rate, and a health check against GET /v1/credits, which validates the key
// without spending tokens.
//
// See docs/reference/llm-vercel-ai-gateway.md. Behaviour the public docs
// leave unstated is listed there and in docs/plans/vercel-ai-gateway-plan.md
// §7; the live test (live_test.go) checks it against the real gateway.
package vercelgateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/anthropic"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	"github.com/teradata-labs/loom/pkg/llm/openairesponses"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

const (
	// DefaultTimeout bounds the health check and is the ceiling for a single
	// non-streamed request. Model calls are streamed (see Client.Chat), so a
	// long answer that keeps producing bytes is not cut off by it.
	DefaultTimeout = 300 * time.Second
	// DefaultStreamFirstByteTimeout bounds the wait for the first response
	// byte, headers included. Measured live on 2026-11-19: headers within 6 s
	// for Sonnet 5.5 with thinking and GPT-5.5 at xhigh reasoning.
	DefaultStreamFirstByteTimeout = 60 * time.Second
	// DefaultStreamIdleTimeout bounds the silence between stream reads.
	// Measured live on 2026-11-19: the longest gap was 13.4 s (GPT-5.5, xhigh
	// reasoning, Chat Completions); Sonnet 5.5 with thinking 5.5 s.
	DefaultStreamIdleTimeout = 90 * time.Second
	// providerTimeoutMargin is added to the longest BYOK provider timeout when
	// it sets the first-byte wait, leaving the gateway time to fail over.
	providerTimeoutMargin = 30 * time.Second
	// DefaultMaxTokens is the per-request output cap when none is configured
	// and the catalog does not know the model.
	DefaultMaxTokens = 4096
	// DefaultTemperature matches the other Loom providers.
	DefaultTemperature = 1.0

	// EnvAPIKey is the gateway's own API key variable (also read by the Jev
	// decision provider in pkg/decision/jev).
	EnvAPIKey = "AI_GATEWAY_API_KEY" // #nosec G101 -- environment variable name, not a credential
	// EnvLoomAPIKey is the loom-scoped override, checked before EnvAPIKey.
	EnvLoomAPIKey = "LOOM_LLM_VERCEL_GATEWAY_API_KEY" // #nosec G101 -- environment variable name, not a credential

	healthBodyLimit = 4096
)

// API surface values for Config.API.
const (
	APIAuto      = "auto"
	APIMessages  = SurfaceMessages
	APIChat      = SurfaceChat
	APIResponses = SurfaceResponses
)

// ValidateAPI reports whether api is a Config.API value.
func ValidateAPI(api string) error {
	switch api {
	case "", APIAuto, APIMessages, APIChat, APIResponses:
		return nil
	}
	return fmt.Errorf("vercel-ai-gateway: gateway_api %q must be one of %s, %s, %s or %s", api, APIAuto, APIMessages, APIChat, APIResponses)
}

// surfaceFor resolves Config.API for model.
func surfaceFor(api, model string) string {
	switch api {
	case APIMessages, APIChat, APIResponses:
		return api
	}
	if IsAnthropicModel(model) {
		return SurfaceMessages
	}
	return SurfaceChat
}

// ResolveAPIKey returns explicit when set, else LOOM_LLM_VERCEL_GATEWAY_API_KEY,
// else AI_GATEWAY_API_KEY.
func ResolveAPIKey(explicit string) string {
	if k := strings.TrimSpace(explicit); k != "" {
		return k
	}
	if k := strings.TrimSpace(os.Getenv(EnvLoomAPIKey)); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv(EnvAPIKey))
}

// Config holds configuration for a gateway client.
type Config struct {
	// Endpoint is the gateway base URL. Empty = DefaultEndpoint. A regional
	// host (eu./us.ai-gateway.vercel.sh) goes here; a pasted surface URL is
	// accepted and reduced to the base (NormalizeEndpoint).
	Endpoint string

	// APIKey is the gateway API key. Required unless OIDC is set. Callers
	// resolve it with ResolveAPIKey.
	APIKey string

	// OIDC, when set and APIKey is empty, authenticates every request with a
	// Vercel OIDC token read fresh per request (see OIDCConfig). An API key
	// always wins: the gateway prefers the key when both are sent, even an
	// invalid one, so the client never sends both.
	OIDC *OIDCConfig

	// Model is the gateway model id ("creator/model"). Required: there is no
	// default, because a default would go stale with the gateway catalog.
	Model string

	// API selects the API surface (proto LLMConfig.gateway_api). "" or
	// APIAuto: Claude ids on the Messages API, every other id on Chat
	// Completions. APIMessages, APIChat or APIResponses send every id there;
	// the gateway translates between model families.
	API string

	MaxTokens   int     // Default: DefaultMaxTokens
	Temperature float64 // Default: DefaultTemperature

	// Timeout, when set, caps each model call as a whole (attempt and retry
	// together) and bounds the health check. Zero: model calls have no overall
	// cap, since the stream timeouts below catch a stall without cutting off a
	// long answer that keeps producing bytes; the health check uses
	// DefaultTimeout. looms.yaml llm.timeout_seconds sets it.
	Timeout time.Duration

	// StreamFirstByteTimeout and StreamIdleTimeout bound every model call
	// (Chat is streamed too). A call that exceeds either fails with an error
	// wrapping llm.ErrStreamTimeout and is retried once when nothing had been
	// passed to the caller yet. Defaults: DefaultStreamFirstByteTimeout,
	// DefaultStreamIdleTimeout. The first-byte wait is raised to cover the
	// longest Routing.ProviderTimeoutsMS plus 30 s, so the gateway can fail
	// over from a slow BYOK upstream before loom gives up. looms.yaml:
	// llm.vercel_gateway_stream_first_byte_timeout_seconds and
	// llm.vercel_gateway_stream_idle_timeout_seconds.
	StreamFirstByteTimeout time.Duration
	StreamIdleTimeout      time.Duration

	// ThinkingLevel requests extended thinking ("", "none" = off). Claude ids
	// take Anthropic thinking on the Messages surface; reasoning-capable
	// non-Claude ids take the gateway's reasoning object on the Chat surface.
	ThinkingLevel string

	// StrictToolSchemas is passed through to the inner client.
	StrictToolSchemas bool

	// Routing selects and orders upstream providers (providerOptions.gateway).
	// nil = gateway default routing.
	Routing *Routing

	// RateLimiterConfig is the client-side limiter. Unset numeric fields take
	// DefaultRateLimiterConfig values on both surfaces, so a model has the
	// same limits whichever inner client serves it.
	RateLimiterConfig llm.RateLimiterConfig

	// WrapTransport, when set, wraps the HTTP transport of both inner
	// clients and the health check (tests, request signing).
	WrapTransport func(http.RoundTripper) http.RoundTripper
}

// DefaultRateLimiterConfig is the limiter applied when the configuration
// leaves a field unset. The gateway does not rate limit paid-tier requests
// and publishes no free-tier limit, so these are loom's generic defaults
// (llm.DefaultRateLimiterConfig), not a gateway figure. Raise them for large
// fleets on a paid team.
func DefaultRateLimiterConfig() llm.RateLimiterConfig {
	return llm.DefaultRateLimiterConfig()
}

// mergeRateLimiter fills unset numeric fields of cfg from the defaults. It
// matters for Claude ids: the anthropic client would otherwise fill them with
// Anthropic's Tier 1 limits (0.7 req/s), throttling gateway traffic to
// direct-API levels.
func mergeRateLimiter(cfg llm.RateLimiterConfig) llm.RateLimiterConfig {
	d := DefaultRateLimiterConfig()
	if cfg.RequestsPerSecond <= 0 {
		cfg.RequestsPerSecond = d.RequestsPerSecond
	}
	if cfg.TokensPerMinute <= 0 {
		cfg.TokensPerMinute = d.TokensPerMinute
	}
	if cfg.BurstCapacity <= 0 {
		cfg.BurstCapacity = d.BurstCapacity
	}
	if cfg.MinDelay <= 0 {
		cfg.MinDelay = d.MinDelay
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = d.MaxRetries
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = d.RetryBackoff
	}
	if cfg.QueueTimeout <= 0 {
		cfg.QueueTimeout = d.QueueTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = d.Logger
	}
	return cfg
}

// Client implements types.StreamingLLMProvider and types.HealthChecker for
// one gateway model by delegating to an anthropic.Client (Claude ids) or an
// openai.Client (every other id).
type Client struct {
	model       string
	baseURL     string
	surface     string
	callTimeout time.Duration // 0 = no overall cap on a model call
	inner       types.StreamingLLMProvider
	health      *http.Client
	auth        func(*http.Request) error
}

// NewClient validates cfg and builds the client for cfg.Model.
func NewClient(cfg Config) (*Client, error) {
	if err := ValidateModelID(cfg.Model); err != nil {
		return nil, err
	}
	baseURL, err := NormalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if err := cfg.Routing.Validate(); err != nil {
		return nil, err
	}
	if err := ValidateAPI(cfg.API); err != nil {
		return nil, err
	}
	callTimeout := max(cfg.Timeout, 0)
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.StreamFirstByteTimeout <= 0 {
		cfg.StreamFirstByteTimeout = DefaultStreamFirstByteTimeout
	}
	if cfg.Routing != nil {
		for _, ms := range cfg.Routing.ProviderTimeoutsMS {
			if need := time.Duration(ms)*time.Millisecond + providerTimeoutMargin; need > cfg.StreamFirstByteTimeout {
				cfg.StreamFirstByteTimeout = need
			}
		}
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = DefaultStreamIdleTimeout
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = DefaultTemperature
	}
	if cfg.RateLimiterConfig.Enabled {
		cfg.RateLimiterConfig = mergeRateLimiter(cfg.RateLimiterConfig)
	}

	apiKey := strings.TrimSpace(cfg.APIKey)
	wrap := cfg.WrapTransport
	pinned, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("vercel-ai-gateway: parse endpoint: %w", err)
	}
	var auth func(*http.Request) error
	switch {
	case apiKey != "":
		auth = func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+apiKey)
			return nil
		}
	case cfg.OIDC != nil:
		src, err := newOIDCSource(*cfg.OIDC)
		if err != nil {
			return nil, err
		}
		auth = src.authorize
		inner := wrap
		wrap = func(base http.RoundTripper) http.RoundTripper {
			if inner != nil {
				base = inner(base)
			}
			return &authTransport{base: base, authorize: src.authorize}
		}
	default:
		return nil, fmt.Errorf("vercel-ai-gateway: no credential: set an API key (keyring vercel_gateway_api_key, %s or %s) or enable OIDC", EnvLoomAPIKey, EnvAPIKey)
	}

	// Every model request carries a pseudonymous loom session id, so the
	// gateway keeps a conversation on one upstream (prompt-cache hits) without
	// relying on its own prompt fingerprint.
	// With session_affinity: false the header is not sent at all.
	if cfg.Routing == nil || cfg.Routing.SessionAffinity == nil || *cfg.Routing.SessionAffinity {
		sessWrap := wrap
		wrap = func(base http.RoundTripper) http.RoundTripper {
			if sessWrap != nil {
				base = sessWrap(base)
			}
			return &sessionTransport{base: base}
		}
	}

	// Every request, on every surface and the health check, must go to the
	// configured gateway host. A redirect elsewhere is refused before it is
	// sent: Go strips Authorization on a cross-host redirect but keeps
	// x-api-key, and the OIDC transport would re-add the bearer token.
	authWrap := wrap
	wrap = func(base http.RoundTripper) http.RoundTripper {
		if authWrap != nil {
			base = authWrap(base)
		}
		return &hostPinTransport{scheme: pinned.Scheme, host: pinned.Host, base: base}
	}

	extra, err := cfg.Routing.ResolveExtraBody(nil)
	if err != nil {
		return nil, err
	}
	c := &Client{model: cfg.Model, baseURL: baseURL, auth: auth, callTimeout: callTimeout}

	c.surface = surfaceFor(cfg.API, cfg.Model)
	switch c.surface {
	case SurfaceMessages:
		if err := anthropic.ValidateExtraBody(extra); err != nil {
			return nil, fmt.Errorf("vercel-ai-gateway: %w", err)
		}
		c.inner = anthropic.NewClient(anthropic.Config{
			StrictToolSchemas:      cfg.StrictToolSchemas,
			APIKey:                 apiKey, // sent as x-api-key; empty under OIDC (the transport adds Bearer)
			Model:                  cfg.Model,
			Endpoint:               baseURL + MessagesPath,
			Timeout:                cfg.Timeout,
			StreamFirstByteTimeout: cfg.StreamFirstByteTimeout,
			StreamIdleTimeout:      cfg.StreamIdleTimeout,
			MaxTokens:              cfg.MaxTokens,
			Temperature:            cfg.Temperature,
			ThinkingLevel:          cfg.ThinkingLevel,
			RateLimiterConfig:      cfg.RateLimiterConfig,
			// The only beta loom sends is the obsolete prompt-caching one;
			// cache_control works without it, and an Anthropic-compatible
			// gateway may reject beta values it does not forward.
			NoBetaHeaders:     true,
			CatalogProvider:   CatalogProvider,
			WrapTransport:     wrap,
			ExtraBody:         extra,
			ResponseInspector: newInspector(cfg.Model),
		})
	case SurfaceResponses:
		if err := openairesponses.ValidateExtraBody(extra); err != nil {
			return nil, fmt.Errorf("vercel-ai-gateway: %w", err)
		}
		c.inner = openairesponses.NewClient(openairesponses.Config{
			StrictToolSchemas:      cfg.StrictToolSchemas,
			APIKey:                 apiKey,
			Model:                  cfg.Model,
			Endpoint:               baseURL + ResponsesPath,
			Timeout:                cfg.Timeout,
			StreamFirstByteTimeout: cfg.StreamFirstByteTimeout,
			StreamIdleTimeout:      cfg.StreamIdleTimeout,
			MaxTokens:              cfg.MaxTokens,
			Temperature:            cfg.Temperature,
			ThinkingLevel:          cfg.ThinkingLevel,
			RateLimiterConfig:      cfg.RateLimiterConfig,
			CatalogProvider:        CatalogProvider,
			WrapTransport:          wrap,
			ExtraBody:              extra,
			ResponseInspector:      newInspector(cfg.Model),
			ReportUnpriced:         true,
		})
	default:
		if err := openai.ValidateExtraBody(extra); err != nil {
			return nil, fmt.Errorf("vercel-ai-gateway: %w", err)
		}
		c.inner = openai.NewClient(openai.Config{
			StrictToolSchemas:      cfg.StrictToolSchemas,
			APIKey:                 apiKey,
			Model:                  cfg.Model,
			Endpoint:               baseURL + ChatCompletionsPath,
			Timeout:                cfg.Timeout,
			StreamFirstByteTimeout: cfg.StreamFirstByteTimeout,
			StreamIdleTimeout:      cfg.StreamIdleTimeout,
			MaxTokens:              cfg.MaxTokens,
			Temperature:            cfg.Temperature,
			ThinkingLevel:          cfg.ThinkingLevel,
			ReasoningObject:        true,
			RateLimiterConfig:      cfg.RateLimiterConfig,
			CatalogProvider:        CatalogProvider,
			WrapTransport:          wrap,
			ExtraBody:              extra,
			ResponseInspector:      newInspector(cfg.Model),
			ReportUnpriced:         true,
		})
	}

	healthTransport := http.RoundTripper(http.DefaultTransport)
	if cfg.WrapTransport != nil {
		healthTransport = cfg.WrapTransport(healthTransport)
	}
	healthTransport = &hostPinTransport{scheme: pinned.Scheme, host: pinned.Host, base: healthTransport}
	c.health = &http.Client{Timeout: cfg.Timeout, Transport: healthTransport}
	return c, nil
}

// Name returns the provider identifier.
func (c *Client) Name() string { return ProviderName }

// Model returns the configured gateway model id.
func (c *Client) Model() string { return c.model }

// BaseURL returns the gateway base URL in use.
func (c *Client) BaseURL() string { return c.baseURL }

// Surface returns the API surface this model is sent on: Config.API when
// set, else SurfaceMessages for Claude ids and SurfaceChat for every other id.
func (c *Client) Surface() string { return c.surface }

// Endpoint returns the full URL the inner client posts to.
func (c *Client) Endpoint() string {
	switch c.surface {
	case SurfaceMessages:
		return c.baseURL + MessagesPath
	case SurfaceResponses:
		return c.baseURL + ResponsesPath
	}
	return c.baseURL + ChatCompletionsPath
}

// Chat sends a conversation through the gateway. It is served over a stream
// with no token callback: a blocking request gives no sign of life until the
// whole answer is ready, so a stalled upstream could only be told apart from
// a long answer by the overall timeout. Streaming lets the first-byte and
// idle timeouts catch a stall within seconds (one stalled Sonnet call held an
// agent for the full 300 s in the 2026-11-18 evaluation).
func (c *Client) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return c.ChatStream(ctx, messages, tools, nil)
}

// ChatStream streams a conversation through the gateway. A stream that stalls
// (llm.ErrStreamTimeout) or is cut off (llm.ErrStreamTruncated) before any
// token reached tokenCallback is retried once, and the response carries
// gateway.retried; after a token has been delivered a retry would repeat text
// the caller already has, so the error is returned.
func (c *Client) ChatStream(ctx context.Context, messages []types.Message, tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*types.LLMResponse, error) {
	var delivered atomic.Bool
	cb := tokenCallback
	if tokenCallback != nil {
		cb = func(token string) {
			delivered.Store(true)
			tokenCallback(token)
		}
	}
	if c.callTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.callTimeout)
		defer cancel()
	}
	resp, err := c.inner.ChatStream(ctx, messages, tools, cb)
	retried := ""
	if err != nil && !delivered.Load() && ctx.Err() == nil {
		switch {
		case errors.Is(err, llm.ErrStreamTimeout):
			retried = "stall"
		case errors.Is(err, llm.ErrStreamTruncated):
			retried = "truncated"
		}
	}
	if retried != "" {
		// Nothing reached the caller, so a second attempt repeats nothing. The
		// first attempt's tokens, if the upstream billed any, are not in Usage.
		zap.L().Warn("vercel-ai-gateway: stream failed before any output; retrying once",
			zap.String("model", c.model), zap.String("surface", c.surface),
			zap.String("reason", retried), zap.Error(err))
		resp, err = c.inner.ChatStream(ctx, messages, tools, cb)
	}
	resp, err = c.finish(resp, err)
	if resp != nil && retried != "" {
		resp.Metadata[AttrRetried] = retried
	}
	return resp, err
}

func (c *Client) finish(resp *types.LLMResponse, err error) (*types.LLMResponse, error) {
	if err != nil {
		return nil, classifyError(err)
	}
	if resp != nil {
		if resp.Metadata == nil {
			resp.Metadata = make(map[string]interface{}, 1)
		}
		resp.Metadata[AttrEndpointSurface] = c.surface
	}
	return resp, nil
}

// ErrCreditsExhausted marks a 402 from the gateway: credits exhausted, a
// budget exceeded, or BYOK without a positive credit balance. It is a
// billing condition, not an authentication failure, and is never retried.
var ErrCreditsExhausted = errors.New("vercel-ai-gateway: payment required (credits exhausted, budget exceeded, or BYOK without a positive credit balance); top up or raise the budget in the Vercel dashboard")

// classifyError gives a 402 a message that names credits. The status stays
// attached, so llm.IsDeterministicClientError still treats it as terminal.
func classifyError(err error) error {
	var sc llm.HTTPStatusCoder
	if errors.As(err, &sc) && sc.HTTPStatusCode() == http.StatusPaymentRequired {
		return llm.WrapHTTPStatus(http.StatusPaymentRequired, fmt.Errorf("%w: %w", ErrCreditsExhausted, err))
	}
	return err
}

// Credits is the GET /v1/credits response.
type Credits struct {
	// Balance is the remaining USD credit balance (excludes Token Pass).
	Balance string `json:"balance"`
	// TotalUsed is lifetime USD spend.
	TotalUsed string `json:"total_used"`
}

// HealthCheck verifies the endpoint and the credential with GET /v1/credits.
// It spends no tokens. GET /v1/models is not used: it needs no
// authentication, so it would pass with a bad key.
func (c *Client) HealthCheck(ctx context.Context) error {
	_, err := c.Credits(ctx)
	return err
}

// Credits fetches the account's credit balance.
func (c *Client) Credits(ctx context.Context) (*Credits, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+CreditsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("vercel-ai-gateway: create credits request: %w", err)
	}
	if err := c.auth(req); err != nil {
		return nil, err
	}
	resp, err := c.health.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vercel-ai-gateway: health check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, healthBodyLimit))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, classifyError(llm.WrapHTTPStatus(resp.StatusCode,
			fmt.Errorf("vercel-ai-gateway: health check failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))))
	}
	var credits Credits
	if err := json.Unmarshal(body, &credits); err != nil {
		return nil, fmt.Errorf("vercel-ai-gateway: decode credits: %w", err)
	}
	return &credits, nil
}

// HeaderSessionID is the gateway's session-affinity header.
const HeaderSessionID = "x-session-id"

// sessionTransport sets HeaderSessionID from the loom session on the request
// context. The id is a SHA-256 digest of the loom session id, so the gateway
// sees a stable key per conversation but never loom's own identifier. The
// gateway hashes ids over 256 characters itself; this one is 32.
type sessionTransport struct {
	base http.RoundTripper
}

// RoundTrip adds the header when the context carries a session.
func (t *sessionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if id := session.SessionIDFromContext(r.Context()); id != "" && r.Header.Get(HeaderSessionID) == "" {
		r = r.Clone(r.Context())
		r.Header.Set(HeaderSessionID, gatewaySessionID(id))
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// sessionSalt is random per process, so the id sent to the gateway cannot be
// matched to a loom session id by hashing candidates. Affinity only needs the
// id to be stable for minutes (the gateway pins for at most an hour), so a new
// salt after a restart costs at most one re-pin.
var sessionSalt = func() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("vercel-ai-gateway: crypto/rand: " + err.Error())
	}
	return b
}()

// gatewaySessionID is the pseudonymous id sent for a loom session: an
// HMAC-SHA-256 of the loom session id under the per-process salt.
func gatewaySessionID(loomSessionID string) string {
	m := hmac.New(sha256.New, sessionSalt)
	m.Write([]byte(loomSessionID))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// hostPinTransport refuses any request that is not to the configured gateway
// scheme and host (a redirect to another host, or a downgrade to http), so a
// credential never leaves for a host the operator did not configure.
type hostPinTransport struct {
	scheme, host string
	base         http.RoundTripper
}

// RoundTrip enforces the pin, then delegates.
func (t *hostPinTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.EqualFold(r.URL.Scheme, t.scheme) || !strings.EqualFold(r.URL.Host, t.host) {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, fmt.Errorf("vercel-ai-gateway: refusing request to %s://%s: not the configured gateway host %s://%s (a redirect?)", r.URL.Scheme, r.URL.Host, t.scheme, t.host)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// Ensure Client implements the provider interfaces.
var (
	_ types.StreamingLLMProvider = (*Client)(nil)
	_ llmtypes.HealthChecker     = (*Client)(nil)
)
