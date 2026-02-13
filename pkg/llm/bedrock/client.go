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
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Global rate limiter shared across all Bedrock clients.
// This ensures all agents using Bedrock coordinate through a single rate limiter,
// preventing AWS Bedrock throttling when multiple agents make concurrent requests.
var (
	globalRateLimiter     *llm.RateLimiter
	globalRateLimiterOnce sync.Once
)

// Client implements the LLMProvider interface for AWS Bedrock.
type Client struct {
	client      *bedrockruntime.Client
	modelID     string
	region      string
	maxTokens   int
	temperature float64
	// toolNameMap maps sanitized tool names (used by Bedrock) to original names (used by agent)
	// This is needed because Bedrock requires tool names to match ^[a-zA-Z0-9_-]{1,64}$
	// but MCP tools use names like "filesystem:read_file"
	toolNameMap map[string]string
	// rateLimiter handles request rate limiting to prevent AWS throttling
	rateLimiter *llm.RateLimiter
}

// getOrCreateGlobalRateLimiter returns the singleton rate limiter for all Bedrock clients.
// The first call initializes the rate limiter with the provided config.
// Subsequent calls return the existing rate limiter (ignoring the config parameter).
func getOrCreateGlobalRateLimiter(config llm.RateLimiterConfig) *llm.RateLimiter {
	globalRateLimiterOnce.Do(func() {
		// Apply defaults if not provided
		if config.Logger == nil {
			config = llm.DefaultRateLimiterConfig()
		}
		globalRateLimiter = llm.NewRateLimiter(config)
	})
	return globalRateLimiter
}

// Config holds configuration for the Bedrock client.
type Config struct {
	// AWS Configuration
	Region          string // Required: AWS region (e.g., us-east-1, us-west-2)
	AccessKeyID     string // Optional: if not using IAM role/profile
	SecretAccessKey string // Optional: if not using IAM role/profile
	SessionToken    string // Optional: for temporary credentials
	Profile         string // Optional: AWS profile name from ~/.aws/config

	// Model Configuration
	ModelID     string  // Default: anthropic.claude-3-5-sonnet-20241022-v2:0
	MaxTokens   int     // Default: 4096
	Temperature float64 // Default: 1.0

	// Rate Limiting Configuration
	RateLimiterConfig llm.RateLimiterConfig // Optional: rate limiting config (enables automatic throttle handling)
}

// Default Bedrock configuration values.
// Can be overridden via environment variables:
//   - AWS_BEDROCK_MODEL_ID / LOOM_LLM_BEDROCK_MODEL_ID
//   - AWS_DEFAULT_REGION / LOOM_LLM_BEDROCK_REGION
const (
	// DefaultBedrockModelID uses Claude Sonnet 4.5 with cross-region inference profile (us.* prefix)
	DefaultBedrockModelID     = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	DefaultBedrockRegion      = "us-west-2"
	DefaultBedrockMaxTokens   = 4096
	DefaultBedrockTemperature = 1.0
)

// NewClient creates a new Bedrock client.
func NewClient(cfg Config) (*Client, error) {
	// Set defaults - check environment variables first
	if cfg.ModelID == "" {
		if envModel := os.Getenv("AWS_BEDROCK_MODEL_ID"); envModel != "" {
			cfg.ModelID = envModel
		} else if envModel := os.Getenv("LOOM_LLM_BEDROCK_MODEL_ID"); envModel != "" {
			cfg.ModelID = envModel
		} else {
			cfg.ModelID = DefaultBedrockModelID
		}
	}
	if cfg.Region == "" {
		if envRegion := os.Getenv("AWS_DEFAULT_REGION"); envRegion != "" {
			cfg.Region = envRegion
		} else if envRegion := os.Getenv("LOOM_LLM_BEDROCK_REGION"); envRegion != "" {
			cfg.Region = envRegion
		} else {
			cfg.Region = DefaultBedrockRegion
		}
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = DefaultBedrockMaxTokens
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = DefaultBedrockTemperature
	}

	// Build AWS config
	var awsCfg aws.Config
	var err error

	// Option 1: Explicit credentials provided
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		awsCfg, err = config.LoadDefaultConfig(context.Background(),
			config.WithRegion(cfg.Region),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID,
				cfg.SecretAccessKey,
				cfg.SessionToken,
			)),
		)
	} else if cfg.Profile != "" {
		// Option 2: Use named profile
		awsCfg, err = config.LoadDefaultConfig(context.Background(),
			config.WithRegion(cfg.Region),
			config.WithSharedConfigProfile(cfg.Profile),
		)
	} else {
		// Option 3: Use default credentials chain (IAM role, env vars, profile)
		awsCfg, err = config.LoadDefaultConfig(context.Background(),
			config.WithRegion(cfg.Region),
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Initialize rate limiter - use global singleton shared across all Bedrock clients.
	// This prevents AWS throttling when multiple agents make concurrent requests.
	var rateLimiter *llm.RateLimiter
	if cfg.RateLimiterConfig.Enabled {
		// Build config with defaults for missing values
		rlCfg := llm.DefaultRateLimiterConfig()
		if cfg.RateLimiterConfig.Logger != nil {
			rlCfg.Logger = cfg.RateLimiterConfig.Logger
		}
		if cfg.RateLimiterConfig.RequestsPerSecond > 0 {
			rlCfg.RequestsPerSecond = cfg.RateLimiterConfig.RequestsPerSecond
		}
		if cfg.RateLimiterConfig.TokensPerMinute > 0 {
			rlCfg.TokensPerMinute = cfg.RateLimiterConfig.TokensPerMinute
		}
		if cfg.RateLimiterConfig.BurstCapacity > 0 {
			rlCfg.BurstCapacity = cfg.RateLimiterConfig.BurstCapacity
		}
		if cfg.RateLimiterConfig.MinDelay > 0 {
			rlCfg.MinDelay = cfg.RateLimiterConfig.MinDelay
		}
		if cfg.RateLimiterConfig.MaxRetries > 0 {
			rlCfg.MaxRetries = cfg.RateLimiterConfig.MaxRetries
		}
		if cfg.RateLimiterConfig.RetryBackoff > 0 {
			rlCfg.RetryBackoff = cfg.RateLimiterConfig.RetryBackoff
		}
		if cfg.RateLimiterConfig.QueueTimeout > 0 {
			rlCfg.QueueTimeout = cfg.RateLimiterConfig.QueueTimeout
		}

		// Get or create the global singleton rate limiter
		// Note: Only the first client's config is used to initialize the rate limiter
		rateLimiter = getOrCreateGlobalRateLimiter(rlCfg)
	}

	return &Client{
		client:      bedrockruntime.NewFromConfig(awsCfg),
		modelID:     cfg.ModelID,
		region:      cfg.Region,
		maxTokens:   cfg.MaxTokens,
		temperature: cfg.Temperature,
		toolNameMap: make(map[string]string),
		rateLimiter: rateLimiter,
	}, nil
}

// Name returns the provider name.
func (c *Client) Name() string {
	return "bedrock"
}

// Model returns the model identifier.
func (c *Client) Model() string {
	return c.modelID
}

// Chat sends a conversation to Bedrock and returns the response.
func (c *Client) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Extract system messages and convert to Bedrock format
	systemPrompt, apiMessages := c.convertMessages(messages)

	// Validate that we have at least one message (Bedrock requires non-empty messages array)
	if len(apiMessages) == 0 {
		return nil, fmt.Errorf("no valid messages to send (messages may be empty)")
	}

	// Build request (Bedrock uses Anthropic's message format)
	// AWS docs: anthropic_version MUST be "bedrock-2023-05-31" for all Claude models
	request := map[string]interface{}{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        c.maxTokens,
		"temperature":       c.temperature,
		"messages":          apiMessages,
	}

	// Add system prompt if present (Anthropic Messages API requires separate system field)
	if systemPrompt != "" {
		request["system"] = systemPrompt
	}

	// Add tools if provided
	if len(tools) > 0 {
		request["tools"] = c.convertTools(tools)
	}

	// Marshal request
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Debug logging if LOOM_DEBUG_BEDROCK is set
	if os.Getenv("LOOM_DEBUG_BEDROCK") == "1" {
		fmt.Printf("\n=== BEDROCK REQUEST ===\n")
		fmt.Printf("Model: %s\n", c.modelID)
		var prettyRequest map[string]interface{}
		_ = json.Unmarshal(body, &prettyRequest)
		prettyJSON, _ := json.MarshalIndent(prettyRequest, "", "  ")
		fmt.Printf("%s\n", prettyJSON)
		fmt.Printf("=== END REQUEST ===\n\n")
	}

	// Call Bedrock with rate limiting if configured
	var output *bedrockruntime.InvokeModelOutput
	if c.rateLimiter != nil {
		// Use rate limiter with automatic retry on throttling
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			return c.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
				ModelId:     aws.String(c.modelID),
				Body:        body,
				ContentType: aws.String("application/json"),
			})
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock invocation failed: %w", err)
		}
		output = result.(*bedrockruntime.InvokeModelOutput)
	} else {
		// Direct call without rate limiting
		output, err = c.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
			ModelId:     aws.String(c.modelID),
			Body:        body,
			ContentType: aws.String("application/json"),
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock invocation failed: %w", err)
		}
	}

	// Debug logging if LOOM_DEBUG_BEDROCK is set
	if os.Getenv("LOOM_DEBUG_BEDROCK") == "1" {
		fmt.Printf("\n=== BEDROCK RESPONSE ===\n")
		var prettyResponse map[string]interface{}
		_ = json.Unmarshal(output.Body, &prettyResponse)
		prettyJSON, _ := json.MarshalIndent(prettyResponse, "", "  ")
		fmt.Printf("%s\n", prettyJSON)
		fmt.Printf("=== END RESPONSE ===\n\n")
	}

	// Parse response
	var response bedrockResponse
	if err := json.Unmarshal(output.Body, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Convert to agent format
	llmResp := c.convertResponse(&response)

	// Record token usage for rate limiter metrics
	if c.rateLimiter != nil {
		totalTokens := int64(response.Usage.InputTokens + response.Usage.OutputTokens)
		c.rateLimiter.RecordTokenUsage(totalTokens)
	}

	return llmResp, nil
}

// chatStreamDisabled is the streaming implementation, currently disabled due to
// a bug in Bedrock's InvokeModelWithResponseStream API where tool input parameters
// are not streamed correctly (empty input_json_delta events).
// Use non-streaming Chat() method instead until this is resolved.
//
//nolint:unused // Intentionally disabled until AWS fixes InvokeModelWithResponseStream bug
func (c *Client) chatStreamDisabled(ctx context.Context, messages []llmtypes.Message,
	tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// 1. Build request body (extract system messages and convert to Bedrock format)
	systemPrompt, apiMessages := c.convertMessages(messages)

	// AWS docs: anthropic_version MUST be "bedrock-2023-05-31" for all Claude models
	request := map[string]interface{}{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        c.maxTokens,
		"temperature":       c.temperature,
		"messages":          apiMessages,
	}

	// Add system prompt if present (Anthropic Messages API requires separate system field)
	if systemPrompt != "" {
		request["system"] = systemPrompt
	}

	// Add tools if provided
	if len(tools) > 0 {
		request["tools"] = c.convertTools(tools)
	}

	// Marshal request
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 2. Use InvokeModelWithResponseStream instead of InvokeModel
	var output *bedrockruntime.InvokeModelWithResponseStreamOutput
	if c.rateLimiter != nil {
		// Use rate limiter with automatic retry on throttling
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			return c.client.InvokeModelWithResponseStream(ctx,
				&bedrockruntime.InvokeModelWithResponseStreamInput{
					ModelId:     aws.String(c.modelID),
					Body:        body,
					ContentType: aws.String("application/json"),
				})
		})
		if err != nil {
			return nil, fmt.Errorf("failed to start stream: %w", err)
		}
		output = result.(*bedrockruntime.InvokeModelWithResponseStreamOutput)
	} else {
		// Direct call without rate limiting
		output, err = c.client.InvokeModelWithResponseStream(ctx,
			&bedrockruntime.InvokeModelWithResponseStreamInput{
				ModelId:     aws.String(c.modelID),
				Body:        body,
				ContentType: aws.String("application/json"),
			})
		if err != nil {
			return nil, fmt.Errorf("failed to start stream: %w", err)
		}
	}

	// 3. Process event stream
	buffer := strings.Builder{}
	usage := llmtypes.Usage{}
	var stopReason string
	tokenCount := 0
	var toolCalls []llmtypes.ToolCall
	// Track tool input JSON as it streams in (indexed by content block index)
	toolInputBuffers := make(map[int]*strings.Builder)

	for event := range output.GetStream().Events() {
		switch e := event.(type) {
		case *bedrocktypes.ResponseStreamMemberChunk:
			// Parse chunk (Bedrock sends JSON chunks)
			var chunk bedrockStreamChunk
			if err := json.Unmarshal(e.Value.Bytes, &chunk); err != nil {
				// Skip malformed chunks but continue processing
				continue
			}

			// Handle content delta (text streaming)
			if chunk.Type == "content_block_delta" && chunk.Delta.Text != "" {
				token := chunk.Delta.Text
				buffer.WriteString(token)
				tokenCount++

				// Call token callback (non-blocking)
				if tokenCallback != nil {
					tokenCallback(token)
				}

				// Check context cancellation
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}
			}

			// Handle content block start (for tool use)
			if chunk.Type == "content_block_start" && chunk.ContentBlock.Type == "tool_use" {
				// Start tracking a new tool call with empty input (will be populated from deltas)
				toolCall := llmtypes.ToolCall{
					ID:    chunk.ContentBlock.ID,
					Name:  chunk.ContentBlock.Name,
					Input: make(map[string]interface{}), // Initialize to empty map (never nil)
				}
				toolCalls = append(toolCalls, toolCall)
				// Initialize buffer for this tool's input JSON
				toolInputBuffers[chunk.Index] = &strings.Builder{}
			}

			// Handle tool input delta (accumulate JSON for tool parameters)
			if chunk.Type == "content_block_delta" && chunk.Delta.Type == "input_json_delta" {
				// Accumulate the JSON delta
				if buf, exists := toolInputBuffers[chunk.Index]; exists {
					buf.WriteString(chunk.Delta.Text)
				}
			}

			// Handle content block stop (finalize tool input)
			if chunk.Type == "content_block_stop" {
				// If we have accumulated input JSON for this block, parse it
				if buf, exists := toolInputBuffers[chunk.Index]; exists && buf.Len() > 0 {
					var input map[string]interface{}
					if err := json.Unmarshal([]byte(buf.String()), &input); err == nil {
						// Update the tool call with parsed input
						if chunk.Index < len(toolCalls) {
							toolCalls[chunk.Index].Input = input
						}
					}
					// Clean up buffer
					delete(toolInputBuffers, chunk.Index)
				}
			}

			// Handle message stop
			if chunk.Type == "message_stop" {
				stopReason = chunk.StopReason
			}

			// Handle usage data (comes in message_delta events)
			if chunk.Type == "message_delta" && chunk.Usage != nil {
				usage.OutputTokens = chunk.Usage.OutputTokens
			}

			// Handle usage data (alternative: in message_stop)
			if chunk.Type == "message_stop" && chunk.Usage != nil {
				usage.InputTokens = chunk.Usage.InputTokens
				usage.OutputTokens = chunk.Usage.OutputTokens
			}
		}
	}

	// 4. Build final response
	// If output tokens not set by stream, use our token count
	if usage.OutputTokens == 0 {
		usage.OutputTokens = tokenCount
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	usage.CostUSD = c.calculateCost(usage.InputTokens, usage.OutputTokens)

	// Record token usage for rate limiter metrics
	if c.rateLimiter != nil {
		totalTokens := int64(usage.InputTokens + usage.OutputTokens)
		c.rateLimiter.RecordTokenUsage(totalTokens)
	}

	// Map sanitized tool names back to original names
	for i := range toolCalls {
		if originalName, exists := c.toolNameMap[toolCalls[i].Name]; exists {
			toolCalls[i].Name = originalName
		}
	}

	return &llmtypes.LLMResponse{
		Content:    buffer.String(),
		StopReason: stopReason,
		Usage:      usage,
		ToolCalls:  toolCalls,
		Metadata: map[string]interface{}{
			"model":       c.modelID,
			"stop_reason": stopReason,
			"streaming":   true,
		},
	}, nil
}

// convertMessages converts agent messages to Bedrock/Anthropic format.
// Returns the system prompt (combined from all system messages) and the API messages.
// System messages are extracted and combined, as Anthropic Messages API requires
// them to be sent as a separate "system" field, not in the messages array.
func (c *Client) convertMessages(messages []llmtypes.Message) (string, []map[string]interface{}) {
	var systemPrompts []string
	var apiMessages []map[string]interface{}

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
				// Convert content blocks from agent format to Bedrock/Anthropic format
				var content []map[string]interface{}
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						// Skip empty text blocks - Bedrock rejects them
						if block.Text != "" {
							content = append(content, map[string]interface{}{
								"type": "text",
								"text": block.Text,
							})
						}
					case "image":
						if block.Image != nil {
							imageBlock := map[string]interface{}{
								"type": "image",
								"source": map[string]interface{}{
									"type":       block.Image.Source.Type,
									"media_type": block.Image.Source.MediaType,
								},
							}
							// Add either data or url based on source type
							if block.Image.Source.Type == "base64" {
								imageBlock["source"].(map[string]interface{})["data"] = block.Image.Source.Data
							} else if block.Image.Source.Type == "url" {
								imageBlock["source"].(map[string]interface{})["url"] = block.Image.Source.URL
							}
							content = append(content, imageBlock)
						}
					}
				}
				// Only add user message if there's actual content
				if len(content) > 0 {
					apiMessages = append(apiMessages, map[string]interface{}{
						"role":    "user",
						"content": content,
					})
				}
			} else if msg.Content != "" {
				// Fallback to plain text (backward compatible) - skip empty messages
				apiMessages = append(apiMessages, map[string]interface{}{
					"role": "user",
					"content": []map[string]interface{}{
						{"type": "text", "text": msg.Content},
					},
				})
			}

		case "assistant":
			var content []map[string]interface{}

			if msg.Content != "" {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": msg.Content,
				})
			}

			for _, tc := range msg.ToolCalls {
				// Ensure input is never null - Bedrock requires an object (even if empty)
				input := tc.Input
				if input == nil {
					input = map[string]interface{}{}
				}
				content = append(content, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  llm.SanitizeToolName(tc.Name),
					"input": input,
				})
			}

			if len(content) > 0 {
				apiMessages = append(apiMessages, map[string]interface{}{
					"role":    "assistant",
					"content": content,
				})
			}

		case "tool":
			apiMessages = append(apiMessages, map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": msg.ToolUseID,
						"content":     msg.Content,
					},
				},
			})
		}
	}

	// Combine all system prompts with double newlines
	systemPrompt := strings.Join(systemPrompts, "\n\n")

	return systemPrompt, apiMessages
}

// convertTools converts shuttle tools to Bedrock/Anthropic format.
// Uses standard Anthropic Messages API format with sanitized tool names.
func (c *Client) convertTools(tools []shuttle.Tool) []map[string]interface{} {
	var apiTools []map[string]interface{}

	// Clear previous mapping
	c.toolNameMap = make(map[string]string)

	for _, tool := range tools {
		originalName := tool.Name()
		sanitizedName := llm.SanitizeToolName(originalName)

		// Store mapping for later conversion back
		c.toolNameMap[sanitizedName] = originalName

		apiTool := map[string]interface{}{
			"name":        sanitizedName,
			"description": tool.Description(),
		}

		schema := tool.InputSchema()
		if schema != nil {
			// Ensure type field is not empty (default to "object")
			schemaType := schema.Type
			if schemaType == "" {
				schemaType = "object"
			}

			apiTool["input_schema"] = map[string]interface{}{
				"type":       schemaType,
				"properties": convertSchemaProperties(schema.Properties),
				"required":   schema.Required,
			}
		}

		apiTools = append(apiTools, apiTool)
	}

	return apiTools
}

// convertSchemaProperties converts JSONSchema properties to Anthropic/Bedrock format.
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
		if schema.Properties != nil {
			propMap["properties"] = convertSchemaProperties(schema.Properties)
		}
		if schema.Items != nil {
			propMap["items"] = convertSchemaItem(schema.Items)
		}

		result[key] = propMap
	}
	return result
}

// convertSchemaItem converts a JSONSchema item for arrays.
func convertSchemaItem(item *shuttle.JSONSchema) map[string]interface{} {
	itemMap := make(map[string]interface{})
	itemMap["type"] = item.Type

	if item.Description != "" {
		itemMap["description"] = item.Description
	}
	if item.Enum != nil {
		itemMap["enum"] = item.Enum
	}
	if item.Properties != nil {
		itemMap["properties"] = convertSchemaProperties(item.Properties)
	}

	return itemMap
}

// convertResponse converts Bedrock response to agent format.
func (c *Client) convertResponse(resp *bedrockResponse) *llmtypes.LLMResponse {
	llmResp := &llmtypes.LLMResponse{
		StopReason: resp.StopReason,
		Usage: llmtypes.Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
			CostUSD:      c.calculateCost(resp.Usage.InputTokens, resp.Usage.OutputTokens),
		},
		Metadata: map[string]interface{}{
			"model":       c.modelID,
			"stop_reason": resp.StopReason,
		},
	}

	// Extract content and tool calls
	for _, block := range resp.Content {
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			if text, ok := block["text"].(string); ok {
				llmResp.Content += text
			}

		case "tool_use":
			toolCall := llmtypes.ToolCall{}
			if id, ok := block["id"].(string); ok {
				toolCall.ID = id
			}
			if sanitizedName, ok := block["name"].(string); ok {
				// Map sanitized name back to original name
				if originalName, exists := c.toolNameMap[sanitizedName]; exists {
					toolCall.Name = originalName
				} else {
					// Fallback: use sanitized name if mapping not found
					toolCall.Name = sanitizedName
				}
			}
			if input, ok := block["input"].(map[string]interface{}); ok {
				toolCall.Input = input
			}
			llmResp.ToolCalls = append(llmResp.ToolCalls, toolCall)
		}
	}

	return llmResp
}

// calculateCost estimates cost for Bedrock Claude models.
// Pricing varies by model and region - these are approximate.
func (c *Client) calculateCost(inputTokens, outputTokens int) float64 {
	// Pricing based on model ID
	var inputPricePerMillion, outputPricePerMillion float64

	// Check model ID prefix to determine pricing
	switch {
	case strings.Contains(c.modelID, "claude-sonnet-4"):
		// Claude Sonnet 4.5: $3 per 1M input, $15 per 1M output
		inputPricePerMillion = 3.0
		outputPricePerMillion = 15.0
	case strings.Contains(c.modelID, "claude-haiku-4"):
		// Claude Haiku 4.5: $0.8 per 1M input, $4 per 1M output
		inputPricePerMillion = 0.8
		outputPricePerMillion = 4.0
	case strings.Contains(c.modelID, "claude-opus-4"):
		// Claude Opus 4.5: $15 per 1M input, $75 per 1M output
		inputPricePerMillion = 15.0
		outputPricePerMillion = 75.0
	default:
		// Default to Sonnet pricing for unknown models
		inputPricePerMillion = 3.0
		outputPricePerMillion = 15.0
	}

	inputCost := float64(inputTokens) * inputPricePerMillion / 1_000_000
	outputCost := float64(outputTokens) * outputPricePerMillion / 1_000_000
	return inputCost + outputCost
}

// bedrockResponse represents Bedrock's response format (Anthropic-compatible).
type bedrockResponse struct {
	ID         string                   `json:"id"`
	Type       string                   `json:"type"`
	Role       string                   `json:"role"`
	Content    []map[string]interface{} `json:"content"`
	Model      string                   `json:"model"`
	StopReason string                   `json:"stop_reason"`
	Usage      bedrockUsage             `json:"usage"`
}

type bedrockUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// bedrockStreamChunk represents a chunk from Bedrock's streaming response.
// Bedrock uses Anthropic's streaming format with JSON chunks for different event types.
type bedrockStreamChunk struct {
	Type  string `json:"type"` // message_start, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop
	Index int    `json:"index,omitempty"`

	// For content_block_start events
	ContentBlock struct {
		Type string `json:"type"` // text, tool_use
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block,omitempty"`

	// For content_block_delta events
	Delta struct {
		Type string `json:"type"`           // text_delta, input_json_delta
		Text string `json:"text,omitempty"` // For text_delta and input_json_delta (JSON string chunks)
	} `json:"delta,omitempty"`

	// For message_stop events
	StopReason string `json:"stop_reason,omitempty"`

	// For message_delta and message_stop events
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// Bedrock Client implements LLMProvider (non-streaming only).
// Streaming is DISABLED due to bugs in ConverseStream API where tool schemas
// are not properly serialized via document.NewLazyDocument (all tool inputs are empty {}).
// The legacy InvokeModel API works correctly, so we use that until AWS fixes the SDK.
var _ llmtypes.LLMProvider = (*Client)(nil)

// var _ llmtypes.StreamingLLMProvider = (*Client)(nil)  // DISABLED
