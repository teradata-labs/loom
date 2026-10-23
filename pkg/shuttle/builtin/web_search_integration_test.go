// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebSearchTool_DefaultProvider_NoAPIKey tests that with no API keys
// configured, a query without a provider uses the keyless fallback instead
// of failing on Tavily.
func TestWebSearchTool_DefaultProvider_NoAPIKey(t *testing.T) {
	tool := newMockedDDGTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "golang concurrency",
	})

	require.NoError(t, err)
	require.True(t, result.Success, "default search should use duckduckgo when no key is configured")
	assert.Equal(t, "duckduckgo", result.Metadata["provider"])
}

// TestWebSearchTool_ExplicitTavily_NoAPIKey tests that the execution-time key
// check still rejects an explicitly requested provider whose key is missing.
func TestWebSearchTool_ExplicitTavily_NoAPIKey(t *testing.T) {
	tool := NewWebSearchTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":    "golang concurrency",
		"provider": "tavily",
	})

	require.NoError(t, err)
	assert.False(t, result.Success, "Search should fail without API key for Tavily")
	require.NotNil(t, result.Error)
	assert.Equal(t, "MISSING_API_KEY", result.Error.Code)
	assert.Contains(t, result.Error.Message, "tavily")
	assert.Contains(t, result.Error.Suggestion, "TAVILY_API_KEY")
}

// TestWebSearchTool_MinimalUsage tests the absolute minimal usage:
// just a query plus a provider that needs no API key (DuckDuckGo).
// The DDG endpoint is pointed at a local mock (newMockedDDGTool) — unit CI
// must not depend on the live API, which throttles hosted runners (flaked
// at the 30s client timeout).
func TestWebSearchTool_MinimalUsage(t *testing.T) {
	tool := newMockedDDGTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":    "test",
		"provider": "duckduckgo",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.NotNil(t, result.Data)
}

// TestWebSearchTool_DefaultsInSchema tests that the schema has correct defaults
func TestWebSearchTool_DefaultsInSchema(t *testing.T) {
	tool := NewWebSearchTool()
	schema := tool.InputSchema()

	// With no keys configured (TestMain clears them), the advertised default
	// is the keyless fallback, not tavily.
	providerSchema := schema.Properties["provider"]
	require.NotNil(t, providerSchema)
	assert.Equal(t, "duckduckgo", providerSchema.Default)

	// Check that query is the only required field
	assert.Equal(t, []string{"query"}, schema.Required)

	// Verify enum includes all providers
	assert.Contains(t, providerSchema.Enum, "tavily")
	assert.Contains(t, providerSchema.Enum, "brave")
	assert.Contains(t, providerSchema.Enum, "serpapi")
	assert.Contains(t, providerSchema.Enum, "duckduckgo")
}
