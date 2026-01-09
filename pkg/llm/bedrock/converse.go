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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ChatConverse implements non-streaming using AWS Bedrock Converse API.
// This is the modern, unified API that properly handles tool use.
// This method is used when streaming is not needed or disabled.
func (c *Client) ChatConverse(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool) (*llmtypes.LLMResponse, error) {
	startTime := time.Now()

	// Convert messages and tools to Converse API format (reuses same converter as streaming)
	systemBlocks, converseMessages := c.convertMessagesToConverse(messages)

	// Validate that we have at least one message
	if len(converseMessages) == 0 {
		return nil, fmt.Errorf("no valid messages to send (messages may be empty)")
	}

	// Build Converse input
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(c.modelID),
		Messages: converseMessages,
		InferenceConfig: &bedrocktypes.InferenceConfiguration{
			MaxTokens:   aws.Int32(int32(c.maxTokens)),
			Temperature: aws.Float32(float32(c.temperature)),
		},
	}

	// Debug logging if LOOM_DEBUG_BEDROCK is set
	if os.Getenv("LOOM_DEBUG_BEDROCK") == "1" {
		debugJSON, _ := json.MarshalIndent(map[string]interface{}{
			"model_id":         c.modelID,
			"num_messages":     len(converseMessages),
			"num_system":       len(systemBlocks),
			"has_tools":        len(tools) > 0,
			"inference_config": input.InferenceConfig,
		}, "", "  ")
		fmt.Printf("\n=== BEDROCK CONVERSE REQUEST (NON-STREAMING) ===\n%s\n", debugJSON)

		// Log last 2 messages for debugging
		for i := len(converseMessages) - 2; i < len(converseMessages) && i >= 0; i++ {
			msg := converseMessages[i]
			fmt.Printf("\nMessage %d [%s]:\n", i, msg.Role)
			for j, block := range msg.Content {
				switch b := block.(type) {
				case *bedrocktypes.ContentBlockMemberText:
					fmt.Printf("  Block %d [text]: %q\n", j, b.Value)
				case *bedrocktypes.ContentBlockMemberToolUse:
					fmt.Printf("  Block %d [tool_use]: %s (id: %s)\n", j, aws.ToString(b.Value.Name), aws.ToString(b.Value.ToolUseId))
				case *bedrocktypes.ContentBlockMemberToolResult:
					fmt.Printf("  Block %d [tool_result]: tool_use_id=%s\n", j, aws.ToString(b.Value.ToolUseId))
					for k, content := range b.Value.Content {
						switch c := content.(type) {
						case *bedrocktypes.ToolResultContentBlockMemberText:
							preview := c.Value
							if len(preview) > 100 {
								preview = preview[:100] + "..."
							}
							fmt.Printf("    Content %d [text]: %q\n", k, preview)
						case *bedrocktypes.ToolResultContentBlockMemberJson:
							fmt.Printf("    Content %d [json]: <document>\n", k)
						}
					}
				}
			}
		}
		fmt.Printf("=== END REQUEST ===\n\n")
	}

	// Add system prompts if present
	if len(systemBlocks) > 0 {
		input.System = systemBlocks
	}

	// Add tools if provided
	if len(tools) > 0 {
		input.ToolConfig = c.convertToolsToConverse(tools)
	}

	// Execute Converse with rate limiting if configured
	var output *bedrockruntime.ConverseOutput
	var err error

	if c.rateLimiter != nil {
		result, err := c.rateLimiter.Do(ctx, func(ctx context.Context) (interface{}, error) {
			return c.client.Converse(ctx, input)
		})
		if err != nil {
			return nil, fmt.Errorf("bedrock converse failed: %w", err)
		}
		output = result.(*bedrockruntime.ConverseOutput)
	} else {
		output, err = c.client.Converse(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("bedrock converse failed: %w", err)
		}
	}

	// Debug logging
	if os.Getenv("LOOM_DEBUG_BEDROCK") == "1" {
		fmt.Printf("\n=== BEDROCK CONVERSE RESPONSE ===\n")
		fmt.Printf("Stop Reason: %s\n", output.StopReason)
		if output.Usage != nil {
			fmt.Printf("Usage: input=%d output=%d total=%d\n",
				aws.ToInt32(output.Usage.InputTokens),
				aws.ToInt32(output.Usage.OutputTokens),
				aws.ToInt32(output.Usage.TotalTokens))
		}
		fmt.Printf("=== END RESPONSE ===\n\n")
	}

	// Extract response content
	var contentText string
	var toolCalls []llmtypes.ToolCall

	if output.Output != nil {
		switch o := output.Output.(type) {
		case *bedrocktypes.ConverseOutputMemberMessage:
			// Extract content blocks from the message
			for _, block := range o.Value.Content {
				switch b := block.(type) {
				case *bedrocktypes.ContentBlockMemberText:
					contentText += b.Value

				case *bedrocktypes.ContentBlockMemberToolUse:
					// Extract tool call
					toolCall := llmtypes.ToolCall{
						ID:    aws.ToString(b.Value.ToolUseId),
						Name:  aws.ToString(b.Value.Name),
						Input: make(map[string]interface{}),
					}

					// Map sanitized name back to original name
					if originalName, found := c.toolNameMap[toolCall.Name]; found {
						toolCall.Name = originalName
					}

					// Convert document.Interface to map[string]interface{}
					if b.Value.Input != nil {
						inputBytes, err := json.Marshal(b.Value.Input)
						if err == nil {
							_ = json.Unmarshal(inputBytes, &toolCall.Input)
						}
					}

					toolCalls = append(toolCalls, toolCall)
				}
			}
		}
	}

	// Extract usage
	usage := llmtypes.Usage{}
	if output.Usage != nil {
		usage.InputTokens = int(aws.ToInt32(output.Usage.InputTokens))
		usage.OutputTokens = int(aws.ToInt32(output.Usage.OutputTokens))
		usage.TotalTokens = int(aws.ToInt32(output.Usage.TotalTokens))
		usage.CostUSD = c.calculateCost(usage.InputTokens, usage.OutputTokens)
	}

	// Build response
	response := &llmtypes.LLMResponse{
		Content:    contentText,
		ToolCalls:  toolCalls,
		StopReason: string(output.StopReason),
		Usage:      usage,
		Metadata: map[string]interface{}{
			"model":       c.modelID,
			"stop_reason": output.StopReason,
			"latency_ms":  time.Since(startTime).Milliseconds(),
		},
	}

	// Record token usage for rate limiter metrics
	if c.rateLimiter != nil {
		c.rateLimiter.RecordTokenUsage(int64(usage.TotalTokens))
	}

	return response, nil
}
