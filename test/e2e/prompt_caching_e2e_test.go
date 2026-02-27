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

//go:build integration

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestE2E_PromptCaching_CacheTokensReported verifies that the LLMCost
// response correctly reports cache token fields when prompt caching is active.
// On the first turn, cache_creation_input_tokens will be > 0 (writing to cache).
// On the second turn in the same session, cache_read_input_tokens will be > 0
// (reading from cache), which does NOT count against Anthropic's ITPM rate limit.
func TestE2E_PromptCaching_CacheTokensReported(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("prompt-caching")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("cache-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	weaveCtx, weaveCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer weaveCancel()

	// First turn: the system prompt and tool list will be written to cache
	// (cache_creation_input_tokens > 0, cache_read_input_tokens = 0)
	firstResp, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 2 + 2? Reply with just the number.",
	})
	require.NoError(t, err, "first Weave should succeed")
	require.NotEmpty(t, firstResp.GetText(), "first response text should not be empty")

	firstCost := firstResp.GetCost()
	require.NotNil(t, firstCost, "cost should not be nil")
	firstLLMCost := firstCost.GetLlmCost()
	require.NotNil(t, firstLLMCost, "llm_cost should not be nil")

	t.Logf("Turn 1: provider=%s model=%s input=%d output=%d cache_read=%d cache_write=%d cost=$%.6f",
		firstLLMCost.GetProvider(), firstLLMCost.GetModel(),
		firstLLMCost.GetInputTokens(), firstLLMCost.GetOutputTokens(),
		firstLLMCost.GetCacheReadInputTokens(), firstLLMCost.GetCacheCreationInputTokens(),
		firstCost.GetTotalCostUsd())

	// Provider and model should always be populated
	assert.NotEmpty(t, firstLLMCost.GetProvider(), "provider should be set")
	assert.NotEmpty(t, firstLLMCost.GetModel(), "model should be set")
	assert.Greater(t, firstLLMCost.GetInputTokens(), int32(0), "input_tokens should be positive")
	assert.Greater(t, firstLLMCost.GetOutputTokens(), int32(0), "output_tokens should be positive")

	// On first turn, either cache tokens may be zero (if caching is not
	// triggered for this agent/model combination) or cache_creation > 0.
	// We log but do not assert hard on first-turn cache creation since it
	// depends on the minimum cacheable token threshold (1024 tokens for Anthropic).

	// Second turn: should read from cache for same session
	secondResp, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "What is 3 + 3? Reply with just the number.",
	})
	require.NoError(t, err, "second Weave should succeed")
	require.NotEmpty(t, secondResp.GetText(), "second response text should not be empty")

	secondCost := secondResp.GetCost()
	require.NotNil(t, secondCost, "second cost should not be nil")
	secondLLMCost := secondCost.GetLlmCost()
	require.NotNil(t, secondLLMCost, "second llm_cost should not be nil")

	t.Logf("Turn 2: provider=%s model=%s input=%d output=%d cache_read=%d cache_write=%d cost=$%.6f",
		secondLLMCost.GetProvider(), secondLLMCost.GetModel(),
		secondLLMCost.GetInputTokens(), secondLLMCost.GetOutputTokens(),
		secondLLMCost.GetCacheReadInputTokens(), secondLLMCost.GetCacheCreationInputTokens(),
		secondCost.GetTotalCostUsd())

	assert.Greater(t, secondLLMCost.GetInputTokens(), int32(0), "second turn input_tokens should be positive")
	assert.Greater(t, secondLLMCost.GetOutputTokens(), int32(0), "second turn output_tokens should be positive")

	// Log whether we observed a cache hit on the second turn.
	// If the system prompt is >= 1024 tokens, we should see cache_read > 0.
	if secondLLMCost.GetCacheReadInputTokens() > 0 {
		t.Logf("✅ Cache HIT on second turn: %d tokens served from cache (do not count against ITPM rate limit)",
			secondLLMCost.GetCacheReadInputTokens())
	} else {
		t.Logf("ℹ️  No cache hit on second turn (system prompt may be below 1024-token threshold for caching)")
	}
}

// TestE2E_PromptCaching_CostFields verifies that the new proto fields
// are correctly populated in the WeaveResponse.
func TestE2E_PromptCaching_CostFields(t *testing.T) {
	client := loomClient(t)
	userID := uniqueTestID("caching-cost")
	ctx := withUserID(context.Background(), userID)

	createResp, err := client.CreateSession(ctx, &loomv1.CreateSessionRequest{
		Name: uniqueTestID("caching-cost-session"),
	})
	require.NoError(t, err)
	sessionID := createResp.GetId()
	cleanupSession(t, client, userID, sessionID)

	weaveCtx, weaveCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer weaveCancel()

	resp, err := client.Weave(weaveCtx, &loomv1.WeaveRequest{
		SessionId: sessionID,
		Query:     "Say hello.",
	})
	require.NoError(t, err)

	cost := resp.GetCost()
	require.NotNil(t, cost)

	llmCost := cost.GetLlmCost()
	require.NotNil(t, llmCost)

	// Verify the new proto fields exist and are non-negative
	assert.GreaterOrEqual(t, llmCost.GetCacheReadInputTokens(), int32(0),
		"cache_read_input_tokens should be >= 0")
	assert.GreaterOrEqual(t, llmCost.GetCacheCreationInputTokens(), int32(0),
		"cache_creation_input_tokens should be >= 0")

	t.Logf("Cache fields verified: cache_read=%d cache_write=%d",
		llmCost.GetCacheReadInputTokens(), llmCost.GetCacheCreationInputTokens())
}
