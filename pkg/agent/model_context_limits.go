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
package agent

// ModelContextLimits defines the context window and output reservation for a model
type ModelContextLimits struct {
	MaxContextTokens     int // Total context window size
	ReservedOutputTokens int // Tokens reserved for model output (typically 10%)
}

// modelContextLimits is a lookup table for known model context limits.
// Models are keyed by their base name (without version/variant suffixes).
// If a model is not in this table, use the provider's default or auto-detect.
var modelContextLimits = map[string]ModelContextLimits{
	// Anthropic Claude models
	"claude-sonnet-4":   {MaxContextTokens: 200000, ReservedOutputTokens: 20000}, // Claude Sonnet 4.5
	"claude-3-5-sonnet": {MaxContextTokens: 200000, ReservedOutputTokens: 20000},
	"claude-3-opus":     {MaxContextTokens: 200000, ReservedOutputTokens: 20000},
	"claude-3-sonnet":   {MaxContextTokens: 200000, ReservedOutputTokens: 20000},
	"claude-3-haiku":    {MaxContextTokens: 200000, ReservedOutputTokens: 20000},
	"claude-2.1":        {MaxContextTokens: 200000, ReservedOutputTokens: 20000},
	"claude-2.0":        {MaxContextTokens: 100000, ReservedOutputTokens: 10000},

	// Meta Llama models
	"llama3.3":     {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"llama3.2":     {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"llama3.1":     {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"llama3.1:8b":  {MaxContextTokens: 128000, ReservedOutputTokens: 12800}, // Llama 3.1 8B
	"llama3.1:70b": {MaxContextTokens: 128000, ReservedOutputTokens: 12800}, // Llama 3.1 70B
	"llama3":       {MaxContextTokens: 8192, ReservedOutputTokens: 819},
	"llama2":       {MaxContextTokens: 4096, ReservedOutputTokens: 409},
	"llama-3.3":    {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"llama-3.2":    {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"llama-3.1":    {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"llama-3":      {MaxContextTokens: 8192, ReservedOutputTokens: 819},
	"llama-2":      {MaxContextTokens: 4096, ReservedOutputTokens: 409},

	// Mistral models
	"mistral":        {MaxContextTokens: 32000, ReservedOutputTokens: 3200},
	"mixtral":        {MaxContextTokens: 32000, ReservedOutputTokens: 3200},
	"mistral-large":  {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"mistral-medium": {MaxContextTokens: 32000, ReservedOutputTokens: 3200},
	"mistral-small":  {MaxContextTokens: 32000, ReservedOutputTokens: 3200},

	// Qwen models
	"qwen2.5":           {MaxContextTokens: 32000, ReservedOutputTokens: 3200},
	"qwen2.5-coder":     {MaxContextTokens: 32000, ReservedOutputTokens: 3200},
	"qwen2.5-coder:7b":  {MaxContextTokens: 32000, ReservedOutputTokens: 3200},
	"qwen2.5-coder:32b": {MaxContextTokens: 131072, ReservedOutputTokens: 13107},
	"qwen2":             {MaxContextTokens: 32000, ReservedOutputTokens: 3200},

	// DeepSeek models
	"deepseek-r1":       {MaxContextTokens: 64000, ReservedOutputTokens: 6400},
	"deepseek-r1:7b":    {MaxContextTokens: 64000, ReservedOutputTokens: 6400},
	"deepseek-r1:70b":   {MaxContextTokens: 64000, ReservedOutputTokens: 6400},
	"deepseek-coder-v2": {MaxContextTokens: 64000, ReservedOutputTokens: 6400},
	"deepseek-coder":    {MaxContextTokens: 16000, ReservedOutputTokens: 1600},

	// Google Gemma models
	"gemma2":     {MaxContextTokens: 8192, ReservedOutputTokens: 819},
	"gemma2:9b":  {MaxContextTokens: 8192, ReservedOutputTokens: 819},
	"gemma2:27b": {MaxContextTokens: 8192, ReservedOutputTokens: 819},
	"gemma":      {MaxContextTokens: 8192, ReservedOutputTokens: 819},

	// Microsoft Phi models
	"phi4":  {MaxContextTokens: 16000, ReservedOutputTokens: 1600},   // Phi-4 has 16K context
	"phi3":  {MaxContextTokens: 128000, ReservedOutputTokens: 12800}, // Phi-3 supports 128K
	"phi-4": {MaxContextTokens: 16000, ReservedOutputTokens: 1600},
	"phi-3": {MaxContextTokens: 128000, ReservedOutputTokens: 12800},

	// Functionary (specialized for tool calling)
	"functionary": {MaxContextTokens: 32000, ReservedOutputTokens: 3200},

	// OpenAI models (for reference)
	"gpt-4-turbo":       {MaxContextTokens: 128000, ReservedOutputTokens: 12800},
	"gpt-4":             {MaxContextTokens: 8192, ReservedOutputTokens: 819},
	"gpt-3.5-turbo":     {MaxContextTokens: 16385, ReservedOutputTokens: 1638},
	"gpt-3.5-turbo-16k": {MaxContextTokens: 16385, ReservedOutputTokens: 1638},

	// Google Gemini models (for reference)
	"gemini-1.5-pro":   {MaxContextTokens: 1000000, ReservedOutputTokens: 100000}, // 1M context!
	"gemini-1.5-flash": {MaxContextTokens: 1000000, ReservedOutputTokens: 100000},
	"gemini-1.0-pro":   {MaxContextTokens: 32000, ReservedOutputTokens: 3200},
}

// GetModelContextLimits returns the context limits for a given model name.
// Returns the limits if found, or nil if the model is not in the lookup table.
func GetModelContextLimits(modelName string) *ModelContextLimits {
	// Try exact match first
	if limits, ok := modelContextLimits[modelName]; ok {
		return &limits
	}

	// Try prefix matching for versioned models
	// e.g., "llama3.1:8b" -> "llama3.1", "claude-3-5-sonnet-20241022" -> "claude-3-5-sonnet"
	// Use longest matching prefix to avoid ambiguity (e.g., "llama3" vs "llama3.1")
	var bestMatch string
	var bestLimits *ModelContextLimits
	for baseModel, limits := range modelContextLimits {
		if len(modelName) >= len(baseModel) && modelName[:len(baseModel)] == baseModel {
			// Keep the longest match
			if len(baseModel) > len(bestMatch) {
				bestMatch = baseModel
				limitsCopy := limits
				bestLimits = &limitsCopy
			}
		}
	}

	return bestLimits
}

// GetProviderDefaultLimits returns sensible defaults for a provider.
// Used when model-specific limits are not available.
func GetProviderDefaultLimits(provider string) ModelContextLimits {
	switch provider {
	case "anthropic":
		return ModelContextLimits{MaxContextTokens: 200000, ReservedOutputTokens: 20000}
	case "bedrock":
		// Bedrock varies by model, but Claude is most common
		return ModelContextLimits{MaxContextTokens: 200000, ReservedOutputTokens: 20000}
	case "ollama":
		// Conservative default for local models
		return ModelContextLimits{MaxContextTokens: 32000, ReservedOutputTokens: 3200}
	case "openai":
		return ModelContextLimits{MaxContextTokens: 128000, ReservedOutputTokens: 12800}
	case "gemini":
		return ModelContextLimits{MaxContextTokens: 1000000, ReservedOutputTokens: 100000}
	case "azureopenai":
		return ModelContextLimits{MaxContextTokens: 128000, ReservedOutputTokens: 12800}
	default:
		// Very conservative fallback
		return ModelContextLimits{MaxContextTokens: 8192, ReservedOutputTokens: 819}
	}
}

// ResolveContextLimits determines the context limits to use, with fallback precedence:
// 1. Explicit configuration (if maxContextTokens > 0)
// 2. Model lookup table
// 3. Provider defaults
// 4. System-wide default (200K for backwards compatibility)
func ResolveContextLimits(provider, model string, configuredMax, configuredReserved int32) ModelContextLimits {
	// If both are explicitly configured, use them
	if configuredMax > 0 && configuredReserved > 0 {
		return ModelContextLimits{
			MaxContextTokens:     int(configuredMax),
			ReservedOutputTokens: int(configuredReserved),
		}
	}

	// If only max is configured, calculate reserved as 10%
	if configuredMax > 0 {
		return ModelContextLimits{
			MaxContextTokens:     int(configuredMax),
			ReservedOutputTokens: int(configuredMax) / 10,
		}
	}

	// Try model-specific limits
	if limits := GetModelContextLimits(model); limits != nil {
		// If reserved is explicitly configured, use it instead of the lookup value
		if configuredReserved > 0 {
			limits.ReservedOutputTokens = int(configuredReserved)
		}
		return *limits
	}

	// Fall back to provider defaults
	limits := GetProviderDefaultLimits(provider)
	if configuredReserved > 0 {
		limits.ReservedOutputTokens = int(configuredReserved)
	}
	return limits
}
