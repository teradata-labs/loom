// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package litellm provides an LLM provider that routes requests through a
// LiteLLM proxy (https://litellm.ai). The proxy exposes an OpenAI-compatible
// /chat/completions endpoint and forwards requests to any upstream provider
// (Anthropic, Bedrock, Azure, Gemini, Ollama, …) based on the model name.
//
// Configuration example (looms.yaml):
//
//	llm:
//	  provider: litellm
//	  litellm_endpoint: http://litellm:4000/v1/chat/completions
//	  litellm_model: anthropic/claude-sonnet-4-5-20250929
//	  litellm_api_key: sk-...   # optional – set via LITELLM_API_KEY env var
package litellm

import (
	"context"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

const (
	// DefaultEndpoint is the default LiteLLM proxy address when running
	// in-cluster via the standard Kubernetes service name.
	DefaultEndpoint    = "http://litellm:4000/v1/chat/completions"
	DefaultModel       = "anthropic/claude-sonnet-4-5-20250929"
	DefaultTimeout     = 120 * time.Second
	DefaultMaxTokens   = 4096
	DefaultTemperature = 1.0
)

// Config holds configuration for the LiteLLM client.
type Config struct {
	// Endpoint is the full URL of the LiteLLM proxy chat-completions endpoint.
	// Defaults to http://litellm:4000/v1/chat/completions.
	Endpoint string

	// APIKey is the LiteLLM virtual key (LITELLM_API_KEY). Optional when the
	// proxy is configured to allow unauthenticated requests.
	APIKey string

	// Model is the LiteLLM model routing string, e.g.
	//   "anthropic/claude-sonnet-4-5-20250929"
	//   "azure/gpt-4o-deployment"
	//   "bedrock/anthropic.claude-3-5-sonnet"
	//   "ollama/llama3.2"
	Model string

	MaxTokens   int
	Temperature float64
	Timeout     time.Duration

	RateLimiterConfig llm.RateLimiterConfig
}

// Client implements types.LLMProvider and types.StreamingLLMProvider by
// delegating to an openai.Client pointed at the LiteLLM proxy endpoint.
type Client struct {
	inner *openai.Client
	model string
}

// NewClient creates a new LiteLLM client.
//
// The Endpoint may be either a full chat-completions URL
// (http://host:4000/v1/chat/completions) or a bare base URL
// (http://host:4000 / http://host:4000/v1). The latter is what
// avmo-tera-cloud injects via LOOM_LLM_LITELLM_ENDPOINT — this function
// normalises it to the full path automatically.
func NewClient(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	} else {
		cfg.Endpoint = normalizeEndpoint(cfg.Endpoint)
	}
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

	inner := openai.NewClient(openai.Config{
		APIKey:            cfg.APIKey,
		Model:             cfg.Model,
		Endpoint:          cfg.Endpoint,
		Timeout:           cfg.Timeout,
		MaxTokens:         cfg.MaxTokens,
		Temperature:       cfg.Temperature,
		RateLimiterConfig: cfg.RateLimiterConfig,
	})

	return &Client{inner: inner, model: cfg.Model}
}

// Name returns the provider identifier.
func (c *Client) Name() string { return "litellm" }

// Model returns the configured model routing string.
func (c *Client) Model() string { return c.model }

// Chat sends a conversation to the LiteLLM proxy and returns the response.
func (c *Client) Chat(ctx context.Context, messages []types.Message, tools []shuttle.Tool) (*types.LLMResponse, error) {
	return c.inner.Chat(ctx, messages, tools)
}

// ChatStream streams tokens from the LiteLLM proxy as they are generated.
func (c *Client) ChatStream(ctx context.Context, messages []types.Message, tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*types.LLMResponse, error) {
	return c.inner.ChatStream(ctx, messages, tools, tokenCallback)
}

// HealthCheck verifies the proxy is reachable via a minimal OPTIONS/GET probe.
// Delegates to the inner openai.Client health check.
func (c *Client) HealthCheck(ctx context.Context) error {
	if hc, ok := any(c.inner).(interface {
		HealthCheck(context.Context) error
	}); ok {
		return hc.HealthCheck(ctx)
	}
	return nil
}

// normalizeEndpoint ensures the endpoint is the full chat-completions path.
// avmo-tera-cloud injects LOOM_LLM_LITELLM_ENDPOINT as a bare base URL
// (e.g. "http://litellm:4000"), so we append the standard path when it is absent.
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(endpoint, "/chat/completions") {
		return endpoint
	}
	if strings.HasSuffix(endpoint, "/v1") {
		return endpoint + "/chat/completions"
	}
	return endpoint + "/v1/chat/completions"
}
