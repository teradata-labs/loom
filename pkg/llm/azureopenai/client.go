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
package azureopenai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/openai"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Global singleton rate limiter shared across all Azure OpenAI clients
var (
	globalRateLimiter     *llm.RateLimiter
	globalRateLimiterOnce sync.Once
)

// Client implements the LLMProvider interface for Azure OpenAI.
// Azure OpenAI uses deployment-based routing and supports dual authentication.
type Client struct {
	// Azure-specific configuration
	endpoint     string // https://{resource}.openai.azure.com
	deploymentID string // User's deployment name (not model name)
	apiVersion   string // API version (e.g., "2024-10-21")

	// Authentication (use one or the other)
	apiKey     string // Option 1: API key authentication
	entraToken string // Option 2: Microsoft Entra ID token

	// HTTP client
	httpClient *http.Client

	// Model configuration
	maxTokens   int
	temperature float64

	// Model name for cost calculation (inferred from deployment)
	modelName string

	// Rate limiter
	rateLimiter *llm.RateLimiter

	// Tool name mapping: sanitized name → original name
	// Azure OpenAI requires tool names to match ^[a-zA-Z0-9_.\-]+$
	// MCP tools use colon namespacing (e.g., "vantage-mcp:execute_sql")
	toolNameMap map[string]string
}

// Config holds configuration for the Azure OpenAI client.
type Config struct {
	// Required: Azure OpenAI endpoint
	// Format: https://{resource-name}.openai.azure.com
	Endpoint string

	// Required: Deployment ID (your deployment name, not the model name)
	DeploymentID string

	// API version (default: "2024-10-21")
	APIVersion string

	// Authentication: Use ONE of these
	APIKey     string // Option 1: API key (from Azure portal)
	EntraToken string // Option 2: Microsoft Entra ID token

	// Optional: Model name for cost calculation
	// If not provided, attempts to infer from deployment ID
	ModelName string

	// Request configuration
	MaxTokens         int           // Default: 4096
	Temperature       float64       // Default: 1.0
	Timeout           time.Duration // Default: 60s
	RateLimiterConfig llm.RateLimiterConfig
}

// NewClient creates a new Azure OpenAI client.
func NewClient(config Config) (*Client, error) {
	// Validate required fields
	if config.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if config.DeploymentID == "" {
		return nil, fmt.Errorf("deployment ID is required")
	}

	// Require at least one authentication method
	if config.APIKey == "" && config.EntraToken == "" {
		return nil, fmt.Errorf("either APIKey or EntraToken must be provided")
	}

	// Set defaults
	if config.APIVersion == "" {
		config.APIVersion = "2024-10-21"
	}
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 4096
	}
	if config.Temperature == 0 {
		config.Temperature = 1.0
	}

	// Infer model name from deployment ID if not provided
	modelName := config.ModelName
	if modelName == "" {
		// Try to infer from deployment ID (common naming: gpt-4o-deployment -> gpt-4o)
		modelName = inferModelFromDeployment(config.DeploymentID)
	}

	// Initialize rate limiter if enabled
	var rateLimiter *llm.RateLimiter
	if config.RateLimiterConfig.Enabled {
		rateLimiter = getOrCreateGlobalRateLimiter(config.RateLimiterConfig)
	}

	return &Client{
		endpoint:     config.Endpoint,
		deploymentID: config.DeploymentID,
		apiVersion:   config.APIVersion,
		apiKey:       config.APIKey,
		entraToken:   config.EntraToken,
		maxTokens:    config.MaxTokens,
		temperature:  config.Temperature,
		modelName:    modelName,
		rateLimiter:  rateLimiter,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
	}, nil
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
	return "azure-openai"
}

// Model returns the model identifier (deployment ID).
func (c *Client) Model() string {
	return c.deploymentID
}

// Chat sends a conversation to Azure OpenAI and returns the response.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Convert messages to OpenAI format (Azure uses same structure)
	apiMessages := convertMessages(messages)

	// Convert tools to OpenAI format with name sanitization
	// Azure OpenAI requires names matching ^[a-zA-Z0-9_.\-]+$ (no colons)
	c.toolNameMap = make(map[string]string)
	apiTools := convertTools(tools, c.toolNameMap)

	// Sanitize tool schemas to remove problematic fields
	// Azure OpenAI is strict about: empty arrays, empty defaults, etc.
	apiTools = SanitizeToolSchemas(apiTools)

	// Build request (same as OpenAI)
	req := &openai.ChatCompletionRequest{
		Model:       c.deploymentID, // Azure ignores this but include for completeness
		Messages:    apiMessages,
		Temperature: c.temperature,
	}

	// Azure OpenAI: Newer models (gpt-4o, gpt-4-turbo-2024-04-09+) require max_completion_tokens
	// Older models (gpt-4, gpt-35-turbo) require max_tokens
	if c.usesMaxCompletionTokens() {
		req.MaxCompletionTokens = c.maxTokens
	} else {
		req.MaxTokens = c.maxTokens
	}

	if len(apiTools) > 0 {
		req.Tools = apiTools
		req.ToolChoice = "auto"
	}

	// Call Azure OpenAI API
	resp, err := c.callAPI(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("API call failed: %w", err)
	}

	// Convert response with tool name reverse mapping
	return c.convertResponse(resp), nil
}

// callAPI makes the HTTP request to Azure OpenAI's API.
func (c *Client) callAPI(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	// Build Azure-specific URL
	// Format: https://{endpoint}/openai/deployments/{deployment-id}/chat/completions?api-version={version}
	apiURL := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		c.endpoint,
		url.PathEscape(c.deploymentID),
		url.QueryEscape(c.apiVersion),
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

	// Authentication: Use api-key OR Authorization header
	if c.apiKey != "" {
		// Option 1: API key authentication
		httpReq.Header.Set("api-key", c.apiKey)
	} else {
		// Option 2: Microsoft Entra ID token
		httpReq.Header.Set("Authorization", "Bearer "+c.entraToken)
	}

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
	var resp openai.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for API errors
	if resp.Error != nil {
		return nil, fmt.Errorf("Azure OpenAI API error: %s (type: %s)", resp.Error.Message, resp.Error.Type)
	}

	// Check status code
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	return &resp, nil
}

// convertResponse converts OpenAI response to agent format.
func (c *Client) convertResponse(resp *openai.ChatCompletionResponse) *llmtypes.LLMResponse {
	llmResp := &llmtypes.LLMResponse{
		Usage: llmtypes.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
			CostUSD:      c.calculateCost(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		},
		Metadata: map[string]interface{}{
			"model":         resp.Model,
			"deployment":    c.deploymentID,
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

		// Extract tool calls with reverse name mapping
		for _, tc := range choice.Message.ToolCalls {
			// Parse arguments JSON string back to map
			var input map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				// If parsing fails, store as raw string
				input = map[string]interface{}{
					"_raw": tc.Function.Arguments,
				}
			}

			// Map sanitized name back to original name
			toolName := llm.ReverseToolName(c.toolNameMap, tc.Function.Name)

			llmResp.ToolCalls = append(llmResp.ToolCalls, llmtypes.ToolCall{
				ID:    tc.ID,
				Name:  toolName,
				Input: input,
			})
		}
	}

	return llmResp
}

// calculateCost estimates the cost in USD based on token usage.
// Azure OpenAI pricing varies by region and deployment type.
// These are approximate costs based on Pay-As-You-Go pricing.
func (c *Client) calculateCost(inputTokens, outputTokens int) float64 {
	// Pricing per million tokens (approximate, varies by region)
	var inputCostPerM, outputCostPerM float64

	switch c.modelName {
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
	case "gpt-35-turbo", "gpt-3.5-turbo": // Azure uses gpt-35-turbo naming
		inputCostPerM = 0.50
		outputCostPerM = 1.50
	default:
		// Default to gpt-4o pricing
		inputCostPerM = 2.50
		outputCostPerM = 10.00
	}

	inputCost := float64(inputTokens) * inputCostPerM / 1_000_000
	outputCost := float64(outputTokens) * outputCostPerM / 1_000_000
	return inputCost + outputCost
}

// usesMaxCompletionTokens returns true if this deployment requires max_completion_tokens
// instead of max_tokens.
//
// Azure OpenAI behavior:
// - API version 2024-08-01-preview and later: uses max_completion_tokens
// - Newer models (gpt-4o, gpt-5+, o1, o3, etc.): use max_completion_tokens
// - Older models (gpt-4, gpt-35-turbo): use max_tokens
//
// Strategy: Default to max_completion_tokens (forward-compatible) unless we detect
// an explicitly old model that requires max_tokens.
func (c *Client) usesMaxCompletionTokens() bool {
	// Check API version (2024-08-01-preview and later always use max_completion_tokens)
	if c.apiVersion >= "2024-08-01" {
		return true
	}

	// Check if this is an old model that explicitly requires max_tokens
	// These are the only models that DON'T support max_completion_tokens:
	oldModels := []string{
		"gpt-4-0613",    // Original GPT-4
		"gpt-4-32k",     // GPT-4 32k context
		"gpt-35-turbo",  // GPT-3.5-turbo (Azure naming)
		"gpt-3.5-turbo", // GPT-3.5-turbo (OpenAI naming)
	}

	modelLower := toLower(c.modelName)
	for _, oldModel := range oldModels {
		// Exact match or starts with the old model name
		if modelLower == toLower(oldModel) || contains(modelLower, toLower(oldModel)) {
			return false // Use max_tokens for old models
		}
	}

	// Default to max_completion_tokens for all other models (forward-compatible)
	// This covers: gpt-4o, gpt-4o-mini, gpt-5+, o1, o3, gpt-4-turbo, and future models
	return true
}

// inferModelFromDeployment attempts to infer the model name from deployment ID.
// Common patterns: "gpt-4o-deployment" -> "gpt-4o", "my-gpt4-turbo" -> "gpt-4-turbo"
func inferModelFromDeployment(deploymentID string) string {
	// Common model prefixes
	models := []string{
		"gpt-4o-mini",
		"gpt-4o",
		"gpt-4-turbo",
		"gpt-4",
		"gpt-35-turbo",
		"gpt-3.5-turbo",
	}

	// Check if deployment ID contains any known model name
	for _, model := range models {
		if contains(deploymentID, model) {
			return model
		}
	}

	// Fallback: return deployment ID as-is
	return deploymentID
}

// contains checks if s contains substr (case-insensitive).
func contains(s, substr string) bool {
	// Simple case-insensitive check
	sLower := toLower(s)
	substrLower := toLower(substr)
	return indexOf(sLower, substrLower) >= 0
}

func toLower(s string) string {
	result := ""
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			result += string(r + 32)
		} else {
			result += string(r)
		}
	}
	return result
}

func indexOf(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// Conversion helpers (reuse OpenAI logic)

func convertMessages(messages []llmtypes.Message) []openai.ChatMessage {
	var apiMessages []openai.ChatMessage

	for _, msg := range messages {
		switch msg.Role {
		case "system", "user":
			apiMessages = append(apiMessages, openai.ChatMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})

		case "assistant":
			apiMsg := openai.ChatMessage{
				Role: "assistant",
			}

			if msg.Content != "" {
				apiMsg.Content = msg.Content
			}

			if len(msg.ToolCalls) > 0 {
				var toolCalls []openai.ToolCall
				for _, tc := range msg.ToolCalls {
					argsJSON, err := json.Marshal(tc.Input)
					if err != nil {
						argsJSON = []byte("{}")
					}

					toolCalls = append(toolCalls, openai.ToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: openai.FunctionCall{
							Name:      llm.SanitizeToolName(tc.Name),
							Arguments: string(argsJSON),
						},
					})
				}
				apiMsg.ToolCalls = toolCalls
			}

			apiMessages = append(apiMessages, apiMsg)

		case "tool":
			apiMessages = append(apiMessages, openai.ChatMessage{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolUseID,
			})
		}
	}

	return apiMessages
}

func convertTools(tools []shuttle.Tool, nameMap map[string]string) []openai.Tool {
	var apiTools []openai.Tool

	for _, tool := range tools {
		originalName := tool.Name()
		sanitizedName := llm.SanitizeToolName(originalName)

		// Store mapping for reverse lookup in responses
		if nameMap != nil {
			nameMap[sanitizedName] = originalName
		}

		apiTool := openai.Tool{
			Type: "function",
			Function: openai.FunctionDef{
				Name:        sanitizedName,
				Description: tool.Description(),
			},
		}

		schema := tool.InputSchema()
		if schema != nil {
			params := make(map[string]interface{})
			params["type"] = schema.Type
			if schema.Type == "" {
				params["type"] = "object"
			}

			// Add properties only if they exist (same as regular OpenAI)
			if schema.Properties != nil {
				params["properties"] = convertSchemaProperties(schema.Properties)
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

func convertSchemaProperties(props map[string]*shuttle.JSONSchema) map[string]interface{} {
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
		// Add properties/items only if they exist (same as regular OpenAI)
		if schema.Properties != nil {
			propMap["properties"] = convertSchemaProperties(schema.Properties)
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

// ChatStream implements token-by-token streaming for Azure OpenAI.
// This delegates to OpenAI's streaming logic since Azure OpenAI uses the same API format.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// Build request (same as non-streaming)
	apiMessages := convertMessages(messages)
	c.toolNameMap = make(map[string]string)
	apiTools := convertTools(tools, c.toolNameMap)

	// Sanitize tool schemas (same as non-streaming)
	apiTools = SanitizeToolSchemas(apiTools)

	req := &openai.ChatCompletionRequest{
		Model:       c.deploymentID,
		Messages:    apiMessages,
		Temperature: c.temperature,
		Stream:      true, // Enable streaming
	}

	// Azure OpenAI: Newer models require max_completion_tokens instead of max_tokens
	if c.usesMaxCompletionTokens() {
		req.MaxCompletionTokens = c.maxTokens
	} else {
		req.MaxTokens = c.maxTokens
	}

	if len(apiTools) > 0 {
		req.Tools = apiTools
		req.ToolChoice = "auto"
	}

	// Build Azure-specific URL
	apiURL := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
		c.endpoint,
		url.PathEscape(c.deploymentID),
		url.QueryEscape(c.apiVersion),
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
	if c.apiKey != "" {
		httpReq.Header.Set("api-key", c.apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+c.entraToken)
	}

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

	// Process SSE stream (same logic as OpenAI)
	var contentBuffer strings.Builder
	usage := llmtypes.Usage{}
	var finishReason string
	tokenCount := 0
	var toolCalls []llmtypes.ToolCall
	toolCallMap := make(map[int]*llmtypes.ToolCall)

	scanner := bufio.NewScanner(httpResp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")

		if jsonData == "[DONE]" {
			break
		}

		var chunk openai.ChatCompletionStreamChunk
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			if choice.Delta.Content != nil {
				if str, ok := choice.Delta.Content.(string); ok && str != "" {
					token := str
					contentBuffer.WriteString(token)
					tokenCount++

					if tokenCallback != nil {
						tokenCallback(token)
					}
				}
			}

			if len(choice.Delta.ToolCalls) > 0 {
				for _, tcDelta := range choice.Delta.ToolCalls {
					idx := tcDelta.Index
					if _, exists := toolCallMap[idx]; !exists {
						// Map sanitized name back to original
						toolName := llm.ReverseToolName(c.toolNameMap, tcDelta.Function.Name)
						toolCallMap[idx] = &llmtypes.ToolCall{
							ID:    tcDelta.ID,
							Name:  toolName,
							Input: make(map[string]interface{}),
						}
					}

					if tcDelta.Function.Arguments != "" {
						tc := toolCallMap[idx]
						if existingArgs, ok := tc.Input["_args"].(string); ok {
							tc.Input["_args"] = existingArgs + tcDelta.Function.Arguments
						} else {
							tc.Input["_args"] = tcDelta.Function.Arguments
						}
					}
				}
			}

			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}

		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
			usage.TotalTokens = chunk.Usage.TotalTokens
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading stream: %w", err)
	}

	// Parse accumulated tool call arguments
	for _, tc := range toolCallMap {
		if argsStr, ok := tc.Input["_args"].(string); ok {
			var parsedArgs map[string]interface{}
			if err := json.Unmarshal([]byte(argsStr), &parsedArgs); err != nil {
				parsedArgs = map[string]interface{}{
					"_raw": argsStr,
				}
			}
			tc.Input = parsedArgs
		}
		toolCalls = append(toolCalls, *tc)
	}

	// Build final response
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
			"deployment":    c.deploymentID,
			"finish_reason": finishReason,
			"streaming":     true,
		},
	}, nil
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
