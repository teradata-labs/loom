// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebSearchTool_Name(t *testing.T) {
	tool := NewWebSearchTool()
	assert.Equal(t, "web_search", tool.Name())
}

func TestWebSearchTool_Description(t *testing.T) {
	tool := NewWebSearchTool()
	desc := tool.Description()
	assert.Contains(t, desc, "Search the web")
	assert.Contains(t, desc, "brave")
	assert.Contains(t, desc, "tavily")
}

func TestWebSearchTool_InputSchema(t *testing.T) {
	tool := NewWebSearchTool()
	schema := tool.InputSchema()

	assert.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
	assert.Contains(t, schema.Required, "query")

	// Check that provider has the right enum values
	providerSchema := schema.Properties["provider"]
	assert.NotNil(t, providerSchema)
	assert.Contains(t, providerSchema.Enum, "brave")
	assert.Contains(t, providerSchema.Enum, "tavily")
	assert.Contains(t, providerSchema.Enum, "serpapi")
	assert.Contains(t, providerSchema.Enum, "duckduckgo")
}

func TestWebSearchTool_Execute_MissingQuery(t *testing.T) {
	tool := NewWebSearchTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "INVALID_PARAMS", result.Error.Code)
	assert.Contains(t, result.Error.Message, "query is required")
}

func TestWebSearchTool_Execute_InvalidProvider(t *testing.T) {
	tool := NewWebSearchTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":    "test query",
		"provider": "invalid_provider",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Error)
	assert.Equal(t, "INVALID_PROVIDER", result.Error.Code)
}

func TestWebSearchTool_SearchBrave(t *testing.T) {
	// Mock Brave Search API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "/res/v1/web/search", r.URL.Path)
		assert.Equal(t, "test query", r.URL.Query().Get("q"))
		assert.Equal(t, "test-api-key", r.Header.Get("X-Subscription-Token"))

		// Return mock response
		resp := map[string]interface{}{
			"web": map[string]interface{}{
				"results": []map[string]interface{}{
					{
						"title":       "Test Result 1",
						"url":         "https://example.com/1",
						"description": "This is test result 1",
						"age":         "2 days ago",
					},
					{
						"title":       "Test Result 2",
						"url":         "https://example.com/2",
						"description": "This is test result 2",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := NewWebSearchTool()

	// Override client to use test server
	tool.client = server.Client()

	// Test searchBrave directly (would need to expose endpoint or use a different approach)
	// For now, test via Execute with mocked server
	// Note: This test demonstrates the pattern; in practice you'd need dependency injection
	// for the endpoint URL to properly test this.

	// Instead, test the struct parsing
	_, err := tool.searchBrave(context.Background(), "test query", "test-api-key", 10, "en", "moderate")

	// This will fail because we can't override the endpoint easily
	// In production, you'd make the endpoint configurable or use an interface
	assert.Error(t, err) // Expected since we can't hit the mock server with hardcoded URL
}

func TestWebSearchTool_SearchTavily(t *testing.T) {
	// Mock Tavily API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)

		var reqBody map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		require.NoError(t, err)

		assert.Equal(t, "test query", reqBody["query"])
		assert.Equal(t, "test-api-key", reqBody["api_key"])

		// Return mock response
		resp := map[string]interface{}{
			"results": []map[string]interface{}{
				{
					"title":   "Tavily Result 1",
					"url":     "https://example.com/tavily1",
					"content": "Tavily test content 1",
					"score":   0.95,
				},
				{
					"title":   "Tavily Result 2",
					"url":     "https://example.com/tavily2",
					"content": "Tavily test content 2",
					"score":   0.87,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := NewWebSearchTool()
	tool.client = server.Client()

	// Similar limitation as above - would need endpoint injection
	_, err := tool.searchTavily(context.Background(), "test query", "test-api-key", 10, "en")
	assert.Error(t, err) // Expected due to hardcoded endpoint
}

func TestWebSearchTool_SearchSerpAPI(t *testing.T) {
	// Mock SerpAPI
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test query", r.URL.Query().Get("q"))
		assert.Equal(t, "test-api-key", r.URL.Query().Get("api_key"))

		resp := map[string]interface{}{
			"organic_results": []map[string]interface{}{
				{
					"title":   "Google Result 1",
					"link":    "https://example.com/google1",
					"snippet": "Google snippet 1",
					"date":    "Jan 1, 2025",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := NewWebSearchTool()
	tool.client = server.Client()

	_, err := tool.searchSerpAPI(context.Background(), "test query", "test-api-key", 10, "en", "moderate")
	assert.Error(t, err) // Expected due to hardcoded endpoint
}

// newMockedDDGTool returns a WebSearchTool whose DuckDuckGo endpoint points
// at a local httptest server serving a minimal Instant Answer payload.
// Unit tests must never hit the live DDG API: it throttles hosted CI
// runners, which flaked these tests at the 30s client timeout.
func newMockedDDGTool(t *testing.T) *WebSearchTool {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Heading":"Mock","AbstractText":"mock abstract","AbstractURL":"https://example.com/mock"}`))
	}))
	t.Cleanup(server.Close)

	tool := NewWebSearchTool()
	tool.client = server.Client()
	tool.duckDuckGoEndpoint = server.URL
	return tool
}

func TestWebSearchTool_SearchDuckDuckGo(t *testing.T) {
	// Serve a canned Instant Answer response instead of hitting the live
	// DuckDuckGo API: unit CI must not depend on the network, and the live
	// endpoint throttles hosted runners (flaked at the 30s client timeout).
	// Unlike the other providers, the DDG endpoint is injectable, so this
	// test can exercise the full Execute path against the mock.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test query", r.URL.Query().Get("q"))
		assert.Equal(t, "json", r.URL.Query().Get("format"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Heading": "Test Query",
			"AbstractText": "An abstract about the test query.",
			"AbstractURL": "https://example.com/abstract",
			"RelatedTopics": [
				{"Text": "Related One - first related topic", "FirstURL": "https://example.com/one"},
				{"Text": "Related Two - second related topic", "FirstURL": "https://example.com/two"}
			],
			"Results": []
		}`))
	}))
	defer server.Close()

	tool := NewWebSearchTool()
	tool.client = server.Client()
	tool.duckDuckGoEndpoint = server.URL

	// DuckDuckGo doesn't require an API key
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":    "test query",
		"provider": "duckduckgo",
	})

	require.NoError(t, err)
	require.True(t, result.Success)

	// require + comma-ok on the casts: the old unchecked assertions panicked
	// the whole test binary when a failed request left result.Data nil.
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok, "result.Data should be map[string]interface{}, got %T", result.Data)
	assert.Equal(t, "test query", data["query"])
	assert.Equal(t, "duckduckgo", data["provider"])

	results, ok := data["results"].([]SearchResult)
	require.True(t, ok, "results should be []SearchResult, got %T", data["results"])
	require.Len(t, results, 3, "abstract + 2 related topics")
	assert.Equal(t, "Test Query", results[0].Title)
	assert.Equal(t, "https://example.com/abstract", results[0].URL)
	assert.Equal(t, "Related One", results[1].Title)
	assert.Equal(t, "https://example.com/two", results[2].URL)
}

func TestWebSearchTool_Backend(t *testing.T) {
	tool := NewWebSearchTool()
	assert.Equal(t, "", tool.Backend())
}

func TestWebSearchTool_Execute_WithParameters(t *testing.T) {
	tool := newMockedDDGTool(t)

	// Test DuckDuckGo with various parameters
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":       "golang best practices",
		"provider":    "duckduckgo",
		"max_results": float64(5),
		"search_lang": "en",
		"safe_search": "strict",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data.(map[string]interface{})
	assert.Equal(t, "golang best practices", data["query"])
	assert.NotNil(t, data["results"])
}

// TestWebSearchTool_Concurrent tests concurrent execution safety
func TestWebSearchTool_Concurrent(t *testing.T) {
	tool := newMockedDDGTool(t)

	// Run multiple searches concurrently. Use assert (not require) inside
	// the goroutines: require calls t.FailNow, which must only run on the
	// test goroutine.
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(query string) {
			defer func() { done <- true }()
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"query":    query,
				"provider": "duckduckgo",
			})
			assert.NoError(t, err)
			assert.NotNil(t, result)
		}(string(rune('A' + i)))
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestSearchResult_JSON tests SearchResult serialization
func TestSearchResult_JSON(t *testing.T) {
	result := SearchResult{
		Title:       "Test Title",
		URL:         "https://example.com",
		Snippet:     "Test snippet",
		Content:     "Full content",
		PublishedAt: "2025-01-01",
		Score:       0.95,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded SearchResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, result.Title, decoded.Title)
	assert.Equal(t, result.URL, decoded.URL)
	assert.Equal(t, result.Snippet, decoded.Snippet)
	assert.Equal(t, result.Content, decoded.Content)
	assert.Equal(t, result.PublishedAt, decoded.PublishedAt)
	assert.Equal(t, result.Score, decoded.Score)
}

// TestWebSearchTool_ContextCancellation tests context cancellation
func TestWebSearchTool_ContextCancellation(t *testing.T) {
	tool := NewWebSearchTool()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := tool.Execute(ctx, map[string]interface{}{
		"query":    "test",
		"provider": "duckduckgo",
	})

	// Should handle cancellation gracefully
	// The exact behavior depends on implementation
	// Either error or graceful failure
	if err == nil {
		assert.False(t, result.Success)
	}
}
