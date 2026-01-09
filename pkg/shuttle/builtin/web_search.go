// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Default web search API endpoints (can be overridden via environment variables)
const (
	DefaultBraveEndpoint      = "https://api.search.brave.com/res/v1/web/search"
	DefaultTavilyEndpoint     = "https://api.tavily.com/search"
	DefaultSerpAPIEndpoint    = "https://serpapi.com/search"
	DefaultDuckDuckGoEndpoint = "https://api.duckduckgo.com/"
	DefaultSearchTimeout      = 30 * time.Second
)

// WebSearchTool provides web search capabilities via multiple providers.
// Supports: Brave Search, Tavily, SerpAPI, and DuckDuckGo.
// Apple-style: It just works with sensible defaults.
//
// Endpoints can be configured via environment variables:
//   - LOOM_WEB_SEARCH_BRAVE_ENDPOINT
//   - LOOM_WEB_SEARCH_TAVILY_ENDPOINT
//   - LOOM_WEB_SEARCH_SERPAPI_ENDPOINT
//   - LOOM_WEB_SEARCH_DUCKDUCKGO_ENDPOINT
//   - LOOM_WEB_SEARCH_TIMEOUT_SECONDS
type WebSearchTool struct {
	client             *http.Client
	braveEndpoint      string
	tavilyEndpoint     string
	serpAPIEndpoint    string
	duckDuckGoEndpoint string
}

// NewWebSearchTool creates a new web search tool.
func NewWebSearchTool() *WebSearchTool {
	// Determine timeout from environment or use default
	timeout := DefaultSearchTimeout
	if timeoutStr := os.Getenv("LOOM_WEB_SEARCH_TIMEOUT_SECONDS"); timeoutStr != "" {
		if t, err := time.ParseDuration(timeoutStr + "s"); err == nil {
			timeout = t
		}
	}

	return &WebSearchTool{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		braveEndpoint:      getEnvOrDefault("LOOM_WEB_SEARCH_BRAVE_ENDPOINT", DefaultBraveEndpoint),
		tavilyEndpoint:     getEnvOrDefault("LOOM_WEB_SEARCH_TAVILY_ENDPOINT", DefaultTavilyEndpoint),
		serpAPIEndpoint:    getEnvOrDefault("LOOM_WEB_SEARCH_SERPAPI_ENDPOINT", DefaultSerpAPIEndpoint),
		duckDuckGoEndpoint: getEnvOrDefault("LOOM_WEB_SEARCH_DUCKDUCKGO_ENDPOINT", DefaultDuckDuckGoEndpoint),
	}
}

// getEnvOrDefault returns the environment variable value or the default if not set.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func (t *WebSearchTool) Name() string {
	return "web_search"
}

// Description returns the tool description.
// Deprecated: Description loaded from PromptRegistry (prompts/tools/web_search.yaml).
// This fallback is used only when prompts are not configured.
func (t *WebSearchTool) Description() string {
	return `Search the web for current information, news, articles, and documentation.
Returns search results with titles, URLs, snippets, and optional content.
AI-optimized search results using Tavily by default!

Supports multiple providers:
- Tavily (default, AI-optimized results, requires FREE API key, 1000/month)
- Brave Search (excellent results, requires FREE API key, 2000/month)
- SerpAPI (Google results, requires API key, 100/month free)
- DuckDuckGo (no API key, instant answers for factual queries, limited results)

Use this tool to:
- Find current information beyond training data cutoff
- Research recent news and events
- Look up documentation and guides
- Verify facts and claims
- Find sources for citations
- Search for recipes, shopping, how-tos, and more

Note: Tavily provides AI-optimized search results. Get your FREE API key from:
- Tavily: https://tavily.com/ (1000 searches/month FREE)
- Brave Search: https://brave.com/search/api/ (2000 searches/month FREE)

API keys can be configured via:
- CLI: 'looms config set-key tavily_api_key' (secure system keyring)
- Environment: TAVILY_API_KEY, BRAVE_API_KEY, SERPAPI_KEY
- Parameter: api_key="your-key" (direct specification)`
}

func (t *WebSearchTool) InputSchema() *shuttle.JSONSchema {
	return shuttle.NewObjectSchema(
		"Parameters for web search",
		map[string]*shuttle.JSONSchema{
			"query": shuttle.NewStringSchema("The search query (required)"),
			"provider": shuttle.NewStringSchema("Search provider to use (default: tavily)").
				WithEnum("tavily", "brave", "serpapi", "duckduckgo").
				WithDefault("tavily"),
			"api_key": shuttle.NewStringSchema("API key for the search provider (required for brave, tavily, serpapi; not needed for duckduckgo)"),
			"max_results": shuttle.NewNumberSchema("Maximum number of results to return (default: 10)").
				WithDefault(10),
			"search_lang": shuttle.NewStringSchema("Search language code (default: en)").
				WithDefault("en"),
			"safe_search": shuttle.NewStringSchema("Safe search level (default: moderate)").
				WithEnum("strict", "moderate", "off").
				WithDefault("moderate"),
		},
		[]string{"query"},
	)
}

func (t *WebSearchTool) Execute(ctx context.Context, params map[string]interface{}) (*shuttle.Result, error) {
	start := time.Now()

	// Extract parameters
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PARAMS",
				Message:    "query is required",
				Suggestion: "Provide a search query (e.g., 'latest Go releases 2025')",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	provider := "tavily"
	if p, ok := params["provider"].(string); ok {
		provider = strings.ToLower(p)
	}

	apiKey, _ := params["api_key"].(string)

	// FORCE TAVILY if API key is available (override LLM's provider choice)
	if provider != "tavily" && os.Getenv("TAVILY_API_KEY") != "" {
		provider = "tavily"
	}

	// If no API key provided in params, check environment variables
	if apiKey == "" {
		switch provider {
		case "brave":
			// Try both BRAVE_API_KEY and BRAVE_SEARCH_API_KEY
			if key := os.Getenv("BRAVE_API_KEY"); key != "" {
				apiKey = key
			} else if key := os.Getenv("BRAVE_SEARCH_API_KEY"); key != "" {
				apiKey = key
			}
		case "tavily":
			apiKey = os.Getenv("TAVILY_API_KEY")
		case "serpapi":
			// Try both SERPAPI_KEY and SERPAPI_API_KEY
			if key := os.Getenv("SERPAPI_KEY"); key != "" {
				apiKey = key
			} else if key := os.Getenv("SERPAPI_API_KEY"); key != "" {
				apiKey = key
			}
		}
	}

	maxResults := 10
	if m, ok := params["max_results"].(float64); ok {
		maxResults = int(m)
	}

	searchLang := "en"
	if l, ok := params["search_lang"].(string); ok {
		searchLang = l
	}

	safeSearch := "moderate"
	if s, ok := params["safe_search"].(string); ok {
		safeSearch = s
	}

	// Validate API key for providers that require it
	if (provider == "brave" || provider == "tavily" || provider == "serpapi") && apiKey == "" {
		var envVarName string
		switch provider {
		case "brave":
			envVarName = "BRAVE_API_KEY or BRAVE_SEARCH_API_KEY"
		case "tavily":
			envVarName = "TAVILY_API_KEY"
		case "serpapi":
			envVarName = "SERPAPI_KEY or SERPAPI_API_KEY"
		}
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "MISSING_API_KEY",
				Message:    fmt.Sprintf("API key required for provider: %s", provider),
				Suggestion: fmt.Sprintf("Set api_key parameter, configure via 'looms config set-key', or set %s environment variable. Or use duckduckgo which doesn't require an API key.", envVarName),
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Execute search based on provider
	var results []SearchResult
	var err error

	switch provider {
	case "brave":
		results, err = t.searchBrave(ctx, query, apiKey, maxResults, searchLang, safeSearch)
	case "tavily":
		results, err = t.searchTavily(ctx, query, apiKey, maxResults, searchLang)
	case "serpapi":
		results, err = t.searchSerpAPI(ctx, query, apiKey, maxResults, searchLang, safeSearch)
	case "duckduckgo":
		results, err = t.searchDuckDuckGo(ctx, query, maxResults)
	default:
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "INVALID_PROVIDER",
				Message:    fmt.Sprintf("Unknown search provider: %s", provider),
				Suggestion: "Use one of: brave, tavily, serpapi, duckduckgo",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	if err != nil {
		return &shuttle.Result{
			Success: false,
			Error: &shuttle.Error{
				Code:       "SEARCH_FAILED",
				Message:    fmt.Sprintf("Search failed: %v", err),
				Retryable:  true,
				Suggestion: "Check API key and network connectivity",
			},
			ExecutionTimeMs: time.Since(start).Milliseconds(),
		}, nil
	}

	return &shuttle.Result{
		Success: true,
		Data: map[string]interface{}{
			"query":        query,
			"provider":     provider,
			"results":      results,
			"result_count": len(results),
		},
		Metadata: map[string]interface{}{
			"provider":     provider,
			"query":        query,
			"result_count": len(results),
		},
		ExecutionTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func (t *WebSearchTool) Backend() string {
	return "" // Backend-agnostic
}

// SearchResult represents a single search result.
type SearchResult struct {
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Content     string  `json:"content,omitempty"`      // Full content if available
	PublishedAt string  `json:"published_at,omitempty"` // Publication date if available
	Score       float64 `json:"score,omitempty"`        // Relevance score if available
}

// searchBrave searches using Brave Search API.
// Docs: https://api.search.brave.com/app/documentation
func (t *WebSearchTool) searchBrave(ctx context.Context, query, apiKey string, maxResults int, lang, safeSearch string) ([]SearchResult, error) {
	endpoint := t.braveEndpoint

	// Build query parameters
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", fmt.Sprintf("%d", maxResults))
	params.Set("search_lang", lang)

	// Map safe search values
	safeSearchMap := map[string]string{
		"strict":   "strict",
		"moderate": "moderate",
		"off":      "off",
	}
	if val, ok := safeSearchMap[safeSearch]; ok {
		params.Set("safesearch", val)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var braveResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age,omitempty"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	results := make([]SearchResult, 0, len(braveResp.Web.Results))
	for _, r := range braveResp.Web.Results {
		results = append(results, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Snippet:     r.Description,
			PublishedAt: r.Age,
		})
	}

	return results, nil
}

// searchTavily searches using Tavily AI Search API.
// Docs: https://docs.tavily.com/
func (t *WebSearchTool) searchTavily(ctx context.Context, query, apiKey string, maxResults int, lang string) ([]SearchResult, error) {
	endpoint := t.tavilyEndpoint

	reqBody := map[string]interface{}{
		"api_key":        apiKey,
		"query":          query,
		"max_results":    maxResults,
		"search_depth":   "basic",
		"include_answer": false,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var tavilyResp struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tavilyResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	results := make([]SearchResult, 0, len(tavilyResp.Results))
	for _, r := range tavilyResp.Results {
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
			Content: r.Content,
			Score:   r.Score,
		})
	}

	return results, nil
}

// searchSerpAPI searches using SerpAPI (Google results).
// Docs: https://serpapi.com/
func (t *WebSearchTool) searchSerpAPI(ctx context.Context, query, apiKey string, maxResults int, lang, safeSearch string) ([]SearchResult, error) {
	endpoint := t.serpAPIEndpoint

	params := url.Values{}
	params.Set("q", query)
	params.Set("api_key", apiKey)
	params.Set("num", fmt.Sprintf("%d", maxResults))
	params.Set("hl", lang)

	// Map safe search values
	if safeSearch == "strict" {
		params.Set("safe", "active")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var serpResp struct {
		OrganicResults []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
			Date    string `json:"date,omitempty"`
		} `json:"organic_results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&serpResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	results := make([]SearchResult, 0, len(serpResp.OrganicResults))
	for _, r := range serpResp.OrganicResults {
		results = append(results, SearchResult{
			Title:       r.Title,
			URL:         r.Link,
			Snippet:     r.Snippet,
			PublishedAt: r.Date,
		})
	}

	return results, nil
}

// searchDuckDuckGo searches using DuckDuckGo Instant Answer API (free, no auth required).
// This uses the DuckDuckGo Instant Answer API which is free and doesn't require an API key.
// Note: This API provides instant answers, not full web search results like Brave/Tavily.
// For comprehensive web results, use Brave or Tavily with an API key.
func (t *WebSearchTool) searchDuckDuckGo(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	// DuckDuckGo Instant Answer API (free, no auth)
	// Docs: https://duckduckgo.com/api
	endpoint := t.duckDuckGoEndpoint

	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("no_html", "1")
	params.Set("skip_disambig", "1")

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Loom-Agent/1.0")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var ddgResp struct {
		Abstract       string `json:"Abstract"`
		AbstractText   string `json:"AbstractText"`
		AbstractSource string `json:"AbstractSource"`
		AbstractURL    string `json:"AbstractURL"`
		Heading        string `json:"Heading"`
		RelatedTopics  []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"RelatedTopics"`
		Results []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"Results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&ddgResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	results := make([]SearchResult, 0)

	// Add abstract/instant answer if available
	if ddgResp.AbstractText != "" && ddgResp.AbstractURL != "" {
		results = append(results, SearchResult{
			Title:   ddgResp.Heading,
			URL:     ddgResp.AbstractURL,
			Snippet: ddgResp.AbstractText,
			Content: ddgResp.AbstractText,
		})
	}

	// Add direct results
	for _, r := range ddgResp.Results {
		if len(results) >= maxResults {
			break
		}
		if r.FirstURL != "" {
			results = append(results, SearchResult{
				Title:   extractTitleFromText(r.Text),
				URL:     r.FirstURL,
				Snippet: r.Text,
			})
		}
	}

	// Add related topics
	for _, topic := range ddgResp.RelatedTopics {
		if len(results) >= maxResults {
			break
		}
		if topic.FirstURL != "" {
			results = append(results, SearchResult{
				Title:   extractTitleFromText(topic.Text),
				URL:     topic.FirstURL,
				Snippet: topic.Text,
			})
		}
	}

	// If no results found, provide helpful message
	if len(results) == 0 {
		results = append(results, SearchResult{
			Title:   "No instant answers found",
			URL:     "https://duckduckgo.com/?q=" + url.QueryEscape(query),
			Snippet: fmt.Sprintf("DuckDuckGo's Instant Answer API didn't find results for '%s'. Try the full DuckDuckGo search, or use Brave/Tavily for comprehensive web results.", query),
		})
	}

	return results, nil
}

// extractTitleFromText extracts a title from DuckDuckGo's text field.
// The text format is typically "Title - Description" or just text.
func extractTitleFromText(text string) string {
	// Try to extract title before " - "
	if idx := strings.Index(text, " - "); idx > 0 {
		return text[:idx]
	}

	// Try to extract title before ": "
	if idx := strings.Index(text, ": "); idx > 0 {
		return text[:idx]
	}

	// Use first 100 chars as title
	if len(text) > 100 {
		return text[:97] + "..."
	}

	return text
}
