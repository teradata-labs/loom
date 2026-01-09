// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package metadata

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoader_Load(t *testing.T) {
	// Get project root (3 levels up from pkg/shuttle/metadata)
	projectRoot := filepath.Join("..", "..", "..")
	metadataDir := filepath.Join(projectRoot, "tool_metadata")

	loader := NewLoader(metadataDir)

	t.Run("load web_search metadata", func(t *testing.T) {
		metadata, err := loader.Load("web_search")
		require.NoError(t, err)
		require.NotNil(t, metadata)

		// Verify core fields
		assert.Equal(t, "web_search", metadata.Name)
		assert.Equal(t, "Web Search - Multi-Provider Search Engine", metadata.Title)
		assert.Equal(t, "web", metadata.Category)

		// Verify capabilities
		assert.Contains(t, metadata.Capabilities, "search")
		assert.Contains(t, metadata.Capabilities, "web")

		// Verify use cases
		assert.NotEmpty(t, metadata.UseCases)
		assert.Greater(t, len(metadata.UseCases), 0)

		// Verify conflicts
		assert.NotEmpty(t, metadata.Conflicts)
		// Should have conflict with http_request
		foundConflict := false
		for _, conflict := range metadata.Conflicts {
			if conflict.ToolName == "http_request" {
				foundConflict = true
				assert.Equal(t, "high", conflict.Severity)
				assert.NotEmpty(t, conflict.Reason)
				assert.NotEmpty(t, conflict.WhenPreferThis)
				assert.NotEmpty(t, conflict.WhenPreferOther)
			}
		}
		assert.True(t, foundConflict, "Should have conflict with http_request")

		// Verify examples
		assert.NotEmpty(t, metadata.Examples)

		// Verify prerequisites
		assert.NotEmpty(t, metadata.Prerequisites)

		// Verify providers
		assert.Contains(t, metadata.Providers, "tavily")
		assert.Contains(t, metadata.Providers, "brave")
		assert.Contains(t, metadata.Providers, "duckduckgo")
	})

	t.Run("load http_request metadata", func(t *testing.T) {
		metadata, err := loader.Load("http_request")
		require.NoError(t, err)
		require.NotNil(t, metadata)

		// Verify core fields
		assert.Equal(t, "http_request", metadata.Name)
		assert.Equal(t, "HTTP Request - REST API Client", metadata.Title)
		assert.Equal(t, "rest_api", metadata.Category)

		// Verify capabilities
		assert.Contains(t, metadata.Capabilities, "http")
		assert.Contains(t, metadata.Capabilities, "rest")
		assert.Contains(t, metadata.Capabilities, "api")

		// Verify conflict with web_search
		foundConflict := false
		for _, conflict := range metadata.Conflicts {
			if conflict.ToolName == "web_search" {
				foundConflict = true
				assert.Equal(t, "high", conflict.Severity)
			}
		}
		assert.True(t, foundConflict, "Should have conflict with web_search")
	})

	t.Run("load non-existent tool returns nil", func(t *testing.T) {
		metadata, err := loader.Load("nonexistent_tool")
		require.NoError(t, err)
		assert.Nil(t, metadata, "Should return nil for non-existent metadata")
	})
}

func TestLoader_LoadAll(t *testing.T) {
	// Get project root
	projectRoot := filepath.Join("..", "..", "..")
	metadataDir := filepath.Join(projectRoot, "tool_metadata")

	loader := NewLoader(metadataDir)

	all, err := loader.LoadAll()
	require.NoError(t, err)
	require.NotNil(t, all)

	// Should have at least web_search and http_request
	assert.Contains(t, all, "web_search")
	assert.Contains(t, all, "http_request")

	// Verify metadata integrity
	assert.NotNil(t, all["web_search"])
	assert.NotNil(t, all["http_request"])
}

func TestLoader_NonExistentDirectory(t *testing.T) {
	loader := NewLoader("/nonexistent/directory")

	// Load should handle non-existent directory gracefully
	metadata, err := loader.Load("web_search")
	require.NoError(t, err)
	assert.Nil(t, metadata)

	// LoadAll should return empty map
	all, err := loader.LoadAll()
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestLoader_Caching(t *testing.T) {
	// Get project root
	projectRoot := filepath.Join("..", "..", "..")
	metadataDir := filepath.Join(projectRoot, "tool_metadata")

	loader := NewLoader(metadataDir)

	t.Run("cache hit returns same pointer", func(t *testing.T) {
		// First load
		metadata1, err1 := loader.Load("web_search")
		require.NoError(t, err1)
		require.NotNil(t, metadata1)

		// Second load should return cached result
		metadata2, err2 := loader.Load("web_search")
		require.NoError(t, err2)
		require.NotNil(t, metadata2)

		// Should be the exact same pointer (cached)
		assert.Same(t, metadata1, metadata2, "Cached result should return same pointer")
	})

	t.Run("cache non-existent tool", func(t *testing.T) {
		// First load of non-existent tool
		metadata1, err1 := loader.Load("nonexistent_tool")
		require.NoError(t, err1)
		assert.Nil(t, metadata1)

		// Second load should also return nil quickly (from cache)
		metadata2, err2 := loader.Load("nonexistent_tool")
		require.NoError(t, err2)
		assert.Nil(t, metadata2)
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		// Test concurrent loads don't cause race conditions
		const numGoroutines = 10
		done := make(chan bool, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer func() { done <- true }()

				// Load different tools concurrently
				_, err1 := loader.Load("web_search")
				assert.NoError(t, err1)

				_, err2 := loader.Load("http_request")
				assert.NoError(t, err2)

				_, err3 := loader.Load("file_read")
				assert.NoError(t, err3)
			}()
		}

		// Wait for all goroutines
		for i := 0; i < numGoroutines; i++ {
			<-done
		}
	})
}

func TestDefaultLoader(t *testing.T) {
	loader := DefaultLoader()
	require.NotNil(t, loader)
	assert.Equal(t, "tool_metadata", loader.metadataDir)
	assert.NotNil(t, loader.cache)
}
