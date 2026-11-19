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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	llmtypes "github.com/teradata-labs/loom/pkg/llm/types"
)

func TestEmptyReplyText(t *testing.T) {
	cases := []struct {
		name       string
		stopReason string
		tools      int
		turns      int
		wantPrefix string
		notContain string
	}{
		{
			name:       "refusal is named as a refusal",
			stopReason: "refusal",
			wantPrefix: "The model declined to answer this request.",
			notContain: "Completed",
		},
		{
			name:       "empty end_turn reports the stop reason and the work done",
			stopReason: "end_turn",
			tools:      2,
			turns:      3,
			wantPrefix: "The model returned no text (stop reason: end_turn) after 2 tool executions across 3 turns.",
			notContain: "Completed",
		},
		{
			name:       "missing stop reason is reported as unknown",
			stopReason: "  ",
			wantPrefix: "The model returned no text (stop reason: unknown)",
			notContain: "Completed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := emptyReplyText(c.stopReason, c.tools, c.turns)
			assert.True(t, strings.HasPrefix(got, c.wantPrefix), "got %q", got)
			assert.NotContains(t, got, c.notContain)
		})
	}
}

// A refusal is a deliberate answer. It must not be retried, and the reply
// must say so rather than look like a completed task.
func TestEmptyReply_RefusalIsNotRetriedAndIsSaidAloud(t *testing.T) {
	llm := &usageScriptLLM{responses: []*LLMResponse{{
		Content:    "",
		StopReason: "refusal",
		Usage:      llmtypes.Usage{InputTokens: 50, OutputTokens: 0, TotalTokens: 50, CostUSD: 0.001},
	}}}
	ag := newUsageTestAgent(llm)

	resp, err := ag.Chat(context.Background(), "refusal-session", "Delete everything in /etc.")
	require.NoError(t, err)

	assert.Equal(t, 1, resp.TurnLLMCalls, "a refusal must not trigger the one-shot retry")
	assert.Contains(t, resp.Content, "declined to answer")
	assert.NotContains(t, resp.Content, "Completed")
	assert.Equal(t, true, resp.Metadata["empty_reply"])
	assert.Equal(t, "refusal", resp.Metadata["stop_reason"])
}

// An empty end_turn gets the one retry. If it is still empty, the reply
// names the stop reason instead of claiming tool executions completed.
func TestEmptyReply_EmptyEndTurnRetriesOnceThenReportsReason(t *testing.T) {
	llm := &usageScriptLLM{responses: []*LLMResponse{
		{Content: "", StopReason: "end_turn", Usage: llmtypes.Usage{InputTokens: 10, TotalTokens: 10, CostUSD: 0.001}},
		{Content: "", StopReason: "end_turn", Usage: llmtypes.Usage{InputTokens: 12, TotalTokens: 12, CostUSD: 0.001}},
	}}
	ag := newUsageTestAgent(llm)

	resp, err := ag.Chat(context.Background(), "empty-twice-session", "hello")
	require.NoError(t, err)

	assert.Equal(t, 2, resp.TurnLLMCalls, "one retry, then the fallback")
	assert.Contains(t, resp.Content, "returned no text (stop reason: end_turn)")
	assert.NotContains(t, resp.Content, "Completed")
	assert.Equal(t, true, resp.Metadata["empty_reply"])
	assert.Equal(t, true, resp.Metadata["empty_retried"])
}

// A reply with text is untouched by the empty-reply path.
func TestEmptyReply_NonEmptyReplyIsNotFlagged(t *testing.T) {
	llm := &usageScriptLLM{responses: []*LLMResponse{{
		Content:    "all good",
		StopReason: "end_turn",
		Usage:      llmtypes.Usage{InputTokens: 10, TotalTokens: 10, CostUSD: 0.001},
	}}}
	ag := newUsageTestAgent(llm)

	resp, err := ag.Chat(context.Background(), "normal-session", "hello")
	require.NoError(t, err)
	assert.Equal(t, "all good", resp.Content)
	assert.Equal(t, false, resp.Metadata["empty_reply"])
}
