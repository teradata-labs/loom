// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"fmt"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// CompressionProfile defines memory compression behavior for a specific workload type.
// Profiles provide preset values for thresholds, batch sizes, and L1 cache limits.
type CompressionProfile struct {
	// Profile name (for logging and debugging)
	Name string

	// Maximum messages in L1 cache before compression triggers
	MaxL1Messages int

	// Minimum messages to keep in L1 after compression
	MinL1Messages int

	// Warning threshold as percentage (0-100)
	// Compression triggers when token usage exceeds this
	WarningThresholdPercent int

	// Critical threshold as percentage (0-100)
	// Aggressive compression when token usage exceeds this
	CriticalThresholdPercent int

	// Number of messages to compress in normal conditions
	NormalBatchSize int

	// Number of messages to compress under warning threshold
	WarningBatchSize int

	// Number of messages to compress under critical threshold
	CriticalBatchSize int
}

// ProfileDefaults provides preset profiles for common workload types.
var ProfileDefaults = map[loomv1.WorkloadProfile]CompressionProfile{
	loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED: {
		Name:                     "balanced",
		MaxL1Messages:            8,
		MinL1Messages:            4,
		WarningThresholdPercent:  60,
		CriticalThresholdPercent: 75,
		NormalBatchSize:          3,
		WarningBatchSize:         5,
		CriticalBatchSize:        7,
	},
	loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE: {
		Name:                     "data_intensive",
		MaxL1Messages:            5,
		MinL1Messages:            3,
		WarningThresholdPercent:  50,
		CriticalThresholdPercent: 70,
		NormalBatchSize:          2,
		WarningBatchSize:         4,
		CriticalBatchSize:        6,
	},
	loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL: {
		Name:                     "conversational",
		MaxL1Messages:            12,
		MinL1Messages:            6,
		WarningThresholdPercent:  70,
		CriticalThresholdPercent: 85,
		NormalBatchSize:          4,
		WarningBatchSize:         6,
		CriticalBatchSize:        8,
	},
}

// ResolveCompressionProfile resolves a compression configuration into a final profile.
// Precedence: Explicit config values > Profile defaults > Balanced profile defaults
func ResolveCompressionProfile(config *loomv1.MemoryCompressionConfig) (CompressionProfile, error) {
	// Start with balanced profile as base
	profile := ProfileDefaults[loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED]

	// If config is nil, return balanced profile
	if config == nil {
		return profile, nil
	}

	// Apply profile defaults if specified
	if config.WorkloadProfile != loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED {
		if defaultProfile, exists := ProfileDefaults[config.WorkloadProfile]; exists {
			profile = defaultProfile
		} else {
			return profile, fmt.Errorf("unknown workload profile: %v", config.WorkloadProfile)
		}
	}

	// Override with explicit config values (if non-zero)
	if config.MaxL1Messages > 0 {
		profile.MaxL1Messages = int(config.MaxL1Messages)
	}
	if config.MinL1Messages > 0 {
		profile.MinL1Messages = int(config.MinL1Messages)
	}
	if config.WarningThresholdPercent > 0 {
		profile.WarningThresholdPercent = int(config.WarningThresholdPercent)
	}
	if config.CriticalThresholdPercent > 0 {
		profile.CriticalThresholdPercent = int(config.CriticalThresholdPercent)
	}

	// Override batch sizes if specified
	if config.BatchSizes != nil {
		if config.BatchSizes.Normal > 0 {
			profile.NormalBatchSize = int(config.BatchSizes.Normal)
		}
		if config.BatchSizes.Warning > 0 {
			profile.WarningBatchSize = int(config.BatchSizes.Warning)
		}
		if config.BatchSizes.Critical > 0 {
			profile.CriticalBatchSize = int(config.BatchSizes.Critical)
		}
	}

	// Validate final profile
	if err := profile.Validate(); err != nil {
		return profile, fmt.Errorf("invalid compression profile: %w", err)
	}

	return profile, nil
}

// Validate checks if the profile has valid values.
func (p CompressionProfile) Validate() error {
	// MaxL1Messages must be positive and reasonable
	if p.MaxL1Messages <= 0 {
		return fmt.Errorf("max_l1_messages must be positive, got %d", p.MaxL1Messages)
	}
	if p.MaxL1Messages > 50 {
		return fmt.Errorf("max_l1_messages too large (>50), got %d", p.MaxL1Messages)
	}

	// MinL1Messages must be positive and less than max
	if p.MinL1Messages <= 0 {
		return fmt.Errorf("min_l1_messages must be positive, got %d", p.MinL1Messages)
	}
	if p.MinL1Messages >= p.MaxL1Messages {
		return fmt.Errorf("min_l1_messages (%d) must be less than max_l1_messages (%d)", p.MinL1Messages, p.MaxL1Messages)
	}

	// Thresholds must be in valid range (0-100)
	if p.WarningThresholdPercent < 0 || p.WarningThresholdPercent > 100 {
		return fmt.Errorf("warning_threshold_percent must be 0-100, got %d", p.WarningThresholdPercent)
	}
	if p.CriticalThresholdPercent < 0 || p.CriticalThresholdPercent > 100 {
		return fmt.Errorf("critical_threshold_percent must be 0-100, got %d", p.CriticalThresholdPercent)
	}

	// Critical must be higher than warning
	if p.CriticalThresholdPercent <= p.WarningThresholdPercent {
		return fmt.Errorf("critical_threshold_percent (%d) must be greater than warning_threshold_percent (%d)",
			p.CriticalThresholdPercent, p.WarningThresholdPercent)
	}

	// Batch sizes must be positive and reasonable
	if p.NormalBatchSize <= 0 {
		return fmt.Errorf("normal_batch_size must be positive, got %d", p.NormalBatchSize)
	}
	if p.WarningBatchSize <= 0 {
		return fmt.Errorf("warning_batch_size must be positive, got %d", p.WarningBatchSize)
	}
	if p.CriticalBatchSize <= 0 {
		return fmt.Errorf("critical_batch_size must be positive, got %d", p.CriticalBatchSize)
	}

	// Batch sizes should increase with severity
	if p.WarningBatchSize < p.NormalBatchSize {
		return fmt.Errorf("warning_batch_size (%d) should be >= normal_batch_size (%d)",
			p.WarningBatchSize, p.NormalBatchSize)
	}
	if p.CriticalBatchSize < p.WarningBatchSize {
		return fmt.Errorf("critical_batch_size (%d) should be >= warning_batch_size (%d)",
			p.CriticalBatchSize, p.WarningBatchSize)
	}

	// Batch sizes shouldn't exceed reasonable limits
	if p.CriticalBatchSize > 20 {
		return fmt.Errorf("critical_batch_size too large (>20), got %d", p.CriticalBatchSize)
	}

	return nil
}
