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
	"fmt"
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

	SystemPromptTokens      int // system messages (ROM)
	SummaryTokens           int // the running summary (a user-role system-reminder)
	ToolSchemaTokens        int // advertised tool schemas
	HistoryTokens           int // user and assistant text
	ToolCallTokens          int // assistant tool-call inputs
	ToolResultTokens        int // tool results sent whole
	BoundedToolResultTokens int // tool results bounded at arrival (write rule §4.1)
	SkillBodyTokens         int // skill-body rows
	ReminderTokens          int // other system-reminder rows (recall note, turn notes)
	ThinkingTokens          int // replayed thinking blocks
	ToolResults             int // tool-result rows, bounded ones included
	BoundedToolResults      int // tool-result rows bounded at arrival
	LargestToolResult       int // tokens of the biggest whole tool result
}

// total is the sum of every part.
func (b requestBreakdown) total() int {
	return b.SystemPromptTokens + b.SummaryTokens + b.ToolSchemaTokens + b.HistoryTokens +
		b.ToolCallTokens + b.ToolResultTokens + b.BoundedToolResultTokens + b.SkillBodyTokens +
		b.ReminderTokens + b.ThinkingTokens
}

// boundedTailScanBytes bounds how far from the end of a tool row the §4.1
// tail may start. The longest tail is pageableTailFormat with four numbers and
// a tool_use_id, well under this, so a payload is never scanned whole.
const boundedTailScanBytes = 1024

// boundedTailHead is the part both §4.1 tails share, from "[truncated: " to
// the token figures: the common prefix of truncationTailFormat and
// pageableTailFormat. isBoundedToolResult parses it, so the log cannot drift
// from the formats.
var boundedTailHead = commonPrefix(truncationTailFormat, pageableTailFormat)

// commonPrefix is the longest prefix a and b share.
func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

// isBoundedToolResult reports whether a tool row's content was bounded at
// arrival (boundToolRow): it ends with a §4.1 tail (pageableTailFormat, or
// truncationTailFormat when the threshold is too small for the pageable one)
// whose first figure is the number of bytes in front of it. That check rules
// out a payload that only quotes a tail. Only the end of the row is examined.
func isBoundedToolResult(content string) bool {
	if !strings.HasSuffix(content, "]") {
		return false
	}
	open := boundedTailHead[:strings.Index(boundedTailHead, "%d")]
	from := max(len(content)-boundedTailScanBytes, 0)
	i := strings.LastIndex(content[from:], open)
	if i < 0 {
		return false
	}
	i += from
	var shown, total, shownTokens, totalTokens int
	if n, err := fmt.Sscanf(content[i:], boundedTailHead, &shown, &total, &shownTokens, &totalTokens); err != nil || n != 4 {
		return false
	}
	return shown == i && shown < total
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
// summaryOpen is the opening of the summary row the session's memory sends
// (SegmentedMemory.SummaryRowPrefix); a user row that starts with it is
// counted as the summary.
func measureRequest(messages []Message, tools []shuttle.Tool, summaryOpen string) requestBreakdown {
	var b requestBreakdown
	b.Messages = len(messages)
	b.Tools = len(tools)
	b.ToolSchemaTokens = tokenFigure(advertisedToolsBytes(tools))

	for i := range messages {
		m := &messages[i]
		n := tokenFigure(messageTextBytes(m))

		switch {
		case m.Role == "system":
			b.SystemPromptTokens += n
		case m.Role == roleSkillBody:
			b.SkillBodyTokens += n
		case m.Role == "tool":
			b.ToolResults++
			if isBoundedToolResult(m.Content) {
				b.BoundedToolResults++
				b.BoundedToolResultTokens += n
			} else {
				b.ToolResultTokens += n
				b.LargestToolResult = max(b.LargestToolResult, n)
			}
		case m.Role == "user" && strings.HasPrefix(m.Content, summaryOpen):
			// The summary row is a system-reminder too; it is counted as the
			// summary, not as a reminder.
			b.SummaryTokens += n
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
		zap.String("model", a.GetLLMModel()),
		zap.Int("messages", b.Messages),
		zap.Int("tools", b.Tools),
		zap.Int("est_total_tokens", b.total()),
		zap.Int("est_system_prompt_tokens", b.SystemPromptTokens),
		zap.Int("est_summary_tokens", b.SummaryTokens),
		zap.Int("est_tool_schema_tokens", b.ToolSchemaTokens),
		zap.Int("est_history_tokens", b.HistoryTokens),
		zap.Int("est_tool_call_tokens", b.ToolCallTokens),
		zap.Int("est_tool_result_tokens", b.ToolResultTokens),
		zap.Int("est_bounded_tool_result_tokens", b.BoundedToolResultTokens),
		zap.Int("est_skill_body_tokens", b.SkillBodyTokens),
		zap.Int("est_reminder_tokens", b.ReminderTokens),
		zap.Int("est_thinking_tokens", b.ThinkingTokens),
		zap.Int("tool_results", b.ToolResults),
		zap.Int("bounded_tool_results", b.BoundedToolResults),
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
