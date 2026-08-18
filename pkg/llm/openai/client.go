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
package openai

import (
	"errors"
	"go.uber.org/zap"

	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Global singleton rate limiter shared across all OpenAI clients
var (
	globalRateLimiter     *llm.RateLimiter
	globalRateLimiterOnce sync.Once
)

// Client implements the LLMProvider interface for OpenAI's API.
type Client struct {
	apiKey        string
	model         string
	endpoint      string
	httpClient    *http.Client
	maxTokens     int
	temperature   float64
	thinkingLevel string // "", "none": off; any other value requests adaptive thinking on Claude models
	rateLimiter   *llm.RateLimiter
	toolNameMap   map[string]string // sanitized name → original name
	extraHeaders  map[string]string // additional headers sent with every request
}

// Config holds configuration for the OpenAI client.
type Config struct {
	APIKey            string
	Model             string        // Default: gpt-4o
	Endpoint          string        // Default: https://api.openai.com/v1/chat/completions
	Timeout           time.Duration // Default: 60s
	MaxTokens         int           // Default: 4096
	Temperature       float64       // Default: 1.0
	// ThinkingLevel requests extended thinking ("", "none" = off; "low",
	// "medium", "high", "auto" = on). On this wire every non-off level maps
	// to Anthropic adaptive thinking, and only for Claude-family models —
	// other models' requests are byte-identical to a client without it.
	ThinkingLevel     string
	RateLimiterConfig llm.RateLimiterConfig
	// ExtraHeaders are additional HTTP headers sent with every request.
	// Useful for proxy-specific metadata (e.g. LiteLLM user tracking tags).
	//
	// Headers are applied after Content-Type and Authorization, so a key
	// present in ExtraHeaders will override those standard headers if the
	// same name is used. Use with care when targeting proxies that require
	// a different auth scheme.
	ExtraHeaders map[string]string
}

// Default OpenAI configuration values.
// Can be overridden via environment variables:
//   - OPENAI_DEFAULT_MODEL / LOOM_LLM_OPENAI_MODEL
//   - OPENAI_API_ENDPOINT / LOOM_LLM_OPENAI_ENDPOINT
const (
	// DefaultOpenAIModel uses GPT-4.1 (latest general-purpose model as of 2025)
	DefaultOpenAIModel       = "gpt-4.1"
	DefaultOpenAIEndpoint    = "https://api.openai.com/v1/chat/completions"
	DefaultOpenAITimeout     = 60 * time.Second
	DefaultOpenAIMaxTokens   = 4096
	DefaultOpenAITemperature = 1.0
)

// NewClient creates a new OpenAI client.
func NewClient(config Config) *Client {
	if config.Model == "" {
		// Check environment variable first, then use default
		if envModel := os.Getenv("OPENAI_DEFAULT_MODEL"); envModel != "" {
			config.Model = envModel
		} else if envModel := os.Getenv("LOOM_LLM_OPENAI_MODEL"); envModel != "" {
			config.Model = envModel
		} else {
			config.Model = DefaultOpenAIModel
		}
	}
	if config.Endpoint == "" {
		// Check environment variable first, then use default
		if envEndpoint := os.Getenv("OPENAI_API_ENDPOINT"); envEndpoint != "" {
			config.Endpoint = envEndpoint
		} else if envEndpoint := os.Getenv("LOOM_LLM_OPENAI_ENDPOINT"); envEndpoint != "" {
			config.Endpoint = envEndpoint
		} else {
			config.Endpoint = DefaultOpenAIEndpoint
		}
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultOpenAITimeout
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = DefaultOpenAIMaxTokens
	}
	if config.Temperature == 0 {
		config.Temperature = DefaultOpenAITemperature
	}

	// Initialize rate limiter if enabled
	var rateLimiter *llm.RateLimiter
	if config.RateLimiterConfig.Enabled {
		rateLimiter = getOrCreateGlobalRateLimiter(config.RateLimiterConfig)
	}

	return &Client{
		apiKey:        config.APIKey,
		model:         config.Model,
		endpoint:      config.Endpoint,
		maxTokens:     config.MaxTokens,
		temperature:   config.Temperature,
		thinkingLevel: config.ThinkingLevel,
		rateLimiter:   rateLimiter,
		extraHeaders:  copyHeaders(config.ExtraHeaders),
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
	}
}

// copyHeaders returns a shallow copy of m, or nil when m is nil/empty.
// Prevents callers from racing with the client after construction.
func copyHeaders(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// getOrCreateGlobalRateLimiter returns the global rate limiter, creating it if necessary.
func getOrCreateGlobalRateLimiter(config llm.RateLimiterConfig) *llm.RateLimiter {
	globalRateLimiterOnce.Do(func() {
		globalRateLimiter = llm.NewRateLimiter(config)
	})
	return globalRateLimiter
}

// Name returns the provider name.
func (c *Client) Name() string {
	return "openai"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.model
}

// Chat sends a conversation to OpenAI and returns the response.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Convert messages to OpenAI format
	apiMessages := c.convertMessages(messages)

	// Convert tools to OpenAI format with name sanitization
	c.toolNameMap = make(map[string]string)
	apiTools := c.convertTools(tools)

	// Build request
	req := &ChatCompletionRequest{
		Model:       c.model,
		Messages:    apiMessages,
		Temperature: c.temperature,
		Thinking:    c.thinkingParam(),
	}
	if c.usesMaxCompletionTokens() {
		req.MaxCompletionTokens = c.maxTokens
	} else {
		req.MaxTokens = c.maxTokens
	}

	if len(apiTools) > 0 {
		req.Tools = apiTools
		req.ToolChoice = "auto"
	}

	// Call API
	resp, hdr, err := c.callAPI(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("API call failed: %w", err)
	}

	// Convert response. The gateway's own cost (cache-aware) wins when present.
	return c.convertResponse(resp, parseProviderCost(hdr)), nil
}

// convertMessages converts agent messages to OpenAI format.
func (c *Client) convertMessages(messages []llmtypes.Message) []ChatMessage {
	var apiMessages []ChatMessage

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// System messages always use plain text
			apiMessages = append(apiMessages, ChatMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})

		case "user":
			// Check if message has ContentBlocks (multi-modal content with images)
			if len(msg.ContentBlocks) > 0 {
				// Build content array for multi-modal message
				var content []map[string]interface{}
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						content = append(content, map[string]interface{}{
							"type": "text",
							"text": block.Text,
						})
					case "image":
						if block.Image != nil {
							// OpenAI expects images as data URLs or direct URLs
							var imageURL string
							if block.Image.Source.Type == "base64" {
								// Convert base64 to data URL
								imageURL = fmt.Sprintf("data:%s;base64,%s",
									block.Image.Source.MediaType,
									block.Image.Source.Data)
							} else {
								// Direct URL
								imageURL = block.Image.Source.URL
							}
							content = append(content, map[string]interface{}{
								"type": "image_url",
								"image_url": map[string]interface{}{
									"url": imageURL,
								},
							})
						}
					}
				}
				apiMessages = append(apiMessages, ChatMessage{
					Role:    "user",
					Content: content,
				})
			} else {
				// Fallback to plain text (backward compatible)
				apiMessages = append(apiMessages, ChatMessage{
					Role:    msg.Role,
					Content: msg.Content,
				})
			}

		case "assistant":
			apiMsg := ChatMessage{
				Role: "assistant",
			}

			// Add text content if present
			if msg.Content != "" {
				apiMsg.Content = msg.Content
			}

			// Add tool calls if present
			if len(msg.ToolCalls) > 0 {
				var toolCalls []ToolCall
				for _, tc := range msg.ToolCalls {
					// Marshal input to JSON string.
					// Guard against nil Input — json.Marshal(nil) returns "null"
					// (no error), which LiteLLM forwards to Vertex AI as a non-dict
					// tool_use.input, causing a 400 "Input should be a valid dictionary".
					argsJSON, err := json.Marshal(tc.Input)
					if err != nil || string(argsJSON) == "null" {
						// Fallback to empty object
						argsJSON = []byte("{}")
					}

					toolCalls = append(toolCalls, ToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: FunctionCall{
							Name:      llm.SanitizeToolName(tc.Name),
							Arguments: string(argsJSON),
						},
					})
				}
				apiMsg.ToolCalls = toolCalls
			}

			// In-turn thinking replay: blocks ride back verbatim so the
			// provider's signatures survive the round-trip. Settled turns
			// arrive here already stripped by the compile. A block that is
			// still text-empty (capture should have completed it) is dropped
			// rather than replayed: Anthropic 400s on a thinking block with
			// no thinking field, while a missing block is tolerated (probed).
			if len(msg.ThinkingBlocks) > 0 {
				wire := make([]WireThinkingBlock, 0, len(msg.ThinkingBlocks))
				for _, b := range msg.ThinkingBlocks {
					if b.Thinking == "" && b.Type != "redacted_thinking" {
						continue
					}
					wire = append(wire, WireThinkingBlock{Type: b.Type, Thinking: b.Thinking, Signature: b.Signature})
				}
				apiMsg.ThinkingBlocks = wire
			}

			apiMessages = append(apiMessages, apiMsg)

		case "tool":
			// Tool results as tool role message
			apiMessages = append(apiMessages, ChatMessage{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolUseID,
			})
		}
	}

	// Prompt-cache breakpoints: the compile marks the "perfect place" (ROM,
	// summary, last message before any current-turn offload stub). Each maps 1:1
	// to an apiMessage; convert its content to the block form litellm forwards to
	// Anthropic as cache_control. Anthropic allows at most 4 breakpoints; the
	// compile marks at most three (+ the tool list), so we stay within budget.
	// Emitted only for Anthropic-backed models: cache_control is Anthropic's
	// field, and a strict OpenAI-native endpoint may reject the foreign key or
	// the string→block content reshaping that carries it.
	if c.emitsCacheControl() {
		for i := range messages {
			if i < len(apiMessages) && messages[i].CacheBreakpoint {
				apiMessages[i].Content = withCacheControl(apiMessages[i].Content)
			}
		}
	}

	return apiMessages
}

// emitsCacheControl reports whether the configured model is Anthropic-backed —
// the only upstream that consumes cache_control. The OpenAI-shaped client
// fronts many gateways (LiteLLM, Mistral, HuggingFace); only requests routed
// to a claude model benefit from the marker, and only those get it.
func (c *Client) emitsCacheControl() bool {
	return strings.Contains(strings.ToLower(c.model), "claude")
}

// completeThinkingBlocks pairs relocated thinking text back into blocks that
// arrived signature-only — the gateway's split shape puts the text in
// reasoning_content and ships a block skeleton carrying only the signature.
// Anthropic requires the thinking field on replay (400 without it), and the
// signature verifies against exactly this text, so reattaching it re-forms
// the original signed block. A block that carried its own text keeps it; the
// fill applies only when no non-redacted block carried any.
func completeThinkingBlocks(blocks []llmtypes.ThinkingBlock, text string) {
	if text == "" {
		return
	}
	for i := range blocks {
		if blocks[i].Type != "redacted_thinking" && blocks[i].Thinking != "" {
			return
		}
	}
	for i := range blocks {
		if blocks[i].Type != "redacted_thinking" {
			blocks[i].Thinking = text
			return
		}
	}
}

// thinkingParam returns the request's thinking field, or nil (omitted on the
// wire) when thinking is off or the model is not Claude-family — the same
// gate as cache_control, kept one predicate on purpose. Every non-off level
// maps to adaptive: budget_tokens is rejected by Claude 4.6+/5, and adaptive
// carries no effort knob on this wire.
func (c *Client) thinkingParam() map[string]interface{} {
	if c.thinkingLevel == "" || c.thinkingLevel == "none" || !c.emitsCacheControl() {
		return nil
	}
	return map[string]interface{}{"type": "adaptive"}
}

// withCacheControl rewrites a message's content into the block form carrying a
// cache_control marker on its last block. A plain string becomes a single text
// block; an existing block list gets the marker appended to its last block.
func withCacheControl(content interface{}) interface{} {
	cc := map[string]interface{}{"type": "ephemeral"}
	switch v := content.(type) {
	case string:
		if v == "" {
			return content
		}
		return []map[string]interface{}{{"type": "text", "text": v, "cache_control": cc}}
	case []map[string]interface{}:
		if len(v) > 0 {
			v[len(v)-1]["cache_control"] = cc
		}
		return v
	default:
		return content
	}
}

// convertTools converts shuttle tools to OpenAI format.
// Tool names are sanitized to replace colons (MCP namespace separator)
// with underscores for provider compatibility.
func (c *Client) convertTools(tools []shuttle.Tool) []Tool {
	var apiTools []Tool

	for _, tool := range tools {
		originalName := tool.Name()
		sanitizedName := llm.SanitizeToolName(originalName)
		if c.toolNameMap != nil {
			c.toolNameMap[sanitizedName] = originalName
		}

		apiTool := Tool{
			Type: "function",
			Function: FunctionDef{
				Name:        sanitizedName,
				Description: tool.Description(),
			},
		}

		// Convert JSONSchema to OpenAI's parameters format
		schema := tool.InputSchema()
		if schema != nil {
			params := make(map[string]interface{})
			params["type"] = schema.Type
			if schema.Type == "" {
				params["type"] = "object"
			}

			if schema.Properties != nil {
				params["properties"] = c.convertSchemaProperties(schema.Properties)
			}

			if len(schema.Required) > 0 {
				params["required"] = schema.Required
			}

			apiTool.Function.Parameters = params
		}

		apiTools = append(apiTools, apiTool)
	}

	return apiTools
}

// convertSchemaProperties converts JSONSchema properties to OpenAI format.
func (c *Client) convertSchemaProperties(props map[string]*shuttle.JSONSchema) map[string]interface{} {
	if props == nil {
		return nil
	}

	result := make(map[string]interface{})
	for key, schema := range props {
		propMap := make(map[string]interface{})
		propType := schema.Type
		if propType == "" {
			propType = "string" // MCP tools may omit type; default to string
		}
		propMap["type"] = propType

		if schema.Description != "" {
			propMap["description"] = schema.Description
		}
		if schema.Enum != nil {
			propMap["enum"] = schema.Enum
		}
		if schema.Default != nil {
			propMap["default"] = schema.Default
		}
		if schema.Properties != nil {
			propMap["properties"] = c.convertSchemaProperties(schema.Properties)
			if propType == "string" {
				propMap["type"] = "object"
			}
		}
		if schema.Items != nil {
			itemMap := make(map[string]interface{})
			itemType := schema.Items.Type
			if itemType == "" {
				itemType = "string"
			}
			itemMap["type"] = itemType
			if schema.Items.Description != "" {
				itemMap["description"] = schema.Items.Description
			}
			propMap["items"] = itemMap
			if propType == "string" {
				propMap["type"] = "array"
			}
		}

		result[key] = propMap
	}
	return result
}

// convertResponse converts OpenAI response to agent format.
func (c *Client) convertResponse(resp *ChatCompletionResponse, providerCostUSD float64) *llmtypes.LLMResponse {
	llmResp := &llmtypes.LLMResponse{
		Usage: llmtypes.Usage{
			InputTokens:              resp.Usage.PromptTokens,
			OutputTokens:             resp.Usage.CompletionTokens,
			TotalTokens:              resp.Usage.TotalTokens,
			CacheReadInputTokens:     resp.Usage.CacheRead(),
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
			CostUSD: costOrEstimate(providerCostUSD, func() float64 {
				return c.calculateCost(resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
					resp.Usage.CacheRead(), resp.Usage.CacheCreationInputTokens)
			}),
		},
		Metadata: map[string]interface{}{
			"model":         resp.Model,
			"finish_reason": resp.Choices[0].FinishReason,
		},
	}

	// Prompt-cache measurement: one line per call so a run's hit rate is legible
	// in the logs (cache_read>0 means the provider served a cached prefix).
	zap.L().Info("prompt cache usage",
		zap.Int("input_tokens", resp.Usage.PromptTokens),
		zap.Int("cache_read", resp.Usage.CacheRead()),
		zap.Int("cache_creation", resp.Usage.CacheCreationInputTokens))

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]

		// Map finish_reason to stop_reason
		switch choice.FinishReason {
		case "stop":
			llmResp.StopReason = "end_turn"
		case "length":
			llmResp.StopReason = "max_tokens"
		case "tool_calls", "function_call":
			llmResp.StopReason = "tool_use"
		case "content_filter":
			llmResp.StopReason = "content_filter"
		default:
			llmResp.StopReason = choice.FinishReason
		}

		// Extract content
		if choice.Message.Content != nil {
			if str, ok := choice.Message.Content.(string); ok {
				llmResp.Content = str
			}
		}

		// Extract thinking. Both observed shapes handled: text in
		// reasoning_content with signature-only blocks, and text inside the
		// blocks with empty reasoning_content.
		if len(choice.Message.ThinkingBlocks) > 0 {
			for _, b := range choice.Message.ThinkingBlocks {
				llmResp.ThinkingBlocks = append(llmResp.ThinkingBlocks, llmtypes.ThinkingBlock{
					Type: b.Type, Thinking: b.Thinking, Signature: b.Signature,
				})
				if b.Type != "redacted_thinking" {
					llmResp.Thinking += b.Thinking
				}
			}
		}
		if llmResp.Thinking == "" {
			llmResp.Thinking = choice.Message.ReasoningContent
		}
		completeThinkingBlocks(llmResp.ThinkingBlocks, llmResp.Thinking)

		// Extract tool calls
		for _, tc := range choice.Message.ToolCalls {
			// Parse arguments JSON string back to map
			var input map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				// If parsing fails, store as raw string
				input = map[string]interface{}{
					"_raw": tc.Function.Arguments,
				}
			}

			llmResp.ToolCalls = append(llmResp.ToolCalls, llmtypes.ToolCall{
				ID:    tc.ID,
				Name:  llm.ReverseToolName(c.toolNameMap, tc.Function.Name),
				Input: input,
			})
		}
	}

	return llmResp
}

// openAICacheReadMultiplier returns the fraction of the input rate that OpenAI
// charges for a cached prompt token. The gpt-5 and o-series generations discount
// cached input by 90%; the gpt-4 generation by 50%. Applying the older 0.5 to a
// gpt-5 model overstates cost five-fold on the cached share, which on agentic
// workloads is the great majority of all input.
func openAICacheReadMultiplier(modelID string) float64 {
	switch {
	case strings.HasPrefix(modelID, "gpt-5"),
		strings.HasPrefix(modelID, "o1"),
		strings.HasPrefix(modelID, "o3"),
		strings.HasPrefix(modelID, "o4"):
		return 0.10
	default:
		return 0.50
	}
}

// anthropicFallbackPricing returns published Anthropic rates for a model id
// proxied through an OpenAI-compatible endpoint. Mirrors the bedrock client's
// substring matching so a gateway-proxied Claude is never priced as a GPT.
func anthropicFallbackPricing(modelID string) (inputPerM, outputPerM float64, matched bool) {
	switch {
	case strings.Contains(modelID, "claude-opus-4-1"):
		return 15.0, 75.0, true
	case strings.Contains(modelID, "claude-opus"):
		return 5.0, 25.0, true
	case strings.Contains(modelID, "claude-haiku"):
		return 1.0, 5.0, true
	case strings.Contains(modelID, "claude-sonnet"), strings.Contains(modelID, "claude-3-5-sonnet"):
		return 3.0, 15.0, true
	}
	return 0, 0, false
}

// providerCostHeader is litellm's own computed cost for the call.
//
// Streaming caveat, and why calculateCost must remain: HTTP response headers
// are written before the SSE body streams, so on a streaming call the gateway
// cannot know the final token counts at header time — the header is absent or
// zero there, and only non-streaming responses carry an authoritative figure.
// Zero therefore falls back to the local estimate. It is
// cache-aware and authoritative — preferred over any local estimate.
const providerCostHeader = "x-litellm-response-cost"

// parseProviderCost reads the gateway's reported cost, if it sent one.
// Returns 0 when absent or unparseable, meaning "fall back to the estimate".
func parseProviderCost(h http.Header) float64 {
	if h == nil {
		return 0
	}
	v := strings.TrimSpace(h.Get(providerCostHeader))
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	// ParseFloat accepts "Inf" and "NaN"; +Inf also passes a plain `< 0` check,
	// so a garbage header would otherwise poison CostUSD with +Inf downstream.
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0
	}
	return f
}

// costOrEstimate prefers the provider's reported cost; estimate() is used only
// when the provider did not report one.
func costOrEstimate(providerCostUSD float64, estimate func() float64) float64 {
	if providerCostUSD > 0 {
		return providerCostUSD
	}
	return estimate()
}

// calculateCost estimates the cost in USD based on token usage.
//
// Cache tiers matter, and they are PER FAMILY: Anthropic bills a cache write
// at 1.25x the input rate and a cache read at 0.10x; OpenAI bills cached input
// at 0.5x (gpt-4o: $1.25/M cached vs $2.50/M) with NO write premium. A
// cache-blind total over-charges a cache-heavy workload several fold — and a
// single Anthropic-shaped multiplier under-charges genuine OpenAI cached reads
// 5x. The multipliers below therefore follow the rate card that was selected.
// NOTE the OpenAI-compatible semantics: prompt_tokens INCLUDES cached tokens
// (unlike Anthropic native, where input_tokens excludes them), so the uncached
// remainder must be derived by subtraction.
//
// This is the FALLBACK. When the gateway reports its own cost (litellm's
// x-litellm-response-cost header) that figure is authoritative and is used
// instead — see providerCostUSD.
func (c *Client) calculateCost(inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int) float64 {
	// Cache multipliers of the selected rate card; OpenAI defaults, swapped to
	// Anthropic's when the family matcher below selects Anthropic rates.
	// OpenAI discounts cached input by 90% from gpt-5/o-series onward and by 50%
	// on the gpt-4 generation, so the read multiplier follows the family. Writes
	// are never surcharged — caching is automatic, with no write to bill.
	cacheWriteMult, cacheReadMult := 1.0, openAICacheReadMultiplier(c.model)
	// The catalog (pkg/llm/catalog) is the source of truth for pricing. Fall back
	// to the provider-local rates below only for model ids it does not list.
	inputCostPerM, outputCostPerM, ok := catalog.LookupPricing("openai", c.model)
	if !ok {
		// Gateways (litellm et al.) proxy non-OpenAI models through this
		// OpenAI-compatible client under ids like "coding-agent/claude-sonnet-4-6".
		// Falling through to the GPT rate card below would price them wrongly, so
		// recognise the Anthropic family first. Order matters: check "opus-4-1"
		// before "opus-4", since Contains("opus-4-5","opus-4") is true.
		if in, out, matched := anthropicFallbackPricing(c.model); matched {
			inputCostPerM, outputCostPerM = in, out
			ok = true
			cacheWriteMult, cacheReadMult = 1.25, 0.10
		}
	}
	if !ok {
		switch c.model {
		case "gpt-4o":
			inputCostPerM = 2.50
			outputCostPerM = 10.00
		case "gpt-4o-mini":
			inputCostPerM = 0.15
			outputCostPerM = 0.60
		case "gpt-4-turbo", "gpt-4-turbo-preview":
			inputCostPerM = 10.00
			outputCostPerM = 30.00
		case "gpt-4", "gpt-4-0613":
			inputCostPerM = 30.00
			outputCostPerM = 60.00
		case "gpt-3.5-turbo", "gpt-3.5-turbo-0125":
			inputCostPerM = 0.50
			outputCostPerM = 1.50
		case "o1-preview":
			inputCostPerM = 15.00
			outputCostPerM = 60.00
		case "o1-mini":
			inputCostPerM = 3.00
			outputCostPerM = 12.00
		case "gpt-5":
			inputCostPerM = 2.50
			outputCostPerM = 10.00
		case "gpt-5-mini":
			inputCostPerM = 0.40
			outputCostPerM = 1.60
		case "gpt-4.1":
			inputCostPerM = 2.00
			outputCostPerM = 8.00
		case "gpt-4.1-mini":
			inputCostPerM = 0.40
			outputCostPerM = 1.60
		case "gpt-4.1-nano":
			inputCostPerM = 0.10
			outputCostPerM = 0.40
		case "o3":
			inputCostPerM = 10.00
			outputCostPerM = 40.00
		case "o3-mini":
			inputCostPerM = 1.10
			outputCostPerM = 4.40
		case "o4-mini":
			inputCostPerM = 1.10
			outputCostPerM = 4.40
		default:
			// Default to gpt-4o pricing
			inputCostPerM = 2.50
			outputCostPerM = 10.00
		}
	}

	uncached := inputTokens - cacheReadTokens - cacheCreationTokens
	if uncached < 0 {
		uncached = 0
	}
	inputCost := float64(uncached) * inputCostPerM / 1_000_000
	cacheWriteCost := float64(cacheCreationTokens) * inputCostPerM * cacheWriteMult / 1_000_000
	cacheReadCost := float64(cacheReadTokens) * inputCostPerM * cacheReadMult / 1_000_000
	outputCost := float64(outputTokens) * outputCostPerM / 1_000_000
	return inputCost + cacheWriteCost + cacheReadCost + outputCost
}

// usesMaxCompletionTokens returns true when the model requires max_completion_tokens
// instead of the legacy max_tokens parameter.
//
// OpenAI deprecated max_tokens for newer models:
//   - o-series (o1, o3, o4, …): only accept max_completion_tokens
//   - gpt-4o family: only accept max_completion_tokens
//   - Legacy (gpt-3.5-turbo, gpt-4-0613, gpt-4-32k): require max_tokens
//
// Default: max_completion_tokens (forward-compatible for future models).
func (c *Client) usesMaxCompletionTokens() bool {
	m := strings.ToLower(c.model)
	// Legacy models that still require the old max_tokens field
	legacyPrefixes := []string{"gpt-3.5", "gpt-35", "gpt-4-0613", "gpt-4-32k"}
	for _, p := range legacyPrefixes {
		if strings.HasPrefix(m, p) {
			return false
		}
	}
	return true // gpt-4o, o1, o3, o4, future models
}

// ChatStream implements token-by-token streaming for OpenAI.
// This method uses OpenAI's Chat Completions API with stream=true to stream tokens
// as they are generated. The tokenCallback is called for each token received.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// Latency split stamps, surfaced via response Metadata: prep (conversion +
	// marshal), then wait-to-first-SSE-chunk of ANY delta type. The token
	// callback fires only on text deltas, so tool-call turns are invisible to
	// callback-based TTFT — these stamps are the client's own ground truth.
	prepStart := time.Now()

	// 1. Build request body (reuse existing message and tool conversion)
	apiMessages := c.convertMessages(messages)
	c.toolNameMap = make(map[string]string)
	apiTools := c.convertTools(tools)

	req := &ChatCompletionRequest{
		Model:         c.model,
		Messages:      apiMessages,
		Temperature:   c.temperature,
		Thinking:      c.thinkingParam(),
		Stream:        true,                               // Enable streaming
		StreamOptions: &StreamOptions{IncludeUsage: true}, // final usage chunk (tokens + cache)
	}
	if c.usesMaxCompletionTokens() {
		req.MaxCompletionTokens = c.maxTokens
	} else {
		req.MaxTokens = c.maxTokens
	}

	if len(apiTools) > 0 {
		req.Tools = apiTools
		req.ToolChoice = "auto"
	}

	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	for k, v := range c.extraHeaders {
		httpReq.Header.Set(k, v)
	}

	// 2. Send request with rate limiting if enabled
	sendStart := time.Now()
	var firstChunkAt time.Time
	sseChunks := 0
	var httpResp *http.Response
	if c.rateLimiter != nil {
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			return c.httpClient.Do(httpReq)
		})
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
		httpResp = result.(*http.Response)
	} else {
		var err error
		httpResp, err = c.httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
	}
	defer func() { _ = httpResp.Body.Close() }()

	// Check status code before streaming
	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		// Positive identification of the provider's context-too-long refusal
		// (HLD §5.2 step 12) — the only relief trigger.
		if llm.IsOpenAIContextTooLong(httpResp.StatusCode, respBody) {
			return nil, fmt.Errorf("API error (status %d): %s: %w", httpResp.StatusCode, string(respBody), llm.ErrContextTooLong)
		}
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// 3. Process Server-Sent Events (SSE) stream
	var contentBuffer strings.Builder
	usage := llmtypes.Usage{}
	var finishReason string
	tokenCount := 0
	var toolCalls []llmtypes.ToolCall
	toolCallMap := make(map[int]*llmtypes.ToolCall) // Track tool calls by index

	// Streaming thinking assembly (observed litellm contract): text arrives
	// as reasoning_content deltas and/or inside thinking_blocks fragments;
	// the signature arrives in a block fragment. Fragments merge by index.
	var reasoningBuffer strings.Builder
	thinkBlockMap := make(map[int]*llmtypes.ThinkingBlock)
	var thinkBlockOrder []int

	scanner := bufio.NewScanner(httpResp.Body)
	// A gateway may coalesce a whole response (or an error echoing the request)
	// into one SSE line; the Scanner default 64KB line cap aborts the stream.
	// Grown on demand, so steady-state memory is unchanged.
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "data: <json>" or "data: [DONE]"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		// Extract JSON data after "data: "
		jsonData := strings.TrimPrefix(line, "data: ")

		// Check for [DONE] message
		if jsonData == "[DONE]" {
			break
		}

		// Parse streaming chunk
		var chunk ChatCompletionStreamChunk
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			// Skip malformed chunks but continue processing
			continue
		}
		if firstChunkAt.IsZero() {
			firstChunkAt = time.Now()
		}
		sseChunks++

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			// Extract text content delta
			if choice.Delta.Content != nil {
				if str, ok := choice.Delta.Content.(string); ok && str != "" {
					token := str
					contentBuffer.WriteString(token)
					tokenCount++

					// Call token callback (non-blocking)
					if tokenCallback != nil {
						tokenCallback(token)
					}
				}
			}

			// Extract thinking deltas — accumulated only, never forwarded to
			// the token callback (thinking is not user-visible output).
			if choice.Delta.ReasoningContent != "" {
				reasoningBuffer.WriteString(choice.Delta.ReasoningContent)
			}
			for _, tb := range choice.Delta.ThinkingBlocks {
				idx := 0
				if tb.Index != nil {
					idx = *tb.Index
				}
				blk, exists := thinkBlockMap[idx]
				if !exists {
					blk = &llmtypes.ThinkingBlock{}
					thinkBlockMap[idx] = blk
					thinkBlockOrder = append(thinkBlockOrder, idx)
				}
				if tb.Type != "" {
					blk.Type = tb.Type
				}
				blk.Thinking += tb.Thinking
				if tb.Signature != "" {
					blk.Signature = tb.Signature
				}
			}

			// Extract tool call deltas
			if len(choice.Delta.ToolCalls) > 0 {
				for _, tcDelta := range choice.Delta.ToolCalls {
					idx := tcDelta.Index
					if _, exists := toolCallMap[idx]; !exists {
						// New tool call - map sanitized name back to original
						toolCallMap[idx] = &llmtypes.ToolCall{
							ID:    tcDelta.ID,
							Name:  llm.ReverseToolName(c.toolNameMap, tcDelta.Function.Name),
							Input: make(map[string]interface{}),
						}
					}

					// Accumulate function arguments (they come in chunks)
					if tcDelta.Function.Arguments != "" {
						tc := toolCallMap[idx]
						// Note: Arguments are accumulated as string, parsed at the end
						if existingArgs, ok := tc.Input["_args"].(string); ok {
							tc.Input["_args"] = existingArgs + tcDelta.Function.Arguments
						} else {
							tc.Input["_args"] = tcDelta.Function.Arguments
						}
					}
				}
			}

			// Extract finish reason
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}

		// Extract usage (only in final chunk, if provided)
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
			usage.TotalTokens = chunk.Usage.TotalTokens
			usage.CacheReadInputTokens = chunk.Usage.CacheRead()
			usage.CacheCreationInputTokens = chunk.Usage.CacheCreationInputTokens
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("error reading stream: SSE line exceeded the 8MB cap: %w", err)
		}
		return nil, fmt.Errorf("error reading stream: %w", err)
	}

	// 4. Parse accumulated tool call arguments
	for _, tc := range toolCallMap {
		if argsStr, ok := tc.Input["_args"].(string); ok {
			var parsedArgs map[string]interface{}
			if err := json.Unmarshal([]byte(argsStr), &parsedArgs); err != nil {
				// If parsing fails, store as raw string
				parsedArgs = map[string]interface{}{
					"_raw": argsStr,
				}
			}
			tc.Input = parsedArgs
		}
		toolCalls = append(toolCalls, *tc)
	}

	// 5. Build final response
	if usage.TotalTokens == 0 {
		usage.OutputTokens = tokenCount
		usage.TotalTokens = tokenCount // Input tokens not available in stream
	}
	usage.CostUSD = costOrEstimate(parseProviderCost(httpResp.Header), func() float64 {
		return c.calculateCost(usage.InputTokens, usage.OutputTokens,
			usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
	})
	zap.L().Info("prompt cache usage (stream)",
		zap.Int("input_tokens", usage.InputTokens),
		zap.Int("cache_read", usage.CacheReadInputTokens),
		zap.Int("cache_creation", usage.CacheCreationInputTokens))

	// Record token usage for rate limiter metrics
	if c.rateLimiter != nil {
		totalTokens := int64(usage.InputTokens + usage.OutputTokens)
		c.rateLimiter.RecordTokenUsage(totalTokens)
	}

	// Map finish_reason to stop_reason
	var stopReason string
	switch finishReason {
	case "stop":
		stopReason = "end_turn"
	case "length":
		stopReason = "max_tokens"
	case "tool_calls", "function_call":
		stopReason = "tool_use"
	case "content_filter":
		stopReason = "content_filter"
	default:
		stopReason = finishReason
	}

	// Assemble thinking: blocks in arrival order; plain text from the blocks
	// when they carried it, else from the reasoning_content deltas.
	var thinkingBlocks []llmtypes.ThinkingBlock
	thinkingText := ""
	for _, idx := range thinkBlockOrder {
		blk := thinkBlockMap[idx]
		if blk.Type == "" {
			blk.Type = "thinking"
		}
		thinkingBlocks = append(thinkingBlocks, *blk)
		if blk.Type != "redacted_thinking" {
			thinkingText += blk.Thinking
		}
	}
	if thinkingText == "" {
		thinkingText = reasoningBuffer.String()
	}
	completeThinkingBlocks(thinkingBlocks, thinkingText)

	md := map[string]interface{}{
		"model":         c.model,
		"finish_reason": finishReason,
		"streaming":     true,
		"prep_ms":       sendStart.Sub(prepStart).Milliseconds(),
		"sse_chunks":    sseChunks,
	}
	if !firstChunkAt.IsZero() {
		md["ttft_ms"] = firstChunkAt.Sub(sendStart).Milliseconds()
		md["gen_ms"] = time.Since(firstChunkAt).Milliseconds()
	}

	return &llmtypes.LLMResponse{
		Content:        contentBuffer.String(),
		StopReason:     stopReason,
		Usage:          usage,
		ToolCalls:      toolCalls,
		Thinking:       thinkingText,
		ThinkingBlocks: thinkingBlocks,
		Metadata:       md,
	}, nil
}

// callAPI makes the HTTP request to OpenAI's API.
func (c *Client) callAPI(ctx context.Context, req *ChatCompletionRequest) (*ChatCompletionResponse, http.Header, error) {
	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	for k, v := range c.extraHeaders {
		httpReq.Header.Set(k, v)
	}

	// Send request with rate limiting if enabled
	var httpResp *http.Response
	if c.rateLimiter != nil {
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			return c.httpClient.Do(httpReq)
		})
		if err != nil {
			return nil, nil, fmt.Errorf("HTTP request failed: %w", err)
		}
		httpResp = result.(*http.Response)
	} else {
		var err error
		httpResp, err = c.httpClient.Do(httpReq)
		if err != nil {
			return nil, nil, fmt.Errorf("HTTP request failed: %w", err)
		}
	}
	defer func() { _ = httpResp.Body.Close() }()

	// Read response
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var resp ChatCompletionResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Positive identification of the provider's context-too-long refusal
	// (HLD §5.2 step 12) — the only relief trigger.
	if llm.IsOpenAIContextTooLong(httpResp.StatusCode, respBody) {
		return nil, nil, fmt.Errorf("API error (status %d): %s: %w", httpResp.StatusCode, string(respBody), llm.ErrContextTooLong)
	}

	// Check for API errors
	if resp.Error != nil {
		return nil, nil, fmt.Errorf("OpenAI API error: %s (type: %s)", resp.Error.Message, resp.Error.Type)
	}

	// Check status code
	if httpResp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	return &resp, httpResp.Header, nil
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
