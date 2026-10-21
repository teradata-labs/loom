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
	"fmt"
	"strconv"

	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Limits on what one LLM span records while observability.CaptureLLMMessages
// is on. Each message content and tool-call argument is first bounded by
// observability.TruncateContent; these bound the span as a whole, so a long
// conversation cannot produce a span the exporter or the backend rejects.
const (
	// captureInputBudget bounds the bytes of input message content and tool
	// call arguments one span records.
	captureInputBudget = 256 << 10
	// captureInputMaxAttributes bounds the attributes the input messages use
	// (OTelTracer keeps up to 4096 attributes per span).
	captureInputMaxAttributes = 2000
	// captureToolsBudget bounds the bytes of tool schemas one span records.
	captureToolsBudget = 64 << 10
	// omittedMessagesRole is the role of the placeholder that stands where
	// messages were left out, so the omission shows in the message list.
	omittedMessagesRole = "loom"
)

// messageAttr is one OpenInference message attribute, keyed relative to the
// message (for example "message.role").
type messageAttr struct {
	key   string
	value interface{}
}

// recordLLMRequest records the request's messages, the tool schemas offered
// and the provider's invocation parameters as OpenInference attributes
// (llm.input_messages.*, llm.tools.*, llm.invocation_parameters) when
// message capture is on.
func recordLLMRequest(ctx context.Context, span *observability.Span, provider llmtypes.LLMProvider, messages []llmtypes.Message, tools []shuttle.Tool) {
	if span == nil || !observability.CaptureLLMMessages() {
		return
	}
	recordInputMessages(span, messages)
	recordToolSchemas(span, tools)
	recordInvocationParameters(ctx, span, provider)
}

// recordLLMResponse records the response as llm.output_messages.0 when
// message capture is on.
func recordLLMResponse(span *observability.Span, resp *llmtypes.LLMResponse) {
	if span == nil || resp == nil || !observability.CaptureLLMMessages() {
		return
	}
	attrs, _ := captureMessage(llmtypes.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
	setMessageAttrs(span, "llm.output_messages.0.", attrs)
}

// recordInputMessages records messages as llm.input_messages.<i>.*. Within
// the span budget it keeps the leading system messages (the system prompt
// and session context) and the newest messages; older messages in between
// are replaced by one placeholder message, and llm.messages.omitted counts
// them.
func recordInputMessages(span *observability.Span, messages []llmtypes.Message) {
	type captured struct {
		attrs []messageAttr
		size  int
	}
	all := make([]captured, len(messages))
	for i, m := range messages {
		attrs, size := captureMessage(m)
		all[i] = captured{attrs, size}
	}

	lead := 0
	for lead < len(messages) && messages[lead].Role == "system" {
		lead++
	}
	used, usedAttrs := 0, 0
	for _, c := range all[:lead] {
		used += c.size
		usedAttrs += len(c.attrs)
	}
	// first is the oldest message kept after the lead; the newest message is
	// always kept.
	first := len(messages)
	for first > lead {
		c := all[first-1]
		if first < len(messages) && (used+c.size > captureInputBudget || usedAttrs+len(c.attrs) > captureInputMaxAttributes) {
			break
		}
		used += c.size
		usedAttrs += len(c.attrs)
		first--
	}

	idx := 0
	next := func(attrs []messageAttr) {
		setMessageAttrs(span, "llm.input_messages."+strconv.Itoa(idx)+".", attrs)
		idx++
	}
	for _, c := range all[:lead] {
		next(c.attrs)
	}
	if omitted := first - lead; omitted > 0 {
		next([]messageAttr{
			{"message.role", omittedMessagesRole},
			{"message.content", fmt.Sprintf("[%d earlier messages not exported: over the span's %d KiB message budget]", omitted, captureInputBudget>>10)},
		})
		span.SetAttribute("llm.messages.omitted", omitted)
	}
	for _, c := range all[first:] {
		next(c.attrs)
	}
}

// captureMessage returns m's OpenInference message attributes and the bytes
// of content they record: role, content (bounded; image blocks are named,
// not exported), the tool-result ID and the tool calls with their arguments.
func captureMessage(m llmtypes.Message) ([]messageAttr, int) {
	content := observability.TruncateContent(messageText(m))
	if n := imageBlockCount(m); n > 0 {
		if content != "" {
			content += "\n"
		}
		content += fmt.Sprintf("[%d image block(s) not exported]", n)
	}
	attrs := []messageAttr{{"message.role", m.Role}}
	size := len(content)
	if content != "" {
		attrs = append(attrs, messageAttr{"message.content", content})
	}
	if m.ToolUseID != "" {
		attrs = append(attrs, messageAttr{"message.tool_call_id", m.ToolUseID})
	}
	for j, tc := range m.ToolCalls {
		prefix := "message.tool_calls." + strconv.Itoa(j) + ".tool_call."
		args := "{}"
		if len(tc.Input) > 0 {
			if b, err := json.Marshal(tc.Input); err == nil {
				args = string(b)
			} else {
				args = fmt.Sprintf("%v", tc.Input)
			}
		}
		args = observability.TruncateContent(args)
		if tc.ID != "" {
			attrs = append(attrs, messageAttr{prefix + "id", tc.ID})
		}
		attrs = append(attrs,
			messageAttr{prefix + "function.name", tc.Name},
			messageAttr{prefix + "function.arguments", args})
		size += len(tc.Name) + len(args)
	}
	return attrs, size
}

func imageBlockCount(m llmtypes.Message) int {
	n := 0
	for _, b := range m.ContentBlocks {
		if b.Type == "image" {
			n++
		}
	}
	return n
}

func setMessageAttrs(span *observability.Span, prefix string, attrs []messageAttr) {
	for _, a := range attrs {
		span.SetAttribute(prefix+a.key, a.value)
	}
}

// openInferenceTool is the OpenAI function-tool form Phoenix renders for
// llm.tools.<i>.tool.json_schema.
type openInferenceTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string              `json:"name"`
		Description string              `json:"description,omitempty"`
		Parameters  *shuttle.JSONSchema `json:"parameters,omitempty"`
	} `json:"function"`
}

// recordToolSchemas records each offered tool as llm.tools.<i>.tool.json_schema
// within the span's tool budget; llm.tools.omitted counts tools left out.
func recordToolSchemas(span *observability.Span, tools []shuttle.Tool) {
	used, idx, omitted := 0, 0, 0
	for _, t := range tools {
		if t == nil {
			continue
		}
		var schema openInferenceTool
		schema.Type = "function"
		schema.Function.Name = t.Name()
		schema.Function.Description = t.Description()
		schema.Function.Parameters = t.InputSchema()
		b, err := json.Marshal(schema)
		if err != nil || used+len(b) > captureToolsBudget {
			omitted++
			continue
		}
		used += len(b)
		span.SetAttribute("llm.tools."+strconv.Itoa(idx)+".tool.json_schema", string(b))
		idx++
	}
	if omitted > 0 {
		span.SetAttribute("llm.tools.omitted", omitted)
	}
}

// recordInvocationParameters records llm.invocation_parameters as a JSON
// object: the model, plus what the provider reports for this request when it
// implements llmtypes.InvocationParameterReporter.
func recordInvocationParameters(ctx context.Context, span *observability.Span, provider llmtypes.LLMProvider) {
	params := map[string]interface{}{"model": provider.Model()}
	if r, ok := provider.(llmtypes.InvocationParameterReporter); ok {
		for k, v := range r.InvocationParameters(ctx) {
			params[k] = v
		}
	}
	if b, err := json.Marshal(params); err == nil {
		span.SetAttribute("llm.invocation_parameters", string(b))
	}
}
