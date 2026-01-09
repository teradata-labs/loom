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

func TestProfileDefaults(t *testing.T) {
	tests := []struct {
		name                  string
		profile               loomv1.WorkloadProfile
		expectedMaxL1         int
		expectedMinL1         int
		expectedWarning       int
		expectedCritical      int
		expectedNormalBatch   int
		expectedWarningBatch  int
		expectedCriticalBatch int
	}{
		{
			name:                  "balanced profile",
			profile:               loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
			expectedMaxL1:         8,
			expectedMinL1:         4,
			expectedWarning:       60,
			expectedCritical:      75,
			expectedNormalBatch:   3,
			expectedWarningBatch:  5,
			expectedCriticalBatch: 7,
		},
		{
			name:                  "data_intensive profile",
			profile:               loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE,
			expectedMaxL1:         5,
			expectedMinL1:         3,
			expectedWarning:       50,
			expectedCritical:      70,
			expectedNormalBatch:   2,
			expectedWarningBatch:  4,
			expectedCriticalBatch: 6,
		},
		{
			name:                  "conversational profile",
			profile:               loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL,
			expectedMaxL1:         12,
			expectedMinL1:         6,
			expectedWarning:       70,
			expectedCritical:      85,
			expectedNormalBatch:   4,
			expectedWarningBatch:  6,
			expectedCriticalBatch: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, exists := ProfileDefaults[tt.profile]
			require.True(t, exists, "Profile should exist in defaults")

			assert.Equal(t, tt.expectedMaxL1, profile.MaxL1Messages)
			assert.Equal(t, tt.expectedMinL1, profile.MinL1Messages)
			assert.Equal(t, tt.expectedWarning, profile.WarningThresholdPercent)
			assert.Equal(t, tt.expectedCritical, profile.CriticalThresholdPercent)
			assert.Equal(t, tt.expectedNormalBatch, profile.NormalBatchSize)
			assert.Equal(t, tt.expectedWarningBatch, profile.WarningBatchSize)
			assert.Equal(t, tt.expectedCriticalBatch, profile.CriticalBatchSize)

			// Validate that defaults pass validation
			err := profile.Validate()
			assert.NoError(t, err, "Default profile should be valid")
		})
	}
}

func TestResolveCompressionProfile_NilConfig(t *testing.T) {
	profile, err := ResolveCompressionProfile(nil)
	require.NoError(t, err)

	// Should return balanced profile as default
	expected := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]
	assert.Equal(t, expected, profile)
}

func TestResolveCompressionProfile_ProfileOnly(t *testing.T) {
	tests := []struct {
		name            string
		workloadProfile loomv1.WorkloadProfile
		expectedName    string
	}{
		{
			name:            "data_intensive",
			workloadProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE,
			expectedName:    "data_intensive",
		},
		{
			name:            "conversational",
			workloadProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL,
			expectedName:    "conversational",
		},
		{
			name:            "balanced",
			workloadProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
			expectedName:    "balanced",
		},
		{
			name:            "unspecified defaults to balanced",
			workloadProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED,
			expectedName:    "balanced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &loomv1.MemoryCompressionConfig{
				WorkloadProfile: tt.workloadProfile,
			}

			profile, err := ResolveCompressionProfile(config)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedName, profile.Name)
		})
	}
}

func TestResolveCompressionProfile_Overrides(t *testing.T) {
	// Start with data_intensive profile, override maxL1Messages
	config := &loomv1.MemoryCompressionConfig{
		WorkloadProfile: loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE,
		MaxL1Messages:   10, // Override default 5
	}

	profile, err := ResolveCompressionProfile(config)
	require.NoError(t, err)

	assert.Equal(t, 10, profile.MaxL1Messages, "Should use overridden value")
	assert.Equal(t, 3, profile.MinL1Messages, "Should use profile default")
	assert.Equal(t, 50, profile.WarningThresholdPercent, "Should use profile default")
}

func TestResolveCompressionProfile_FullCustomization(t *testing.T) {
	config := &loomv1.MemoryCompressionConfig{
		WorkloadProfile:          loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
		MaxL1Messages:            15,
		MinL1Messages:            7,
		WarningThresholdPercent:  55,
		CriticalThresholdPercent: 80,
		BatchSizes: &loomv1.MemoryCompressionBatchSizes{
			Normal:   2,
			Warning:  4,
			Critical: 6,
		},
	}

	profile, err := ResolveCompressionProfile(config)
	require.NoError(t, err)

	assert.Equal(t, 15, profile.MaxL1Messages)
	assert.Equal(t, 7, profile.MinL1Messages)
	assert.Equal(t, 55, profile.WarningThresholdPercent)
	assert.Equal(t, 80, profile.CriticalThresholdPercent)
	assert.Equal(t, 2, profile.NormalBatchSize)
	assert.Equal(t, 4, profile.WarningBatchSize)
	assert.Equal(t, 6, profile.CriticalBatchSize)
}

func TestCompressionProfile_Validate(t *testing.T) {
	tests := []struct {
		name        string
		profile     CompressionProfile
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid profile",
			profile: CompressionProfile{
				MaxL1Messages:            8,
				MinL1Messages:            4,
				WarningThresholdPercent:  60,
				CriticalThresholdPercent: 75,
				NormalBatchSize:          3,
				WarningBatchSize:         5,
				CriticalBatchSize:        7,
			},
			expectError: false,
		},
		{
			name: "max_l1_messages zero",
			profile: CompressionProfile{
				MaxL1Messages:            0,
				MinL1Messages:            4,
				WarningThresholdPercent:  60,
				CriticalThresholdPercent: 75,
				NormalBatchSize:          3,
				WarningBatchSize:         5,
				CriticalBatchSize:        7,
			},
			expectError: true,
			errorMsg:    "max_l1_messages must be positive",
		},
		{
			name: "max_l1_messages too large",
			profile: CompressionProfile{
				MaxL1Messages:            100,
				MinL1Messages:            4,
				WarningThresholdPercent:  60,
				CriticalThresholdPercent: 75,
				NormalBatchSize:          3,
				WarningBatchSize:         5,
				CriticalBatchSize:        7,
			},
			expectError: true,
			errorMsg:    "max_l1_messages too large",
		},
		{
			name: "min_l1_messages >= max_l1_messages",
			profile: CompressionProfile{
				MaxL1Messages:            8,
				MinL1Messages:            8,
				WarningThresholdPercent:  60,
				CriticalThresholdPercent: 75,
				NormalBatchSize:          3,
				WarningBatchSize:         5,
				CriticalBatchSize:        7,
			},
			expectError: true,
			errorMsg:    "min_l1_messages (8) must be less than max_l1_messages (8)",
		},
		{
			name: "warning_threshold out of range",
			profile: CompressionProfile{
				MaxL1Messages:            8,
				MinL1Messages:            4,
				WarningThresholdPercent:  150,
				CriticalThresholdPercent: 75,
				NormalBatchSize:          3,
				WarningBatchSize:         5,
				CriticalBatchSize:        7,
			},
			expectError: true,
			errorMsg:    "warning_threshold_percent must be 0-100",
		},
		{
			name: "critical_threshold <= warning_threshold",
			profile: CompressionProfile{
				MaxL1Messages:            8,
				MinL1Messages:            4,
				WarningThresholdPercent:  70,
				CriticalThresholdPercent: 60,
				NormalBatchSize:          3,
				WarningBatchSize:         5,
				CriticalBatchSize:        7,
			},
			expectError: true,
			errorMsg:    "critical_threshold_percent (60) must be greater than warning_threshold_percent (70)",
		},
		{
			name: "warning_batch_size < normal_batch_size",
			profile: CompressionProfile{
				MaxL1Messages:            8,
				MinL1Messages:            4,
				WarningThresholdPercent:  60,
				CriticalThresholdPercent: 75,
				NormalBatchSize:          5,
				WarningBatchSize:         3,
				CriticalBatchSize:        7,
			},
			expectError: true,
			errorMsg:    "warning_batch_size (3) should be >= normal_batch_size (5)",
		},
		{
			name: "critical_batch_size too large",
			profile: CompressionProfile{
				MaxL1Messages:            8,
				MinL1Messages:            4,
				WarningThresholdPercent:  60,
				CriticalThresholdPercent: 75,
				NormalBatchSize:          3,
				WarningBatchSize:         5,
				CriticalBatchSize:        25,
			},
			expectError: true,
			errorMsg:    "critical_batch_size too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.profile.Validate()
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResolveCompressionProfile_InvalidProfileReturnsError(t *testing.T) {
	// Create a config that will result in an invalid profile
	config := &loomv1.MemoryCompressionConfig{
		WorkloadProfile:          loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
		MaxL1Messages:            2,  // Too small
		MinL1Messages:            10, // Larger than max
		WarningThresholdPercent:  60,
		CriticalThresholdPercent: 75,
	}

	_, err := ResolveCompressionProfile(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid compression profile")
}
