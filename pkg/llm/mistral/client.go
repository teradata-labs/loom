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
package mistral

import (
	"context"
	"fmt"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Client implements the LLMProvider interface for Mistral AI.
// Mistral AI uses an OpenAI-compatible API, so we wrap the OpenAI client.
type Client struct {
	openai *openai.Client
	model  string
}

// Config holds configuration for the Mistral AI client.
type Config struct {
	// Required: Mistral API key from https://console.mistral.ai/
	APIKey string

	// Model to use (default: "mistral-large-latest")
	// Available models:
	// - open-mistral-7b: 7B open model
	// - open-mixtral-8x7b: 8x7B MoE model
	// - open-mixtral-8x22b: 8x22B MoE model
	// - mistral-small-latest: Latest small model
	// - mistral-medium-latest: Latest medium model (deprecated)
	// - mistral-large-latest: Latest large model (recommended)
	Model string

	// Optional configuration
	MaxTokens         int           // Default: 4096
	Temperature       float64       // Default: 1.0
	Timeout           time.Duration // Default: 60s
	RateLimiterConfig llm.RateLimiterConfig
}

// NewClient creates a new Mistral AI client.
// Mistral uses an OpenAI-compatible API at https://api.mistral.ai/v1/chat/completions
func NewClient(config Config) *Client {
	// Set defaults
	if config.Model == "" {
		config.Model = "mistral-large-latest"
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 4096
	}
	if config.Temperature == 0 {
		config.Temperature = 1.0
	}
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}

	// Create OpenAI client with Mistral endpoint
	openaiClient := openai.NewClient(openai.Config{
		APIKey:            config.APIKey,
		Model:             config.Model,
		Endpoint:          "https://api.mistral.ai/v1/chat/completions",
		MaxTokens:         config.MaxTokens,
		Temperature:       config.Temperature,
		Timeout:           config.Timeout,
		RateLimiterConfig: config.RateLimiterConfig,
	})

	return &Client{
		openai: openaiClient,
		model:  config.Model,
	}
}

// Name returns the provider name.
func (c *Client) Name() string {
	return "mistral"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.model
}

// Chat sends a conversation to Mistral AI and returns the response.
// This delegates to the OpenAI client since Mistral uses the same API format.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Delegate to OpenAI client (API-compatible)
	resp, err := c.openai.Chat(ctx, messages, tools)
	if err != nil {
		return nil, err
	}

	// Recalculate cost using Mistral pricing
	resp.Usage.CostUSD = c.calculateCost(resp.Usage.InputTokens, resp.Usage.OutputTokens)

	// Update metadata to reflect Mistral provider
	if resp.Metadata == nil {
		resp.Metadata = make(map[string]interface{})
	}
	resp.Metadata["provider"] = "mistral"

	return resp, nil
}

// calculateCost estimates the cost in USD based on token usage.
// Pricing as of 2024-11 (approximate, per million tokens):
//
// Open Models (free/permissive):
// - open-mistral-7b: $0.25 / $0.25
// - open-mixtral-8x7b: $0.70 / $0.70
// - open-mixtral-8x22b: $2.00 / $6.00
//
// Commercial Models:
// - mistral-small-latest: $1.00 / $3.00
// - mistral-medium-latest: $2.70 / $8.10
// - mistral-large-latest: $4.00 / $12.00
//
// Note: Prices may vary. Check https://mistral.ai/technology/#pricing for current rates.
func (c *Client) calculateCost(inputTokens, outputTokens int) float64 {
	var inputCostPerM, outputCostPerM float64

	switch c.model {
	case "open-mistral-7b", "mistral-tiny-2312": // Legacy tiny
		inputCostPerM = 0.25
		outputCostPerM = 0.25

	case "open-mixtral-8x7b", "mistral-small-2312": // Legacy small
		inputCostPerM = 0.70
		outputCostPerM = 0.70

	case "open-mixtral-8x22b":
		inputCostPerM = 2.00
		outputCostPerM = 6.00

	case "mistral-small-latest", "mistral-small-2402":
		inputCostPerM = 1.00
		outputCostPerM = 3.00

	case "mistral-medium-latest", "mistral-medium-2312": // Deprecated
		inputCostPerM = 2.70
		outputCostPerM = 8.10

	case "mistral-large-latest", "mistral-large-2402", "mistral-large-2407":
		inputCostPerM = 4.00
		outputCostPerM = 12.00

	default:
		// Default to large pricing for unknown models
		inputCostPerM = 4.00
		outputCostPerM = 12.00
	}

	inputCost := float64(inputTokens) * inputCostPerM / 1_000_000
	outputCost := float64(outputTokens) * outputCostPerM / 1_000_000
	return inputCost + outputCost
}

// ChatStream implements token-by-token streaming for Mistral AI.
// Since Mistral uses an OpenAI-compatible API, this delegates directly to the OpenAI client's ChatStream.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// Delegate to OpenAI client (API-compatible)
	// The OpenAI client implements StreamingLLMProvider
	streamProvider, ok := interface{}(c.openai).(llmtypes.StreamingLLMProvider)
	if !ok {
		return nil, fmt.Errorf("OpenAI client does not support streaming")
	}

	resp, err := streamProvider.ChatStream(ctx, messages, tools, tokenCallback)
	if err != nil {
		return nil, err
	}

	// Recalculate cost using Mistral pricing
	resp.Usage.CostUSD = c.calculateCost(resp.Usage.InputTokens, resp.Usage.OutputTokens)

	// Update metadata to reflect Mistral provider
	if resp.Metadata == nil {
		resp.Metadata = make(map[string]interface{})
	}
	resp.Metadata["provider"] = "mistral"

	return resp, nil
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
