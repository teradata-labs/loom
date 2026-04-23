package factory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelRegistry_GetModelsForProvider(t *testing.T) {
	reg := NewModelRegistry()

	// Counts updated 2026-04-22 after refreshing the built-in catalog to current
	// provider offerings (added Opus 4.7, GPT-5.x ladder, Gemini 3.x previews,
	// new Mistral dated IDs, llama4/gemma4/phi4-mini/qwen3.5).
	tests := []struct {
		provider      string
		expectedCount int
	}{
		{"anthropic", 7},
		{"openai", 16},
		{"gemini", 7},
		{"bedrock", 8},
		{"ollama", 14},
		{"mistral", 13},
		{"azure-openai", 8},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			models := reg.GetModelsForProvider(tt.provider)
			require.NotNil(t, models, "provider %s should have models", tt.provider)
			assert.Len(t, models, tt.expectedCount, "provider %s model count", tt.provider)

			// Verify all models have the correct provider set
			for _, m := range models {
				assert.Equal(t, tt.provider, m.Provider)
				assert.NotEmpty(t, m.Id)
				assert.NotEmpty(t, m.Name)
				assert.Greater(t, m.ContextWindow, int32(0))
			}
		})
	}
}

func TestModelRegistry_GetModelsForProvider_HuggingFaceRemoved(t *testing.T) {
	reg := NewModelRegistry()
	models := reg.GetModelsForProvider("huggingface")
	assert.Nil(t, models, "huggingface provider should not exist in registry")
}

func TestModelRegistry_GetAllModels_TotalCount(t *testing.T) {
	reg := NewModelRegistry()
	all := reg.GetAllModels()
	// anthropic(7) + openai(16) + gemini(7) + bedrock(8) + ollama(14) + mistral(13) + azure-openai(8) = 73
	assert.Len(t, all, 73)
}

func TestModelRegistry_NewFields(t *testing.T) {
	reg := NewModelRegistry()

	t.Run("claude-opus-4-6 has expected new fields", func(t *testing.T) {
		models := reg.GetModelsForProvider("anthropic")
		require.NotEmpty(t, models)

		var found bool
		for _, m := range models {
			if m.Id == "claude-opus-4-6" {
				found = true
				assert.Equal(t, int32(128_000), m.MaxOutputTokens)
				assert.True(t, m.IsReasoning)
				assert.True(t, m.ShowInDropdown)
				break
			}
		}
		require.True(t, found, "claude-opus-4-6 should exist in anthropic models")
	})

	t.Run("claude-haiku-4-5 has expected new fields", func(t *testing.T) {
		models := reg.GetModelsForProvider("anthropic")
		for _, m := range models {
			if m.Id == "claude-haiku-4-5-20251001" {
				assert.True(t, m.IsReasoning)
				assert.True(t, m.ShowInDropdown)
				assert.Equal(t, int32(64_000), m.MaxOutputTokens)
				return
			}
		}
		t.Fatal("claude-haiku-4-5-20251001 not found")
	})

	t.Run("bedrock and azure models have ShowInDropdown=false", func(t *testing.T) {
		for _, provider := range []string{"bedrock", "azure-openai"} {
			models := reg.GetModelsForProvider(provider)
			require.NotEmpty(t, models, "provider %s should have models", provider)
			for _, m := range models {
				assert.False(t, m.ShowInDropdown,
					"model %s (%s) should have ShowInDropdown=false", m.Id, provider)
			}
		}
	})

	t.Run("openai reasoning models", func(t *testing.T) {
		models := reg.GetModelsForProvider("openai")
		reasoningIDs := map[string]bool{
			"o3": true, "o3-mini": true, "o4-mini": true,
		}
		for _, m := range models {
			if reasoningIDs[m.Id] {
				assert.True(t, m.IsReasoning, "model %s should be reasoning", m.Id)
				assert.Greater(t, m.MaxOutputTokens, int32(0), "model %s should have MaxOutputTokens", m.Id)
			}
		}
	})

	t.Run("all models have MaxOutputTokens set", func(t *testing.T) {
		all := reg.GetAllModels()
		for _, m := range all {
			assert.Greater(t, m.MaxOutputTokens, int32(0),
				"model %s (%s) should have MaxOutputTokens > 0", m.Id, m.Provider)
		}
	})
}

func TestModelRegistry_DiscoverOllamaModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		resp := ollamaTagsResponse{
			Models: []ollamaModelEntry{
				{Name: "qwen3:8b", Model: "qwen3:8b", Size: 5000000000},
				{Name: "llama3.3:70b", Model: "llama3.3:70b", Size: 40000000000},
				{Name: "mistral-small3.1:latest", Model: "mistral-small3.1:latest", Size: 15000000000},
				{Name: "qwen3-coder:30b", Model: "qwen3-coder:30b", Size: 18000000000},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reg := NewModelRegistry()

	err := reg.DiscoverOllamaModels(server.URL)
	require.NoError(t, err)

	models := reg.GetModelsForProvider("ollama")
	require.Len(t, models, 4)

	// Verify model IDs match what Ollama reported
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.Id
		assert.Equal(t, "ollama", m.Provider)
		assert.Equal(t, float64(0), m.CostPer_1MInputUsd)
		assert.True(t, m.Available)
	}
	assert.Contains(t, ids, "qwen3:8b")
	assert.Contains(t, ids, "llama3.3:70b")
	assert.Contains(t, ids, "mistral-small3.1:latest")
	assert.Contains(t, ids, "qwen3-coder:30b")
}

func TestModelRegistry_DiscoverOllamaModels_Unreachable(t *testing.T) {
	reg := NewModelRegistry()

	err := reg.DiscoverOllamaModels("http://localhost:1")
	assert.Error(t, err)

	// Static models should remain intact. Count tracks the Ollama section of
	// the built-in catalog (14 entries as of 2026-04-22).
	models := reg.GetModelsForProvider("ollama")
	assert.Len(t, models, 14)
}

func TestModelRegistry_DiscoverOllamaModels_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaTagsResponse{Models: []ollamaModelEntry{}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	reg := NewModelRegistry()

	err := reg.DiscoverOllamaModels(server.URL)
	require.NoError(t, err)

	// Should keep static defaults when no models discovered. Count tracks the
	// Ollama section of the built-in catalog (14 entries as of 2026-04-22).
	models := reg.GetModelsForProvider("ollama")
	assert.Len(t, models, 14)
}

func TestFormatOllamaDisplayName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"llama3.1", "Llama3.1 (Ollama)"},
		{"qwen3-coder:30b", "Qwen3 coder 30B (Ollama)"},
		{"mistral-small3.1:latest", "Mistral small3.1 (Ollama)"},
		{"llama3.3:70b", "Llama3.3 70B (Ollama)"},
		{"qwen3:8b", "Qwen3 8B (Ollama)"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, formatOllamaDisplayName(tt.input))
		})
	}
}
