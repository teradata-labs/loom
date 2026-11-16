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
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/types"
)

const reasoningNS = "reasoning-test-ns"

// reasoningCatalog registers one reasoning and one plain model under a test
// namespace and restores the previous source afterwards.
type reasoningCatalog struct{}

func (reasoningCatalog) Lookup(_ context.Context, provider, id string) *loomv1.ModelInfo {
	if provider != reasoningNS {
		return nil
	}
	switch id {
	case "openai/gpt-5":
		return &loomv1.ModelInfo{Id: id, Provider: provider, IsReasoning: true, CostPer_1MInputUsd: 1, CostPer_1MOutputUsd: 2}
	case "meta/llama-plain":
		return &loomv1.ModelInfo{Id: id, Provider: provider, CostPer_1MInputUsd: 1, CostPer_1MOutputUsd: 2}
	}
	return nil
}

func (reasoningCatalog) List(context.Context) map[string][]*loomv1.ModelInfo { return nil }

func withReasoningCatalog(t *testing.T) {
	t.Helper()
	catalog.RegisterNamed("openai-reasoning-test", reasoningCatalog{})
	t.Cleanup(func() { catalog.RegisterNamed("openai-reasoning-test", nil) })
}

func TestReasoningParam(t *testing.T) {
	withReasoningCatalog(t)
	tests := []struct {
		name   string
		model  string
		level  string
		object bool
		want   map[string]interface{}
	}{
		{name: "off when option off", model: "openai/gpt-5", level: "high", object: false, want: nil},
		{name: "off when level empty", model: "openai/gpt-5", level: "", object: true, want: nil},
		{name: "off when none", model: "openai/gpt-5", level: "none", object: true, want: nil},
		{name: "catalog reasoning model", model: "openai/gpt-5", level: "high", object: true,
			want: map[string]interface{}{"enabled": true, "effort": "high"}},
		{name: "xhigh caps at high", model: "openai/gpt-5", level: "xhigh", object: true,
			want: map[string]interface{}{"enabled": true, "effort": "high"}},
		{name: "auto sets no effort", model: "openai/gpt-5", level: "auto", object: true,
			want: map[string]interface{}{"enabled": true}},
		{name: "claude family", model: "anthropic/claude-sonnet-4.5", level: "low", object: true,
			want: map[string]interface{}{"enabled": true, "effort": "low"}},
		{name: "non-reasoning catalog model", model: "meta/llama-plain", level: "high", object: true, want: nil},
		{name: "unknown model", model: "acme/unknown", level: "high", object: true, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(Config{APIKey: "k", Model: tt.model, ThinkingLevel: tt.level,
				CatalogProvider: reasoningNS, ReasoningObject: tt.object})
			assert.Equal(t, tt.want, c.reasoningParam(context.Background()))
			if tt.object {
				assert.Nil(t, c.thinkingParam(context.Background()), "the litellm thinking field is never sent with the reasoning object")
			}
		})
	}
}

func TestReasoningObject_ClaudeForcesTemperatureOne(t *testing.T) {
	c := NewClient(Config{APIKey: "k", Model: "anthropic/claude-sonnet-4.5", ThinkingLevel: "high",
		Temperature: 0.2, ReasoningObject: true})
	assert.Equal(t, 1.0, c.temperatureParam(context.Background()))
	off := NewClient(Config{APIKey: "k", Model: "anthropic/claude-sonnet-4.5", Temperature: 0.2, ReasoningObject: true})
	assert.InDelta(t, 0.2, off.temperatureParam(context.Background()), 1e-9)
}

const reasoningResponse = `{"id":"gen_1","object":"chat.completion","created":1,"model":"openai/gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"42","reasoning":"think think","reasoning_details":[{"type":"reasoning.text","text":"think think","signature":"sig-A","format":"anthropic-claude-v1","index":0},{"type":"reasoning.encrypted","data":"ENC","index":1}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

func TestReasoningDetails_ParsedAndReplayed(t *testing.T) {
	withReasoningCatalog(t)
	var cap capturedBodies
	srv := newBodyServer(t, &cap, func(w http.ResponseWriter, _ bool) { _, _ = io.WriteString(w, reasoningResponse) })
	c := NewClient(Config{APIKey: "k", Model: "openai/gpt-5", Endpoint: srv.URL, ThinkingLevel: "high",
		CatalogProvider: reasoningNS, ReasoningObject: true})

	resp, err := c.Chat(context.Background(), []types.Message{{Role: "user", Content: "q", Turn: 1}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "think think", resp.Thinking)
	require.Len(t, resp.ThinkingBlocks, 2)
	assert.Equal(t, llmtypes.ThinkingBlock{Type: "reasoning.text", Thinking: "think think", Signature: "sig-A", Format: "anthropic-claude-v1"}, resp.ThinkingBlocks[0])
	assert.Equal(t, llmtypes.ThinkingBlock{Type: "reasoning.encrypted", Thinking: "ENC"}, resp.ThinkingBlocks[1])
	assert.Equal(t, map[string]any{"enabled": true, "effort": "high"}, cap.last(t)["reasoning"])

	// In-turn replay: the assistant message carries reasoning_details back.
	history := []types.Message{
		{Role: "user", Content: "q", Turn: 1},
		{Role: "assistant", Content: "42", Turn: 1, ThinkingBlocks: resp.ThinkingBlocks},
		{Role: "user", Content: "and?", Turn: 1},
	}
	_, err = c.Chat(context.Background(), history, nil)
	require.NoError(t, err)
	msgs := cap.last(t)["messages"].([]any)
	var assistant map[string]any
	for _, m := range msgs {
		if mm := m.(map[string]any); mm["role"] == "assistant" {
			assistant = mm
		}
	}
	require.NotNil(t, assistant)
	assert.Nil(t, assistant["thinking_blocks"], "gateway details never ride the litellm field")
	assert.Equal(t, []any{
		map[string]any{"type": "reasoning.text", "text": "think think", "signature": "sig-A", "format": "anthropic-claude-v1", "index": float64(0)},
		map[string]any{"type": "reasoning.encrypted", "data": "ENC", "index": float64(1)},
	}, assistant["reasoning_details"])
}

func TestReasoningDetails_NotReplayedWithoutOption(t *testing.T) {
	var cap capturedBodies
	srv := newBodyServer(t, &cap, func(w http.ResponseWriter, _ bool) { _, _ = io.WriteString(w, plainChatResponse) })
	c := NewClient(Config{APIKey: "k", Model: "claude-sonnet-4-5", Endpoint: srv.URL})
	history := []types.Message{
		{Role: "user", Content: "q", Turn: 1},
		{Role: "assistant", Content: "a", Turn: 1, ThinkingBlocks: []llmtypes.ThinkingBlock{
			{Type: "reasoning.text", Thinking: "x", Signature: "s"},
			{Type: "thinking", Thinking: "y", Signature: "t"},
		}},
		{Role: "user", Content: "next", Turn: 1},
	}
	_, err := c.Chat(context.Background(), history, nil)
	require.NoError(t, err)
	for _, m := range cap.last(t)["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		assert.Nil(t, mm["reasoning_details"])
		assert.Equal(t, []any{map[string]any{"type": "thinking", "thinking": "y", "signature": "t"}}, mm["thinking_blocks"])
	}
}

func TestReasoningDetails_StreamMergedByIndex(t *testing.T) {
	withReasoningCatalog(t)
	stream := `data: {"id":"gen_1","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.text","text":"thi","index":0}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"gen_1","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[{"index":0,"delta":{"reasoning_details":[{"text":"nk","signature":"sig","index":0}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"gen_1","object":"chat.completion.chunk","created":1,"model":"openai/gpt-5","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	var cap capturedBodies
	srv := newBodyServer(t, &cap, func(w http.ResponseWriter, _ bool) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	})
	c := NewClient(Config{APIKey: "k", Model: "openai/gpt-5", Endpoint: srv.URL, ThinkingLevel: "medium",
		CatalogProvider: reasoningNS, ReasoningObject: true})
	resp, err := c.ChatStream(context.Background(), []types.Message{{Role: "user", Content: "q"}}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, "think", resp.Thinking)
	assert.Equal(t, []llmtypes.ThinkingBlock{{Type: "reasoning.text", Thinking: "think", Signature: "sig"}}, resp.ThinkingBlocks)
	assert.Equal(t, map[string]any{"enabled": true, "effort": "medium"}, cap.last(t)["reasoning"])
}
