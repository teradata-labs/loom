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
// Package catwalk provides model provider types (stub replacement for github.com/charmbracelet/catwalk).
package catwalk

// InferenceProvider identifies a model provider.
type InferenceProvider string

// Known inference providers.
const (
	InferenceProviderAnthropic InferenceProvider = "anthropic"
	InferenceProviderOpenAI    InferenceProvider = "openai"
	InferenceProviderGoogle    InferenceProvider = "google"
	InferenceProviderAzure     InferenceProvider = "azure"
	InferenceProviderBedrock   InferenceProvider = "bedrock"
	InferenceProviderOllama    InferenceProvider = "ollama"
)

// Type identifies a provider type.
type Type string

// Known provider types.
const (
	TypeAnthropic Type = "anthropic"
	TypeOpenAI    Type = "openai"
	TypeGoogle    Type = "google"
	TypeAzure     Type = "azure"
	TypeBedrock   Type = "bedrock"
	TypeOllama    Type = "ollama"
)

// Provider represents a model provider.
type Provider struct {
	ID       InferenceProvider
	Name     string
	Type     Type
	Models   []Model
	Color    string // Changed from lipgloss.Color to string
	APIKeyFn func() string
}

// Model represents an LLM model.
type Model struct {
	ID              string
	Name            string
	Description     string
	ContextWindow   int
	MaxOutputTokens int
	InputCost       float64 // per 1M tokens
	OutputCost      float64 // per 1M tokens
	Capabilities    []string
}

// GetProviders returns known providers.
func GetProviders() []Provider {
	return []Provider{
		{
			ID:   InferenceProviderAnthropic,
			Name: "Anthropic",
			Type: TypeAnthropic,
			Models: []Model{
				{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4", ContextWindow: 200000},
				{ID: "claude-opus-4-20250514", Name: "Claude Opus 4", ContextWindow: 200000},
			},
		},
		{
			ID:   InferenceProviderBedrock,
			Name: "Amazon Bedrock",
			Type: TypeBedrock,
			Models: []Model{
				{ID: "anthropic.claude-sonnet-4-20250514-v1:0", Name: "Claude Sonnet 4 (Bedrock)", ContextWindow: 200000},
			},
		},
		{
			ID:   InferenceProviderOllama,
			Name: "Ollama",
			Type: TypeOllama,
			Models: []Model{
				{ID: "llama3.2", Name: "Llama 3.2", ContextWindow: 128000},
			},
		},
	}
}

// GetProvider returns a provider by ID.
func GetProvider(id InferenceProvider) *Provider {
	for _, p := range GetProviders() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}
