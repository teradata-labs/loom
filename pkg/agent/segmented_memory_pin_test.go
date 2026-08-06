// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// ter320Question mirrors the live failure from issue #262: byte 50 lands one
// character into the table name, so the old 50-char summary stub reduced
// `demo_user.telecocustomer` to a table named "t".
const ter320Question = "/td-data-profile How many columns does demo_user.telecocustomer have and what are the data types?"

// bigToolExchange builds an assistant tool_use + tool_result pair whose result
// is large enough to drive L1 over its token budget within a few exchanges.
func bigToolExchange(id string, repeats int) []Message {
	return []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "teradata_tool_call"}}},
		{Role: "tool", ToolUseID: id, Content: strings.Repeat("row data ", repeats)},
	}
}

// l1ContainsUserMessage reports whether L1 still holds the exact user message.
func l1ContainsUserMessage(sm *SegmentedMemory, content string) bool {
	for _, m := range sm.l1Messages {
		if m.Role == "user" && m.Content == content {
			return true
		}
	}
	return false
}

// TestAddMessage_PinsActiveUserMessage is the issue #262 regression: a single
// user question followed by large tool results must survive mid-turn L1
// compression verbatim. No compressor is set, matching the production path
// that fell back to summarizeMessages and destroyed the question.
func TestAddMessage_PinsActiveUserMessage(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]
	sm := NewSegmentedMemoryWithCompression("rom", 10000, 1000, profile)
	ctx := context.Background()

	sm.AddMessage(ctx, Message{Role: "user", Content: ter320Question})
	totalAdded := 1
	for i := 0; i < 6; i++ {
		for _, m := range bigToolExchange(fmt.Sprintf("tool_%d", i), 400) {
			sm.AddMessage(ctx, m)
			totalAdded++
		}
	}

	require.NotEmpty(t, sm.l2Summary, "test setup must drive L1 compression")
	require.Less(t, len(sm.l1Messages), totalAdded, "test setup must evict messages from L1")

	assert.True(t, l1ContainsUserMessage(sm, ter320Question),
		"the active user question must never be evicted from L1")
	assert.NotContains(t, sm.l2Summary, "User asked about",
		"the only user message was pinned, so no user stub should reach L2")
}

// TestReplayMessages_PinsActiveUserMessage covers the session-restore path.
func TestReplayMessages_PinsActiveUserMessage(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]
	sm := NewSegmentedMemoryWithCompression("rom", 10000, 1000, profile)

	msgs := []Message{{Role: "user", Content: ter320Question}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, bigToolExchange(fmt.Sprintf("tool_%d", i), 400)...)
	}
	sm.ReplayMessages(context.Background(), msgs)

	require.Less(t, len(sm.l1Messages), len(msgs), "test setup must drive replay compression")
	assert.True(t, l1ContainsUserMessage(sm, ter320Question),
		"the active user question must survive replay compression")
}

// TestReplayMessages_TerminatesWhenPinnedMessageHeadsL1 guards the compression
// loop against spinning forever when the eviction window lands on the pinned
// user message: the window widens past it, evicts the next message instead,
// and the loop terminates at minL1Messages with the question intact.
func TestReplayMessages_TerminatesWhenPinnedMessageHeadsL1(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]
	sm := NewSegmentedMemoryWithCompression("rom", 10000, 1000, profile)

	// One huge user message pushes L1 over budget; the nominal batch is only
	// the pinned message (len - minL1Messages = 1).
	msgs := []Message{
		{Role: "user", Content: strings.Repeat("describe my data ", 1500)},
		{Role: "assistant", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "assistant", Content: "c"},
	}
	sm.ReplayMessages(context.Background(), msgs) // must not hang

	assert.Equal(t, msgs[0].Content, sm.l1Messages[0].Content,
		"the pinned user message must stay at the head of L1")
	assert.GreaterOrEqual(t, len(sm.l1Messages), sm.minL1Messages,
		"compression must respect the L1 floor")
}

func TestEvictL1Prefix(t *testing.T) {
	newSM := func(msgs ...Message) *SegmentedMemory {
		sm := NewSegmentedMemory("rom", 200000, 20000)
		sm.l1Messages = msgs
		return sm
	}
	user := func(c string) Message { return Message{Role: "user", Content: c} }
	asst := func(c string) Message { return Message{Role: "assistant", Content: c} }

	t.Run("pinned message inside window is spliced out and kept at L1 head", func(t *testing.T) {
		sm := newSM(user("Q"), asst("a1"), asst("a2"), asst("a3"))
		evicted := sm.evictL1Prefix(3)

		// Window widens by one to compensate for the pinned message.
		require.Len(t, evicted, 3)
		assert.Equal(t, "a1", evicted[0].Content)
		assert.Equal(t, "a3", evicted[2].Content)
		require.Len(t, sm.l1Messages, 1)
		assert.Equal(t, "Q", sm.l1Messages[0].Content)
	})

	t.Run("window covering only the pinned message widens past it", func(t *testing.T) {
		sm := newSM(user("Q"), asst("a1"), asst("a2"))
		evicted := sm.evictL1Prefix(1)

		require.Len(t, evicted, 1)
		assert.Equal(t, "a1", evicted[0].Content)
		require.Len(t, sm.l1Messages, 2)
		assert.Equal(t, "Q", sm.l1Messages[0].Content)
		assert.Equal(t, "a2", sm.l1Messages[1].Content)
	})

	t.Run("older user turns still compress when a newer one exists", func(t *testing.T) {
		sm := newSM(user("old question"), asst("a1"), user("new question"), asst("a2"))
		evicted := sm.evictL1Prefix(2)

		require.Len(t, evicted, 2)
		assert.Equal(t, "old question", evicted[0].Content)
		require.Len(t, sm.l1Messages, 2)
		assert.Equal(t, "new question", sm.l1Messages[0].Content)
	})

	t.Run("no user message falls back to plain prefix eviction", func(t *testing.T) {
		sm := newSM(asst("a1"), asst("a2"), asst("a3"))
		evicted := sm.evictL1Prefix(2)

		require.Len(t, evicted, 2)
		require.Len(t, sm.l1Messages, 1)
		assert.Equal(t, "a3", sm.l1Messages[0].Content)
	})

	t.Run("count larger than L1 is clamped", func(t *testing.T) {
		sm := newSM(asst("a1"), user("Q"))
		evicted := sm.evictL1Prefix(10)

		require.Len(t, evicted, 1)
		assert.Equal(t, "a1", evicted[0].Content)
		require.Len(t, sm.l1Messages, 1)
		assert.Equal(t, "Q", sm.l1Messages[0].Content)
	})
}

func TestSummarizeMessages_PreservesUserQuery(t *testing.T) {
	sm := NewSegmentedMemory("rom", 200000, 20000)

	summary := sm.summarizeMessages([]Message{
		{Role: "user", Content: ter320Question},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Name: "base_tableList"}}},
		{Role: "tool", ToolUseID: "t1", Content: "tables..."},
	})

	assert.Contains(t, summary, ter320Question,
		"a summarized user turn must carry the full question, not a 50-char stub")
}

func TestSummarizeMessages_CapsVeryLongUserQuery(t *testing.T) {
	sm := NewSegmentedMemory("rom", 200000, 20000)
	long := strings.Repeat("x", maxSummaryUserQueryChars+100)

	summary := sm.summarizeMessages([]Message{{Role: "user", Content: long}})

	assert.Contains(t, summary, long[:maxSummaryUserQueryChars]+"...")
	assert.NotContains(t, summary, long)
}

func TestSimpleCompressorFallbacks_PreserveUserQuery(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: ter320Question},
		{Role: "assistant", Content: "Let me check that table for you right away."},
	}

	llmFallback := (&LLMCompressor{}).simpleCompress(msgs)
	assert.Contains(t, llmFallback, ter320Question)

	simple, err := NewSimpleCompressor().CompressMessages(context.Background(), msgs)
	require.NoError(t, err)
	assert.Contains(t, simple, ter320Question)
}

func TestTruncateForSummary_UTF8Safe(t *testing.T) {
	assert.Equal(t, "short", truncateForSummary("short", 500), "short text passes through")

	multibyte := strings.Repeat("€", 200) // 600 bytes; byte 500 splits a rune
	got := truncateForSummary(multibyte, 500)
	assert.True(t, utf8.ValidString(got), "truncation must not split a rune")
	assert.True(t, strings.HasSuffix(got, "..."))
	assert.LessOrEqual(t, len(got), 500+len("..."))
}
