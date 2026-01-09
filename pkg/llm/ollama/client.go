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
package ollama

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

// Global singleton rate limiter shared across all Ollama clients
var (
	globalRateLimiter     *llm.RateLimiter
	globalRateLimiterOnce sync.Once
)

// Client implements the LLMProvider interface for Ollama.
type Client struct {
	endpoint    string
	model       string
	httpClient  *http.Client
	maxTokens   int
	temperature float64
	toolMode    ToolMode
	rateLimiter *llm.RateLimiter
}

// Models known to support native tool calling (Ollama v0.12.3+)
var toolSupportedModels = map[string]bool{
	"llama3.3":      true,
	"llama3.2":      true,
	"llama3.1":      true,
	"qwen2.5":       true,
	"qwen2.5-coder": true,
	"mistral":       true,
	"mixtral":       true,
	"deepseek-r1":   true,
	"functionary":   true,
}

// ToolMode defines how tools are handled.
type ToolMode string

const (
	// ToolModeAuto automatically detects if the model supports native tool calling
	ToolModeAuto ToolMode = "auto"
	// ToolModeNative uses Ollama's native tool calling API (requires Ollama v0.12.3+)
	ToolModeNative ToolMode = "native"
	// ToolModePrompt uses prompt engineering to simulate tool calling
	ToolModePrompt ToolMode = "prompt"
)

// Config holds configuration for the Ollama client.
type Config struct {
	Endpoint          string        // Default: http://localhost:11434
	Model             string        // Required: e.g., llama3.1, mistral, qwen2.5-coder
	MaxTokens         int           // Default: model-aware (4096 for 7B/8B, 6144 for 13B-32B, 8192 for 70B+)
	Temperature       float64       // Default: 0.8
	Timeout           time.Duration // Default: 120s
	ToolMode          ToolMode      // Default: auto (detect native support)
	RateLimiterConfig llm.RateLimiterConfig
}

// getDefaultMaxTokens returns intelligent max_tokens based on model name.
// Smaller models (7B-8B) benefit from shorter outputs, while larger models can handle more.
func getDefaultMaxTokens(model string) int {
	modelLower := strings.ToLower(model)

	// Large models (70B+ parameters) - full capacity
	// These are explicit large models
	if strings.Contains(modelLower, "70b") || strings.Contains(modelLower, "72b") ||
		strings.Contains(modelLower, "405b") ||
		strings.Contains(modelLower, "claude") || strings.Contains(modelLower, "gpt-4") {
		return 8192 // 8K for large models
	}

	// Medium models (13B-32B parameters) - balanced outputs
	if strings.Contains(modelLower, "13b") || strings.Contains(modelLower, "14b") ||
		strings.Contains(modelLower, "20b") || strings.Contains(modelLower, "32b") {
		return 6144 // 6K for medium models
	}

	// Small models (7B-8B parameters) or unknown - conservative default
	// This includes: llama3.1:8b, qwen2.5:7b, gemma, phi, and base names without size
	return 4096 // 4K for small models (safe default)
}

// NewClient creates a new Ollama client.
func NewClient(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://localhost:11434"
	}
	if cfg.Model == "" {
		cfg.Model = "llama3.1"
	}
	if cfg.MaxTokens == 0 {
		// Use model-aware default instead of fixed 4096
		cfg.MaxTokens = getDefaultMaxTokens(cfg.Model)
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 0.8
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.ToolMode == "" {
		cfg.ToolMode = ToolModeAuto
	}

	// Initialize rate limiter if enabled
	var rateLimiter *llm.RateLimiter
	if cfg.RateLimiterConfig.Enabled {
		rateLimiter = getOrCreateGlobalRateLimiter(cfg.RateLimiterConfig)
	}

	return &Client{
		endpoint:    cfg.Endpoint,
		model:       cfg.Model,
		maxTokens:   cfg.MaxTokens,
		temperature: cfg.Temperature,
		toolMode:    cfg.ToolMode,
		rateLimiter: rateLimiter,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
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
	return "ollama"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.model
}

// supportsNativeTools checks if the model supports native tool calling.
func (c *Client) supportsNativeTools() bool {
	// Check if explicitly set to native or prompt mode
	if c.toolMode == ToolModeNative {
		return true
	}
	if c.toolMode == ToolModePrompt {
		return false
	}

	// Auto mode: check model compatibility
	// Extract base model name (remove version/variant suffixes)
	model := c.model
	for baseModel := range toolSupportedModels {
		if len(model) >= len(baseModel) && model[:len(baseModel)] == baseModel {
			return true
		}
	}
	return false
}

// Chat sends a conversation to Ollama and returns the response.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Convert messages to Ollama format
	apiMessages := c.convertMessages(messages)

	// Build request
	req := chatRequest{
		Model:    c.model,
		Messages: apiMessages,
		Stream:   false,
		Options: map[string]interface{}{
			"temperature": c.temperature,
			"num_predict": c.maxTokens,
		},
	}

	// Add tools if native support is available
	if c.supportsNativeTools() && len(tools) > 0 {
		req.Tools = c.convertTools(tools)
	}

	// Call API
	resp, err := c.callAPI(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ollama API call failed: %w", err)
	}

	// Convert response
	return c.convertResponse(resp), nil
}

// convertTools converts shuttle.Tool to Ollama tool format.
func (c *Client) convertTools(tools []shuttle.Tool) []ollamaTool {
	ollamaTools := make([]ollamaTool, len(tools))
	for i, tool := range tools {
		ollamaTools[i] = ollamaTool{
			Type: "function",
			Function: ollamaFunction{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.InputSchema(),
			},
		}
	}
	return ollamaTools
}

// convertMessages converts agent messages to Ollama format.
func (c *Client) convertMessages(messages []llmtypes.Message) []ollamaMessage {
	var apiMessages []ollamaMessage

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// Ollama supports system messages
			apiMessages = append(apiMessages, ollamaMessage{
				Role:    "system",
				Content: msg.Content,
			})

		case "user":
			// Check if message has ContentBlocks (multi-modal content with images)
			if len(msg.ContentBlocks) > 0 {
				var textParts []string
				var images []string

				// Extract text and images from content blocks
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						textParts = append(textParts, block.Text)
					case "image":
						if block.Image != nil && block.Image.Source.Type == "base64" {
							// Ollama expects base64 images in the images array
							images = append(images, block.Image.Source.Data)
						}
					}
				}

				// Combine text parts
				content := strings.Join(textParts, "\n")

				apiMessages = append(apiMessages, ollamaMessage{
					Role:    "user",
					Content: content,
					Images:  images,
				})
			} else {
				// Fallback to plain text (backward compatible)
				apiMessages = append(apiMessages, ollamaMessage{
					Role:    msg.Role,
					Content: msg.Content,
				})
			}

		case "assistant":
			apiMessages = append(apiMessages, ollamaMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})

		case "tool":
			if c.supportsNativeTools() {
				// Native tool format (Ollama v0.12.3+)
				apiMessages = append(apiMessages, ollamaMessage{
					Role:    "tool",
					Content: msg.Content,
				})
			} else {
				// Fallback: Include as user message
				apiMessages = append(apiMessages, ollamaMessage{
					Role:    "user",
					Content: fmt.Sprintf("Tool result: %s", msg.Content),
				})
			}
		}
	}

	return apiMessages
}

// cleanJSONString removes common formatting issues from JSON strings.
func (c *Client) cleanJSONString(s string) string {
	// Trim whitespace
	s = strings.TrimSpace(s)

	// Strip surrounding backticks (common in Ollama responses)
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' {
		s = s[1 : len(s)-1]
	}

	// Strip "json" language marker after opening backticks
	if len(s) >= 4 && strings.HasPrefix(s, "json") {
		// Check if followed by newline or whitespace
		if len(s) > 4 && (s[4] == '\n' || s[4] == '\r' || s[4] == ' ' || s[4] == '\t') {
			s = strings.TrimSpace(s[4:])
		}
	}

	return s
}

// convertResponse converts Ollama response to agent format.
func (c *Client) convertResponse(resp *chatResponse) *llmtypes.LLMResponse {
	// Parse tool calls if present
	var toolCalls []llmtypes.ToolCall
	if len(resp.Message.ToolCalls) > 0 {
		toolCalls = make([]llmtypes.ToolCall, len(resp.Message.ToolCalls))
		for i, tc := range resp.Message.ToolCalls {
			// Parse function arguments (may be string or map)
			var params map[string]interface{}
			switch args := tc.Function.Arguments.(type) {
			case string:
				// Clean JSON string (strip backticks, trim whitespace)
				cleanedArgs := c.cleanJSONString(args)

				// Parse JSON string
				if err := json.Unmarshal([]byte(cleanedArgs), &params); err != nil {
					// Log parsing error with raw JSON for debugging
					fmt.Printf("WARNING: Failed to parse tool arguments for %s: %v\nRaw JSON: %s\nCleaned JSON: %s\n",
						tc.Function.Name, err, args, cleanedArgs)
					// Use empty params as fallback
					params = make(map[string]interface{})
				}
			case map[string]interface{}:
				params = args
			default:
				params = make(map[string]interface{})
			}

			toolCalls[i] = llmtypes.ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: params,
			}
		}
	}

	return &llmtypes.LLMResponse{
		Content:    resp.Message.Content,
		ToolCalls:  toolCalls,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  resp.PromptEvalCount,
			OutputTokens: resp.EvalCount,
			TotalTokens:  resp.PromptEvalCount + resp.EvalCount,
			CostUSD:      0, // Ollama is free (local)
		},
		Metadata: map[string]interface{}{
			"model":         resp.Model,
			"eval_duration": resp.EvalDuration,
			"native_tools":  c.supportsNativeTools(),
			"tool_mode":     string(c.toolMode),
		},
	}
}

// ChatStream implements token-by-token streaming for Ollama.
// This method uses Ollama's /api/chat endpoint with stream=true to stream tokens
// as they are generated. The tokenCallback is called for each token received.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// 1. Build request body (reuse existing message and tool conversion)
	apiMessages := c.convertMessages(messages)

	req := chatRequest{
		Model:    c.model,
		Messages: apiMessages,
		Stream:   true, // Enable streaming
		Options: map[string]interface{}{
			"temperature": c.temperature,
			"num_predict": c.maxTokens,
		},
	}

	// Add tools if native support is available
	if c.supportsNativeTools() && len(tools) > 0 {
		req.Tools = c.convertTools(tools)
	}

	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

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

	// 3. Process JSON lines stream (newline-delimited JSON)
	var contentBuffer strings.Builder
	var toolCalls []llmtypes.ToolCall
	var lastResponse chatResponse // Keep track of final response for metadata
	tokenCount := 0

	scanner := bufio.NewScanner(httpResp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()

		// Parse JSON line
		var chunk chatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			// Skip malformed lines but continue processing
			continue
		}

		// Extract token from message content
		if chunk.Message.Content != "" {
			token := chunk.Message.Content
			contentBuffer.WriteString(token)
			tokenCount++

			// Call token callback (non-blocking)
			if tokenCallback != nil {
				tokenCallback(token)
			}
		}

		// Extract tool calls if present
		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
				// Parse function arguments
				var params map[string]interface{}
				switch args := tc.Function.Arguments.(type) {
				case string:
					cleanedArgs := c.cleanJSONString(args)
					if err := json.Unmarshal([]byte(cleanedArgs), &params); err != nil {
						params = make(map[string]interface{})
					}
				case map[string]interface{}:
					params = args
				default:
					params = make(map[string]interface{})
				}

				toolCalls = append(toolCalls, llmtypes.ToolCall{
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: params,
				})
			}
		}

		// Keep final response for metadata
		if chunk.Done {
			lastResponse = chunk
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
	return &llmtypes.LLMResponse{
		Content:    contentBuffer.String(),
		ToolCalls:  toolCalls,
		StopReason: "stop",
		Usage: llmtypes.Usage{
			InputTokens:  lastResponse.PromptEvalCount,
			OutputTokens: lastResponse.EvalCount,
			TotalTokens:  lastResponse.PromptEvalCount + lastResponse.EvalCount,
			CostUSD:      0, // Ollama is free (local)
		},
		Metadata: map[string]interface{}{
			"model":         lastResponse.Model,
			"eval_duration": lastResponse.EvalDuration,
			"native_tools":  c.supportsNativeTools(),
			"tool_mode":     string(c.toolMode),
			"streaming":     true,
		},
	}, nil
}

// callAPI makes the HTTP request to Ollama.
func (c *Client) callAPI(ctx context.Context, req chatRequest) (*chatResponse, error) {
	// Marshal request
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

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

	// Check status
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	// Parse response
	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

// Ollama API types

type chatRequest struct {
	Model    string                 `json:"model"`
	Messages []ollamaMessage        `json:"messages"`
	Stream   bool                   `json:"stream"`
	Tools    []ollamaTool           `json:"tools,omitempty"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type ollamaTool struct {
	Type     string         `json:"type"`
	Function ollamaFunction `json:"function"`
}

type ollamaFunction struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Parameters  *shuttle.JSONSchema `json:"parameters"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Images    []string         `json:"images,omitempty"` // Base64-encoded images for vision models
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string      `json:"name"`
	Arguments interface{} `json:"arguments"` // Can be string or map
}

type chatResponse struct {
	Model           string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	TotalDuration   int64         `json:"total_duration"`
	LoadDuration    int64         `json:"load_duration"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
	EvalDuration    int64         `json:"eval_duration"`
}

// Ensure Client implements LLMProvider interface.
var _ llmtypes.LLMProvider = (*Client)(nil)

// Ensure Client implements StreamingLLMProvider interface.
var _ llmtypes.StreamingLLMProvider = (*Client)(nil)
