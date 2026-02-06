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

	// Static Ollama models should be present
	models := reg.GetModelsForProvider("ollama")
	require.NotNil(t, models)
	assert.Len(t, models, 3) // llama3.1, llama3.2, qwen2.5
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

	// Static models should remain intact
	models := reg.GetModelsForProvider("ollama")
	assert.Len(t, models, 3)
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

	// Should keep static defaults when no models discovered
	models := reg.GetModelsForProvider("ollama")
	assert.Len(t, models, 3)
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
