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

// OpenAI API types for chat completions
// Reference: https://platform.openai.com/docs/api-reference/chat

// ChatCompletionRequest represents a request to the OpenAI chat completions API.
type ChatCompletionRequest struct {
	Model            string                 `json:"model"`
	Messages         []ChatMessage          `json:"messages"`
	Temperature      float64                `json:"temperature,omitempty"`
	MaxTokens        int                    `json:"max_tokens,omitempty"`
	TopP             float64                `json:"top_p,omitempty"`
	FrequencyPenalty float64                `json:"frequency_penalty,omitempty"`
	PresencePenalty  float64                `json:"presence_penalty,omitempty"`
	Tools            []Tool                 `json:"tools,omitempty"`
	ToolChoice       interface{}            `json:"tool_choice,omitempty"` // "auto", "none", or {"type": "function", "function": {"name": "..."}}
	Stream           bool                   `json:"stream,omitempty"`
	User             string                 `json:"user,omitempty"`
	ResponseFormat   map[string]interface{} `json:"response_format,omitempty"`
}

// ChatMessage represents a message in the conversation.
type ChatMessage struct {
	Role       string      `json:"role"` // "system", "user", "assistant", "tool"
	Content    interface{} `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"` // For tool role messages
}

// ToolCall represents a function call from the assistant.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // Always "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall represents the function being called.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// Tool represents a function that the model can call.
type Tool struct {
	Type     string      `json:"type"` // Always "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef defines a function available to the model.
type FunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// ChatCompletionResponse represents the response from OpenAI.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   ChatCompletionUsage    `json:"usage"`
	Error   *OpenAIError           `json:"error,omitempty"`
}

// ChatCompletionChoice represents a completion choice.
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"` // "stop", "length", "tool_calls", "content_filter", "function_call"
}

// ChatCompletionUsage represents token usage information.
type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIError represents an error from the OpenAI API.
type OpenAIError struct {
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Param   interface{} `json:"param"`
	Code    interface{} `json:"code"`
}

// ChatCompletionStreamChunk represents a chunk in a streaming response.
type ChatCompletionStreamChunk struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"` // "chat.completion.chunk"
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []ChatCompletionStreamChoice `json:"choices"`
	Usage   *ChatCompletionUsage         `json:"usage,omitempty"` // Only present in final chunk
}

// ChatCompletionStreamChoice represents a choice in a streaming chunk.
type ChatCompletionStreamChoice struct {
	Index        int              `json:"index"`
	Delta        ChatMessageDelta `json:"delta"`
	FinishReason string           `json:"finish_reason,omitempty"`
}

// ChatMessageDelta represents a delta in a streaming message.
type ChatMessageDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   interface{}     `json:"content,omitempty"` // string or null
	ToolCalls []ToolCallDelta `json:"tool_calls,omitempty"`
}

// ToolCallDelta represents a delta in a tool call.
type ToolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"` // "function"
	Function FunctionCallDelta `json:"function,omitempty"`
}

// FunctionCallDelta represents a delta in a function call.
type FunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // Partial JSON string
}
