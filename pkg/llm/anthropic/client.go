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
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

const (
	// DefaultAnthropicModel is the default Claude model
	DefaultAnthropicModel = "claude-sonnet-4-5-20250929"
	// DefaultAnthropicEndpoint is the default Anthropic API endpoint
	DefaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
	// DefaultMaxTokens is the default maximum tokens per request
	DefaultMaxTokens = 4096
	// DefaultTemperature is the default LLM temperature
	DefaultTemperature = 1.0
	// DefaultTimeout is the default HTTP timeout
	DefaultTimeout = 60 * time.Second
)

// Global singleton rate limiter shared across all Anthropic clients
var (
	globalRateLimiter     *llm.RateLimiter
	globalRateLimiterOnce sync.Once
)

// DefaultAnthropicRateLimiterConfig returns safe defaults for Anthropic's API.
//
// Anthropic rate limits by tier (as of 2026):
//   - Free / Tier 1: 50 RPM, 30K–100K ITPM
//   - Tier 2:        1000 RPM, 2M ITPM
//   - Tier 3+:       5000+ RPM
//
// These defaults target Tier 1 (the most common). Users on higher tiers should
// increase requests_per_second and tokens_per_minute in loom.yaml.
func DefaultAnthropicRateLimiterConfig() llm.RateLimiterConfig {
	return llm.RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 0.7,                    // ~42 RPM — safely under Tier 1 50 RPM limit
		TokensPerMinute:   80000,                  // 80% of Tier 1 100K ITPM (30K on free)
		BurstCapacity:     3,                      // Conservative burst for multi-agent sessions
		MinDelay:          800 * time.Millisecond, // ~1.25 RPS ceiling; prevents burst overshoots
		MaxRetries:        5,
		RetryBackoff:      2 * time.Second, // Longer initial backoff for Anthropic 429s
		QueueTimeout:      5 * time.Minute,
	}
}

// Client implements the LLMProvider interface for Anthropic's Claude API.
type Client struct {
	apiKey      string
	model       string
	endpoint    string
	httpClient  *http.Client
	maxTokens   int
	temperature float64
	rateLimiter *llm.RateLimiter
	toolNameMap map[string]string // sanitized name → original name
}

// Config holds configuration for the Anthropic client.
type Config struct {
	APIKey            string
	Model             string // Default: claude-sonnet-4-5-20250929
	Endpoint          string // Default: https://api.anthropic.com/v1/messages
	Timeout           time.Duration
	MaxTokens         int     // Default: 4096
	Temperature       float64 // Default: 1.0
	RateLimiterConfig llm.RateLimiterConfig
}

// NewClient creates a new Anthropic client.
func NewClient(config Config) *Client {
	if config.Model == "" {
		// Check environment variable first, then use default
		if envModel := os.Getenv("ANTHROPIC_DEFAULT_MODEL"); envModel != "" {
			config.Model = envModel
		} else {
			config.Model = DefaultAnthropicModel
		}
	}
	if config.Endpoint == "" {
		// Check environment variable first, then use default
		if envEndpoint := os.Getenv("ANTHROPIC_API_ENDPOINT"); envEndpoint != "" {
			config.Endpoint = envEndpoint
		} else {
			config.Endpoint = DefaultAnthropicEndpoint
		}
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultTimeout
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = DefaultMaxTokens
	}
	if config.Temperature == 0 {
		config.Temperature = DefaultTemperature
	}

	// Initialize rate limiter if enabled
	var rateLimiter *llm.RateLimiter
	if config.RateLimiterConfig.Enabled {
		rateLimiter = getOrCreateGlobalRateLimiter(config.RateLimiterConfig)
	}

	return &Client{
		apiKey:      config.APIKey,
		model:       config.Model,
		endpoint:    config.Endpoint,
		maxTokens:   config.MaxTokens,
		temperature: config.Temperature,
		rateLimiter: rateLimiter,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
	}
}

// getOrCreateGlobalRateLimiter returns the global rate limiter, creating it if necessary.
// Caller-supplied non-zero fields override DefaultAnthropicRateLimiterConfig values.
func getOrCreateGlobalRateLimiter(config llm.RateLimiterConfig) *llm.RateLimiter {
	globalRateLimiterOnce.Do(func() {
		// Start from Anthropic-specific defaults, then apply caller overrides.
		// This ensures we don't blindly fall through to DefaultRateLimiterConfig()
		// (which is tuned for Bedrock and allows 2 RPS — exceeding Anthropic Tier 1).
		merged := DefaultAnthropicRateLimiterConfig()
		merged.Enabled = config.Enabled
		if config.Logger != nil {
			merged.Logger = config.Logger
		}
		if config.RequestsPerSecond > 0 {
			merged.RequestsPerSecond = config.RequestsPerSecond
		}
		if config.TokensPerMinute > 0 {
			merged.TokensPerMinute = config.TokensPerMinute
		}
		if config.BurstCapacity > 0 {
			merged.BurstCapacity = config.BurstCapacity
		}
		if config.MinDelay > 0 {
			merged.MinDelay = config.MinDelay
		}
		if config.MaxRetries > 0 {
			merged.MaxRetries = config.MaxRetries
		}
		if config.RetryBackoff > 0 {
			merged.RetryBackoff = config.RetryBackoff
		}
		if config.QueueTimeout > 0 {
			merged.QueueTimeout = config.QueueTimeout
		}
		globalRateLimiter = llm.NewRateLimiter(merged)
	})
	return globalRateLimiter
}

// Name returns the provider name.
func (c *Client) Name() string {
	return "anthropic"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.model
}

// Chat sends a conversation to Claude and returns the response.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Extract system messages and convert to Anthropic format
	systemPrompt, apiMessages := c.convertMessages(messages)

	// Convert tools to Anthropic format with name sanitization
	c.toolNameMap = make(map[string]string)
	apiTools := c.convertTools(tools)

	// Build request
	req := &MessagesRequest{
		Model:       c.model,
		Messages:    apiMessages,
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
	}

	// Add system prompt blocks if present (Anthropic Messages API requires separate system field)
	if len(systemPrompt) > 0 {
		req.System = systemPrompt
	}

	if len(apiTools) > 0 {
		req.Tools = apiTools
	}

	// Call API
	resp, err := c.callAPI(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("API call failed: %w", err)
	}

	// Convert response
	return c.convertResponse(resp), nil
}

// convertMessages converts agent messages to Anthropic format.
// Returns the system prompt blocks (with cache_control on the last block) and the API messages.
// System messages are extracted and combined, as Anthropic Messages API requires
// them to be sent as a separate "system" field, not in the messages array.
func (c *Client) convertMessages(messages []llmtypes.Message) ([]TextBlockParam, []Message) {
	var systemPrompts []string
	var apiMessages []Message

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// Extract system messages - they'll be combined and sent separately
			if msg.Content != "" {
				systemPrompts = append(systemPrompts, msg.Content)
			}

		case "user":
			// Check if message has ContentBlocks (multi-modal content with images)
			if len(msg.ContentBlocks) > 0 {
				// Convert content blocks from agent format to Anthropic format
				var content []ContentBlock
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						content = append(content, ContentBlock{
							Type: "text",
							Text: block.Text,
						})
					case "image":
						if block.Image != nil {
							content = append(content, ContentBlock{
								Type: "image",
								Source: &ImageSource{
									Type:      block.Image.Source.Type,
									MediaType: block.Image.Source.MediaType,
									Data:      block.Image.Source.Data,
									URL:       block.Image.Source.URL,
								},
							})
						}
					}
				}
				apiMessages = append(apiMessages, Message{
					Role:    "user",
					Content: content,
				})
			} else {
				// Fallback to plain text (backward compatible)
				apiMessages = append(apiMessages, Message{
					Role: "user",
					Content: []ContentBlock{
						{Type: "text", Text: msg.Content},
					},
				})
			}

		case "assistant":
			var content []ContentBlock

			// Add text content if present
			if msg.Content != "" {
				content = append(content, ContentBlock{
					Type: "text",
					Text: msg.Content,
				})
			}

			// Add tool calls if present (sanitize names for API compatibility)
			for _, tc := range msg.ToolCalls {
				input := tc.Input
				if input == nil {
					input = map[string]interface{}{} // Anthropic API requires non-null input for tool_use blocks
				}
				content = append(content, ContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  llm.SanitizeToolName(tc.Name),
					Input: input,
				})
			}

			if len(content) > 0 {
				apiMessages = append(apiMessages, Message{
					Role:    "assistant",
					Content: content,
				})
			}

		case "tool":
			// Tool results
			apiMessages = append(apiMessages, Message{
				Role: "user",
				Content: []ContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: msg.ToolUseID,
						Content:   msg.Content,
					},
				},
			})
		}
	}

	// Combine all system prompts and wrap in a TextBlockParam with cache_control.
	// Placing cache_control on the system block caches it for ~5 minutes.
	// For Anthropic, cached tokens don't count against the ITPM rate limit.
	if len(systemPrompts) == 0 {
		return nil, apiMessages
	}
	systemText := strings.Join(systemPrompts, "\n\n")
	systemBlocks := []TextBlockParam{
		{
			Type:         "text",
			Text:         systemText,
			CacheControl: &CacheControl{Type: "ephemeral"},
		},
	}
	return systemBlocks, apiMessages
}

// convertTools converts shuttle tools to Anthropic format.
// Tool names are sanitized to replace colons with underscores for provider compatibility.
// The last tool in the list is marked with cache_control: ephemeral so the entire tool
// list is cached. For Anthropic, cached tool tokens don't count against ITPM rate limits.
func (c *Client) convertTools(tools []shuttle.Tool) []CacheableTool {
	var apiTools []CacheableTool

	for _, tool := range tools {
		originalName := tool.Name()
		sanitizedName := llm.SanitizeToolName(originalName)
		if c.toolNameMap != nil {
			c.toolNameMap[sanitizedName] = originalName
		}

		apiTool := CacheableTool{
			Name:        sanitizedName,
			Description: tool.Description(),
		}

		// Convert JSONSchema to Anthropic's input schema format
		schema := tool.InputSchema()
		if schema != nil {
			apiTool.InputSchema = InputSchema{
				Type:       schema.Type,
				Properties: c.convertSchemaProperties(schema.Properties),
				Required:   schema.Required,
			}
		}

		apiTools = append(apiTools, apiTool)
	}

	// Mark the last tool with cache_control to cache the entire tool list.
	// Anthropic caches everything up to and including the marked breakpoint.
	if len(apiTools) > 0 {
		apiTools[len(apiTools)-1].CacheControl = &CacheControl{Type: "ephemeral"}
	}

	return apiTools
}

// convertSchemaProperties converts JSONSchema properties to Anthropic format.
func (c *Client) convertSchemaProperties(props map[string]*shuttle.JSONSchema) map[string]map[string]interface{} {
	if props == nil {
		return nil
	}

	result := make(map[string]map[string]interface{})
	for key, schema := range props {
		propMap := make(map[string]interface{})
		propMap["type"] = schema.Type

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
		}
		if schema.Items != nil {
			propMap["items"] = map[string]interface{}{
				"type": schema.Items.Type,
			}
		}

		result[key] = propMap
	}
	return result
}

// convertResponse converts Anthropic response to agent format.
func (c *Client) convertResponse(resp *MessagesResponse) *llmtypes.LLMResponse {
	llmResp := &llmtypes.LLMResponse{
		StopReason: resp.StopReason,
		Usage: llmtypes.Usage{
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			TotalTokens:              resp.Usage.InputTokens + resp.Usage.OutputTokens,
			CostUSD:                  c.calculateCost(resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadInputTokens, resp.Usage.CacheCreationInputTokens),
			CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
		},
		Metadata: map[string]interface{}{
			"model":       resp.Model,
			"stop_reason": resp.StopReason,
		},
	}

	// Extract content and tool calls
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			llmResp.Content += block.Text

		case "tool_use":
			llmResp.ToolCalls = append(llmResp.ToolCalls, llmtypes.ToolCall{
				ID:    block.ID,
				Name:  llm.ReverseToolName(c.toolNameMap, block.Name),
				Input: block.Input,
			})
		}
	}

	return llmResp
}

// calculateCost estimates the cost in USD based on token usage.
// Pricing as of 2025-01 for Claude claude-sonnet-4-6.
// Cache pricing: cache_creation at 1.25x input, cache_read at 0.10x input.
func (c *Client) calculateCost(inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int) float64 {
	// Claude claude-sonnet-4-6 pricing (2025-01):
	// Input: $3 per million tokens
	// Output: $15 per million tokens
	// Cache write (creation): $3.75 per million tokens (1.25x input)
	// Cache read: $0.30 per million tokens (0.10x input)
	inputCost := float64(inputTokens) * 3.0 / 1_000_000
	outputCost := float64(outputTokens) * 15.0 / 1_000_000
	cacheWriteCost := float64(cacheCreationTokens) * 3.75 / 1_000_000
	cacheReadCost := float64(cacheReadTokens) * 0.30 / 1_000_000
	return inputCost + outputCost + cacheWriteCost + cacheReadCost
}

// ChatStream implements token-by-token streaming for Anthropic.
// This method uses Anthropic's Messages API with stream=true to stream tokens
// as they are generated. The tokenCallback is called for each token received.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// 1. Build request body (extract system messages and convert to Anthropic format)
	systemPrompt, apiMessages := c.convertMessages(messages)
	c.toolNameMap = make(map[string]string)
	apiTools := c.convertTools(tools)

	req := &MessagesRequest{
		Model:       c.model,
		Messages:    apiMessages,
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
		Stream:      true, // Enable streaming
	}

	// Add system prompt blocks if present (Anthropic Messages API requires separate system field)
	if len(systemPrompt) > 0 {
		req.System = systemPrompt
	}

	if len(apiTools) > 0 {
		req.Tools = apiTools
	}

	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 2. Send request with rate limiting if enabled.
	// The lambda creates a fresh HTTP request on each attempt so the request body
	// can be re-read on a 429 retry. A 429 response is converted to an error so
	// the rate limiter's exponential-backoff retry logic fires automatically.
	buildStreamReq := func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("x-api-key", c.apiKey)
		r.Header.Set("anthropic-version", "2023-06-01")
		// Enable prompt caching beta — cached tokens don't count against Anthropic's ITPM rate limit
		r.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
		return r, nil
	}
	var httpResp *http.Response
	if c.rateLimiter != nil {
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			req, err := buildStreamReq(ctx)
			if err != nil {
				return nil, err
			}
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return nil, err
			}
			// Convert 429 to a retryable error so the rate limiter backs off and retries.
			if resp.StatusCode == http.StatusTooManyRequests {
				respBody, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				return nil, fmt.Errorf("API error (status 429): %s", string(respBody))
			}
			return resp, nil
		})
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
		httpResp = result.(*http.Response)
	} else {
		req, err := buildStreamReq(ctx)
		if err != nil {
			return nil, err
		}
		httpResp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
	}
	defer func() { _ = httpResp.Body.Close() }()

	// Check status code before streaming
	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// 3. Process Server-Sent Events (SSE) stream
	var contentBuffer strings.Builder
	usage := llmtypes.Usage{}
	var stopReason string
	tokenCount := 0
	var toolCalls []llmtypes.ToolCall
	// Track tool input JSON as it streams in (indexed by content block index)
	toolInputBuffers := make(map[int]*strings.Builder)
	// Map content block index → toolCalls slice index for tool_use blocks
	toolCallIndex := make(map[int]int)

	scanner := bufio.NewScanner(httpResp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "event: <event_type>" or "data: <json>"
		if strings.HasPrefix(line, "event:") {
			// Event type line, ignore for now
			continue
		}

		if strings.HasPrefix(line, "data:") {
			// Extract JSON data after "data: "
			jsonData := strings.TrimPrefix(line, "data: ")

			// Parse event
			var event StreamEvent
			if err := json.Unmarshal([]byte(jsonData), &event); err != nil {
				// Skip malformed events but continue processing
				continue
			}

			// Handle different event types
			switch event.Type {
			case "content_block_delta":
				if event.Delta == nil {
					continue
				}
				switch event.Delta.Type {
				case "text_delta":
					if event.Delta.Text != "" {
						token := event.Delta.Text
						contentBuffer.WriteString(token)
						tokenCount++

						// Call token callback (non-blocking)
						if tokenCallback != nil {
							tokenCallback(token)
						}
					}
				case "input_json_delta":
					// Accumulate tool input JSON fragments
					if buf, exists := toolInputBuffers[event.Index]; exists {
						buf.WriteString(event.Delta.PartialJSON)
					}
				}

			case "content_block_start":
				if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
					// Start tracking a new tool call
					idx := len(toolCalls)
					toolCalls = append(toolCalls, llmtypes.ToolCall{
						ID:    event.ContentBlock.ID,
						Name:  llm.ReverseToolName(c.toolNameMap, event.ContentBlock.Name),
						Input: make(map[string]interface{}),
					})
					// Initialize buffer for this tool's input JSON
					toolInputBuffers[event.Index] = &strings.Builder{}
					toolCallIndex[event.Index] = idx
				}

			case "content_block_stop":
				// Finalize tool input: parse accumulated JSON
				if buf, exists := toolInputBuffers[event.Index]; exists && buf.Len() > 0 {
					var input map[string]interface{}
					if err := json.Unmarshal([]byte(buf.String()), &input); err == nil {
						if idx, ok := toolCallIndex[event.Index]; ok && idx < len(toolCalls) {
							toolCalls[idx].Input = input
						}
					}
				}
				// Clean up buffers for this block
				delete(toolInputBuffers, event.Index)
				delete(toolCallIndex, event.Index)

			case "message_start":
				// Initial event: capture input tokens and cache token counts
				if event.Message != nil {
					usage.InputTokens = event.Message.Usage.InputTokens
					usage.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
					usage.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
				}

			case "message_delta":
				if event.Delta != nil && event.Delta.StopReason != "" {
					stopReason = event.Delta.StopReason
				}
				if event.Usage != nil {
					usage.OutputTokens = event.Usage.OutputTokens
				}

			case "message_stop":
				// Final event — usage may be updated here with cache tokens too
				if event.Usage != nil {
					if event.Usage.InputTokens > 0 {
						usage.InputTokens = event.Usage.InputTokens
					}
					if event.Usage.OutputTokens > 0 {
						usage.OutputTokens = event.Usage.OutputTokens
					}
					if event.Usage.CacheReadInputTokens > 0 {
						usage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
					}
					if event.Usage.CacheCreationInputTokens > 0 {
						usage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
					}
				}
			}

			// Check context cancellation
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading stream: %w", err)
	}

	// 4. Build final response
	if usage.OutputTokens == 0 {
		usage.OutputTokens = tokenCount
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	usage.CostUSD = c.calculateCost(usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens)

	// Record token usage for rate limiter metrics
	if c.rateLimiter != nil {
		totalTokens := int64(usage.InputTokens + usage.OutputTokens)
		c.rateLimiter.RecordTokenUsage(totalTokens)
	}

	return &llmtypes.LLMResponse{
		Content:    contentBuffer.String(),
		StopReason: stopReason,
		Usage:      usage,
		ToolCalls:  toolCalls,
		Metadata: map[string]interface{}{
			"model":       c.model,
			"stop_reason": stopReason,
			"streaming":   true,
		},
	}, nil
}

// callAPI makes the HTTP request to Anthropic's API.
func (c *Client) callAPI(ctx context.Context, req *MessagesRequest) (*MessagesResponse, error) {
	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Send request with rate limiting if enabled.
	// The lambda creates a fresh HTTP request on each attempt so the request body
	// can be re-read on a 429 retry. A 429 response is converted to an error so
	// the rate limiter's exponential-backoff retry logic fires automatically.
	buildAPIReq := func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("x-api-key", c.apiKey)
		r.Header.Set("anthropic-version", "2023-06-01")
		// Enable prompt caching beta — cached tokens don't count against Anthropic's ITPM rate limit
		r.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
		return r, nil
	}
	var httpResp *http.Response
	if c.rateLimiter != nil {
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			req, err := buildAPIReq(ctx)
			if err != nil {
				return nil, err
			}
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return nil, err
			}
			// Convert 429 to a retryable error so the rate limiter backs off and retries.
			if resp.StatusCode == http.StatusTooManyRequests {
				respBody, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				return nil, fmt.Errorf("API error (status 429): %s", string(respBody))
			}
			return resp, nil
		})
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
		httpResp = result.(*http.Response)
	} else {
		req, err := buildAPIReq(ctx)
		if err != nil {
			return nil, err
		}
		httpResp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
	}
	defer func() { _ = httpResp.Body.Close() }()

	// Read response
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check status code
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// Parse response
	var resp MessagesResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
