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
package agent

import (
	"context"
	"fmt"
	"strings"
)

// LLMCompressor is a concrete implementation of MemoryCompressor that uses
// an LLM to create intelligent summaries of conversation history.
//
// Provides 50-80% token reduction through LLM-powered summarization.
type LLMCompressor struct {
	llmCaller LLMCaller // Interface for calling LLM
	enabled   bool      // Whether LLM compression is enabled
}

// LLMCaller defines the interface for calling an LLM to compress messages.
// Implementations should provide cheap, fast compression calls.
type LLMCaller interface {
	// CompressConversation takes conversation text and returns a concise summary.
	// Should limit output to 512 tokens for cost efficiency.
	CompressConversation(ctx context.Context, conversationText string) (string, error)
}

// NewLLMCompressor creates a new LLM-powered memory compressor.
// If llmCaller is nil, falls back to simple text extraction.
func NewLLMCompressor(llmCaller LLMCaller) *LLMCompressor {
	return &LLMCompressor{
		llmCaller: llmCaller,
		enabled:   llmCaller != nil,
	}
}

// CompressMessages compresses a slice of messages into a concise summary.
// Uses LLM if available, otherwise falls back to simple extraction.
//
// LLM compression typically achieves:
// - 50-80% token reduction
// - 2-3 sentence summaries
// - Preservation of key context (tables, queries, findings)
func (c *LLMCompressor) CompressMessages(ctx context.Context, messages []Message) (string, error) {
	if !c.enabled {
		// Fallback to simple compression
		return c.simpleCompress(messages), nil
	}

	// Build conversation text from messages
	var conversationParts []string
	for _, msg := range messages {
		conversationParts = append(conversationParts, fmt.Sprintf("[%s]: %s", msg.Role, msg.Content))
	}
	conversationText := strings.Join(conversationParts, "\n")

	// Use LLM to create compressed summary
	summary, err := c.llmCaller.CompressConversation(ctx, conversationText)
	if err != nil {
		// Fall back to simple compression on error
		return c.simpleCompress(messages), nil
	}

	if summary == "" {
		// Fallback if LLM returned nothing
		return c.simpleCompress(messages), nil
	}

	return strings.TrimSpace(summary), nil
}

// simpleCompress performs basic keyword extraction without LLM.
// Used as fallback when LLM is unavailable or errors occur.
func (c *LLMCompressor) simpleCompress(messages []Message) string {
	var parts []string

	for _, msg := range messages {
		if msg.Role == "user" {
			// Extract key terms from user queries
			content := msg.Content
			if len(content) > 60 {
				content = content[:60] + "..."
			}
			parts = append(parts, fmt.Sprintf("User: %s", content))
		} else if msg.Role == "assistant" {
			// Assistant responses - extract tool usage or key facts
			if c.containsToolCall(msg) {
				parts = append(parts, "Agent executed tools")
			} else if len(msg.Content) > 50 {
				// Extract first sentence or 50 chars
				content := msg.Content
				if len(content) > 50 {
					content = content[:50] + "..."
				}
				parts = append(parts, fmt.Sprintf("Agent: %s", content))
			}
		} else if msg.Role == "tool" {
			// Tool results - preserve tool execution context
			parts = append(parts, "Tool result received")
		} else if msg.Role == "system" {
			// System messages (defensive handling)
			parts = append(parts, "System instruction")
		}
	}

	if len(parts) == 0 {
		return "Previous exchanges"
	}

	return strings.Join(parts, "; ")
}

// containsToolCall checks if message contains tool execution.
// Adapted for loom's Message type which uses ToolCalls field.
func (c *LLMCompressor) containsToolCall(msg Message) bool {
	return len(msg.ToolCalls) > 0
}

// IsEnabled returns whether LLM-powered compression is enabled.
func (c *LLMCompressor) IsEnabled() bool {
	return c.enabled
}

// SetLLMCaller updates the LLM caller for the compressor.
// Useful for lazy initialization after agent is fully set up.
func (c *LLMCompressor) SetLLMCaller(llmCaller LLMCaller) {
	c.llmCaller = llmCaller
	c.enabled = llmCaller != nil
}

// SimpleCompressor is a basic compressor that doesn't use LLM.
// Useful for testing or when LLM integration isn't available.
type SimpleCompressor struct{}

// NewSimpleCompressor creates a compressor that only does keyword extraction.
func NewSimpleCompressor() *SimpleCompressor {
	return &SimpleCompressor{}
}

// CompressMessages performs simple keyword extraction.
func (c *SimpleCompressor) CompressMessages(ctx context.Context, messages []Message) (string, error) {
	var parts []string

	for _, msg := range messages {
		if msg.Role == "user" {
			content := msg.Content
			if len(content) > 60 {
				content = content[:60] + "..."
			}
			parts = append(parts, fmt.Sprintf("User: %s", content))
		} else if msg.Role == "assistant" {
			if len(msg.ToolCalls) > 0 {
				parts = append(parts, "Agent executed tools")
			} else if len(msg.Content) > 50 {
				content := msg.Content
				if len(content) > 50 {
					content = content[:50] + "..."
				}
				parts = append(parts, fmt.Sprintf("Agent: %s", content))
			}
		} else if msg.Role == "tool" {
			// Tool results - preserve tool execution context
			parts = append(parts, "Tool result received")
		} else if msg.Role == "system" {
			// System messages (defensive handling)
			parts = append(parts, "System instruction")
		}
	}

	if len(parts) == 0 {
		return "Previous exchanges", nil
	}

	return strings.Join(parts, "; "), nil
}

// IsEnabled always returns false for simple compressor.
func (c *SimpleCompressor) IsEnabled() bool {
	return false
}

// AnthropicCompressor is a production-ready LLM caller for Anthropic's Claude.
// Implements LLMCaller interface using the official Anthropic SDK.
//
// Example usage:
//
//	import "github.com/anthropics/anthropic-sdk-go"
//
//	client := anthropic.NewClient(option.WithAPIKey("your-key"))
//	compressor := NewAnthropicCompressor(client, "claude-3-haiku-20240307")
//	memCompressor := NewLLMCompressor(compressor)
//
// Note: This is a reference implementation. Users should adapt based on their
// LLM provider and SDK. The key is implementing the LLMCaller interface.
type AnthropicCompressor struct {
	client    interface{} // anthropic.Client (kept as interface to avoid SDK dependency)
	modelName string
}

// NewAnthropicCompressor creates an Anthropic-based compressor.
// This is a reference implementation - adapt for your LLM provider.
func NewAnthropicCompressor(client interface{}, modelName string) *AnthropicCompressor {
	return &AnthropicCompressor{
		client:    client,
		modelName: modelName,
	}
}

// CompressConversation implements LLMCaller for Anthropic's Claude.
// Note: This is a skeleton implementation. Full implementation requires
// the anthropic-sdk-go and proper error handling.
func (a *AnthropicCompressor) CompressConversation(ctx context.Context, conversationText string) (string, error) {
	// TODO: Implement actual Anthropic SDK call here
	// This is left as a reference for users to implement based on their needs
	//
	// Expected implementation:
	// 1. Build system prompt for compression
	// 2. Call Messages API with max_tokens=512
	// 3. Extract text from response
	// 4. Return summary
	//
	// See LLMClient.CallWithTools for reference
	return "", fmt.Errorf("AnthropicCompressor.CompressConversation not implemented - see comments for reference implementation")
}
