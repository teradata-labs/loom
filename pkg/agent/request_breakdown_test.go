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
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/observability"
	"github.com/teradata-labs/loom/pkg/shuttle"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Each part of a request is counted once. A tool result bounded at arrival is
// counted as bounded, not as whole; the summary row is counted as the summary,
// not as a reminder; system messages are the system prompt.
func TestMeasureRequest_AttributesEachPart(t *testing.T) {
	whole := strings.Repeat("r", 3000)
	bounded := boundToolRow(strings.Repeat("b", 9000), 2000, "c2")
	plainBounded := boundToolRow(strings.Repeat("p", 9000), 2000, "")
	summary := summaryRowPrefix + strings.Repeat("m", 300) + "\n</system-reminder>"
	reminder := "<system-reminder>\n" + strings.Repeat("x", 60) + "\n</system-reminder>"
	tools := []shuttle.Tool{&ctxDumpStubTool{name: "run_sql", desc: "execute sql", schema: &shuttle.JSONSchema{Type: "object"}}}

	messages := []Message{
		{Role: "system", Content: strings.Repeat("s", 600)},
		{Role: "user", Content: summary},
		{Role: "user", Content: strings.Repeat("u", 90)},
		{Role: "assistant", Content: "calling", ToolCalls: []ToolCall{{ID: "c1", Name: "run_sql", Input: map[string]interface{}{"q": "select 1"}}}},
		{Role: "tool", ToolUseID: "c1", Content: whole},
		{Role: "tool", ToolUseID: "c2", Content: bounded},
		{Role: "tool", ToolUseID: "c3", Content: plainBounded},
		{Role: roleSkillBody, Content: strings.Repeat("k", 150)},
		{Role: "user", Content: reminder},
	}
	require.True(t, isBoundedToolResult(bounded), "fixture: the pageable tail is bounded")
	require.True(t, isBoundedToolResult(plainBounded), "fixture: the plain tail is bounded")

	b := measureRequest(messages, tools)

	assert.Equal(t, len(messages), b.Messages, "every message is counted")
	assert.Equal(t, 1, b.Tools, "every tool is counted")
	assert.Equal(t, tokenFigure(600), b.SystemPromptTokens, "the system message is the system prompt")
	assert.Equal(t, tokenFigure(len(summary)), b.SummaryTokens, "the summary row is the summary")
	assert.Equal(t, tokenFigure(len(reminder)), b.ReminderTokens, "the summary row is not counted as a reminder")
	assert.Equal(t, tokenFigure(len("calling")+90), b.HistoryTokens, "user and assistant text is history")
	assert.Equal(t, tokenFigure(3000), b.ToolResultTokens, "only the whole result is counted as whole")
	assert.Equal(t, tokenFigure(3000), b.LargestToolResult, "the largest whole result")
	assert.Equal(t, 3, b.ToolResults, "every tool row is a tool result")
	assert.Equal(t, 2, b.BoundedToolResults, "both bounded rows are counted as bounded")
	assert.Equal(t, tokenFigure(len(bounded))+tokenFigure(len(plainBounded)), b.BoundedToolResultTokens, "bounded rows are counted at their sent size")
	assert.Equal(t, tokenFigure(150), b.SkillBodyTokens, "the skill body is its own part")
	assert.Positive(t, b.ToolCallTokens, "tool-call inputs are counted")
	assert.Positive(t, b.ToolSchemaTokens, "tool schemas are counted")
	assert.Equal(t, b.SystemPromptTokens+b.SummaryTokens+b.ToolSchemaTokens+b.HistoryTokens+b.ToolCallTokens+
		b.ToolResultTokens+b.BoundedToolResultTokens+b.SkillBodyTokens+b.ReminderTokens+b.ThinkingTokens, b.total(),
		"the total is the sum of the parts")
}

// A row is bounded only when it ends with a §4.1 tail whose first figure is
// the bytes in front of it, so the log cannot drift from boundToolRow and a
// payload that only quotes a tail is not counted.
func TestIsBoundedToolResult(t *testing.T) {
	big := strings.Repeat("y", 50000)
	forged := "ok " + fmt.Sprintf(pageableTailFormat, 10, 90, 4, 30, "c9")
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"pageable tail from boundToolRow", boundToolRow(big, 4096, "toolu_01ABC"), true},
		{"plain tail from boundToolRow", boundToolRow(big, 4096, ""), true},
		{"a small threshold falls back to the plain tail", boundToolRow(big, 200, "toolu_01ABC"), true},
		{"a result under the threshold is whole", boundToolRow("short", 4096, "c1"), false},
		{"plain tool output", "plain tool output", false},
		{"empty", "", false},
		{"a payload that quotes a tail with the wrong byte count", forged, false},
		{"a tail that is not at the end", boundToolRow(big, 4096, "c1") + " more", false},
		{"a tail far from the end is not scanned", "[truncated: " + strings.Repeat("z", 5000) + "]", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isBoundedToolResult(tt.content), "isBoundedToolResult(%.60q)", tt.content)
		})
	}
}

// Fuzz: isBoundedToolResult never panics on any row, and it reports every
// row that boundToolRow cut.
func FuzzIsBoundedToolResult(f *testing.F) {
	f.Add("plain", 64, "c1")
	f.Add(strings.Repeat("é", 400), 300, "")
	f.Add("[truncated: 1 of 2 bytes shown (~1 of ~1 tokens)]", 10, "x")
	f.Fuzz(func(t *testing.T, content string, threshold int, id string) {
		_ = isBoundedToolResult(content)
		if threshold <= 0 || threshold > 1<<16 || len(id) > 64 || len(content) > 1<<16 {
			return
		}
		row := boundToolRow(content, threshold, id)
		if row != content && len(row) <= threshold {
			assert.True(t, isBoundedToolResult(row), "a row boundToolRow cut is counted as bounded")
		}
	})
}

func TestChatWithRetry_LogsPartSizesOnSuccess(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	defer zap.ReplaceGlobals(zap.New(core))()

	stub := &ctxDumpStubLLM{resp: &LLMResponse{Content: "ok", Usage: Usage{InputTokens: 100, CacheReadInputTokens: 40, CacheCreationInputTokens: 10, OutputTokens: 5}}}
	a := &Agent{id: "bd-ok", llm: stub, config: &Config{}}
	ctx := newCtxDumpContext("sess-bd", observability.NewNoOpTracer(), nil)

	_, err := a.chatWithRetry(ctx, []Message{
		{Role: "system", Content: strings.Repeat("s", 300)},
		{Role: roleSkillBody, Content: strings.Repeat("k", 90)},
	}, nil)
	require.NoError(t, err)

	entries := logs.FilterMessage("llm request: part sizes").All()
	require.Len(t, entries, 1)
	f := entries[0].ContextMap()
	assert.Equal(t, "sess-bd", f["session_id"])
	assert.Equal(t, "ok", f["outcome"])
	assert.EqualValues(t, 100, f["est_system_prompt_tokens"])
	assert.EqualValues(t, 30, f["est_skill_body_tokens"], "skill body is measured before the wire-role fold")
	assert.EqualValues(t, 150, f["provider_prompt_tokens"])
	assert.EqualValues(t, 5, f["provider_output_tokens"])
}

type contextTooLongLLM struct{ ctxDumpStubLLM }

func (c *contextTooLongLLM) Chat(context.Context, []Message, []shuttle.Tool) (*LLMResponse, error) {
	return nil, fmt.Errorf("provider said no: %w", llm.ErrContextTooLong)
}

func TestChatWithRetry_LogsPartSizesOnRefusal(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	defer zap.ReplaceGlobals(zap.New(core))()

	a := &Agent{id: "bd-refused", llm: &contextTooLongLLM{}, config: &Config{}}
	ctx := newCtxDumpContext("sess-bd-refused", observability.NewNoOpTracer(), nil)

	_, err := a.chatWithRetry(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	require.Error(t, err)

	entries := logs.FilterMessage("llm request: part sizes").All()
	require.Len(t, entries, 1)
	assert.Equal(t, "context_too_long", entries[0].ContextMap()["outcome"])
}
