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
	"fmt"
	"strings"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// includeEncryptedReasoning asks the API to return reasoning items with
// encrypted_content, which store=false requires for replay.
const includeEncryptedReasoning = "reasoning.encrypted_content"

// Reasoning items ride on types.ThinkingBlock (Message.ThinkingBlocks and
// LLMResponse.ThinkingBlocks), the same field the anthropic and openai
// clients use for their opaque thinking. One reasoning item becomes:
//
//   - a start block: Type blockTypeReasoning, Format = the item id (rs_…),
//     Signature = encrypted_content, Thinking = the first summary part's text
//     ("" when the summary is empty);
//   - one continuation block per further summary part: Type
//     blockTypeReasoningSummary, Thinking = that part's text.
//
// The split keeps replay lossless (each summary part goes back as it came)
// without depending on the item id being present. The type names do not
// start with "reasoning." and are not "thinking"/"redacted_thinking", so the
// other clients' own block handling does not mistake them for theirs.
const (
	blockTypeReasoning        = "responses.reasoning"
	blockTypeReasoningSummary = "responses.reasoning.summary"
)

// buildRequest converts loom messages and tools into a request. nameMap
// receives sanitized → original tool names for the response mapping.
func (c *Client) buildRequest(ctx context.Context, messages []llmtypes.Message, tools []shuttle.Tool,
	nameMap map[string]string, stream bool) (*responsesRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	instructions, input := convertInput(messages)
	apiTools, err := c.convertTools(ctx, tools, nameMap)
	if err != nil {
		return nil, err
	}
	req := &responsesRequest{
		Model:           c.model,
		Instructions:    instructions,
		Input:           input,
		MaxOutputTokens: c.maxTokens,
		Store:           false,
		Stream:          stream,
	}
	if r := c.reasoningParam(ctx); r != nil {
		req.Reasoning = r
		req.Include = []string{includeEncryptedReasoning}
	} else {
		t := c.temperature
		req.Temperature = &t
	}
	if len(apiTools) > 0 {
		req.Tools = apiTools
		req.ToolChoice = "auto"
		parallel := true
		req.ParallelToolCalls = &parallel
	}
	return req, nil
}

// reasoningEffort maps a loom thinking level to a Responses effort, or ""
// when reasoning is off. Unknown levels are treated as "medium" so a
// misspelt level still asks for reasoning rather than silently disabling it.
func reasoningEffort(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "none", "off":
		return ""
	case "low":
		return "low"
	case "high", "xhigh", "max":
		// The Responses API tops out at "high" for most models; xhigh and max
		// map there, as they do on the Chat surface (openai.reasoning_object).
		return "high"
	default: // "medium", "auto"
		return "medium"
	}
}

// reasoningParam returns the request's reasoning object, or nil when
// thinking is off. A Claude model that takes only the enabled thinking form
// gets none while llm.ThinkingSuppressed is set (a rebuilt tool turn that has
// no thinking block to satisfy Anthropic's requirement), as in pkg/llm/openai.
func (c *Client) reasoningParam(ctx context.Context) *reasoningParam {
	effort := reasoningEffort(c.thinkingLevel)
	if effort == "" {
		return nil
	}
	if _, _, claude := catalog.ClaudeFamilyPricing(c.model); claude &&
		!llm.IsAdaptiveThinkingModel(c.model) && llm.ThinkingSuppressed(ctx) {
		return nil
	}
	return &reasoningParam{Effort: effort, Summary: "auto"}
}

// convertInput splits the leading system messages into instructions and maps
// the rest to input items. See the package doc for the mapping.
func convertInput(messages []llmtypes.Message) (string, []inputItem) {
	validCallIDs := make(map[string]struct{})
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && llm.SanitizeToolName(tc.Name) != "" {
				validCallIDs[tc.ID] = struct{}{}
			}
		}
	}

	var systemTexts []string
	input := make([]inputItem, 0, len(messages))
	leading := true
	for _, m := range messages {
		if m.Role == "system" && leading {
			if m.Content != "" {
				systemTexts = append(systemTexts, m.Content)
			}
			continue
		}
		leading = false
		switch m.Role {
		case "system":
			if m.Content != "" {
				input = append(input, inputItem{Role: "system", Content: []map[string]interface{}{inputText(m.Content)}})
			}
		case "user":
			input = append(input, inputItem{Role: "user", Content: userContent(m)})
		case "assistant":
			input = append(input, assistantItems(m)...)
		case "tool":
			if _, ok := validCallIDs[m.ToolUseID]; !ok {
				continue
			}
			out := m.Content
			input = append(input, inputItem{Type: "function_call_output", CallID: m.ToolUseID, Output: &out})
		}
	}
	return strings.Join(systemTexts, "\n\n"), input
}

func inputText(text string) map[string]interface{} {
	return map[string]interface{}{"type": "input_text", "text": text}
}

// userContent maps a user message to input parts: ContentBlocks when present
// (text and images, as pkg/llm/openai does), else the plain Content.
func userContent(m llmtypes.Message) []map[string]interface{} {
	if len(m.ContentBlocks) == 0 {
		return []map[string]interface{}{inputText(m.Content)}
	}
	parts := make([]map[string]interface{}, 0, len(m.ContentBlocks))
	for _, b := range m.ContentBlocks {
		switch b.Type {
		case "text":
			parts = append(parts, inputText(b.Text))
		case "image":
			if b.Image == nil {
				continue
			}
			url := b.Image.Source.URL
			if b.Image.Source.Type == "base64" {
				url = fmt.Sprintf("data:%s;base64,%s", b.Image.Source.MediaType, b.Image.Source.Data)
			}
			parts = append(parts, map[string]interface{}{"type": "input_image", "image_url": url})
		}
	}
	if len(parts) == 0 {
		return []map[string]interface{}{inputText(m.Content)}
	}
	return parts
}

// assistantItems maps one assistant message to input items in output order:
// replayed reasoning items, then the text, then one function_call per tool
// call.
func assistantItems(m llmtypes.Message) []inputItem {
	items := reasoningItems(m.ThinkingBlocks)
	if m.Content != "" {
		items = append(items, inputItem{
			Role:    "assistant",
			Content: []map[string]interface{}{{"type": "output_text", "text": m.Content}},
		})
	}
	for _, tc := range m.ToolCalls {
		name := llm.SanitizeToolName(tc.Name)
		if tc.ID == "" || name == "" {
			continue
		}
		args, err := json.Marshal(tc.Input)
		if err != nil || string(args) == "null" {
			args = []byte("{}")
		}
		items = append(items, inputItem{Type: "function_call", CallID: tc.ID, Name: name, Arguments: string(args)})
	}
	return items
}

// reasoningItems rebuilds reasoning items from the blocks this client
// produced (see blockTypeReasoning). Blocks of other types are skipped: they
// belong to another provider and this API cannot take them. A continuation
// block with no start block before it is dropped.
func reasoningItems(blocks []llmtypes.ThinkingBlock) []inputItem {
	var items []inputItem
	for _, b := range blocks {
		switch b.Type {
		case blockTypeReasoning:
			summary := []summaryPart{}
			if b.Thinking != "" {
				summary = append(summary, summaryPart{Type: "summary_text", Text: b.Thinking})
			}
			items = append(items, inputItem{
				Type:             "reasoning",
				ID:               b.Format,
				EncryptedContent: b.Signature,
				Summary:          &summary,
			})
		case blockTypeReasoningSummary:
			if len(items) == 0 {
				continue
			}
			s := items[len(items)-1].Summary
			*s = append(*s, summaryPart{Type: "summary_text", Text: b.Thinking})
		}
	}
	return items
}

// reasoningBlocks is the inverse of reasoningItems for one output item.
func reasoningBlocks(item outputItem) []llmtypes.ThinkingBlock {
	start := llmtypes.ThinkingBlock{Type: blockTypeReasoning, Format: item.ID, Signature: item.EncryptedContent}
	if len(item.Summary) > 0 {
		start.Thinking = item.Summary[0].Text
	}
	blocks := []llmtypes.ThinkingBlock{start}
	for _, p := range item.Summary[min(1, len(item.Summary)):] {
		blocks = append(blocks, llmtypes.ThinkingBlock{Type: blockTypeReasoningSummary, Thinking: p.Text})
	}
	return blocks
}

// convertTools maps shuttle tools to flat function tools. Names are
// sanitized (MCP colons) and recorded in nameMap for llm.ReverseToolName.
func (c *Client) convertTools(ctx context.Context, tools []shuttle.Tool, nameMap map[string]string) ([]functionTool, error) {
	var out []functionTool
	for _, tool := range tools {
		original := tool.Name()
		params, keep, err := c.schemaPolicy.Prepare(ctx, original, tool.InputSchema(), c.strictToolSchemas, "enum", "not")
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		name := llm.SanitizeToolName(original)
		nameMap[name] = original
		out = append(out, functionTool{
			Type:        "function",
			Name:        name,
			Description: tool.Description(),
			Parameters:  params,
			Strict:      false,
		})
	}
	return out, nil
}
