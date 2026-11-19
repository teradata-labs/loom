// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package openairesponses

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/llm"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// toJSON round-trips v into generic JSON for shape assertions.
func toJSON(t *testing.T, v interface{}) interface{} {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err, "marshal")
	var out interface{}
	require.NoError(t, json.Unmarshal(b, &out), "unmarshal")
	return out
}

func TestRequestBodyShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		config   Config
		messages []llmtypes.Message
		tools    []shuttle.Tool
		check    func(t *testing.T, body map[string]interface{})
	}{
		{
			name:     "text only",
			config:   Config{Model: "openai/gpt-5.5", MaxTokens: 400, Temperature: 0.3},
			messages: []llmtypes.Message{{Role: "user", Content: "hello"}},
			check: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, "openai/gpt-5.5", body["model"], "model")
				assert.Equal(t, false, body["store"], "store must always be sent as false")
				assert.EqualValues(t, 400, body["max_output_tokens"], "max_output_tokens")
				assert.InDelta(t, 0.3, body["temperature"], 1e-9, "temperature is sent with reasoning off")
				assert.NotContains(t, body, "tools", "no tools member without tools")
				assert.NotContains(t, body, "tool_choice", "no tool_choice without tools")
				assert.NotContains(t, body, "parallel_tool_calls", "no parallel_tool_calls without tools")
				assert.NotContains(t, body, "reasoning", "no reasoning with thinking off")
				assert.NotContains(t, body, "include", "no include with thinking off")
				assert.NotContains(t, body, "stream", "stream omitted on a blocking call")
				assert.NotContains(t, body, "previous_response_id", "never sent")
				assert.NotContains(t, body, "instructions", "no instructions without system messages")
				assert.Equal(t, toJSON(t, []interface{}{
					map[string]interface{}{"role": "user", "content": []interface{}{
						map[string]interface{}{"type": "input_text", "text": "hello"}}},
				}), body["input"], "input")
			},
		},
		{
			name:     "tools",
			config:   Config{Model: "m"},
			messages: []llmtypes.Message{{Role: "user", Content: "weather?"}},
			tools:    []shuttle.Tool{weatherTool()},
			check: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, "auto", body["tool_choice"], "tool_choice")
				assert.Equal(t, true, body["parallel_tool_calls"], "parallel_tool_calls")
				tools, ok := body["tools"].([]interface{})
				require.True(t, ok, "tools must be an array")
				require.Len(t, tools, 1, "one tool")
				tool := tools[0].(map[string]interface{})
				assert.Equal(t, "function", tool["type"], "flat function tool")
				assert.Equal(t, "weather_get_weather", tool["name"], "name sanitized")
				assert.Equal(t, "Get weather", tool["description"], "description")
				assert.Equal(t, false, tool["strict"], "strict must be sent as false")
				params := tool["parameters"].(map[string]interface{})
				assert.Equal(t, "object", params["type"], "parameters schema")
				assert.NotContains(t, tool, "function", "Chat Completions nesting must not appear")
			},
		},
		{
			name:   "instructions from leading system messages; later system stays in place",
			config: Config{Model: "m"},
			messages: []llmtypes.Message{
				{Role: "system", Content: "rule one"},
				{Role: "system", Content: "rule two"},
				{Role: "user", Content: "hi"},
				{Role: "system", Content: "late note"},
			},
			check: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, "rule one\n\nrule two", body["instructions"], "leading system messages joined")
				input := body["input"].([]interface{})
				require.Len(t, input, 2, "user + late system")
				late := input[1].(map[string]interface{})
				assert.Equal(t, "system", late["role"], "late system message keeps its role")
			},
		},
		{
			name:     "reasoning on",
			config:   Config{Model: "openai/gpt-5.5", ThinkingLevel: "high", Temperature: 0.2},
			messages: []llmtypes.Message{{Role: "user", Content: "think"}},
			check: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, map[string]interface{}{"effort": "high", "summary": "auto"}, body["reasoning"], "reasoning")
				assert.Equal(t, []interface{}{"reasoning.encrypted_content"}, body["include"], "include")
				assert.NotContains(t, body, "temperature", "temperature omitted with reasoning on")
			},
		},
		{
			name:     "thinking auto maps to medium",
			config:   Config{Model: "m", ThinkingLevel: "auto"},
			messages: []llmtypes.Message{{Role: "user", Content: "x"}},
			check: func(t *testing.T, body map[string]interface{}) {
				assert.Equal(t, "medium", body["reasoning"].(map[string]interface{})["effort"], "auto → medium")
			},
		},
		{
			name:     "thinking none is off",
			config:   Config{Model: "m", ThinkingLevel: "none"},
			messages: []llmtypes.Message{{Role: "user", Content: "x"}},
			check: func(t *testing.T, body map[string]interface{}) {
				assert.NotContains(t, body, "reasoning", "none sends no reasoning")
				assert.InDelta(t, DefaultTemperature, body["temperature"], 1e-9, "default temperature")
			},
		},
		{
			name:   "user images",
			config: Config{Model: "m"},
			messages: []llmtypes.Message{{Role: "user", ContentBlocks: []types.ContentBlock{
				{Type: "text", Text: "look"},
				{Type: "image", Image: &types.ImageContent{Source: types.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}}},
				{Type: "image", Image: &types.ImageContent{Source: types.ImageSource{Type: "url", URL: "https://example.invalid/a.png"}}},
				{Type: "image"},
			}}},
			check: func(t *testing.T, body map[string]interface{}) {
				content := body["input"].([]interface{})[0].(map[string]interface{})["content"]
				assert.Equal(t, toJSON(t, []interface{}{
					map[string]interface{}{"type": "input_text", "text": "look"},
					map[string]interface{}{"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
					map[string]interface{}{"type": "input_image", "image_url": "https://example.invalid/a.png"},
				}), content, "image parts")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
			cfg := tt.config
			cfg.APIKey = testAPIKey
			cfg.Endpoint = rec.srv.URL
			_, err := NewClient(cfg).Chat(context.Background(), tt.messages, tt.tools)
			require.NoError(t, err, "chat must succeed")
			tt.check(t, rec.body(t, 0))
		})
	}
}

func TestRequestHeaders(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
	c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL, ExtraHeaders: map[string]string{"X-Tag": "v"}})
	_, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil)
	require.NoError(t, err, "chat")
	h := rec.hdrs[0]
	assert.Equal(t, "Bearer "+testAPIKey, h.Get("Authorization"), "bearer auth")
	assert.Equal(t, "application/json", h.Get("Content-Type"), "content type")
	assert.Equal(t, "v", h.Get("X-Tag"), "extra header")
}

// A multi-turn tool loop: the assistant's reasoning items go back first, in
// order, then its text, then its function_call items, then the outputs.
func TestMultiTurnToolLoopWithReasoningReplay(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
	c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL, Model: "openai/gpt-5.5", ThinkingLevel: "medium"})

	messages := []llmtypes.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "weather in Paris and Rome?"},
		{
			Role:    "assistant",
			Content: "Checking both.",
			ThinkingBlocks: []llmtypes.ThinkingBlock{
				{Type: blockTypeReasoning, Format: "rs_1", Signature: "enc-1", Thinking: "part a"},
				{Type: blockTypeReasoningSummary, Thinking: "part b"},
				{Type: "thinking", Thinking: "foreign anthropic block", Signature: "sig"},
				{Type: blockTypeReasoning, Format: "rs_2", Signature: "enc-2"},
			},
			ToolCalls: []llmtypes.ToolCall{
				{ID: "call_1", Name: "weather:get_weather", Input: map[string]interface{}{"location": "Paris"}},
				{ID: "call_2", Name: "weather:get_weather"},
				{ID: "", Name: "dropped"},
			},
		},
		{Role: "tool", ToolUseID: "call_1", Content: "sunny"},
		{Role: "tool", ToolUseID: "call_2", Content: ""},
		{Role: "tool", ToolUseID: "call_orphan", Content: "orphan"},
	}
	_, err := c.Chat(context.Background(), messages, []shuttle.Tool{weatherTool()})
	require.NoError(t, err, "chat")

	want := []interface{}{
		map[string]interface{}{"role": "user", "content": []interface{}{
			map[string]interface{}{"type": "input_text", "text": "weather in Paris and Rome?"}}},
		map[string]interface{}{"type": "reasoning", "id": "rs_1", "encrypted_content": "enc-1", "summary": []interface{}{
			map[string]interface{}{"type": "summary_text", "text": "part a"},
			map[string]interface{}{"type": "summary_text", "text": "part b"}}},
		map[string]interface{}{"type": "reasoning", "id": "rs_2", "encrypted_content": "enc-2", "summary": []interface{}{}},
		map[string]interface{}{"role": "assistant", "content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": "Checking both."}}},
		map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "weather_get_weather", "arguments": `{"location":"Paris"}`},
		map[string]interface{}{"type": "function_call", "call_id": "call_2", "name": "weather_get_weather", "arguments": `{}`},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_2", "output": ""},
	}
	assert.Equal(t, want, rec.body(t, 0)["input"], "reasoning first, foreign blocks and orphan outputs dropped")
}

// Blocks produced by convertResponse must replay to the same item.
func TestReasoningBlocksRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		item outputItem
	}{
		{"empty summary", outputItem{Type: "reasoning", ID: "rs_a", EncryptedContent: "e1", Summary: []summaryPart{}}},
		{"one part", outputItem{Type: "reasoning", ID: "rs_b", EncryptedContent: "e2", Summary: []summaryPart{{Type: "summary_text", Text: "x"}}}},
		{"three parts, no id", outputItem{Type: "reasoning", EncryptedContent: "e3", Summary: []summaryPart{
			{Type: "summary_text", Text: "a"}, {Type: "summary_text", Text: ""}, {Type: "summary_text", Text: "c"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			items := reasoningItems(reasoningBlocks(tt.item))
			require.Len(t, items, 1, "one item back")
			got := items[0]
			assert.Equal(t, tt.item.ID, got.ID, "id")
			assert.Equal(t, tt.item.EncryptedContent, got.EncryptedContent, "encrypted_content")
			require.NotNil(t, got.Summary, "summary always present")
			assert.Equal(t, len(tt.item.Summary), len(*got.Summary), "summary part count")
			for i := range tt.item.Summary {
				assert.Equal(t, tt.item.Summary[i].Text, (*got.Summary)[i].Text, "summary part %d", i)
			}
		})
	}
	assert.Empty(t, reasoningItems([]llmtypes.ThinkingBlock{{Type: blockTypeReasoningSummary, Thinking: "stray"}}),
		"a continuation block without a start block is dropped")
}

func TestReasoningSuppressedForEnabledFormClaude(t *testing.T) {
	t.Parallel()
	c := NewClient(Config{Model: "anthropic/claude-sonnet-4-5", ThinkingLevel: "high"})
	assert.NotNil(t, c.reasoningParam(context.Background()), "reasoning requested normally")
	assert.Nil(t, c.reasoningParam(llm.ContextSuppressThinking(context.Background())),
		"suppressed on a rebuilt tool turn for an enabled-form Claude model")
}

func TestExtraBody(t *testing.T) {
	t.Parallel()
	t.Run("merged into the body", func(t *testing.T) {
		t.Parallel()
		rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
		extra := llm.ExtraBody{"providerOptions": map[string]interface{}{"gateway": map[string]interface{}{"order": []string{"openai"}}}}
		require.NoError(t, ValidateExtraBody(extra), "gateway options are valid")
		c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL, ExtraBody: extra})
		_, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil)
		require.NoError(t, err, "chat")
		body := rec.body(t, 0)
		assert.Equal(t, toJSON(t, extra["providerOptions"]), body["providerOptions"], "extra member present")
		assert.Equal(t, "gpt-4.1", body["model"], "request fields intact")
	})
	for _, key := range []string{"model", "input", "store", "instructions", "tools", "reasoning", "include", "stream", "previous_response_id"} {
		t.Run("rejects "+key, func(t *testing.T) {
			t.Parallel()
			extra := llm.ExtraBody{key: "x"}
			require.Error(t, ValidateExtraBody(extra), "%s collides", key)
			rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
			c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL, ExtraBody: extra})
			_, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil)
			require.Error(t, err, "every call fails with an invalid ExtraBody")
			_, err = c.ChatStream(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, nil, nil)
			require.Error(t, err, "streaming calls fail too")
			assert.Equal(t, 0, rec.count(), "nothing is sent")
		})
	}
}

func TestToolSchemaErrorStrict(t *testing.T) {
	t.Parallel()
	bad := &shuttle.MockTool{MockName: "bad", MockSchema: &shuttle.JSONSchema{Type: "string"}}
	rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
	c := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL, StrictToolSchemas: true})
	_, err := c.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, []shuttle.Tool{bad})
	require.Error(t, err, "strict schema policy fails the call")
	assert.Equal(t, 0, rec.count(), "nothing is sent")

	lenient := NewClient(Config{APIKey: testAPIKey, Endpoint: rec.srv.URL})
	_, err = lenient.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "x"}}, []shuttle.Tool{bad})
	require.NoError(t, err, "lenient policy omits the tool")
	assert.NotContains(t, rec.body(t, 0), "tools", "bad tool omitted")
}

func TestCanceledContextSendsNothing(t *testing.T) {
	t.Parallel()
	rec := newRecorder(t, jsonHandler(http.StatusOK, minimalCompleted))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClient(Config{Endpoint: rec.srv.URL}).Chat(ctx, nil, nil)
	require.ErrorIs(t, err, context.Canceled, "canceled context")
	assert.Equal(t, 0, rec.count(), "nothing sent")
}

func TestNameModelDefaults(t *testing.T) {
	t.Parallel()
	c := NewClient(Config{})
	assert.Equal(t, ProviderName, c.Name(), "name")
	assert.Equal(t, DefaultModel, c.Model(), "default model")
	assert.Equal(t, DefaultEndpoint, c.endpoint, "default endpoint")
	assert.Equal(t, DefaultMaxTokens, c.maxTokens, "default max tokens")
	assert.Equal(t, DefaultCatalogProvider, c.catalogProvider, "default catalog provider")
}

func TestReasoningEffortMapsEveryLoomLevel(t *testing.T) {
	tests := map[string]string{
		"": "", "none": "", "off": "",
		"low": "low", "medium": "medium", "auto": "medium",
		"high": "high", "xhigh": "high", "max": "high",
	}
	for level, want := range tests {
		assert.Equal(t, want, reasoningEffort(level), "level %q", level)
	}
}
