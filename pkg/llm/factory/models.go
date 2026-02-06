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
package factory

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"google.golang.org/protobuf/proto"
)

// ModelRegistry holds information about all supported models across providers.
type ModelRegistry struct {
	models map[string][]*loomv1.ModelInfo
}

// NewModelRegistry creates a new model registry with all supported models.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		models: map[string][]*loomv1.ModelInfo{
			"anthropic": {
				{
					Id:                  "claude-sonnet-4-5-20250929",
					Name:                "Claude Sonnet 4.5",
					Provider:            "anthropic",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       200000,
					CostPer_1MInputUsd:  3.0,
					CostPer_1MOutputUsd: 15.0,
				},
				{
					Id:                  "claude-3-5-sonnet-20241022",
					Name:                "Claude 3.5 Sonnet",
					Provider:            "anthropic",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       200000,
					CostPer_1MInputUsd:  3.0,
					CostPer_1MOutputUsd: 15.0,
				},
				{
					Id:                  "claude-3-opus-20240229",
					Name:                "Claude 3 Opus",
					Provider:            "anthropic",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       200000,
					CostPer_1MInputUsd:  15.0,
					CostPer_1MOutputUsd: 75.0,
				},
			},
			"bedrock": {
				{
					Id:                  "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
					Name:                "Claude Sonnet 4.5 (Bedrock)",
					Provider:            "bedrock",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       200000,
					CostPer_1MInputUsd:  3.0,
					CostPer_1MOutputUsd: 15.0,
				},
				{
					Id:                  "us.anthropic.claude-opus-4-5-20251101-v1:0",
					Name:                "Claude Opus 4.5 (Bedrock)",
					Provider:            "bedrock",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       200000,
					CostPer_1MInputUsd:  15.0,
					CostPer_1MOutputUsd: 75.0,
				},
				{
					Id:                  "us.anthropic.claude-haiku-4-5-20251001-v1:0",
					Name:                "Claude Haiku 4.5 (Bedrock)",
					Provider:            "bedrock",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       200000,
					CostPer_1MInputUsd:  0.8,
					CostPer_1MOutputUsd: 4.0,
				},
			},
			"ollama": {
				{
					Id:                  "llama3.1",
					Name:                "Llama 3.1 (Ollama)",
					Provider:            "ollama",
					Capabilities:        []string{"text", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  0.0,
					CostPer_1MOutputUsd: 0.0,
				},
				{
					Id:                  "llama3.2",
					Name:                "Llama 3.2 (Ollama)",
					Provider:            "ollama",
					Capabilities:        []string{"text", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  0.0,
					CostPer_1MOutputUsd: 0.0,
				},
				{
					Id:                  "qwen2.5",
					Name:                "Qwen 2.5 (Ollama)",
					Provider:            "ollama",
					Capabilities:        []string{"text", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  0.0,
					CostPer_1MOutputUsd: 0.0,
				},
			},
			"openai": {
				{
					Id:                  "gpt-4o",
					Name:                "GPT-4o",
					Provider:            "openai",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  2.5,
					CostPer_1MOutputUsd: 10.0,
				},
				{
					Id:                  "gpt-4-turbo",
					Name:                "GPT-4 Turbo",
					Provider:            "openai",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  10.0,
					CostPer_1MOutputUsd: 30.0,
				},
				{
					Id:                  "gpt-4o-mini",
					Name:                "GPT-4o Mini",
					Provider:            "openai",
					Capabilities:        []string{"text", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  0.15,
					CostPer_1MOutputUsd: 0.6,
				},
			},
			"azure-openai": {
				{
					Id:                  "gpt-4o",
					Name:                "GPT-4o (Azure)",
					Provider:            "azure-openai",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  2.5,
					CostPer_1MOutputUsd: 10.0,
				},
			},
			"mistral": {
				{
					Id:                  "mistral-large-latest",
					Name:                "Mistral Large",
					Provider:            "mistral",
					Capabilities:        []string{"text", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  2.0,
					CostPer_1MOutputUsd: 6.0,
				},
				{
					Id:                  "mistral-small-latest",
					Name:                "Mistral Small",
					Provider:            "mistral",
					Capabilities:        []string{"text", "tool-use"},
					ContextWindow:       32000,
					CostPer_1MInputUsd:  0.2,
					CostPer_1MOutputUsd: 0.6,
				},
			},
			"gemini": {
				{
					Id:                  "gemini-2.0-flash-exp",
					Name:                "Gemini 2.0 Flash",
					Provider:            "gemini",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       1000000,
					CostPer_1MInputUsd:  0.0, // Free during preview
					CostPer_1MOutputUsd: 0.0,
				},
				{
					Id:                  "gemini-1.5-pro",
					Name:                "Gemini 1.5 Pro",
					Provider:            "gemini",
					Capabilities:        []string{"text", "vision", "tool-use"},
					ContextWindow:       2000000,
					CostPer_1MInputUsd:  1.25,
					CostPer_1MOutputUsd: 5.0,
				},
			},
			"huggingface": {
				{
					Id:                  "meta-llama/Llama-3.1-70B-Instruct",
					Name:                "Llama 3.1 70B (HuggingFace)",
					Provider:            "huggingface",
					Capabilities:        []string{"text", "tool-use"},
					ContextWindow:       128000,
					CostPer_1MInputUsd:  0.0, // Varies by deployment
					CostPer_1MOutputUsd: 0.0,
				},
			},
		},
	}
}

// GetModelsForProvider returns all models for a specific provider.
func (r *ModelRegistry) GetModelsForProvider(provider string) []*loomv1.ModelInfo {
	// Normalize provider name
	if provider == "azureopenai" {
		provider = "azure-openai"
	}

	models := r.models[provider]
	if models == nil {
		return nil
	}

	// Return copies to prevent modification
	result := make([]*loomv1.ModelInfo, len(models))
	for i, m := range models {
		result[i] = proto.Clone(m).(*loomv1.ModelInfo)
	}
	return result
}

// GetAllModels returns all models from all providers.
func (r *ModelRegistry) GetAllModels() []*loomv1.ModelInfo {
	var all []*loomv1.ModelInfo
	for _, models := range r.models {
		for _, m := range models {
			all = append(all, proto.Clone(m).(*loomv1.ModelInfo))
		}
	}
	return all
}

// GetAvailableModels returns models from available providers only.
// Uses the factory to check which providers are actually configured.
func (r *ModelRegistry) GetAvailableModels(factory *ProviderFactory) []*loomv1.ModelInfo {
	var available []*loomv1.ModelInfo

	for provider, models := range r.models {
		if factory.IsProviderAvailable(provider) {
			for _, m := range models {
				cloned := proto.Clone(m).(*loomv1.ModelInfo)
				cloned.Available = true
				available = append(available, cloned)
			}
		} else {
			// Still include but mark as unavailable
			for _, m := range models {
				cloned := proto.Clone(m).(*loomv1.ModelInfo)
				cloned.Available = false
				available = append(available, cloned)
			}
		}
	}

	return available
}

// ollamaTagsResponse represents Ollama's /api/tags response.
type ollamaTagsResponse struct {
	Models []ollamaModelEntry `json:"models"`
}

// ollamaModelEntry represents a single model from Ollama's /api/tags.
type ollamaModelEntry struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
}

// DiscoverOllamaModels queries the local Ollama instance's /api/tags endpoint
// and returns ModelInfo entries for all installed models. This replaces the
// hardcoded Ollama model list with whatever is actually available.
func (r *ModelRegistry) DiscoverOllamaModels(endpoint string) error {
	if endpoint == "" {
		endpoint = os.Getenv("OLLAMA_ENDPOINT")
	}
	if endpoint == "" {
		endpoint = os.Getenv("OLLAMA_BASE_URL")
	}
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint + "/api/tags")
	if err != nil {
		return fmt.Errorf("failed to reach Ollama at %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ollama /api/tags returned status %d", resp.StatusCode)
	}

	var tagsResp ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return fmt.Errorf("failed to decode Ollama /api/tags response: %w", err)
	}

	if len(tagsResp.Models) == 0 {
		return nil // No models installed, keep static defaults
	}

	// Replace static Ollama entries with discovered models
	discovered := make([]*loomv1.ModelInfo, 0, len(tagsResp.Models))
	for _, m := range tagsResp.Models {
		modelID := m.Name
		displayName := formatOllamaDisplayName(modelID)
		capabilities := []string{"text", "tool-use"} // Ollama tool support is probed at runtime

		discovered = append(discovered, &loomv1.ModelInfo{
			Id:                  modelID,
			Name:                displayName,
			Provider:            "ollama",
			Capabilities:        capabilities,
			ContextWindow:       128000, // Most Ollama models support 128K
			CostPer_1MInputUsd:  0.0,
			CostPer_1MOutputUsd: 0.0,
			Available:           true,
		})
	}

	r.models["ollama"] = discovered
	return nil
}

// formatOllamaDisplayName creates a human-readable name from an Ollama model tag.
// e.g. "qwen3-coder:30b" -> "Qwen3 Coder 30B (Ollama)"
func formatOllamaDisplayName(modelID string) string {
	// Split on colon to separate name from tag
	parts := strings.SplitN(modelID, ":", 2)
	name := parts[0]
	tag := ""
	if len(parts) > 1 {
		tag = strings.ToUpper(parts[1])
	}

	// Capitalize first letter of name
	if len(name) > 0 {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	// Replace hyphens with spaces for readability
	name = strings.ReplaceAll(name, "-", " ")

	if tag != "" && tag != "LATEST" {
		return fmt.Sprintf("%s %s (Ollama)", name, tag)
	}
	return fmt.Sprintf("%s (Ollama)", name)
}
