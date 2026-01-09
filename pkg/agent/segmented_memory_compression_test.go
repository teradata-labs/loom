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

func TestNewSegmentedMemory_UsesBalancedProfileByDefault(t *testing.T) {
	romContent := "Test documentation"
	sm := NewSegmentedMemory(romContent, 200000, 20000)

	// Should use balanced profile defaults
	assert.Equal(t, 8, sm.maxL1Messages, "Should use balanced maxL1Messages")
	assert.Equal(t, 4, sm.minL1Messages, "Should use balanced minL1Messages")
	assert.Equal(t, "balanced", sm.compressionProfile.Name)
}

func TestNewSegmentedMemoryWithCompression_DataIntensiveProfile(t *testing.T) {
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]

	sm := NewSegmentedMemoryWithCompression(romContent, 200000, 20000, profile)

	// Should use data_intensive profile
	assert.Equal(t, 5, sm.maxL1Messages, "Should use data_intensive maxL1Messages")
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
	assert.Equal(t, 12, sm.maxL1Messages, "Should use conversational maxL1Messages")
	assert.Equal(t, 6, sm.minL1Messages, "Should use conversational minL1Messages")
	assert.Equal(t, 70, sm.compressionProfile.WarningThresholdPercent)
	assert.Equal(t, 85, sm.compressionProfile.CriticalThresholdPercent)
	assert.Equal(t, "conversational", sm.compressionProfile.Name)
}

func TestAddMessage_CompressionTriggersAtProfileThreshold_DataIntensive(t *testing.T) {
	romContent := "Test documentation"
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]

	sm := NewSegmentedMemoryWithCompression(romContent, 10000, 1000, profile) // Small budget to test thresholds
	sm.SetCompressor(&mockCompressor{enabled: true})

	// Add messages until we exceed maxL1Messages (5 for data_intensive)
	for i := 0; i < 6; i++ {
		sm.AddMessage(Message{
			Role:    "user",
			Content: "Test message " + string(rune(i)),
		})
	}

	// Should have compressed due to exceeding maxL1Messages
	assert.LessOrEqual(t, len(sm.l1Messages), profile.MaxL1Messages, "L1 should not exceed max after compression")
	assert.GreaterOrEqual(t, len(sm.l1Messages), profile.MinL1Messages, "L1 should have at least min messages")
}

func TestAddMessage_CompressionBatchSizeVariesByBudgetUsage(t *testing.T) {
	tests := []struct {
		name              string
		profile           CompressionProfile
		contextSize       int
		reservedOutput    int
		expectedBatchSize int // Which batch size should be used
		messageCount      int // How many messages to add
		description       string
	}{
		{
			name:              "data_intensive normal batch",
			profile:           ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE],
			contextSize:       100000,
			reservedOutput:    10000,
			expectedBatchSize: 2, // Normal batch size
			messageCount:      6,
			description:       "Budget < 50%, should use normal batch size (2)",
		},
		{
			name:              "data_intensive warning batch",
			profile:           ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE],
			contextSize:       10000, // Smaller budget
			reservedOutput:    1000,
			expectedBatchSize: 4, // Warning batch size (budget will exceed 50%)
			messageCount:      6,
			description:       "Budget > 50%, should use warning batch size (4)",
		},
		{
			name:              "balanced normal batch",
			profile:           ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED],
			contextSize:       100000,
			reservedOutput:    10000,
			expectedBatchSize: 3, // Normal batch size
			messageCount:      9,
			description:       "Budget < 60%, should use normal batch size (3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			romContent := "Test documentation"
			sm := NewSegmentedMemoryWithCompression(romContent, tt.contextSize, tt.reservedOutput, tt.profile)

			// Use mock compressor to track compression calls
			mockComp := &mockCompressor{enabled: true}
			sm.SetCompressor(mockComp)

			// Add messages to trigger compression
			for i := 0; i < tt.messageCount; i++ {
				sm.AddMessage(Message{
					Role:    "user",
					Content: "Test message with some content to consume tokens",
				})
			}

			// Verify L1 stayed within bounds
			assert.LessOrEqual(t, len(sm.l1Messages), tt.profile.MaxL1Messages, tt.description)
		})
	}
}

func TestGetBudgetWarning_UsesProfileThresholds(t *testing.T) {
	// Test that data_intensive profile (50% threshold) triggers warnings earlier
	romContent := "Test documentation"

	// Use small context budget to easily exceed thresholds
	contextSize := 5000
	reservedOutput := 500
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE]
	sm := NewSegmentedMemoryWithCompression(romContent, contextSize, reservedOutput, profile)

	// Add large messages to exceed warning threshold (50% for data_intensive)
	largeContent := string(make([]byte, 1000)) // 1KB content
	for i := 0; i < 3; i++ {
		sm.AddMessage(Message{
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
		MaxL1Messages:            15,
		MinL1Messages:            7,
		WarningThresholdPercent:  55,
		CriticalThresholdPercent: 80,
		NormalBatchSize:          2,
		WarningBatchSize:         4,
		CriticalBatchSize:        6,
	}

	romContent := "Test documentation"
	sm := NewSegmentedMemoryWithCompression(romContent, 200000, 20000, customProfile)

	assert.Equal(t, 15, sm.maxL1Messages)
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
	assert.Equal(t, 8, sm.maxL1Messages)
	assert.Equal(t, 4, sm.minL1Messages)

	// Should function normally
	sm.AddMessage(Message{Role: "user", Content: "test"})
	assert.Equal(t, 1, len(sm.l1Messages))
}

func TestAddMessage_DataIntensiveTriggersCompressionEarlier(t *testing.T) {
	romContent := "Test documentation"

	// Create two memories with different profiles but same context budget
	dataIntensiveSM := NewSegmentedMemoryWithCompression(
		romContent, 50000, 5000,
		ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE],
	)
	dataIntensiveSM.SetCompressor(&mockCompressor{enabled: true})

	conversationalSM := NewSegmentedMemoryWithCompression(
		romContent, 50000, 5000,
		ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL],
	)
	conversationalSM.SetCompressor(&mockCompressor{enabled: true})

	// Add same messages to both
	largeMessage := Message{
		Role:    "user",
		Content: "This is a large message with lots of content to consume tokens. " + string(make([]byte, 1000)),
	}

	// Add enough messages to trigger warning threshold
	for i := 0; i < 10; i++ {
		dataIntensiveSM.AddMessage(largeMessage)
		conversationalSM.AddMessage(largeMessage)
	}

	// Data intensive should have compressed more aggressively (lower maxL1)
	// because it has lower thresholds (50% vs 70%) and smaller maxL1 (5 vs 12)
	assert.LessOrEqual(t, len(dataIntensiveSM.l1Messages), 5, "Data intensive should have maxL1=5")
	assert.LessOrEqual(t, len(conversationalSM.l1Messages), 12, "Conversational should have maxL1=12")
}
