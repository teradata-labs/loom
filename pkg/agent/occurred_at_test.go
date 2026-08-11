// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestChat_OccurredAtOverride_AnchorsAllRows verifies WithOccurredAt threads a
// replay/import arrival time through the whole conversation call: every row
// appendMessage persists — the user turn and the assistant reply — carries the
// override instead of the wall clock, so temporal grounding reads the
// conversation's historical time.
func TestChat_OccurredAtOverride_AnchorsAllRows(t *testing.T) {
	llm := &capturingLLM{response: "ok"}
	ag := contentBlocksTestAgent(llm)

	occurred := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	ctx := WithOccurredAt(context.Background(), occurred)

	_, err := ag.Chat(ctx, "occurred-at-session", "what ran that day?")
	require.NoError(t, err)

	session, ok := ag.GetSession("occurred-at-session")
	require.True(t, ok)
	messages := session.GetMessages()
	require.NotEmpty(t, messages)

	var stamped int
	for _, m := range messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		require.True(t, m.Timestamp.Equal(occurred),
			"%s row Timestamp = %s, want the occurred_at override %s", m.Role, m.Timestamp, occurred)
		stamped++
	}
	require.GreaterOrEqual(t, stamped, 2, "user turn and assistant reply should both be anchored")
}

// TestChat_NoOverride_KeepsWallClock pins the default: without WithOccurredAt,
// rows keep their wall-clock arrival timestamps.
func TestChat_NoOverride_KeepsWallClock(t *testing.T) {
	llm := &capturingLLM{response: "ok"}
	ag := contentBlocksTestAgent(llm)

	before := time.Now().Add(-time.Minute)
	_, err := ag.Chat(context.Background(), "wall-clock-session", "hello")
	require.NoError(t, err)

	session, ok := ag.GetSession("wall-clock-session")
	require.True(t, ok)
	for _, m := range session.GetMessages() {
		if m.Role == "user" {
			require.True(t, m.Timestamp.After(before),
				"user row keeps its wall-clock arrival time without an override")
		}
	}
}
