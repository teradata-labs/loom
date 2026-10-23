// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package builtin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teradata-labs/loom/pkg/prompts"
	"github.com/teradata-labs/loom/pkg/shuttle"
)

// Tests in this file use t.Setenv, so they must not call t.Parallel.
// TestMain clears every provider key before the package runs.

const testSecret = "test-secret-value-must-not-leak"

func TestWebSearchTool_Readiness(t *testing.T) {
	tests := []struct {
		name            string
		env             map[string]string
		wantState       shuttle.ReadinessState
		wantDefault     string
		wantInSummary   []string
		wantRemediation string
	}{
		{
			name:        "no credentials",
			wantState:   shuttle.ReadinessDegraded,
			wantDefault: "duckduckgo",
			wantInSummary: []string{
				"No API key for tavily, brave, serpapi",
				"MISSING_API_KEY",
				"Default duckduckgo returns instant answers only",
			},
			wantRemediation: "TAVILY_API_KEY",
		},
		{
			name:          "tavily configured",
			env:           map[string]string{"TAVILY_API_KEY": testSecret},
			wantState:     shuttle.ReadinessReady,
			wantDefault:   "tavily",
			wantInSummary: []string{"default tavily; configured: tavily.", "No API key for brave, serpapi"},
		},
		{
			name:          "brave configured via alternate variable",
			env:           map[string]string{"BRAVE_SEARCH_API_KEY": testSecret},
			wantState:     shuttle.ReadinessReady,
			wantDefault:   "brave",
			wantInSummary: []string{"default brave; configured: brave.", "No API key for tavily, serpapi"},
		},
		{
			name:          "multiple configured picks the first in order",
			env:           map[string]string{"BRAVE_API_KEY": testSecret, "SERPAPI_API_KEY": testSecret},
			wantState:     shuttle.ReadinessReady,
			wantDefault:   "brave",
			wantInSummary: []string{"default brave; configured: brave, serpapi.", "No API key for tavily "},
		},
		{
			name:          "all configured",
			env:           map[string]string{"TAVILY_API_KEY": testSecret, "BRAVE_API_KEY": testSecret, "SERPAPI_KEY": testSecret},
			wantState:     shuttle.ReadinessReady,
			wantDefault:   "tavily",
			wantInSummary: []string{"default tavily; configured: tavily, brave, serpapi."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			tool := NewWebSearchTool()

			readiness := tool.Readiness(context.Background())
			assert.Equal(t, tt.wantState, readiness.State)
			for _, want := range tt.wantInSummary {
				assert.Contains(t, readiness.Summary, want)
			}
			if tt.wantRemediation != "" {
				assert.Contains(t, readiness.Remediation, tt.wantRemediation)
			} else {
				assert.Empty(t, readiness.Remediation)
			}

			schema := tool.InputSchema()
			assert.Equal(t, tt.wantDefault, schema.Properties["provider"].Default)
			assert.Contains(t, schema.Properties["provider"].Description, "default: "+tt.wantDefault)
			assert.Contains(t, tool.Description(), readiness.Summary)

			assert.NotContains(t, readiness.Summary, "..")

			// Readiness output reaches the LLM and the logs: never a key value.
			for _, text := range []string{readiness.Summary, readiness.Remediation, tool.Description(), schema.Properties["provider"].Description} {
				assert.NotContains(t, text, testSecret)
			}
		})
	}
}

// TestWebSearchTool_Readiness_ReflectsLaterConfiguration tests that readiness
// is evaluated per call: looms exports keyring keys to the environment after
// tools may already exist.
func TestWebSearchTool_Readiness_ReflectsLaterConfiguration(t *testing.T) {
	tool := NewWebSearchTool()
	require.Equal(t, shuttle.ReadinessDegraded, tool.Readiness(context.Background()).State)

	t.Setenv("TAVILY_API_KEY", testSecret)
	assert.Equal(t, shuttle.ReadinessReady, tool.Readiness(context.Background()).State)
	assert.Equal(t, "tavily", tool.InputSchema().Properties["provider"].Default)
}

// TestWebSearchTool_DefaultProvider_UsesConfiguredProvider tests that a query
// without a provider goes to the configured provider, with its key.
func TestWebSearchTool_DefaultProvider_UsesConfiguredProvider(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", testSecret)

	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Brave","url":"https://example.com","description":"result"}]}}`))
	}))
	t.Cleanup(server.Close)

	tool := NewWebSearchTool()
	tool.client = server.Client()
	tool.braveEndpoint = server.URL

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "golang"})
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Equal(t, "brave", result.Metadata["provider"])
	assert.Equal(t, testSecret, gotToken)
}

// TestWebSearchTool_ExplicitProvider_MissingKey tests the execution-time
// check for each keyed provider, which stays as defense in depth.
func TestWebSearchTool_ExplicitProvider_MissingKey(t *testing.T) {
	tests := []struct {
		provider   string
		wantEnvVar string
	}{
		{provider: "tavily", wantEnvVar: "TAVILY_API_KEY"},
		{provider: "brave", wantEnvVar: "BRAVE_API_KEY or BRAVE_SEARCH_API_KEY"},
		{provider: "serpapi", wantEnvVar: "SERPAPI_KEY or SERPAPI_API_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			tool := NewWebSearchTool()
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"query":    "golang",
				"provider": tt.provider,
			})
			require.NoError(t, err)
			assert.False(t, result.Success)
			require.NotNil(t, result.Error)
			assert.Equal(t, "MISSING_API_KEY", result.Error.Code)
			assert.Contains(t, result.Error.Suggestion, tt.wantEnvVar)
		})
	}
}

// TestWebSearchTool_PromptAwareDescriptionCarriesReadiness tests that the
// shipped prompt (prompts/tools/web_search.yaml), which is static text, is
// still followed by the live provider status and no longer claims Tavily is
// always the default.
func TestWebSearchTool_PromptAwareDescriptionCarriesReadiness(t *testing.T) {
	ctx := context.Background()
	registry := prompts.NewFileRegistry(filepath.Join("..", "..", "..", "prompts"))
	require.NoError(t, registry.Reload(ctx))
	// The nested prompt file (name: tools, namespace: loom.web) is keyed
	// tools.web.web_search.
	const promptKey = "tools.web.web_search"
	prompt, err := registry.Get(ctx, promptKey, nil)
	require.NoError(t, err)
	assert.NotContains(t, prompt, "Tavily by default")
	assert.NotContains(t, prompt, "Tavily (default")

	tool := NewWebSearchTool()
	desc := shuttle.NewPromptAwareTool(tool, registry, promptKey).Description()
	assert.Contains(t, desc, strings.TrimRight(prompt, "\n"))
	assert.Contains(t, desc, tool.Readiness(ctx).Summary)
}
