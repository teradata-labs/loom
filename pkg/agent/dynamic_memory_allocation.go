// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"fmt"
	"time"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
	"github.com/teradata-labs/loom/pkg/observability"
)

// DynamicMemoryConfig defines percentage-based memory allocation that adapts
// to any LLM context window size (Ollama 4K, Claude 200K, GPT-4 128K, etc.)
type DynamicMemoryConfig struct {
	// L2 allocation as percentage of available budget (after ROM/Kernel)
	// Small models: 5-8%, Large models: 8-10%
	L2PercentOfAvailable float64

	// L1 target allocation as percentage of available budget
	// This determines how many recent messages to keep
	L1PercentOfAvailable float64

	// Estimated tokens per message exchange (user + assistant)
	// Used to calculate MaxL1Messages from budget
	AvgTokensPerExchange int

	// Profile multipliers (preserve behavioral differences)
	ProfileMultiplier float64 // data_intensive: 0.6, balanced: 1.0, conversational: 1.5
}

// ProfileMultipliers define how each workload type scales memory allocation
var ProfileMultipliers = map[loomv1.WorkloadProfile]float64{
	loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE: 0.6, // Fewer messages, more tool results
	loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED:       1.0, // Baseline
	loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL: 1.5, // More messages, less tool-heavy
}

// CalculateDynamicMemoryAllocation computes memory thresholds based on actual token budget.
// This allows the same code to work with 4K (Ollama), 200K (Claude), or 128K (GPT-4) contexts.
//
// Parameters:
//   - totalBudget: Total context window (e.g., 200000, 8000, 128000)
//   - outputReserve: Tokens reserved for LLM output (typically 10% of total)
//   - romTokens: Measured ROM size (calculated at init)
//   - kernelEstimate: Estimated kernel layer size (tool defs, schemas, findings)
//   - profile: Workload profile for behavioral tuning
//
// Returns:
//   - CompressionProfile with dynamic MaxL1Tokens
//   - maxL2Tokens: Maximum tokens in L2 before eviction to swap
func CalculateDynamicMemoryAllocation(
	totalBudget int,
	outputReserve int,
	romTokens int,
	kernelEstimate int,
	profile loomv1.WorkloadProfile,
) (CompressionProfile, int) {
	// Get base profile for threshold percentages and batch sizes
	baseProfile, exists := ProfileDefaults[profile]
	if !exists {
		baseProfile = ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	}

	// Calculate available budget after reserves
	availableTokens := totalBudget - outputReserve - romTokens - kernelEstimate

	// Handle edge case: very small models (< 4K available)
	if availableTokens < 4000 {
		// For very small models, use static profile but with reduced L2
		adjustedProfile := baseProfile
		adjustedProfile.Name = fmt.Sprintf("%s-dynamic-minimal", baseProfile.Name)
		adjustedProfile.MaxL1Tokens = 1000 // Minimal L1 token budget
		adjustedProfile.MinL1Messages = 2  // Ensure at least 2 messages for recency
		return adjustedProfile, 500        // Minimal L2
	}

	// Get profile multiplier
	multiplier := ProfileMultipliers[profile]
	if multiplier == 0 {
		multiplier = 1.0 // Default to balanced if unknown
	}

	// Calculate dynamic L2 threshold (5-10% of available)
	// Smaller models: 5% (more aggressive eviction)
	// Larger models: 10% (more buffering)
	l2Percent := 0.05 // Start at 5%
	if availableTokens > 50000 {
		l2Percent = 0.08 // Medium models: 8%
	}
	if availableTokens > 100000 {
		l2Percent = 0.10 // Large models: 10%
	}
	maxL2Tokens := int(float64(availableTokens) * l2Percent)

	// Calculate dynamic L1 token budget
	// Target: 40-60% of available budget for recent messages (adaptive)
	l1TargetPercent := 0.40 // 40% for small models
	if availableTokens > 50000 {
		l1TargetPercent = 0.50 // 50% for medium models
	}
	if availableTokens > 100000 {
		l1TargetPercent = 0.60 // 60% for large models
	}

	// Apply profile multiplier to L1 allocation
	maxL1Tokens := int(float64(availableTokens) * l1TargetPercent * multiplier)

	// Enforce sane bounds
	if maxL1Tokens < 1000 {
		maxL1Tokens = 1000 // Absolute minimum (1-2 lightweight messages)
	}
	if maxL1Tokens > 150000 {
		maxL1Tokens = 150000 // Reasonable maximum (even for 200K models)
	}

	// Calculate minL1Messages (minimum messages to keep for recency)
	// Based on available budget
	minL1Messages := 2 // Absolute minimum
	if availableTokens > 10000 {
		minL1Messages = 3
	}
	if availableTokens > 50000 {
		minL1Messages = 4
	}
	if availableTokens > 100000 {
		minL1Messages = 5
	}

	// Create adjusted profile
	adjusted := CompressionProfile{
		Name:                     fmt.Sprintf("%s-dynamic", baseProfile.Name),
		MaxL1Tokens:              maxL1Tokens,
		MinL1Messages:            minL1Messages,
		WarningThresholdPercent:  baseProfile.WarningThresholdPercent,  // Keep profile behavior
		CriticalThresholdPercent: baseProfile.CriticalThresholdPercent, // Keep profile behavior
		NormalBatchSize:          baseProfile.NormalBatchSize,
		WarningBatchSize:         baseProfile.WarningBatchSize,
		CriticalBatchSize:        baseProfile.CriticalBatchSize,
	}

	return adjusted, maxL2Tokens
}

// EstimateKernelSize estimates the kernel layer size based on tool count and schema cache.
// This is a heuristic used for dynamic allocation.
func EstimateKernelSize(toolCount int, maxSchemas int, maxFindings int) int {
	// Rough estimates:
	// - Tool definition: ~200 tokens each
	// - Schema cache entry: ~500 tokens
	// - Finding entry: ~100 tokens
	// - Tool results: ~1000 tokens (max 1-2 recent)

	toolDefTokens := toolCount * 200
	schemaTokens := maxSchemas * 500
	findingsTokens := maxFindings * 100
	toolResultsTokens := 1000 // Conservative estimate

	return toolDefTokens + schemaTokens + findingsTokens + toolResultsTokens
}

// Example allocations for different models (token-based L1):
//
// Claude Sonnet 4.5 (200K context):
//   - Total: 200K
//   - Output Reserve: 20K (10%)
//   - ROM: ~1.75K
//   - Kernel: ~8K (50 tools, 10 schemas, 50 findings)
//   - Available: ~170K
//   - L2 Threshold: ~17K (10%)
//   - L1 (Balanced): ~102K tokens (60% * 170K) = 100+ lightweight OR 10 tool-heavy messages
//   - L1 (Data Intensive): ~61K tokens (36% * 170K) = fewer messages, more tool results
//   - L1 (Conversational): ~153K tokens (90% * 170K, capped at 150K) = many lightweight messages
//
// Ollama Llama2 (8K context):
//   - Total: 8K
//   - Output Reserve: 800 (10%)
//   - ROM: ~1.75K
//   - Kernel: ~2K
//   - Available: ~3.5K
//   - L2 Threshold: ~175 (5%)
//   - L1 (Balanced): ~1.4K tokens (40% * 3.5K) = 5-7 lightweight OR 1-2 tool-heavy messages
//   - L1 (Data Intensive): ~840 tokens (24% * 3.5K) = minimal
//   - L1 (Conversational): ~2.1K tokens (60% * 3.5K) = a few lightweight messages
//
// GPT-4 (128K context):
//   - Total: 128K
//   - Output Reserve: 12.8K (10%)
//   - ROM: ~1.75K
//   - Kernel: ~8K
//   - Available: ~105K
//   - L2 Threshold: ~10.5K (10%)
//   - L1 (Balanced): ~63K tokens (60% * 105K) = 70+ lightweight OR 6-7 tool-heavy messages
//   - L1 (Data Intensive): ~38K tokens (36% * 105K) = fewer messages, more tool results
//   - L1 (Conversational): ~95K tokens (90% * 105K) = many lightweight messages

// NewSegmentedMemoryWithDynamicAllocation creates memory with runtime-calculated thresholds.
// Adapts automatically to any LLM context window size.
//
// This is the RECOMMENDED constructor for new code as it handles:
// - Small models (Ollama 4K-32K)
// - Medium models (GPT-3.5 16K, Claude Instant 100K)
// - Large models (Claude Sonnet 200K, GPT-4 128K)
//
// For backward compatibility, use NewSegmentedMemory() which uses static thresholds.
func NewSegmentedMemoryWithDynamicAllocation(
	romContent string,
	maxContextTokens int,
	reservedOutputTokens int,
	workloadProfile loomv1.WorkloadProfile,
) *SegmentedMemory {
	// Use defaults if not specified
	if maxContextTokens == 0 {
		maxContextTokens = 200000 // Claude Sonnet 4.5 default
	}
	if reservedOutputTokens == 0 {
		reservedOutputTokens = maxContextTokens / 10 // 10% of total
	}

	// Measure ROM size
	tokenCounter := GetTokenCounter()
	romTokens := tokenCounter.CountTokens(romContent)

	// Estimate kernel size (will be refined at runtime)
	// Conservative estimate: 50 tools, 10 schemas, 50 findings
	kernelEstimate := EstimateKernelSize(50, 10, 50)

	// Calculate dynamic memory allocation
	profile, maxL2Tokens := CalculateDynamicMemoryAllocation(
		maxContextTokens,
		reservedOutputTokens,
		romTokens,
		kernelEstimate,
		workloadProfile,
	)

	// Initialize token budget
	tokenBudget := NewTokenBudget(maxContextTokens, reservedOutputTokens)

	sm := &SegmentedMemory{
		romContent:         romContent,
		tools:              make([]string, 0),
		toolResults:        make([]CachedToolResult, 0),
		schemaCache:        make(map[string]string),
		schemaAccessLog:    make(map[string]time.Time),
		maxSchemas:         10,
		findingsCache:      make(map[string]Finding),
		maxFindings:        50,
		l1Messages:         make([]Message, 0),
		promotedContext:    make([]Message, 0),
		sessionStore:       nil,
		sessionID:          "",
		swapEnabled:        false,
		maxL2Tokens:        maxL2Tokens, // DYNAMIC based on available budget
		swapEvictionCount:  0,
		swapRetrievalCount: 0,
		tokenCounter:       tokenCounter,
		tokenBudget:        tokenBudget,
		compressor:         nil,
		tracer:             observability.NewNoOpTracer(),
		maxL1Tokens:        profile.MaxL1Tokens,   // DYNAMIC based on available budget
		minL1Messages:      profile.MinL1Messages, // DYNAMIC based on available budget
		maxToolResults:     1,
		compressionProfile: profile,
	}

	return sm
}
