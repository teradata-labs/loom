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

func TestMeasureRequest_AttributesEachPart(t *testing.T) {
	whole := strings.Repeat("r", 3000)
	stub := fmt.Sprintf(pressureStubFormat, "query", 900, 7)
	tools := []shuttle.Tool{&ctxDumpStubTool{name: "run_sql", desc: "execute sql", schema: &shuttle.JSONSchema{Type: "object"}}}

	messages := []Message{
		{Role: "system", Content: strings.Repeat("s", 600)},
		{Role: "system", Content: strings.Repeat("m", 300)},
		{Role: "user", Content: strings.Repeat("u", 90)},
		{Role: "assistant", Content: "calling", ToolCalls: []ToolCall{{ID: "c1", Name: "run_sql", Input: map[string]interface{}{"q": "select 1"}}}},
		{Role: "tool", ToolUseID: "c1", Content: whole},
		{Role: "tool", ToolUseID: "c2", Content: stub},
		{Role: "tool", ToolUseID: "c3", Content: fmt.Sprintf(evictedStubFormat, "query", 50, "", "shape: {}")},
		{Role: roleSkillBody, Content: strings.Repeat("k", 150)},
		{Role: "user", Content: "<system-reminder>\n" + strings.Repeat("x", 60) + "\n</system-reminder>"},
	}

	b := measureRequest(messages, tools)

	assert.Equal(t, len(messages), b.Messages)
	assert.Equal(t, 1, b.Tools)
	assert.Equal(t, tokenFigure(600), b.SystemPromptTokens)
	assert.Equal(t, tokenFigure(300), b.SummaryTokens)
	assert.Equal(t, tokenFigure(len("calling")+90), b.HistoryTokens)
	assert.Equal(t, tokenFigure(3000), b.ToolResultTokens)
	assert.Equal(t, tokenFigure(3000), b.LargestToolResult)
	assert.Equal(t, 3, b.ToolResults)
	assert.Equal(t, 2, b.StubbedToolResults)
	assert.Equal(t, tokenFigure(len(stub))+tokenFigure(len(messages[6].Content)), b.ToolStubTokens)
	assert.Equal(t, tokenFigure(150), b.SkillBodyTokens)
	assert.Equal(t, tokenFigure(len(messages[8].Content)), b.ReminderTokens)
	assert.Positive(t, b.ToolCallTokens)
	assert.Positive(t, b.ToolSchemaTokens)
	assert.Equal(t, b.SystemPromptTokens+b.SummaryTokens+b.ToolSchemaTokens+b.HistoryTokens+b.ToolCallTokens+
		b.ToolResultTokens+b.ToolStubTokens+b.SkillBodyTokens+b.ReminderTokens+b.ThinkingTokens, b.total())
}

func TestIsOffloadStub_LongContentIsNeverAStub(t *testing.T) {
	assert.False(t, isOffloadStub("[x held in memory this turn "+strings.Repeat("a", stubScanBytes)))
	assert.False(t, isOffloadStub("plain tool output"))
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
