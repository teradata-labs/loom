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

func TestLoadConfigFromString_MemoryCompression_DataIntensive(t *testing.T) {
	yamlConfig := `
agent:
  name: test-agent
  description: Test agent with data_intensive compression
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: sqlite
    path: /tmp/test.db
    memory_compression:
      workload_profile: data_intensive
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	assert.Equal(t, "test-agent", config.Name)
	assert.NotNil(t, config.Memory)
	assert.NotNil(t, config.Memory.MemoryCompression)
	assert.Equal(t, loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE, config.Memory.MemoryCompression.WorkloadProfile)
}

func TestLoadConfigFromString_MemoryCompression_Conversational(t *testing.T) {
	yamlConfig := `
agent:
  name: test-agent
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

	assert.NotNil(t, config.Memory.MemoryCompression)
	assert.Equal(t, loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL, config.Memory.MemoryCompression.WorkloadProfile)
}

func TestLoadConfigFromString_MemoryCompression_Balanced(t *testing.T) {
	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    memory_compression:
      workload_profile: balanced
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	assert.NotNil(t, config.Memory.MemoryCompression)
	assert.Equal(t, loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED, config.Memory.MemoryCompression.WorkloadProfile)
}

func TestLoadConfigFromString_MemoryCompression_CustomValues(t *testing.T) {
	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    memory_compression:
      workload_profile: balanced
      max_l1_messages: 15
      min_l1_messages: 7
      warning_threshold_percent: 55
      critical_threshold_percent: 80
      batch_sizes:
        normal: 2
        warning: 4
        critical: 6
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	assert.NotNil(t, config.Memory.MemoryCompression)
	assert.Equal(t, loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED, config.Memory.MemoryCompression.WorkloadProfile)
	assert.Equal(t, int32(15), config.Memory.MemoryCompression.MaxL1Messages)
	assert.Equal(t, int32(7), config.Memory.MemoryCompression.MinL1Messages)
	assert.Equal(t, int32(55), config.Memory.MemoryCompression.WarningThresholdPercent)
	assert.Equal(t, int32(80), config.Memory.MemoryCompression.CriticalThresholdPercent)

	assert.NotNil(t, config.Memory.MemoryCompression.BatchSizes)
	assert.Equal(t, int32(2), config.Memory.MemoryCompression.BatchSizes.Normal)
	assert.Equal(t, int32(4), config.Memory.MemoryCompression.BatchSizes.Warning)
	assert.Equal(t, int32(6), config.Memory.MemoryCompression.BatchSizes.Critical)
}

func TestLoadConfigFromString_MemoryCompression_NoProfile(t *testing.T) {
	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	// Memory compression config should be nil when not specified
	assert.Nil(t, config.Memory.MemoryCompression)
}

func TestLoadConfigFromString_MemoryCompression_UnknownProfile(t *testing.T) {
	yamlConfig := `
agent:
  name: test-agent
  llm:
    provider: anthropic
    model: claude-sonnet-4-5-20250929
  memory:
    type: memory
    memory_compression:
      workload_profile: unknown_profile
`

	config, err := LoadConfigFromString(yamlConfig)
	require.NoError(t, err)

	// Unknown profiles should default to unspecified (which resolves to balanced)
	assert.NotNil(t, config.Memory.MemoryCompression)
	assert.Equal(t, loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED, config.Memory.MemoryCompression.WorkloadProfile)
}

func TestParseMemoryCompressionConfig_ProfileEnumMapping(t *testing.T) {
	tests := []struct {
		name            string
		profileString   string
		expectedProfile loomv1.WorkloadProfile
	}{
		{
			name:            "data_intensive",
			profileString:   "data_intensive",
			expectedProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE,
		},
		{
			name:            "conversational",
			profileString:   "conversational",
			expectedProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL,
		},
		{
			name:            "balanced",
			profileString:   "balanced",
			expectedProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
		},
		{
			name:            "empty defaults to unspecified",
			profileString:   "",
			expectedProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED,
		},
		{
			name:            "unknown defaults to unspecified",
			profileString:   "invalid",
			expectedProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yamlConfig := &MemoryCompressionConfigYAML{
				WorkloadProfile: tt.profileString,
			}

			protoConfig := parseMemoryCompressionConfig(yamlConfig)
			require.NotNil(t, protoConfig)
			assert.Equal(t, tt.expectedProfile, protoConfig.WorkloadProfile)
		})
	}
}
