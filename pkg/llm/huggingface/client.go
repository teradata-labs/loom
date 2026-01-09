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
package huggingface

import (
	"context"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Client implements the LLMProvider interface for HuggingFace Inference API.
// HuggingFace uses an OpenAI-compatible API, so we wrap the OpenAI client.
type Client struct {
	openai *openai.Client
	model  string
}

// Config holds configuration for the HuggingFace client.
type Config struct {
	// Required: HuggingFace token from https://huggingface.co/settings/tokens
	// Note: This is a "token" not an "API key" in HuggingFace terminology
	Token string

	// Model to use (default: "meta-llama/Meta-Llama-3.1-70B-Instruct")
	// Available models (examples):
	// - meta-llama/Meta-Llama-3.1-70B-Instruct: Llama 3.1 70B (recommended)
	// - meta-llama/Meta-Llama-3.1-8B-Instruct: Llama 3.1 8B (faster)
	// - mistralai/Mixtral-8x7B-Instruct-v0.1: Mixtral 8x7B
	// - google/gemma-2-9b-it: Gemma 2 9B
	// - Qwen/Qwen2.5-72B-Instruct: Qwen 2.5 72B
	// - Many more available at https://huggingface.co/models
	Model string

	// Optional configuration
	MaxTokens         int           // Default: 4096
	Temperature       float64       // Default: 1.0
	Timeout           time.Duration // Default: 60s
	RateLimiterConfig llm.RateLimiterConfig
}

// NewClient creates a new HuggingFace client.
// HuggingFace uses an OpenAI-compatible API at https://router.huggingface.co/v1
func NewClient(config Config) *Client {
	// Set defaults
	if config.Model == "" {
		config.Model = "meta-llama/Meta-Llama-3.1-70B-Instruct"
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

	// Create OpenAI client with HuggingFace endpoint
	// Note: We use the full chat/completions path since OpenAI client expects it
	openaiClient := openai.NewClient(openai.Config{
		APIKey:            config.Token, // HuggingFace calls it a "token" but it works as API key
		Model:             config.Model,
		Endpoint:          "https://router.huggingface.co/v1/chat/completions",
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
	return "huggingface"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.model
}

// Chat sends a conversation to HuggingFace and returns the response.
// This delegates to the OpenAI client since HuggingFace uses the same API format.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Delegate to OpenAI client (API-compatible)
	resp, err := c.openai.Chat(ctx, messages, tools)
	if err != nil {
		return nil, err
	}

	// Recalculate cost using HuggingFace pricing
	// Note: HuggingFace pricing varies significantly by provider (Together, Cohere, Groq, etc.)
	// We use generic estimates here. For accurate pricing, consult specific provider docs.
	resp.Usage.CostUSD = c.calculateCost(resp.Usage.InputTokens, resp.Usage.OutputTokens)

	// Update metadata to reflect HuggingFace provider
	if resp.Metadata == nil {
		resp.Metadata = make(map[string]interface{})
	}
	resp.Metadata["provider"] = "huggingface"

	return resp, nil
}

// calculateCost estimates the cost in USD based on token usage.
//
// NOTE: HuggingFace pricing varies significantly depending on the backend provider:
// - Together AI: ~$0.60-$0.90 per 1M tokens
// - Cohere: ~$1.00-$15.00 per 1M tokens
// - Groq: Free tier available, varies by model
// - Self-hosted: No API costs (compute costs only)
//
// Since pricing is highly variable and model-specific, we use conservative estimates.
// For accurate pricing, check:
// - https://huggingface.co/pricing
// - Individual model provider pricing pages
//
// Pricing estimates (per million tokens, input/output):
// - Llama 3.1 70B: $0.80 / $0.80 (Together AI)
// - Llama 3.1 8B: $0.20 / $0.20 (Together AI)
// - Mixtral 8x7B: $0.60 / $0.60 (Together AI)
// - Qwen 2.5 72B: $0.80 / $0.80 (estimated)
// - Default: $1.00 / $1.00 (conservative estimate)
func (c *Client) calculateCost(inputTokens, outputTokens int) float64 {
	var inputCostPerM, outputCostPerM float64

	switch c.model {
	// Llama models (via Together AI)
	case "meta-llama/Meta-Llama-3.1-70B-Instruct",
		"meta-llama/Llama-3.1-70B-Instruct":
		inputCostPerM = 0.80
		outputCostPerM = 0.80

	case "meta-llama/Meta-Llama-3.1-8B-Instruct",
		"meta-llama/Llama-3.1-8B-Instruct":
		inputCostPerM = 0.20
		outputCostPerM = 0.20

	// Mixtral models
	case "mistralai/Mixtral-8x7B-Instruct-v0.1",
		"mistralai/Mixtral-8x22B-Instruct-v0.1":
		inputCostPerM = 0.60
		outputCostPerM = 0.60

	// Qwen models
	case "Qwen/Qwen2.5-72B-Instruct",
		"Qwen/Qwen2.5-Coder-32B-Instruct":
		inputCostPerM = 0.80
		outputCostPerM = 0.80

	// Gemma models
	case "google/gemma-2-9b-it",
		"google/gemma-2-27b-it":
		inputCostPerM = 0.30
		outputCostPerM = 0.30

	default:
		// Default to conservative $1.00 per 1M tokens for unknown models
		// This is a middle-ground estimate given the wide range of HF providers
		inputCostPerM = 1.00
		outputCostPerM = 1.00
	}

	inputCost := float64(inputTokens) * inputCostPerM / 1_000_000
	outputCost := float64(outputTokens) * outputCostPerM / 1_000_000
	return inputCost + outputCost
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)
