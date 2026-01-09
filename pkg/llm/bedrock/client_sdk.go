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
	"io"
	"os"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// SDKClient implements the LLMProvider interface using the official Anthropic SDK for Bedrock.
// This is simpler and better maintained than the direct AWS SDK approach.
type SDKClient struct {
	client      anthropic.Client
	modelID     string
	region      string
	maxTokens   int64
	temperature float64
	rateLimiter *llm.RateLimiter
}

// NewSDKClient creates a new Bedrock client using the Anthropic SDK.
func NewSDKClient(cfg Config) (*SDKClient, error) {
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

	// Build AWS config for Bedrock
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

	// Initialize rate limiter if enabled
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

		rateLimiter = getOrCreateGlobalRateLimiter(rlCfg)
	}

	// Create Anthropic client with Bedrock backend
	// The bedrock.WithConfig handles all the AWS signing and endpoint configuration
	client := anthropic.NewClient(
		bedrock.WithConfig(awsCfg),
	)

	return &SDKClient{
		client:      client,
		modelID:     cfg.ModelID,
		region:      cfg.Region,
		maxTokens:   int64(cfg.MaxTokens),
		temperature: cfg.Temperature,
		rateLimiter: rateLimiter,
	}, nil
}

// Name returns the provider name.
func (c *SDKClient) Name() string {
	return "bedrock-sdk"
}

// Model returns the model identifier.
func (c *SDKClient) Model() string {
	return c.modelID
}

// Chat sends a conversation to Bedrock using the Anthropic SDK and returns the response.
func (c *SDKClient) Chat(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	// Convert messages to Anthropic SDK format
	systemPrompt, sdkMessages := c.convertMessagesToSDK(messages)

	// Validate that we have at least one message
	if len(sdkMessages) == 0 {
		return nil, fmt.Errorf("no valid messages to send (messages may be empty)")
	}

	// Build message params
	params := anthropic.MessageNewParams{
		Model:       anthropic.Model(c.modelID),
		Messages:    sdkMessages,
		MaxTokens:   c.maxTokens,
		Temperature: anthropic.Float(c.temperature),
	}

	// Add system prompt if present
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	// Add tools if provided
	if len(tools) > 0 {
		sdkTools := c.convertToolsToSDK(tools)
		// Convert []ToolParam to []ToolUnionParam
		toolUnions := make([]anthropic.ToolUnionParam, len(sdkTools))
		for i := range sdkTools {
			toolUnions[i] = anthropic.ToolUnionParam{
				OfTool: &sdkTools[i],
			}
		}
		params.Tools = toolUnions
	}

	// Call API with rate limiting if configured
	var message *anthropic.Message
	var err error

	if c.rateLimiter != nil {
		// Use rate limiter with automatic retry on throttling
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			return c.client.Messages.New(ctx, params)
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock SDK invocation failed: %w", err)
		}
		message = result.(*anthropic.Message)
	} else {
		// Direct call without rate limiting
		message, err = c.client.Messages.New(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("bedrock SDK invocation failed: %w", err)
		}
	}

	// Convert response to our format
	llmResp := c.convertResponseFromSDK(message)

	// Record token usage for rate limiter metrics
	if c.rateLimiter != nil {
		totalTokens := int64(message.Usage.InputTokens + message.Usage.OutputTokens)
		c.rateLimiter.RecordTokenUsage(totalTokens)
	}

	return llmResp, nil
}

// convertMessagesToSDK converts agent messages to Anthropic SDK format.
// Returns the system prompt and the API messages.
func (c *SDKClient) convertMessagesToSDK(messages []llmtypes.Message) (string, []anthropic.MessageParam) {
	var systemPrompts []string
	var sdkMessages []anthropic.MessageParam

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
				var content []anthropic.ContentBlockParamUnion
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							content = append(content, anthropic.NewTextBlock(block.Text))
						}
					case "image":
						if block.Image != nil {
							if block.Image.Source.Type == "base64" {
								content = append(content, anthropic.NewImageBlockBase64(
									block.Image.Source.MediaType,
									block.Image.Source.Data,
								))
							}
						}
					}
				}
				if len(content) > 0 {
					sdkMessages = append(sdkMessages, anthropic.NewUserMessage(content...))
				}
			} else if msg.Content != "" {
				// Plain text message
				sdkMessages = append(sdkMessages, anthropic.NewUserMessage(
					anthropic.NewTextBlock(msg.Content),
				))
			}

		case "assistant":
			var content []anthropic.ContentBlockParamUnion

			// Add text content if present
			if msg.Content != "" {
				content = append(content, anthropic.NewTextBlock(msg.Content))
			}

			// Add tool calls
			for _, tc := range msg.ToolCalls {
				// Ensure input is never null
				var input interface{}
				if tc.Input != nil {
					input = tc.Input
				} else {
					input = map[string]interface{}{}
				}
				content = append(content, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
			}

			if len(content) > 0 {
				sdkMessages = append(sdkMessages, anthropic.NewAssistantMessage(content...))
			}

		case "tool":
			sdkMessages = append(sdkMessages, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(msg.ToolUseID, msg.Content, false),
			))
		}
	}

	// Combine all system prompts
	systemPrompt := strings.Join(systemPrompts, "\n\n")

	return systemPrompt, sdkMessages
}

// convertToolsToSDK converts shuttle tools to Anthropic SDK format.
func (c *SDKClient) convertToolsToSDK(tools []shuttle.Tool) []anthropic.ToolParam {
	var sdkTools []anthropic.ToolParam

	for _, tool := range tools {
		sdkTool := anthropic.ToolParam{
			Name:        tool.Name(),
			Description: anthropic.String(tool.Description()),
		}

		schema := tool.InputSchema()
		if schema != nil {
			// Marshal and unmarshal to get proper anthropic.ToolInputSchemaParam
			schemaMap := map[string]interface{}{
				"type":       schema.Type,
				"properties": schema.Properties,
				"required":   schema.Required,
			}
			schemaJSON, _ := json.Marshal(schemaMap)
			var inputSchema anthropic.ToolInputSchemaParam
			_ = json.Unmarshal(schemaJSON, &inputSchema)
			sdkTool.InputSchema = inputSchema
		}

		sdkTools = append(sdkTools, sdkTool)
	}

	return sdkTools
}

// convertResponseFromSDK converts Anthropic SDK response to agent format.
func (c *SDKClient) convertResponseFromSDK(message *anthropic.Message) *llmtypes.LLMResponse {
	llmResp := &llmtypes.LLMResponse{
		StopReason: string(message.StopReason),
		Usage: llmtypes.Usage{
			InputTokens:  int(message.Usage.InputTokens),
			OutputTokens: int(message.Usage.OutputTokens),
			TotalTokens:  int(message.Usage.InputTokens + message.Usage.OutputTokens),
			CostUSD:      c.calculateCost(int(message.Usage.InputTokens), int(message.Usage.OutputTokens)),
		},
		Metadata: map[string]interface{}{
			"model":       c.modelID,
			"stop_reason": message.StopReason,
			"message_id":  message.ID,
		},
	}

	// Extract content and tool calls based on block type
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			llmResp.Content += block.Text
		case "tool_use":
			// Parse tool input from JSON
			var input map[string]interface{}
			if block.Input != nil {
				_ = json.Unmarshal(block.Input, &input)
			}
			if input == nil {
				input = map[string]interface{}{}
			}

			toolCall := llmtypes.ToolCall{
				ID:    block.ID,
				Name:  block.Name,
				Input: input,
			}
			llmResp.ToolCalls = append(llmResp.ToolCalls, toolCall)
		}
	}

	return llmResp
}

// calculateCost estimates cost for Bedrock Claude models.
func (c *SDKClient) calculateCost(inputTokens, outputTokens int) float64 {
	var inputPricePerMillion, outputPricePerMillion float64

	switch {
	case strings.Contains(c.modelID, "claude-sonnet-4"):
		inputPricePerMillion = 3.0
		outputPricePerMillion = 15.0
	case strings.Contains(c.modelID, "claude-haiku-4"):
		inputPricePerMillion = 0.8
		outputPricePerMillion = 4.0
	case strings.Contains(c.modelID, "claude-opus-4"):
		inputPricePerMillion = 15.0
		outputPricePerMillion = 75.0
	default:
		inputPricePerMillion = 3.0
		outputPricePerMillion = 15.0
	}

	inputCost := float64(inputTokens) * inputPricePerMillion / 1_000_000
	outputCost := float64(outputTokens) * outputPricePerMillion / 1_000_000
	return inputCost + outputCost
}

// ChatStream streams tokens as they're generated from Bedrock using the Anthropic SDK.
func (c *SDKClient) ChatStream(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool,
	tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {

	// Convert messages to SDK format
	systemPrompt, sdkMessages := c.convertMessagesToSDK(messages)

	// Validate that we have at least one message
	if len(sdkMessages) == 0 {
		return nil, fmt.Errorf("no valid messages to send (messages may be empty)")
	}

	// Build message params
	params := anthropic.MessageNewParams{
		Model:       anthropic.Model(c.modelID),
		Messages:    sdkMessages,
		MaxTokens:   c.maxTokens,
		Temperature: anthropic.Float(c.temperature),
	}

	// Add system prompt if present
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	// Add tools if provided
	if len(tools) > 0 {
		sdkTools := c.convertToolsToSDK(tools)
		toolUnions := make([]anthropic.ToolUnionParam, len(sdkTools))
		for i := range sdkTools {
			toolUnions[i] = anthropic.ToolUnionParam{
				OfTool: &sdkTools[i],
			}
		}
		params.Tools = toolUnions
	}

	// Call streaming API (rate limiting doesn't apply well to streams)
	// The stream will be consumed synchronously, so we don't need rate limiting here
	stream := c.client.Messages.NewStreaming(ctx, params)

	// Process stream events
	var contentBuffer strings.Builder
	var toolCalls []llmtypes.ToolCall
	var usage llmtypes.Usage
	var stopReason string
	var messageID string

	// Track tool inputs as they stream in (indexed by content block index)
	toolInputBuffers := make(map[int64]*strings.Builder)
	// Map content block index to tool call index in our array
	blockIndexToToolIndex := make(map[int64]int)

	for stream.Next() {
		event := stream.Current()

		switch event.Type {
		case "message_start":
			// Extract message ID and initial usage
			messageID = event.Message.ID
			usage.InputTokens = int(event.Message.Usage.InputTokens)

		case "content_block_start":
			// Check if this is a tool use block
			if event.ContentBlock.Type == "tool_use" {
				// Start tracking a new tool call
				toolCall := llmtypes.ToolCall{
					ID:    event.ContentBlock.ID,
					Name:  event.ContentBlock.Name,
					Input: make(map[string]interface{}), // Will be populated from deltas
				}
				toolCallIndex := len(toolCalls)
				toolCalls = append(toolCalls, toolCall)
				// Initialize buffer for this tool's input JSON
				toolInputBuffers[event.Index] = &strings.Builder{}
				// Map block index to tool call index
				blockIndexToToolIndex[event.Index] = toolCallIndex
			}

		case "content_block_delta":
			// Handle text delta
			if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				token := event.Delta.Text
				contentBuffer.WriteString(token)

				// Call token callback (non-blocking)
				if tokenCallback != nil {
					tokenCallback(token)
				}
			}

			// Handle tool input delta
			if event.Delta.Type == "input_json_delta" {
				// Accumulate the JSON delta (uses PartialJSON field, not Text)
				if buf, exists := toolInputBuffers[event.Index]; exists {
					buf.WriteString(event.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			// If we have accumulated input JSON for this block, parse it
			if buf, exists := toolInputBuffers[event.Index]; exists && buf.Len() > 0 {
				var input map[string]interface{}
				if err := json.Unmarshal([]byte(buf.String()), &input); err == nil {
					// Update the tool call with parsed input using the mapped index
					if toolIdx, ok := blockIndexToToolIndex[event.Index]; ok && toolIdx < len(toolCalls) {
						toolCalls[toolIdx].Input = input
					}
				}
				// Clean up buffer
				delete(toolInputBuffers, event.Index)
			}

		case "message_delta":
			// Update stop reason and output tokens
			if event.Delta.StopReason != "" {
				stopReason = string(event.Delta.StopReason)
			}
			if event.Usage.OutputTokens > 0 {
				usage.OutputTokens = int(event.Usage.OutputTokens)
			}

		case "message_stop":
			// Final usage data
			// (usually already set by message_delta, but use this as fallback)
		}
	}

	// Check for stream errors (EOF is normal at end of stream)
	if err := stream.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("stream error: %w", err)
	}

	// Build final response
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	usage.CostUSD = c.calculateCost(usage.InputTokens, usage.OutputTokens)

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
			"model":       c.modelID,
			"stop_reason": stopReason,
			"message_id":  messageID,
			"streaming":   true,
		},
	}, nil
}

// Ensure SDKClient implements both LLMProvider and StreamingLLMProvider interfaces
var _ llmtypes.LLMProvider = (*SDKClient)(nil)
var _ llmtypes.StreamingLLMProvider = (*SDKClient)(nil)
