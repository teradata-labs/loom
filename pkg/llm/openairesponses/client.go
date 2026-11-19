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

package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// Defaults. The model, timeouts, token cap and temperature are the
// pkg/llm/openai defaults, so the two OpenAI clients behave alike when a
// field is left unset.
const (
	// ProviderName is what Client.Name returns.
	ProviderName = "openai-responses"
	// DefaultEndpoint is OpenAI's own Responses endpoint.
	DefaultEndpoint = "https://api.openai.com/v1/responses"
	// DefaultCatalogProvider is the catalog namespace used for pricing when
	// Config.CatalogProvider is empty.
	DefaultCatalogProvider = openai.DefaultCatalogProvider

	DefaultModel                  = openai.DefaultOpenAIModel
	DefaultTimeout                = openai.DefaultOpenAITimeout
	DefaultStreamFirstByteTimeout = openai.DefaultOpenAIStreamFirstByteTimeout
	DefaultStreamIdleTimeout      = openai.DefaultOpenAIStreamIdleTimeout
	DefaultMaxTokens              = openai.DefaultOpenAIMaxTokens
	DefaultTemperature            = openai.DefaultOpenAITemperature
)

// Config holds the client configuration. Fields mirror openai.Config where
// the two APIs agree.
type Config struct {
	// StrictToolSchemas makes a tool whose schema cannot be sent fail the
	// request instead of being omitted with a warning (llm.ToolSchemaPolicy).
	// It does not set the wire "strict" flag, which is always false.
	StrictToolSchemas bool
	APIKey            string
	Model             string        // Default: DefaultModel
	Endpoint          string        // Full URL to /v1/responses. Default: DefaultEndpoint
	Timeout           time.Duration // Blocking-call timeout. Default: DefaultTimeout
	// StreamFirstByteTimeout limits the wait for the first response-body
	// bytes in ChatStream; StreamIdleTimeout limits silence between later
	// reads. Non-positive values use the defaults.
	StreamFirstByteTimeout time.Duration
	StreamIdleTimeout      time.Duration
	// MaxTokens is sent as max_output_tokens. Reasoning tokens count against
	// it. Default: DefaultMaxTokens.
	MaxTokens int
	// Temperature is sent when reasoning is off. Default: DefaultTemperature.
	Temperature float64
	// ThinkingLevel: "" or "none" = off; "low", "medium", "high" = that
	// reasoning effort; "auto" = "medium".
	ThinkingLevel     string
	RateLimiterConfig llm.RateLimiterConfig
	// CatalogProvider is the catalog namespace used to price responses.
	// Default: DefaultCatalogProvider.
	CatalogProvider string
	// WrapTransport, when set, wraps the blocking and the streaming
	// transports after the client has tuned them.
	WrapTransport func(http.RoundTripper) http.RoundTripper
	// ExtraBody adds top-level members to every request body. A key that
	// names a request field, or previous_response_id, is rejected; check with
	// ValidateExtraBody before NewClient to fail at construction, otherwise
	// every call returns the validation error.
	ExtraBody llm.ExtraBody
	// ResponseInspector, when set, reads gateway cost and routing facts from
	// the blocking body and from every SSE data payload. A cost it reports
	// wins over the local estimate; its attributes go into
	// LLMResponse.Metadata.
	ResponseInspector llm.ResponseInspector
	// ReportUnpriced: when no cost is reported and the catalog has no rate
	// for the model, estimate the cost at the median rate of the same
	// creator's catalog models and flag it llm.cost_estimated. Without it the
	// cost of such a call is 0. Either way the call is flagged
	// llm.cost_unpriced. This client never guesses a fixed default rate.
	ReportUnpriced bool
	// ExtraHeaders are sent with every request, after Content-Type and
	// Authorization (so the same name overrides those).
	ExtraHeaders map[string]string
}

// Client is a Responses API client. It is safe for concurrent use: all
// per-request state (the tool name map) lives on the call stack.
type Client struct {
	schemaPolicy           llm.ToolSchemaPolicy
	strictToolSchemas      bool
	apiKey                 string
	model                  string
	endpoint               string
	httpClient             *http.Client
	streamHTTPClient       *http.Client
	streamFirstByteTimeout time.Duration
	streamIdleTimeout      time.Duration
	maxTokens              int
	temperature            float64
	thinkingLevel          string
	rateLimiter            *llm.RateLimiter
	extraHeaders           map[string]string
	catalogProvider        string
	extraBody              []byte
	extraBodyErr           error
	inspector              llm.ResponseInspector
	reportUnpriced         bool
}

// requestFields are the reserved ExtraBody keys: every member
// responsesRequest writes, plus previous_response_id, which this client
// never sends (the gateway rejects it with gen_ ids, and the client replays
// the whole conversation instead).
var requestFields = func() map[string]struct{} {
	names := llm.JSONFieldNames(responsesRequest{})
	names["previous_response_id"] = struct{}{}
	return names
}()

// ValidateExtraBody reports whether extra can be sent by this client: no key
// may name a field the client writes itself (or previous_response_id), and
// every value must marshal. Callers use it to fail at configuration time.
func ValidateExtraBody(extra llm.ExtraBody) error {
	_, err := llm.ValidateExtraBody(extra, requestFields)
	return err
}

// NewClient creates a client, applying defaults to unset fields. An invalid
// ExtraBody is logged here and returned by every call.
func NewClient(config Config) *Client {
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if config.Endpoint == "" {
		config.Endpoint = DefaultEndpoint
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultTimeout
	}
	if config.StreamFirstByteTimeout <= 0 {
		config.StreamFirstByteTimeout = DefaultStreamFirstByteTimeout
	}
	if config.StreamIdleTimeout <= 0 {
		config.StreamIdleTimeout = DefaultStreamIdleTimeout
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = DefaultMaxTokens
	}
	if config.Temperature == 0 {
		config.Temperature = DefaultTemperature
	}
	if config.CatalogProvider == "" {
		config.CatalogProvider = DefaultCatalogProvider
	}

	httpTransport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: config.Timeout,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          100,
		// Short idle timeout: a proxy that closed its side hands out EOFs.
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConnsPerHost: 5,
	}
	httpClient := &http.Client{Timeout: config.Timeout, Transport: httpTransport}
	streamTransport := httpTransport.Clone()
	streamTransport.ResponseHeaderTimeout = config.StreamFirstByteTimeout
	streamHTTPClient := &http.Client{Transport: streamTransport}
	if config.WrapTransport != nil {
		httpClient.Transport = config.WrapTransport(httpTransport)
		streamHTTPClient.Transport = config.WrapTransport(streamTransport)
	}

	// Keyed by credential+endpoint+model so clients with independent quotas
	// do not throttle each other.
	var rateLimiter *llm.RateLimiter
	if config.RateLimiterConfig.Enabled {
		scope := ProviderName + "|" + llm.CredentialScope(config.APIKey) + "|" + config.Endpoint + "|" + config.Model
		rateLimiter = llm.SharedRateLimiter(scope, config.RateLimiterConfig)
	}

	extraBody, extraBodyErr := llm.ValidateExtraBody(config.ExtraBody, requestFields)
	if extraBodyErr != nil {
		zap.L().Error("openairesponses: invalid ExtraBody; every request from this client will fail",
			zap.String("endpoint", config.Endpoint), zap.Error(extraBodyErr))
	}

	var headers map[string]string
	if len(config.ExtraHeaders) > 0 {
		headers = maps.Clone(config.ExtraHeaders)
	}

	return &Client{
		strictToolSchemas:      config.StrictToolSchemas,
		apiKey:                 config.APIKey,
		model:                  config.Model,
		endpoint:               config.Endpoint,
		httpClient:             httpClient,
		streamHTTPClient:       streamHTTPClient,
		streamFirstByteTimeout: config.StreamFirstByteTimeout,
		streamIdleTimeout:      config.StreamIdleTimeout,
		maxTokens:              config.MaxTokens,
		temperature:            config.Temperature,
		thinkingLevel:          config.ThinkingLevel,
		rateLimiter:            rateLimiter,
		extraHeaders:           headers,
		catalogProvider:        config.CatalogProvider,
		extraBody:              extraBody,
		extraBodyErr:           extraBodyErr,
		inspector:              config.ResponseInspector,
		reportUnpriced:         config.ReportUnpriced,
	}
}

// Name returns the provider name.
func (c *Client) Name() string { return ProviderName }

// Model returns the model identifier.
func (c *Client) Model() string { return c.model }

// Chat sends the conversation and returns the parsed response (blocking).
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	nameMap := make(map[string]string)
	req, err := c.buildRequest(ctx, messages, tools, nameMap, false)
	if err != nil {
		return nil, err
	}
	body, err := c.marshalRequest(req)
	if err != nil {
		return nil, err
	}
	httpResp, err := c.sendRequest(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if err := checkStatus(httpResp); err != nil {
		return nil, err
	}
	respBody, err := readAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: read response: %w", err)
	}
	var resp responseObject
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("openairesponses: decode response: %w", err)
	}
	facts := c.inspect(httpResp.Header, respBody)
	out, err := c.convertResponse(&resp, nameMap, facts.CostUSD)
	if err != nil {
		return nil, err
	}
	out.Metadata = facts.Apply(out.Metadata)
	c.recordTokens(&resp)
	return out, nil
}

// marshalRequest encodes req and appends Config.ExtraBody.
func (c *Client) marshalRequest(req *responsesRequest) ([]byte, error) {
	if c.extraBodyErr != nil {
		return nil, fmt.Errorf("openairesponses: %w", c.extraBodyErr)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: marshal request: %w", err)
	}
	return llm.MergeExtraBody(body, c.extraBody)
}

// inspect runs the configured ResponseInspector; zero facts without one.
func (c *Client) inspect(h http.Header, payload []byte) llm.ResponseFacts {
	if c.inspector == nil {
		return llm.ResponseFacts{}
	}
	return c.inspector(h, payload)
}

// recordTokens feeds the raw token total to the rate limiter's usage metric.
func (c *Client) recordTokens(resp *responseObject) {
	if c.rateLimiter == nil || resp == nil || resp.Usage == nil {
		return
	}
	c.rateLimiter.RecordTokenUsage(int64(meteredTokens(resp.Usage)))
}

// Interface checks.
var (
	_ llmtypes.LLMProvider          = (*Client)(nil)
	_ llmtypes.StreamingLLMProvider = (*Client)(nil)
)
