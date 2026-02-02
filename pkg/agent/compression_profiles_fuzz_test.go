// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agent

import (
	"testing"

	loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"
)

// FuzzCompressionProfile tests compression profile validation with random configuration values.
// Properties tested:
// - Validate() correctly rejects invalid configurations
// - Valid configurations never cause panics
// - Threshold ordering is enforced (critical > warning)
// - Batch size ordering is enforced (critical >= warning >= normal)
// - All bounds are checked (token limits, message limits, percentages)
func FuzzCompressionProfile(f *testing.F) {
	// Seed with valid configurations
	f.Add(int32(6400), int32(4), int32(60), int32(75), int32(3), int32(5), int32(7))
	f.Add(int32(9600), int32(6), int32(70), int32(85), int32(4), int32(6), int32(8))
	f.Add(int32(1000), int32(2), int32(50), int32(80), int32(2), int32(3), int32(5))

	// Seed with some invalid configurations
	f.Add(int32(-100), int32(4), int32(60), int32(75), int32(3), int32(5), int32(7))   // Negative max tokens
	f.Add(int32(6400), int32(0), int32(60), int32(75), int32(3), int32(5), int32(7))   // Zero min messages
	f.Add(int32(6400), int32(4), int32(90), int32(75), int32(3), int32(5), int32(7))   // Warning > critical
	f.Add(int32(6400), int32(4), int32(60), int32(75), int32(10), int32(5), int32(7))  // Normal > warning batch
	f.Add(int32(300000), int32(4), int32(60), int32(75), int32(3), int32(5), int32(7)) // Too large max tokens

	f.Fuzz(func(t *testing.T, maxL1Tokens, minL1Messages, warningPct, criticalPct, normalBatch, warningBatch, criticalBatch int32) {
		profile := CompressionProfile{
			Name:                     "fuzz_test",
			MaxL1Tokens:              int(maxL1Tokens),
			MinL1Messages:            int(minL1Messages),
			WarningThresholdPercent:  int(warningPct),
			CriticalThresholdPercent: int(criticalPct),
			NormalBatchSize:          int(normalBatch),
			WarningBatchSize:         int(warningBatch),
			CriticalBatchSize:        int(criticalBatch),
		}

		// Should never panic during validation
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Validate() panicked: %v", r)
				}
			}()
			err = profile.Validate()
		}()

		// Check validation logic consistency
		// If profile passes validation, it should satisfy all invariants
		if err == nil {
			// Property 1: MaxL1Tokens must be positive and reasonable
			if profile.MaxL1Tokens <= 0 {
				t.Errorf("validation passed but MaxL1Tokens <= 0: %d", profile.MaxL1Tokens)
			}
			if profile.MaxL1Tokens > 200000 {
				t.Errorf("validation passed but MaxL1Tokens > 200000: %d", profile.MaxL1Tokens)
			}

			// Property 2: MinL1Messages must be positive and reasonable
			if profile.MinL1Messages <= 0 {
				t.Errorf("validation passed but MinL1Messages <= 0: %d", profile.MinL1Messages)
			}
			if profile.MinL1Messages > 20 {
				t.Errorf("validation passed but MinL1Messages > 20: %d", profile.MinL1Messages)
			}

			// Property 3: Threshold percentages must be in valid range
			if profile.WarningThresholdPercent < 0 || profile.WarningThresholdPercent > 100 {
				t.Errorf("validation passed but WarningThresholdPercent out of range: %d", profile.WarningThresholdPercent)
			}
			if profile.CriticalThresholdPercent < 0 || profile.CriticalThresholdPercent > 100 {
				t.Errorf("validation passed but CriticalThresholdPercent out of range: %d", profile.CriticalThresholdPercent)
			}

			// Property 4: Critical threshold must be greater than warning
			if profile.CriticalThresholdPercent <= profile.WarningThresholdPercent {
				t.Errorf("validation passed but critical (%d) <= warning (%d)",
					profile.CriticalThresholdPercent, profile.WarningThresholdPercent)
			}

			// Property 5: Batch sizes must be positive and reasonable
			if profile.NormalBatchSize <= 0 {
				t.Errorf("validation passed but NormalBatchSize <= 0: %d", profile.NormalBatchSize)
			}
			if profile.WarningBatchSize <= 0 {
				t.Errorf("validation passed but WarningBatchSize <= 0: %d", profile.WarningBatchSize)
			}
			if profile.CriticalBatchSize <= 0 {
				t.Errorf("validation passed but CriticalBatchSize <= 0: %d", profile.CriticalBatchSize)
			}

			// Property 6: Batch sizes should increase with severity
			if profile.WarningBatchSize < profile.NormalBatchSize {
				t.Errorf("validation passed but warning batch (%d) < normal batch (%d)",
					profile.WarningBatchSize, profile.NormalBatchSize)
			}
			if profile.CriticalBatchSize < profile.WarningBatchSize {
				t.Errorf("validation passed but critical batch (%d) < warning batch (%d)",
					profile.CriticalBatchSize, profile.WarningBatchSize)
			}

			// Property 7: Critical batch size shouldn't exceed limit
			if profile.CriticalBatchSize > 20 {
				t.Errorf("validation passed but CriticalBatchSize > 20: %d", profile.CriticalBatchSize)
			}
		} else {
			// If validation failed, at least one invariant should be violated
			// This is a sanity check that validation is actually working

			// Check if the error is justified by checking if ANY invariant is violated
			hasViolation := false

			if profile.MaxL1Tokens <= 0 || profile.MaxL1Tokens > 200000 {
				hasViolation = true
			}
			if profile.MinL1Messages <= 0 || profile.MinL1Messages > 20 {
				hasViolation = true
			}
			if profile.WarningThresholdPercent < 0 || profile.WarningThresholdPercent > 100 {
				hasViolation = true
			}
			if profile.CriticalThresholdPercent < 0 || profile.CriticalThresholdPercent > 100 {
				hasViolation = true
			}
			if profile.CriticalThresholdPercent <= profile.WarningThresholdPercent {
				hasViolation = true
			}
			if profile.NormalBatchSize <= 0 || profile.WarningBatchSize <= 0 || profile.CriticalBatchSize <= 0 {
				hasViolation = true
			}
			if profile.WarningBatchSize < profile.NormalBatchSize {
				hasViolation = true
			}
			if profile.CriticalBatchSize < profile.WarningBatchSize {
				hasViolation = true
			}
			if profile.CriticalBatchSize > 20 {
				hasViolation = true
			}

			if !hasViolation {
				t.Errorf("validation failed but no invariant violation detected: %v", err)
			}
		}
	})
}

// FuzzResolveCompressionProfile tests the profile resolution logic with random configs.
func FuzzResolveCompressionProfile(f *testing.F) {
	// Seed with various profile types
	f.Add(int32(loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED), int32(8), int32(4))
	f.Add(int32(loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE), int32(5), int32(3))
	f.Add(int32(loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL), int32(12), int32(6))
	f.Add(int32(loomv1.WorkloadProfile_WORKLOAD_PROFILE_UNSPECIFIED), int32(0), int32(0))

	f.Fuzz(func(t *testing.T, profileType, maxL1Messages, minL1Messages int32) {
		// Build a config
		config := &loomv1.MemoryCompressionConfig{
			WorkloadProfile: loomv1.WorkloadProfile(profileType),
			MaxL1Messages:   maxL1Messages,
			MinL1Messages:   minL1Messages,
		}

		// Should not panic
		var profile CompressionProfile
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ResolveCompressionProfile panicked: %v", r)
				}
			}()
			profile, err = ResolveCompressionProfile(config)
		}()

		// If resolution succeeded, validate should pass
		if err == nil {
			validateErr := profile.Validate()
			if validateErr != nil {
				t.Errorf("ResolveCompressionProfile returned valid profile but Validate failed: %v", validateErr)
			}
		}

		// Property: If maxL1Messages is set, it should be converted to tokens (800 per message)
		if err == nil && maxL1Messages > 0 {
			expectedTokens := int(maxL1Messages) * 800
			if profile.MaxL1Tokens != expectedTokens {
				t.Errorf("MaxL1Tokens conversion incorrect: got %d, expected %d (maxL1Messages=%d)",
					profile.MaxL1Tokens, expectedTokens, maxL1Messages)
			}
		}

		// Property: If minL1Messages is set, it should be used
		if err == nil && minL1Messages > 0 {
			if profile.MinL1Messages != int(minL1Messages) {
				t.Errorf("MinL1Messages not applied: got %d, expected %d",
					profile.MinL1Messages, minL1Messages)
			}
		}
	})
}

// FuzzCompressionProfileDefaults tests that all predefined profiles are valid.
func FuzzCompressionProfileDefaults(f *testing.F) {
	// Test all default profiles
	profiles := []loomv1.WorkloadProfile{
		loomv1.WorkloadProfile_WORKLOAD_PROFILE_BALANCED,
		loomv1.WorkloadProfile_WORKLOAD_PROFILE_DATA_INTENSIVE,
		loomv1.WorkloadProfile_WORKLOAD_PROFILE_CONVERSATIONAL,
	}

	f.Add(int32(0))

	f.Fuzz(func(t *testing.T, seed int32) {
		// Use seed to select a profile
		profileIdx := int(seed) % len(profiles)
		if profileIdx < 0 {
			profileIdx = -profileIdx
		}

		profileType := profiles[profileIdx]
		profile, exists := ProfileDefaults[profileType]
		if !exists {
			t.Fatalf("default profile not found: %v", profileType)
		}

		// All default profiles must be valid
		err := profile.Validate()
		if err != nil {
			t.Errorf("default profile %v failed validation: %v", profileType, err)
		}

		// Sanity check: default profiles should have reasonable values
		if profile.MaxL1Tokens < 1000 || profile.MaxL1Tokens > 20000 {
			t.Errorf("default profile %v has unreasonable MaxL1Tokens: %d", profileType, profile.MaxL1Tokens)
		}
	})
}
