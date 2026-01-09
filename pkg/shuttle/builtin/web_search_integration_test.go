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

// TestWebSearchTool_DefaultProvider_NoAPIKey tests that the tool requires
// an API key for Tavily (the default provider).
func TestWebSearchTool_DefaultProvider_NoAPIKey(t *testing.T) {
	tool := NewWebSearchTool()

	// Execute search with only a query - no provider, no API key
	// This should fail because Tavily (default) requires an API key
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "golang concurrency",
	})

	require.NoError(t, err)
	assert.False(t, result.Success, "Search should fail without API key for Tavily")
	assert.NotNil(t, result.Error)
	assert.Equal(t, "MISSING_API_KEY", result.Error.Code)
	assert.Contains(t, result.Error.Message, "tavily")
}

// TestWebSearchTool_MinimalUsage tests the absolute minimal usage
// Now uses DuckDuckGo explicitly since Tavily requires an API key
func TestWebSearchTool_MinimalUsage(t *testing.T) {
	tool := NewWebSearchTool()

	// Just a query with duckduckgo (which doesn't need API key)
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

	// Check provider default
	providerSchema := schema.Properties["provider"]
	assert.NotNil(t, providerSchema)
	assert.Equal(t, "tavily", providerSchema.Default, "Default provider should be tavily")

	// Check that query is the only required field
	assert.Equal(t, []string{"query"}, schema.Required)

	// Verify enum includes all providers (order changed, tavily first)
	assert.Contains(t, providerSchema.Enum, "tavily")
	assert.Contains(t, providerSchema.Enum, "brave")
	assert.Contains(t, providerSchema.Enum, "serpapi")
	assert.Contains(t, providerSchema.Enum, "duckduckgo")
}
