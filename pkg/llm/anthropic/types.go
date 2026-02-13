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

import "encoding/json"

// MessagesRequest represents a request to the Anthropic Messages API.
type MessagesRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	System      string    `json:"system,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// MessagesResponse represents a response from the Anthropic Messages API.
type MessagesResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// Message represents a single message in the conversation.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock represents a content block in a message.
// Uses custom MarshalJSON to ensure tool_use blocks always include "input": {}.
type ContentBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   string                 `json:"content,omitempty"`
	Source    *ImageSource           `json:"source,omitempty"` // For image content blocks
}

// MarshalJSON implements custom JSON marshaling for ContentBlock.
// Anthropic's API requires tool_use blocks to always have "input" present (even if empty {}).
// Go's omitempty treats empty maps the same as nil, so we handle this explicitly.
func (cb ContentBlock) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{
		"type": cb.Type,
	}
	if cb.Text != "" {
		m["text"] = cb.Text
	}
	if cb.ID != "" {
		m["id"] = cb.ID
	}
	if cb.Name != "" {
		m["name"] = cb.Name
	}
	if cb.Type == "tool_use" {
		// Anthropic API requires "input" to always be present for tool_use blocks
		if len(cb.Input) == 0 {
			m["input"] = map[string]interface{}{}
		} else {
			m["input"] = cb.Input
		}
	} else if len(cb.Input) > 0 {
		m["input"] = cb.Input
	}
	if cb.ToolUseID != "" {
		m["tool_use_id"] = cb.ToolUseID
	}
	if cb.Content != "" {
		m["content"] = cb.Content
	}
	if cb.Source != nil {
		m["source"] = cb.Source
	}
	return json.Marshal(m)
}

// ImageSource represents an image source in a content block.
type ImageSource struct {
	Type      string `json:"type"`       // "base64" or "url"
	MediaType string `json:"media_type"` // "image/jpeg", "image/png", etc.
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// Tool represents a tool definition for Claude.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"input_schema"`
}

// InputSchema represents the JSON schema for tool inputs.
type InputSchema struct {
	Type       string                            `json:"type"`
	Properties map[string]map[string]interface{} `json:"properties,omitempty"`
	Required   []string                          `json:"required,omitempty"`
}

// Usage represents token usage information.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamEvent represents a streaming event from the Anthropic API.
type StreamEvent struct {
	Type         string        `json:"type"` // message_start, content_block_start, content_block_delta, message_delta, message_stop
	Message      *Message      `json:"message,omitempty"`
	Index        int           `json:"index,omitempty"`
	ContentBlock *ContentBlock `json:"content_block,omitempty"`
	Delta        *StreamDelta  `json:"delta,omitempty"`
	Usage        *Usage        `json:"usage,omitempty"`
}

// StreamDelta represents a delta in a streaming event.
type StreamDelta struct {
	Type        string `json:"type,omitempty"`         // text_delta, input_json_delta
	Text        string `json:"text,omitempty"`         // For text deltas
	PartialJSON string `json:"partial_json,omitempty"` // For input_json_delta (tool input streaming)
	StopReason  string `json:"stop_reason,omitempty"`  // For message_delta events
}
