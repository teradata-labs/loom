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
	InferenceProviderAnthropic     InferenceProvider = "anthropic"
	InferenceProviderOpenAI        InferenceProvider = "openai"
	InferenceProviderGoogle        InferenceProvider = "google"
	InferenceProviderAzure         InferenceProvider = "azure"
	InferenceProviderBedrock       InferenceProvider = "bedrock"
	InferenceProviderBedrockMantle InferenceProvider = "bedrock-mantle"
	InferenceProviderOllama        InferenceProvider = "ollama"
)

// Type identifies a provider type.
type Type string

// Known provider types.
const (
	TypeAnthropic     Type = "anthropic"
	TypeOpenAI        Type = "openai"
	TypeGoogle        Type = "google"
	TypeAzure         Type = "azure"
	TypeBedrock       Type = "bedrock"
	TypeBedrockMantle Type = "bedrock-mantle"
	TypeOllama        Type = "ollama"
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
			ID:   InferenceProviderBedrockMantle,
			Name: "Amazon Bedrock Mantle",
			Type: TypeBedrockMantle,
			// Every Claude id AWS documents on bedrock-mantle, matching the
			// anthropic.* rows of pkg/llm/catalog "bedrock-mantle" (costs are the
			// regional, single-Region rates). catwalk_test.go keeps the two in step.
			Models: []Model{
				{ID: "anthropic.claude-fable-5-1", Name: "Claude Fable 5.1 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 11.0, OutputCost: 55.0},
				{ID: "anthropic.claude-fable-5", Name: "Claude Fable 5 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 11.0, OutputCost: 55.0},
				{ID: "anthropic.claude-mythos-5", Name: "Claude Mythos 5 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 11.0, OutputCost: 55.0},
				{ID: "anthropic.claude-opus-5-5", Name: "Claude Opus 5.5 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 4.40, OutputCost: 22.0},
				{ID: "anthropic.claude-opus-5", Name: "Claude Opus 5 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 5.50, OutputCost: 27.50},
				{ID: "anthropic.claude-opus-4-8", Name: "Claude Opus 4.8 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 5.50, OutputCost: 27.50},
				{ID: "anthropic.claude-opus-4-7", Name: "Claude Opus 4.7 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 5.50, OutputCost: 27.50},
				{ID: "anthropic.claude-sonnet-5-5", Name: "Claude Sonnet 5.5 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 2.20, OutputCost: 11.0},
				{ID: "anthropic.claude-sonnet-5", Name: "Claude Sonnet 5 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 2.20, OutputCost: 11.0},
				{ID: "anthropic.claude-haiku-5-5", Name: "Claude Haiku 5.5 (Bedrock Mantle)", ContextWindow: 1000000, MaxOutputTokens: 128000, InputCost: 0.11, OutputCost: 0.55},
				{ID: "anthropic.claude-haiku-4-5", Name: "Claude Haiku 4.5 (Bedrock Mantle)", ContextWindow: 200000, MaxOutputTokens: 64000, InputCost: 1.10, OutputCost: 5.50},
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
