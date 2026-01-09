// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// TestCompressionProfile_EndToEnd_DataIntensive verifies the full flow:
// YAML config → proto → ResolveCompressionProfile → Agent → SegmentedMemory
func TestCompressionProfile_EndToEnd_DataIntensive(t *testing.T) {
	// Step 1: Load config from YAML (simulating user's agent config file)
	yamlConfig := `
agent:
  name: sql-agent-data-intensive
  description: SQL agent optimized for large result sets
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    memory_compression:
      workload_profile: data_intensive
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)
	require.NotNil(t, config.Memory.MemoryCompression)
	assert.Equal(t, loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE, config.Memory.MemoryCompression.WorkloadProfile)

	// Step 2: Resolve compression profile from proto config
	profile, err := ResolveCompressionProfile(config.Memory.MemoryCompression)
	require.NoError(t, err)

	// Verify profile has data_intensive defaults
	assert.Equal(t, "data_intensive", profile.Name)
	assert.Equal(t, 5, profile.MaxL1Messages)
	assert.Equal(t, 3, profile.MinL1Messages)
	assert.Equal(t, 50, profile.WarningThresholdPercent)
	assert.Equal(t, 70, profile.CriticalThresholdPercent)

	// Step 3: Create agent with compression profile
	agent := createTestAgentWithProfile(profile)
	require.NotNil(t, agent)

	// Step 4: Create session and verify SegmentedMemory uses the profile
	session := agent.memory.GetOrCreateSession("test-session")
	require.NotNil(t, session)
	require.NotNil(t, session.SegmentedMem)

	segMem, ok := session.SegmentedMem.(*SegmentedMemory)
	require.True(t, ok, "Should be SegmentedMemory")

	// Verify SegmentedMemory is configured with data_intensive profile values
	assert.Equal(t, 5, segMem.maxL1Messages, "Should use data_intensive maxL1Messages")
	assert.Equal(t, 3, segMem.minL1Messages, "Should use data_intensive minL1Messages")
	assert.Equal(t, "data_intensive", segMem.compressionProfile.Name)
	assert.Equal(t, 50, segMem.compressionProfile.WarningThresholdPercent)
	assert.Equal(t, 70, segMem.compressionProfile.CriticalThresholdPercent)
}

// TestCompressionProfile_EndToEnd_Conversational verifies conversational profile
func TestCompressionProfile_EndToEnd_Conversational(t *testing.T) {
	yamlConfig := `
agent:
  name: chat-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    memory_compression:
      workload_profile: conversational
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	profile, err := ResolveCompressionProfile(config.Memory.MemoryCompression)
	require.NoError(t, err)

	assert.Equal(t, "conversational", profile.Name)
	assert.Equal(t, 12, profile.MaxL1Messages)
	assert.Equal(t, 6, profile.MinL1Messages)
	assert.Equal(t, 70, profile.WarningThresholdPercent)
	assert.Equal(t, 85, profile.CriticalThresholdPercent)

	// Create agent and verify memory uses conversational profile
	agent := createTestAgentWithProfile(profile)
	session := agent.memory.GetOrCreateSession("test-session")
	segMem := session.SegmentedMem.(*SegmentedMemory)

	assert.Equal(t, 12, segMem.maxL1Messages)
	assert.Equal(t, 6, segMem.minL1Messages)
}

// TestCompressionProfile_EndToEnd_CustomOverrides verifies custom overrides work
func TestCompressionProfile_EndToEnd_CustomOverrides(t *testing.T) {
	yamlConfig := `
agent:
  name: custom-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    memory_compression:
      workload_profile: balanced
      max_l1_messages: 15
      warning_threshold_percent: 55
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	profile, err := ResolveCompressionProfile(config.Memory.MemoryCompression)
	require.NoError(t, err)

	// Verify overrides took effect
	assert.Equal(t, 15, profile.MaxL1Messages, "Custom maxL1Messages should override balanced default")
	assert.Equal(t, 55, profile.WarningThresholdPercent, "Custom warning threshold should override balanced default")
	// Non-overridden values should use balanced defaults
	assert.Equal(t, 4, profile.MinL1Messages, "Should use balanced default minL1Messages")
	assert.Equal(t, 75, profile.CriticalThresholdPercent, "Should use balanced default critical threshold")

	agent := createTestAgentWithProfile(profile)
	session := agent.memory.GetOrCreateSession("test-session")
	segMem := session.SegmentedMem.(*SegmentedMemory)

	assert.Equal(t, 15, segMem.maxL1Messages)
	assert.Equal(t, 4, segMem.minL1Messages)
}

// TestCompressionProfile_EndToEnd_NoProfileUsesDefaults verifies backward compatibility
func TestCompressionProfile_EndToEnd_NoProfileUsesDefaults(t *testing.T) {
	yamlConfig := `
agent:
  name: legacy-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	// No memory_compression specified, should be nil
	assert.Nil(t, config.Memory.MemoryCompression)

	// Create agent without compression profile (backwards compatible)
	backend := &mockBackend{}
	llmProvider := &simpleLLM{}
	agent := NewAgent(backend, llmProvider)

	session := agent.memory.GetOrCreateSession("test-session")
	segMem := session.SegmentedMem.(*SegmentedMemory)

	// Should use balanced profile defaults (backwards compatibility)
	assert.Equal(t, 8, segMem.maxL1Messages, "Should default to balanced maxL1Messages")
	assert.Equal(t, 4, segMem.minL1Messages, "Should default to balanced minL1Messages")
}

// Helper function to create test agent with compression profile
// Uses existing mockBackend and simpleLLM from other test files
func createTestAgentWithProfile(profile CompressionProfile) *Agent {
	backend := &mockBackend{}
	llmProvider := &simpleLLM{}
	return NewAgent(backend, llmProvider, WithCompressionProfile(&profile))
}
