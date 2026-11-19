package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

func TestVerifyItemBinding_SQLiteRoundTripRejectsChangedSequence(t *testing.T) {
	ctx := context.Background()
	store, err := shuttle.NewSQLiteHumanRequestStore(shuttle.SQLiteConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	call := ToolCall{
		ID:    "call-1",
		Name:  "example_tool",
		Input: map[string]interface{}{"value": "same"},
	}
	params, truncated := buildParkParams([]parkItem{{
		call: call,
		seq:  1,
		kind: "approval",
	}})
	require.False(t, truncated)

	now := time.Now()
	req := &shuttle.HumanRequest{
		ID:          "park-sequence-round-trip",
		AgentID:     "agent-1",
		SessionID:   "session-1",
		Question:    "Approve this call?",
		RequestType: "parked",
		Priority:    "normal",
		Kind:        "approval",
		Summary:     "example_tool",
		Timeout:     time.Minute,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Minute),
		Status:      "pending",
		Params:      params,
	}
	require.NoError(t, store.Store(ctx, req))

	got, err := store.Get(ctx, req.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.ErrorIs(t, verifyItemBinding(got, Message{ToolCalls: []ToolCall{call}}), ErrStaleDecision)
}
