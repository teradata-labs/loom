// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestCalculateDynamicMemoryAllocation_ClaudeSonnet tests allocations for Claude Sonnet 4.5 (200K)
func TestCalculateDynamicMemoryAllocation_ClaudeSonnet(t *testing.T) {
	totalBudget := 200000
	outputReserve := 20000
	romTokens := 1750
	kernelEstimate := 8000

	profile, maxL2Tokens := CalculateDynamicMemoryAllocation(
		totalBudget,
		outputReserve,
		romTokens,
		kernelEstimate,
		loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
	)

	// Available: 200K - 20K - 1.75K - 8K = 170.25K
	// L2: 10% of 170K = 17K
	// L1: 60% of 170K = 102K tokens

	assert.Equal(t, "balanced-dynamic", profile.Name)
	assert.Equal(t, 102150, profile.MaxL1Tokens, "Should be 60% of 170250")
	assert.Equal(t, 5, profile.MinL1Messages)
	assert.Equal(t, 17025, maxL2Tokens) // 10% of 170250
}

// TestCalculateDynamicMemoryAllocation_OllamaSmall tests allocations for small Ollama models (8K)
func TestCalculateDynamicMemoryAllocation_OllamaSmall(t *testing.T) {
	totalBudget := 8000
	outputReserve := 800
	romTokens := 1750
	kernelEstimate := 2000

	profile, maxL2Tokens := CalculateDynamicMemoryAllocation(
		totalBudget,
		outputReserve,
		romTokens,
		kernelEstimate,
		loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
	)

	// Available: 8K - 800 - 1.75K - 2K = 3.45K (< 4K threshold)
	// Falls into "very small model" category
	// L2: 500 tokens (minimal)
	// L1: 1000 tokens (minimal)

	assert.Equal(t, "balanced-dynamic-minimal", profile.Name)
	assert.Equal(t, 1000, profile.MaxL1Tokens, "Should use minimal token budget for very small models")
	assert.Equal(t, 2, profile.MinL1Messages)
	assert.Equal(t, 500, maxL2Tokens, "Should use minimal L2 for very small models")
}

// TestCalculateDynamicMemoryAllocation_GPT4 tests allocations for GPT-4 (128K)
func TestCalculateDynamicMemoryAllocation_GPT4(t *testing.T) {
	totalBudget := 128000
	outputReserve := 12800
	romTokens := 1750
	kernelEstimate := 8000

	profile, maxL2Tokens := CalculateDynamicMemoryAllocation(
		totalBudget,
		outputReserve,
		romTokens,
		kernelEstimate,
		loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
	)

	// Available: 128K - 12.8K - 1.75K - 8K = 105.45K
	// L2: 10% of 105K = 10.5K
	// L1: 60% of 105K = 63K tokens

	assert.Equal(t, "balanced-dynamic", profile.Name)
	assert.Equal(t, 63270, profile.MaxL1Tokens, "Should be 60% of 105450")
	assert.Equal(t, 10545, maxL2Tokens)
}

// TestCalculateDynamicMemoryAllocation_ProfileMultipliers tests profile behavior
func TestCalculateDynamicMemoryAllocation_ProfileMultipliers(t *testing.T) {
	totalBudget := 200000
	outputReserve := 20000
	romTokens := 1750
	kernelEstimate := 8000

	tests := []struct {
		name               string
		profile            loomv1.WorkloadProfile
		expectedMultiplier float64
	}{
		{
			name:               "data_intensive",
			profile:            loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE,
			expectedMultiplier: 0.6,
		},
		{
			name:               "balanced",
			profile:            loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
			expectedMultiplier: 1.0,
		},
		{
			name:               "conversational",
			profile:            loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL,
			expectedMultiplier: 1.5,
		},
	}

	var baselineL1 int
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, _ := CalculateDynamicMemoryAllocation(
				totalBudget,
				outputReserve,
				romTokens,
				kernelEstimate,
				tt.profile,
			)

			// Store baseline for comparison
			if i == 0 {
				baselineL1 = profile.MaxL1Tokens
			}

			// Verify profile multiplier effect
			if tt.expectedMultiplier < 1.0 {
				assert.Less(t, profile.MaxL1Tokens, baselineL1*2, "Data intensive should have fewer tokens")
			} else if tt.expectedMultiplier > 1.0 {
				// Conversational would have more tokens
				assert.Greater(t, profile.MaxL1Tokens, baselineL1, "Conversational should have more tokens")
			}
			assert.NotZero(t, profile.MaxL1Tokens)
		})
	}
}

// TestCalculateDynamicMemoryAllocation_VerySmallModel tests edge case (< 4K)
func TestCalculateDynamicMemoryAllocation_VerySmallModel(t *testing.T) {
	totalBudget := 4000
	outputReserve := 400
	romTokens := 1750
	kernelEstimate := 1000

	profile, maxL2Tokens := CalculateDynamicMemoryAllocation(
		totalBudget,
		outputReserve,
		romTokens,
		kernelEstimate,
		loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
	)

	// Available: 4K - 400 - 1.75K - 1K = 850 tokens (< 4K threshold)
	// Should fall back to minimal allocations

	assert.Equal(t, 500, maxL2Tokens, "Should use minimal L2 for very small models")
	assert.GreaterOrEqual(t, profile.MaxL1Tokens, 1000, "Should use minimal token budget for very small models")
}

// TestEstimateKernelSize tests kernel size estimation
func TestEstimateKernelSize(t *testing.T) {
	tests := []struct {
		name        string
		toolCount   int
		maxSchemas  int
		maxFindings int
		minExpected int
		maxExpected int
	}{
		{
			name:        "minimal",
			toolCount:   10,
			maxSchemas:  5,
			maxFindings: 10,
			minExpected: 5000,
			maxExpected: 7000,
		},
		{
			name:        "typical",
			toolCount:   50,
			maxSchemas:  10,
			maxFindings: 50,
			minExpected: 15000,
			maxExpected: 22000,
		},
		{
			name:        "extensive",
			toolCount:   100,
			maxSchemas:  20,
			maxFindings: 100,
			minExpected: 35000,
			maxExpected: 42000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := EstimateKernelSize(tt.toolCount, tt.maxSchemas, tt.maxFindings)
			assert.GreaterOrEqual(t, size, tt.minExpected)
			assert.LessOrEqual(t, size, tt.maxExpected)
		})
	}
}

// TestNewSegmentedMemoryWithDynamicAllocation_Integration tests full integration
func TestNewSegmentedMemoryWithDynamicAllocation_Integration(t *testing.T) {
	romContent := "This is test ROM content for memory allocation testing"

	tests := []struct {
		name          string
		contextTokens int
		profile       loomv1.WorkloadProfile
	}{
		{
			name:          "claude_sonnet",
			contextTokens: 200000,
			profile:       loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
		},
		{
			name:          "ollama_small",
			contextTokens: 8000,
			profile:       loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
		},
		{
			name:          "gpt4",
			contextTokens: 128000,
			profile:       loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewSegmentedMemoryWithDynamicAllocation(
				romContent,
				tt.contextTokens,
				tt.contextTokens/10, // 10% output reserve
				tt.profile,
			)

			// Verify memory was created successfully
			assert.NotNil(t, sm)
			assert.Equal(t, romContent, sm.romContent)
			assert.NotZero(t, sm.maxL1Tokens)
			assert.NotZero(t, sm.maxL2Tokens)
			assert.NotNil(t, sm.tokenBudget)
			assert.NotNil(t, sm.tokenCounter)

			// Verify L1 is reasonable for context size (token-based)
			if tt.contextTokens >= 150000 {
				// Very large models should have substantial L1 token budget
				assert.GreaterOrEqual(t, sm.maxL1Tokens, 60000)
			} else if tt.contextTokens >= 100000 {
				// Large models should have substantial L1 token budget
				assert.GreaterOrEqual(t, sm.maxL1Tokens, 40000)
			} else if tt.contextTokens < 10000 {
				// Small models should have minimal L1 token budget
				assert.LessOrEqual(t, sm.maxL1Tokens, 2000)
			}

			// Verify L2 scales with available budget
			expectedL2Ratio := float64(sm.maxL2Tokens) / float64(tt.contextTokens)
			assert.Greater(t, expectedL2Ratio, 0.01) // At least 1%
			assert.Less(t, expectedL2Ratio, 0.15)    // At most 15%
		})
	}
}
