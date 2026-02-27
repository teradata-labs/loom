// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package evals

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestABTestStore_SaveAndGet(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	result := ABTestResult{
		ABTestID:       "test-id-123",
		SessionID:      "sess-abc",
		Prompt:         "What is Go?",
		Mode:           "side_by_side",
		WinnerProvider: "claude",
		Responses: []ABTestResponse{
			{
				ProviderName: "claude",
				ResponseText: "Go is a compiled language.",
				Score:        8.5,
				LatencyMs:    350,
				CostUSD:      0.001,
			},
			{
				ProviderName: "llama",
				ResponseText: "Go is an open-source programming language.",
				Score:        7.0,
				LatencyMs:    500,
				CostUSD:      0.0,
			},
		},
	}

	err = store.SaveABTest(ctx, result)
	require.NoError(t, err)

	got, err := store.GetABTest(ctx, "test-id-123")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, result.ABTestID, got.ABTestID)
	assert.Equal(t, result.SessionID, got.SessionID)
	assert.Equal(t, result.Prompt, got.Prompt)
	assert.Equal(t, result.Mode, got.Mode)
	assert.Equal(t, result.WinnerProvider, got.WinnerProvider)
	require.Len(t, got.Responses, 2)

	// Responses may be in any order from DB; find by provider name
	responsesByName := make(map[string]ABTestResponse)
	for _, r := range got.Responses {
		responsesByName[r.ProviderName] = r
	}
	claudeResp, ok := responsesByName["claude"]
	require.True(t, ok, "claude response must exist")
	assert.Equal(t, "Go is a compiled language.", claudeResp.ResponseText)
	assert.InDelta(t, 8.5, claudeResp.Score, 0.001)
	assert.Equal(t, int64(350), claudeResp.LatencyMs)
	assert.InDelta(t, 0.001, claudeResp.CostUSD, 0.0001)

	llamaResp, ok := responsesByName["llama"]
	require.True(t, ok, "llama response must exist")
	assert.Equal(t, "Go is an open-source programming language.", llamaResp.ResponseText)
}

func TestABTestStore_GetNotFound(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	_, err = store.GetABTest(ctx, "nonexistent-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestABTestStore_ListABTests(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// Save multiple A/B tests across two sessions
	for i, tc := range []struct {
		id      string
		session string
		winner  string
	}{
		{"ab-1", "sess-1", "claude"},
		{"ab-2", "sess-1", "llama"},
		{"ab-3", "sess-2", "gpt4"},
	} {
		err = store.SaveABTest(ctx, ABTestResult{
			ABTestID:       tc.id,
			SessionID:      tc.session,
			Prompt:         "prompt " + tc.id,
			Mode:           "side_by_side",
			WinnerProvider: tc.winner,
			Responses: []ABTestResponse{
				{ProviderName: tc.winner, ResponseText: "resp " + tc.id, Score: float64(i) + 1.0},
			},
		})
		require.NoError(t, err)
	}

	t.Run("list all tests without filter", func(t *testing.T) {
		results, err := store.ListABTests(ctx, "", 0)
		require.NoError(t, err)
		assert.Len(t, results, 3)
	})

	t.Run("list by session", func(t *testing.T) {
		results, err := store.ListABTests(ctx, "sess-1", 0)
		require.NoError(t, err)
		assert.Len(t, results, 2)
		for _, r := range results {
			assert.Equal(t, "sess-1", r.SessionID)
		}
	})

	t.Run("list with limit", func(t *testing.T) {
		results, err := store.ListABTests(ctx, "", 2)
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("list for unknown session returns empty", func(t *testing.T) {
		results, err := store.ListABTests(ctx, "sess-unknown", 0)
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}

func TestABTestStore_SaveWithNoResponses(t *testing.T) {
	store, err := NewStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	result := ABTestResult{
		ABTestID:  "empty-responses",
		SessionID: "sess-x",
		Prompt:    "test",
		Mode:      "shadow",
	}
	err = store.SaveABTest(ctx, result)
	require.NoError(t, err)

	got, err := store.GetABTest(ctx, "empty-responses")
	require.NoError(t, err)
	assert.Empty(t, got.Responses)
}
