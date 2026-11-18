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
	"encoding/json"
	"errors"
	"strings"

	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/session"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
)

// requestBreakdown is the per-part size of one provider request, measured on
// the exact (messages, tools) handed to the provider. Token figures use the
// byte→token figure of the write rules (tokenFigure), which needs no
// tokenizer pass on the hot path; the provider-reported prompt size logged
// next to it is the ground truth the figures are read against.
type requestBreakdown struct {
	Messages int
	Tools    int

	SystemPromptTokens int // first system message (ROM)
	SummaryTokens      int // the running summary's row
	ToolSchemaTokens   int // advertised tool schemas
	HistoryTokens      int // user and assistant text
	ToolCallTokens     int // assistant tool-call inputs
	ToolResultTokens   int // tool results rendered whole
	SkillBodyTokens    int // skill-body rows
	ReminderTokens     int // transient system-reminder tail
	ThinkingTokens     int // replayed thinking blocks

	ToolResults       int // tool-result rows
	LargestToolResult int // tokens of the biggest tool result
}

// total is the sum of every part.
func (b requestBreakdown) total() int {
	return b.SystemPromptTokens + b.SummaryTokens + b.ToolSchemaTokens + b.HistoryTokens +
		b.ToolCallTokens + b.ToolResultTokens + b.SkillBodyTokens +
		b.ReminderTokens + b.ThinkingTokens
}

// messageTextBytes is the text a message sends: Content, or the text blocks
// when blocks are present (blocks take precedence).
func messageTextBytes(m *Message) int {
	if len(m.ContentBlocks) == 0 {
		return len(m.Content)
	}
	n := 0
	for _, b := range m.ContentBlocks {
		n += len(b.Text)
	}
	return n
}

// measureRequest sizes one provider request by part. It must see messages
// BEFORE normalizeWireRoles, which folds the skill-body role into "user".
func measureRequest(messages []Message, tools []shuttle.Tool) requestBreakdown {
	var b requestBreakdown
	b.Messages = len(messages)
	b.Tools = len(tools)
	b.ToolSchemaTokens = tokenFigure(advertisedToolsBytes(tools))

	seenSystem := false
	for i := range messages {
		m := &messages[i]
		n := tokenFigure(messageTextBytes(m))

		switch {
		case m.Role == "system":
			if !seenSystem {
				b.SystemPromptTokens += n
				seenSystem = true
			} else {
				b.SummaryTokens += n
			}
		case m.Role == "user" && strings.HasPrefix(m.Content, summaryRowPrefix):
			// The summary is sent as a user-role row at the head of the
			// conversation, not as a system message.
			b.SummaryTokens += n
		case m.Role == roleSkillBody:
			b.SkillBodyTokens += n
		case m.Role == "tool":
			b.ToolResults++
			b.ToolResultTokens += n
			b.LargestToolResult = max(b.LargestToolResult, n)
		case IsSystemReminder(*m):
			b.ReminderTokens += n
		default:
			b.HistoryTokens += n
		}

		if len(m.ToolCalls) > 0 {
			if raw, err := json.Marshal(m.ToolCalls); err == nil {
				b.ToolCallTokens += tokenFigure(len(raw))
			}
		}
		for _, tb := range m.ThinkingBlocks {
			b.ThinkingTokens += tokenFigure(len(tb.Thinking) + len(tb.Signature))
		}
	}
	return b
}

// logRequest writes the one per-call line that says what was sent to the
// provider and what came back, so an oversized or refused request can be
// attributed to a part. It logs sizes only, never content.
func (a *Agent) logRequest(ctx Context, b requestBreakdown, resp *LLMResponse, err error) {
	fields := []zap.Field{
		zap.String("session_id", session.SessionIDFromContext(ctx)),
		zap.String("model", a.llmModel()),
		zap.Int("messages", b.Messages),
		zap.Int("tools", b.Tools),
		zap.Int("est_total_tokens", b.total()),
		zap.Int("est_system_prompt_tokens", b.SystemPromptTokens),
		zap.Int("est_summary_tokens", b.SummaryTokens),
		zap.Int("est_tool_schema_tokens", b.ToolSchemaTokens),
		zap.Int("est_history_tokens", b.HistoryTokens),
		zap.Int("est_tool_call_tokens", b.ToolCallTokens),
		zap.Int("est_tool_result_tokens", b.ToolResultTokens),
		zap.Int("est_skill_body_tokens", b.SkillBodyTokens),
		zap.Int("est_reminder_tokens", b.ReminderTokens),
		zap.Int("est_thinking_tokens", b.ThinkingTokens),
		zap.Int("tool_results", b.ToolResults),
		zap.Int("largest_tool_result_tokens", b.LargestToolResult),
	}
	switch {
	case err == nil && resp != nil:
		u := resp.Usage
		fields = append(fields,
			zap.String("outcome", "ok"),
			zap.Int("provider_prompt_tokens", u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens),
			zap.Int("provider_cache_read_tokens", u.CacheReadInputTokens),
			zap.Int("provider_cache_write_tokens", u.CacheCreationInputTokens),
			zap.Int("provider_output_tokens", u.OutputTokens))
	case errors.Is(err, llm.ErrContextTooLong):
		fields = append(fields, zap.String("outcome", "context_too_long"), zap.Error(err))
	default:
		fields = append(fields, zap.String("outcome", "error"), zap.Error(err))
	}
	zap.L().Info("llm request: part sizes", fields...)
}

func (a *Agent) llmModel() string {
	if a.llm == nil {
		return ""
	}
	return a.llm.Model()
}
