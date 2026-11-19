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

import (
	"github.com/teradata-labs/loom/pkg/llm"
	"github.com/teradata-labs/loom/pkg/llm/catalog"
)

// ModelContextLimits defines the context window and output reservation for a model
type ModelContextLimits struct {
	MaxContextTokens     int // Total context window size
	ReservedOutputTokens int // Tokens reserved for model output (typically 10%)
}

// modelContextLimits is the legacy prefix-matched fallback used when a model
// ID is not in the built-in catalog (pkg/llm/catalog). Prefer adding new
// entries to the catalog — the catalog is authoritative, this map is only
// consulted after an exact-match miss.
//
// Claude 4.x entries intentionally omitted: every current Claude 4.x model is
// in the catalog with accurate per-model ContextWindow and MaxOutputTokens.
// Leaving a "claude-opus-4" prefix entry here would silently override real
// catalog values (e.g. Opus 4.6/4.7 at 1M) with stale 200K numbers for any
// unqualified or future variant not yet in the catalog.
var modelContextLimits = map[string]ModelContextLimits{
	// Anthropic Claude 3.x — pre-catalog, kept for prefix-match fallback.
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

	// Google Gemini models
	"gemini-3-pro-preview":   {MaxContextTokens: 1048576, ReservedOutputTokens: 65536},
	"gemini-3-flash-preview": {MaxContextTokens: 1048576, ReservedOutputTokens: 65536},
	"gemini-2.5-pro":         {MaxContextTokens: 1048576, ReservedOutputTokens: 65536},
	"gemini-2.5-flash":       {MaxContextTokens: 1048576, ReservedOutputTokens: 65536},
	"gemini-2.5-flash-lite":  {MaxContextTokens: 1048576, ReservedOutputTokens: 65536},
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
	// Use longest matching prefix to avoid ambiguity (e.g., "llama3" vs "llama3.1").
	// The prefix must end at a separator: "gpt-4" names "gpt-4" and "gpt-4-0613",
	// not "gpt-4o", which is another model with another window.
	var bestMatch string
	var bestLimits *ModelContextLimits
	for baseModel, limits := range modelContextLimits {
		if len(modelName) >= len(baseModel) && modelName[:len(baseModel)] == baseModel && prefixEndsAtSeparator(modelName, len(baseModel)) {
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

// prefixEndsAtSeparator reports whether modelName[at:] is empty or starts a
// new segment (':' tag, '-' or '.' version, '@' or '/' qualifier).
func prefixEndsAtSeparator(modelName string, at int) bool {
	if at >= len(modelName) {
		return true
	}
	switch modelName[at] {
	case ':', '-', '.', '@', '/':
		return true
	}
	return false
}

// GetProviderDefaultLimits returns sensible defaults for a provider.
// Used when model-specific limits are not available.
func GetProviderDefaultLimits(provider string) ModelContextLimits {
	switch provider {
	case "anthropic":
		return ModelContextLimits{MaxContextTokens: 200000, ReservedOutputTokens: 20000}
	case "bedrock", "bedrock-mantle":
		// Bedrock varies by model, but Claude is most common (on both endpoints)
		return ModelContextLimits{MaxContextTokens: 200000, ReservedOutputTokens: 20000}
	case "ollama":
		// Conservative default for local models
		return ModelContextLimits{MaxContextTokens: 32000, ReservedOutputTokens: 3200}
	case "openai":
		return ModelContextLimits{MaxContextTokens: 128000, ReservedOutputTokens: 12800}
	case "gemini":
		return ModelContextLimits{MaxContextTokens: 1048576, ReservedOutputTokens: 65536}
	case "azureopenai":
		return ModelContextLimits{MaxContextTokens: 128000, ReservedOutputTokens: 12800}
	case "vercel-ai-gateway", "vercel":
		// Used only by ResolveContextLimits, when the gateway catalog does
		// not list the model. ResolveContextLimits has no production caller:
		// an agent's fold budget comes from gatewayBudgetWindow, which keeps
		// the memory default (defaultContextWindowTokens) on a catalog miss.
		// The gateway fronts many families; 128K is the common floor of the
		// current ones (Claude 200K+, GPT-5 400K, Gemini 1M).
		return ModelContextLimits{MaxContextTokens: 128000, ReservedOutputTokens: 12800}
	default:
		// Very conservative fallback
		return ModelContextLimits{MaxContextTokens: 8192, ReservedOutputTokens: 819}
	}
}

// EffectiveOutputReservation returns the token count the relief water marks
// must subtract from the window (HLD §5.1 "usable"). The provider accepts
// exactly when prompt + max_tokens ≤ window, so the reservation has to cover
// the request's REAL max_tokens — otherwise usable is over-stated and the
// marks sit above the provider's refusal line, where proactive relief can
// never fire.
//
// The reservation covers the max_tokens that goes on the wire:
//
//  1. the configured reserve (llm.reserved_output_tokens), or 10% of the
//     window when unset — the historical default;
//  2. llm.max_tokens, when explicitly configured — sent verbatim by every
//     build path, so it is what is reserved;
//  3. the model's catalog MaxOutputTokens, only when llm.max_tokens is unset —
//     what the factory then sends (factory.resolveMaxOutput). With max_tokens
//     set the catalog figure is not sent, and reserving it would take room
//     from the prompt that the provider accepts.
//
// Returns 0 when nothing is known, so callers keep their existing defaults.
func EffectiveOutputReservation(provider, model string, configuredMaxTokens, configuredReserved, maxContextTokens int) int {
	reservation := configuredReserved
	if reservation <= 0 && maxContextTokens > 0 {
		reservation = maxContextTokens / 10
	}
	if configuredMaxTokens > reservation {
		reservation = configuredMaxTokens
	}
	if configuredMaxTokens <= 0 {
		if info := catalog.Lookup(provider, model); info != nil && int(info.MaxOutputTokens) > reservation {
			reservation = int(info.MaxOutputTokens)
		}
	}
	// The reserve must leave a working budget: never claim more than half the
	// window (the SegmentedMemory constructor applies the same cap).
	if maxContextTokens > 0 && reservation >= maxContextTokens {
		reservation = maxContextTokens / 2
	}
	return reservation
}

// ProviderPromptCeiling is the largest prompt the provider accepts for
// provider+model: the window the model is served with, less the output
// reservation. servedWindow (max_model_context_tokens) states that window
// when the server caps it below the model's listing — a vLLM --max-model-len,
// a TGI --max-total-tokens — and comes first. Otherwise the model's listed
// window: the catalog's, else the prefix-matched table's. The configured
// max_context_tokens is where loom folds, not where the provider refuses, so
// it is used only when neither knows the model. No provider is named: a
// server that caps its window (an Ollama num_ctx included) states it in
// servedWindow.
// reserved is the reply reservation as the agent's builder resolved it
// (max_tokens included, EffectiveOutputReservation) and is taken as given;
// only when none was resolved is one derived from the listing. Returns 0 when
// no window is known.
func ProviderPromptCeiling(provider, model string, reserved, configuredWindow, servedWindow int) int {
	window := 0
	if servedWindow > 0 {
		window = servedWindow
	} else if info := catalog.Lookup(provider, model); info != nil && info.ContextWindow > 0 {
		window = int(info.ContextWindow)
	} else if limits := GetModelContextLimits(model); limits != nil {
		window = limits.MaxContextTokens
	}
	if window <= 0 {
		window = configuredWindow
	}
	if window <= 0 {
		return 0
	}
	if reserved <= 0 {
		reserved = EffectiveOutputReservation(provider, model, 0, 0, window)
	}
	if reserved >= window {
		reserved = window / 2
	}
	return window - reserved
}

// budgetWindowSource names where gatewayBudgetWindow took the fold window
// from. applyGatewayBudgetWindow logs it.
type budgetWindowSource string

const (
	// budgetWindowConfigured: llm.max_context_tokens is set; it is used as is.
	budgetWindowConfigured budgetWindowSource = "max_context_tokens"
	// budgetWindowNotGateway: the provider is not the Vercel AI Gateway; the
	// memory's default window applies, as before.
	budgetWindowNotGateway budgetWindowSource = "not_gateway"
	// budgetWindowServed: llm.max_model_context_tokens is set; the window the
	// model is served with wins over the catalog listing.
	budgetWindowServed budgetWindowSource = "max_model_context_tokens"
	// budgetWindowCatalog: the gateway catalog (GET /v1/models) lists the
	// model's context_window below the memory default.
	budgetWindowCatalog budgetWindowSource = "gateway_catalog"
	// budgetWindowCatalogNotSmaller: the catalog lists a window at or above
	// the memory default; the default applies, as before.
	budgetWindowCatalogNotSmaller budgetWindowSource = "gateway_catalog_not_smaller"
	// budgetWindowCatalogMiss: no catalog entry for the model (the fetch
	// failed, has not landed, or does not list it); the memory's default
	// window applies, as before.
	budgetWindowCatalogMiss budgetWindowSource = "catalog_miss"
)

// gatewayBudgetWindow returns the window the fold budgets against for an
// agent whose LLM is the Vercel AI Gateway, and where it came from. The
// gateway fronts models whose windows differ widely, so the memory's single
// default (defaultContextWindowTokens) over-states a smaller model's window
// and lets the conversation grow past it.
//
// The order is the one ProviderPromptCeiling uses for the provider's window,
// with the operator's settings first:
//
//  1. configured (llm.max_context_tokens), when > 0;
//  2. served (llm.max_model_context_tokens), when > 0;
//  3. the catalog's ContextWindow for provider+model, only when it is below
//     the memory default.
//
// The catalog step only lowers the budget. Many gateway models list a
// window above 200K (Claude Sonnet 4.x and Gemini at 1M, GPT-5 at 400K), but
// whether the gateway serves that window without a provider beta header is
// not verified, and a larger budget makes long sessions send more input per
// call. An operator who wants the larger window sets max_context_tokens.
//
// It returns window 0 when the provider is not the gateway, when the catalog
// has no entry, or when the catalog's window is not below the default, so
// the caller keeps today's default. NewAgent calls it through
// applyGatewayBudgetWindow. Other providers are unchanged: their budget
// default is a separate decision.
func gatewayBudgetWindow(provider, model string, configured, served int) (int, budgetWindowSource) {
	if configured > 0 {
		return configured, budgetWindowConfigured
	}
	if !llm.IsVercelAIGateway(provider) {
		return 0, budgetWindowNotGateway
	}
	if served > 0 {
		return served, budgetWindowServed
	}
	info := catalog.Lookup(llm.NormalizeProviderName(provider), model)
	if info == nil || info.ContextWindow <= 0 {
		return 0, budgetWindowCatalogMiss
	}
	if int(info.ContextWindow) >= defaultContextWindowTokens {
		return 0, budgetWindowCatalogNotSmaller
	}
	return int(info.ContextWindow), budgetWindowCatalog
}

// ResolveContextLimits determines the context limits to use, with fallback precedence:
//  1. Explicit configuration (if configuredMax > 0)
//  2. Built-in catalog entry for provider+model (authoritative ContextWindow /
//     MaxOutputTokens from pkg/llm/catalog)
//  3. Legacy prefix-matched lookup table (covers Ollama tags and pre-catalog
//     model name variants)
//  4. Provider defaults
//  5. System-wide conservative fallback
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

	// Built-in catalog is the source of truth for known provider+model pairs.
	// Reserve the model's documented MaxOutputTokens for output unless the
	// caller has supplied an explicit override.
	if info := catalog.Lookup(provider, model); info != nil && info.ContextWindow > 0 {
		reserved := int(info.MaxOutputTokens)
		if configuredReserved > 0 {
			reserved = int(configuredReserved)
		}
		return ModelContextLimits{
			MaxContextTokens:     int(info.ContextWindow),
			ReservedOutputTokens: reserved,
		}
	}

	// Legacy prefix-matched lookup for model names not in the catalog
	// (e.g. Ollama tags like "llama3.1:8b", older Claude 3.x aliases).
	if limits := GetModelContextLimits(model); limits != nil {
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
