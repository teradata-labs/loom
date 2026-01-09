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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// ChatStream is DISABLED due to bugs with document.NewLazyDocument causing empty tool inputs.
// The ConverseStream API cannot properly serialize tool schemas, resulting in all tool calls
// having empty parameters ({}). Use the non-streaming Chat() method instead.
func (c *Client) ChatStream(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool, tokenCallback llmtypes.TokenCallback) (*llmtypes.LLMResponse, error) {
	// DISABLED: Fall back to non-streaming
	// The ConverseStream API has issues with tool parameter serialization
	return c.Chat(ctx, messages, tools)
}

// convertMessagesToConverse converts internal messages to Bedrock Converse API format.
// CRITICAL: AWS Bedrock requires all tool results from the same turn to be in a single user message.
// We aggregate consecutive tool messages into one message with multiple tool_result blocks.
func (c *Client) convertMessagesToConverse(messages []llmtypes.Message) ([]bedrocktypes.SystemContentBlock, []bedrocktypes.Message) {
	var systemBlocks []bedrocktypes.SystemContentBlock
	var converseMessages []bedrocktypes.Message

	// Track pending tool results to aggregate them
	var pendingToolResults []bedrocktypes.ContentBlock

	// Helper to flush pending tool results
	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			converseMessages = append(converseMessages, bedrocktypes.Message{
				Role:    bedrocktypes.ConversationRoleUser,
				Content: pendingToolResults,
			})
			pendingToolResults = nil
		}
	}

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// System messages go in separate system field
			if msg.Content != "" {
				systemBlocks = append(systemBlocks, &bedrocktypes.SystemContentBlockMemberText{
					Value: msg.Content,
				})
			}

		case "user":
			// Flush any pending tool results before adding user message
			flushToolResults()

			var contentBlocks []bedrocktypes.ContentBlock

			// Handle multi-modal content blocks
			if len(msg.ContentBlocks) > 0 {
				for _, block := range msg.ContentBlocks {
					switch block.Type {
					case "text":
						if block.Text != "" {
							contentBlocks = append(contentBlocks, &bedrocktypes.ContentBlockMemberText{
								Value: block.Text,
							})
						}
					case "image":
						if block.Image != nil {
							var imageSource bedrocktypes.ImageSource
							if block.Image.Source.Type == "base64" {
								imageSource = &bedrocktypes.ImageSourceMemberBytes{
									Value: []byte(block.Image.Source.Data),
								}
							}
							contentBlocks = append(contentBlocks, &bedrocktypes.ContentBlockMemberImage{
								Value: bedrocktypes.ImageBlock{
									Format: bedrocktypes.ImageFormat(block.Image.Source.MediaType),
									Source: imageSource,
								},
							})
						}
					}
				}
			} else if msg.Content != "" {
				// Plain text message
				contentBlocks = append(contentBlocks, &bedrocktypes.ContentBlockMemberText{
					Value: msg.Content,
				})
			}

			if len(contentBlocks) > 0 {
				converseMessages = append(converseMessages, bedrocktypes.Message{
					Role:    bedrocktypes.ConversationRoleUser,
					Content: contentBlocks,
				})
			}

		case "assistant":
			// Flush any pending tool results before adding assistant message
			flushToolResults()

			var contentBlocks []bedrocktypes.ContentBlock

			// Add text content if present
			if msg.Content != "" {
				contentBlocks = append(contentBlocks, &bedrocktypes.ContentBlockMemberText{
					Value: msg.Content,
				})
			}

			// Add tool calls
			for _, tc := range msg.ToolCalls {
				// Ensure input is never nil
				input := tc.Input
				if input == nil {
					input = map[string]interface{}{}
				}

				// Convert input to document.Interface
				inputDoc := document.NewLazyDocument(input)

				contentBlocks = append(contentBlocks, &bedrocktypes.ContentBlockMemberToolUse{
					Value: bedrocktypes.ToolUseBlock{
						ToolUseId: aws.String(tc.ID),
						Name:      aws.String(sanitizeToolName(tc.Name)),
						Input:     inputDoc,
					},
				})

				// Store name mapping for later
				c.toolNameMap[sanitizeToolName(tc.Name)] = tc.Name
			}

			if len(contentBlocks) > 0 {
				converseMessages = append(converseMessages, bedrocktypes.Message{
					Role:    bedrocktypes.ConversationRoleAssistant,
					Content: contentBlocks,
				})
			}

		case "tool":
			// Tool results must be aggregated into a single user message
			// Add this tool result to pending results (will be flushed when we see next non-tool message)
			var toolResultContent bedrocktypes.ToolResultContentBlock

			// Try to parse content as JSON for structured results
			var contentData interface{}
			if err := json.Unmarshal([]byte(msg.Content), &contentData); err == nil {
				// Content is valid JSON - use JSON block
				toolResultDoc := document.NewLazyDocument(contentData)
				toolResultContent = &bedrocktypes.ToolResultContentBlockMemberJson{
					Value: toolResultDoc,
				}
			} else {
				// Content is plain text (including error messages) - use text block
				toolResultContent = &bedrocktypes.ToolResultContentBlockMemberText{
					Value: msg.Content,
				}
			}

			// Add to pending tool results (will be combined into one message)
			pendingToolResults = append(pendingToolResults, &bedrocktypes.ContentBlockMemberToolResult{
				Value: bedrocktypes.ToolResultBlock{
					ToolUseId: aws.String(msg.ToolUseID),
					Content: []bedrocktypes.ToolResultContentBlock{
						toolResultContent,
					},
				},
			})
		}
	}

	// Flush any remaining tool results at the end
	flushToolResults()

	return systemBlocks, converseMessages
}

// convertToolsToConverse converts shuttle tools to Bedrock Converse ToolConfiguration.
func (c *Client) convertToolsToConverse(tools []shuttle.Tool) *bedrocktypes.ToolConfiguration {
	var converseTools []bedrocktypes.Tool

	// Clear previous mapping
	c.toolNameMap = make(map[string]string)

	for _, tool := range tools {
		originalName := tool.Name()
		sanitizedName := sanitizeToolName(originalName)

		// Store mapping for later conversion back
		c.toolNameMap[sanitizedName] = originalName

		// Convert input schema
		schema := tool.InputSchema()
		var inputSchema bedrocktypes.ToolInputSchema

		if schema != nil {
			// Build JSON schema document
			schemaMap := map[string]interface{}{
				"type":       "object",
				"properties": convertSchemaProperties(schema.Properties),
			}
			if len(schema.Required) > 0 {
				schemaMap["required"] = schema.Required
			}

			// Debug: Log the schema map before converting to document
			if os.Getenv("LOOM_DEBUG_BEDROCK") == "1" {
				schemaJSON, _ := json.MarshalIndent(schemaMap, "", "  ")
				fmt.Printf("DEBUG: Schema for tool %s:\n%s\n", sanitizedName, schemaJSON)
			}

			// Create document from the schema map
			// NOTE: Pass the map value, not a pointer to it
			doc := document.NewLazyDocument(schemaMap)
			inputSchema = &bedrocktypes.ToolInputSchemaMemberJson{
				Value: doc,
			}
		}

		// Create tool specification
		converseTools = append(converseTools, &bedrocktypes.ToolMemberToolSpec{
			Value: bedrocktypes.ToolSpecification{
				Name:        aws.String(sanitizedName),
				Description: aws.String(tool.Description()),
				InputSchema: inputSchema,
			},
		})
	}

	return &bedrocktypes.ToolConfiguration{
		Tools: converseTools,
	}
}
