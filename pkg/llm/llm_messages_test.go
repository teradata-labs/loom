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
package llm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"github.com/teradata-labs/loom/pkg/types"
)

// captureTool is a minimal shuttle.Tool with a fixed schema.
type captureTool struct {
	name, description string
	schema            *shuttle.JSONSchema
}

func (c captureTool) Name() string                     { return c.name }
func (c captureTool) Description() string              { return c.description }
func (c captureTool) InputSchema() *shuttle.JSONSchema { return c.schema }
func (c captureTool) Backend() string                  { return "" }
func (c captureTool) Execute(context.Context, map[string]interface{}) (*shuttle.Result, error) {
	return &shuttle.Result{Success: true}, nil
}

// paramProvider is a provider that reports its invocation parameters.
type paramProvider struct {
	*mockStreamingLLMProvider
	params map[string]interface{}
}

func (p paramProvider) InvocationParameters(context.Context) map[string]interface{} { return p.params }

// withMessageCapture turns message capture on (and sets the content bound)
// for one test.
func withMessageCapture(t *testing.T, maxContent int) {
	t.Helper()
	observability.SetCaptureLLMMessages(true)
	observability.SetMaxContentLength(maxContent)
	t.Cleanup(func() {
		observability.SetCaptureLLMMessages(false)
		observability.SetMaxContentLength(observability.DefaultMaxContentLength)
	})
}

func captureKeys(attrs map[string]interface{}) []string {
	var keys []string
	for k := range attrs {
		if observability.IsLLMMessageCaptureKey(k) {
			keys = append(keys, k)
		}
	}
	return keys
}

func conversation() []llmtypes.Message {
	return []llmtypes.Message{
		{Role: "system", Content: "You are the conference agent."},
		{Role: "user", Content: "Find AI conferences in December."},
		{Role: "assistant", Content: "Searching.", ToolCalls: []llmtypes.ToolCall{
			{ID: "toolu_1", Name: "web_search", Input: map[string]interface{}{"query": "AI conferences December 2026"}},
		}},
		{Role: "tool", ToolUseID: "toolu_1", Content: `{"results":["NeurIPS"]}`},
		{Role: "system", Content: "Relevant memories from past conversations:\n- User prefers academic events"},
		{Role: "user", ContentBlocks: []types.ContentBlock{
			{Type: "text", Text: "And this poster?"},
			{Type: "image", Image: &types.ImageContent{Type: "image"}},
		}},
	}
}

func TestInstrumentedProvider_MessageCaptureOffByDefault(t *testing.T) {
	tracer := newMockTracer()
	provider := NewInstrumentedProvider(&mockLLMProvider{name: "bedrock", model: "claude", response: &llmtypes.LLMResponse{Content: "ok"}}, tracer)

	_, err := provider.Chat(context.Background(), conversation(), []shuttle.Tool{captureTool{name: "web_search"}})
	require.NoError(t, err)
	require.Len(t, tracer.spans, 1)
	assert.Empty(t, captureKeys(tracer.spans[0].Attributes), "nothing beyond the previews unless otlp_capture_llm_messages is on")
}

// TestInstrumentedProvider_CapturesRequestAndResponseMessages: with capture on,
// both call paths record every message (system prompt, history, tool call and
// result, the memory block, image placeholders), the tool schemas, the
// provider's parameters and the response as OpenInference attributes.
func TestInstrumentedProvider_CapturesRequestAndResponseMessages(t *testing.T) {
	withMessageCapture(t, 0)
	response := &llmtypes.LLMResponse{Content: "Checking the venue.", ToolCalls: []llmtypes.ToolCall{
		{ID: "toolu_2", Name: "web_fetch", Input: map[string]interface{}{"url": "https://example.org"}},
	}}
	tools := []shuttle.Tool{captureTool{
		name:        "web_search",
		description: "Search the web.",
		schema:      shuttle.NewObjectSchema("", map[string]*shuttle.JSONSchema{"query": shuttle.NewStringSchema("What to search for")}, []string{"query"}),
	}}

	for _, stream := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			tracer := newMockTracer()
			inner := paramProvider{
				mockStreamingLLMProvider: &mockStreamingLLMProvider{&mockLLMProvider{name: "bedrock", model: "claude-opus", response: response}},
				params:                   map[string]interface{}{"max_tokens": 4096, "temperature": 0.2},
			}
			provider := NewInstrumentedProvider(inner, tracer)
			var err error
			if stream {
				_, err = provider.ChatStream(context.Background(), conversation(), tools, nil)
			} else {
				_, err = provider.Chat(context.Background(), conversation(), tools)
			}
			require.NoError(t, err)
			require.Len(t, tracer.spans, 1)
			a := tracer.spans[0].Attributes

			assert.Equal(t, "system", a["llm.input_messages.0.message.role"])
			assert.Equal(t, "You are the conference agent.", a["llm.input_messages.0.message.content"])
			assert.Equal(t, "user", a["llm.input_messages.1.message.role"])
			assert.Equal(t, "Searching.", a["llm.input_messages.2.message.content"])
			assert.Equal(t, "toolu_1", a["llm.input_messages.2.message.tool_calls.0.tool_call.id"])
			assert.Equal(t, "web_search", a["llm.input_messages.2.message.tool_calls.0.tool_call.function.name"])
			assert.JSONEq(t, `{"query":"AI conferences December 2026"}`, a["llm.input_messages.2.message.tool_calls.0.tool_call.function.arguments"].(string))
			assert.Equal(t, "tool", a["llm.input_messages.3.message.role"])
			assert.Equal(t, "toolu_1", a["llm.input_messages.3.message.tool_call_id"])
			assert.Equal(t, `{"results":["NeurIPS"]}`, a["llm.input_messages.3.message.content"])
			assert.Contains(t, a["llm.input_messages.4.message.content"], "Relevant memories from past conversations")
			assert.Equal(t, "And this poster?\n[1 image block(s) not exported]", a["llm.input_messages.5.message.content"])
			assert.NotContains(t, a, "llm.input_messages.6.message.role")
			assert.NotContains(t, a, "llm.messages.omitted")

			var schema map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(a["llm.tools.0.tool.json_schema"].(string)), &schema))
			assert.Equal(t, "function", schema["type"])
			fn := schema["function"].(map[string]interface{})
			assert.Equal(t, "web_search", fn["name"])
			assert.Equal(t, "Search the web.", fn["description"])
			assert.Contains(t, fn["parameters"].(map[string]interface{})["properties"], "query")

			assert.JSONEq(t, `{"model":"claude-opus","max_tokens":4096,"temperature":0.2}`, a["llm.invocation_parameters"].(string))

			assert.Equal(t, "assistant", a["llm.output_messages.0.message.role"])
			assert.Equal(t, "Checking the venue.", a["llm.output_messages.0.message.content"])
			assert.Equal(t, "web_fetch", a["llm.output_messages.0.message.tool_calls.0.tool_call.function.name"])

			// The previews are unchanged.
			assert.Equal(t, "And this poster?", a["message.preview"])
			assert.Equal(t, 1, a["llm.tools.count"])
		})
	}
}

func TestInstrumentedProvider_CaptureWithoutParameterReporter(t *testing.T) {
	withMessageCapture(t, 0)
	tracer := newMockTracer()
	provider := NewInstrumentedProvider(&mockLLMProvider{name: "ollama", model: "llama3", response: &llmtypes.LLMResponse{Content: "hi"}}, tracer)
	_, err := provider.Chat(context.Background(), []llmtypes.Message{{Role: "user", Content: "hello"}}, nil)
	require.NoError(t, err)
	a := tracer.spans[0].Attributes
	assert.JSONEq(t, `{"model":"llama3"}`, a["llm.invocation_parameters"].(string))
	assert.NotContains(t, a, "llm.tools.0.tool.json_schema")
}

func TestInstrumentedProvider_CaptureFollowsContentBound(t *testing.T) {
	withMessageCapture(t, 10)
	tracer := newMockTracer()
	provider := NewInstrumentedProvider(&mockLLMProvider{name: "bedrock", model: "claude", response: &llmtypes.LLMResponse{Content: "a long answer here"}}, tracer)
	_, err := provider.Chat(context.Background(), []llmtypes.Message{
		{Role: "user", Content: "a long question here"},
		{Role: "assistant", ToolCalls: []llmtypes.ToolCall{{Name: "t", Input: map[string]interface{}{"q": "0123456789"}}}},
	}, nil)
	require.NoError(t, err)
	a := tracer.spans[0].Attributes
	assert.Equal(t, "a long que…", a["llm.input_messages.0.message.content"])
	assert.Equal(t, `{"q":"0123…`, a["llm.input_messages.1.message.tool_calls.0.tool_call.function.arguments"])
	assert.Equal(t, "a long ans…", a["llm.output_messages.0.message.content"])
}

// TestRecordInputMessages_KeepsSystemPromptAndNewestWithinBudget: a
// conversation over the span budget keeps the leading system messages and the
// newest messages, and one placeholder (counted in llm.messages.omitted)
// stands for the history in between, with contiguous indices.
func TestRecordInputMessages_KeepsSystemPromptAndNewestWithinBudget(t *testing.T) {
	withMessageCapture(t, 0)
	kib := strings.Repeat("x", 1024)
	messages := []llmtypes.Message{{Role: "system", Content: "system prompt"}, {Role: "system", Content: "session context"}}
	for i := 0; i < 400; i++ {
		messages = append(messages, llmtypes.Message{Role: "user", Content: strconv.Itoa(i) + kib})
	}
	span := &observability.Span{Attributes: map[string]interface{}{}}
	recordInputMessages(span, messages)
	a := span.Attributes

	omitted, ok := a["llm.messages.omitted"].(int)
	require.True(t, ok)
	kept := 400 - omitted
	assert.Greater(t, omitted, 0)
	assert.Greater(t, kept, 200)
	assert.Equal(t, "system prompt", a["llm.input_messages.0.message.content"])
	assert.Equal(t, "session context", a["llm.input_messages.1.message.content"])
	assert.Equal(t, omittedMessagesRole, a["llm.input_messages.2.message.role"])
	assert.Contains(t, a["llm.input_messages.2.message.content"], strconv.Itoa(omitted)+" earlier messages not exported")
	last := 3 + kept - 1
	assert.True(t, strings.HasPrefix(a["llm.input_messages."+strconv.Itoa(last)+".message.content"].(string), "399x"), "the newest message is kept")
	assert.True(t, strings.HasPrefix(a["llm.input_messages.3.message.content"].(string), strconv.Itoa(omitted)+"x"), "the oldest kept follows the placeholder")
	assert.NotContains(t, a, "llm.input_messages."+strconv.Itoa(last+1)+".message.role")

	size := 0
	for k, v := range a {
		if strings.HasSuffix(k, ".message.content") && k != "llm.input_messages.2.message.content" {
			size += len(v.(string))
		}
	}
	assert.LessOrEqual(t, size, captureInputBudget)
}

func TestRecordInputMessages_AttributeBudget(t *testing.T) {
	withMessageCapture(t, 0)
	var messages []llmtypes.Message
	for i := 0; i < 1500; i++ {
		messages = append(messages, llmtypes.Message{Role: "assistant", Content: "c", ToolCalls: []llmtypes.ToolCall{{ID: "i", Name: "n"}}})
	}
	span := &observability.Span{Attributes: map[string]interface{}{}}
	recordInputMessages(span, messages)
	assert.LessOrEqual(t, len(captureKeys(span.Attributes)), captureInputMaxAttributes+2, "the placeholder adds two")
	assert.Positive(t, span.Attributes["llm.messages.omitted"])
}

func TestRecordInputMessages_NewestKeptEvenWhenOverBudget(t *testing.T) {
	withMessageCapture(t, 0)
	huge := strings.Repeat("y", captureInputBudget+1)
	span := &observability.Span{Attributes: map[string]interface{}{}}
	recordInputMessages(span, []llmtypes.Message{{Role: "user", Content: "old"}, {Role: "user", Content: huge}})
	assert.Equal(t, omittedMessagesRole, span.Attributes["llm.input_messages.0.message.role"])
	assert.Equal(t, huge, span.Attributes["llm.input_messages.1.message.content"])
	assert.Equal(t, 1, span.Attributes["llm.messages.omitted"])
}

func TestRecordToolSchemas_Budget(t *testing.T) {
	big := strings.Repeat("d", 40<<10)
	tools := []shuttle.Tool{
		captureTool{name: "a", description: big},
		captureTool{name: "b", description: big}, // would exceed the budget
		captureTool{name: "c", description: "small"},
	}
	span := &observability.Span{Attributes: map[string]interface{}{}}
	recordToolSchemas(span, tools)
	assert.Contains(t, span.Attributes["llm.tools.0.tool.json_schema"], `"name":"a"`)
	assert.Contains(t, span.Attributes["llm.tools.1.tool.json_schema"], `"name":"c"`)
	assert.NotContains(t, span.Attributes, "llm.tools.2.tool.json_schema")
	assert.Equal(t, 1, span.Attributes["llm.tools.omitted"])
}
