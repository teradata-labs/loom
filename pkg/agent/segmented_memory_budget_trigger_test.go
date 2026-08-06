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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// Story D-C — budget-driven compaction trigger. Compaction fires strictly on
// total budget usage crossing the profile warning threshold; the former
// maxL1Tokens L1-size cap no longer gates it. These tests drive the real
// SegmentedMemory interface (AddMessage / ReplayMessages) and assert the trigger
// and compaction shape.

// l1HasToolID reports whether any message left in L1 carries the given tool id,
// either as an assistant tool_use or as a tool_result.
func l1HasToolID(sm *SegmentedMemory, id string) bool {
	for _, m := range sm.GetMessages() {
		if m.Role == "tool" && m.ToolUseID == id {
			return true
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == id {
				return true
			}
		}
	}
	return false
}

// assertNoSplitToolPairs asserts that every tool_result remaining in L1 has its
// assistant tool_use also in L1 and vice versa — i.e. compaction never split a
// tool_use/tool_result pair across the L1/L2 boundary.
func assertNoSplitToolPairs(t *testing.T, msgs []Message) {
	t.Helper()
	toolUses := map[string]bool{}
	toolResults := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					toolUses[tc.ID] = true
				}
			}
		}
		if m.Role == "tool" && m.ToolUseID != "" {
			toolResults[m.ToolUseID] = true
		}
	}
	for id := range toolResults {
		assert.Truef(t, toolUses[id], "tool_result %s left in L1 without its tool_use — pair was split", id)
	}
	for id := range toolUses {
		assert.Truef(t, toolResults[id], "tool_use %s left in L1 without its tool_result — pair was split", id)
	}
}

// TestAddMessage_NoCompactionWhileUnderWarningEvenPastFormerL1Cap covers AC1:
// with the L1-token cap removed from the trigger, L1 growing well past the former
// maxL1Tokens ceiling does NOT compact as long as total budget usage stays at or
// below the profile warning threshold.
func TestAddMessage_NoCompactionWhileUnderWarningEvenPastFormerL1Cap(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	// A very large window keeps budget usage far below the 60% warning line even
	// after L1 grows past the 6400-token former cap.
	sm := NewSegmentedMemoryWithCompression("", 2_000_000, 200_000, profile)
	// A working compressor makes a stray compaction observable as L2 content.
	sm.SetCompressor(&mockCompressor{enabled: true})
	ctx := context.Background()

	content := strings.Repeat("budget trigger payload ", 40)
	added := 0
	for sm.cachedL1Tokens <= 2*sm.maxL1Tokens {
		sm.AddMessage(ctx, Message{Role: "user", Content: content})
		added++
		require.Less(t, added, 100000, "L1 never reached twice the former cap")
	}

	require.Greater(t, sm.cachedL1Tokens, sm.maxL1Tokens,
		"test drives L1 past the former maxL1Tokens cap")
	require.Greater(t, added, profile.MinL1Messages,
		"L1 holds more than the floor, so only the threshold could gate compaction")
	require.LessOrEqual(t, sm.tokenBudget.UsagePercentage(), float64(profile.WarningThresholdPercent),
		"precondition: budget usage stays at or below the warning threshold")

	assert.False(t, sm.HasL2Content(), "no compaction while budget usage is under the warning threshold")
	assert.Empty(t, sm.GetL2Summary(), "L2 stays empty while under the warning threshold")
	assert.Equal(t, added, sm.GetL1MessageCount(), "no messages evicted from L1")
}

// TestAddMessage_CompactionFiresOnceBudgetCrossesWarning covers AC2 on the live
// path: compaction fires once total budget usage crosses the warning zone — and
// not merely because L1 holds more than minL1Messages.
func TestAddMessage_CompactionFiresOnceBudgetCrossesWarning(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	sm := NewSegmentedMemoryWithCompression("", 8000, 800, profile)
	sm.SetCompressor(&mockCompressor{enabled: true})
	ctx := context.Background()
	warning := float64(profile.WarningThresholdPercent)

	// Phase 1 — below the warning zone: enough small messages to clear the
	// minL1Messages floor, yet total usage stays under warning, so no compaction.
	for i := 0; i < profile.MinL1Messages*2; i++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: "ping"})
	}
	require.Greater(t, sm.GetL1MessageCount(), profile.MinL1Messages,
		"floor already cleared, so only the budget threshold can gate compaction")
	require.LessOrEqual(t, sm.tokenBudget.UsagePercentage(), warning,
		"phase 1 stays at or below the warning threshold")
	require.False(t, sm.HasL2Content(), "no compaction below the warning threshold")

	// Phase 2 — accumulate until total usage crosses the warning zone. Because the
	// floor is already cleared, the only remaining gate is budget > warning, so the
	// appearance of L2 content proves usage crossed the warning threshold.
	big := strings.Repeat("payload token ", 40)
	for i := 0; i < 500 && !sm.HasL2Content(); i++ {
		sm.AddMessage(ctx, Message{Role: "user", Content: big})
	}
	assert.True(t, sm.HasL2Content(),
		"compaction fires once total budget usage crosses the warning threshold")
}

// TestAddMessage_CompactionShape_OldestBatchToCumulativeL2FloorKeptPairsIntact
// covers AC3 (must-not-regress): a compaction compresses the OLDEST batch into a
// cumulative L2 summary, keeps at least minL1Messages recent messages verbatim in
// L1, and never splits a tool_use/tool_result pair.
func TestAddMessage_CompactionShape_OldestBatchToCumulativeL2FloorKeptPairsIntact(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]
	sm := NewSegmentedMemoryWithCompression("", 10000, 1000, profile)
	ctx := context.Background()

	// Tag each compressed batch so cumulative L2 growth is observable across
	// rounds, and count batches so the test drives at least two of them.
	batchCount := 0
	sm.SetCompressor(&mockCompressor{
		enabled: true,
		compressFn: func(msgs []Message) string {
			batchCount++
			return fmt.Sprintf("[batch %d: %d msgs]", batchCount, len(msgs))
		},
	})

	// A pinned user question, then tool exchanges (assistant tool_use + tool
	// result) added until at least two compaction batches have run.
	sm.AddMessage(ctx, Message{Role: "user", Content: "profile the orders table"})
	exchange := 0
	for batchCount < 2 && exchange < 60 {
		for _, m := range bigToolExchange(fmt.Sprintf("tc_%d", exchange), 400) {
			sm.AddMessage(ctx, m)
		}
		exchange++
	}
	require.GreaterOrEqual(t, batchCount, 2, "test must drive at least two compaction batches")

	// Cumulative L2: the first batch's summary is still present after later
	// batches appended, rather than being overwritten.
	l2 := sm.GetL2Summary()
	assert.Contains(t, l2, "[batch 1:", "oldest batch summary retained in cumulative L2")
	assert.Contains(t, l2, "[batch 2:", "later batch summary appended to the same L2")

	// Oldest-first: the earliest tool exchange is compressed out of L1 while the
	// newest exchange is kept verbatim.
	assert.False(t, l1HasToolID(sm, "tc_0"), "oldest tool exchange compressed out of L1")
	assert.True(t, l1HasToolID(sm, fmt.Sprintf("tc_%d", exchange-1)),
		"newest tool exchange kept verbatim in L1")

	// Floor: at least minL1Messages recent messages remain in L1.
	assert.GreaterOrEqual(t, sm.GetL1MessageCount(), profile.MinL1Messages,
		"compaction respects the minL1Messages floor")

	// Pairs intact: no tool_use/tool_result pair was split across the boundary.
	assertNoSplitToolPairs(t, sm.GetMessages())
}

// TestReplayMessages_CompactionFiresOnceBudgetCrossesWarning covers AC2 on the
// session-restore path. The LLD requires the ReplayMessages trigger to change
// identically to AddMessage so "live and restored sessions compact the same way":
// restoring a history whose total budget usage crosses the warning threshold must
// compact, dropping some messages into L2.
func TestReplayMessages_CompactionFiresOnceBudgetCrossesWarning(t *testing.T) {
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	sm := NewSegmentedMemoryWithCompression("", 8000, 800, profile)
	sm.SetCompressor(&mockCompressor{enabled: true})

	// A restored history far larger than the warning zone of the 8000-token window.
	var msgs []Message
	big := strings.Repeat("payload token ", 40)
	for i := 0; i < 300; i++ {
		msgs = append(msgs, Message{Role: "user", Content: big})
	}
	sm.ReplayMessages(context.Background(), msgs)

	assert.True(t, sm.HasL2Content(),
		"restore path compacts once total budget usage crosses the warning threshold")
	assert.Less(t, sm.GetL1MessageCount(), len(msgs),
		"restore compaction evicts the oldest messages from L1 into L2")
}
