// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

func TestNewSegmentedMemory_UsesBalancedProfileByDefault(t *testing.T) {
	romContent := "Test documentation"
	sm := NewSegmentedMemory(romContent, 200000, 20000)

	// Should use balanced profile defaults
	assert.Equal(t, 6400, sm.compressionProfile.MaxL1Tokens, "Should use balanced MaxL1Tokens (6400 tokens)")
	assert.Equal(t, 4, sm.minL1Messages, "Should use balanced minL1Messages")
	assert.Equal(t, "balanced", sm.compressionProfile.Name)
}

func TestNewSegmentedMemoryWithCompression_DataIntensiveProfile(t *testing.T) {
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]

	sm := NewSegmentedMemoryWithCompression(romContent, 200000, 20000, profile)

	// Should use data_intensive profile
	assert.Equal(t, 4000, sm.compressionProfile.MaxL1Tokens, "Should use data_intensive MaxL1Tokens (4000 tokens)")
	assert.Equal(t, 3, sm.minL1Messages, "Should use data_intensive minL1Messages")
	assert.Equal(t, 50, sm.compressionProfile.WarningThresholdPercent)
	assert.Equal(t, 70, sm.compressionProfile.CriticalThresholdPercent)
	assert.Equal(t, "data_intensive", sm.compressionProfile.Name)
}

func TestNewSegmentedMemoryWithCompression_ConversationalProfile(t *testing.T) {
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL]

	sm := NewSegmentedMemoryWithCompression(romContent, 200000, 20000, profile)

	// Should use conversational profile
	assert.Equal(t, 9600, sm.compressionProfile.MaxL1Tokens, "Should use conversational MaxL1Tokens (9600 tokens)")
	assert.Equal(t, 6, sm.minL1Messages, "Should use conversational minL1Messages")
	assert.Equal(t, 70, sm.compressionProfile.WarningThresholdPercent)
	assert.Equal(t, 85, sm.compressionProfile.CriticalThresholdPercent)
	assert.Equal(t, "conversational", sm.compressionProfile.Name)
}

func TestGetBudgetWarning_UsesProfileThresholds(t *testing.T) {
	// Test that data_intensive profile (50% threshold) triggers warnings earlier
	romContent := "Test documentation"

	// Use small context budget to easily exceed thresholds
	contextSize := 5000
	reservedOutput := 500
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]
	sm := NewSegmentedMemoryWithCompression(romContent, contextSize, reservedOutput, profile)

	ctx := context.Background()

	// Add large messages to exceed warning threshold (50% for data_intensive)
	largeContent := string(make([]byte, 1000)) // 1KB content
	for i := 0; i < 3; i++ {
		sm.AddMessage(ctx, Message{
			Role:    "user",
			Content: "Large message: " + largeContent,
		})
	}

	// Get warning message
	warning := sm.getBudgetWarning()

	// With data_intensive profile, should see INFO or WARNING about token budget
	// (the exact threshold depends on actual token count)
	assert.Contains(t, warning, "Token budget", "Should show token budget warning")
}

func TestSegmentedMemory_CustomProfile(t *testing.T) {
	// Create a custom profile with specific values
	customProfile := CompressionProfile{
		Name:                     "custom",
		MaxL1Tokens:              12000, // ~15 messages worth
		MinL1Messages:            7,
		WarningThresholdPercent:  55,
		CriticalThresholdPercent: 80,
		NormalBatchSize:          2,
		WarningBatchSize:         4,
		CriticalBatchSize:        6,
	}

	romContent := "Test documentation"
	sm := NewSegmentedMemoryWithCompression(romContent, 200000, 20000, customProfile)

	assert.Equal(t, 12000, sm.compressionProfile.MaxL1Tokens, "Should use custom profile's MaxL1Tokens")
	assert.Equal(t, 7, sm.minL1Messages)
	assert.Equal(t, 55, sm.compressionProfile.WarningThresholdPercent)
	assert.Equal(t, 80, sm.compressionProfile.CriticalThresholdPercent)
}

func TestSegmentedMemory_BackwardsCompatibility(t *testing.T) {
	// Old code using NewSegmentedMemory should still work
	romContent := "Test documentation"
	sm := NewSegmentedMemory(romContent, 200000, 20000)

	// Should get balanced profile by default
	assert.NotNil(t, sm.compressionProfile)
	assert.Equal(t, "balanced", sm.compressionProfile.Name)
	assert.Equal(t, 6400, sm.compressionProfile.MaxL1Tokens, "Should use balanced MaxL1Tokens (6400 tokens)")
	assert.Equal(t, 4, sm.minL1Messages)

	// Should function normally
	sm.AddMessage(context.Background(), Message{Role: "user", Content: "test"})
	assert.Equal(t, 1, len(sm.l1Messages))
}
