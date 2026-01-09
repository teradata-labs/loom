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
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Global singleton rate limiter shared across all Gemini clients
var (
	globalRateLimiter     *llm.RateLimiter
	globalRateLimiterOnce sync.Once
)

// Client implements the LLMProvider interface for Google Gemini.
type Client struct {
	apiKey      string
	model       string
	httpClient  *http.Client
	maxTokens   int
	temperature float64
	rateLimiter *llm.RateLimiter
}

// Config holds configuration for the Gemini client.
type Config struct {
	// Required: Gemini API key from https://makersuite.google.com/
	APIKey string

	// Model to use (default: "gemini-2.5-flash")
	// Available models:
	// - gemini-3-pro-preview: Most intelligent, $2-4/$12-18 per 1M tokens
	// - gemini-2.5-pro: Complex reasoning, $1.25-2.50/$10-15 per 1M tokens
	// - gemini-2.5-flash: Best price/performance, $0.30/$2.50 per 1M tokens
	// - gemini-2.5-flash-lite: Fastest/cheapest, similar to Flash pricing
	Model string

	// Optional configuration
	MaxTokens         int           // Default: 8192
	Temperature       float64       // Default: 1.0
	Timeout           time.Duration // Default: 60s
	RateLimiterConfig llm.RateLimiterConfig
}

// NewClient creates a new Google Gemini client.
func NewClient(config Config) *Client {
	// Set defaults
	if config.Model == "" {
		config.Model = "gemini-2.5-flash"
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 8192
	}
	if config.Temperature == 0 {
		config.Temperature = 1.0
	}
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}

	// Initialize rate limiter if enabled
	var rateLimiter *llm.RateLimiter
	if config.RateLimiterConfig.Enabled {
		rateLimiter = getOrCreateGlobalRateLimiter(config.RateLimiterConfig)
	}

	return &Client{
		apiKey:      config.APIKey,
		model:       config.Model,
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
	return "gemini"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.model
}

// Chat sends a conversation to Google Gemini and returns the response.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Convert messages to Gemini format
	contents := convertMessages(messages)

	// Convert tools to Gemini format
	var functionDeclarations []FunctionDeclaration
	if len(tools) > 0 {
		functionDeclarations = convertTools(tools)
	}

	// Build request
	req := &GenerateContentRequest{
		Contents: contents,
		GenerationConfig: GenerationConfig{
			Temperature:     c.temperature,
			MaxOutputTokens: c.maxTokens,
		},
	}

	if len(functionDeclarations) > 0 {
		req.Tools = []Tool{
			{
				FunctionDeclarations: functionDeclarations,
			},
		}
	}

	// Call Gemini API
	resp, err := c.callAPI(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("API call failed: %w", err)
	}

	// Convert response
	return c.convertResponse(resp), nil
}

// callAPI makes the HTTP request to Gemini's API.
func (c *Client) callAPI(ctx context.Context, req *GenerateContentRequest) (*GenerateContentResponse, error) {
	// Build URL
	// Format: https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={apiKey}
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		c.model,
		c.apiKey,
	)

	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")

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

	// Check status code
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// Parse response
	var resp GenerateContentResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for API errors
	if resp.Error != nil {
		return nil, fmt.Errorf("Gemini API error: %s (code: %d)", resp.Error.Message, resp.Error.Code)
	}

	return &resp, nil
}

// convertResponse converts Gemini response to agent format.
func (c *Client) convertResponse(resp *GenerateContentResponse) *llmtypes.LLMResponse {
	llmResp := &llmtypes.LLMResponse{
		Usage: llmtypes.Usage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:  resp.UsageMetadata.TotalTokenCount,
			CostUSD:      c.calculateCost(resp.UsageMetadata.PromptTokenCount, resp.UsageMetadata.CandidatesTokenCount),
		},
		Metadata: map[string]interface{}{
			"provider": "gemini",
			"model":    c.model,
		},
	}

	if len(resp.Candidates) > 0 {
		candidate := resp.Candidates[0]

		// Map finish_reason to stop_reason
		switch candidate.FinishReason {
		case "STOP":
			llmResp.StopReason = "end_turn"
		case "MAX_TOKENS":
			llmResp.StopReason = "max_tokens"
		case "SAFETY":
			llmResp.StopReason = "content_filter"
		case "RECITATION":
			llmResp.StopReason = "content_filter"
		default:
			llmResp.StopReason = candidate.FinishReason
		}

		// Extract content and tool calls
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				llmResp.Content += part.Text
			}

			if part.FunctionCall != nil {
				llmResp.StopReason = "tool_use"
				llmResp.ToolCalls = append(llmResp.ToolCalls, llmtypes.ToolCall{
					ID:    part.FunctionCall.Name, // Gemini doesn't provide call IDs
					Name:  part.FunctionCall.Name,
					Input: part.FunctionCall.Args,
				})
			}
		}
	}

	return llmResp
}

// calculateCost estimates the cost in USD based on token usage.
// Pricing as of 2025-01 (per million tokens):
//
// Gemini 3 Pro Preview:
// - Input: $2.00-$4.00 (varies by tier)
// - Output: $12.00-$18.00
//
// Gemini 2.5 Pro:
// - Input: $1.25-$2.50
// - Output: $10.00-$15.00
//
// Gemini 2.5 Flash:
// - Input: $0.30
// - Output: $2.50
//
// Gemini 2.5 Flash-Lite:
// - Input: ~$0.30
// - Output: ~$2.50
//
// Note: Prices may vary. Check https://ai.google.dev/pricing for current rates.
func (c *Client) calculateCost(inputTokens, outputTokens int) float64 {
	var inputCostPerM, outputCostPerM float64

	switch c.model {
	case "gemini-3-pro-preview", "gemini-3-pro":
		// Use mid-range pricing
		inputCostPerM = 3.00
		outputCostPerM = 15.00

	case "gemini-2.5-pro":
		// Use mid-range pricing
		inputCostPerM = 1.875
		outputCostPerM = 12.50

	case "gemini-2.5-flash":
		inputCostPerM = 0.30
		outputCostPerM = 2.50

	case "gemini-2.5-flash-lite":
		inputCostPerM = 0.30
		outputCostPerM = 2.50

	default:
		// Default to Flash pricing for unknown models
		inputCostPerM = 0.30
		outputCostPerM = 2.50
	}

	inputCost := float64(inputTokens) * inputCostPerM / 1_000_000
	outputCost := float64(outputTokens) * outputCostPerM / 1_000_000
	return inputCost + outputCost
}

// Conversion helpers

func convertMessages(messages []llmtypes.Message) []Content {
	var contents []Content

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// Gemini doesn't have a system role, prepend as user message
			contents = append(contents, Content{
				Role: "user",
				Parts: []Part{
					{Text: "System instruction: " + msg.Content},
				},
			})

		case "user":
			// Check if message has ContentBlocks (multi-modal content with images)
			if len(msg.ContentBlocks) > 0 {
				// Convert content blocks to Gemini parts
				var parts []Part
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						parts = append(parts, Part{Text: block.Text})
					case "image":
						if block.Image != nil && block.Image.Source.Type == "base64" {
							// Gemini uses inlineData for images
							parts = append(parts, Part{
								InlineData: &InlineData{
									MimeType: block.Image.Source.MediaType,
									Data:     block.Image.Source.Data,
								},
							})
						}
						// Note: Gemini doesn't support URL-based images directly
					}
				}
				contents = append(contents, Content{
					Role:  "user",
					Parts: parts,
				})
			} else {
				// Fallback to plain text (backward compatible)
				contents = append(contents, Content{
					Role: "user",
					Parts: []Part{
						{Text: msg.Content},
					},
				})
			}

		case "assistant":
			parts := []Part{}

			if msg.Content != "" {
				parts = append(parts, Part{Text: msg.Content})
			}

			// Add tool calls as function call parts
			for _, tc := range msg.ToolCalls {
				parts = append(parts, Part{
					FunctionCall: &FunctionCall{
						Name: tc.Name,
						Args: tc.Input,
					},
				})
			}

			contents = append(contents, Content{
				Role:  "model", // Gemini uses "model" instead of "assistant"
				Parts: parts,
			})

		case "tool":
			// Tool results as function response
			contents = append(contents, Content{
				Role: "function", // Gemini uses "function" for tool results
				Parts: []Part{
					{
						FunctionResponse: &FunctionResponse{
							Name: msg.ToolUseID, // Use tool ID as function name
							Response: map[string]interface{}{
								"result": msg.Content,
							},
						},
					},
				},
			})
		}
	}

	return contents
}

func convertTools(tools []shuttle.Tool) []FunctionDeclaration {
	var declarations []FunctionDeclaration

	for _, tool := range tools {
		decl := FunctionDeclaration{
			Name:        tool.Name(),
			Description: tool.Description(),
		}

		schema := tool.InputSchema()
		if schema != nil {
			params := Schema{
				Type:       schema.Type,
				Properties: convertSchemaProperties(schema.Properties),
				Required:   schema.Required,
			}
			if params.Type == "" {
				params.Type = "object"
			}
			decl.Parameters = params
		}

		declarations = append(declarations, decl)
	}

	return declarations
}

func convertSchemaProperties(props map[string]*shuttle.JSONSchema) map[string]Schema {
	if props == nil {
		return nil
	}

	result := make(map[string]Schema)
	for key, schema := range props {
		s := Schema{
			Type:        schema.Type,
			Description: schema.Description,
			Enum:        schema.Enum,
		}

		if schema.Properties != nil {
			s.Properties = convertSchemaProperties(schema.Properties)
		}

		if schema.Items != nil {
			s.Items = &Schema{
				Type:        schema.Items.Type,
				Description: schema.Items.Description,
			}
		}

		result[key] = s
	}

	return result
}

// ChatStream implements token-by-token streaming for Google Gemini.
// This method uses Gemini's streamGenerateContent endpoint to stream tokens
// as they are generated. The tokenCallback is called for each token received.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// 1. Build request body (reuse existing message and tool conversion)
	contents := convertMessages(messages)
	var functionDeclarations []FunctionDeclaration
	if len(tools) > 0 {
		functionDeclarations = convertTools(tools)
	}

	req := &GenerateContentRequest{
		Contents: contents,
		GenerationConfig: GenerationConfig{
			Temperature:     c.temperature,
			MaxOutputTokens: c.maxTokens,
		},
	}

	if len(functionDeclarations) > 0 {
		req.Tools = []Tool{
			{
				FunctionDeclarations: functionDeclarations,
			},
		}
	}

	// 2. Build streaming URL (uses :streamGenerateContent instead of :generateContent)
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?key=%s&alt=sse",
		c.model,
		c.apiKey,
	)

	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")

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

	// Check status code before streaming
	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// 3. Process Server-Sent Events stream
	var contentBuffer strings.Builder
	usage := llmtypes.Usage{}
	var finishReason string
	tokenCount := 0
	var toolCalls []llmtypes.ToolCall

	scanner := bufio.NewScanner(httpResp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "data: <json>"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		// Extract JSON data after "data: "
		jsonData := strings.TrimPrefix(line, "data: ")

		// Parse streaming chunk
		var chunk GenerateContentResponse
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			// Skip malformed chunks but continue processing
			continue
		}

		// Check for API errors
		if chunk.Error != nil {
			return nil, fmt.Errorf("Gemini API error: %s (code: %d)", chunk.Error.Message, chunk.Error.Code)
		}

		if len(chunk.Candidates) > 0 {
			candidate := chunk.Candidates[0]

			// Extract text content from parts
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					token := part.Text
					contentBuffer.WriteString(token)
					tokenCount++

					// Call token callback (non-blocking)
					if tokenCallback != nil {
						tokenCallback(token)
					}
				}

				// Extract tool calls
				if part.FunctionCall != nil {
					toolCalls = append(toolCalls, llmtypes.ToolCall{
						ID:    part.FunctionCall.Name, // Gemini doesn't provide call IDs, use name
						Name:  part.FunctionCall.Name,
						Input: part.FunctionCall.Args,
					})
				}
			}

			// Extract finish reason
			if candidate.FinishReason != "" {
				finishReason = candidate.FinishReason
			}
		}

		// Extract usage metadata
		if chunk.UsageMetadata.TotalTokenCount > 0 {
			usage.InputTokens = chunk.UsageMetadata.PromptTokenCount
			usage.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
			usage.TotalTokens = chunk.UsageMetadata.TotalTokenCount
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

	// 4. Build final response
	if usage.TotalTokens == 0 {
		usage.OutputTokens = tokenCount
		usage.TotalTokens = tokenCount
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
	case "STOP":
		stopReason = "end_turn"
	case "MAX_TOKENS":
		stopReason = "max_tokens"
	case "SAFETY", "RECITATION":
		stopReason = "content_filter"
	default:
		stopReason = finishReason
	}

	// Set stop reason for tool calls
	if len(toolCalls) > 0 {
		stopReason = "tool_use"
	}

	return &llmtypes.LLMResponse{
		Content:    contentBuffer.String(),
		StopReason: stopReason,
		Usage:      usage,
		ToolCalls:  toolCalls,
		Metadata: map[string]interface{}{
			"provider":  "gemini",
			"model":     c.model,
			"streaming": true,
		},
	}, nil
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
