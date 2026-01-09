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

// Global singleton rate limiter shared across all OpenAI clients
var (
	globalRateLimiter     *llm.RateLimiter
	globalRateLimiterOnce sync.Once
)

// Client implements the LLMProvider interface for OpenAI's API.
type Client struct {
	apiKey      string
	model       string
	endpoint    string
	httpClient  *http.Client
	maxTokens   int
	temperature float64
	rateLimiter *llm.RateLimiter
}

// Config holds configuration for the OpenAI client.
type Config struct {
	APIKey            string
	Model             string        // Default: gpt-4o
	Endpoint          string        // Default: https://api.openai.com/v1/chat/completions
	Timeout           time.Duration // Default: 60s
	MaxTokens         int           // Default: 4096
	Temperature       float64       // Default: 1.0
	RateLimiterConfig llm.RateLimiterConfig
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

	// Convert tools to OpenAI format
	apiTools := c.convertTools(tools)

	// Build request
	req := &ChatCompletionRequest{
		Model:       c.model,
		Messages:    apiMessages,
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
	}

	if len(apiTools) > 0 {
		req.Tools = apiTools
		req.ToolChoice = "auto"
	}

	// Call API
	resp, err := c.callAPI(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("API call failed: %w", err)
	}

	// Convert response
	return c.convertResponse(resp), nil
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
					// Marshal input to JSON string
					argsJSON, err := json.Marshal(tc.Input)
					if err != nil {
						// Fallback to empty object
						argsJSON = []byte("{}")
					}

					toolCalls = append(toolCalls, ToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: FunctionCall{
							Name:      tc.Name,
							Arguments: string(argsJSON),
						},
					})
				}
				apiMsg.ToolCalls = toolCalls
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

	return apiMessages
}

// convertTools converts shuttle tools to OpenAI format.
func (c *Client) convertTools(tools []shuttle.Tool) []Tool {
	var apiTools []Tool

	for _, tool := range tools {
		apiTool := Tool{
			Type: "function",
			Function: FunctionDef{
				Name:        tool.Name(),
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
			itemMap := make(map[string]interface{})
			itemMap["type"] = schema.Items.Type
			if schema.Items.Description != "" {
				itemMap["description"] = schema.Items.Description
			}
			propMap["items"] = itemMap
		}

		result[key] = propMap
	}
	return result
}

// convertResponse converts OpenAI response to agent format.
func (c *Client) convertResponse(resp *ChatCompletionResponse) *llmtypes.LLMResponse {
	llmResp := &llmtypes.LLMResponse{
		Usage: llmtypes.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
			CostUSD:      c.calculateCost(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		},
		Metadata: map[string]interface{}{
			"model":         resp.Model,
			"finish_reason": resp.Choices[0].FinishReason,
		},
	}

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
				Name:  tc.Function.Name,
				Input: input,
			})
		}
	}

	return llmResp
}

// calculateCost estimates the cost in USD based on token usage.
// Pricing as of 2024-11 for various OpenAI models.
func (c *Client) calculateCost(inputTokens, outputTokens int) float64 {
	// Pricing per million tokens (as of 2024-11)
	var inputCostPerM, outputCostPerM float64

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
	default:
		// Default to gpt-4o pricing
		inputCostPerM = 2.50
		outputCostPerM = 10.00
	}

	inputCost := float64(inputTokens) * inputCostPerM / 1_000_000
	outputCost := float64(outputTokens) * outputCostPerM / 1_000_000
	return inputCost + outputCost
}

// ChatStream implements token-by-token streaming for OpenAI.
// This method uses OpenAI's Chat Completions API with stream=true to stream tokens
// as they are generated. The tokenCallback is called for each token received.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// 1. Build request body (reuse existing message and tool conversion)
	apiMessages := c.convertMessages(messages)
	apiTools := c.convertTools(tools)

	req := &ChatCompletionRequest{
		Model:       c.model,
		Messages:    apiMessages,
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
		Stream:      true, // Enable streaming
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

	// 2. Send request with rate limiting if enabled
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
	defer httpResp.Body.Close()

	// Check status code before streaming
	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// 3. Process Server-Sent Events (SSE) stream
	var contentBuffer strings.Builder
	usage := llmtypes.Usage{}
	var finishReason string
	tokenCount := 0
	var toolCalls []llmtypes.ToolCall
	toolCallMap := make(map[int]*llmtypes.ToolCall) // Track tool calls by index

	scanner := bufio.NewScanner(httpResp.Body)
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

			// Extract tool call deltas
			if len(choice.Delta.ToolCalls) > 0 {
				for _, tcDelta := range choice.Delta.ToolCalls {
					idx := tcDelta.Index
					if _, exists := toolCallMap[idx]; !exists {
						// New tool call
						toolCallMap[idx] = &llmtypes.ToolCall{
							ID:    tcDelta.ID,
							Name:  tcDelta.Function.Name,
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
		}

		// Check context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	if err := scanner.Err(); err != nil {
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
	usage.CostUSD = c.calculateCost(usage.InputTokens, usage.OutputTokens)

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

	return &llmtypes.LLMResponse{
		Content:    contentBuffer.String(),
		StopReason: stopReason,
		Usage:      usage,
		ToolCalls:  toolCalls,
		Metadata: map[string]interface{}{
			"model":         c.model,
			"finish_reason": finishReason,
			"streaming":     true,
		},
	}, nil
}

// callAPI makes the HTTP request to OpenAI's API.
func (c *Client) callAPI(ctx context.Context, req *ChatCompletionRequest) (*ChatCompletionResponse, error) {
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

	// Send request with rate limiting if enabled
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
	defer httpResp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var resp ChatCompletionResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for API errors
	if resp.Error != nil {
		return nil, fmt.Errorf("OpenAI API error: %s (type: %s)", resp.Error.Message, resp.Error.Type)
	}

	// Check status code
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	return &resp, nil
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
