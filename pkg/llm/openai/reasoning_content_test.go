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

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reasoning models served through OpenAI-compatible gateways (LiteLLM) return
// their trace in message.reasoning_content. It must land on Thinking — never
// Content — so reasoning-heavy turns are observable without polluting the
// user-facing answer or the replayed conversation.
func TestClient_ConvertResponse_ReasoningContent(t *testing.T) {
	client := NewClient(Config{APIKey: "test", Model: "deepseek-v3"})

	resp := &ChatCompletionResponse{
		Model: "deepseek-v3",
		Choices: []ChatCompletionChoice{
			{
				Message: ChatMessage{
					Role:             "assistant",
					Content:          "The answer is 42.",
					ReasoningContent: "the user wants the answer; compute it",
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12},
	}

	got := client.convertResponse(resp)
	assert.Equal(t, "The answer is 42.", got.Content)
	assert.Equal(t, "the user wants the answer; compute it", got.Thinking)
}

// A reasoning-only turn (no visible text) still surfaces the trace on
// Thinking while Content stays empty — the empty-answer handling above this
// layer remains in charge of what the user sees.
func TestClient_ConvertResponse_ReasoningOnly(t *testing.T) {
	client := NewClient(Config{APIKey: "test", Model: "deepseek-v3"})

	resp := &ChatCompletionResponse{
		Model: "deepseek-v3",
		Choices: []ChatCompletionChoice{
			{
				Message: ChatMessage{
					Role:             "assistant",
					ReasoningContent: "…still thinking…",
				},
				FinishReason: "length",
			},
		},
	}

	got := client.convertResponse(resp)
	assert.Empty(t, got.Content)
	assert.Equal(t, "…still thinking…", got.Thinking)
}

// reasoning_content is response-only: a request-side ChatMessage without it
// must serialize without the key at all (omitempty), so outgoing payloads to
// strict OpenAI-compatible servers are unchanged.
func TestChatMessage_ReasoningContentOmittedFromRequests(t *testing.T) {
	raw, err := json.Marshal(ChatMessage{Role: "user", Content: "hi"})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "reasoning_content")
}
